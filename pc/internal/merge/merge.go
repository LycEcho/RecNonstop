// Package merge 把多个录音分段合并成一个 16kHz 单声道 WAV。
//
// 合并后整段重新跑一次 ASR + CAM++，说话人编号在整段内一致，
// 不会出现“每段 spk_0 都是不同人”的问题。每段在合并文件里的
// 起始偏移会写进 SourceSpan，便于把整段转写映射回原始分段。
package merge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"warehouse-audio/internal/job"
)

var (
	ErrFFmpegMissing = errors.New("ffmpeg/ffprobe not found")
	ErrTooLong       = errors.New("merged audio exceeds limit")
)

type Options struct {
	FFmpeg     string
	FFprobe    string
	SampleRate int
}

type Source struct {
	JobID       string
	SegmentName string
	Path        string
}

func (o *Options) normalize() {
	if o.FFmpeg == "" {
		o.FFmpeg = "ffmpeg"
	}
	if o.FFprobe == "" {
		o.FFprobe = "ffprobe"
	}
	if o.SampleRate <= 0 {
		o.SampleRate = 16000
	}
}

// Build 按 sources 顺序合并音频。maxSeconds<=0 表示不限制总时长。
// 返回每一段的偏移映射和合并后的实际时长。
func Build(ctx context.Context, opts Options, sources []Source, outPath string, maxSeconds float64) ([]job.SourceSpan, float64, error) {
	if len(sources) == 0 {
		return nil, 0, errors.New("no sources")
	}
	opts.normalize()
	if _, err := exec.LookPath(opts.FFmpeg); err != nil {
		return nil, 0, fmt.Errorf("%w: %s", ErrFFmpegMissing, opts.FFmpeg)
	}
	if _, err := exec.LookPath(opts.FFprobe); err != nil {
		return nil, 0, fmt.Errorf("%w: %s", ErrFFmpegMissing, opts.FFprobe)
	}

	spans := make([]job.SourceSpan, 0, len(sources))
	offset := 0.0
	for _, s := range sources {
		if s.Path == "" {
			return nil, 0, fmt.Errorf("source %s has no audio path", s.JobID)
		}
		if st, err := os.Stat(s.Path); err != nil {
			return nil, 0, fmt.Errorf("source %s: %w", s.JobID, err)
		} else if st.Size() == 0 {
			return nil, 0, fmt.Errorf("source %s: empty audio", s.JobID)
		}
		d, err := probeDuration(ctx, opts.FFprobe, s.Path)
		if err != nil {
			return nil, 0, fmt.Errorf("probe %s: %w", s.JobID, err)
		}
		if d <= 0 {
			return nil, 0, fmt.Errorf("probe %s: invalid duration", s.JobID)
		}
		spans = append(spans, job.SourceSpan{
			JobID:       s.JobID,
			SegmentName: s.SegmentName,
			Offset:      round3(offset),
			Duration:    round3(d),
		})
		offset += d
	}
	if maxSeconds > 0 && offset > maxSeconds {
		return nil, 0, fmt.Errorf("%w: %.0fs > %.0fs", ErrTooLong, offset, maxSeconds)
	}

	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return nil, 0, err
	}
	tmp := strings.TrimSuffix(outPath, filepath.Ext(outPath)) + ".part.wav"
	_ = os.Remove(tmp)

	args := []string{"-hide_banner", "-nostdin", "-v", "error", "-y"}
	for _, s := range sources {
		args = append(args, "-i", s.Path)
	}
	var fc strings.Builder
	for i := range sources {
		fmt.Fprintf(&fc, "[%d:a]aresample=%d,aformat=sample_fmts=s16:channel_layouts=mono[a%d];", i, opts.SampleRate, i)
	}
	for i := range sources {
		fmt.Fprintf(&fc, "[a%d]", i)
	}
	fmt.Fprintf(&fc, "concat=n=%d:v=0:a=1[out]", len(sources))
	args = append(args, "-filter_complex", fc.String(),
		"-map", "[out]", "-ar", strconv.Itoa(opts.SampleRate), "-ac", "1",
		"-c:a", "pcm_s16le", tmp)

	cmd := exec.CommandContext(ctx, opts.FFmpeg, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		_ = os.Remove(tmp)
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 800 {
			msg = msg[len(msg)-800:]
		}
		if msg == "" {
			return nil, 0, fmt.Errorf("ffmpeg: %w", err)
		}
		return nil, 0, fmt.Errorf("ffmpeg: %w: %s", err, msg)
	}

	outDur, err := probeDuration(ctx, opts.FFprobe, tmp)
	if err != nil || outDur <= 0 {
		outDur = offset
	}
	if err := os.Rename(tmp, outPath); err != nil {
		_ = os.Remove(tmp)
		return nil, 0, err
	}
	return spans, round3(outDur), nil
}

func probeDuration(ctx context.Context, ffprobe, path string) (float64, error) {
	out, err := exec.CommandContext(ctx, ffprobe,
		"-v", "error", "-show_entries", "format=duration",
		"-of", "default=nw=1:nk=1", path).Output()
	if err != nil {
		return 0, err
	}
	return strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
}

func round3(v float64) float64 {
	return float64(int64(v*1000+0.5)) / 1000
}
