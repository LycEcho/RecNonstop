"""Synthetic audio and mocked model replies; no model or recording required."""
import io
import json
import struct
import unittest
from unittest.mock import patch

# Importing the worker normally starts its backend health probe.
with patch("threading.Thread.start"):
    import worker


def pcm(seconds, amplitude=1000):
    return struct.pack("<h", amplitude) * int(seconds * worker.PCM_RATE)


class TranscriptionTests(unittest.TestCase):
    def test_long_audio_is_bounded_and_preserved(self):
        audio = pcm(26) + pcm(1, 0) + pcm(43, 2000)
        submitted = []

        def infer(chunk):
            submitted.append(chunk)
            return "测试片段", "Chinese"

        with patch.object(worker, "_decode_to_pcm", return_value=audio), \
                patch.object(worker, "_transcribe_pcm", side_effect=infer):
            text, duration, language, segments = worker.transcribe_qwen3("synthetic.wav")
        self.assertEqual(b"".join(submitted), audio)
        self.assertTrue(all(len(ch) <= 30 * 32000 for ch in submitted))
        self.assertEqual(duration, 70)
        self.assertEqual(language, "Chinese")
        self.assertEqual(text.count("测试片段"), len(submitted))
        self.assertEqual(segments[0]["start"], 0)
        self.assertEqual(segments[-1]["end"], 70)
        for previous, current in zip(segments, segments[1:]):
            self.assertEqual(previous["end"], current["start"])
        self.assertGreater(segments[0]["end"], 26)
        self.assertLess(segments[0]["end"], 27)

    def test_truncated_output_is_retried_in_smaller_spans(self):
        audio = pcm(8)
        with patch.object(worker, "_transcribe_pcm", side_effect=[
            worker._TruncatedTranscript(), ("前半句", "Chinese"), ("后半句", "Chinese"),
        ]) as infer:
            segments, language = worker._transcribe_span(audio, 32000)
        self.assertEqual(infer.call_count, 3)
        self.assertEqual(segments, [
            {"spk": 0, "start": 1, "end": 5, "text": "前半句"},
            {"spk": 0, "start": 5, "end": 9, "text": "后半句"},
        ])
        self.assertEqual(language, "Chinese")

    def test_persistently_truncated_output_is_an_error(self):
        with patch.object(worker, "_transcribe_pcm", side_effect=worker._TruncatedTranscript()):
            with self.assertRaises(worker.HTTPException) as err:
                worker._transcribe_span(pcm(2), 0)
        self.assertEqual(err.exception.status_code, 502)

    def test_digital_silence_skips_model_but_quiet_speech_does_not(self):
        with patch.object(worker, "_transcribe_pcm", return_value=("轻声测试", "Chinese")) as infer:
            self.assertEqual(worker._transcribe_span(pcm(1, 0), 0), ([], ""))
            self.assertFalse(infer.called)
            segments, _ = worker._transcribe_span(pcm(1, 1), 0)
            self.assertEqual(segments[0]["text"], "轻声测试")
            self.assertEqual(infer.call_count, 1)

    def test_model_reply_must_not_silently_truncate(self):
        reply = {"choices": [{"message": {"content": "不完整"}, "finish_reason": "length"}]}
        with patch.object(worker.urllib.request, "urlopen", return_value=io.StringIO(json.dumps(reply))):
            with self.assertRaises(worker._TruncatedTranscript):
                worker._transcribe_pcm(pcm(1))

    def test_missing_model_content_is_an_error(self):
        for reply in ({"choices": []}, {"choices": [{"message": {}}]}):
            with self.subTest(reply=reply), \
                    patch.object(worker.urllib.request, "urlopen", return_value=io.StringIO(json.dumps(reply))):
                with self.assertRaises(worker.HTTPException) as err:
                    worker._transcribe_pcm(pcm(1))
                self.assertEqual(err.exception.status_code, 502)


if __name__ == "__main__":
    unittest.main()
