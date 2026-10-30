package infer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWorkerASRPostsAudioPath(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]string
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode: %v", err)
		}
		got = req["audio_path"]
		_ = json.NewEncoder(w).Encode(map[string]any{
			"sentences": []map[string]any{{"spk": 0, "start": 0, "end": 1, "text": "测试"}},
			"duration":  1,
		})
	}))
	defer srv.Close()

	wa := NewWorkerASR(srv.URL)
	tr, err := wa.Transcribe(context.Background(), "/tmp/x.wav")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/tmp/x.wav" {
		t.Fatalf("audio_path = %q", got)
	}
	if len(tr.Segments) != 1 || tr.Segments[0].Text != "测试" {
		t.Fatalf("segments = %+v", tr.Segments)
	}
}
