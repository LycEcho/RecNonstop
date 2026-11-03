package infer

import (
	"context"
	"math"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestAudioContextFiltersWeakLabelsAndBoundsMaterial(t *testing.T) {
	if got := audioContext([]AudioEvent{{Tags: []AudioEventTag{{Label: "Music", Score: .119}, {Label: "Speech", Score: math.NaN()}}}}); got != "" {
		t.Fatalf("weak or invalid labels became scene evidence: %q", got)
	}
	labels := []AudioEventTag{{Label: "Music", Score: .7}, {Label: "Speech", Score: .8}, {Label: "Music", Score: .9}, {Label: "Noise", Score: .6}, {Label: "Wind", Score: .55}, {Label: "Bell", Score: .5}, {Label: "invalid\nlabel", Score: 1}}
	got := audioContext([]AudioEvent{{Tags: labels}})
	if !strings.Contains(got, "Music、Speech、Noise、Wind") || strings.Contains(got, "Bell") || strings.Contains(got, "invalid") || utf8.RuneCountInString(got) > 320 {
		t.Fatalf("unbounded or unfiltered sound context: %q", got)
	}
}

func TestAnalyzeReceivesOptionalSoundHintsAlongsideFacts(t *testing.T) {
	c := mockChatClient(t, func(req chatRequest) (string, string) {
		prompt := req.Messages[1].Content
		for _, want := range []string{"Music", "可能有误", "不能据此", "整段声音线索", "不能作为逐句线索", "测试甲周五补齐4件"} {
			if !strings.Contains(prompt, want) {
				t.Fatalf("missing source or sound limitation %q", want)
			}
		}
		return `{"summary":"测试甲周五补齐4件。","speakers":[],"actions":[],"risks":[]}`, "stop"
	})
	_, err := c.Analyze(context.Background(), &Transcript{Text: "测试甲周五补齐4件。", AudioEvents: []AudioEvent{{Tags: []AudioEventTag{{Label: "Music", Score: .9}}}}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestAnalyzeSoundHintsDoNotCrossSegmentWindows(t *testing.T) {
	calls := 0
	c := mockChatClient(t, func(req chatRequest) (string, string) {
		calls++
		prompt := req.Messages[1].Content
		switch calls {
		case 1:
			if !strings.Contains(prompt, "Speech") || strings.Contains(prompt, "Music") || strings.Contains(prompt, "Bell") || !strings.Contains(prompt, "对应转写发言时段") {
				t.Fatalf("first chunk received sound from another time window: %q", prompt)
			}
		case 2:
			if !strings.Contains(prompt, "Music") || strings.Contains(prompt, "Speech") || strings.Contains(prompt, "Bell") || !strings.Contains(prompt, "周五补齐4件，否则可能延期") {
				t.Fatalf("second chunk lost its task or received unrelated sound: %q", prompt)
			}
		case 3:
			if strings.Contains(prompt, "声音线索") {
				t.Fatal("raw acoustic tags were carried into merged facts")
			}
		default:
			t.Fatalf("unexpected extra model request %d", calls)
		}
		return `{"summary":"保留原文明示事项。","speakers":[],"actions":[],"risks":[]}`, "stop"
	})
	_, err := c.Analyze(context.Background(), &Transcript{
		Segments: []Segment{
			{Start: 0, End: 10, Speaker: "spk_0", Text: strings.Repeat("前段合成材料。", 180)},
			{Start: 30, End: 40, Speaker: "spk_1", Text: strings.Repeat("后段合成材料。", 180) + "周五补齐4件，否则可能延期。"},
		},
		AudioEvents: []AudioEvent{
			{Start: 0, End: 10, Tags: []AudioEventTag{{Label: "Speech", Score: .8}}},
			{Start: 10, End: 30, Tags: []AudioEventTag{{Label: "Bell", Score: .9}}},
			{Start: 30, End: 40, Tags: []AudioEventTag{{Label: "Music", Score: .9}}},
		},
	})
	if err != nil || calls != 3 {
		t.Fatalf("Analyze calls=%d err=%v", calls, err)
	}
}

func TestChunkAudioMappingKeepsGapsAndLongUtterancesSeparate(t *testing.T) {
	events := []AudioEvent{
		{Start: 0, End: 10, Tags: []AudioEventTag{{Label: "Speech", Score: .8}}},
		{Start: 10, End: 30, Tags: []AudioEventTag{{Label: "Bell", Score: .9}}},
		{Start: 30, End: 40, Tags: []AudioEventTag{{Label: "Music", Score: .9}}},
	}
	t.Run("gap inside one chunk", func(t *testing.T) {
		chunks := chunkTranscriptWithAudio(&Transcript{Segments: []Segment{
			{Start: 0, End: 10, Text: "库存8件。"}, {Start: 30, End: 40, Text: "周五补齐4件。"},
		}, AudioEvents: events}, 500)
		if len(chunks) != 1 || !strings.Contains(chunks[0].context, "Speech") || !strings.Contains(chunks[0].context, "Music") || strings.Contains(chunks[0].context, "Bell") {
			t.Fatalf("gap was mistaken for spoken time: %+v", chunks)
		}
	})
	t.Run("long and multiline utterance", func(t *testing.T) {
		chunks := chunkTranscriptWithAudio(&Transcript{Segments: []Segment{
			{Start: 0, End: 10, Text: strings.Repeat("重复合成原文。", 50) + "\r\n尾部任务：周五补齐4件。"},
		}, AudioEvents: events}, 80)
		if len(chunks) < 2 {
			t.Fatal("long utterance was not split")
		}
		var original strings.Builder
		for _, chunk := range chunks {
			original.WriteString(chunk.text)
			if !strings.Contains(chunk.context, "Speech") || strings.Contains(chunk.context, "Music") || strings.Contains(chunk.context, "Bell") || strings.Contains(chunk.context, "整段声音线索") {
				t.Fatalf("continuation lost its original utterance window: %q", chunk.context)
			}
		}
		if !strings.Contains(original.String(), "尾部任务：周五补齐4件。") {
			t.Fatal("audio mapping removed the end of the source")
		}
	})
	for _, span := range [][2]float64{{10, 10}, {-1, 10}, {math.NaN(), 10}, {0, math.Inf(1)}} {
		if labels := AudioLabelsForRange(events, span[0], span[1]); len(labels) != 0 {
			t.Fatalf("invalid utterance timing created local labels: %v", labels)
		}
	}
}
