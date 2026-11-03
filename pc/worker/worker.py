import array
import base64
import io
import json
import os
import re
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.request
import wave

from fastapi import FastAPI, HTTPException
from pydantic import BaseModel

QWEN3_URL = os.environ.get("QWEN3_ASR_URL", "http://127.0.0.1:18091").rstrip("/")
VOICE_SERVICE_URL = os.environ.get("VOICE_SERVICE_URL", "").rstrip("/")
ASR_ROOT = os.environ.get("ASR_ROOT", "")
PCM_RATE = 16000
PCM_BYTES_PER_SECOND = PCM_RATE * 2
# Bound both audio context and output tokens, including merged recordings.
ASR_CHUNK_SECONDS = 30

app = FastAPI(title="warehouse-audio-worker")
_infer_lock = threading.Lock()
_ready = threading.Event()
_warm_error = ""




def _warmup():
    global _warm_error
    try:
        with urllib.request.urlopen(QWEN3_URL + "/health", timeout=10) as r:
            if r.status != 200:
                raise RuntimeError("qwen3-asr health status %s" % r.status)
        _ready.set()
    except Exception as e:  # noqa: BLE001
        _warm_error = str(e)


threading.Thread(target=_warmup, daemon=True, name="asr-warmup").start()


class TranscribeRequest(BaseModel):
    audio_path: str


class EnrollSegment(BaseModel):
    start: float
    end: float


class EnrollRequest(BaseModel):
    audio_path: str
    name: str
    segments: list[EnrollSegment]


def _decode_to_pcm(path):
    """用 ffmpeg 解成 16k 单声道 s16le，兼容 aac/m4a/mp3/wav。"""
    cmd = [
        "ffmpeg", "-v", "error", "-i", path,
        "-ar", "16000", "-ac", "1", "-f", "s16le", "-",
    ]
    p = subprocess.run(cmd, capture_output=True)
    if p.returncode != 0 or not p.stdout:
        msg = p.stderr.decode("utf-8", "replace")[-500:]
        raise HTTPException(status_code=400, detail="音频解码失败：" + msg)
    return p.stdout


def _wav_bytes(pcm_bytes):
    buf = io.BytesIO()
    with wave.open(buf, "wb") as w:
        w.setnchannels(1)
        w.setsampwidth(2)
        w.setframerate(PCM_RATE)
        w.writeframes(pcm_bytes)
    return buf.getvalue()


def _quiet_boundary(pcm, target, search_seconds=5):
    """Prefer a quiet 100 ms window before a cut; never discard audio."""
    window = PCM_RATE // 10
    end = target // 2
    start = max(0, end - int(search_seconds * PCM_RATE))
    samples = array.array("h", pcm[start * 2:end * 2])
    if sys.byteorder != "little":
        samples.byteswap()
    # Search backwards so equal-energy windows favor longer chunks.
    best, best_energy = end, None
    for pos in range(len(samples) - window, -1, -window):
        energy = sum(x * x for x in samples[pos:pos + window])
        if best_energy is None or energy < best_energy:
            best, best_energy = start + pos + window // 2, energy
    return best * 2


class _TruncatedTranscript(Exception):
    pass


def _transcribe_pcm(pcm):
    body = {
        "model": "qwen3-asr",
        "messages": [{
            "role": "user",
            "content": [
                {"type": "text", "text": "Transcribe the audio."},
                {"type": "input_audio", "input_audio": {
                    "data": base64.b64encode(_wav_bytes(pcm)).decode(), "format": "wav",
                }},
            ],
        }],
        "temperature": 0,
        "max_tokens": 1024,
        "repeat_penalty": 1.05,
        "top_k": 100,
        "top_p": 0.8,
    }
    req = urllib.request.Request(
        QWEN3_URL + "/v1/chat/completions",
        data=json.dumps(body).encode(),
        headers={"Content-Type": "application/json"},
    )
    try:
        with urllib.request.urlopen(req, timeout=900) as r:
            resp = json.load(r)
    except urllib.error.HTTPError as e:
        msg = e.read().decode("utf-8", "replace")[:500]
        raise HTTPException(status_code=502, detail="qwen3-asr: " + msg)
    except Exception as e:  # noqa: BLE001
        raise HTTPException(status_code=502, detail="qwen3-asr: " + str(e))

    choices = resp.get("choices")
    if not choices or not isinstance(choices[0].get("message", {}).get("content"), str):
        raise HTTPException(status_code=502, detail="qwen3-asr: invalid response")
    choice = choices[0]
    if choice.get("finish_reason") == "length":
        raise _TruncatedTranscript()
    content = choice["message"]["content"]
    language = "zh"
    m = re.search(r"language\s+([^<\s]+)<asr_text>", content)
    if m:
        language = m.group(1)
    text = content.split("<asr_text>", 1)[1] if "<asr_text>" in content else content
    text = re.sub(r"<\|[^>]+\|>", "", text).strip()
    return text, language


def _transcribe_span(pcm, offset):
    # Only skip exact digital silence. Quiet speech must still reach the model.
    if not any(pcm):
        return [], ""
    try:
        text, language = _transcribe_pcm(pcm)
    except _TruncatedTranscript:
        # A token limit must never turn a partial transcript into a success.
        if len(pcm) <= PCM_BYTES_PER_SECOND * 2:
            raise HTTPException(status_code=502, detail="qwen3-asr: 转写输出被截断")
        midpoint = (len(pcm) // 4) * 2
        left, left_lang = _transcribe_span(pcm[:midpoint], offset)
        right, right_lang = _transcribe_span(pcm[midpoint:], offset + midpoint)
        return left + right, left_lang or right_lang
    sentences = []
    if text:
        sentences.append({
            "spk": 0,
            "start": round(offset / PCM_BYTES_PER_SECOND, 3),
            "end": round((offset + len(pcm)) / PCM_BYTES_PER_SECOND, 3),
            "text": text,
        })
    return sentences, language


def transcribe_qwen3(path):
    pcm = _decode_to_pcm(path)
    duration = round(len(pcm) / PCM_BYTES_PER_SECOND, 3)
    chunk_bytes = ASR_CHUNK_SECONDS * PCM_BYTES_PER_SECOND
    sentences = []
    language = ""
    start = 0
    while start < len(pcm):
        end = min(start + chunk_bytes, len(pcm))
        if end < len(pcm):
            end = start + _quiet_boundary(pcm[start:end], chunk_bytes)
        parts, chunk_language = _transcribe_span(pcm[start:end], start)
        sentences.extend(parts)
        language = language or chunk_language
        start = end
    return "\n".join(s["text"] for s in sentences), duration, language or "zh", sentences


@app.get("/health")
def health():
    return {
        "status": "ok",
        "ready": _ready.is_set(),
        "model_loaded": _ready.is_set(),
        "warm_error": _warm_error,
        "backend": "qwen3",
        "model": "qwen3-asr-1.7b",
        "punc": True,
        "voiceprints": bool(VOICE_SERVICE_URL),
    }


@app.post("/v1/voices/enroll")
def enroll_voice(req: EnrollRequest):
    if not VOICE_SERVICE_URL:
        raise HTTPException(status_code=501, detail="voice service not configured")
    if not os.path.exists(req.audio_path):
        raise HTTPException(status_code=400, detail="audio not found")
    body = {
        "audio_path": req.audio_path,
        "name": req.name,
        "segments": [{"start": s.start, "end": s.end} for s in req.segments],
    }
    request = urllib.request.Request(
        VOICE_SERVICE_URL + "/v1/voices/enroll",
        data=json.dumps(body).encode(),
        headers={"Content-Type": "application/json"},
    )
    try:
        with urllib.request.urlopen(request, timeout=900) as r:
            return json.load(r)
    except urllib.error.HTTPError as e:
        msg = e.read().decode("utf-8", "replace")[:500]
        raise HTTPException(status_code=502, detail="voice service: " + msg)
    except Exception as e:  # noqa: BLE001
        raise HTTPException(status_code=502, detail="voice service: " + str(e))


@app.post("/v1/transcribe")
def transcribe(req: TranscribeRequest):
    if not os.path.exists(req.audio_path):
        raise HTTPException(status_code=400, detail="audio not found")
    if ASR_ROOT:
        root = os.path.realpath(ASR_ROOT)
        path = os.path.realpath(req.audio_path)
        if not path.startswith(root + os.sep):
            raise HTTPException(status_code=400, detail="audio path outside ASR_ROOT")

    t0 = time.time()
    with _infer_lock:
        text, duration, language, sentences = transcribe_qwen3(req.audio_path)
    return {
        "text": text,
        "sentences": sentences,
        "language": language,
        "duration": duration,
        "elapsed": round(time.time() - t0, 2),
    }


if __name__ == "__main__":
    import uvicorn

    uvicorn.run(app, host="127.0.0.1", port=int(os.environ.get("ASR_PORT", "18090")))
