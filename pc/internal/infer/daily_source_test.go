package infer

import (
	"strings"
	"testing"
)

func TestDailyEvidenceMaterialRemovesOnlyKnownModelsInSameRecording(t *testing.T) {
	const first = "### 录音 1 · 01-02 09:00:00\n" +
		"历史自动摘要（仅供参考，不是独立证据）：旧解释。\n" +
		"原始转写节选：这只是引用的合成表达，没有安排任务。\n" +
		"明确待办：[]\n" +
		"分段模型提取的候选风险（须核对原始转写，不代表已确认）：[\"旧推断\"]\n"
	const second = "### 录音 2 · 01-02 10:00:00\n" +
		"原始转写缺失：自动分析仅供参考，不代表已确认。\n" +
		"自动摘要（原始转写缺失，仅供参考）：保留降级参考。\n" +
		"分段模型提取的待办候选（须核对原文，不代表已确认）：[\"待核对\"]\n"
	input := "日期：2026-01-02\n" + first + second
	want := "日期：2026-01-02\n### 录音 1 · 01-02 09:00:00\n" +
		"原始转写节选：这只是引用的合成表达，没有安排任务。\n" + second
	if got := dailyEvidenceMaterial(input); got != want {
		t.Fatalf("wrong per-record evidence copy:\n%s", got)
	}
	if input != "日期：2026-01-02\n"+first+second {
		t.Fatal("stored input was changed")
	}
}

func TestDailyEvidenceMaterialRecognizesOriginalFormsAndPreservesLabels(t *testing.T) {
	for _, marker := range []string{"原始转写（主证据）：", "原始转写：", "原始转写节选：", "原始转写:"} {
		for _, separator := range []string{"", "\n"} {
			t.Run(marker+separator, func(t *testing.T) {
				const source = "[01-02 09:00:00.123 · 录音1/spk_0] 测试甲周五核对4件，否则可能延期。"
				input := "###录音 1\n自动摘要（需核对原文）：旧解释。\n" + marker + separator + source + "\n" +
					"模型待办候选：[\"旧任务\"]\n模型风险候选：[\"旧风险\"]\n" +
					"分段模型提取的限制候选（须核对原文，不代表已确认）：[\"旧限制\"]\n"
				want := "###录音 1\n" + marker + separator + source + "\n"
				if got := dailyEvidenceMaterial(input); got != want {
					t.Fatalf("source changed or model candidates leaked:\n%s", got)
				}
			})
		}
	}
}

func TestDailyEvidenceMaterialKeepsMissingAndEmptyOriginalReferences(t *testing.T) {
	for _, source := range []string{"", "原始转写：\n\n", "原始转写节选： \t\n", "原始转写（主证据）：\n[01-02 09:00:00.000 · 录音1/spk_0]\n"} {
		input := "### 录音 1\n历史自动摘要（仅供参考）：参考待核实。\n" + source +
			"分段模型提取的风险候选（不代表已确认）：[\"候选\"]\n"
		if got := dailyEvidenceMaterial(input); got != input {
			t.Fatalf("empty original removed fallback references: %q", got)
		}
	}
	const noRecording = "历史自动摘要：旧解释。\n原始转写节选：没有录音边界，不删。\n"
	if got := dailyEvidenceMaterial(noRecording); got != noRecording {
		t.Fatal("unscoped material was treated as a recording")
	}
}

func TestDailyEvidenceMaterialPreservesSpokenAndQuotedMetadata(t *testing.T) {
	const source = "原始转写（主证据）：\n" +
		"[01-02 09:00:00.000 · 录音1/spk_0] 历史自动摘要（仅供参考）：这是读出的字段名称。\n" +
		"风险：库存不足可能延期。\n明确待办：测试甲周五补齐4件。\n" +
		"> 模型风险候选：[\"引用，不删\"]\n" +
		"  模型待办候选：[\"缩进引用，不删\"]\n" +
		"“模型风险候选：[\"带引号，不删\"]”\n" +
		"他说：“\n模型风险候选：[\"跨行引用，不删\"]\n”\n" +
		"```text\n模型风险候选：[\"代码引用，不删\"]\n```\n"
	input := "### 录音 1\n历史自动摘要：框架旧摘要。\n" + source + "模型风险候选：[\"框架旧候选\"]\n"
	if got := dailyEvidenceMaterial(input); got != "### 录音 1\n"+source {
		t.Fatalf("spoken/quoted original was removed:\n%s", got)
	}
}

func TestDailyEvidenceMaterialKeepsAmbiguousUnmarkedMultilineSpeech(t *testing.T) {
	const source = "原始转写：\n下面逐行念一段配置说明。\n" +
		"模型风险候选：[\"这是可能被念出的字段\"]\n风险：测试风险。\n"
	input := "### 录音 1\n历史自动摘要：框架旧解释。\n" + source
	if got := dailyEvidenceMaterial(input); got != "### 录音 1\n"+source {
		t.Fatalf("ambiguous original was removed: %s", got)
	}
	// An empty original in a later recording must not inherit the earlier one.
	input += "### 录音 2\n原始转写：\n模型风险候选：[\"仍需保留的降级参考\"]\n"
	if !strings.HasSuffix(dailyEvidenceMaterial(input), "模型风险候选：[\"仍需保留的降级参考\"]\n") {
		t.Fatal("original presence leaked across recording boundaries")
	}
}

func TestDailyEvidenceMaterialPreservesExactBytesAndIsIdempotent(t *testing.T) {
	input := "### 录音 1\r\n自动摘要：旧内容。\r\n原始转写节选：合成原文。\r\n模型风险候选：[]"
	want := "### 录音 1\r\n原始转写节选：合成原文。\r\n"
	got := dailyEvidenceMaterial(input)
	if got != want || dailyEvidenceMaterial(got) != got {
		t.Fatalf("line endings changed or filtering was not idempotent: %q", got)
	}
}

func TestDailyEvidenceMaterialDoesNotJoinRecordsAcrossUnclosedQuotes(t *testing.T) {
	const first = "### 录音 1\n原始转写节选：一个没有结束的引用：“\n"
	const second = "### 录音 2\n自动摘要（仅供参考）：这个引用到这里结束。”\n" +
		"原始转写：\n模型风险候选：[\"此录音没有原文，不能跨块删除\"]\n"
	if got := dailyEvidenceMaterial(first + second); got != first+second {
		t.Fatalf("an unmatched source quotation crossed recording scopes: %s", got)
	}
}
