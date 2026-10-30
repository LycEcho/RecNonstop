package job

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStorePersistsAndIdempotency(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	j, created := s.CreateFull(&Job{
		ID:             "j1",
		AudioPath:      filepath.Join(dir, "uploads", "a.aac"),
		IdempotencyKey: "same-key",
	})
	if !created || j.ID != "j1" {
		t.Fatalf("create: created=%v id=%s", created, j.ID)
	}

	// 重启后必须还能查到：这是之前最大的数据丢失点。
	s2, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s2.Get("j1"); !ok {
		t.Fatal("job not reloaded from disk")
	}

	dup, created := s2.CreateFull(&Job{
		ID: "j2", AudioPath: filepath.Join(dir, "uploads", "b.aac"),
		IdempotencyKey: "same-key",
	})
	if created {
		t.Fatal("duplicate idempotency key created a new job")
	}
	if dup.ID != "j1" {
		t.Fatalf("duplicate returned %s", dup.ID)
	}
}

func TestRunningRequeuedOnRestart(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewStore(dir)
	_, _ = s.CreateFull(&Job{ID: "j1", AudioPath: filepath.Join(dir, "a.aac")})
	if err := s.Update("j1", func(j *Job) {
		j.Status = StatusRunning
		j.Stage = "analyzing"
	}); err != nil {
		t.Fatal(err)
	}

	s2, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	j, ok := s2.Get("j1")
	if !ok {
		t.Fatal("job missing")
	}
	if j.Status != StatusQueued {
		t.Fatalf("status = %s, want queued", j.Status)
	}
	if j.Attempts != 1 {
		t.Fatalf("attempts = %d, want 1", j.Attempts)
	}
}

func TestRetention(t *testing.T) {
	dir := t.TempDir()
	audio := filepath.Join(dir, "uploads", "old.aac")
	if err := os.MkdirAll(filepath.Dir(audio), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(audio, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(audio, old, old); err != nil {
		t.Fatal(err)
	}

	s, _ := NewStore(dir)
	_, _ = s.CreateFull(&Job{
		ID: "j1", AudioPath: audio, Status: StatusDone, CreatedAt: old,
	})

	a, j := s.Retention(1, 1)
	if a != 1 || j != 1 {
		t.Fatalf("retention removed audio=%d jobs=%d, want 1/1", a, j)
	}
	if _, err := os.Stat(audio); !os.IsNotExist(err) {
		t.Fatal("audio file still exists")
	}
	if _, ok := s.Get("j1"); ok {
		t.Fatal("job still exists")
	}
}

func TestInvalidAnalysisDoesNotBreakMarshal(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewStore(dir)
	_, _ = s.CreateFull(&Job{ID: "j1", AudioPath: filepath.Join(dir, "a.aac")})
	if err := s.Update("j1", func(j *Job) {
		j.Result = &Result{JobID: "j1", Summary: "ok"}
	}); err != nil {
		t.Fatal(err)
	}
	s2, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if j, ok := s2.Get("j1"); !ok || j.Result == nil || j.Result.Summary != "ok" {
		t.Fatal("result not persisted")
	}
}
