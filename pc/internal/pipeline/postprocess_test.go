package pipeline

import (
	"testing"

	"warehouse-audio/internal/infer"
)

func TestNormalizeSegmentsMergesShortSameSpeaker(t *testing.T) {
	in := []infer.Segment{
		{Start: 0, End: 1, Speaker: "spk_0", Text: "你好。"},
		{Start: 1.5, End: 2, Speaker: "spk_0", Text: "今天开会"},
		{Start: 2.2, End: 3, Speaker: "spk_1", Text: "好"},
		{Start: 10, End: 11, Speaker: "spk_1", Text: "再见"},
		{Start: 11.1, End: 11.2, Speaker: "spk_1", Text: "。"},
		{Start: 12.5, End: 13, Speaker: "spk_1", Text: "第一点"},
		{Start: 13.4, End: 14, Speaker: "spk_1", Text: "第二点"},
	}
	out := normalizeSegments(in)
	if len(out) != 4 {
		t.Fatalf("segments = %d, want 4: %+v", len(out), out)
	}
	if out[0].Text != "你好。今天开会" || out[0].End != 2 {
		t.Fatalf("seg0 = %+v", out[0])
	}
	if out[1].Text != "好" || out[1].Speaker != "spk_1" {
		t.Fatalf("seg1 = %+v", out[1])
	}
	if out[2].Text != "再见" {
		t.Fatalf("seg2 = %+v", out[2])
	}
	if out[3].Text != "第一点，第二点" || out[3].End != 14 {
		t.Fatalf("seg3 = %+v", out[3])
	}
}

func TestNormalizeSegmentsDropsEmptyAndPunct(t *testing.T) {
	out := normalizeSegments([]infer.Segment{
		{Start: 0, End: 1, Speaker: "spk_0", Text: ""},
		{Start: 1, End: 2, Speaker: "spk_0", Text: " 。，"},
		{Start: 2, End: 3, Speaker: "spk_0", Text: "有内容"},
	})
	if len(out) != 1 || out[0].Text != "有内容" {
		t.Fatalf("out = %+v", out)
	}
}
