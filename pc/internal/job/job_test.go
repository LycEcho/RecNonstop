package job

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAudioEventsPersistAsIndependentOptionalResultData(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.CreateFull(&Job{ID: "audio-tags", Status: StatusDone, Result: &Result{
		AudioEvents: []AudioEvent{{Start: 1, End: 3, Tags: []AudioEventTag{{Label: "Speech", Score: 0.75}}}, {Start: 3, End: 5, Tags: []AudioEventTag{}}},
	}})
	read, _ := s.Get("audio-tags")
	read.Result.AudioEvents[0].Start = 99
	read.Result.AudioEvents[0].Tags[0].Label = "reader-local"
	again, _ := s.Get("audio-tags")
	if again.Result.AudioEvents[0].Start != 1 || again.Result.AudioEvents[0].Tags[0].Label != "Speech" {
		t.Fatal("result snapshots shared mutable audio events or nested tags")
	}
	reloaded, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	saved, _ := reloaded.Get("audio-tags")
	encoded, err := json.Marshal(saved.Result)
	if err != nil || !strings.Contains(string(encoded), `"audio_events":[`) || !strings.Contains(string(encoded), `"label":"Speech","score":0.75`) || !strings.Contains(string(encoded), `"tags":[]`) {
		t.Fatalf("audio events did not survive storage or result JSON: %s %v", encoded, err)
	}
	var legacy Result
	if err := json.Unmarshal([]byte(`{"job_id":"legacy","segments":[]}`), &legacy); err != nil || len(legacy.AudioEvents) != 0 {
		t.Fatalf("legacy result incompatible: %+v %v", legacy, err)
	}
	encoded, err = json.Marshal(legacy)
	if err != nil || strings.Contains(string(encoded), "audio_events") {
		t.Fatalf("absent optional events changed legacy output: %s %v", encoded, err)
	}
}

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

func TestConcurrentReadersReceiveConsistentJobSnapshots(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s.CreateFull(&Job{ID: "snapshot", IdempotencyKey: "synthetic-key", Error: "0",
		Result: &Result{Summary: "0", Segments: []Segment{{Text: "0"}}, Backend: map[string]string{"version": "0"}}})
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for i := 1; i <= 100; i++ {
			value := strconv.Itoa(i)
			if err := s.Update("snapshot", func(j *Job) {
				j.Error = value
				j.Result.Summary = value
				j.Result.Segments[0].Text = value
				j.Result.Backend["version"] = value
			}); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	for reader := 0; reader < 4; reader++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < 300; i++ {
				for _, read := range []func() (*Job, bool){
					func() (*Job, bool) { return s.Get("snapshot") },
					func() (*Job, bool) { return s.FindIdem("synthetic-key") },
				} {
					j, ok := read()
					if !ok || j.Result == nil || j.Error != j.Result.Summary || j.Error != j.Result.Segments[0].Text || j.Error != j.Result.Backend["version"] {
						t.Errorf("inconsistent snapshot: %+v", j)
						return
					}
					// Returned nested maps must not still belong to the store.
					j.Result.Backend["version"] = "reader-local"
				}
			}
		}()
	}
	close(start)
	wg.Wait()
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

func TestRecordingStartCompatibility(t *testing.T) {
	start := time.Date(2026, 1, 2, 23, 59, 0, 123*int(time.Millisecond), time.Local)
	const filename = "rec_20260102_235900_123.aac"
	cases := []struct {
		name string
		job  Job
		want time.Time
	}{
		{"current start epoch", Job{RecordedAt: start.UnixMilli(), SegmentName: filename}, start},
		{"legacy end epoch", Job{RecordedAt: start.Add(3 * time.Minute).UnixMilli(), SegmentName: filename, Result: &Result{Duration: 180}}, start},
		{"legacy pending without duration", Job{RecordedAt: start.Add(3 * time.Minute).UnixMilli(), SegmentName: filename}, start},
		{"known longer duration", Job{RecordedAt: start.Add(20 * time.Minute).UnixMilli(), SegmentName: filename, Result: &Result{Duration: 1200}}, start},
		{"seconds filename", Job{RecordedAt: start.Add(3 * time.Minute).UnixMilli(), SegmentName: "rec_20260102_235900.m4a"}, start.Truncate(time.Second)},
		{"eight hour timezone mismatch", Job{RecordedAt: start.Add(8 * time.Hour).UnixMilli(), SegmentName: filename}, start.Add(8 * time.Hour)},
		{"future filename rejected", Job{RecordedAt: start.Add(-6 * time.Second).UnixMilli(), SegmentName: filename}, start.Add(-6 * time.Second)},
		{"unknown filename", Job{RecordedAt: start.UnixMilli(), SegmentName: "meeting.aac", Result: &Result{Duration: 180}}, start},
		{"invalid date", Job{RecordedAt: start.UnixMilli(), SegmentName: "rec_20260230_235900_123.aac"}, start},
		{"invalid milliseconds", Job{RecordedAt: start.UnixMilli(), SegmentName: "rec_20260102_235900_12.aac"}, start},
		{"directory not ordinary name", Job{RecordedAt: start.UnixMilli(), SegmentName: "recordings/rec_20260102_235900_123.aac"}, start},
		{"merged title ignored", Job{Kind: "merge", RecordedAt: start.Add(3 * time.Minute).UnixMilli(), SegmentName: filename}, start.Add(3 * time.Minute)},
		{"no epoch uses duration", Job{CreatedAt: start.Add(3 * time.Minute), SegmentName: "rec_20260101_120000.aac", Result: &Result{Duration: 180}}, start},
		{"no epoch uses segment end", Job{CreatedAt: start.Add(90 * time.Second), Result: &Result{Segments: []Segment{{End: 90}}}}, start},
		{"no epoch or duration", Job{CreatedAt: start}, start},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := RecordingStart(&tc.job); !got.Equal(tc.want) {
				t.Fatalf("start = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestReportWindowsUseRecordingStartAcrossMidnight(t *testing.T) {
	day := time.Date(2026, 1, 2, 0, 0, 0, 0, time.Local)
	midnight := day.AddDate(0, 0, 1)
	legacyStart := midnight.Add(-time.Minute)
	currentStart := legacyStart.Add(30 * time.Second)
	s := &Store{jobs: map[string]*Job{
		"legacy": {ID: "legacy", Status: StatusDone, RecordedAt: legacyStart.Add(3 * time.Minute).UnixMilli(),
			SegmentName: "rec_20260102_235900_000.aac", Result: &Result{Duration: 180, Segments: []Segment{{Text: "合成旧记录"}}}},
		"current": {ID: "current", Status: StatusDone, RecordedAt: currentStart.UnixMilli(),
			SegmentName: "rec_20260102_235930_000.aac", Result: &Result{Duration: 60, Segments: []Segment{{Text: "合成新记录"}}}},
		"pending": {ID: "pending", Status: StatusQueued, RecordedAt: legacyStart.Add(3 * time.Minute).UnixMilli(),
			SegmentName: "rec_20260102_235900_500.aac"},
	}}
	completed := s.CompletedBetween(day.UnixMilli(), midnight.UnixMilli())
	if len(completed) != 2 || completed[0].ID != "legacy" || completed[1].ID != "current" {
		t.Fatalf("wrong day or order for legacy records: %+v", completed)
	}
	if n := s.UnfinishedBetween(day.UnixMilli(), midnight.UnixMilli()); n != 1 {
		t.Fatalf("previous day's pending count = %d, want 1", n)
	}
	if got := s.CompletedBetween(midnight.UnixMilli(), midnight.AddDate(0, 0, 1).UnixMilli()); len(got) != 0 {
		t.Fatalf("previous day's recording leaked into next day: %+v", got)
	}
	if n := s.UnfinishedBetween(midnight.UnixMilli(), midnight.AddDate(0, 0, 1).UnixMilli()); n != 0 {
		t.Fatalf("previous day's pending count in next day = %d", n)
	}
}
