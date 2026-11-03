package infer

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestAnalyzeRestoresLeafFactsOmittedByFinalModel(t *testing.T) {
	leaves := 0
	c := mockChatClient(t, func(req chatRequest) (string, string) {
		if strings.Contains(req.Messages[1].Content, "分段分析的事实记录") {
			// Both intermediate reducers and the final model forget source facts.
			return `{"summary":"最终整体概括","speakers":[],"actions":[{"speaker":"spk_0","action":"额外承诺","details":"明天完成"}],"risks":["凭空增加的风险"],"decisions":["保持原计划"]}`, "stop"
		}
		leaves++
		b, _ := json.Marshal(Analysis{
			Summary: strings.Repeat("合成分段摘要。", 180),
			Actions: []Action{{Speaker: "spk_0", Action: "核实事项" + strings.Repeat("甲", leaves), Details: "周五之前"}},
			Risks:   []string{"需要核实的风险" + strings.Repeat("甲", leaves)},
		})
		return string(b), "stop"
	})
	var segments []Segment
	for i := 0; i < 6; i++ {
		segments = append(segments, Segment{Speaker: "spk_0", Text: strings.Repeat("合成记录", 400)})
	}
	a, err := c.Analyze(context.Background(), &Transcript{Segments: segments})
	if err != nil {
		t.Fatal(err)
	}
	if leaves != 6 || len(a.Actions) != 6 || len(a.Risks) != 6 {
		t.Fatalf("facts omitted: leaves=%d actions=%+v risks=%+v", leaves, a.Actions, a.Risks)
	}
	if a.Summary != "最终整体概括" {
		t.Fatalf("summary replaced: %q", a.Summary)
	}
	var persisted Analysis
	if err := json.Unmarshal(a.Raw, &persisted); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(persisted.Actions, a.Actions) || !reflect.DeepEqual(persisted.Risks, a.Risks) {
		t.Fatal("Raw did not preserve the same facts for daily reports")
	}
	if strings.Contains(string(a.Raw), "额外承诺") || strings.Contains(string(a.Raw), "凭空增加的风险") {
		t.Fatal("final model introduced a fact not extracted from the transcript")
	}
	if !strings.Contains(string(a.Raw), "保持原计划") {
		t.Fatal("unrelated final analysis fields were lost")
	}
}

func TestMergeAnalysisFactsOnlyDeduplicatesExactNormalizedFields(t *testing.T) {
	leaf := &Analysis{
		Actions: []Action{
			{Speaker: " spk_0 ", Action: "确认  交付", Details: "周五\n之前"},
			{Speaker: "spk_0", Action: "确认 交付", Details: "周五 之前"},
			{Speaker: "", Action: "确认交付", Details: ""},
			{Speaker: "spk_1", Action: "确认  交付", Details: "周五\n之前"},
			{Speaker: "spk_0", Action: "确认 交付", Details: "周六之前"},
		},
		Risks: []string{" 交付  时间未定 ", "交付 时间未定"},
	}
	final := &Analysis{
		Summary: "合成概括",
		Actions: []Action{
			{Speaker: "spk_0", Action: "确认 交付", Details: "周五 之前"},
			{Speaker: "spk_0", Action: "确认 交付", Details: "周六之前"},
		},
		Risks: []string{"交付 时间未定", "可能缺少材料"},
	}
	if err := mergeAnalysisFacts(final, []*Analysis{leaf}); err != nil {
		t.Fatal(err)
	}
	if len(final.Actions) != 4 || len(final.Risks) != 1 {
		t.Fatalf("incorrect union: actions=%+v risks=%+v", final.Actions, final.Risks)
	}
	if final.Actions[1].Speaker != "" || final.Actions[1].Details != "" {
		t.Fatal("inferred an unspecified owner or deadline")
	}
	if final.Actions[0].Details != "周五\n之前" {
		t.Fatal("rewrote the original detail rather than normalizing only the deduplication key")
	}
	if !json.Valid(final.Raw) {
		t.Fatal("Raw not synchronized for a plain-text final response")
	}
}
