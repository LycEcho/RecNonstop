package infer

import "testing"

func TestDailyRiskSectionContradiction(t *testing.T) {
	const heading = "## 风险与异常\n"
	const positive = "- **补货延期风险**：若缺少的物料未补齐，可能导致延期。\n"
	const none = "未发现明确风险。"
	for _, tc := range []struct {
		name, report string
		want         bool
	}{
		{"listed risk then global denial", heading + positive + none, true},
		{"global denial then numbered risk", heading + none + "\n1. 库存不足，可能延期。", true},
		{"bold denial in bullet", heading + positive + "- **未发现明确风险**。", true},
		{"same bullet separate sentences", heading + "- 存在延期风险。未发现明确风险。", true},
		{"actual fault without risk label", heading + "- 设备故障已确认，需要维修。\n" + none, true},
		{"conditional not yet adverse", heading + "- 未补齐时可能延期，目前尚未延期。\n" + none, true},
		{"consequence without risk noun", heading + "- 若备用件未到，将导致维修暂缓。\n" + none, true},
		{"uncertainty candidate without risk noun", heading + "- 旧约定已撤销，存在交付不确定性。\n" + none, true},
		{"denied consequence", heading + "- 没有证据说明将造成损失。\n" + none, false},
		{"quoted consequence", heading + "- 教材写着“可能造成损失”，这是方法说明。\n" + none, false},
		{"other risks excluded", heading + positive + "未发现其他风险。", false},
		{"no additional risks", heading + positive + "除此之外，未发现明确风险。", false},
		{"local negative", heading + positive + "另一批物料未发现明确风险。", false},
		{"no risks only", heading + none, false},
		{"empty list", heading + "-\n" + none, false},
		{"pure negative bullets", heading + "- 未发现任何延期风险。\n- 没有设备故障。\n- 正常审批不构成风险。\n" + none, false},
		{"long pure denial", heading + "- 目前未发现任何明确且具体的质量或交付延期风险。\n" + none, false},
		{"resolved risk after noun", heading + "- 延期风险已排除。\n" + none, false},
		{"resolved risk before noun", heading + "- 已排除延期风险。\n" + none, false},
		{"risk inspection is not risk", heading + "- 风险排查：未发现问题。\n" + none, false},
		{"normal prerequisite only", heading + "- 签批完成之后才发货，当前继续等待。\n" + none, false},
		{"quoted risk", heading + "- 剧情对白：“存在延期风险，设备故障已经发生。”\n" + none, false},
		{"generic risk label with quote", heading + "- **风险**：剧情原话“存在延期风险”。\n" + none, false},
		{"explicit risk label with unquoted content", heading + "- 风险：运输途中可能碰撞。\n" + none, true},
		{"negation concerns a different object", heading + "- 没有备用机且设备故障尚未修复。\n" + none, true},
		{"quoted denial", heading + positive + "示例写着“未发现明确风险”。", false},
		{"block quote", heading + "> - 存在延期风险。\n" + none, false},
		{"list block quote", heading + "- > 存在延期风险。\n" + none, false},
		{"inline code", heading + "- `存在延期风险`只是模板文字。\n" + none, false},
		{"fenced example", heading + "```markdown\n" + positive + "```\n" + none, false},
		{"indented code", heading + "    - 存在延期风险。\n" + none, false},
		{"whole report quoted", "> ## 风险与异常\n> " + positive + "> " + none, false},
		{"other section", "## 主要事项\n" + positive + none, false},
		{"heading boundary", heading + positive + "## 今日概览\n" + none, false},
		{"higher heading boundary", heading + positive + "# 附录\n" + none, false},
		{"third level is not target", "### 风险与异常\n" + positive + none, false},
		{"separate repeated sections", heading + positive + heading + none, false},
		{"unlisted prose intentionally outside scope", heading + "存在延期风险。\n" + none, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := contradictoryDailyRisks(tc.report); got != tc.want {
				t.Fatalf("contradiction=%v, want %v: %s", got, tc.want, tc.report)
			}
		})
	}
}

func TestDailyResolvedRiskDoesNotHideActiveRisk(t *testing.T) {
	for _, tc := range []struct {
		name, items string
		want        bool
	}{
		{"historical fault repaired", "- 昨天的设备故障已经修复。", false},
		{"fault repaired", "- 设备故障已修复。", false},
		{"fully repaired", "- 设备故障已完全修复。", false},
		{"repair before fault", "- 已经修复设备故障。", false},
		{"risk cleared", "- 延期风险已解除。", false},
		{"cleared before risk", "- 已解除延期风险。", false},
		{"back to normal", "- 设备故障已恢复正常。", false},
		{"normal without already", "- 设备故障恢复正常。", false},
		{"repaired and normal", "- 设备故障已修复并恢复正常。", false},
		{"comma repair", "- 昨天的设备故障，现已修复。", false},
		{"comma cleared", "- 延期风险，目前已经解除。", false},
		{"comma normal", "- 设备故障，已恢复正常。", false},
		{"not repaired", "- 设备故障尚未修复。", true},
		{"repair ongoing", "- 设备故障正在修复。", true},
		{"repair planned", "- 设备故障计划明天修复。", true},
		{"partially repaired", "- 设备故障只修复了一部分。", true},
		{"completed phrase with partial qualifier", "- 设备故障已经修复一部分。", true},
		{"risk not cleared", "- 延期风险尚未解除。", true},
		{"different item still active", "- 昨天的设备故障已经修复。\n- 库存不足，若未补齐可能延期。", true},
		{"different sentence still active", "- 设备故障已经修复。另有延期风险仍在。", true},
		{"same sentence comma active", "- 设备故障已修复，但库存不足，可能延期。", true},
		{"same clause contrast active", "- 设备故障已修复但延期风险仍在。", true},
		{"normal but risk persists", "- 设备故障已恢复正常，不过风险仍然存在。", true},
		{"cleared but other active risk", "- 已解除设备故障风险但交付延期风险仍在。", true},
		{"previous exclusion does not swallow contrast", "- 已排除延期风险但库存不足仍需处理。", true},
		{"negative first clause positive next", "- 没有设备故障但是延期风险仍在。", true},
		{"bare completion then different risk", "- 设备故障，现已修复，然而库存不足可能延期。", true},
		{"other object repaired", "- 设备故障，已修复另一台仪器。", true},
		{"nonlocal resolution", "- 设备故障仍未解决，另一项延期风险已解除。", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report := "## 风险与异常\n" + tc.items + "\n未发现明确风险。"
			if got := contradictoryDailyRisks(report); got != tc.want {
				t.Fatalf("contradiction=%v, want %v: %s", got, tc.want, report)
			}
		})
	}
}
