package report

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"warehouse-audio/internal/infer"
	"warehouse-audio/internal/job"
)

type Reporter interface {
	GenerateDailyReport(ctx context.Context, material string) (string, error)
}

// GenerationTimeout bounds a complete report, including all chunk summaries.
// Individual model requests retain their separately configured timeout.
const GenerationTimeout = time.Hour

// ErrInvalidDate marks an invalid or future date supplied by a manual caller.
var ErrInvalidDate = errors.New("invalid report date")

// Settings 是运行时可改的日报开关和生成时间。
// config.json 提供默认值，手机 App 修改后写到 storage.dir/report_settings.json。
type Settings struct {
	Enabled    bool   `json:"enabled"`
	GenerateAt string `json:"generate_at"`
}

type State struct {
	Enabled    bool       `json:"enabled"`
	GenerateAt string     `json:"generate_at"`
	NextRun    *time.Time `json:"next_run,omitempty"`
	Title      string     `json:"title,omitempty"`
}

type Scheduler struct {
	store        *Store
	jobs         *job.Store
	reporter     Reporter
	settingsPath string
	title        string

	mu       sync.Mutex
	settings Settings
	running  bool

	genMu chan struct{}
	now   func() time.Time
}

func NewScheduler(store *Store, jobs *job.Store, reporter Reporter, settingsPath, title string, def Settings) *Scheduler {
	if title == "" {
		title = "录音日报"
	}
	if strings.TrimSpace(settingsPath) == "" {
		settingsPath = "report_settings.json"
	}
	settings := loadSettings(settingsPath, def)
	return &Scheduler{
		store:        store,
		jobs:         jobs,
		reporter:     reporter,
		settingsPath: settingsPath,
		title:        title,
		settings:     settings,
		genMu:        make(chan struct{}, 1),
		now:          time.Now,
	}
}

func (s *Scheduler) Run(ctx context.Context) {
	s.tick(ctx)
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.tick(ctx)
		}
	}
}

func (s *Scheduler) tick(ctx context.Context) {
	s.mu.Lock()
	enabled, at, running := s.settings.Enabled, s.settings.GenerateAt, s.running
	s.mu.Unlock()
	if !enabled || running || s.jobs == nil {
		return
	}
	now := s.now()
	lastRun, err := lastRunAt(now, at)
	if err != nil {
		log.Printf("report: invalid generate_at %q: %v", at, err)
		return
	}
	// 用“最近一次应该运行的时间”算日报区间：电脑 18:00 关机、
	// 第二天早上才启动时，仍然能补出昨天 18:00 那一份。
	date, start, end, err := PeriodFor(lastRun, at)
	if err != nil {
		log.Printf("report: period: %v", err)
		return
	}
	force := false
	if s.store.HasDate(date) {
		latest, ok := s.store.LatestForDate(date)
		if !ok || latest.Status != StatusDone {
			return
		}
		// 已经覆盖到本次结束时间就不用重做；空日报、或以前只生成到
		// 18:00 而后来把时间改到 22:00 的，需要补一份更完整的。
		if latest.JobCount > 0 && !latest.PeriodEnd.Before(end) {
			return
		}
		if !s.readyToGenerate(now, lastRun, start, end) {
			return
		}
		if len(s.jobs.CompletedBetween(start.UnixMilli(), end.UnixMilli())) == 0 {
			return
		}
		force = true
	} else if !s.readyToGenerate(now, lastRun, start, end) {
		return
	}
	go func() {
		// 给全天材料的多轮汇总预留时间；总超时后保留分段材料。
		genCtx, cancel := context.WithTimeout(ctx, GenerationTimeout)
		defer cancel()
		if _, err := s.generate(genCtx, date, start, end, force); err != nil {
			log.Printf("report: generate %s: %v", date, err)
		}
	}()
}

// readyToGenerate 会等窗口内的队列跑完，避免日报只包含前几段。
// 最多等 2 小时：队列卡死时仍然生成一份现有结果，不能永远不出日报。
func (s *Scheduler) readyToGenerate(now, lastRun, start, end time.Time) bool {
	if now.Sub(lastRun) > 2*time.Hour {
		return true
	}
	return s.jobs.UnfinishedBetween(start.UnixMilli(), end.UnixMilli()) == 0
}

// GenerateNow 是手动触发入口，会等待生成完成。
func (s *Scheduler) GenerateNow(ctx context.Context) (*Report, error) {
	s.mu.Lock()
	at := s.settings.GenerateAt
	s.mu.Unlock()
	now := s.now()
	date, start, end, err := PeriodFor(now, at)
	if err != nil {
		return nil, err
	}
	return s.generate(ctx, date, start, end, true)
}

// GenerateDate creates another report for a local calendar day, preserving all
// existing versions. Historical days cover the full day; today stops at now.
func (s *Scheduler) GenerateDate(ctx context.Context, date string) (*Report, error) {
	now := s.now()
	start, err := time.ParseInLocation("2006-01-02", date, now.Location())
	if err != nil || start.Format("2006-01-02") != date {
		return nil, fmt.Errorf("%w: expected YYYY-MM-DD", ErrInvalidDate)
	}
	y, m, d := now.Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, now.Location())
	if start.After(today) {
		return nil, fmt.Errorf("%w: future dates are not allowed", ErrInvalidDate)
	}
	end := start.AddDate(0, 0, 1)
	if start.Equal(today) {
		end = now
	}
	return s.generate(ctx, date, start, end, true)
}

func (s *Scheduler) generate(ctx context.Context, date string, start, end time.Time, force bool) (*Report, error) {
	select {
	case s.genMu <- struct{}{}:
		defer func() { <-s.genMu }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if s.jobs == nil {
		return nil, fmt.Errorf("no job store")
	}
	if !force && s.store.HasDate(date) {
		return nil, nil
	}

	s.mu.Lock()
	s.running = true
	title := s.title
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
	}()

	now := s.now()
	r := &Report{
		ID:          s.store.NewID(now),
		Date:        date,
		Title:       fmt.Sprintf("%d月%d日 %s", start.Month(), start.Day(), title),
		Status:      StatusRunning,
		Stage:       "collecting",
		PeriodStart: start,
		PeriodEnd:   end,
		CreatedAt:   now,
	}
	if err := s.store.Save(r); err != nil {
		return nil, err
	}

	jobs := s.jobs.CompletedBetween(start.UnixMilli(), end.UnixMilli())
	pending := s.jobs.UnfinishedBetween(start.UnixMilli(), end.UnixMilli())
	coverageNote := ""
	if pending > 0 {
		coverageNote = fmt.Sprintf("> 本报告仅包含已完成分析的录音。生成时还有 %d 段录音正在处理，尚未纳入。\n\n", pending)
	}
	r.JobCount = len(jobs)
	r.DurationSeconds = totalDuration(jobs)
	if len(jobs) == 0 {
		r.Content = coverageNote + emptyContent(start, end)
		r.Summary = "这个时间段没有已分析的录音。"
		if pending > 0 {
			r.Summary += fmt.Sprintf("另有 %d 段录音待处理。", pending)
		}
		r.Status = StatusDone
		r.Stage = "done"
		r.GeneratedAt = s.now()
		_ = s.store.Save(r)
		return r, nil
	}

	_ = s.store.Update(r.ID, func(x *Report) {
		x.JobCount = r.JobCount
		x.DurationSeconds = r.DurationSeconds
		x.Stage = "generating"
	})

	material := buildMaterial(r, jobs)
	detailJobs := jobs
	content, err := "", error(nil)
	if s.reporter != nil {
		content, err = s.reporter.GenerateDailyReport(ctx, material)
	} else {
		err = fmt.Errorf("no report model configured")
	}
	if err != nil || strings.TrimSpace(content) == "" {
		if err == nil {
			err = fmt.Errorf("empty report")
		}
		r.Error = "日报汇总未完成，已保留分段材料：" + err.Error()
		content = fallbackContent(r, jobs)
		detailJobs = fallbackDetailJobs(jobs)
	}
	r.Content = strings.TrimSpace(content)
	r.Summary = summarize(r, jobs)
	// Counts and duration come from stored jobs, never from model arithmetic.
	r.Content = coverageNote + fmt.Sprintf("> 已分析 %d 段录音，合计 %.1f 分钟。\n\n", r.JobCount, r.DurationSeconds/60) + r.Content
	if pending > 0 {
		r.Summary += fmt.Sprintf("另有 %d 段录音待处理。", pending)
	}
	if detail, derr := buildInteractiveDetail(r, detailJobs); derr == nil {
		r.Detail = detail
	} else {
		log.Printf("report: interactive detail: %v", derr)
	}
	r.Status = StatusDone
	r.Stage = "done"
	r.GeneratedAt = s.now()
	if err := s.store.Save(r); err != nil {
		return nil, err
	}
	log.Printf("report: %s done (%d jobs, %.0fs)", r.Date, r.JobCount, r.DurationSeconds)
	return r, nil
}

func (s *Scheduler) State() State {
	s.mu.Lock()
	st := State{Enabled: s.settings.Enabled, GenerateAt: s.settings.GenerateAt, Title: s.title}
	s.mu.Unlock()
	now := s.now()
	if st.Enabled {
		at, err := NormalizeClock(st.GenerateAt)
		if err == nil {
			lastRun, lerr := lastRunAt(now, at)
			date, _, _, perr := PeriodFor(lastRun, at)
			if lerr == nil && perr == nil {
				next := lastRun.AddDate(0, 0, 1)
				if !s.store.HasDate(date) {
					next = now
				}
				st.NextRun = &next
			}
		}
	}
	return st
}

func (s *Scheduler) Update(enabled bool, generateAt string) error {
	at, err := NormalizeClock(generateAt)
	if err != nil {
		return err
	}
	next := Settings{Enabled: enabled, GenerateAt: at}
	if err := saveSettings(s.settingsPath, next); err != nil {
		return err
	}
	s.mu.Lock()
	s.settings = next
	s.mu.Unlock()
	log.Printf("report: schedule updated enabled=%v at=%s", enabled, at)
	return nil
}

func loadSettings(path string, def Settings) Settings {
	at, err := NormalizeClock(def.GenerateAt)
	if err != nil {
		log.Printf("report: invalid default generate_at %q, using 18:00", def.GenerateAt)
		at = "18:00"
	}
	out := Settings{Enabled: def.Enabled, GenerateAt: at}
	b, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	var s Settings
	if err := json.Unmarshal(b, &s); err != nil {
		log.Printf("report: invalid settings %s: %v", path, err)
		return out
	}
	if strings.TrimSpace(s.GenerateAt) == "" {
		s.GenerateAt = out.GenerateAt
	} else if v, err := NormalizeClock(s.GenerateAt); err == nil {
		s.GenerateAt = v
	} else {
		log.Printf("report: invalid stored generate_at %q, using %s", s.GenerateAt, out.GenerateAt)
		s.GenerateAt = out.GenerateAt
	}
	return s
}

func saveSettings(path string, s Settings) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return atomicWrite(path, b, 0o600)
}

// NormalizeClock 接受 8:00 / 08:00 两种写法，统一成 HH:MM。
func NormalizeClock(v string) (string, error) {
	v = strings.TrimSpace(v)
	if len(v) == 4 && v[1] == ':' {
		v = "0" + v
	}
	t, err := time.Parse("15:04", v)
	if err != nil {
		return "", fmt.Errorf("时间格式应为 HH:MM")
	}
	return t.Format("15:04"), nil
}

// PeriodFor 决定这次日报覆盖哪段时间：
// 上午生成的日报总结前一天；中午以后生成的日报总结当天 00:00 到现在。
func PeriodFor(now time.Time, generateAt string) (string, time.Time, time.Time, error) {
	at, err := NormalizeClock(generateAt)
	if err != nil {
		return "", time.Time{}, time.Time{}, err
	}
	h, _ := strconv.Atoi(at[:2])
	loc := now.Location()
	y, m, d := now.Date()
	startOfToday := time.Date(y, m, d, 0, 0, 0, 0, loc)
	if h < 12 {
		end := startOfToday
		start := end.AddDate(0, 0, -1)
		return start.Format("2006-01-02"), start, end, nil
	}
	return startOfToday.Format("2006-01-02"), startOfToday, now, nil
}

func scheduledAt(now time.Time, generateAt string) (time.Time, error) {
	at, err := NormalizeClock(generateAt)
	if err != nil {
		return time.Time{}, err
	}
	h, _ := strconv.Atoi(at[:2])
	m, _ := strconv.Atoi(at[3:])
	y, mo, d := now.Date()
	return time.Date(y, mo, d, h, m, 0, 0, now.Location()), nil
}

// lastRunAt 返回最近一次已经到点的生成时间（今天或昨天）。
func lastRunAt(now time.Time, generateAt string) (time.Time, error) {
	today, err := scheduledAt(now, generateAt)
	if err != nil {
		return time.Time{}, err
	}
	if now.Before(today) {
		return today.AddDate(0, 0, -1), nil
	}
	return today, nil
}

func totalDuration(jobs []*job.Job) float64 {
	var total float64
	for _, j := range jobs {
		total += jobDuration(j)
	}
	return total
}

func jobDuration(j *job.Job) float64 {
	if j.Result == nil {
		return 0
	}
	if j.Result.Duration > 0 {
		return j.Result.Duration
	}
	var end float64
	for _, s := range j.Result.Segments {
		if s.End > end {
			end = s.End
		}
	}
	return end
}

func buildMaterial(r *Report, jobs []*job.Job) string {
	var b strings.Builder
	fmt.Fprintf(&b, "日期：%s\n时间段：%s - %s\n录音：%d 段，共 %d 分钟\n\n",
		r.Date, r.PeriodStart.Format("01-02 15:04"), r.PeriodEnd.Format("01-02 15:04"),
		r.JobCount, int(r.DurationSeconds/60+0.5))

	b.WriteString("说明：以下仅为本时间段已完成分析的录音材料与统计，不代表手机上全部录音。未命名的说话人编号只在各自分段内有效，不能据此认定跨段是同一人。\n")
	b.WriteString("来源限制：录音可能包含背景播放、引用或抒情内容，不能据第一人称判断是录音者本人的经历、承诺或心理状态。分段摘要和风险字段也可能包含模型归纳；来源未确认的心理、情绪解读不是已发生风险，只描述材料字面内容，保留原文明示的实际事件与具体风险。\n")
	b.WriteString("证据优先级：有原始转写的录音仅提供完整原文；只有原文缺失时才提供自动摘要及待办、风险候选作为参考，不得直接当作已确认事实。\n")
	// 交给 Reporter 分块归并，不能在这里提前截掉一天后半段的材料。
	for i, j := range jobs {
		if j.Result == nil {
			continue
		}
		fmt.Fprintf(&b, "\n### 录音 %d · %s（%.1f 分钟）\n", i+1,
			jobStart(j).Format("01-02 15:04:05"), jobDuration(j)/60)
		transcript := materialTranscriptWithAudio(j, i, true)
		if transcript != "" {
			fmt.Fprintf(&b, "原始转写（主证据）：\n%s\n", transcript)
			// Earlier model candidates can contaminate a fresh report even when
			// labelled uncertain. With source text available, only send the source.
			continue
		}
		b.WriteString("原始转写缺失：以下自动分析仅供参考，无法据此核实为原文事实。\n")
		if summary := strings.TrimSpace(j.Result.Summary); summary != "" {
			fmt.Fprintf(&b, "自动摘要（原始转写缺失，仅供参考）：%s\n", summary)
		}
		facts := segmentFacts(j)
		for k := range facts.Actions {
			facts.Actions[k].Speaker = actionSpeaker(j, i, facts.Actions[k].Speaker)
		}
		if len(facts.Actions) > 0 {
			data, _ := json.Marshal(facts.Actions)
			fmt.Fprintf(&b, "分段模型提取的待办候选（须核对原文，不代表已确认）：%s\n", data)
		}
		if len(facts.Risks) > 0 {
			data, _ := json.Marshal(facts.Risks)
			fmt.Fprintf(&b, "分段模型提取的风险候选（须核对原文，不代表已确认）：%s\n", data)
		}
		if len(facts.Constraints) > 0 {
			data, _ := json.Marshal(facts.Constraints)
			fmt.Fprintf(&b, "分段模型提取的限制候选（须核对原文，不代表已确认）：%s\n", data)
		}
	}
	return b.String()
}

// Keep source information beside each utterance so report chunking cannot
// turn local speaker numbers into identities shared across recordings.
func materialTranscript(j *job.Job, index int) string {
	return materialTranscriptWithAudio(j, index, false)
}

func materialTranscriptWithAudio(j *job.Job, index int, includeAudio bool) string {
	parts := make([]string, 0, len(j.Result.Segments))
	var events []infer.AudioEvent
	if includeAudio {
		for _, event := range j.Result.AudioEvents {
			converted := infer.AudioEvent{Start: event.Start, End: event.End}
			for _, tag := range event.Tags {
				converted.Tags = append(converted.Tags, infer.AudioEventTag{Label: tag.Label, Score: tag.Score})
			}
			events = append(events, converted)
		}
	}
	start := jobStart(j)
	for _, seg := range j.Result.Segments {
		text := strings.TrimSpace(seg.Text)
		if text == "" {
			continue
		}
		if labels := infer.AudioLabelsForRange(events, seg.Start, seg.End); len(labels) > 0 {
			parts = append(parts, "自动声音线索：对应下方原文时段，仅辅助，非场景证明；"+strings.Join(labels, "、"))
		}
		parts = append(parts, fmt.Sprintf("[%s] %s", utteranceSourceLabel(start, index, seg), text))
	}
	return strings.Join(parts, "\n")
}

// The label is the same in evidence material and downloaded detail. It is a
// display label, not a unique key: equal timestamps/names can legitimately collide.
func utteranceSourceLabel(start time.Time, recordingIndex int, seg job.Segment) string {
	name := strings.TrimSpace(seg.PersonName)
	if name == "" {
		name = strings.TrimSpace(seg.Person)
	}
	if name == "" {
		speaker := strings.TrimSpace(seg.Speaker)
		if speaker == "" {
			speaker = "未确认"
		}
		name = fmt.Sprintf("录音%d/%s", recordingIndex+1, speaker)
	}
	at := start.Add(time.Duration(seg.Start * float64(time.Second)))
	return fmt.Sprintf("%s · %s", at.Format("01-02 15:04:05.000"), name)
}

type recordedFacts struct {
	Actions []struct {
		Speaker string `json:"speaker"`
		Owner   string `json:"owner,omitempty"`
		Action  string `json:"action"`
		Details string `json:"details"`
	} `json:"actions"`
	Risks       []string `json:"risks"`
	Constraints []string `json:"constraints"`
}

func segmentFacts(j *job.Job) recordedFacts {
	var facts recordedFacts
	if j.Result != nil {
		_ = json.Unmarshal(j.Result.Analysis, &facts)
	}
	return facts
}

// 未命名的局部编号必须附带来源，避免后续分块把同号说话人合并为同一人。
func actionSpeaker(j *job.Job, index int, speaker string) string {
	speaker = strings.TrimSpace(speaker)
	if speaker == "" {
		return "未明确"
	}
	if j.Result != nil {
		for _, seg := range j.Result.Segments {
			if seg.Speaker == speaker {
				if name := strings.TrimSpace(seg.PersonName); name != "" {
					return name
				}
				if name := strings.TrimSpace(seg.Person); name != "" {
					return name
				}
				return fmt.Sprintf("录音%d/%s", index+1, speaker)
			}
		}
	}
	if strings.HasPrefix(speaker, "spk") || strings.HasPrefix(speaker, "说话人") {
		return fmt.Sprintf("录音%d/%s", index+1, speaker)
	}
	return speaker
}

func toSingleLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func actionDescription(owner, action, details string) string {
	text := strings.TrimSpace(action + " " + details)
	if owner = strings.TrimSpace(owner); owner != "" {
		return "负责人：" + owner + " · " + text
	}
	return "负责人未明确 · " + text
}

func transcriptText(segs []job.Segment) string {
	parts := make([]string, 0, len(segs))
	for _, s := range segs {
		t := strings.TrimSpace(s.Text)
		if t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, " ")
}

func excerpt(s string, max int) string {
	rs := []rune(strings.TrimSpace(s))
	if len(rs) <= max {
		return string(rs)
	}
	return string(rs[:max]) + "…"
}

func jobStart(j *job.Job) time.Time {
	return job.RecordingStart(j)
}

func emptyContent(start, end time.Time) string {
	return fmt.Sprintf("## 今日概览\n\n%s 到 %s 没有已分析的录音。\n\n## 待办\n\n未发现明确待办\n\n## 风险与异常\n\n未发现",
		start.Format("01-02 15:04"), end.Format("01-02 15:04"))
}

func fallbackContent(r *Report, jobs []*job.Job) string {
	var b strings.Builder
	b.WriteString("> 本次日报汇总未完成，以下为已保留的分段材料。有原文的录音展示完整转写；原文缺失时的自动摘要、待办和风险候选需核对，不代表已确认。\n\n")
	fmt.Fprintf(&b, "## 今日概览\n\n- 录音 %d 段，共 %d 分钟\n\n## 分段材料\n\n",
		r.JobCount, int(r.DurationSeconds/60+0.5))
	var actions, risks, constraints []string
	for i, j := range jobs {
		if j.Result == nil {
			continue
		}
		when := jobStart(j).Format("01-02 15:04:05")
		fmt.Fprintf(&b, "### 录音 %d · %s（%.1f 分钟）\n\n", i+1, when, jobDuration(j)/60)
		if transcript := materialTranscript(j, i); transcript != "" {
			fmt.Fprintf(&b, "原始转写：\n\n%s\n\n", transcript)
			// A failed synthesis must not reintroduce old model inferences that
			// were deliberately excluded from the source-backed report input.
			continue
		}
		b.WriteString("> 原始转写缺失，以下自动分析需核对原文，不代表已确认。\n\n")
		if summary := strings.TrimSpace(j.Result.Summary); summary != "" {
			fmt.Fprintf(&b, "自动摘要（需核对原文）：%s\n\n", summary)
		}
		source := fmt.Sprintf("录音 %d · %s", i+1, when)
		facts := segmentFacts(j)
		for _, a := range facts.Actions {
			if strings.TrimSpace(a.Action) != "" {
				actions = append(actions, fmt.Sprintf("- %s · 来源：%s · %s", source,
					toSingleLine(actionSpeaker(j, i, a.Speaker)), toSingleLine(actionDescription(a.Owner, a.Action, a.Details))))
			}
		}
		for _, risk := range facts.Risks {
			if strings.TrimSpace(risk) != "" {
				risks = append(risks, "- "+source+" · "+toSingleLine(risk))
			}
		}
		for _, constraint := range facts.Constraints {
			if strings.TrimSpace(constraint) != "" {
				constraints = append(constraints, "- "+source+" · "+toSingleLine(constraint))
			}
		}
	}
	if len(actions) > 0 {
		b.WriteString("\n## 分段待办候选（原文缺失，须核对，不代表已确认）\n\n")
		b.WriteString(strings.Join(actions, "\n"))
	}
	if len(risks) > 0 {
		b.WriteString("\n\n## 分段风险候选（原文缺失，须核对，不代表已确认）\n\n")
		b.WriteString(strings.Join(risks, "\n"))
	}
	if len(constraints) > 0 {
		b.WriteString("\n\n## 分段限制候选（原文缺失，须核对，不代表已确认）\n\n")
		b.WriteString(strings.Join(constraints, "\n"))
	}
	return b.String()
}

// Fallback pages must not reintroduce old model topics or actions beside the
// source-only body. Use display copies; preserve original stored analysis.
func fallbackDetailJobs(jobs []*job.Job) []*job.Job {
	out := append([]*job.Job(nil), jobs...)
	for i, j := range jobs {
		if j == nil || j.Result == nil || transcriptText(j.Result.Segments) == "" {
			continue
		}
		copyJob := *j
		copyResult := *j.Result
		copyResult.Summary = ""
		copyResult.Analysis = nil
		copyJob.Result = &copyResult
		out[i] = &copyJob
	}
	return out
}

func summarize(r *Report, jobs []*job.Job) string {
	for _, line := range strings.Split(r.Content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimLeft(line, "-*•> ")
		line = strings.TrimSpace(strings.ReplaceAll(line, "**", ""))
		if line == "" {
			continue
		}
		return excerpt(line, 120)
	}
	if r.JobCount > 0 {
		return fmt.Sprintf("今日 %d 段录音，共 %d 分钟", r.JobCount, int(r.DurationSeconds/60+0.5))
	}
	return "今日没有已分析的录音"
}

// buildInteractiveDetail 生成手机端可点击的时间线/人物/上下文数据。
// 主题优先使用每段已生成的分段摘要；没有摘要时用“对话”。
type detailUtterance struct {
	RecordingIndex int    `json:"recording_index"` // One-based position in this report.
	SegmentIndex   int    `json:"segment_index"`   // Zero-based original segment, including empty entries.
	StartMS        int64  `json:"start_ms"`        // Offset within this recording.
	SourceLabel    string `json:"source_label"`
	ID             int    `json:"id"`
	Session        int    `json:"session"`
	Time           string `json:"time"`
	HM             string `json:"hm"`
	Person         string `json:"person"`
	PID            string `json:"pid"`
	Speaker        string `json:"speaker"`
	Topic          string `json:"topic"`
	Text           string `json:"text"`
}

type detailPerson struct {
	Name   string `json:"name"`
	ID     string `json:"id"`
	Status string `json:"status"`
	Turns  int    `json:"turns"`
	Talk   string `json:"talk"`
	Chars  string `json:"chars"`
	Topics string `json:"topics"`
	Blurb  string `json:"blurb"`
}

type detailSession struct {
	ID     int      `json:"id"`
	Start  string   `json:"start"`
	End    string   `json:"end"`
	Title  string   `json:"title"`
	Topic  string   `json:"topic"`
	People []string `json:"people"`
	Turns  int      `json:"turns"`
}

type detailStats struct {
	TotalFiles       int     `json:"total_files"`
	TotalMinutes     float64 `json:"total_minutes"`
	CandidateMinutes float64 `json:"candidate_minutes"`
	TalkMinutes      float64 `json:"talk_minutes"`
	Utterances       int     `json:"utterances"`
	ContentFiles     int     `json:"content_files"`
	ProcessedFiles   int     `json:"processed_files"`
}

type detailPayload struct {
	Version    int               `json:"version"`
	Stats      detailStats       `json:"stats"`
	People     []detailPerson    `json:"people"`
	Sessions   []detailSession   `json:"sessions"`
	Utterances []detailUtterance `json:"utterances"`
	Actions    []detailAction    `json:"actions,omitempty"`
}

type detailAction struct {
	FirstUtteranceID int    `json:"first_utterance_id"` // Recording start, not inferred action evidence.
	Text             string `json:"text"`
	Source           string `json:"source"`
	Session          int    `json:"session"`
}

type detailRow struct {
	detailUtterance
	when     time.Time
	jobIndex int
}

func shortTopic(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if s == "" {
		return "对话"
	}
	r := []rune(s)
	if len(r) > 24 {
		s = string(r[:24]) + "…"
	}
	return s
}

func buildInteractiveDetail(r *Report, jobs []*job.Job) (json.RawMessage, error) {
	rows := make([]detailRow, 0)
	counts := map[string]int{}
	chars := map[string]int{}
	talk := map[string]float64{}
	contentFiles := 0
	for jobIndex, j := range jobs {
		if j.Result == nil {
			continue
		}
		start := jobStart(j)
		topic := shortTopic(j.Result.Summary)
		has := false
		for segmentIndex, seg := range j.Result.Segments {
			text := strings.TrimSpace(seg.Text)
			if text == "" {
				continue
			}
			has = true
			name := strings.TrimSpace(seg.PersonName)
			if name == "" {
				name = strings.TrimSpace(seg.Person)
			}
			if name == "" {
				label := strings.TrimSpace(seg.Speaker)
				if label == "" {
					label = "说话人"
				}
				name = fmt.Sprintf("未确认·片段%d/%s", jobIndex+1, label)
			}
			t := start.Add(time.Duration(seg.Start * float64(time.Second)))
			d := seg.End - seg.Start
			if d < 0 {
				d = 0
			}
			counts[name]++
			chars[name] += len([]rune(text))
			talk[name] += d
			rows = append(rows, detailRow{
				detailUtterance: detailUtterance{
					RecordingIndex: jobIndex + 1, SegmentIndex: segmentIndex,
					StartMS:     int64(time.Duration(seg.Start*float64(time.Second)) / time.Millisecond),
					SourceLabel: utteranceSourceLabel(start, jobIndex, seg),
					Time:        t.Format("01-02 15:04:05"), HM: t.Format("15:04:05"),
					Person: name, Speaker: seg.Speaker, Topic: topic, Text: text,
				},
				when: t, jobIndex: jobIndex,
			})
		}
		if has {
			contentFiles++
		}
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].when.Before(rows[j].when) })
	// 180 秒无有效转写视为新会话。
	sessions := make([]detailSession, 0)
	utt := make([]detailUtterance, 0, len(rows))
	session := -1
	jobSessions := map[int]int{}
	jobFirstUtterances := map[int]int{}
	var last time.Time
	for i := range rows {
		row := &rows[i]
		if session < 0 || row.when.Sub(last).Seconds() > 180 {
			session++
			sessions = append(sessions, detailSession{ID: session, Title: row.Topic, Topic: row.Topic})
		}
		last = row.when
		row.Session = session
		if _, exists := jobSessions[row.jobIndex]; !exists {
			jobSessions[row.jobIndex] = session
			jobFirstUtterances[row.jobIndex] = i
		}
		row.ID = i
		utt = append(utt, row.detailUtterance)
		sessions[session].Turns++
		sessions[session].End = row.when.Format("15:04")
		if sessions[session].Start == "" {
			sessions[session].Start = row.when.Format("01-02 15:04")
		}
		if !containsString(sessions[session].People, row.Person) {
			sessions[session].People = append(sessions[session].People, row.Person)
		}
	}
	// People，按出现顺序 A/B/C 的通常顺序做稳定排序；未知放后面。
	order := map[string]int{"A": 0, "B": 1, "C": 2}
	names := make([]string, 0, len(counts))
	for n := range counts {
		names = append(names, n)
	}
	for i := 0; i < len(names); i++ {
		for k := i + 1; k < len(names); k++ {
			oi, ok := order[names[i]]
			if !ok {
				oi = 99
			}
			kj, ok2 := order[names[k]]
			if !ok2 {
				kj = 99
			}
			if kj < oi || (kj == oi && names[k] < names[i]) {
				names[i], names[k] = names[k], names[i]
			}
		}
	}
	people := make([]detailPerson, 0, len(names))
	for _, n := range names {
		status := "已命名"
		if strings.HasPrefix(n, "未确认·") {
			status = "未确认"
		}
		people = append(people, detailPerson{
			Name: n, ID: "p_" + n, Status: status, Turns: counts[n],
			Talk: fmt.Sprintf("%.1f", talk[n]/60), Chars: fmt.Sprintf("%d", chars[n]),
			Topics: "本日报中的发言", Blurb: "来自已转写录音的说话人。",
		})
	}
	var totalTalk float64
	for _, v := range talk {
		totalTalk += v
	}
	actions := make([]detailAction, 0)
	for i, j := range jobs {
		sid, ok := jobSessions[i]
		if !ok {
			continue
		}
		for _, action := range segmentFacts(j).Actions {
			if strings.TrimSpace(action.Action) == "" {
				continue
			}
			actions = append(actions, detailAction{
				FirstUtteranceID: jobFirstUtterances[i],
				Text:             actionDescription(action.Owner, action.Action, action.Details),
				Source:           fmt.Sprintf("回看录音 %d 起点 · %s · %s", i+1, jobStart(j).Format("15:04"), actionSpeaker(j, i, action.Speaker)),
				Session:          sid,
			})
		}
	}
	payload := detailPayload{
		Version: 2,
		Stats: detailStats{
			TotalMinutes: r.DurationSeconds / 60, CandidateMinutes: r.DurationSeconds / 60,
			TalkMinutes: totalTalk / 60, Utterances: len(utt), ContentFiles: contentFiles,
			ProcessedFiles: len(jobs),
		},
		People: people, Sessions: sessions, Utterances: utt, Actions: actions,
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(b), nil
}

func containsString(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
