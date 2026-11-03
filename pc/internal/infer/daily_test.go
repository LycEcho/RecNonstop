package infer

import (
	"context"
	"strings"
	"testing"
)

func TestDailyValidationUsesMaterialInsteadOfInstructionExamples(t *testing.T) {
	const source = "录音：2 段，共6分钟\n库存8件，缺4件，若周五未补齐可能延期。"
	const corrected = "## 今日概览\n录音2段，共6分钟。\n库存8件，缺4件，若周五未补齐可能延期。"
	for _, tc := range []struct {
		name, instruction, unsupported string
	}{
		{"count", "禁止输出示例：全天99段录音。", "全天99段录音。"},
		{"identity", "示例“核实说话人身份”属于禁止新增内容。", "## 待确认问题\n需要核实说话人身份。"},
		{"diagnosis", "不得擅自断言笔误、口误、单位错误或数值异常。", "数值范围异常，可能存在笔误。"},
		{"calendar_year", "禁止补写示例中的2023年或2024年。", "2024年发生了库存问题。"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			c := mockChatClient(t, func(req chatRequest) (string, string) {
				calls++
				if calls == 1 {
					return tc.unsupported, "stop"
				}
				if !strings.Contains(req.Messages[1].Content, "输出校验发现") || !strings.Contains(req.Messages[1].Content, source) {
					t.Fatal("correction retry lost source or validation feedback")
				}
				return corrected, "stop"
			})
			out, err := c.dailyChat(context.Background(), dailyReportPrompt(source)+"\n"+tc.instruction, source, 2048)
			if err != nil || calls != 2 || out != corrected {
				t.Fatalf("instruction authorized unsupported output: out=%q err=%v calls=%d", out, err, calls)
			}
		})
	}
}

func TestDailyCorrectionFeedbackCannotAuthorizeErrorDiagnosis(t *testing.T) {
	const source = "清单记为12件，待测试负责人核对。"
	calls := 0
	c := mockChatClient(t, func(req chatRequest) (string, string) {
		calls++
		if calls == 2 && !strings.Contains(req.Messages[1].Content, "错误诊断") {
			t.Fatal("error diagnosis did not enter the existing correction retry")
		}
		return "可能存在笔误或单位错误。", "stop"
	})
	out, err := c.dailyChat(context.Background(), dailyReportPrompt(source), source, 2048)
	if err == nil || out != "" || calls != 2 {
		t.Fatalf("repair feedback authorized the repeated unsupported claim: out=%q err=%v calls=%d", out, err, calls)
	}
}

func TestDailyKeepsSourceSupportedErrorDiagnosisAndBusinessRisk(t *testing.T) {
	const source = "原文明示清单有笔误，正确数量为12件。现有8件，缺4件，测试甲须周五补齐，否则可能延期。"
	calls := 0
	c := mockChatClient(t, func(chatRequest) (string, string) {
		calls++
		return source, "stop"
	})
	out, err := c.GenerateDailyReport(context.Background(), source)
	if err != nil || out != source || calls != 1 {
		t.Fatalf("supported diagnosis or real task/risk was removed: out=%q err=%v calls=%d", out, err, calls)
	}
}

func TestDailySoundHintCannotAuthorizeCalendarYear(t *testing.T) {
	const material = "日期：2026-01-02\n录音：1 段，共1分钟\n自动声音线索：2024年音乐（仅辅助，非场景证明）\n[01-02 09:00:00.000 · 录音1/spk_0] 提到9月8日公告，没有说明年份。"
	calls := 0
	c := mockChatClient(t, func(req chatRequest) (string, string) {
		calls++
		if calls == 1 {
			return "录音中提到2024年9月8日公告。", "stop"
		}
		if !strings.Contains(req.Messages[1].Content, "材料未明示的年份") {
			t.Fatal("sound label authorized a year absent from original speech")
		}
		return "录音中提到9月8日公告，年份未明。", "stop"
	})
	out, err := c.GenerateDailyReport(context.Background(), material)
	if err != nil || calls != 2 || strings.Contains(out, "2024") {
		t.Fatalf("auxiliary label became a fact: %q %v calls=%d", out, err, calls)
	}
}

func TestDailyKeepsReportedExternalEventAndSourceYear(t *testing.T) {
	const source = "录音中提到2026年近期某测试平台发生事故，并遭到攻击；未给出具体日期，也未安排听者任务。"
	calls := 0
	c := mockChatClient(t, func(chatRequest) (string, string) {
		calls++
		return source, "stop"
	})
	out, err := c.GenerateDailyReport(context.Background(), source)
	if err != nil || out != source || calls != 1 {
		t.Fatalf("source-supported year or reported external event rejected: %q %v calls=%d", out, err, calls)
	}
}

func TestDailyRiskContradictionUsesExistingSingleRepair(t *testing.T) {
	const source = "库存缺少2件，若周五前未补齐，可能延期。签批前不发货只是正常前提，没有新交付承诺。"
	const contradictory = "## 风险与异常\n- 库存不足，存在延期风险。\n- 缺少承诺的风险。\n未发现明确风险。"
	const corrected = "## 风险与异常\n- 库存缺少2件，若周五前未补齐，可能延期。"
	for _, staysContradictory := range []bool{false, true} {
		t.Run(map[bool]string{false: "source checked repair", true: "no third attempt"}[staysContradictory], func(t *testing.T) {
			calls := 0
			c := mockChatClient(t, func(req chatRequest) (string, string) {
				calls++
				if calls == 1 {
					return contradictory, "stop"
				}
				prompt := req.Messages[1].Content
				if !strings.Contains(prompt, dailyRiskContradictionIssue) || !strings.Contains(prompt, source) || !strings.Contains(prompt, "正常前提、取消或未承诺本身不是风险") {
					t.Fatal("risk repair failed to retain source and verify each candidate's evidence")
				}
				if staysContradictory {
					return contradictory, "stop"
				}
				return corrected, "stop"
			})
			out, err := c.dailyChat(context.Background(), dailyReportPrompt(source), source, 2048)
			if calls != 2 || (err != nil) != staysContradictory {
				t.Fatalf("unexpected retry budget: calls=%d out=%q err=%v", calls, out, err)
			}
			if staysContradictory && out != "" || !staysContradictory && out != corrected {
				t.Fatalf("contradictory or rewritten output escaped: %q", out)
			}
		})
	}
}

func TestDailyOtherRiskDisclaimerNeedsNoRepair(t *testing.T) {
	const source = "库存不足可能延期，未说明其他风险。"
	const report = "## 风险与异常\n- 库存不足可能延期。\n未发现其他风险。"
	calls := 0
	c := mockChatClient(t, func(chatRequest) (string, string) {
		calls++
		return report, "stop"
	})
	out, err := c.dailyChat(context.Background(), dailyReportPrompt(source), source, 2048)
	if err != nil || calls != 1 || out != report {
		t.Fatalf("qualified disclaimer was rejected: calls=%d out=%q err=%v", calls, out, err)
	}
}

func TestDailyMetadataEventDateUsesOriginalSourceForRepair(t *testing.T) {
	const source = "日期：2028-06-17\n录音：1 段，共 2 分钟\n\n### 录音 1 · 06-17 09:00:00\n原始转写（主证据）：\n刚刚发布了合成产品，未说明具体发布日期。"
	const invented = "## 主要事项\n- 合成产品于 2028 年 6 月 17 日发布。"
	const corrected = "## 主要事项\n- 录音中提到合成产品刚刚发布，具体发布日期未明。"
	for _, repeats := range []bool{false, true} {
		calls := 0
		c := mockChatClient(t, func(req chatRequest) (string, string) {
			calls++
			if calls == 1 {
				return invented, "stop"
			}
			if !strings.Contains(req.Messages[1].Content, source) || !strings.Contains(req.Messages[1].Content, "录音或报告日期被用作事件日期") {
				t.Fatal("event-date repair lost original source or issue")
			}
			if repeats {
				return invented, "stop"
			}
			return corrected, "stop"
		})
		out, err := c.dailyChat(context.Background(), dailyReportPrompt(source), source, 2048)
		if calls != 2 || (err != nil) != repeats || repeats && out != "" || !repeats && out != corrected {
			t.Fatalf("metadata date escaped grounding or exceeded retry budget: repeats=%v calls=%d out=%q err=%v", repeats, calls, out, err)
		}
	}
}

func TestDailyResolvedExceptionNeedsNoRepair(t *testing.T) {
	const source = "昨天设备发生故障，现已完全修复并恢复正常，目前未发现明确风险。"
	const report = "## 风险与异常\n- 昨天的设备故障已经修复。\n未发现明确风险。"
	calls := 0
	c := mockChatClient(t, func(chatRequest) (string, string) {
		calls++
		return report, "stop"
	})
	out, err := c.dailyChat(context.Background(), dailyReportPrompt(source), source, 2048)
	if err != nil || calls != 1 || out != report {
		t.Fatalf("resolved historical exception triggered repair: calls=%d out=%q err=%v", calls, out, err)
	}
}

func TestDailyResolvedExceptionDoesNotSuppressOtherRiskRepair(t *testing.T) {
	const source = "昨天设备故障已经修复，但库存不足，未补齐可能延期。"
	const contradictory = "## 风险与异常\n- 昨天的设备故障已经修复，但库存不足，未补齐可能延期。\n未发现明确风险。"
	const corrected = "## 风险与异常\n- 昨天的设备故障已经修复。\n- 库存不足，未补齐可能延期。"
	calls := 0
	c := mockChatClient(t, func(req chatRequest) (string, string) {
		calls++
		if calls == 1 {
			return contradictory, "stop"
		}
		if !strings.Contains(req.Messages[1].Content, dailyRiskContradictionIssue) {
			t.Fatal("resolved exception hid a separate active risk")
		}
		return corrected, "stop"
	})
	out, err := c.dailyChat(context.Background(), dailyReportPrompt(source), source, 2048)
	if err != nil || calls != 2 || out != corrected {
		t.Fatalf("active risk not checked after a repair: calls=%d out=%q err=%v", calls, out, err)
	}
}
