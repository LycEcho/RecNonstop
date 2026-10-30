package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"warehouse-audio/internal/infer"
	"warehouse-audio/internal/job"
)

type fakeASR struct{}

func (fakeASR) Transcribe(context.Context, string) (*infer.Transcript, error) {
	return &infer.Transcript{
		Duration: 12.5,
		Segments: []infer.Segment{
			{Start: 0, End: 2, Speaker: "spk_0", Text: "你好"},
			{Start: 2, End: 4, Speaker: "spk_1", Text: "在的"},
		},
	}, nil
}

type fakeAnalyzer struct{ err error }

func (f fakeAnalyzer) Analyze(context.Context, *infer.Transcript) (*infer.Analysis, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &infer.Analysis{
		Summary: "一段测试对话",
		Raw:     json.RawMessage(`{"summary":"一段测试对话"}`),
	}, nil
}

func newStore(t *testing.T) (*job.Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := job.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	audio := filepath.Join(dir, "uploads", "a.aac")
	_, _ = s.CreateFull(&job.Job{ID: "j1", AudioPath: audio})
	return s, audio
}

func waitStatus(t *testing.T, s *job.Store, id string) *job.Job {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		j, ok := s.Get(id)
		if ok && (j.Status == job.StatusDone || j.Status == job.StatusFailed) {
			return j
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("job did not finish")
	return nil
}

func TestPipelineSuccess(t *testing.T) {
	s, audio := newStore(t)
	p := New(fakeASR{}, fakeAnalyzer{}, s, "local", nil, 1, 10, 5*time.Second)
	if err := p.Enqueue("j1", audio); err != nil {
		t.Fatal(err)
	}
	j := waitStatus(t, s, "j1")
	p.Close()

	if j.Status != job.StatusDone {
		t.Fatalf("status = %s error=%s", j.Status, j.Error)
	}
	if j.Result == nil || j.Result.Summary != "一段测试对话" {
		t.Fatalf("result = %+v", j.Result)
	}
	if len(j.Result.Segments) != 2 {
		t.Fatalf("segments = %d", len(j.Result.Segments))
	}
	if !json.Valid(j.Result.Analysis) {
		t.Fatal("analysis is invalid json")
	}
}

func TestPipelineKeepsTranscriptWhenAnalyzeFails(t *testing.T) {
	s, audio := newStore(t)
	p := New(fakeASR{}, fakeAnalyzer{err: errors.New("llm down")}, s, "local", nil, 1, 10, 5*time.Second)
	if err := p.Enqueue("j1", audio); err != nil {
		t.Fatal(err)
	}
	j := waitStatus(t, s, "j1")
	p.Close()

	if j.Status != job.StatusDone {
		t.Fatalf("status = %s", j.Status)
	}
	if j.Result == nil || len(j.Result.Segments) != 2 {
		t.Fatal("transcript was lost when analysis failed")
	}
	if j.Result.Summary != "" {
		t.Fatalf("summary = %q", j.Result.Summary)
	}
	if j.Error != "" {
		t.Fatalf("job error = %q", j.Error)
	}
}
