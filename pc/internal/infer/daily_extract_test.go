package infer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestDailySchemaWireOrderClassifiesAllLinesBeforeBackground(t *testing.T) {
	wire, err := json.Marshal(dailySelectionFormat(14))
	if err != nil {
		t.Fatal(err)
	}
	s := string(wire)
	if strings.Index(s, `"labels":{`) > strings.Index(s, `"background":{`) {
		t.Fatal("background generated before source classifications")
	}
	previous := -1
	for i := 1; i <= 14; i++ {
		at := strings.Index(s, fmt.Sprintf(`"L%d":{`, i))
		if at <= previous {
			t.Fatalf("source L%d is missing or appears out of source order: %s", i, s)
		}
		previous = at
	}
}

func syntheticDailyExtraction(background string, ids ...int) string {
	if ids == nil {
		ids = []int{}
	}
	b, _ := json.Marshal(map[string]any{"background": background, "actual_lines": ids})
	return string(b)
}

func syntheticDailyLabels(background string, labels ...string) string {
	values := make(map[string]string, len(labels))
	for i, label := range labels {
		values[fmt.Sprintf("L%d", i+1)] = label
	}
	b, _ := json.Marshal(map[string]any{"background": background, "labels": values})
	return string(b)
}

func TestDailyLabelsKeepAllStatementsAndReportedOriginalsInSourceOrder(t *testing.T) {
	var source []string
	labels := make([]string, 14)
	for i := range labels {
		labels[i] = "background"
		source = append(source, fmt.Sprintf("[01-02 09:%02d:00.000 · 录音1/spk_0] 合成背景表达。", i))
	}
	source[1] = "[01-02 09:01:00.000 · 录音1/spk_0] 库存8件，需要12件，缺4件。"
	source[11] = "[01-02 09:11:00.000 · 录音1/spk_0] 新闻提到外地测试站近期发生停电，未给年份。"
	source[13] = "[01-02 09:13:00.000 · 录音1/spk_0] 测试甲周五补齐4件，否则可能延期；没有承诺额外采购。"
	labels[1], labels[11], labels[13] = "具体事项", "转述事项", "具体事项"
	chunk := strings.Join(source, "\n")
	note, err := renderDailyExtraction(chunk, syntheticDailyLabels("合成抒情背景。", labels...))
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range []int{1, 11, 13} {
		if strings.Count(note, source[i]) != 1 {
			t.Fatalf("missing or duplicated original source L%d", i+1)
		}
	}
	if strings.Contains(note, source[0]) || strings.Index(note, source[1]) > strings.Index(note, source[11]) || strings.Index(note, source[11]) > strings.Index(note, source[13]) {
		t.Fatal("background was retained as a statement or source order changed")
	}
	if strings.Count(note, "录音原文信息（保留原有来源和语境）：") != 1 ||
		!strings.Contains(note, "录音原文信息（保留原有来源和语境）：\n"+source[1]) {
		t.Fatal("source block lost its neutral attribution or repeats unnecessary headings")
	}
	if _, err := renderDailyExtraction(chunk, syntheticDailyLabels("", labels[:13]...)); err == nil {
		t.Fatal("missing final source label silently discarded the final task")
	}
}

func TestDailyLabelsRejectIncompleteUnknownInvalidOrMixedShapes(t *testing.T) {
	const chunk = "[01-02 09:00:00.000 · 录音1/spk_0] 核对清单。\n[01-02 09:01:00.000 · 录音1/spk_0] 周五补齐4件。"
	for _, response := range []string{
		`{"background":"","labels":{"L1":"statement"}}`,
		`{"background":"","labels":{"L1":"statement","L3":"reported"}}`,
		`{"background":"","labels":{"L1":"statement","L2":"reported","L3":"background"}}`,
		`{"background":"","labels":{"L01":"statement","L2":"reported"}}`,
		`{"background":"","labels":{"L1":"personal","L2":"reported"}}`,
		`{"background":"","labels":{"L1":null,"L2":"reported"}}`,
		`{"background":"","labels":{"L1":2,"L2":"reported"}}`,
		`{"background":"","labels":[]}`, `{"background":"","labels":null}`,
		`{"background":"","labels":{"L1":"statement","L2":"reported"},"actual_lines":[]}`,
		`{"background":"","labels":{"L1":"statement","L2":"reported"},"actual_lines":null}`,
		`{"background":"","labels":null,"actual_lines":[1,2]}`,
		`{"background":"","labels":{"L1":"statement","L2":"reported"},"source":"guessed"}`,
	} {
		if note, err := renderDailyExtraction(chunk, response); err == nil || note != "" {
			t.Fatalf("invalid or incomplete labels returned partial material: %q %v", note, err)
		}
	}
}

func TestDailyLabelsAllowPureBackgroundAndChineseEquivalentValues(t *testing.T) {
	const chunk = "[01-02 09:00:00.000 · 录音1/spk_0] 合成抒情表达。"
	for _, label := range []string{"background", "背景表达"} {
		note, err := renderDailyExtraction(chunk, syntheticDailyLabels("", label))
		if err != nil || note != "本部分仅有背景表达，未提取到具体事项。" || strings.Contains(note, chunk) {
			t.Fatalf("pure background became a factual statement: %q %v", note, err)
		}
	}
	for _, label := range []string{"保留原文", "statement", "reported", "具体事项", "转述事项"} {
		note, err := renderDailyExtraction(chunk, syntheticDailyLabels("", label))
		if err != nil || !strings.Contains(note, chunk) {
			t.Fatalf("supported equivalent label lost the source: %q %v", note, err)
		}
	}
}

func TestDailyLabelsKeepInformationalContentWithoutInventingActions(t *testing.T) {
	const knowledge = "[01-02 09:00:00.000 · 录音1/spk_0] 这是一段方法介绍：先保存草稿，再预览排版。这不表示已经完成提交。"
	const opinion = "[01-02 09:01:00.000 · 录音1/spk_0] 我觉得测试工具甲更容易上手，工具乙导出的文件更小，没有安排采购。"
	const external = "[01-02 09:02:00.000 · 录音1/spk_0] 节目介绍两种过滤方法，第二种多一道清洗步骤，未声称已经部署。"
	note, err := renderDailyExtraction(knowledge+"\n"+opinion+"\n"+external,
		syntheticDailyLabels("", "具体事项", "具体事项", "转述事项"))
	if err != nil {
		t.Fatal(err)
	}
	for _, original := range []string{knowledge, opinion, external} {
		if strings.Count(note, original) != 1 {
			t.Fatal("informational original lost its qualifiers or received an inferred source category")
		}
	}
	if !strings.HasPrefix(note, "录音原文信息（保留原有来源和语境）：\n") {
		t.Fatal("informational source block lost its neutral heading")
	}
	if strings.Contains(note, "实际事项相关") || strings.Contains(note, "录音中转述/引用的事项") {
		t.Fatal("model labels were promoted into asserted reality or verified attribution")
	}
}

func TestDailyLabelsRepairMissingFinalSourceOnceAndKeepItsConditions(t *testing.T) {
	const first = "[01-02 09:00:00.000 · 录音1/spk_0] 合成背景表达。"
	const last = "[01-02 09:01:00.000 · 录音1/spk_0] 测试乙周五补齐4件，否则可能延期。"
	for _, repaired := range []bool{true, false} {
		calls := 0
		c := mockChatClient(t, func(req chatRequest) (string, string) {
			calls++
			if req.MaxTokens != 2048 || !strings.Contains(req.Messages[1].Content, "[L2]\n"+last) {
				t.Fatal("repair changed the budget or lost the last source")
			}
			if calls == 2 {
				if !strings.Contains(req.Messages[1].Content, dailyExtractRepair) {
					t.Fatal("missing complete-label repair instruction")
				}
				if repaired {
					return syntheticDailyLabels("", "背景表达", "具体事项"), "stop"
				}
			}
			return syntheticDailyLabels("", "background"), "stop"
		})
		note, err := c.dailyExtractChunk(context.Background(), first+"\n"+last, 2048)
		if calls != 2 || repaired && (err != nil || !strings.Contains(note, last)) || !repaired && (err == nil || note != "") {
			t.Fatalf("missing labels bypassed bounded recovery: repaired=%v calls=%d note=%q err=%v", repaired, calls, note, err)
		}
	}
}

func TestDailyExtractionRestoresWholeUtterancesAndRealSources(t *testing.T) {
	const first = "[01-02 09:00:00.000 · 录音1/spk_0] 合成库存现有5件、需要9件。\n测试甲须于1月4日15:00补齐缺少的4件，否则延期；未承诺额外采购。"
	const second = "[01-02 10:00:00.000 · 录音2/spk_0] 测试乙须于1月5日提交合成清单。"
	chunk := "### 录音 1\n原始转写（主证据）：\n" + first + "\n" + second
	note, err := renderDailyExtraction(chunk, syntheticDailyExtraction("合成背景表达。", 2, 1))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{first, second, "背景表达（不作为现实事项）：合成背景表达。"} {
		if !strings.Contains(note, want) {
			t.Errorf("lost original utterance, source or background: %q", want)
		}
	}
	if strings.Count(note, first) != 1 || strings.Index(note, first) > strings.Index(note, second) {
		t.Fatal("multiple items duplicated or reordered the source utterances")
	}
	if strings.Contains(note, "[L1]") || strings.Contains(note, "[L2]") {
		t.Fatal("selection IDs were displayed as source identities")
	}
	deduped, err := renderDailyExtraction(chunk, syntheticDailyExtraction("", 1, 1))
	if err != nil || strings.Count(deduped, first) != 1 || strings.Contains(deduped, second) {
		t.Fatalf("source selection was not deduplicated: %q %v", deduped, err)
	}
}

func TestDailyExtractionKeepsOriginalWhitespaceAndRepeatedSources(t *testing.T) {
	chunk := "[01-02 09:00:00.000 · 录音1/spk_0] 需要\t4件配件。\n[01-02 10:00:00.000 · 录音2/spk_0] 需要  4件配件。"
	note, err := renderDailyExtraction(chunk, syntheticDailyExtraction("", 1, 2))
	if err != nil || !strings.Contains(note, "需要\t4件配件。") || !strings.Contains(note, "需要  4件配件。") ||
		!strings.Contains(note, "录音1/spk_0") || !strings.Contains(note, "录音2/spk_0") {
		t.Fatalf("whitespace matching lost actual text or conflated repeated sources: %q %v", note, err)
	}
}

func TestDailyExtractionUnlabeledFragmentDoesNotInventSource(t *testing.T) {
	const chunk = "续接的合成原文：1月4日前补齐4件，否则延期。"
	note, err := renderDailyExtraction(chunk, syntheticDailyExtraction("", 1))
	if err != nil || !strings.Contains(note, "本部分原文（来源未标注）") || !strings.Contains(note, chunk) || strings.Contains(note, "模型猜测") {
		t.Fatalf("unlabeled fragment was lost or assigned a guessed identity: %q %v", note, err)
	}
}

func TestDailySelectionKeepsNearestRecordingTitleForUnlabeledUnits(t *testing.T) {
	const titleA = "### 录音 40 · 01-02 17:50:00（1.0 分钟）"
	const titleB = "### 录音 41 · 01-02 18:10:00（1.0 分钟）"
	const taskA = "测试甲须于次日15:00补齐4件，否则延期；未承诺额外采购。"
	const taskB = "测试乙须于周五提交另一份合成清单。"
	chunk := titleA + "\n" + taskA + "\n" + titleB + "\n" + taskB
	units := dailySourceUnits(chunk)
	input := dailyNumberedUnits(chunk, units)
	if input != "[L1]\n"+titleA+"\n"+taskA+"\n\n[L2]\n"+titleB+"\n"+taskB {
		t.Fatalf("numbered input lost original recording context: %q", input)
	}
	for _, tc := range []struct {
		id       int
		title    string
		task     string
		excluded string
	}{{1, titleA, taskA, titleB}, {2, titleB, taskB, titleA}} {
		note, err := renderDailyExtraction(chunk, syntheticDailyExtraction("", tc.id))
		if err != nil || !strings.Contains(note, tc.title+"\n"+tc.task) || strings.Contains(note, tc.excluded) || strings.Contains(note, "来源未标注") {
			t.Fatalf("recording title was lost or borrowed from an adjacent recording: %q %v", note, err)
		}
	}
	const labeled = "[01-02 17:51:12.000 · 录音40/spk_0] 已有逐句来源。"
	note, err := renderDailyExtraction(titleA+"\n"+labeled, syntheticDailyExtraction("", 1))
	if err != nil || !strings.Contains(note, labeled) || strings.Contains(note, titleA) {
		t.Fatalf("recording title replaced an existing utterance source: %q %v", note, err)
	}
}

func TestDailyExtractionAllowsBackgroundAndLocallyEmptySelection(t *testing.T) {
	for _, background := range []string{"只概括合成背景主题。", ""} {
		note, err := renderDailyExtraction("合成背景文本。", syntheticDailyExtraction(background))
		if err != nil || note == "" || strings.Contains(note, "全天") {
			t.Fatalf("local background/empty extraction incorrectly failed or made a day-wide claim: %q %v", note, err)
		}
	}
}

func TestDailyExtractionRejectsInvalidSchemaAndIDs(t *testing.T) {
	const chunk = "[01-02 09:00:00.000 · 录音1/spk_0] 明天核对清单。另有说明。周五交付。"
	for _, response := range []string{
		"not JSON", `{"background":"","actual_lines":null}`, `{"actual_lines":[]}`,
		`{"background":8,"actual_lines":[]}`, `{"background":"","actual_lines":[1.5]}`,
		`{"background":"","actual_lines":["1"]}`, `{"background":"","actual_lines":[null]}`,
		`{"background":"","actual_lines":[],"source":"猜测身份"}`, `{"background":"","actual_lines":[]} {}`,
		syntheticDailyExtraction("", 0), syntheticDailyExtraction("", -1), syntheticDailyExtraction("", 2),
		syntheticDailyExtraction("", 1, 1), syntheticDailyExtraction(strings.Repeat("字", 321)),
	} {
		if _, err := renderDailyExtraction(chunk, response); err == nil {
			t.Errorf("accepted invalid schema, out-of-range ID or oversized background: %q", response)
		}
	}
	if _, err := renderDailyExtraction(chunk, syntheticDailyExtraction(strings.Repeat("字", 320))); err != nil {
		t.Fatalf("valid Unicode background boundary rejected: %v", err)
	}
}

func TestDailyExtractionRepairsOnceWithoutCopyingInvalidOutput(t *testing.T) {
	const chunk = "[01-02 09:00:00.000 · 录音1/spk_0] 1月4日前提交合成清单。"
	calls := 0
	c := mockChatClient(t, func(req chatRequest) (string, string) {
		calls++
		if req.Messages[0].Content != dailyExtractSystemPrompt || req.ResponseFormat != nil {
			t.Fatal("wrong extraction system or ignored disabled JSON mode")
		}
		if calls == 1 {
			return syntheticDailyExtraction("先前无效输出不得进入反馈", 99), "stop"
		}
		if !strings.Contains(req.Messages[1].Content, dailyExtractRepair) || strings.Contains(req.Messages[1].Content, "先前无效输出") {
			t.Fatal("repair omitted fixed instructions or copied invalid output into evidence")
		}
		return syntheticDailyExtraction("", 1), "stop"
	})
	note, err := c.dailyExtractChunk(context.Background(), chunk, 2048)
	if err != nil || calls != 2 || !strings.Contains(note, chunk) {
		t.Fatalf("repair failed: %q %v calls=%d", note, err, calls)
	}
	for _, reason := range []string{"stop", "length"} {
		attempts := 0
		bad := mockChatClient(t, func(chatRequest) (string, string) {
			attempts++
			return syntheticDailyExtraction("", 99), reason
		})
		if result, err := bad.dailyExtractChunk(context.Background(), chunk, 2048); err == nil || result != "" || attempts != 2 {
			t.Fatalf("invalid/truncated extraction escaped retry limit: %q %v attempts=%d", result, err, attempts)
		}
	}
}

func TestDailyExtractionJSONFormatFallbackSharesTwoAttempts(t *testing.T) {
	c := mockChatClient(t, func(chatRequest) (string, string) { return "", "stop" })
	c.JSONMode = true
	calls := 0
	c.HC.Transport = mockChatTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		wire, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		var req chatRequest
		if err := json.Unmarshal(wire, &req); err != nil {
			t.Fatal(err)
		}
		status, body := http.StatusBadRequest, `{"error":"unsupported response_format"}`
		if calls == 1 && req.ResponseFormat == nil || calls == 2 && req.ResponseFormat != nil {
			t.Fatal("JSON mode configuration or format fallback ignored")
		}
		if calls == 1 {
			var raw struct {
				Format json.RawMessage `json:"response_format"`
			}
			if err := json.Unmarshal(wire, &raw); err != nil {
				t.Fatal(err)
			}
			got := raw.Format
			want, _ := json.Marshal(dailySelectionFormat(1))
			if string(got) != string(want) || !strings.Contains(string(got), `"type":"json_schema"`) {
				t.Fatalf("selector did not send its strict per-source schema: %s", got)
			}
		}
		if calls == 2 {
			status = http.StatusOK
			encoded, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{
				"message": map[string]string{"content": syntheticDailyExtraction("合成背景。")}, "finish_reason": "stop",
			}}})
			body = string(encoded)
		}
		return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	if note, err := c.dailyExtractChunk(context.Background(), "合成背景原文。", 2048); err != nil || calls != 2 || !strings.Contains(note, "合成背景") {
		t.Fatalf("format fallback failed: %q %v calls=%d", note, err, calls)
	}
}

func TestDailySelectionSchemaAndNumberedUnits(t *testing.T) {
	const chunk = "日期：2026-01-02\n[01-02 09:00:00.000 · 录音1/spk_0] 合成事项甲。\n[01-02 10:00:00.000 · 录音2/spk_0] 合成事项乙。"
	units := dailySourceUnits(chunk)
	want := "[L1]\n[01-02 09:00:00.000 · 录音1/spk_0] 合成事项甲。\n\n[L2]\n[01-02 10:00:00.000 · 录音2/spk_0] 合成事项乙。"
	if got := dailyNumberedUnits(chunk, units); got != want {
		t.Fatalf("numbering lost source boundaries or included metadata: %q", got)
	}
	format, _ := json.Marshal(dailySelectionFormat(2))
	for _, required := range []string{`"type":"json_schema"`, `"strict":true`, `"additionalProperties":false`,
		`"required":["labels","background"]`, `"maxLength":320`, `"required":["L1","L2"]`, `"enum":["背景表达","具体事项","转述事项"]`} {
		if !strings.Contains(string(format), required) {
			t.Errorf("missing schema constraint: %s", required)
		}
	}
	if strings.Contains(string(format), "actual_lines") {
		t.Fatal("new strict requests still permit sparse legacy selections")
	}
}

func TestLegacyChatOncePreservesJSONModeBehavior(t *testing.T) {
	for _, jsonMode := range []bool{false, true} {
		c := mockChatClient(t, func(req chatRequest) (string, string) {
			encoded, _ := json.Marshal(req.ResponseFormat)
			if jsonMode && string(encoded) != `{"type":"json_object"}` || !jsonMode && req.ResponseFormat != nil {
				t.Fatalf("legacy chat format changed: %s", encoded)
			}
			return "ok", "stop"
		})
		if _, err := c.chatOnce(context.Background(), "system", "synthetic", 2048, jsonMode); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDailyExtractionDropsUnsupportedOptionalBackgroundAfterRepair(t *testing.T) {
	for _, background := range []string{"全天共有99段录音。", "\n## 待确认问题\n核实说话人身份。", "2024年讨论了合成背景。"} {
		calls := 0
		c := mockChatClient(t, func(chatRequest) (string, string) {
			calls++
			return syntheticDailyExtraction(background), "stop"
		})
		if note, err := c.dailyExtractChunk(context.Background(), "合成背景原文。", 2048); err != nil || note != "本部分仅有背景表达，未提取到具体事项。" || calls != 2 {
			t.Fatalf("unsupported optional background survived correction or prevented local empty selection: %q %v calls=%d", note, err, calls)
		}
	}
}

func TestDailyExtractionRepeatedBackgroundYearKeepsSelectedOriginals(t *testing.T) {
	const first = "[01-02 09:00:00.000 · 录音1/spk_0] 9月8日发布合成公告，未说明年份。"
	const second = "[01-02 10:00:00.000 · 录音2/spk_0] 测试甲须于1月4日15:00补齐缺少的4件，否则可能延期；没有额外采购承诺。"
	chunk := first + "\n" + second
	calls := 0
	c := mockChatClient(t, func(req chatRequest) (string, string) {
		calls++
		if req.MaxTokens != 2048 {
			t.Fatal("optional background recovery changed the output budget")
		}
		if calls == 2 && (!strings.Contains(req.Messages[1].Content, "材料未明示的年份：2024年") || !strings.Contains(req.Messages[1].Content, dailyExtractRepair)) {
			t.Fatal("optional background was dropped before attempting a specific correction")
		}
		return syntheticDailyExtraction("2024年发布了合成公告。", 2, 1), "stop"
	})
	note, err := c.dailyExtractChunk(context.Background(), chunk, 2048)
	if err != nil || calls != 2 || !strings.Contains(note, first) || !strings.Contains(note, second) ||
		strings.Contains(note, "2024") || strings.Contains(note, "背景表达") || strings.Index(note, first) > strings.Index(note, second) {
		t.Fatalf("optional background failure lost or changed validated originals: %q %v calls=%d", note, err, calls)
	}
}

func TestDailyExtractionBackgroundRecoveryStillRejectsInvalidFinalSelection(t *testing.T) {
	const chunk = "[01-02 09:00:00.000 · 录音1/spk_0] 测试甲须于次日交付合成清单。"
	for _, response := range []string{
		syntheticDailyExtraction("2024年合成背景。", 99),
		`{"background":"2024年合成背景。","actual_lines":[1],"extra":true}`,
		syntheticDailyExtraction(strings.Repeat("字", 321), 1),
	} {
		calls := 0
		c := mockChatClient(t, func(chatRequest) (string, string) {
			calls++
			if calls == 1 {
				return syntheticDailyExtraction("2024年合成背景。", 1), "stop"
			}
			return response, "stop"
		})
		if note, err := c.dailyExtractChunk(context.Background(), chunk, 2048); err == nil || note != "" || calls != 2 {
			t.Fatalf("invalid final schema or ID bypassed background recovery: %q %v calls=%d", note, err, calls)
		}
	}
}

func TestDailyExtractionRepairsUnsupportedYearWithSpecificFeedback(t *testing.T) {
	const chunk = "[01-02 09:00:00.000 · 录音1/spk_0] 录音中提到近期某测试平台发生事故，未给具体日期。"
	calls := 0
	c := mockChatClient(t, func(req chatRequest) (string, string) {
		calls++
		if req.MaxTokens != 2048 {
			t.Fatal("grounding correction changed the output budget")
		}
		if calls == 1 {
			return syntheticDailyExtraction("2024年测试平台发生事故。"), "stop"
		}
		prompt := req.Messages[1].Content
		if !strings.Contains(prompt, "输出校验发现") || !strings.Contains(prompt, "2024") ||
			!strings.Contains(prompt, "年份") || !strings.Contains(prompt, dailyExtractRepair) || !strings.Contains(prompt, chunk) {
			t.Fatal("correction lacked the unsupported year, original material, or JSON requirement")
		}
		return syntheticDailyExtraction("", 1), "stop"
	})
	note, err := c.dailyExtractChunk(context.Background(), chunk, 2048)
	if err != nil || calls != 2 || !strings.Contains(note, chunk) || strings.Contains(note, "2024") {
		t.Fatalf("specific year correction failed: %q %v calls=%d", note, err, calls)
	}
}

func TestDailyExtractionStopsAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	c := mockChatClient(t, func(chatRequest) (string, string) {
		calls++
		cancel()
		return syntheticDailyExtraction("合成背景。"), "stop"
	})
	if note, err := c.dailyExtractChunk(ctx, "合成背景。", 2048); err != context.Canceled || note != "" || calls != 1 {
		t.Fatalf("extraction ignored cancellation: %q %v calls=%d", note, err, calls)
	}
}

func TestDailyExtractionSkipsMetadataOnlyWithoutCallingModel(t *testing.T) {
	c := mockChatClient(t, func(chatRequest) (string, string) {
		t.Fatal("metadata-only chunk must not call the model")
		return "", "stop"
	})
	const metadata = "日期：2026-01-02\n时间段：01-02 00:00 - 01-02 18:00\n录音：3 段，共 9 分钟\n\n说明：合成来源说明。\n来源限制：不推断背景表达的现实主体。\n证据优先级：原文优先。\n### 录音 1 · 01-02 09:00:00\n原始转写（主证据）：\n"
	for _, chunk := range []string{metadata, "### 录音 40 · 01-02 17:50:00（1.0 分钟）\n", " \n\t "} {
		note, err := c.dailyExtractChunk(context.Background(), chunk, 2048)
		if err != nil || note != "本部分未包含可提炼原文。" {
			t.Fatalf("metadata-only chunk did not return a local explanation: %q %v", note, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if note, err := c.dailyExtractChunk(ctx, metadata, 2048); err != context.Canceled || note != "" {
		t.Fatalf("metadata skip ignored cancellation: %q %v", note, err)
	}
}

func TestDailySoundHintsStayAdjacentAndOutsideEvidence(t *testing.T) {
	const first = "[01-02 09:00:00.000 · 录音1/spk_0] 测试甲周五补齐4件。"
	const second = "[01-02 09:00:20.000 · 录音1/spk_0] 这句是引用表达。"
	chunk := first + "\n自动声音线索：Music（仅辅助，非场景证明）\n" + second + "\n"
	units := dailySourceUnits(chunk)
	if len(units) != 2 || units[0].audioHint != "" || !strings.Contains(units[1].audioHint, "Music") {
		t.Fatalf("sound hint changed source count or crossed utterances: %+v", units)
	}
	if prompt := dailyNumberedUnits(chunk, units); !strings.Contains(prompt, "[L2]\n自动声音线索：") {
		t.Fatalf("selection cannot see the auxiliary hint: %q", prompt)
	}
	note, err := renderDailyExtraction(chunk, syntheticDailyLabels("", "具体事项", "转述事项"))
	if err != nil || !strings.Contains(note, first) || !strings.Contains(note, second) || strings.Contains(note, "Music") || strings.Contains(note, "自动声音线索") {
		t.Fatalf("sound annotation leaked into source evidence: %q %v", note, err)
	}
	if got := dailySourceUnits("自动声音线索：Music\n"); len(got) != 0 {
		t.Fatalf("annotation-only chunk became an utterance: %+v", got)
	}
}
