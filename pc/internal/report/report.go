// Package report 生成并保存每日录音日报，同时记录每台手机是否已经推送过。
package report

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Status string

const (
	StatusRunning Status = "running"
	StatusDone    Status = "done"
	StatusFailed  Status = "failed"
)

// Report 是一份日报。Content 是给手机显示的 Markdown 文本，
// Summary 是通知栏用的一两句短摘要。
type Report struct {
	ID              string    `json:"id"`
	Date            string    `json:"date"` // YYYY-MM-DD
	Title           string    `json:"title"`
	Status          Status    `json:"status"`
	Stage           string    `json:"stage,omitempty"`
	Error           string    `json:"error,omitempty"`
	PeriodStart     time.Time `json:"period_start"`
	PeriodEnd       time.Time `json:"period_end"`
	JobCount        int       `json:"job_count"`
	DurationSeconds float64   `json:"duration_seconds"`
	Summary         string          `json:"summary"`
	Content         string          `json:"content"`
	Detail          json.RawMessage `json:"detail,omitempty"`
	CreatedAt       time.Time       `json:"created_at"`
	GeneratedAt     time.Time `json:"generated_at,omitempty"`
	DeliveredTo     []string  `json:"delivered_to,omitempty"`
}

type Store struct {
	mu      sync.RWMutex
	dir     string
	reports map[string]*Report
}

func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	s := &Store{dir: dir, reports: map[string]*Report{}}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		b, err := os.ReadFile(path)
		if err != nil {
			log.Printf("report: read %s: %v", path, err)
			continue
		}
		var r Report
		if err := json.Unmarshal(b, &r); err != nil || r.ID == "" {
			log.Printf("report: skip invalid %s", path)
			continue
		}
		if r.Status == StatusRunning {
			// 生成过程中服务被杀：标记失败，下一次定时检查会重新生成。
			r.Status = StatusFailed
			r.Stage = "interrupted"
			r.Error = "server restarted while generating"
			if r.CreatedAt.IsZero() {
				r.CreatedAt = time.Now()
			}
			_ = s.saveFile(&r)
		}
		s.reports[r.ID] = &r
	}
	log.Printf("report: loaded %d reports from %s", len(s.reports), dir)
	return s, nil
}

func (s *Store) Save(r *Report) error {
	if r == nil || r.ID == "" {
		return os.ErrInvalid
	}
	cp := clone(r)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reports[cp.ID] = cp
	return s.saveFile(cp)
}

func (s *Store) Get(id string) (*Report, bool) {
	s.mu.RLock()
	r, ok := s.reports[id]
	s.mu.RUnlock()
	if !ok {
		return nil, false
	}
	return clone(r), true
}

func (s *Store) Update(id string, fn func(*Report)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.reports[id]
	if !ok {
		return os.ErrNotExist
	}
	fn(r)
	return s.saveFile(r)
}

func (s *Store) List(limit int) []*Report {
	if limit <= 0 || limit > 500 {
		limit = 60
	}
	s.mu.RLock()
	out := make([]*Report, 0, len(s.reports))
	for _, r := range s.reports {
		out = append(out, clone(r))
	}
	s.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		return reportTime(out[i]).After(reportTime(out[j]))
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func (s *Store) LatestDone() (*Report, bool) {
	for _, r := range s.List(200) {
		if r.Status == StatusDone {
			return r, true
		}
	}
	return nil, false
}

// LatestForDate 返回某一天最新的一份日报（手动重新生成时可能有多份）。
func (s *Store) LatestForDate(date string) (*Report, bool) {
	for _, r := range s.List(200) {
		if r.Date == date {
			return r, true
		}
	}
	return nil, false
}

// HasDate 表示这一天的日报已经生成/正在生成，定时器不要再重复生成。
func (s *Store) HasDate(date string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, r := range s.reports {
		if r.Date == date && (r.Status == StatusDone || r.Status == StatusRunning) {
			return true
		}
	}
	return false
}

// PendingFor 返回最新一份还没推送给这台设备的日报。
// 只返回一份，避免手机关机多日后一次弹出十几条通知。
func (s *Store) PendingFor(deviceID string) []*Report {
	if deviceID == "" {
		return nil
	}
	for _, r := range s.List(200) {
		if r.Status != StatusDone || r.JobCount == 0 {
			// 空日报不推送，避免手机收到“今天没有录音”的噪音。
			// 之后如果补传了录音，定时器会重新生成有内容的日报再推。
			continue
		}
		if !contains(r.DeliveredTo, deviceID) {
			return []*Report{r}
		}
	}
	return nil
}

// Ack 标记某台设备已经收到日报。比它更早的日报也一并标记，
// 这样补推时不会按时间倒序把历史日报一条条弹出来。
func (s *Store) Ack(id, deviceID string) bool {
	if deviceID == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	target, ok := s.reports[id]
	if !ok {
		return false
	}
	t := reportTime(target)
	changed := false
	for _, r := range s.reports {
		if r.Status != StatusDone {
			continue
		}
		if r.ID != target.ID && reportTime(r).After(t) {
			continue
		}
		if !contains(r.DeliveredTo, deviceID) {
			r.DeliveredTo = append(r.DeliveredTo, deviceID)
			_ = s.saveFile(r)
			changed = true
		}
	}
	return changed || contains(target.DeliveredTo, deviceID)
}

// Retention 删除超过保留天数的日报（正在进行中的保留）。
func (s *Store) Retention(days int) int {
	if days <= 0 {
		return 0
	}
	cutoff := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := 0
	for id, r := range s.reports {
		if r.Status == StatusRunning || reportTime(r).After(cutoff) {
			continue
		}
		if err := os.Remove(filepath.Join(s.dir, id+".json")); err == nil || os.IsNotExist(err) {
			delete(s.reports, id)
			removed++
		}
	}
	return removed
}

func (s *Store) Stats() map[string]int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := map[string]int{"total": len(s.reports)}
	for _, r := range s.reports {
		out[string(r.Status)]++
	}
	return out
}

func (s *Store) NewID(t time.Time) string {
	base := "r_" + t.Format("20060102_150405")
	s.mu.RLock()
	_, exists := s.reports[base]
	s.mu.RUnlock()
	if !exists {
		return base
	}
	for i := 1; ; i++ {
		id := base + "_" + strconv.Itoa(i)
		s.mu.RLock()
		_, exists := s.reports[id]
		s.mu.RUnlock()
		if !exists {
			return id
		}
	}
}

func (s *Store) saveFile(r *Report) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(s.dir, r.ID+".json"), b, 0o600)
}

func atomicWrite(path string, b []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

func clone(r *Report) *Report {
	if r == nil {
		return nil
	}
	cp := *r
	cp.DeliveredTo = append([]string(nil), r.DeliveredTo...)
	return &cp
}

func reportTime(r *Report) time.Time {
	if !r.GeneratedAt.IsZero() {
		return r.GeneratedAt
	}
	if !r.CreatedAt.IsZero() {
		return r.CreatedAt
	}
	return r.PeriodEnd
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
