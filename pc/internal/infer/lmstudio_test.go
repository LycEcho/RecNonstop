package infer

import (
	"encoding/json"
	"testing"
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
