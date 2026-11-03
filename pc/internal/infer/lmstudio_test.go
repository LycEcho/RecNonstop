package infer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestParseAnalysisValidJSON(t *testing.T) {
	a := parseAnalysis(`{"summary":"讨论了预算","speakers":["spk_0"],"actions":[],"risks":[]}`)
	if a.Summary != "讨论了预算" {
		t.Fatalf("summary = %q", a.Summary)
	}
	if !json.Valid(a.Raw) {
		t.Fatal("raw should be valid JSON")
	}
	if _, err := json.Marshal(a); err != nil {
		t.Fatalf("marshal: %v", err)
	}
}

func TestChunkTranscriptSplitsLongSegmentWithoutLosingText(t *testing.T) {
	text := strings.Repeat("合成测试语句。", 1000)
	chunks := chunkTranscript(&Transcript{Segments: []Segment{{Speaker: "spk_0", Text: text}}}, 3000)
	if len(chunks) < 2 {
		t.Fatal("long single segment was not split")
	}
	for _, ch := range chunks {
		if utf8.RuneCountInString(ch) > 3000 {
			t.Fatalf("chunk exceeds limit: %d", utf8.RuneCountInString(ch))
		}
	}
	if !strings.Contains(strings.Join(chunks, ""), text) {
		t.Fatal("transcript text was lost or changed")
	}
	if got := chunkTranscript(&Transcript{Text: "只有全文的转写"}, 3000); len(got) != 1 || !strings.Contains(got[0], "只有全文的转写") {
		t.Fatalf("text-only transcript lost: %v", got)
	}
}

func TestChunkTextExactLimit(t *testing.T) {
	for _, text := range []string{strings.Repeat("字", 10), strings.Repeat("字", 20), strings.Repeat("字", 21)} {
		chunks := chunkText(text, 10)
		for _, ch := range chunks {
			if utf8.RuneCountInString(ch) > 10 {
				t.Fatalf("chunk exceeds limit: %q", ch)
			}
		}
		if strings.TrimSuffix(strings.Join(chunks, ""), "\n") != text {
			t.Fatal("text was lost")
		}
	}
}

func TestAnalyzePreservesActionsAndRisksWhenMerging(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var req chatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode: %v", err)
		}
		if requests == 3 {
			prompt := req.Messages[1].Content
			for _, fact := range []string{"确认交付时间", "周五之前", "材料短缺", "\"actions\"", "\"risks\""} {
				if !strings.Contains(prompt, fact) {
					t.Errorf("merge prompt lost %q", fact)
				}
			}
		}
		content := `{"summary":"讨论进展","speakers":["spk_0"],"actions":[{"speaker":"spk_0","action":"确认交付时间","details":"周五之前"}],"risks":["材料短缺"]}`
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": content}}}})
	}))
	defer srv.Close()
	c := NewLMStudio(srv.URL, "", "test", "", 1024, 0, time.Second, false)
	_, err := c.Analyze(context.Background(), &Transcript{Segments: []Segment{
		{Speaker: "spk_0", Text: strings.Repeat("测试", 900)},
		{Speaker: "spk_0", Text: strings.Repeat("测试", 900)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if requests != 3 {
		t.Fatalf("requests = %d, want 3", requests)
	}
}

func TestParseAnalysisSchemaDrift(t *testing.T) {
	a := parseAnalysis(`{"analysis":"模型换了字段名"}`)
	if a.Summary != "模型换了字段名" {
		t.Fatalf("summary = %q", a.Summary)
	}
}

func TestParseAnalysisRisksObjects(t *testing.T) {
	a := parseAnalysis(`{"summary":"有风险","risks":[{"type":"物流延迟"}],"actions":[{"speaker":"spk_0","action":"确认","details":"时间"}]}`)
	if a.Summary != "有风险" {
		t.Fatalf("summary = %q", a.Summary)
	}
	if !json.Valid(a.Raw) {
		t.Fatal("raw should stay valid JSON")
	}
	if _, err := json.Marshal(a); err != nil {
		t.Fatalf("marshal: %v", err)
	}
}

func TestParseAnalysisPlainText(t *testing.T) {
	a := parseAnalysis("抱歉，我无法分析这段录音。")
	if a.Summary == "" {
		t.Fatal("summary empty")
	}
	// 关键回归：非法 JSON 绝不能进入 json.RawMessage。
	if a.Raw != nil {
		t.Fatal("raw must be nil for non-JSON")
	}
	if _, err := json.Marshal(a); err != nil {
		t.Fatalf("marshal with nil raw: %v", err)
	}
}

func TestStripCodeFence(t *testing.T) {
	cases := []string{
		"```json\n{\"summary\":\"x\"}\n```",
		"```JSON\n{\"summary\":\"x\"}\n```",
		"```\n{\"summary\":\"x\"}\n```",
	}
	for _, c := range cases {
		if got := stripCodeFence(c); got != `{"summary":"x"}` {
			t.Errorf("stripCodeFence(%q) = %q", c, got)
		}
	}
}
