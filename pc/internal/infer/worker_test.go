package infer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"syscall"
	"testing"
)

type audioEventsTransport func(*http.Request) (*http.Response, error)

func (f audioEventsTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestWorkerPreservesOptionalAudioEventsWithoutLosingTranscript(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		want      []AudioEvent
	}{
		{name: "old_worker_without_events"},
		{name: "null_events", raw: `null`},
		{name: "empty_events", raw: `[]`},
		{name: "valid_scores_and_empty_window", raw: `[
			{"start":0,"end":10,"tags":[{"label":"Speech","score":0.94},{"label":"Music","score":0.119}]},
			{"start":10,"end":20,"tags":[]}
		]`, want: []AudioEvent{
			{Start: 0, End: 10, Tags: []AudioEventTag{{Label: "Speech", Score: 0.94}, {Label: "Music", Score: 0.119}}},
			{Start: 10, End: 20, Tags: []AudioEventTag{}},
		}},
		{name: "invalid_container", raw: `{"label":"Speech","score":0.8}`},
		{name: "invalid_windows", raw: `[
			{"start":-1,"end":2,"tags":[]},
			{"start":2,"end":2,"tags":[]},
			{"start":0,"end":21,"tags":[]},
			{"start":null,"end":2,"tags":[]},
			{"end":2,"tags":[]},
			{"start":0,"end":2,"tags":null},
			{"start":0,"end":2},
			{"start":0,"end":2,"tags":{}},
			{"start":1e309,"end":2,"tags":[]},
			{"start":0,"end":2,"tags":[]}
		]`, want: []AudioEvent{{Start: 0, End: 2, Tags: []AudioEventTag{}}}},
		{name: "invalid_tags_do_not_poison_valid_tags", raw: `[
			{"start":0,"end":1,"tags":[
				{"label":" Speech ","score":0.7},
				{"label":"Silence","score":0},
				{"label":"","score":0.9},
				{"label":"  ","score":0.9},
				{"label":"Speech\nMusic","score":0.9},
				{"label":"Music","score":1.1},
				{"label":"Music","score":-0.1},
				{"label":"Music","score":"0.8"},
				{"label":"Music","score":null},
				{"label":"Music"},
				{"label":"Music","score":1e309},
				null,"Speech"
			]}
		]`, want: []AudioEvent{{Start: 0, End: 1, Tags: []AudioEventTag{{Label: "Speech", Score: 0.7}, {Label: "Silence", Score: 0}}}}},
		{name: "all_invalid_tags_are_not_empty_evidence", raw: `[{"start":0,"end":1,"tags":[{"label":"Music"}]}]`},
		{name: "oversized_label", raw: fmt.Sprintf(`[{"start":0,"end":1,"tags":[{"label":%q,"score":0.9}]}]`, strings.Repeat("合", 129))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"text":"合成转写","sentences":[],"duration":20`
			if tc.raw != "" {
				body += `,"audio_events":` + tc.raw
			}
			body += `}`
			w := NewWorkerASR("http://example.invalid")
			w.HC.Transport = audioEventsTransport(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header),
					Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			tr, err := w.Transcribe(context.Background(), "synthetic-unavailable.aac")
			if err != nil || tr == nil || tr.Text != "合成转写" || len(tr.Segments) != 1 {
				t.Fatalf("optional events damaged transcript: transcript=%+v err=%v", tr, err)
			}
			if !reflect.DeepEqual(tr.AudioEvents, tc.want) {
				t.Fatalf("events=%+v, want %+v", tr.AudioEvents, tc.want)
			}
			encoded, err := json.Marshal(tr)
			if err != nil {
				t.Fatal(err)
			}
			if tc.want == nil && strings.Contains(string(encoded), "audio_events") {
				t.Fatal("absent or invalid events were serialized as recognized audio")
			}
		})
	}
}

func TestRetryableASRErrorClassification(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"refused", &url.Error{Op: "Post", URL: "http://example.invalid", Err: &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}}, true},
		{"reset", &net.OpError{Op: "read", Err: syscall.ECONNRESET}, true},
		{"timeout", context.DeadlineExceeded, true},
		{"canceled", context.Canceled, false},
		{"bad json", errors.New("invalid character in JSON"), false},
		{"bad URL", &url.Error{Op: "parse", Err: errors.New("invalid URL")}, false},
		{"nil", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsRetryableASRError(tc.err); got != tc.want {
				t.Fatalf("retryable = %v, want %v", got, tc.want)
			}
		})
	}
	for _, status := range []int{400, 401, 403, 404, 413, 422, 429, 500, 502, 503, 504} {
		want := status == 429 || status == 502 || status == 503 || status == 504
		err := fmt.Errorf("wrapped: %w", &WorkerHTTPError{StatusCode: status, Body: "synthetic"})
		if got := IsRetryableASRError(err); got != want {
			t.Errorf("status %d: retryable=%v, want %v", status, got, want)
		}
	}
}

func TestWorkerErrorsRetainHTTPStatusAndDoNotRetryMalformedSuccess(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusServiceUnavailable, http.StatusOK} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				_, _ = w.Write([]byte("synthetic invalid response"))
			}))
			defer srv.Close()
			_, err := NewWorkerASR(srv.URL).Transcribe(context.Background(), "synthetic.aac")
			if err == nil {
				t.Fatal("failed worker returned success")
			}
			if got := IsRetryableASRError(err); got != (status == http.StatusServiceUnavailable) {
				t.Fatalf("status %d: retryable=%v err=%v", status, got, err)
			}
			if status != http.StatusOK && !strings.Contains(err.Error(), "synthetic invalid response") {
				t.Fatalf("response detail lost: %v", err)
			}
		})
	}
}

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
