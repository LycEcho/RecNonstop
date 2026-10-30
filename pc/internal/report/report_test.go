package report

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"warehouse-audio/internal/job"
)

type fakeReporter struct {
	material string
	content  string
	err      error
}

func (f *fakeReporter) GenerateDailyReport(_ context.Context, material string) (string, error) {
	f.material = material
	return f.content, f.err
}

func newTestStore(t *testing.T, jobs []*job.Job) (*Store, *job.Store) {
	t.Helper()
	dir := t.TempDir()
	js, err := job.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, j := range jobs {
		if _, ok := js.CreateFull(j); !ok {
			t.Fatalf("create job %s", j.ID)
		}
	}
	rs, err := NewStore(filepath.Join(dir, "reports"))
	if err != nil {
		t.Fatal(err)
	}
	return rs, js
}

func TestPeriodFor(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	now := time.Date(2026, 10, 28, 18, 30, 0, 0, loc)
	date, start, end, err := PeriodFor(now, "18:00")
	if err != nil {
		t.Fatal(err)
	}
	if date != "2026-10-28" || !start.Equal(time.Date(2026, 10, 28, 0, 0, 0, 0, loc)) || !end.Equal(now) {
		t.Fatalf("evening period = %s %v %v", date, start, end)
	}

	morning := time.Date(2026, 10, 28, 9, 10, 0, 0, loc)
	date, start, end, err = PeriodFor(morning, "09:00")
	if err != nil {
		t.Fatal(err)
	}
	if date != "2026-10-27" ||
		!start.Equal(time.Date(2026, 10, 27, 0, 0, 0, 0, loc)) ||
		!end.Equal(time.Date(2026, 10, 28, 0, 0, 0, 0, loc)) {
		t.Fatalf("morning period = %s %v %v", date, start, end)
	}

	// 电脑 18:00 关机，第二天 09:00 才启动：补昨天 18:00 那一份。
	last, err := lastRunAt(morning, "18:00")
	if err != nil {
		t.Fatal(err)
	}
	if !last.Equal(time.Date(2026, 10, 27, 18, 0, 0, 0, loc)) {
		t.Fatalf("last run = %v", last)
	}
	date, start, end, err = PeriodFor(last, "18:00")
	if err != nil {
		t.Fatal(err)
	}
	if date != "2026-10-27" ||
		!start.Equal(time.Date(2026, 10, 27, 0, 0, 0, 0, loc)) ||
		!end.Equal(time.Date(2026, 10, 27, 18, 0, 0, 0, loc)) {
		t.Fatalf("catch-up period = %s %v %v", date, start, end)
	}
}

func TestNormalizeClock(t *testing.T) {
	for in, want := range map[string]string{"8:05": "08:05", "18:00": "18:00", " 23:59 ": "23:59"} {
		got, err := NormalizeClock(in)
		if err != nil || got != want {
			t.Fatalf("NormalizeClock(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "24:00", "18:60", "1800"} {
		if _, err := NormalizeClock(bad); err == nil {
			t.Fatalf("NormalizeClock(%q) should fail", bad)
		}
	}
}

func TestGenerateAndPushState(t *testing.T) {
	loc := time.Local
	now := time.Date(2026, 10, 28, 18, 30, 0, 0, loc)
	recorded := time.Date(2026, 10, 28, 9, 12, 0, 0, loc).UnixMilli()
	jobs := []*job.Job{
		{
			ID: "j1", Status: job.StatusDone, RecordedAt: recorded,
			Result: &job.Result{
				Duration: 180,
				Segments: []job.Segment{{Start: 0, End: 180, Text: "今天讨论了日报上线时间。"}},
				Summary:  "讨论日报上线时间。",
			},
		},
		{
			ID: "j2", Status: job.StatusDone, RecordedAt: recorded + 3*60*1000,
			Result: &job.Result{
				Duration: 180,
				Segments: []job.Segment{{Start: 0, End: 180, Text: "确认明天修复推送。"}},
				Summary:  "确认明天修复推送。",
			},
		},
	}
	rs, js := newTestStore(t, jobs)
	rep := &fakeReporter{content: "## 今日概览\n\n今天讨论了日报上线时间，并确认明天修复推送。\n\n## 待办\n\n- 明天修复推送\n\n## 风险与异常\n\n未发现"}
	s := NewScheduler(rs, js, rep, filepath.Join(t.TempDir(), "report_settings.json"), "录音日报",
		Settings{Enabled: true, GenerateAt: "18:00"})
	s.now = func() time.Time { return now }

	r, err := s.GenerateNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if r == nil || r.Status != StatusDone || r.JobCount != 2 || r.Date != "2026-10-28" {
		t.Fatalf("report = %+v", r)
	}
	if r.DurationSeconds != 360 {
		t.Fatalf("duration = %v", r.DurationSeconds)
	}
	if !strings.Contains(rep.material, "日报上线时间") || !strings.Contains(rep.material, "修复推送") {
		t.Fatalf("material = %s", rep.material)
	}
	if !strings.Contains(r.Content, "今天讨论了日报上线时间") || r.Summary == "" {
		t.Fatalf("content/summary = %q / %q", r.Content, r.Summary)
	}
	if _, err := os.Stat(filepath.Join(rs.dir, r.ID+".json")); err != nil {
		t.Fatalf("report file: %v", err)
	}
	if pending := rs.PendingFor("dev1"); len(pending) != 1 || pending[0].ID != r.ID {
		t.Fatalf("pending = %+v", pending)
	}
	if !rs.Ack(r.ID, "dev1") {
		t.Fatal("ack failed")
	}
	if pending := rs.PendingFor("dev1"); len(pending) != 0 {
		t.Fatalf("pending after ack = %+v", pending)
	}
	if pending := rs.PendingFor("dev2"); len(pending) != 1 {
		t.Fatalf("other device should still get report: %+v", pending)
	}
}

func TestFallbackWhenReporterFails(t *testing.T) {
	now := time.Date(2026, 10, 28, 18, 30, 0, 0, time.Local)
	jobs := []*job.Job{{
		ID: "j1", Status: job.StatusDone,
		RecordedAt: time.Date(2026, 10, 28, 17, 50, 0, 0, time.Local).UnixMilli(),
		Result: &job.Result{
			Duration: 180,
			Segments: []job.Segment{{Text: "这是一段测试转写。"}},
			Summary:  "测试摘要。",
		},
	}}
	rs, js := newTestStore(t, jobs)
	s := NewScheduler(rs, js, &fakeReporter{err: fmt.Errorf("model down")},
		filepath.Join(t.TempDir(), "report_settings.json"), "录音日报",
		Settings{Enabled: true, GenerateAt: "18:00"})
	s.now = func() time.Time { return now }

	r, err := s.GenerateNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != StatusDone || !strings.Contains(r.Content, "分段摘要") ||
		!strings.Contains(r.Error, "model down") {
		t.Fatalf("fallback report = %+v", r)
	}
}

func TestSettingsUpdatePersists(t *testing.T) {
	rs, js := newTestStore(t, nil)
	path := filepath.Join(t.TempDir(), "report_settings.json")
	s := NewScheduler(rs, js, nil, path, "录音日报",
		Settings{Enabled: true, GenerateAt: "18:00"})
	if err := s.Update(false, "22:30"); err != nil {
		t.Fatal(err)
	}
	st := s.State()
	if st.Enabled || st.GenerateAt != "22:30" {
		t.Fatalf("state = %+v", st)
	}
	reloaded := loadSettings(path, Settings{Enabled: true, GenerateAt: "18:00"})
	if reloaded.Enabled || reloaded.GenerateAt != "22:30" {
		t.Fatalf("reloaded = %+v", reloaded)
	}
	if err := s.Update(true, "25:00"); err == nil {
		t.Fatal("invalid time accepted")
	}
}
