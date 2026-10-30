package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"warehouse-audio/internal/infer"
	"warehouse-audio/internal/job"
	"warehouse-audio/internal/pairing"
	"warehouse-audio/internal/pipeline"
)

type stubASR struct{}

func (stubASR) Transcribe(context.Context, string) (*infer.Transcript, error) {
	return &infer.Transcript{
		Duration: 2.0,
		Segments: []infer.Segment{
			{Start: 0.1, End: 0.9, Speaker: "spk_0", Text: "第一段"},
			{Start: 1.1, End: 1.9, Speaker: "spk_1", Text: "第二段"},
		},
	}, nil
}

type stubAnalyzer struct{}

func (stubAnalyzer) Analyze(context.Context, *infer.Transcript) (*infer.Analysis, error) {
	return &infer.Analysis{Summary: "合并摘要", Speakers: []string{"spk_0", "spk_1"}}, nil
}

type captureAnalyzer struct {
	got *infer.Transcript
}

func (c *captureAnalyzer) Analyze(_ context.Context, t *infer.Transcript) (*infer.Analysis, error) {
	c.got = t
	return &infer.Analysis{Summary: "合并摘要"}, nil
}

func makeTone(t *testing.T, path string) {
	t.Helper()
	cmd := exec.Command("ffmpeg", "-hide_banner", "-nostdin", "-v", "error", "-y",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=1",
		"-ar", "16000", "-ac", "1", "-c:a", "pcm_s16le", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("make tone: %v: %s", err, out)
	}
}

func TestAdminMergeCreatesUnifiedJob(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	uploads := filepath.Join(dir, "uploads")
	if err := os.MkdirAll(uploads, 0o755); err != nil {
		t.Fatal(err)
	}
	a := filepath.Join(uploads, "a.wav")
	b := filepath.Join(uploads, "b.wav")
	makeTone(t, a)
	makeTone(t, b)

	store, err := job.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for i, spec := range []struct {
		id   string
		path string
	}{{"j_a", a}, {"j_b", b}} {
		_, created := store.CreateFull(&job.Job{
			ID:          spec.id,
			Kind:        "segment",
			Status:      job.StatusDone,
			AudioPath:   spec.path,
			SegmentName: spec.id + ".wav",
			RecordedAt:  now.Add(time.Duration(i-2) * time.Minute).UnixMilli(),
			Result:      &job.Result{JobID: spec.id, Duration: 1},
		})
		if !created {
			t.Fatalf("create job %s", spec.id)
		}
	}

	p := pipeline.New(stubASR{}, stubAnalyzer{}, store, "local", nil, 1, 8, 10*time.Second)
	defer p.Close()
	srv := &Server{
		Store:           store,
		Pipeline:        p,
		UploadDir:       uploads,
		MergeDir:        filepath.Join(dir, "merged"),
		MergeMaxSeconds: 60,
		MergeMaxJobs:    10,
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/merge",
		strings.NewReader(`{"job_ids":["j_b","j_a"],"mode":"audio"}`))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var resp struct {
		JobID    string  `json:"job_id"`
		Duration float64 `json:"duration"`
		Sources  int     `json:"sources"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Sources != 2 || resp.Duration < 1.9 {
		t.Fatalf("response = %+v", resp)
	}

	var merged *job.Job
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		j, ok := store.Get(resp.JobID)
		if ok && j.Status == job.StatusDone {
			merged = j
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if merged == nil {
		t.Fatal("merged job did not finish")
	}
	if merged.Kind != "merge" || merged.MergeMode != "audio" || len(merged.SourceJobIDs) != 2 || len(merged.SourceSpans) != 2 {
		t.Fatalf("merged job = %+v", merged)
	}
	// 输入顺序是 b,a；服务端应按 recorded_at 排成 a,b。
	if merged.SourceJobIDs[0] != "j_a" {
		t.Fatalf("source order = %v", merged.SourceJobIDs)
	}
	if merged.Result == nil || len(merged.Result.Sources) != 2 {
		t.Fatalf("result sources missing: %+v", merged.Result)
	}
	if merged.Result.Summary != "合并摘要" {
		t.Fatalf("summary = %q", merged.Result.Summary)
	}
	if _, err := os.Stat(merged.AudioPath); err != nil {
		t.Fatalf("merged audio missing: %v", err)
	}
}

func TestAdminMergeTranscriptMode(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	uploads := filepath.Join(dir, "uploads")
	if err := os.MkdirAll(uploads, 0o755); err != nil {
		t.Fatal(err)
	}
	a := filepath.Join(uploads, "a.wav")
	b := filepath.Join(uploads, "b.wav")
	makeTone(t, a)
	makeTone(t, b)

	store, err := job.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for i, spec := range []struct {
		id, path, text string
	}{{"j_a", a, "第一段"}, {"j_b", b, "第二段"}} {
		_, created := store.CreateFull(&job.Job{
			ID:          spec.id,
			Kind:        "segment",
			Status:      job.StatusDone,
			AudioPath:   spec.path,
			SegmentName: spec.id + ".wav",
			RecordedAt:  now.Add(time.Duration(i-2) * time.Minute).UnixMilli(),
			Result: &job.Result{
				JobID:    spec.id,
				Duration: 1,
				Segments: []job.Segment{{Start: 0.1, End: 0.9, Speaker: "spk_0", Text: spec.text}},
			},
		})
		if !created {
			t.Fatalf("create job %s", spec.id)
		}
	}

	ca := &captureAnalyzer{}
	p := pipeline.New(stubASR{}, ca, store, "local", nil, 1, 8, 10*time.Second)
	defer p.Close()
	srv := &Server{
		Store:           store,
		Pipeline:        p,
		UploadDir:       uploads,
		MergeDir:        filepath.Join(dir, "merged"),
		MergeMaxSeconds: 60,
		MergeMaxJobs:    10,
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/merge",
		strings.NewReader(`{"job_ids":["j_b","j_a"],"mode":"transcript"}`))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var resp struct {
		JobID string `json:"job_id"`
		Mode  string `json:"mode"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Mode != "transcript" {
		t.Fatalf("mode = %q", resp.Mode)
	}
	merged, ok := store.Get(resp.JobID)
	if !ok {
		t.Fatal("merged job missing")
	}
	if merged.Status != job.StatusDone || merged.MergeMode != "transcript" {
		t.Fatalf("job = %+v", merged)
	}
	if merged.Result == nil || len(merged.Result.Segments) != 2 || len(merged.Result.Sources) != 2 {
		t.Fatalf("result = %+v", merged.Result)
	}
	if merged.Result.Segments[0].SourceJobID != "j_a" ||
		merged.Result.Segments[0].Text != "第一段" ||
		merged.Result.Segments[1].SourceJobID != "j_b" {
		t.Fatalf("segments = %+v", merged.Result.Segments)
	}
	if merged.Result.Summary != "合并摘要" {
		t.Fatalf("summary = %q", merged.Result.Summary)
	}
	if ca.got == nil || len(ca.got.Segments) != 2 {
		t.Fatalf("analyzer transcript = %+v", ca.got)
	}
	if !strings.Contains(ca.got.Segments[0].Speaker, "@") ||
		ca.got.Segments[0].Speaker == ca.got.Segments[1].Speaker {
		t.Fatalf("speaker labels not disambiguated: %+v", ca.got.Segments)
	}
	if _, err := os.Stat(merged.AudioPath); err != nil {
		t.Fatalf("merged audio missing: %v", err)
	}
}

func TestMergeJobsRequiresTokenAndOwnership(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	store, err := job.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := pairing.NewManager(filepath.Join(dir, "devices.json"), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	code, _ := pair.StartWindow()
	dev, token, err := pair.Pair("phone", code)
	if err != nil {
		t.Fatal(err)
	}
	own := filepath.Join(dir, "own.wav")
	own2 := filepath.Join(dir, "own2.wav")
	other := filepath.Join(dir, "other.wav")
	for _, p := range []string{own, own2, other} {
		makeTone(t, p)
	}
	for _, spec := range []struct {
		id, device, audio string
	}{{"j_own", dev.ID, own}, {"j_own2", dev.ID, own2}, {"j_other", "someone-else", other}} {
		if _, created := store.CreateFull(&job.Job{
			ID:        spec.id,
			Kind:      "segment",
			Status:    job.StatusDone,
			DeviceID:  spec.device,
			AudioPath: spec.audio,
			Result:    &job.Result{JobID: spec.id, Duration: 1},
		}); !created {
			t.Fatalf("create %s", spec.id)
		}
	}

	p := pipeline.New(stubASR{}, stubAnalyzer{}, store, "local", nil, 1, 8, 10*time.Second)
	defer p.Close()
	srv := &Server{
		Store:           store,
		Pipeline:        p,
		Pair:            pair,
		UploadDir:       filepath.Join(dir, "uploads"),
		MergeDir:        filepath.Join(dir, "merged"),
		MergeMaxSeconds: 60,
		MergeMaxJobs:    10,
	}

	// 没有 token：正式接口必须 401，而不是走本机 admin 放行。
	req := httptest.NewRequest(http.MethodPost, "/api/v1/jobs/merge",
		strings.NewReader(`{"job_ids":["j_own","j_other"]}`))
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("no token status = %d, body = %s", w.Code, w.Body.String())
	}

	// 有 token 也不能合并别的设备的分段。
	req = httptest.NewRequest(http.MethodPost, "/api/v1/jobs/merge",
		strings.NewReader(`{"job_ids":["j_own","j_other"]}`))
	req.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-device status = %d, body = %s", w.Code, w.Body.String())
	}

	// 合并自己的两段：正常 202，并跑完。
	req = httptest.NewRequest(http.MethodPost, "/api/v1/jobs/merge",
		strings.NewReader(`{"job_ids":["j_own","j_own2"],"mode":"audio"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("merge status = %d, body = %s", w.Code, w.Body.String())
	}
	var resp struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		j, ok := store.Get(resp.JobID)
		if ok && j.Status == job.StatusDone {
			if j.Kind != "merge" || len(j.Result.Sources) != 2 {
				t.Fatalf("merged job = %+v", j)
			}
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatal("authenticated merge did not finish")
}

func TestAdminMergeRejectsSingleJob(t *testing.T) {
	dir := t.TempDir()
	store, err := job.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{Store: store, UploadDir: filepath.Join(dir, "uploads")}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/merge",
		strings.NewReader(`{"job_ids":["j_a"]}`))
	req.RemoteAddr = "127.0.0.1:12345"
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
}
