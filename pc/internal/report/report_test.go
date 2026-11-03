package report

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"warehouse-audio/internal/job"
)

type fakeReporter struct {
	material   string
	content    string
	err        error
	onGenerate func(context.Context)
}

func (f *fakeReporter) GenerateDailyReport(ctx context.Context, material string) (string, error) {
	f.material = material
	if f.onGenerate != nil {
		f.onGenerate(ctx)
	}
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

func TestStoreGetReturnsConsistentSnapshotsDuringUpdate(t *testing.T) {
	s, _ := newTestStore(t, nil)
	if err := s.Save(&Report{ID: "concurrent", Summary: "version-0", Content: "version-0",
		Detail: json.RawMessage(`{"version":0}`), DeliveredTo: []string{"version-0"}}); err != nil {
		t.Fatal(err)
	}
	const readers = 4
	stop := make(chan struct{})
	ready := make(chan struct{}, readers)
	readErrors := make(chan error, readers)
	var wg sync.WaitGroup
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ready <- struct{}{}
			for {
				select {
				case <-stop:
					return
				default:
				}
				r, ok := s.Get("concurrent")
				if !ok {
					readErrors <- fmt.Errorf("report disappeared during update")
					return
				}
				want := fmt.Sprintf("version-%d", r.JobCount)
				if r.Summary != want || r.Content != want ||
					string(r.Detail) != fmt.Sprintf(`{"version":%d}`, r.JobCount) ||
					len(r.DeliveredTo) != 1 || r.DeliveredTo[0] != want {
					readErrors <- fmt.Errorf("partially updated report snapshot: %+v", r)
					return
				}
				// A polling client owns its copy, including mutable slices.
				r.Detail[0] = '!'
				r.DeliveredTo[0] = "reader-local"
				runtime.Gosched()
			}
		}()
	}
	for i := 0; i < readers; i++ {
		<-ready
	}
	for version := 1; version <= 100; version++ {
		if err := s.Update("concurrent", func(r *Report) {
			r.JobCount = version
			// Force a scheduling opportunity during a real multi-field update.
			// Get must hold its read lock until the complete clone is finished.
			runtime.Gosched()
			r.Summary = fmt.Sprintf("version-%d", version)
			r.Content = r.Summary
			r.Detail = json.RawMessage(fmt.Sprintf(`{"version":%d}`, version))
			r.DeliveredTo[0] = r.Summary
		}); err != nil {
			t.Error(err)
			break
		}
	}
	close(stop)
	wg.Wait()
	close(readErrors)
	for err := range readErrors {
		t.Error(err)
	}
	final, ok := s.Get("concurrent")
	if !ok || final.JobCount != 100 || final.Summary != "version-100" ||
		string(final.Detail) != `{"version":100}` || len(final.DeliveredTo) != 1 || final.DeliveredTo[0] != "version-100" {
		t.Fatalf("reader mutation escaped its snapshot: %+v", final)
	}
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
	if r.Status != StatusDone || !strings.Contains(r.Content, "分段材料") ||
		r.Error != "日报汇总未完成，已保留分段材料：model down" {
		t.Fatalf("fallback report = %+v", r)
	}
}

func TestIncompleteReportWarnsEvenWithActionsAndRisks(t *testing.T) {
	now := time.Date(2026, 10, 28, 18, 30, 0, 0, time.Local)
	const warning = "本次日报汇总未完成，以下为已保留的分段材料。"
	const oldTopic = "旧模型心理推断"
	const oldAction = "旧模型建议干预"
	const sourceText = "请核对合成清单，缺少配件可能影响安排。"
	for _, tc := range []struct {
		name    string
		failure error
		content string
	}{
		{name: "model_error", failure: fmt.Errorf("synthetic internal model failure")},
		{name: "empty_response"},
		{name: "completed", content: "## 今日概览\n\n已完成合成材料汇总。"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rs, js := newTestStore(t, []*job.Job{{
				ID: "synthetic", Status: job.StatusDone, RecordedAt: now.Add(-time.Hour).UnixMilli(),
				Result: &job.Result{Duration: 60, Summary: oldTopic,
					Segments: []job.Segment{{Text: sourceText}},
					Analysis: json.RawMessage(`{"actions":[{"action":"旧模型建议干预"}],"risks":["旧模型心理风险"]}`)},
			}})
			s := NewScheduler(rs, js, &fakeReporter{err: tc.failure, content: tc.content},
				filepath.Join(t.TempDir(), "report_settings.json"), "录音日报",
				Settings{Enabled: true, GenerateAt: "18:00"})
			s.now = func() time.Time { return now }
			r, err := s.GenerateNow(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			reloaded, err := NewStore(rs.dir)
			if err != nil {
				t.Fatal(err)
			}
			saved, ok := reloaded.Get(r.ID)
			if !ok {
				t.Fatal("report not persisted")
			}
			original, ok := js.Get("synthetic")
			if !ok || original.Result.Summary != oldTopic ||
				!strings.Contains(string(original.Result.Analysis), oldAction) ||
				!strings.Contains(string(original.Result.Analysis), "旧模型心理风险") {
				t.Fatal("report generation removed stored source analysis")
			}
			if !strings.Contains(string(saved.Detail), sourceText) {
				t.Error("persisted detail lost the source transcript")
			}
			if tc.content != "" {
				if strings.Contains(saved.Content+saved.Summary, warning) || saved.Error != "" {
					t.Fatal("completed report incorrectly shows incomplete warning")
				}
				if !strings.Contains(string(saved.Detail), oldTopic) || !strings.Contains(string(saved.Detail), oldAction) {
					t.Error("normal successful report detail was changed by fallback filtering")
				}
				return
			}
			for _, want := range []string{warning, sourceText} {
				if !strings.Contains(saved.Content, want) {
					t.Errorf("incomplete report lost its visible warning or retained material: %q", want)
				}
			}
			for _, old := range []string{oldTopic, oldAction, "旧模型心理风险"} {
				if strings.Contains(saved.Content+string(saved.Detail), old) {
					t.Errorf("fallback content or detail reintroduced old inference: %s", old)
				}
			}
			if strings.Index(saved.Content, warning) > strings.Index(saved.Content, "## 今日概览") ||
				!strings.Contains(saved.Summary, warning) {
				t.Error("incomplete warning is not visible before material and in the summary")
			}
			if tc.failure != nil && strings.Contains(saved.Content+saved.Summary, tc.failure.Error()) {
				t.Error("internal model error exposed in user-facing report text")
			}
		})
	}
}

func TestFallbackUsesFullSourceAndOnlyShowsCandidatesWhenSourceIsMissing(t *testing.T) {
	start := time.Date(2026, 1, 2, 8, 0, 0, 0, time.Local)
	const oldSummary = "旧模型推断说话人持续焦虑。"
	const oldAction = "旧模型建议心理干预"
	const oldRisk = "旧模型推断存在心理危机"
	fullText := "这是一段合成广播朗读，并非个人经历。" + strings.Repeat("完整保留的合成原文。", 500)
	const lastText = "末尾说明：这里只是在朗读测试文本。"
	jobs := []*job.Job{
		{ID: "source", RecordedAt: start.UnixMilli(), Result: &job.Result{
			Duration: 60, Summary: oldSummary,
			Segments: []job.Segment{
				{Start: 1.25, End: 50, Speaker: "spk_0", PersonName: "合成朗读者", Text: fullText},
				{Start: 50.5, End: 60, Speaker: "spk_1", Text: lastText},
			},
			Analysis: json.RawMessage(`{"actions":[{"speaker":"spk_0","action":"旧模型建议心理干预"}],"risks":["旧模型推断存在心理危机"]}`),
		}},
		{ID: "missing-source", RecordedAt: start.Add(time.Minute).UnixMilli(), Result: &job.Result{
			Duration: 60, Summary: "缺少原文时保留的自动摘要。",
			Segments: []job.Segment{{Speaker: "spk_0", Text: " \n\t "}},
			Analysis: json.RawMessage(`{"actions":[{"speaker":"spk_0","action":"待核对的清单安排","details":"下周二之前"}],"risks":["待核对的材料风险"]}`),
		}},
		{ID: "only-candidates", RecordedAt: start.Add(2 * time.Minute).UnixMilli(), Result: &job.Result{
			Duration: 60,
			Analysis: json.RawMessage(`{"actions":[{"action":"没有摘要时也需保留的候选"}],"risks":["没有摘要时保留的风险候选"]}`),
		}},
	}
	r := &Report{Date: "2026-01-02", PeriodStart: start, PeriodEnd: start.Add(3 * time.Minute),
		JobCount: len(jobs), DurationSeconds: 180}
	before, err := json.Marshal(jobs)
	if err != nil {
		t.Fatal(err)
	}
	content := fallbackContent(r, jobs)
	for _, want := range []string{
		"本次日报汇总未完成，以下为已保留的分段材料。", "录音 3 段，共 3 分钟",
		"### 录音 1 · 01-02 08:00:00（1.0 分钟）",
		"[01-02 08:00:01.250 · 合成朗读者] " + fullText,
		"[01-02 08:00:50.500 · 录音1/spk_1] " + lastText,
		"原始转写缺失，以下自动分析需核对原文，不代表已确认。",
		"自动摘要（需核对原文）：缺少原文时保留的自动摘要。",
		"## 分段待办候选（原文缺失，须核对，不代表已确认）",
		"录音 2 · 01-02 08:01:00 · 来源：录音2/spk_0 · 负责人未明确 · 待核对的清单安排 下周二之前",
		"## 分段风险候选（原文缺失，须核对，不代表已确认）",
		"录音 2 · 01-02 08:01:00 · 待核对的材料风险",
		"没有摘要时也需保留的候选", "没有摘要时保留的风险候选",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("fallback omitted source, scope, or labelled candidate: %q", want)
		}
	}
	for _, unwanted := range []string{oldSummary, oldAction, oldRisk, "未提取到明确风险", "未提取到明确待办"} {
		if strings.Contains(content, unwanted) {
			t.Errorf("fallback reintroduced unsupported model conclusions: %q", unwanted)
		}
	}
	after, err := json.Marshal(jobs)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("fallback removed or modified original job data")
	}
	detail, err := buildInteractiveDetail(r, jobs)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(detail), oldAction) {
		t.Error("fallback source preference removed candidates from the interactive detail")
	}
}

func TestScheduledGenerationDeadlineAndIncompleteFallback(t *testing.T) {
	for _, tc := range []struct {
		name          string
		parentTimeout time.Duration
		failure       error
	}{
		{name: "full_day_budget", failure: fmt.Errorf("report output exceeded length limit")},
		{name: "earlier_parent_deadline", parentTimeout: time.Minute, failure: context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Date(2026, 10, 28, 18, 30, 0, 0, time.Local)
			rs, js := newTestStore(t, []*job.Job{{
				ID: "synthetic", Status: job.StatusDone, RecordedAt: now.Add(-time.Hour).UnixMilli(),
				Result: &job.Result{Duration: 60, Summary: "需要保留的合成材料。",
					Segments: []job.Segment{{Text: "需要保留的合成原文。"}}},
			}})
			seen := make(chan time.Time, 1)
			rep := &fakeReporter{err: tc.failure, onGenerate: func(ctx context.Context) {
				deadline, _ := ctx.Deadline()
				seen <- deadline
			}}
			s := NewScheduler(rs, js, rep, filepath.Join(t.TempDir(), "report_settings.json"),
				"录音日报", Settings{Enabled: true, GenerateAt: "18:00"})
			s.now = func() time.Time { return now }
			ctx, cancel := context.WithCancel(context.Background())
			if tc.parentTimeout > 0 {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), tc.parentTimeout)
			}
			defer cancel()
			started := time.Now()
			s.tick(ctx)
			select {
			case deadline := <-seen:
				if tc.parentTimeout > 0 {
					want, _ := ctx.Deadline()
					if !deadline.Equal(want) {
						t.Errorf("parent deadline extended: got %v want %v", deadline, want)
					}
				} else if budget := deadline.Sub(started); budget < time.Hour-5*time.Second || budget > time.Hour+5*time.Second {
					t.Errorf("scheduled full-day report budget = %v, want one hour", budget)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("scheduled reporter did not start")
			}
			// Wait for the scheduled writer before examining or cleaning its files.
			s.genMu <- struct{}{}
			<-s.genMu
			r, ok := rs.LatestDone()
			if !ok || !strings.Contains(r.Content, "需要保留的合成原文") ||
				r.Error != "日报汇总未完成，已保留分段材料："+tc.failure.Error() {
				t.Fatalf("incomplete generation lost material or misstated failure: %+v", r)
			}
		})
	}
}

func TestManualReportShowsUnfinishedRecordingCoverage(t *testing.T) {
	now := time.Date(2026, 10, 28, 18, 30, 0, 0, time.Local)
	recorded := time.Date(2026, 10, 28, 9, 0, 0, 0, time.Local)
	const completedText = "已完成片段的合成摘要。"
	const pendingText = "尚未完成片段的合成转写。"
	const content = "## 今日概览\n\n已完成片段的合成摘要。\n\n## 待办\n\n- 核对测试清单"
	for _, tc := range []struct {
		name      string
		completed bool
		pending   bool
	}{
		{name: "completed_and_queued", completed: true, pending: true},
		{name: "only_queued", pending: true},
		{name: "completed_without_pending", completed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var jobs []*job.Job
			if tc.completed {
				jobs = append(jobs, &job.Job{
					ID: "completed", Status: job.StatusDone, RecordedAt: recorded.UnixMilli(),
					Result: &job.Result{Duration: 60, Summary: completedText,
						Segments: []job.Segment{{Start: 0, End: 60, Text: completedText}}},
				})
			}
			if tc.pending {
				// A resumed job may retain a transcript while waiting for analysis.
				// That unfinished material must not enter a completed report.
				jobs = append(jobs, &job.Job{
					ID: "queued", Status: job.StatusQueued, RecordedAt: recorded.Add(time.Minute).UnixMilli(),
					Result: &job.Result{Duration: 120,
						Segments: []job.Segment{{Start: 0, End: 120, Text: pendingText}}},
				})
			}
			rs, js := newTestStore(t, jobs)
			rep := &fakeReporter{content: content}
			s := NewScheduler(rs, js, rep, filepath.Join(t.TempDir(), "report_settings.json"),
				"录音日报", Settings{Enabled: true, GenerateAt: "18:00"})
			s.now = func() time.Time { return now }

			r, err := s.GenerateNow(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			wantJobs, wantDuration := 0, float64(0)
			if tc.completed {
				wantJobs, wantDuration = 1, 60
			}
			check := func(r *Report) {
				t.Helper()
				if r == nil || r.Status != StatusDone || r.JobCount != wantJobs || r.DurationSeconds != wantDuration {
					t.Fatalf("report includes unfinished recordings in completed totals: %+v", r)
				}
				if tc.pending {
					if !strings.HasPrefix(r.Content, "> 本报告仅包含已完成分析的录音。") ||
						!strings.Contains(r.Content, "1 段录音正在处理") ||
						!strings.Contains(r.Summary, "1 段录音待处理") {
						t.Errorf("missing pending coverage: content=%q summary=%q", r.Content, r.Summary)
					}
					if strings.Count(r.Summary, "1 段录音") != 1 {
						t.Errorf("pending count repeated in summary: %q", r.Summary)
					}
				} else if strings.Contains(r.Content+r.Summary, "正在处理") ||
					strings.Contains(r.Content+r.Summary, "待处理") || strings.Contains(r.Content, "尚未纳入") {
					t.Errorf("complete report incorrectly shows pending coverage: content=%q summary=%q", r.Content, r.Summary)
				}
				if strings.Contains(r.Content+r.Summary, pendingText) {
					t.Error("unfinished transcript leaked into the report")
				}
				if tc.completed && !strings.Contains(r.Content, content) {
					t.Errorf("coverage note replaced completed report content: %q", r.Content)
				}
				if tc.completed && !strings.Contains(r.Content, "> 已分析 1 段录音，合计 1.0 分钟。") {
					t.Errorf("missing deterministic completed-job statistics: %q", r.Content)
				}
				if tc.completed && !strings.Contains(r.Summary, completedText) {
					t.Errorf("coverage note replaced the completed summary: %q", r.Summary)
				}
			}
			check(r)
			if tc.completed {
				if !strings.Contains(rep.material, completedText) ||
					!strings.Contains(rep.material, "已完成分析的录音材料与统计") ||
					strings.Contains(rep.material, pendingText) {
					t.Errorf("model material does not reflect completed coverage: %q", rep.material)
				}
			} else if rep.material != "" {
				t.Errorf("report model called without completed material: %q", rep.material)
			}

			// The coverage warning must survive storage and reach later readers.
			reloaded, err := NewStore(rs.dir)
			if err != nil {
				t.Fatal(err)
			}
			saved, ok := reloaded.Get(r.ID)
			if !ok {
				t.Fatal("generated report was not persisted")
			}
			check(saved)
		})
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

func TestMaterialIncludesLateRecordsAndStructuredFacts(t *testing.T) {
	start := time.Date(2026, 1, 2, 8, 0, 0, 0, time.Local)
	jobs := make([]*job.Job, 100)
	for i := range jobs {
		jobs[i] = &job.Job{ID: fmt.Sprint(i), RecordedAt: start.Add(time.Duration(i) * time.Minute).UnixMilli(),
			Result: &job.Result{Duration: 60, Summary: strings.Repeat("合成会议摘要", 50)}}
	}
	jobs[99].Result.Summary = "当天末尾的明确决定"
	jobs[99].Result.Analysis = json.RawMessage(`{"actions":[{"speaker":"测试负责人","action":"核对清单","details":"下周二之前"}],"risks":["材料尚未齐备"]}`)
	jobs = append(jobs, &job.Job{Result: &job.Result{Segments: []job.Segment{
		{Text: strings.Repeat("合成转写", 200) + "末尾的重要补充"},
	}}})
	r := &Report{Date: "2026-01-02", PeriodStart: start, PeriodEnd: start.Add(3 * time.Hour), JobCount: len(jobs)}
	material := buildMaterial(r, jobs)
	for _, want := range []string{"当天末尾的明确决定", "核对清单", "下周二之前", "材料尚未齐备", "末尾的重要补充"} {
		if !strings.Contains(material, want) {
			t.Errorf("material omitted %q", want)
		}
	}
	content := fallbackContent(r, jobs)
	for _, want := range []string{"当天末尾的明确决定", "核对清单", "下周二之前", "材料尚未齐备"} {
		if !strings.Contains(content, want) {
			t.Errorf("fallback omitted %q", want)
		}
	}
}

func TestMaterialUsesOnlyTranscriptWhenAvailableAndCandidatesOnlyWhenMissing(t *testing.T) {
	start := time.Date(2026, 1, 2, 8, 0, 0, 0, time.Local)
	const unsupportedSummary = "未证实的持续焦虑结论。"
	const summaryWithoutTranscript = "原文缺失时保留的合成摘要。"
	firstTranscript := "下面是合成朗读材料，并非个人经历。" + strings.Repeat("完整保留的合成转写。", 500)
	lastTranscript := "这段朗读到此结束，下一段再讨论具体安排。"
	jobs := []*job.Job{
		{RecordedAt: start.UnixMilli(), Result: &job.Result{
			Duration: 60, Summary: unsupportedSummary,
			Analysis: json.RawMessage(`{"actions":[{"action":"原文存在的旧候选待办"}],"risks":["原文存在的旧候选风险"]}`),
			Segments: []job.Segment{
				{Start: 0, End: 50, Text: firstTranscript},
				{Start: 50, End: 60, Text: lastTranscript},
			},
		}},
		{RecordedAt: start.Add(time.Minute).UnixMilli(), Result: &job.Result{
			Duration: 60, Summary: summaryWithoutTranscript,
			Segments: []job.Segment{{Speaker: "spk_0", Text: " \n\t "}},
			Analysis: json.RawMessage(`{"actions":[{"speaker":"spk_0","action":"缺少原文的候选待办","details":"下周二之前"}],"risks":["缺少原文的候选风险"]}`),
		}},
		{RecordedAt: start.Add(2 * time.Minute).UnixMilli(), Result: &job.Result{
			Duration: 60, Segments: []job.Segment{{Speaker: "spk_0", Text: "最后一段合成转写。"}},
			Analysis: json.RawMessage(`{"actions":[{"speaker":"spk_0","action":"核对末尾清单","details":"下周二之前，先确认材料来源"}],"risks":["末尾候选风险，需查证原文"]}`),
		}},
	}
	r := &Report{Date: "2026-01-02", PeriodStart: start, PeriodEnd: start.Add(3 * time.Minute),
		JobCount: len(jobs), DurationSeconds: 180}
	before, err := json.Marshal(jobs)
	if err != nil {
		t.Fatal(err)
	}
	material := buildMaterial(r, jobs)
	for _, want := range []string{
		"日期：2026-01-02", "时间段：01-02 08:00 - 01-02 08:03", "录音：3 段，共 3 分钟",
		"录音 1 · 01-02 08:00:00", "录音 3 · 01-02 08:02:00",
		"原始转写（主证据）：\n[01-02 08:00:00.000 · 录音1/未确认] " + firstTranscript,
		"[01-02 08:00:50.000 · 录音1/未确认] " + lastTranscript,
		"原始转写缺失：以下自动分析仅供参考，无法据此核实为原文事实。",
		"自动摘要（原始转写缺失，仅供参考）：" + summaryWithoutTranscript,
		"分段模型提取的待办候选（须核对原文，不代表已确认）：",
		`{"speaker":"录音2/spk_0","action":"缺少原文的候选待办","details":"下周二之前"}`,
		"分段模型提取的风险候选（须核对原文，不代表已确认）：",
		`["缺少原文的候选风险"]`,
		"[01-02 08:02:00.000 · 录音3/spk_0] 最后一段合成转写。",
	} {
		if !strings.Contains(material, want) {
			t.Errorf("material omitted evidence, context, or candidate: %q", want)
		}
	}
	for _, unwanted := range []string{unsupportedSummary, "原文存在的旧候选待办", "原文存在的旧候选风险",
		"核对末尾清单", "先确认材料来源", "末尾候选风险", "明确待办：", "有依据的风险："} {
		if strings.Contains(material, unwanted) {
			t.Errorf("material promoted unsupported model output: %q", unwanted)
		}
	}
	after, err := json.Marshal(jobs)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("building report material mutated the stored source results")
	}
	detail, err := buildInteractiveDetail(r, jobs)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(detail), "原文存在的旧候选待办") || !strings.Contains(string(detail), "核对末尾清单") {
		t.Error("excluding model candidates from report input removed them from source detail")
	}
}

func TestMaterialTranscriptKeepsSpeakerSourcesAndTimes(t *testing.T) {
	start := time.Date(2026, 1, 2, 8, 0, 0, 0, time.Local)
	tail := strings.Repeat("末段完整合成转写。", 500) + "最后的明确补充仍需保留。"
	jobs := []*job.Job{
		{RecordedAt: start.UnixMilli(), Result: &job.Result{Duration: 60, Segments: []job.Segment{
			{Start: 1.25, Speaker: "spk_0", PersonName: "测试甲", Person: "旧标识", Text: "第一条已知姓名发言。"},
			{Start: 12.5, Speaker: "spk_0", PersonName: "测试乙", Text: "同编号的本条姓名必须优先。"},
			{Start: 20, Speaker: "spk_1", Person: "测试旧名", Text: "兼容旧姓名字段。"},
			{Start: 30, Speaker: "spk_2", Text: "首段未确认说话人。"},
		}}},
		{RecordedAt: start.Add(time.Hour).UnixMilli(), Result: &job.Result{Duration: 60, Segments: []job.Segment{
			{Start: 5.5, Speaker: "spk_2", Text: tail},
		}}},
	}
	material := buildMaterial(&Report{Date: "2026-01-02", PeriodStart: start,
		PeriodEnd: start.Add(2 * time.Hour), JobCount: len(jobs)}, jobs)
	for _, want := range []string{
		"[01-02 08:00:01.250 · 测试甲] 第一条已知姓名发言。",
		"[01-02 08:00:12.500 · 测试乙] 同编号的本条姓名必须优先。",
		"[01-02 08:00:20.000 · 测试旧名] 兼容旧姓名字段。",
		"[01-02 08:00:30.000 · 录音1/spk_2] 首段未确认说话人。",
		"[01-02 09:00:05.500 · 录音2/spk_2] " + tail,
	} {
		if !strings.Contains(material, want) {
			t.Errorf("missing utterance source, timestamp, or full text: %q", want)
		}
	}
	if strings.Contains(material, "旧标识") {
		t.Error("legacy person field replaced this utterance's known name")
	}
}

func TestMaterialSoundHintsStayOutsideOriginalsAndWithinUtteranceTime(t *testing.T) {
	start := time.Date(2026, 1, 2, 8, 0, 0, 0, time.Local)
	j := &job.Job{RecordedAt: start.UnixMilli(), Result: &job.Result{Duration: 60,
		Segments: []job.Segment{
			{Start: 0, End: 10, Speaker: "spk_0", Text: "库存8件，需要12件。"},
			{Start: 30, End: 40, Speaker: "spk_1", Text: "测试乙周五补齐4件。\n否则可能延期。"},
			{Start: 50, End: 50, Speaker: "spk_2", Text: "没有可靠结束时间。"},
		},
		AudioEvents: []job.AudioEvent{
			{Start: 0, End: 10, Tags: []job.AudioEventTag{{Label: "Speech", Score: .8}}},
			{Start: 10, End: 30, Tags: []job.AudioEventTag{{Label: "Bell", Score: .9}}},
			{Start: 30, End: 40, Tags: []job.AudioEventTag{{Label: "Music", Score: .9}, {Label: "Weak", Score: .49}}},
		},
	}}
	r := &Report{Date: "2026-01-02", PeriodStart: start, PeriodEnd: start.Add(time.Minute), JobCount: 1}
	material := buildMaterial(r, []*job.Job{j})
	for _, want := range []string{
		"自动声音线索：对应下方原文时段，仅辅助，非场景证明；Speech\n[01-02 08:00:00.000 · 录音1/spk_0] 库存8件，需要12件。",
		"自动声音线索：对应下方原文时段，仅辅助，非场景证明；Music\n[01-02 08:00:30.000 · 录音1/spk_1] 测试乙周五补齐4件。\n否则可能延期。",
		"[01-02 08:00:50.000 · 录音1/spk_2] 没有可靠结束时间。",
	} {
		if !strings.Contains(material, want) {
			t.Fatalf("lost scoped sound hint or original task: %q", want)
		}
	}
	if strings.Count(material, "自动声音线索：") != 2 || strings.Contains(material, "Bell") || strings.Contains(material, "Weak") {
		t.Fatal("gap, weak label, or unknown utterance time became local evidence")
	}
	for _, plain := range []string{materialTranscript(j, 0), fallbackContent(r, []*job.Job{j})} {
		if strings.Contains(plain, "自动声音线索：") || !strings.Contains(plain, "否则可能延期。") {
			t.Fatal("fallback/original transcript contains model hints or lost the task condition")
		}
	}
}

func TestTimelineUsesRecordingStartAndKeepsUnknownSpeakersSeparate(t *testing.T) {
	start := time.Date(2026, 1, 2, 8, 0, 0, 0, time.Local)
	jobs := []*job.Job{
		{RecordedAt: start.Add(time.Minute).UnixMilli(), Result: &job.Result{Duration: 60,
			Segments: []job.Segment{{Speaker: "spk_0", Text: "第二段测试", End: 10}},
			Analysis: json.RawMessage(`{"actions":[{"speaker":"","action":"检查测试清单","details":"明天之前"}]}`)}},
		{RecordedAt: start.UnixMilli(), Result: &job.Result{Duration: 60,
			Segments: []job.Segment{{Speaker: "spk_0", Text: "第一段测试", End: 10}}}},
	}
	if got := jobStart(jobs[1]); !got.Equal(start) {
		t.Fatalf("recording start shifted: %v", got)
	}
	raw, err := buildInteractiveDetail(&Report{JobCount: 2}, jobs)
	if err != nil {
		t.Fatal(err)
	}
	var detail detailPayload
	if err := json.Unmarshal(raw, &detail); err != nil {
		t.Fatal(err)
	}
	if len(detail.People) != 2 {
		t.Fatalf("unconfirmed speakers merged across recordings: %+v", detail.People)
	}
	for _, person := range detail.People {
		if person.Status != "未确认" {
			t.Fatalf("unknown speaker shown as identified: %+v", person)
		}
	}
	if len(detail.Actions) != 1 || detail.Actions[0].Text != "负责人未明确 · 检查测试清单 明天之前" ||
		detail.Actions[0].Session != detail.Utterances[1].Session {
		t.Fatalf("source actions missing or linked to wrong conversation: %+v", detail.Actions)
	}
	if len(detail.Utterances) != 2 || detail.Utterances[0].Text != "第一段测试" || detail.Utterances[0].HM != "08:00:00" {
		t.Fatalf("incorrect timeline: %+v", detail.Utterances)
	}
}

func TestDailyActionOwnersStayScopedToTheirRecording(t *testing.T) {
	jobs := []*job.Job{
		{Result: &job.Result{Summary: "第一份合成材料", Segments: []job.Segment{{Speaker: "spk_0"}},
			Analysis: json.RawMessage(`{"actions":[{"speaker":"spk_0","action":"检查清单"}]}`)}},
		{Result: &job.Result{Summary: "第二份合成材料", Segments: []job.Segment{{Speaker: "spk_0"}},
			Analysis: json.RawMessage(`{"actions":[{"speaker":"spk_0","action":"核对数字"}]}`)}},
		{Result: &job.Result{Summary: "第三份合成材料", Segments: []job.Segment{{Speaker: "spk_0", PersonName: "测试负责人"}},
			Analysis: json.RawMessage(`{"actions":[{"speaker":"spk_0","action":"整理结果"}]}`)}},
	}
	r := &Report{JobCount: len(jobs)}
	for _, content := range []string{buildMaterial(r, jobs), fallbackContent(r, jobs)} {
		for _, owner := range []string{"录音1/spk_0", "录音2/spk_0", "测试负责人"} {
			if !strings.Contains(content, owner) {
				t.Errorf("missing scoped owner %s", owner)
			}
		}
	}
}

func TestReportActionsSeparateOwnersFromSourcesAndKeepConstraints(t *testing.T) {
	analysis := json.RawMessage(`{"actions":[{"speaker":"spk_0","owner":"测试乙","action":"复核9份清单","details":"周三14:00前"},{"speaker":"spk_0","action":"确认数量","details":"周五前"}],"constraints":["复核结果出来前不发货"],"risks":["实际缺4件，若周五未补齐可能延期"]}`)
	j := &job.Job{Result: &job.Result{Summary: "测试甲转达任务", Duration: 60,
		Segments: []job.Segment{{Speaker: "spk_0", PersonName: "测试甲", Text: "测试甲转达测试乙的复核安排。", End: 10}}, Analysis: analysis}}
	r := &Report{JobCount: 1}
	raw, err := buildInteractiveDetail(r, []*job.Job{j})
	if err != nil {
		t.Fatal(err)
	}
	var detail detailPayload
	if err := json.Unmarshal(raw, &detail); err != nil {
		t.Fatal(err)
	}
	if len(detail.Actions) != 2 || !strings.Contains(detail.Actions[0].Text, "负责人：测试乙") ||
		!strings.Contains(detail.Actions[0].Source, "测试甲") || strings.Contains(detail.Actions[0].Source, "测试乙") ||
		!strings.Contains(detail.Actions[1].Text, "负责人未明确") || strings.Contains(detail.Actions[1].Text, "测试甲") {
		t.Fatalf("owner and recording source were conflated: %+v", detail.Actions)
	}
	// Raw-first boundaries still exclude model candidates, including new fields.
	for _, content := range []string{buildMaterial(r, []*job.Job{j}), fallbackContent(r, []*job.Job{j})} {
		if strings.Contains(content, "复核结果出来前不发货") || strings.Contains(content, "实际缺4件") {
			t.Fatal("source-backed report included derived candidates")
		}
	}
	j.Result.Segments[0].Text = ""
	for _, content := range []string{buildMaterial(r, []*job.Job{j}), fallbackContent(r, []*job.Job{j})} {
		for _, want := range []string{"测试乙", "复核结果出来前不发货", "实际缺4件", "须核对", "测试甲"} {
			if !strings.Contains(content, want) {
				t.Fatalf("missing retained candidate %q: %s", want, content)
			}
		}
	}
	fallback := fallbackContent(r, []*job.Job{j})
	if !strings.Contains(fallback, "来源：测试甲 · 负责人：测试乙") || !strings.Contains(fallback, "负责人未明确 · 确认数量") {
		t.Fatal("fallback turned a source into an owner")
	}
	if string(j.Result.Analysis) != string(analysis) {
		t.Fatal("display changed stored source analysis")
	}
}

func TestGenerateDateCalendarWindows(t *testing.T) {
	loc := time.FixedZone("synthetic-east", 8*60*60)
	dst, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		now   time.Time
		date  string
		hours time.Duration
	}{
		{"historical full day", time.Date(2027, 4, 13, 1, 15, 0, 0, loc), "2027-04-12", 24},
		{"today until now", time.Date(2027, 4, 12, 10, 0, 0, 0, loc), "2027-04-12", 10},
		{"spring day", time.Date(2027, 3, 15, 1, 0, 0, 0, dst), "2027-03-14", 23},
		{"autumn day", time.Date(2027, 11, 8, 1, 0, 0, 0, dst), "2027-11-07", 25},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start, err := time.ParseInLocation("2006-01-02", tc.date, tc.now.Location())
			if err != nil {
				t.Fatal(err)
			}
			end := start.Add(tc.hours * time.Hour)
			var jobs []*job.Job
			for i, sample := range []struct {
				at   time.Time
				text string
			}{
				{start.Add(-time.Millisecond), "范围以前的原文"},
				{start, "当日开始的原文"},
				{end.Add(-time.Millisecond), "范围最后的原文"},
				{end, "范围结束之外的原文"},
			} {
				jobs = append(jobs, &job.Job{ID: fmt.Sprintf("synthetic-%d", i), Status: job.StatusDone,
					RecordedAt: sample.at.UnixMilli(), Result: &job.Result{Duration: 1,
						Segments: []job.Segment{{Text: sample.text}}}})
			}
			rs, js := newTestStore(t, jobs)
			reporter := &fakeReporter{content: "合成完整日报。"}
			s := NewScheduler(rs, js, reporter, filepath.Join(t.TempDir(), "settings.json"), "合成日报",
				Settings{Enabled: true, GenerateAt: "18:00"})
			s.now = func() time.Time { return tc.now }
			r, err := s.GenerateDate(context.Background(), tc.date)
			if err != nil {
				t.Fatal(err)
			}
			if r.Date != tc.date || !r.PeriodStart.Equal(start) || !r.PeriodEnd.Equal(end) ||
				r.PeriodEnd.Sub(r.PeriodStart) != tc.hours*time.Hour || r.JobCount != 2 {
				t.Fatalf("wrong calendar window: %+v", r)
			}
			for _, want := range []string{"当日开始的原文", "范围最后的原文"} {
				if !strings.Contains(reporter.material, want) {
					t.Errorf("lost in-range recording: %s", want)
				}
			}
			for _, excluded := range []string{"范围以前的原文", "范围结束之外的原文"} {
				if strings.Contains(reporter.material, excluded) {
					t.Errorf("included out-of-range recording: %s", excluded)
				}
			}
		})
	}
}

func TestGenerateDateRejectsInvalidAndFutureBeforeWriting(t *testing.T) {
	rs, js := newTestStore(t, nil)
	reporter := &fakeReporter{onGenerate: func(context.Context) { t.Fatal("invalid date reached model") }}
	s := NewScheduler(rs, js, reporter, filepath.Join(t.TempDir(), "settings.json"), "合成日报",
		Settings{Enabled: true, GenerateAt: "18:00"})
	s.now = func() time.Time { return time.Date(2027, 4, 12, 12, 0, 0, 0, time.UTC) }
	for _, date := range []string{"", " ", "2027-4-12", "2027-02-30", "2027-04-13", "2027-04-12 ", "2027-04-12T00:00:00Z"} {
		if _, err := s.GenerateDate(context.Background(), date); !errors.Is(err, ErrInvalidDate) {
			t.Errorf("date %q: got %v, want ErrInvalidDate", date, err)
		}
	}
	if len(rs.List(100)) != 0 {
		t.Fatal("invalid date created a report")
	}
	files, err := os.ReadDir(rs.dir)
	if err != nil || len(files) != 0 {
		t.Fatalf("invalid date wrote files: %v, %v", files, err)
	}
}

func TestGenerateDatePreservesExistingVersion(t *testing.T) {
	now := time.Date(2027, 4, 13, 1, 0, 0, 0, time.UTC)
	rs, js := newTestStore(t, []*job.Job{{ID: "synthetic", Status: job.StatusDone,
		RecordedAt: now.Add(-4 * time.Hour).UnixMilli(),
		Result:     &job.Result{Duration: 60, Segments: []job.Segment{{Text: "合成历史原文。"}}}}})
	reporter := &fakeReporter{content: "第一版合成内容。"}
	s := NewScheduler(rs, js, reporter, filepath.Join(t.TempDir(), "settings.json"), "合成日报",
		Settings{Enabled: true, GenerateAt: "18:00"})
	s.now = func() time.Time { return now }
	first, err := s.GenerateDate(context.Background(), "2027-04-12")
	if err != nil {
		t.Fatal(err)
	}
	oldBytes, err := os.ReadFile(filepath.Join(rs.dir, first.ID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	reporter.content = "第二版合成内容。"
	second, err := s.GenerateDate(context.Background(), "2027-04-12")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID || first.Date != second.Date || len(rs.List(100)) != 2 {
		t.Fatalf("regeneration replaced a version: first=%s second=%s", first.ID, second.ID)
	}
	stillOld, err := os.ReadFile(filepath.Join(rs.dir, first.ID+".json"))
	if err != nil || string(stillOld) != string(oldBytes) {
		t.Fatal("old persisted report changed")
	}
	reloaded, err := NewStore(rs.dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []*Report{first, second} {
		got, ok := reloaded.Get(want.ID)
		if !ok || got.Content != want.Content || got.JobCount != 1 {
			t.Fatalf("version not independently persisted: %s", want.ID)
		}
	}
}

func TestGenerateDateCancellationWhileWaitingCreatesNoVersion(t *testing.T) {
	now := time.Date(2027, 4, 13, 1, 0, 0, 0, time.UTC)
	rs, js := newTestStore(t, nil)
	s := NewScheduler(rs, js, nil, filepath.Join(t.TempDir(), "settings.json"), "合成日报",
		Settings{Enabled: true, GenerateAt: "18:00"})
	s.now = func() time.Time { return now }
	// Hold the same gate used by an active generation. A canceled queued request
	// must finish while this generation still owns the gate, without any writes.
	s.genMu <- struct{}{}
	defer func() { <-s.genMu }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := s.GenerateDate(ctx, "2027-04-12")
		done <- err
	}()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("queued cancellation = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("canceled request kept waiting for another report")
	}
	if len(rs.List(100)) != 0 {
		t.Fatal("canceled queued request created a report")
	}
}

func TestGenerateDateActiveCancellationPreservesFallback(t *testing.T) {
	now := time.Date(2027, 4, 13, 1, 0, 0, 0, time.UTC)
	rs, js := newTestStore(t, []*job.Job{{ID: "synthetic", Status: job.StatusDone,
		RecordedAt: now.Add(-4 * time.Hour).UnixMilli(),
		Result:     &job.Result{Duration: 60, Segments: []job.Segment{{Text: "取消后仍需保留的合成原文。"}}}}})
	entered := make(chan struct{})
	reporter := &fakeReporter{err: context.Canceled, onGenerate: func(ctx context.Context) {
		close(entered)
		<-ctx.Done()
	}}
	s := NewScheduler(rs, js, reporter, filepath.Join(t.TempDir(), "settings.json"), "合成日报",
		Settings{Enabled: true, GenerateAt: "18:00"})
	s.now = func() time.Time { return now }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := s.GenerateDate(ctx, "2027-04-12")
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("generation did not reach reporter")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reporter did not receive cancellation")
	}
	reports := rs.List(100)
	if len(reports) != 1 || !strings.Contains(reports[0].Error, context.Canceled.Error()) ||
		!strings.Contains(reports[0].Content, "取消后仍需保留的合成原文") {
		t.Fatalf("canceled active generation lost explicit fallback: %+v", reports)
	}
}
