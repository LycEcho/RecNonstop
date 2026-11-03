package infer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

type mockChatTransport func(*http.Request) (*http.Response, error)

func (f mockChatTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func mockChatClient(t *testing.T, reply func(chatRequest) (string, string)) *LMStudio {
	t.Helper()
	c := NewLMStudio("http://model.invalid", "", "synthetic-model", "", 2048, 0, time.Second, false)
	c.HC.Transport = mockChatTransport(func(r *http.Request) (*http.Response, error) {
		var req chatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		content, reason := reply(req)
		body, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{
			"message": map[string]string{"content": content}, "finish_reason": reason,
		}}})
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
	})
	return c
}

func TestReduceNotesBoundsEveryLevelAndKeepsFacts(t *testing.T) {
	var notes []string
	for i := 0; i < 16; i++ {
		notes = append(notes, "事实"+strings.Repeat("甲", i+1)+"\n"+strings.Repeat("合成背景。", 280))
	}
	facts := regexp.MustCompile(`事实甲+`)
	calls := 0
	material, err := reduceNotes(context.Background(), notes, func(_ context.Context, part string) (string, error) {
		calls++
		if utf8.RuneCountInString(part) > summaryMaterialRunes {
			t.Fatal("reduction input exceeds bound")
		}
		return strings.Join(facts.FindAllString(part, -1), "\n") + "\n" + strings.Repeat("背景", 100), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls <= 16 || utf8.RuneCountInString(material) > summaryMaterialRunes {
		t.Fatalf("expected multiple bounded levels; calls=%d length=%d", calls, utf8.RuneCountInString(material))
	}
	got := facts.FindAllString(material, -1)
	if len(got) != 16 {
		t.Fatalf("lost facts: %d, want 16", len(got))
	}
}

func TestReduceNotesRejectsNonConvergingOrEmptyOutput(t *testing.T) {
	for _, empty := range []bool{false, true} {
		_, err := reduceNotes(context.Background(), []string{strings.Repeat("字", 4000)}, func(_ context.Context, part string) (string, error) {
			if empty {
				return "", nil
			}
			return part, nil
		})
		if err == nil {
			t.Fatal("must not silently discard notes or loop on non-converging output")
		}
	}
}

func TestReduceNotesStopsWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := reduceNotes(ctx, []string{strings.Repeat("字", 4000)}, func(context.Context, string) (string, error) {
		t.Fatal("called model after cancellation")
		return "", nil
	})
	if err != context.Canceled {
		t.Fatalf("error = %v", err)
	}
}

func TestAnalyzeHierarchicalMergePreservesStructuredFacts(t *testing.T) {
	leaves, merges := 0, 0
	facts := regexp.MustCompile(`任务_\d+`)
	// Fixed instructions now include two short risk-boundary examples. The
	// source/reduction material still uses the unchanged 2000-rune limit.
	const maxPromptRunes = summaryMaterialRunes + 2200
	c := mockChatClient(t, func(req chatRequest) (string, string) {
		prompt := req.Messages[1].Content
		if utf8.RuneCountInString(prompt) > maxPromptRunes {
			t.Fatalf("unbounded prompt: %d", utf8.RuneCountInString(prompt))
		}
		var actions []Action
		if strings.Contains(prompt, "分段分析的事实记录") {
			merges++
			for _, name := range facts.FindAllString(prompt, -1) {
				actions = append(actions, Action{Speaker: "spk_0", Action: name, Details: "期限未明确"})
			}
		} else {
			leaves++
			actions = append(actions, Action{Speaker: "spk_0", Action: "任务_" + strings.Repeat("1", leaves), Details: "期限未明确"})
		}
		summary := strings.Repeat("合成摘要。", 180)
		if merges > 0 {
			summary = "合并摘要"
		}
		b, _ := json.Marshal(Analysis{Summary: summary, Actions: actions, Risks: []string{"待确认交付时间"}})
		return string(b), "stop"
	})
	var segments []Segment
	for i := 0; i < 10; i++ {
		segments = append(segments, Segment{Speaker: "spk_0", Text: strings.Repeat("合成记录", 400)})
	}
	a, err := c.Analyze(context.Background(), &Transcript{Segments: segments})
	if err != nil {
		t.Fatal(err)
	}
	if leaves != 10 || merges < 2 {
		t.Fatalf("leaves=%d merges=%d", leaves, merges)
	}
	if len(a.Actions) != 10 {
		t.Fatalf("lost actions: %d", len(a.Actions))
	}
	if len(a.Risks) == 0 {
		t.Fatal("lost risk field")
	}
}

func TestDailyReportBoundsMergeAndCarriesDateAndActions(t *testing.T) {
	const metadata = "日期：2026-01-02\n时间段：09:00 - 18:00\n录音：20 段，共 60 分钟"
	const maxPromptRunes = summaryMaterialRunes + 1500 // Fixed instructions, metadata and repair feedback; source remains bounded separately.
	facts := regexp.MustCompile(`事项_\d+`)
	leaves, reductions := 0, 0
	c := mockChatClient(t, func(req chatRequest) (string, string) {
		prompt := req.Messages[1].Content
		if utf8.RuneCountInString(prompt) > maxPromptRunes {
			t.Fatalf("unbounded daily prompt: %d", utf8.RuneCountInString(prompt))
		}
		isExtraction := req.Messages[0].Content == dailyExtractSystemPrompt
		if !isExtraction && !strings.Contains(prompt, metadata) {
			t.Fatal("daily context/date lost in a chunk or reduction")
		}
		if req.MaxTokens < 2048 {
			t.Fatalf("intermediate evidence compressed by smaller token budget: %d", req.MaxTokens)
		}
		if isExtraction {
			leaves++
			var ids []int
			for i := range regexp.MustCompile(`(?m)^\[L[0-9]+\]`).FindAllString(prompt, -1) {
				ids = append(ids, i+1)
			}
			return syntheticDailyExtraction(strings.Repeat("合成背景。", 60), ids...), "stop"
		}
		if strings.Contains(prompt, "待归并要点：") {
			reductions++
		}
		return strings.Join(facts.FindAllString(prompt, -1), "\n") + "\n已确认期限未明确", "stop"
	})
	var source strings.Builder
	source.WriteString(metadata + "\n\n")
	for i := 1; i <= 20; i++ {
		fmt.Fprintf(&source, "[01-02 09:00:00.000 · 录音%d/spk_0] 事项_%d %s\n", i, i, strings.Repeat("记录", 300))
	}
	material := source.String()
	result, err := c.GenerateDailyReport(context.Background(), material)
	if err != nil {
		t.Fatal(err)
	}
	if reductions < 1 || leaves < 3 {
		t.Fatalf("expected reduction, leaves=%d reductions=%d", leaves, reductions)
	}
	if leaves != len(chunkText(material, summaryMaterialRunes)) {
		t.Fatal("valid extraction added model calls per leaf")
	}
	if got := len(facts.FindAllString(result, -1)); got != 20 {
		t.Fatalf("lost daily facts: %d, want 20", got)
	}
}

func TestDailyMetadataDoesNotCopyUnboundedTranscriptLines(t *testing.T) {
	material := "日期：2026-01-02\n时间段：09:00 - 18:00\n录音：20 段，共 60 分钟\n" + strings.Repeat("长转写", 5000)
	meta := dailyMetadata(material)
	if utf8.RuneCountInString(meta) > 600 || !strings.Contains(meta, "2026-01-02") || strings.Contains(meta, "长转写") {
		t.Fatalf("invalid metadata: %q", meta)
	}
}

func TestDailyReductionRetriesExpansionAndKeepsTailTask(t *testing.T) {
	const metadata = "日期：2026-01-02\n时间段：09:00 - 18:00\n录音：4 段，共 60 分钟"
	const maxPromptRunes = summaryMaterialRunes + 1500 // The additional allowance is for fixed instructions, not source text.
	const tailTask = "末尾事项：测试负责人须在1月4日15:00前补齐4件，否则延期；未承诺额外采购。"
	var firstParts, retryParts []string
	c := mockChatClient(t, func(req chatRequest) (string, string) {
		prompt := req.Messages[1].Content
		if utf8.RuneCountInString(prompt) > maxPromptRunes || req.MaxTokens != 2048 {
			t.Fatalf("retry changed the bounded context or output budget: runes=%d tokens=%d", utf8.RuneCountInString(prompt), req.MaxTokens)
		}
		if req.Messages[0].Content == dailyExtractSystemPrompt {
			var ids []int
			for i := range regexp.MustCompile(`(?m)^\[L[0-9]+\]`).FindAllString(prompt, -1) {
				ids = append(ids, i+1)
			}
			return syntheticDailyExtraction(strings.Repeat("合成背景。", 60), ids...), "stop"
		}
		if _, notes, ok := strings.Cut(prompt, "待归并要点：\n"); ok {
			part, _, retry := strings.Cut(notes, "\n\n定向压缩重试：")
			part = strings.TrimSpace(part)
			if utf8.RuneCountInString(part) > summaryMaterialRunes {
				t.Fatal("expanded output was fed back as unbounded retry evidence")
			}
			if !retry {
				firstParts = append(firstParts, part)
				return part + "\n重复的无事项说明。", "stop"
			}
			retryParts = append(retryParts, part)
			if strings.Contains(part, tailTask) {
				return tailTask, "stop"
			}
			return "录音含合成背景表达。", "stop"
		}
		_, notes, _ := strings.Cut(prompt, "分段要点：\n")
		return "## 今日概览\n" + notes, "stop"
	})
	var source strings.Builder
	source.WriteString(metadata + "\n\n")
	for i := 1; i <= 4; i++ {
		fmt.Fprintf(&source, "[01-02 09:00:00.000 · 录音%d/spk_0] %s", i, strings.Repeat("合成原文。", 240))
		if i == 4 {
			source.WriteString(tailTask)
		}
		source.WriteByte('\n')
	}
	material := source.String()
	result, err := c.GenerateDailyReport(context.Background(), material)
	if err != nil {
		t.Fatal(err)
	}
	if len(firstParts) < 2 || len(retryParts) != len(firstParts) {
		t.Fatalf("expected exactly one retry of the non-shrinking level: first=%d retry=%d", len(firstParts), len(retryParts))
	}
	for i := range firstParts {
		if firstParts[i] != retryParts[i] {
			t.Fatalf("retry changed original evidence or order at chunk %d", i)
		}
	}
	if !strings.Contains(result, tailTask) {
		t.Fatal("retry lost the final task, owner, deadline, quantity, negation or condition")
	}
}

func TestReductionRetryRejectsPersistentExpansion(t *testing.T) {
	notes := []string{strings.Repeat("合成背景", 1000)}
	first, retries := 0, 0
	result, err := reduceNotesWithRetry(context.Background(), notes,
		func(_ context.Context, part string) (string, error) {
			first++
			return part + "重复说明", nil
		}, func(_ context.Context, part string) (string, error) {
			retries++
			return part + "再次重复说明", nil
		})
	if err == nil || !strings.Contains(err.Error(), "did not shrink") || result != "" {
		t.Fatalf("non-shrinking retry returned success or partial notes: result=%q err=%v", result, err)
	}
	if first != 2 || retries != 2 {
		t.Fatalf("retry was not bounded to once per level: first=%d retries=%d", first, retries)
	}
}

func TestReductionRetryStopsOnCancellationWithoutPartialNotes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	retries := 0
	result, err := reduceNotesWithRetry(ctx, []string{strings.Repeat("合成背景", 1000)},
		func(_ context.Context, part string) (string, error) { return part + "重复说明", nil },
		func(context.Context, string) (string, error) {
			retries++
			cancel()
			return "第一块完成", nil
		})
	if err != context.Canceled || result != "" || retries != 1 {
		t.Fatalf("retry ignored cancellation or returned partial notes: result=%q err=%v retries=%d", result, err, retries)
	}
}

func TestChatRejectsTruncatedAnalysisAndReport(t *testing.T) {
	c := mockChatClient(t, func(chatRequest) (string, string) { return `{"summary":"部分内容"}`, "length" })
	_, err := c.chatOnce(context.Background(), "system", "synthetic", 1024, false)
	if err == nil || !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("truncated output accepted: %v", err)
	}
}

func TestAnalysisMaterialKeepsSchemaDrift(t *testing.T) {
	a := parseAnalysis(`{"summary":"合成摘要","actions":[{"action":"确认","deadline":"周五"}],"risks":[{"type":"交付延迟","evidence":"尚未确认"}],"decisions":["暂缓"]}`)
	material := analysisMaterial(a)
	for _, fact := range []string{"周五", "尚未确认", "暂缓"} {
		if !strings.Contains(material, fact) {
			t.Errorf("lost drifted fact %q", fact)
		}
	}
}

func TestAnalyzeEmptyTranscriptDoesNotAskModelToInventSummary(t *testing.T) {
	c := mockChatClient(t, func(chatRequest) (string, string) {
		t.Fatal("empty recording should not call model")
		return "", "stop"
	})
	a, err := c.Analyze(context.Background(), &Transcript{})
	if err != nil || a.Summary == "" || len(a.Actions) != 0 || len(a.Risks) != 0 {
		t.Fatalf("empty recording analysis: %+v, %v", a, err)
	}
}

func TestAnalyzeRejectsEmptyModelOutput(t *testing.T) {
	c := mockChatClient(t, func(chatRequest) (string, string) { return `{"summary":""}`, "stop" })
	_, err := c.Analyze(context.Background(), &Transcript{Text: "合成语音文本"})
	if err == nil {
		t.Fatal("empty analysis was accepted as success")
	}
}
