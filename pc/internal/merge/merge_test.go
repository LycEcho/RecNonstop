package merge

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func requireFFmpeg(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not installed")
	}
}

func makeTone(t *testing.T, path string, seconds float64, rate int) {
	t.Helper()
	cmd := exec.Command("ffmpeg", "-hide_banner", "-nostdin", "-v", "error", "-y",
		"-f", "lavfi", "-i", "sine=frequency=440:duration="+strconv.FormatFloat(seconds, 'f', 3, 64),
		"-ar", strconv.Itoa(rate), "-ac", "1", "-c:a", "pcm_s16le", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("make tone: %v: %s", err, out)
	}
}

func TestBuildMergesAndMapsOffsets(t *testing.T) {
	requireFFmpeg(t)
	dir := t.TempDir()
	a := filepath.Join(dir, "a.wav")
	b := filepath.Join(dir, "b.wav")
	makeTone(t, a, 1.2, 8000)
	makeTone(t, b, 0.8, 44100)

	out := filepath.Join(dir, "merged", "m.wav")
	spans, dur, err := Build(context.Background(), Options{}, []Source{
		{JobID: "j1", SegmentName: "one.wav", Path: a},
		{JobID: "j2", SegmentName: "two.wav", Path: b},
	}, out, 0)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(spans) != 2 {
		t.Fatalf("spans = %d, want 2", len(spans))
	}
	if spans[0].Offset != 0 || spans[0].Duration < 1.1 || spans[0].Duration > 1.3 {
		t.Fatalf("span0 = %+v", spans[0])
	}
	wantSecond := spans[0].Duration
	if spans[1].Offset != wantSecond {
		t.Fatalf("span1 offset = %v, want %v", spans[1].Offset, wantSecond)
	}
	if dur < 1.9 || dur > 2.2 {
		t.Fatalf("duration = %v, want ~2.0", dur)
	}

	stats, err := probeStream(context.Background(), out)
	if err != nil {
		t.Fatalf("probe output: %v", err)
	}
	if stats.rate != 16000 || stats.channels != 1 {
		t.Fatalf("output = %d Hz / %d ch, want 16000 / 1", stats.rate, stats.channels)
	}
}

func TestBuildRejectsTooLong(t *testing.T) {
	requireFFmpeg(t)
	dir := t.TempDir()
	a := filepath.Join(dir, "a.wav")
	makeTone(t, a, 2, 16000)
	_, _, err := Build(context.Background(), Options{}, []Source{
		{JobID: "j1", Path: a},
		{JobID: "j2", Path: a},
	}, filepath.Join(dir, "out.wav"), 3)
	if !errors.Is(err, ErrTooLong) {
		t.Fatalf("err = %v, want ErrTooLong", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "out.wav")); !os.IsNotExist(statErr) {
		t.Fatalf("output should not exist on error")
	}
}

type streamInfo struct {
	rate     int
	channels int
}

func probeStream(ctx context.Context, path string) (streamInfo, error) {
	out, err := exec.CommandContext(ctx, "ffprobe", "-v", "error",
		"-select_streams", "a:0",
		"-show_entries", "stream=sample_rate,channels",
		"-of", "default=nw=1", path).Output()
	if err != nil {
		return streamInfo{}, err
	}
	var info streamInfo
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch k {
		case "sample_rate":
			info.rate, _ = strconv.Atoi(v)
		case "channels":
			info.channels, _ = strconv.Atoi(v)
		}
	}
	return info, nil
}
