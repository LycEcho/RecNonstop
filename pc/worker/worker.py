import base64
import json
import os
import re
import subprocess
import tempfile
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


def _write_temp_wav(pcm_bytes):
    f = tempfile.NamedTemporaryFile(suffix=".wav", delete=False)
    f.close()
    with wave.open(f.name, "wb") as w:
        w.setnchannels(1)
        w.setsampwidth(2)
        w.setframerate(16000)
        w.writeframes(pcm_bytes)
    return f.name


def transcribe_qwen3(path):
    pcm = _decode_to_pcm(path)
    duration = round(len(pcm) / 2 / 16000.0, 3)
    tmp = _write_temp_wav(pcm)
    try:
        with open(tmp, "rb") as f:
            b64 = base64.b64encode(f.read()).decode()
        body = {
            "model": "qwen3-asr",
            "messages": [{
                "role": "user",
                "content": [
                    {"type": "text", "text": "Transcribe the audio."},
                    {"type": "input_audio", "input_audio": {"data": b64, "format": "wav"}},
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

        content = (resp.get("choices") or [{}])[0].get("message", {}).get("content", "") or ""
        language = "zh"
        m = re.search(r"language\s+([^<\s]+)<asr_text>", content)
        if m:
            language = m.group(1)
        text = content.split("<asr_text>", 1)[1] if "<asr_text>" in content else content
        text = re.sub(r"<\|[^>]+\|>", "", text).strip()
        return text, duration, language
    finally:
        try:
            os.unlink(tmp)
        except OSError:
            pass


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
        text, duration, language = transcribe_qwen3(req.audio_path)

    sentences = []
    if text:
        sentences.append({
            "spk": 0,
            "start": 0.0,
            "end": duration,
            "text": text,
        })
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
