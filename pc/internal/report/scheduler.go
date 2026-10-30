package report

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"warehouse-audio/internal/job"
)

type Reporter interface {
	GenerateDailyReport(ctx context.Context, material string) (string, error)
}

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

	genMu sync.Mutex
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
		// 模型挂了不能把定时器卡住：超时后生成确定性回退日报。
		genCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
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

func (s *Scheduler) generate(ctx context.Context, date string, start, end time.Time, force bool) (*Report, error) {
	s.genMu.Lock()
	defer s.genMu.Unlock()

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
	r.JobCount = len(jobs)
	r.DurationSeconds = totalDuration(jobs)
	if len(jobs) == 0 {
		r.Content = emptyContent(start, end)
		r.Summary = "这个时间段没有已分析的录音。"
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
		r.Error = "日报模型不可用，已回退为分段摘要：" + err.Error()
		content = fallbackContent(r, jobs)
	}
	r.Content = strings.TrimSpace(content)
	r.Summary = summarize(r, jobs)
	if detail, derr := buildInteractiveDetail(r, jobs); derr == nil {
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

	const maxRunes = 24000
	const summaryRunes = 16000
	used := len([]rune(b.String()))

	// 先把每段的摘要都带上，避免前面的转写把后面的段挤出预算。
	for _, j := range jobs {
		if j.Result == nil {
			continue
		}
		summary := strings.TrimSpace(j.Result.Summary)
		if summary == "" {
			continue
		}
		line := fmt.Sprintf("- %s（%.1f 分钟）%s\n",
			jobStart(j).Format("15:04"), jobDuration(j)/60, toSingleLine(summary))
		if used+len([]rune(line)) > summaryRunes {
			b.WriteString("- （其余分段摘要省略）\n")
			break
		}
		b.WriteString(line)
		used += len([]rune(line))
	}

	// 分析失败/没有摘要的段，补上转写节选，别让这些段完全消失。
	var extra strings.Builder
	extraUsed := 0
	for _, j := range jobs {
		if j.Result == nil || strings.TrimSpace(j.Result.Summary) != "" {
			continue
		}
		text := transcriptText(j.Result.Segments)
		if text == "" {
			continue
		}
		line := fmt.Sprintf("- %s：%s\n",
			jobStart(j).Format("15:04"), excerpt(text, 600))
		if used+extraUsed+len([]rune(line)) > maxRunes {
			extra.WriteString("- （其余转写省略）\n")
			break
		}
		extra.WriteString(line)
		extraUsed += len([]rune(line))
	}
	if extra.Len() > 0 {
		b.WriteString("\n补充转写：\n")
		b.WriteString(extra.String())
	}
	return b.String()
}

func toSingleLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
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
	var end time.Time
	if j.RecordedAt > 0 {
		end = time.UnixMilli(j.RecordedAt)
	} else {
		end = j.CreatedAt
	}
	if d := jobDuration(j); d > 0 {
		return end.Add(-time.Duration(d * float64(time.Second)))
	}
	return end
}

func emptyContent(start, end time.Time) string {
	return fmt.Sprintf("## 今日概览\n\n%s 到 %s 没有已分析的录音。\n\n## 待办\n\n未发现明确待办\n\n## 风险与异常\n\n未发现",
		start.Format("01-02 15:04"), end.Format("01-02 15:04"))
}

func fallbackContent(r *Report, jobs []*job.Job) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## 今日概览\n\n- 录音 %d 段，共 %d 分钟\n\n## 分段摘要\n\n",
		r.JobCount, int(r.DurationSeconds/60+0.5))
	const maxFallbackRunes = 20000
	used := len([]rune(b.String()))
	for _, j := range jobs {
		if j.Result == nil {
			continue
		}
		line := strings.TrimSpace(j.Result.Summary)
		if line == "" {
			line = excerpt(transcriptText(j.Result.Segments), 180)
		}
		if line == "" {
			continue
		}
		add := fmt.Sprintf("- %s · %s\n", jobStart(j).Format("15:04"), toSingleLine(line))
		if used+len([]rune(add)) > maxFallbackRunes {
			b.WriteString("- （其余分段省略）\n")
			break
		}
		b.WriteString(add)
		used += len([]rune(add))
	}
	b.WriteString("\n## 待办\n\n未发现明确待办\n\n## 风险与异常\n\n未发现")
	return b.String()
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
	ID      int    `json:"id"`
	Session int    `json:"session"`
	Time    string `json:"time"`
	HM      string `json:"hm"`
	Person  string `json:"person"`
	PID     string `json:"pid"`
	Speaker string `json:"speaker"`
	Topic   string `json:"topic"`
	Text    string `json:"text"`
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
}

type detailRow struct {
	detailUtterance
	when time.Time
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
	for _, j := range jobs {
		if j.Result == nil {
			continue
		}
		start := jobStart(j)
		topic := shortTopic(j.Result.Summary)
		has := false
		for _, seg := range j.Result.Segments {
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
				name = strings.TrimSpace(seg.Speaker)
			}
			if name == "" {
				name = "未确认"
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
					Time: t.Format("01-02 15:04:05"), HM: t.Format("15:04:05"),
					Person: name, Speaker: seg.Speaker, Topic: topic, Text: text,
				},
				when: t,
			})
		}
		if has {
			contentFiles++
		}
	}
	// 180 秒无有效转写视为新会话。
	sessions := make([]detailSession, 0)
	utt := make([]detailUtterance, 0, len(rows))
	session := -1
	var last time.Time
	for i := range rows {
		row := &rows[i]
		if session < 0 || row.when.Sub(last).Seconds() > 180 {
			session++
			sessions = append(sessions, detailSession{ID: session, Title: row.Topic, Topic: row.Topic})
		}
		last = row.when
		row.Session = session
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
		people = append(people, detailPerson{
			Name: n, ID: "p_" + n, Status: "已登记", Turns: counts[n],
			Talk: fmt.Sprintf("%.1f", talk[n]/60), Chars: fmt.Sprintf("%d", chars[n]),
			Topics: "本日报中的发言", Blurb: "来自已转写录音的说话人。",
		})
	}
	var totalTalk float64
	for _, v := range talk {
		totalTalk += v
	}
	payload := detailPayload{
		Version: 1,
		Stats: detailStats{
			TotalMinutes: r.DurationSeconds / 60, CandidateMinutes: r.DurationSeconds / 60,
			TalkMinutes: totalTalk / 60, Utterances: len(utt), ContentFiles: contentFiles,
			ProcessedFiles: len(jobs),
		},
		People: people, Sessions: sessions, Utterances: utt,
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
