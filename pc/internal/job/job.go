package job

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type Status string

const (
	StatusQueued  Status = "queued"
	StatusRunning Status = "running"
	StatusDone    Status = "done"
	StatusFailed  Status = "failed"
)

type Segment struct {
	Start       float64 `json:"start"`
	End         float64 `json:"end"`
	Speaker     string  `json:"speaker"`
	Person      string  `json:"person,omitempty"`
	PersonName  string  `json:"person_name,omitempty"`
	Text        string  `json:"text"`
	SourceJobID string  `json:"source_job_id,omitempty"`
}

// SourceSpan 记录合并任务里每一段来源音频在合并文件中的位置。
// 合并后重新跑一次 ASR/CAM++，说话人编号在整段内统一；
// 需要回溯到某一段时，用 offset 把合并时间戳映射回来源任务。
type SourceSpan struct {
	JobID       string  `json:"job_id"`
	SegmentName string  `json:"segment_name,omitempty"`
	Offset      float64 `json:"offset"`
	Duration    float64 `json:"duration"`
}

type Result struct {
	JobID    string            `json:"job_id"`
	Kind     string            `json:"kind,omitempty"` // segment | merge
	Mode     string            `json:"mode"`
	Duration float64           `json:"duration"`
	Segments []Segment         `json:"segments"`
	Sources  []SourceSpan      `json:"sources,omitempty"`
	Summary  string            `json:"summary"`
	Error    string            `json:"error,omitempty"`
	Analysis json.RawMessage   `json:"analysis,omitempty"`
	Backend  map[string]string `json:"backend,omitempty"`
}

type Job struct {
	ID             string       `json:"id"`
	Kind           string       `json:"kind,omitempty"`       // segment（默认）| merge
	MergeMode      string       `json:"merge_mode,omitempty"` // transcript | audio
	Status         Status       `json:"status"`
	Stage          string       `json:"stage,omitempty"`
	Error          string       `json:"error,omitempty"`
	AudioPath      string       `json:"audio_path,omitempty"`
	AudioDeleted   bool         `json:"audio_deleted,omitempty"`
	DeviceID       string       `json:"device_id,omitempty"`
	DeviceName     string       `json:"device_name,omitempty"`
	RecordedAt     int64        `json:"recorded_at,omitempty"` // epoch ms
	SegmentName    string       `json:"segment_name,omitempty"`
	Gaps           string       `json:"gaps,omitempty"`
	SourceJobIDs   []string     `json:"source_job_ids,omitempty"`
	SourceSpans    []SourceSpan `json:"source_spans,omitempty"`
	IdempotencyKey string       `json:"idempotency_key,omitempty"`
	Attempts       int          `json:"attempts,omitempty"`
	CreatedAt      time.Time    `json:"created_at"`
	UpdatedAt      time.Time    `json:"updated_at"`
	Result         *Result      `json:"result,omitempty"`
}

type Store struct {
	mu    sync.RWMutex
	jobs  map[string]*Job
	idem  map[string]string
	dir   string
	dirty bool
}

func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(dir, "jobs"), 0o755); err != nil {
		return nil, err
	}
	s := &Store{
		jobs: map[string]*Job{},
		idem: map[string]string{},
		dir:  dir,
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

// load 从磁盘恢复任务。以前 NewStore 只建空 map，PC 一重启所有 job 都 404。
func (s *Store) load() error {
	entries, err := os.ReadDir(filepath.Join(s.dir, "jobs"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(s.dir, "jobs", e.Name())
		b, err := os.ReadFile(path)
		if err != nil {
			log.Printf("job: read %s: %v", path, err)
			continue
		}
		var j Job
		if err := json.Unmarshal(b, &j); err != nil {
			log.Printf("job: skip corrupt %s: %v", path, err)
			continue
		}
		if j.ID == "" {
			continue
		}
		if j.Status == StatusRunning {
			// 服务重启时还在 running 的任务：前两次自动重跑，超过就标记失败，
			// 避免永远卡在 running。
			j.Attempts++
			if j.Attempts <= 2 {
				j.Status = StatusQueued
				j.Stage = "requeued"
				j.Error = "server restarted while running; requeued"
			} else {
				j.Status = StatusFailed
				j.Stage = "failed"
				j.Error = "server restarted repeatedly while running"
			}
		}
		s.jobs[j.ID] = &j
		if j.IdempotencyKey != "" {
			s.idem[j.IdempotencyKey] = j.ID
		}
	}
	s.dirty = true
	s.flushLocked()
	log.Printf("job: loaded %d jobs from %s", len(s.jobs), s.dir)
	return nil
}

func (s *Store) Create(id, audioPath string) *Job {
	j, _ := s.CreateFull(&Job{ID: id, AudioPath: audioPath})
	return j
}

// CreateFull 保存新任务；如果幂等键已存在，返回已有任务和 false。
func (s *Store) CreateFull(j *Job) (*Job, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if j.IdempotencyKey != "" {
		if id, ok := s.idem[j.IdempotencyKey]; ok {
			if old, ok := s.jobs[id]; ok {
				return cloneJob(old), false
			}
		}
	}
	now := time.Now()
	if j.CreatedAt.IsZero() {
		j.CreatedAt = now
	}
	j.UpdatedAt = now
	if j.Status == "" {
		j.Status = StatusQueued
	}
	if j.Stage == "" {
		j.Stage = "queued"
	}
	s.jobs[j.ID] = j
	if j.IdempotencyKey != "" {
		s.idem[j.IdempotencyKey] = j.ID
	}
	if err := s.saveLocked(j); err != nil {
		log.Printf("job: save %s: %v", j.ID, err)
	}
	return cloneJob(j), true
}

func (s *Store) Get(id string) (*Job, bool) {
	s.mu.RLock()
	j, ok := s.jobs[id]
	s.mu.RUnlock()
	if !ok {
		return nil, false
	}
	return cloneJob(j), true
}

// Delete 删除任务和音频（队列满/上传校验失败时回滚用）。
func (s *Store) Delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	if !ok {
		return
	}
	if j.AudioPath != "" {
		_ = os.Remove(j.AudioPath)
	}
	if j.IdempotencyKey != "" {
		delete(s.idem, j.IdempotencyKey)
	}
	delete(s.jobs, id)
	_ = os.Remove(filepath.Join(s.dir, "jobs", id+".json"))
}

func (s *Store) FindIdem(key string) (*Job, bool) {
	if key == "" {
		return nil, false
	}
	s.mu.RLock()
	id, ok := s.idem[key]
	if !ok {
		s.mu.RUnlock()
		return nil, false
	}
	j := s.jobs[id]
	s.mu.RUnlock()
	if j == nil {
		return nil, false
	}
	return cloneJob(j), true
}

func (s *Store) Update(id string, fn func(*Job)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	if !ok {
		return os.ErrNotExist
	}
	fn(j)
	j.UpdatedAt = time.Now()
	return s.saveLocked(j)
}

func (s *Store) List(limit int, query string) []*Job {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := strings.ToLower(strings.TrimSpace(query))
	s.mu.RLock()
	out := make([]*Job, 0, len(s.jobs))
	for _, j := range s.jobs {
		if q != "" && !jobMatches(j, q) {
			continue
		}
		out = append(out, cloneJob(j))
	}
	s.mu.RUnlock()
	sort.Slice(out, func(i, k int) bool {
		return out[i].CreatedAt.After(out[k].CreatedAt)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func jobMatches(j *Job, q string) bool {
	if strings.Contains(strings.ToLower(j.ID), q) ||
		strings.Contains(strings.ToLower(j.DeviceName), q) ||
		strings.Contains(strings.ToLower(j.SegmentName), q) ||
		strings.Contains(strings.ToLower(j.Error), q) {
		return true
	}
	if j.Result == nil {
		return false
	}
	if strings.Contains(strings.ToLower(j.Result.Summary), q) {
		return true
	}
	for _, s := range j.Result.Segments {
		if strings.Contains(strings.ToLower(s.Text), q) ||
			strings.Contains(strings.ToLower(s.Speaker), q) {
			return true
		}
	}
	return false
}

// CompletedBetween 返回时间窗口内已经分析过的分段任务（用于生成日报）。
// 合并任务会重复包含来源分段的内容，因此排除；只保留有转写结果的任务。
func (s *Store) CompletedBetween(startMs, endMs int64) []*Job {
	s.mu.RLock()
	out := make([]*Job, 0)
	for _, j := range s.jobs {
		if j.Kind == "merge" {
			continue
		}
		if j.Status != StatusDone && j.Status != StatusFailed {
			continue
		}
		if j.Result == nil || len(j.Result.Segments) == 0 {
			continue
		}
		ts := j.RecordedAt
		if ts == 0 {
			ts = j.CreatedAt.UnixMilli()
		}
		if ts < startMs || ts >= endMs {
			continue
		}
		out = append(out, cloneJob(j))
	}
	s.mu.RUnlock()
	sort.Slice(out, func(i, k int) bool {
		ti, tk := out[i].RecordedAt, out[k].RecordedAt
		if ti == 0 {
			ti = out[i].CreatedAt.UnixMilli()
		}
		if tk == 0 {
			tk = out[k].CreatedAt.UnixMilli()
		}
		return ti < tk
	})
	return out
}

// UnfinishedBetween 返回时间窗口内还没分析完的分段数量。
// 日报生成前用它判断是否要等队列跑完，避免只把前几段写进日报。
func (s *Store) UnfinishedBetween(startMs, endMs int64) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := 0
	for _, j := range s.jobs {
		if j.Kind == "merge" || (j.Status != StatusQueued && j.Status != StatusRunning) {
			continue
		}
		ts := j.RecordedAt
		if ts == 0 {
			ts = j.CreatedAt.UnixMilli()
		}
		if ts >= startMs && ts < endMs {
			n++
		}
	}
	return n
}

// Queued 返回还没跑的任务（服务重启恢复用）。
func (s *Store) Queued(limit int) []*Job {
	if limit <= 0 {
		limit = 100
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Job, 0)
	for _, j := range s.jobs {
		if j.Status == StatusQueued {
			out = append(out, cloneJob(j))
		}
	}
	sort.Slice(out, func(i, k int) bool {
		return out[i].CreatedAt.Before(out[k].CreatedAt)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// Retention 删除过期音频和过期任务。audioDays<=0 / jobDays<=0 表示不清理。
func (s *Store) Retention(audioDays, jobDays int) (int, int) {
	now := time.Now()
	deletedAudio, deletedJobs := 0, 0

	s.mu.Lock()
	defer s.mu.Unlock()
	for id, j := range s.jobs {
		if audioDays > 0 && !j.AudioDeleted && j.AudioPath != "" {
			if st, err := os.Stat(j.AudioPath); err == nil &&
				now.Sub(st.ModTime()) > time.Duration(audioDays)*24*time.Hour {
				if err := os.Remove(j.AudioPath); err == nil || os.IsNotExist(err) {
					j.AudioDeleted = true
					j.UpdatedAt = now
					if err := s.saveLocked(j); err != nil {
						log.Printf("job: save %s: %v", id, err)
					}
					deletedAudio++
				}
			}
		}
		if jobDays > 0 && now.Sub(j.CreatedAt) > time.Duration(jobDays)*24*time.Hour &&
			(j.Status == StatusDone || j.Status == StatusFailed) {
			//noinspection GoUnhandledErrorResult
			os.Remove(j.AudioPath)
			//noinspection GoUnhandledErrorResult
			os.Remove(filepath.Join(s.dir, "jobs", id+".json"))
			if j.IdempotencyKey != "" {
				delete(s.idem, j.IdempotencyKey)
			}
			delete(s.jobs, id)
			deletedJobs++
		}
	}
	return deletedAudio, deletedJobs
}

func (s *Store) Stats() map[string]int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := map[string]int{"total": len(s.jobs)}
	for _, j := range s.jobs {
		out[string(j.Status)]++
	}
	return out
}

func (s *Store) saveLocked(j *Job) error {
	b, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(s.dir, "jobs", j.ID+".json"), b, 0o644)
}

func (s *Store) flushLocked() {
	if !s.dirty {
		return
	}
	for _, j := range s.jobs {
		if err := s.saveLocked(j); err != nil {
			log.Printf("job: flush %s: %v", j.ID, err)
		}
	}
	s.dirty = false
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

func cloneJob(j *Job) *Job {
	if j == nil {
		return nil
	}
	cp := *j
	cp.SourceJobIDs = append([]string(nil), j.SourceJobIDs...)
	cp.SourceSpans = append([]SourceSpan(nil), j.SourceSpans...)
	if j.Result != nil {
		r := *j.Result
		if j.Result.Segments != nil {
			r.Segments = append([]Segment(nil), j.Result.Segments...)
		}
		if j.Result.Sources != nil {
			r.Sources = append([]SourceSpan(nil), j.Result.Sources...)
		}
		if j.Result.Analysis != nil {
			r.Analysis = append(json.RawMessage(nil), j.Result.Analysis...)
		}
		cp.Result = &r
	}
	return &cp
}
