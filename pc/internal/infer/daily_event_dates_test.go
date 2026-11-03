package infer

import (
	"strings"
	"testing"
)

func TestEventDateDoesNotBorrowRecordingDate(t *testing.T) {
	const relative = "日期：2027-04-12\n时间段：04-12 00:00 - 04-12 18:00\n录音：1 段\n\n### 录音 1 · 04-12 09:00:00\n原始转写（主证据）：\n[04-12 09:00:00.000 · 录音1/spk_0] 云帆工具刚刚发布，新增了离线查找。"
	for _, tc := range []struct {
		name, source, output string
		want                 bool
	}{
		{"release promoted from metadata", relative, "录音中提到云帆工具于 2027 年 4 月 12 日发布，增加离线查找。", true},
		{"date leading release", relative, "2027-04-12正式发布云帆工具。", true},
		{"occurrence promoted from metadata", relative, "开馆活动在2027年4月12日发生。", true},
		{"normalized Chinese date", relative, "云帆工具于二〇二七年四月十二日发布。", true},
		{"relative time preserved", relative, "录音提到云帆工具刚刚发布。", false},
		{"explicit source calendar date", relative + "\n公告明确写了发布日期：2027年4月12日。", "云帆工具于2027/04/12发布。", false},
		{"explicit spaced source date", relative + "\n公告明确写了发布日期：2027 年 4 月 12 日。", "云帆工具于2027年4月12日发布。", false},
		{"date in another source sentence intentionally not event matched", relative + "\n另一次活动日期是2027年4月12日。", "云帆工具于2027年4月12日发布。", false},
		{"month day cannot borrow report year", relative + "\n公告只写四月十二日。", "云帆工具于2027年4月12日发布。", true},
		{"report date alone", relative, "# 2027年4月12日录音日报\n录音提到工具发布。", false},
		{"own report publication", relative, "本次日报于2027年4月12日发布。", false},
		{"date of discussion", relative, "2027年4月12日记录到关于云帆工具发布的讨论。", false},
		{"date before recorded discussion", relative, "在2027年4月12日的录音中提到了刚刚发布的工具。", false},
		{"negative event date", relative, "云帆工具并非在2027年4月12日发布。", false},
		{"unknown event date", relative, "无法确认云帆工具于2027年4月12日发布。", false},
		{"source did not state event date", relative, "原文未说云帆工具于2027年4月12日发布。", false},
		{"event date cannot be substantiated", relative, "无法证实云帆工具于2027年4月12日发布。", false},
		{"question event date", relative, "云帆工具是否于2027年4月12日发布？", false},
		{"planned release not actual event", relative, "云帆工具计划于2027年4月12日发布。", false},
		{"date rejected after event", relative, "云帆工具于2027年4月12日发布的说法未获证实。", false},
		{"direct quote", relative, "示例写着“云帆工具于2027年4月12日发布”。", false},
		{"multiline quote", relative, "示例写着“云帆工具于2027年4月12日发布，\n增加了离线查找”。", false},
		{"block quote", relative, "> 云帆工具于2027年4月12日发布。", false},
		{"code block", relative, "```text\n云帆工具于2027年4月12日发布。\n```", false},
		{"inline code", relative, "模板：`云帆工具于2027年4月12日发布`。", false},
		{"full dated recording heading", "### 录音 1 · 2027-04-12 09:00:00\n原始转写：云帆工具刚刚发布。", "云帆工具于2027年4月12日发布。", true},
		{"full dated source label", "[2027-04-12 09:00:00.000 · 录音1/spk_0] 云帆工具刚刚发布。", "云帆工具于2027年4月12日发布。", true},
		{"label does not remove spoken date", "[2027-04-12 09:00:00.000 · 录音1/spk_0] 云帆工具于2027年4月12日发布。", "云帆工具于2027/4/12发布。", false},
		{"spoken metadata remains evidence", relative + "\n公告原文如下：\n日期：2027年4月12日\n发布云帆工具。", "云帆工具于2027年4月12日发布。", false},
		{"inline date is not framework only", "日期：2027年4月12日，云帆工具发布。", "云帆工具于2027年4月12日发布。", false},
		{"sound hint is not event evidence", relative + "\n自动声音线索：2027年4月12日 发布（自动辅助）", "云帆工具于2027年4月12日发布。", true},
		{"other absolute dates left to calendar guard", relative, "云帆工具于2027年4月11日发布。", false},
		{"no metadata left to calendar guard", "云帆工具刚刚发布。", "云帆工具于2027年4月12日发布。", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			issues := unsupportedEventDates(tc.source, tc.output)
			if (len(issues) > 0) != tc.want {
				t.Fatalf("issues=%v, want flagged=%v", issues, tc.want)
			}
		})
	}
}

func TestEventDateIssueDeduplicatesCalendarSpellings(t *testing.T) {
	const source = "日期：2027-04-12\n原始转写：云帆工具刚刚发布。"
	const output = "云帆工具于2027年4月12日发布。\n另一次活动于2027/04/12发生。"
	issues := unsupportedEventDates(source, output)
	if len(issues) != 1 || !strings.Contains(issues[0], "原文正文未明示该日") {
		t.Fatalf("expected one actionable date issue, got %v", issues)
	}
}
