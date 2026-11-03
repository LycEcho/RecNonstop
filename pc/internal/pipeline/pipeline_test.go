package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"warehouse-audio/internal/infer"
	"warehouse-audio/internal/job"
)

type asrFunc func(context.Context, string) (*infer.Transcript, error)

func (f asrFunc) Transcribe(ctx context.Context, path string) (*infer.Transcript, error) {
	return f(ctx, path)
}

func TestASRBackendOutageRetriesUntilRecovery(t *testing.T) {
	calls, waits := 0, 0
	p := &Pipeline{ASR: asrFunc(func(context.Context, string) (*infer.Transcript, error) {
		calls++
		if calls <= 3 {
			return nil, &infer.WorkerHTTPError{StatusCode: http.StatusServiceUnavailable, Body: "offline"}
		}
		return &infer.Transcript{Text: "合成转写"}, nil
	})}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	tr, err := p.transcribeWithRetry(ctx, "synthetic.aac", func(got context.Context, delay time.Duration) error {
		waits++
		if got != ctx || delay != 25*time.Second {
			t.Fatalf("retry lost job context or backoff: %v", delay)
		}
		return nil // Deterministic clock: no 25-second wall-clock wait.
	})
	if err != nil || tr == nil || tr.Text != "合成转写" || calls != 4 || waits != 3 {
		t.Fatalf("tr=%+v err=%v calls=%d waits=%d", tr, err, calls, waits)
	}
}

func TestASRPermanentFailuresDoNotRetry(t *testing.T) {
	for _, permanent := range []error{
		&infer.WorkerHTTPError{StatusCode: http.StatusBadRequest, Body: "bad audio"},
		errors.New("invalid JSON response"),
	} {
		calls := 0
		p := &Pipeline{ASR: asrFunc(func(context.Context, string) (*infer.Transcript, error) {
			calls++
			return nil, permanent
		})}
		_, err := p.transcribeWithRetry(context.Background(), "synthetic.aac", func(context.Context, time.Duration) error {
			t.Fatal("permanent failure entered retry wait")
			return nil
		})
		if !errors.Is(err, permanent) || calls != 1 {
			t.Fatalf("err=%v calls=%d", err, calls)
		}
	}
}

func TestASRRetryCancellationRetainsLastError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	offline := &infer.WorkerHTTPError{StatusCode: http.StatusBadGateway, Body: "synthetic backend offline"}
	calls := 0
	p := &Pipeline{ASR: asrFunc(func(context.Context, string) (*infer.Transcript, error) {
		calls++
		return nil, offline
	})}
	_, err := p.transcribeWithRetry(ctx, "synthetic.aac", func(ctx context.Context, delay time.Duration) error {
		cancel()
		return waitForASRRetry(ctx, delay)
	})
	if !errors.Is(err, context.Canceled) || !errors.Is(err, offline) || calls != 1 {
		t.Fatalf("cancellation or last error lost: %v (calls=%d)", err, calls)
	}
}

func TestASRRetryHonorsExpiredJobDeadline(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	p := &Pipeline{ASR: asrFunc(func(context.Context, string) (*infer.Transcript, error) {
		t.Fatal("ASR called after total job deadline")
		return nil, nil
	})}
	_, err := p.transcribeWithRetry(ctx, "synthetic.aac", waitForASRRetry)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline lost: %v", err)
	}
}

func TestCloseCancelsASRWaitAndPreservesQueuedJobs(t *testing.T) {
	s, audio := newStore(t)
	_, _ = s.CreateFull(&job.Job{ID: "j2", AudioPath: audio})
	entered := make(chan struct{})
	calls := 0
	p := New(asrFunc(func(context.Context, string) (*infer.Transcript, error) {
		calls++
		if calls == 1 {
			close(entered)
		}
		return nil, &infer.WorkerHTTPError{StatusCode: http.StatusServiceUnavailable, Body: "offline"}
	}), fakeAnalyzer{}, s, "local", nil, 1, 10, 30*time.Minute)
	defer p.Close()
	if err := p.Enqueue("j1", audio); err != nil {
		t.Fatal(err)
	}
	if err := p.Enqueue("j2", audio); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("ASR did not start")
	}
	closed := make(chan struct{})
	go func() {
		p.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("shutdown waited for ASR backoff or drained the queue")
	}
	if calls != 1 {
		t.Fatalf("shutdown consumed backlog: calls=%d", calls)
	}
	for _, id := range []string{"j1", "j2"} {
		j, _ := s.Get(id)
		if j.Status != job.StatusQueued || j.Attempts != 0 {
			t.Fatalf("%s not recoverable after shutdown: %+v", id, j)
		}
	}
}

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

type analyzerFunc func(context.Context, *infer.Transcript) (*infer.Analysis, error)

func (f analyzerFunc) Analyze(ctx context.Context, tr *infer.Transcript) (*infer.Analysis, error) {
	return f(ctx, tr)
}

func TestPipelineKeepsAudioEventsInEarlyAndFinalIndependentResults(t *testing.T) {
	for _, analysisFails := range []bool{false, true} {
		s, audio := newStore(t)
		tr := &infer.Transcript{Duration: 5, Segments: []infer.Segment{{Text: "合成语音。"}},
			AudioEvents: []infer.AudioEvent{{Start: 1, End: 3, Tags: []infer.AudioEventTag{{Label: "Speech", Score: 0.75}}}, {Start: 3, End: 5, Tags: []infer.AudioEventTag{}}}}
		p := &Pipeline{Store: s, Mode: "local", timeout: time.Second, retries: 1,
			ASR: asrFunc(func(context.Context, string) (*infer.Transcript, error) { return tr, nil })}
		var published *job.Result
		p.Analyzer = analyzerFunc(func(_ context.Context, input *infer.Transcript) (*infer.Analysis, error) {
			if err := s.Update("j1", func(j *job.Job) { published = j.Result }); err != nil {
				t.Fatal(err)
			}
			if published == nil || len(published.AudioEvents) != 2 || published.AudioEvents[0].Tags[0].Score != 0.75 || published.AudioEvents[1].Tags == nil {
				t.Fatal("audio events were not published with the early transcript")
			}
			input.AudioEvents[0].Tags[0].Label = "analyzer-local"
			if analysisFails {
				return nil, errors.New("synthetic analysis failure")
			}
			return &infer.Analysis{Summary: "合成摘要。"}, nil
		})
		p.Run(context.Background(), "j1", audio)
		final, _ := s.Get("j1")
		if final.Status != job.StatusDone || len(final.Result.AudioEvents) != 2 || final.Result.AudioEvents[0].Tags[0].Label != "Speech" || published.AudioEvents[0].Tags[0].Label != "Speech" || final.Result.AudioEvents[1].Tags == nil {
			t.Fatal("analysis success/failure lost events or shared mutable transcript tags")
		}
		if err := s.Update("j1", func(j *job.Job) { j.Result.AudioEvents[0].Tags[0].Label = "final-only" }); err != nil {
			t.Fatal(err)
		}
		if published.AudioEvents[0].Tags[0].Label != "Speech" {
			t.Fatal("early result shared audio-event tag storage with the final result")
		}
	}
}

func TestPublishedTranscriptRemainsImmutableDuringAnalysis(t *testing.T) {
	s, audio := newStore(t)
	analyzing := make(chan struct{})
	releaseAnalysis := make(chan struct{})
	finished := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	const summary = "完整测试摘要"
	const analysis = `{"summary":"完整测试摘要","actions":[],"risks":[]}`
	p := &Pipeline{
		ASR: fakeASR{}, Store: s, Mode: "local", timeout: 5 * time.Second, retries: 1,
		Analyzer: analyzerFunc(func(ctx context.Context, _ *infer.Transcript) (*infer.Analysis, error) {
			close(analyzing)
			select {
			case <-releaseAnalysis:
				return &infer.Analysis{Summary: summary, Raw: json.RawMessage(analysis)}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}),
	}
	go func() {
		p.Run(ctx, "j1", audio)
		close(finished)
	}()
	select {
	case <-analyzing:
	case <-time.After(2 * time.Second):
		t.Fatal("analysis did not start")
	}

	// Capture the actual published object under the store lock, so the final
	// assertion also detects pointer reuse even if readers miss a short race.
	var published *job.Result
	if err := s.Update("j1", func(j *job.Job) { published = j.Result }); err != nil {
		t.Fatal(err)
	}
	if published == nil || published.Summary != "" || len(published.Analysis) != 0 {
		t.Fatalf("transcript was not published before analysis: %+v", published)
	}

	validate := func(j *job.Job) error {
		if j == nil || j.Result == nil || len(j.Result.Segments) != 2 {
			return fmt.Errorf("transcript disappeared: %+v", j)
		}
		switch j.Status {
		case job.StatusRunning:
			if j.Stage != "analyzing" || j.Result.Summary != "" || len(j.Result.Analysis) != 0 {
				return fmt.Errorf("analysis leaked into an unfinished snapshot: %+v", j.Result)
			}
		case job.StatusDone:
			if j.Stage != "done" || j.Result.Summary != summary || string(j.Result.Analysis) != analysis {
				return fmt.Errorf("partially published final result: %+v", j.Result)
			}
		default:
			return fmt.Errorf("unexpected status during analysis: %s", j.Status)
		}
		return nil
	}
	read := func() error {
		j, ok := s.Get("j1")
		if !ok {
			return errors.New("Get lost the running job")
		}
		if err := validate(j); err != nil {
			return err
		}
		list := s.List(10, "")
		if len(list) != 1 {
			return fmt.Errorf("List returned %d jobs, want 1", len(list))
		}
		return validate(list[0])
	}
	const readers = 4
	ready := make(chan struct{}, readers)
	readErrors := make(chan error, readers)
	stopReaders := make(chan struct{})
	var wg sync.WaitGroup
	var stopOnce sync.Once
	stop := func() {
		stopOnce.Do(func() { close(stopReaders) })
		wg.Wait()
	}
	defer stop()
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := read()
			ready <- struct{}{}
			for {
				if err != nil {
					readErrors <- err
					return
				}
				select {
				case <-stopReaders:
					return
				default:
				}
				err = read()
				runtime.Gosched()
			}
		}()
	}
	for i := 0; i < readers; i++ {
		<-ready
	}
	close(releaseAnalysis)
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("analysis did not finish")
	}
	stop()
	close(readErrors)
	for err := range readErrors {
		t.Error(err)
	}
	if err := read(); err != nil {
		t.Error(err)
	}
	if published.Summary != "" || len(published.Analysis) != 0 || len(published.Segments) != 2 {
		t.Fatalf("published transcript was later mutated: %+v", published)
	}
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
