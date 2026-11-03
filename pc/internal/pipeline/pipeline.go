package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"warehouse-audio/internal/infer"
	"warehouse-audio/internal/job"
)

var ErrQueueFull = errors.New("pipeline queue full")

type task struct {
	jobID     string
	audioPath string
}

type Pipeline struct {
	ASR      infer.ASR
	Analyzer infer.Analyzer
	Store    *job.Store
	Mode     string
	Backend  map[string]string

	queue    chan task
	ctx      context.Context
	cancel   context.CancelFunc
	timeout  time.Duration
	retries  int
	wg       sync.WaitGroup
	mu       sync.RWMutex
	closed   bool
	inFlight map[string]bool
	active   atomic.Int32
}

func New(asr infer.ASR, analyzer infer.Analyzer, store *job.Store, mode string,
	backend map[string]string, workers, queueSize int, timeout time.Duration) *Pipeline {
	if workers <= 0 {
		workers = 1
	}
	if queueSize <= 0 {
		queueSize = 100
	}
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	ctx, cancel := context.WithCancel(context.Background())
	p := &Pipeline{
		ASR:      asr,
		Analyzer: analyzer,
		Store:    store,
		Mode:     mode,
		Backend:  backend,
		queue:    make(chan task, queueSize),
		ctx:      ctx,
		cancel:   cancel,
		timeout:  timeout,
		retries:  2,
		inFlight: map[string]bool{},
	}
	for i := 0; i < workers; i++ {
		p.wg.Add(1)
		go p.worker(i)
	}
	return p
}

func (p *Pipeline) worker(id int) {
	defer p.wg.Done()
	for t := range p.queue {
		if p.ctx.Err() != nil {
			return // Unstarted jobs remain queued in the store for restart.
		}
		p.mu.Lock()
		delete(p.inFlight, t.jobID)
		p.mu.Unlock()
		p.active.Add(1)
		p.Run(p.ctx, t.jobID, t.audioPath)
		p.active.Add(-1)
	}
}

func (p *Pipeline) Enqueue(jobID, audioPath string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return errors.New("pipeline closed")
	}
	if p.inFlight[jobID] {
		return nil
	}
	p.inFlight[jobID] = true
	select {
	case p.queue <- task{jobID: jobID, audioPath: audioPath}:
		return nil
	default:
		delete(p.inFlight, jobID)
		return ErrQueueFull
	}
}

func (p *Pipeline) QueueLen() int {
	return len(p.queue)
}

// Full 用于上传前快速背压判断，避免先把文件写完再发现队列满。
func (p *Pipeline) Full() bool {
	return len(p.queue) >= cap(p.queue)
}

func (p *Pipeline) Active() int {
	return int(p.active.Load())
}

func (p *Pipeline) Close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	p.cancel()
	close(p.queue)
	p.mu.Unlock()
	p.wg.Wait()
}

// Recover 把服务重启后仍是 queued 的任务重新塞回队列。
func (p *Pipeline) Recover() {
	for _, j := range p.Store.Queued(100) {
		if j.AudioPath == "" {
			continue
		}
		if _, err := os.Stat(j.AudioPath); err != nil {
			_ = p.Store.Update(j.ID, func(x *job.Job) {
				x.Status = job.StatusFailed
				x.Stage = "failed"
				x.Error = "audio file missing after restart"
			})
			continue
		}
		if err := p.Enqueue(j.ID, j.AudioPath); err != nil {
			log.Printf("pipeline: recover %s: %v", j.ID, err)
			return
		}
	}
}

func (p *Pipeline) Run(parent context.Context, jobID, audioPath string) {
	ctx, cancel := context.WithTimeout(parent, p.timeout)
	defer cancel()

	started := time.Now()
	_ = p.Store.Update(jobID, func(j *job.Job) {
		j.Status = job.StatusRunning
		j.Stage = "transcribing"
		j.Attempts++
		j.Error = ""
	})

	tr, err := p.transcribe(ctx, audioPath)
	if err != nil {
		if parent.Err() != nil {
			// A clean shutdown must not fail the active job or drain the
			// backlog. Resume this unfinished recording after restart.
			_ = p.Store.Update(jobID, func(j *job.Job) {
				j.Status = job.StatusQueued
				j.Stage = "queued"
				j.Error = "transcribe interrupted; queued for retry: " + err.Error()
				if j.Attempts > 0 {
					j.Attempts--
				}
			})
			return
		}
		p.fail(jobID, nil, "transcribe: "+err.Error())
		log.Printf("pipeline: %s transcribe failed: %v", jobID, err)
		return
	}
	if tr == nil {
		p.fail(jobID, nil, "transcribe: empty result")
		return
	}

	segs := normalizeSegments(tr.Segments)

	// 先落转写，再做 LLM 分析：分析失败/超时也不至于把整段转写丢掉。
	res := &job.Result{
		JobID:    jobID,
		Mode:     p.Mode,
		Duration: tr.Duration,
		Segments: segs,
		Backend:  p.Backend,
	}
	for _, event := range tr.AudioEvents {
		copied := job.AudioEvent{Start: event.Start, End: event.End}
		if event.Tags != nil {
			copied.Tags = make([]job.AudioEventTag, len(event.Tags))
		}
		for i, tag := range event.Tags {
			copied.Tags[i] = job.AudioEventTag{Label: tag.Label, Score: tag.Score}
		}
		res.AudioEvents = append(res.AudioEvents, copied)
	}
	// 合并任务：把来源偏移带进结果，前端可把整段转写映射回原始分段。
	if j, ok := p.Store.Get(jobID); ok {
		res.Kind = j.Kind
		res.Sources = j.SourceSpans
	}
	_ = p.Store.Update(jobID, func(j *job.Job) {
		j.Stage = "analyzing"
		// Publish an immutable snapshot. The worker adds analysis to res
		// below while API readers may already be reading this transcript.
		transcriptOnly := *res
		transcriptOnly.AudioEvents = append([]job.AudioEvent(nil), res.AudioEvents...)
		for i := range transcriptOnly.AudioEvents {
			if res.AudioEvents[i].Tags != nil {
				transcriptOnly.AudioEvents[i].Tags = append([]job.AudioEventTag{}, res.AudioEvents[i].Tags...)
			}
		}
		j.Result = &transcriptOnly
	})

	an, err := p.analyze(ctx, tr)
	if err != nil {
		// 摘要只是附加能力：LLM 不可用时保留 Qwen3-ASR 转写，任务仍然算完成。
		log.Printf("pipeline: %s analyze failed, keeping transcript: %v", jobID, err)
		_ = p.Store.Update(jobID, func(j *job.Job) {
			j.Status = job.StatusDone
			j.Stage = "done"
			j.Result = res
		})
		return
	}

	res.Summary = an.Summary
	if len(an.Raw) > 0 && json.Valid(an.Raw) {
		res.Analysis = an.Raw
	} else if len(an.Raw) > 0 {
		// 绝不允许把非法 JSON 塞进 json.RawMessage：那会让整条 job
		// marshal 失败、落不了盘、查询返回空对象。
		res.Error = "invalid analysis json; kept transcript"
	}

	_ = p.Store.Update(jobID, func(j *job.Job) {
		j.Status = job.StatusDone
		j.Stage = "done"
		j.Result = res
	})
	log.Printf("pipeline: %s done in %s (%.1fs audio)",
		jobID, time.Since(started).Round(time.Millisecond), tr.Duration)
}

func (p *Pipeline) transcribe(ctx context.Context, audioPath string) (*infer.Transcript, error) {
	return p.transcribeWithRetry(ctx, audioPath, waitForASRRetry)
}

func waitForASRRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (p *Pipeline) transcribeWithRetry(ctx context.Context, audioPath string,
	wait func(context.Context, time.Duration) error) (*infer.Transcript, error) {
	var last error
	for attempt := 1; ; attempt++ {
		if ctx.Err() != nil {
			if last != nil {
				return nil, fmt.Errorf("ASR retry interrupted: %w; last error: %w", ctx.Err(), last)
			}
			return nil, ctx.Err()
		}
		tr, err := p.ASR.Transcribe(ctx, audioPath)
		if err == nil {
			return tr, nil
		}
		last = err
		if !infer.IsRetryableASRError(err) {
			return nil, err
		}
		// Keep this job running while the backend is offline, leaving the rest
		// of the backlog queued. The existing job context bounds all attempts.
		log.Printf("pipeline: ASR unavailable (attempt %d), retry in 25s: %v", attempt, err)
		if waitErr := wait(ctx, 25*time.Second); waitErr != nil {
			return nil, fmt.Errorf("ASR retry interrupted: %w; last error: %w", waitErr, last)
		}
	}
}

func (p *Pipeline) analyze(ctx context.Context, tr *infer.Transcript) (*infer.Analysis, error) {
	var last error
	for i := 0; i < p.retries; i++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		an, err := p.Analyzer.Analyze(ctx, tr)
		if err == nil {
			return an, nil
		}
		last = err
		log.Printf("pipeline: analyze attempt %d/%d: %v", i+1, p.retries, err)
		if i+1 < p.retries {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(2 * time.Second):
			}
		}
	}
	return nil, last
}

func (p *Pipeline) fail(jobID string, res *job.Result, msg string) {
	_ = p.Store.Update(jobID, func(j *job.Job) {
		j.Status = job.StatusFailed
		j.Stage = "failed"
		j.Error = msg
		if res != nil {
			j.Result = res
		}
	})
}
