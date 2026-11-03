package infer

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestCountsImmediatelyAfterRecordingNounNeedEvidence(t *testing.T) {
	for _, output := range []string{"共记录48条，每条复核已完成。", "共录音48段。"} {
		if len(unsupportedRecordingCounts("记录01：巡检正常。记录48：巡检正常。", output)) == 0 {
			t.Fatalf("record IDs authorized invented statistics: %s", output)
		}
	}
	if issues := unsupportedRecordingCounts("共记录48条，巡检正常。", "共记录48条。"); len(issues) > 0 {
		t.Fatalf("explicit count rejected: %v", issues)
	}
	if issues := unsupportedRecordingCounts("备用阀门已复核3次，全部通过。", "已复核3次，全部通过。"); len(issues) > 0 {
		t.Fatalf("business count treated as a recording statistic: %v", issues)
	}
	if issues := unsupportedRecordingCounts("录音：2 段，共6分钟", "当天共记录2段录音。"); len(issues) > 0 {
		t.Fatalf("recorded audio count rejected: %v", issues)
	}
}

func TestRecordingStatisticsRequireExplicitSourceCounts(t *testing.T) {
	cases := []struct {
		name, source, output string
		invalid              bool
	}{
		{"indexed records are not an explicit total", "记录01：正常。记录38：正常。记录48：正常。", "对38个测试巡检记录进行了复核。", true},
		{"declared total", "录音：40 段，共120分钟", "全天共40段录音", false},
		{"invented group", "录音：40 段，共120分钟", "09:01–09:21（21段，共63分钟）", true},
		{"Chinese numerals", "明确复核了三十八条巡检记录。", "复核了38个巡检记录。", false},
		{"actual zero", "今天共有零段录音", "今天0段录音", false},
		{"record and audio totals differ", "录音：2 段，共6分钟", "复核了2条巡检记录", true},
		{"business quantities untouched", "库存8件，缺4件，预算1280元，1月5日17:00前分三段付款。", "库存8件，缺4件，预算1280元，1月5日17:00前分三段付款。", false},
		{"recording number cannot ground a total", "录音01：核对5个配件", "全天5段录音", true},
		{"recording reference with business count", "核对5个配件并返修", "录音中要求核对5个配件并返修", false},
		{"business objects are not record counts", "5个配件的检验记录", "5条记录", true},
		{"payment installments are not audio groups", "明日10:00前支付1200元（3段，每段400元）", "明日10:00前支付1200元（3段，每段400元）", false},
		{"business requirements in numbered recording", "录音01：11月5日的2条要求", "全天2段录音", true},
		{"leading-zero ordinal", "录音01段内容正常", "全天1段录音", true},
		{"partial totals cannot justify all-day count", "上午录音共5段，下午录音共6段", "全天5段录音", true},
		{"another time group cannot ground count", "09:00–10:00（5段录音）", "15:00–16:00（5段）", true},
		{"matching time group", "09:00–10:00（5段录音）", "- **9:00–10:00（5段）**", false},
		{"seconds cannot bypass time-group check", "录音：40段，共120分钟", "- **09:22:00 - 09:39:00（17段）**", true},
		{"zero seconds normalize to minutes", "09:00–10:00（5段录音）", "- **9:00:00–10:00:00（5段）**", false},
		{"matching day-part scope", "上午录音共5段，下午录音共6段", "上午共有5段录音", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := unsupportedRecordingCounts(tc.source, tc.output)
			if (len(got) > 0) != tc.invalid {
				t.Fatalf("issues=%v invalid=%v", got, tc.invalid)
			}
		})
	}
}

func TestUnidentifiedSpeakerIsNotAutomaticallyAVerificationTask(t *testing.T) {
	report := "## 主要事项\n- **待确认问题**：\n  - 未明确录音1/spk_0与录音2/spk_0是否为同一人。\n## 待办\n未发现明确待办"
	for _, source := range []string{
		"两段录音的spk_0未命名，没有证据表明他们是同一人。",
		"不需要核实两个spk_0是否为同一人。",
	} {
		if !unsupportedIdentityQuestion(source, report) {
			t.Fatal("invented identity-verification question was accepted")
		}
	}
	if unsupportedIdentityQuestion("已明确请测试甲核实录音1/spk_0与录音2/spk_0是否同一人。", report) {
		t.Fatal("explicit identity-verification task was removed")
	}
	if unsupportedIdentityQuestion("未确认说话人", "## 主要事项\n两段录音没有证据证明是同一人。\n## 待办\n未发现明确待办") {
		t.Fatal("source-label uncertainty was mistaken for an assigned task")
	}
	for _, denial := range []string{"不列为待确认问题", "不列入待确认问题", "不作为待确认事项", "未被明示为待确认事项"} {
		if unsupportedIdentityQuestion("未确认说话人", "- **待确认问题**：没有证据表明两个spk_0是同一人，"+denial+"。") {
			t.Fatalf("explicit rejection of a verification question was mistaken for a task: %s", denial)
		}
	}
	if !unsupportedIdentityQuestion("未确认说话人", "- **待确认问题**：两人身份不列入待确认问题，但需要测试甲核实两人身份。") {
		t.Fatal("affirmative verification request hidden behind a negation was accepted")
	}
}

func TestCalendarYearsRequireExplicitSourceYears(t *testing.T) {
	for _, tc := range []struct {
		name, source, output string
		issues               int
	}{
		{"month day cannot imply year", "3月5日交付，11月2日复核。", "2023年3月5日交付，2024年11月2日复核。", 2},
		{"explicit year", "计划在2024年交付。", "2024年交付。", 0},
		{"hyphen date grounds year", "交付日为2024-03-05。", "2024年3月5日交付。", 0},
		{"slash date grounds year", "日期为1999/03/05。", "1999年记录。", 0},
		{"invented standard dates", "3月5日交付。", "2023-03-05交付，2024/11/02复核。", 2},
		{"each year needs support", "2024年复核。", "2023年交付，2024年复核。", 1},
		{"Chinese round zero", "二〇二三年三月五日交付。", "2023-03-05交付。", 0},
		{"year alone cannot supply month day", "二〇二三年交付。", "2023-03-05交付。", 1},
		{"Chinese zero", "2023年交付。", "二零二三年交付。", 0},
		{"Chinese unsupported", "11月2日复核。", "二〇二四年复核。", 1},
		{"Chinese twentieth century", "一九九八年记录。", "1998年记录。", 0},
		{"business quantities unchanged", "库存2023件，预算1999元，型号X2024。", "库存2023件，预算1999元，型号X2024。", 0},
		{"quantities cannot ground a year", "库存2023件，型号X2024。", "2023年生产，2024年交付。", 2},
		{"longer number is not a year", "编号12024。", "编号12024年批次，序号22023-03-05。", 0},
		{"deduplicate year forms", "3月5日交付。", "2024年，2024-03-05，二〇二四年。", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := unsupportedCalendarYears(tc.source, tc.output); len(got) != tc.issues {
				t.Fatalf("unsupported years=%v, want %d issues", got, tc.issues)
			}
		})
	}
}

func TestReportYearCannotCompleteUnrelatedEventDate(t *testing.T) {
	source := "日期：2027-02-03。录音提到7月12日的公告，年份未明确；检查期限为2027年8月9日。"
	for _, output := range []string{
		"录音提到2027年7月12日的公告。",
		"公告日期为2027/07/12。",
		"检查期限为2027年7月12日。",
	} {
		if len(unsupportedCalendarYears(source, output)) == 0 {
			t.Fatalf("report year authorized an unrelated date: %q", output)
		}
	}
	for _, output := range []string{
		"报告日期为2027年2月3日；录音提到7月12日的公告，年份未明确。",
		"检查期限：二〇二七年八月九日。",
		"检查期限：2027/08/09。",
	} {
		if issues := unsupportedCalendarYears(source, output); len(issues) != 0 {
			t.Fatalf("legitimate date formatting rejected: %q: %v", output, issues)
		}
	}
}

func TestCalendarDateGuardDistinguishesBusinessQuantitiesAndExplicitDates(t *testing.T) {
	for _, tc := range []struct {
		name, source, output string
		unsupported          bool
	}{
		{"monthly quantity is not a day", "2026年4月共有5个项目完成。", "2026年4月5个项目完成。", false},
		{"monthly quantity with spacing", "2026年4月共有5个项目完成。", "2026年4月5 个项目完成。", false},
		{"Chinese monthly quantity", "2026年4月新增五项工作。", "2026年4月五项工作已登记。", false},
		{"quantity cannot authorize date", "2026年4月5个项目完成。", "完成日期是2026年4月5日。", true},
		{"quantity cannot authorize date at delimiter", "2026年4月5个项目完成。", "完成日期：2026年4月5。", true},
		{"explicit Chinese date", "2026年4月5号完成。", "完成日期为2026-04-05。", false},
		{"markerless date at delimiter", "完成日期：2026年4月5。", "完成日期为2026年4月5日。", false},
		{"spaced slash date", "交付日为2026 / 04 / 05。", "交付日为2026年4月5日。", false},
		{"spaced hyphen date", "交付日为2026 - 04 - 05。", "交付日为2026年4月5日。", false},
		{"spaced date output", "2026年4月5日交付。", "交付日为2026 / 04 / 05。", false},
		{"spaced unsupported year", "4月5日交付，年份未明。", "交付日为2026 / 04 / 05。", true},
		{"report year does not complete event date", "日期：2026 / 04 / 01。另提到4月5日交付，年份未明。", "交付日为2026 / 04 / 05。", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			issues := unsupportedCalendarYears(tc.source, tc.output)
			if (len(issues) > 0) != tc.unsupported {
				t.Fatalf("unsupported=%v, want %v: %v", len(issues) > 0, tc.unsupported, issues)
			}
		})
	}
}

func TestErrorDiagnosesRequireCorrespondingSourceSupport(t *testing.T) {
	for _, tc := range []struct {
		name, source, output string
		unsupported          bool
	}{
		{"numbers alone do not imply errors", "数值从120到3000，预算800元。", "数值范围异常，可能笔误或单位错误。", true},
		{"numeric anomaly", "记录数值120和3000。", "数字跨度明显不合理。", true},
		{"unsupported typo", "清单写的是1200件。", "1200可能是笔误。", true},
		{"unsupported speech slip", "交付量为1200件。", "可能存在口误。", true},
		{"unsupported transcription error", "测量值为320。", "这可能是转写错误。", true},
		{"unsupported unit conversion", "数值为8，单位为米。", "单位换算有误。", true},
		{"explicit typo", "这里1200是笔误，应为120。", "已明确1200是笔误，应为120。", false},
		{"explicit unit correction", "这里写错了单位，应为毫米。", "单位标注错误，应为毫米。", false},
		{"explicit short unit correction", "这里单位错了，应为毫米。", "单位错误，应为毫米。", false},
		{"explicit speech slip", "刚才是口误，应为12件。", "已说明口误并改为12件。", false},
		{"explicit transcription error", "这一处转写有误，原话为12件。", "存在转写错误，原话为12件。", false},
		{"explicit numeric anomaly", "仪器数值范围异常，正在检查。", "记录了数值范围异常。", false},
		{"one diagnosis cannot justify another", "明确是口误，应为12件。", "存在单位错误。", true},
		{"numeric anomaly does not establish typo", "仪器数值范围异常。", "可能是笔误。", true},
		{"source denial is not support", "没有证据表明是笔误或单位错误。", "可能存在笔误。", true},
		{"simple source denial is not support", "核对后确认没有笔误。", "可能是笔误。", true},
		{"denial of all guesses", "数值120和3000。", "未发现笔误、口误或单位错误，不能据此认定转写有误。", false},
		{"simple list denial", "数值120和3000。", "没有笔误、口误或单位错误。", false},
		{"no typo after checking", "清单为1200件。", "核对后无笔误。", false},
		{"do not infer anomaly", "记录数值120和3000。", "不应仅凭数值差异推断数值范围异常。", false},
		{"unsupported guess explicitly rejected", "清单为1200件。", "笔误的猜测没有依据，单位错误的说法不成立。", false},
		{"quoted guess explicitly rejected", "清单为1200件。", "“笔误”的说法不成立。", false},
		{"not a typo", "清单为1200件。", "这不是笔误。", false},
		{"cannot rule out is still a guess", "清单为1200件。", "不能排除笔误。", true},
		{"new assertion after denial", "数值为12。", "没有笔误，但可能存在单位错误。", true},
		{"double negation is assertion", "数值为12。", "并非没有笔误。", true},
		{"real inventory risk is unrelated", "库存8件，缺4件，若周五未补齐可能延期。", "库存8件，缺4件，若周五未补齐可能延期。", false},
		{"business anomaly is unrelated", "库存异常，缺4件。", "库存异常可能影响交付。", false},
		{"business numbers untouched", "预算1200元，3月5日17:00前分3次支付，每次400元。", "预算1200元，3月5日17:00前分3次支付，每次400元。", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := unsupportedErrorDiagnosis(tc.source, tc.output); got != tc.unsupported {
				t.Fatalf("unsupportedErrorDiagnosis = %v, want %v; source=%q output=%q", got, tc.unsupported, tc.source, tc.output)
			}
		})
	}
}

func TestAnalyzeRepairsInventedRecordCountOnce(t *testing.T) {
	calls := 0
	c := mockChatClient(t, func(req chatRequest) (string, string) {
		calls++
		if calls == 1 {
			return `{"summary":"对38个巡检记录进行了复核","actions":[],"risks":[]}`, "stop"
		}
		if !strings.Contains(req.Messages[1].Content, "输出校验发现") {
			t.Fatal("missing targeted repair feedback")
		}
		return `{"summary":"复核多条巡检记录，状态正常","actions":[],"risks":[]}`, "stop"
	})
	a, err := c.Analyze(context.Background(), &Transcript{Text: "记录01：正常。记录38：正常。"})
	if err != nil || calls != 2 || strings.Contains(a.Summary, "38") {
		t.Fatalf("repair result=%+v err=%v calls=%d", a, err, calls)
	}
}

func TestAnalyzeRejectsPersistentInventedCountDespiteRepairFeedback(t *testing.T) {
	calls := 0
	c := mockChatClient(t, func(chatRequest) (string, string) {
		calls++
		return `{"summary":"对38个巡检记录进行了复核","actions":[],"risks":[]}`, "stop"
	})
	_, err := c.Analyze(context.Background(), &Transcript{Text: "记录01：正常。记录38：正常。"})
	if err == nil || calls != 2 {
		t.Fatalf("unsupported statistic accepted: err=%v calls=%d", err, calls)
	}
}

func TestDailyRepairsUnsupportedCountsAndIdentityQuestions(t *testing.T) {
	calls := 0
	c := mockChatClient(t, func(chatRequest) (string, string) {
		calls++
		if calls == 1 {
			return "## 主要事项\n09:00–10:00（21段）\n- **待确认问题**：\n未明确两个说话人是否同一人。", "stop"
		}
		return "## 今日概览\n录音40段，共120分钟。\n## 主要事项\n多段录音复核记录。\n## 待办\n未发现明确待办\n## 风险与异常\n未发现", "stop"
	})
	result, err := c.GenerateDailyReport(context.Background(), "日期：2026-01-02\n录音：40段，共120分钟\n两个未命名说话人，只是各段的来源标签。")
	if err != nil || calls != 2 || strings.Contains(result, "21段") || strings.Contains(result, "待确认问题") {
		t.Fatalf("daily repair result=%q err=%v calls=%d", result, err, calls)
	}
}

func TestRecordRangesAndItemCountsRequireExplicitSourceStatistics(t *testing.T) {
	source := "记录01：巡检正常。记录48：巡检正常。记录90：巡检正常。"
	for _, output := range []string{
		"记录01至记录48均表示复核完成。",
		"记录49至记录90均表示复核完成。",
		"共记录89项，均显示状态正常。",
	} {
		issues := unsupportedRecordRanges(source, output)
		issues = append(issues, unsupportedRecordingCounts(source, output)...)
		if len(issues) == 0 {
			t.Fatalf("unsupported record statistic accepted: %s", output)
		}
	}
	if issues := unsupportedRecordRanges("记录01至记录90：巡检正常。", "记录01至记录90均表示复核完成。"); len(issues) > 0 {
		t.Fatalf("verbatim source range rejected: %v", issues)
	}
}

func TestRepairUnsupportedRecordStatisticsRemovesOnlyTheStatistic(t *testing.T) {
	source := "记录01：巡检正常。记录90：巡检正常。最后测试丙归档青色签字清单。"
	output := "记录01至记录48共48条均表明巡检正常，共记录89项；最后测试丙归档青色签字清单。"
	repaired := repairUnsupportedRecordStatistics(source, output)
	if strings.Contains(repaired, "记录01至记录48") || strings.Contains(repaired, "记录89项") {
		t.Fatalf("unsupported statistic survived: %s", repaired)
	}
	if !strings.Contains(repaired, "巡检正常") || !strings.Contains(repaired, "测试丙归档青色签字清单") {
		t.Fatalf("substantive content lost: %s", repaired)
	}
}

func TestPersonalEmotionAndExplicitNoActionLimits(t *testing.T) {
	source := "第一人称身份未确认。自动摘要认为说话人表现出心理不安、情感决绝。分段模型提取的候选风险：心理不安。"
	if !unsupportedPersonalEmotionInference(source, "背景为个人情感抒发。") {
		t.Fatal("unsupported personal emotion inference accepted")
	}
	if unsupportedPersonalEmotionInference(source, "背景为个人情感抒发，但身份未确认，旧心理推断仅为候选。") {
		t.Fatal("limited emotion statement rejected")
	}
	noAction := "没有人在进行设备评估，也没有实施电压测量，没有安排谁去测量。"
	if !unsupportedNegationDowngrade(noAction, "未说明是否已实施或由谁执行。") {
		t.Fatal("explicit no-action downgraded to unknown was accepted")
	}
	if unsupportedNegationDowngrade(noAction, "没有进行设备评估，也没有实施电压测量，没有安排测量。") {
		t.Fatal("explicit no-action statement rejected")
	}
	if unsupportedNegationDowngrade(noAction, "测量电压可判断电源是否稳定，但明确表示未进行设备评估，未实施电压测量。") {
		t.Fatal("negated '是否稳定' phrase was mistaken for unknown status")
	}
	if unsupportedNegationDowngrade(noAction, "未提及任何人在进行设备评估，未实施电压测量。") {
		t.Fatal("negative existential '未提及任何人在进行' was mistaken for unknown status")
	}
	if !unsupportedNegationDowngrade(noAction, "未涉及实际执行、测量操作。") {
		t.Fatal("weak '未涉及' replaced explicit no-action without explicit negation")
	}
	repaired := repairDailyExplicitNoAction(noAction, "## 今日概览\n未提及实际操作、执行人员或实施状态。\n\n## 主要事项\n未表明有实际设备评估行为、测量实施或责任分工。\n\n## 待办\n未发现明确待办")
	if strings.Contains(repaired, "未说明任何实际操作") || !strings.Contains(repaired, "没有进行实际操作") {
		t.Fatalf("weak no-action phrase not repaired: %s", repaired)
	}
	candidate := preserveDailyCandidateLimits("第一人称身份未确认。自动摘要认为心理不安、情感决绝。", "## 今日概览\n个人情感类描述。\n\n## 待办\n未发现明确待办")
	if !strings.Contains(candidate, "候选") || !strings.Contains(candidate, "心理不安") {
		t.Fatalf("candidate psych limitation not preserved: %s", candidate)
	}
}

func TestFirstPersonNamedSpeakerRepairsEmptyOwner(t *testing.T) {
	transcript := "这是合成录音。合成丁说：原定给客户寄20个样品的安排取消了。现在唯一确认的任务是我在2027年4月14日18:10前给合成戊发送7份电子规格表。合成戊只接收，不负责发送。"
	a := &Analysis{
		Summary: "现在唯一确认的任务是spk_0在2027年4月14日18:10前给合成戊发送7份电子规格表。",
		Actions: []Action{{Speaker: "spk_0", Action: "发送电子规格表", Details: "在2027年4月14日18:10前向合成戊发送7份电子规格表"}},
	}
	repairFirstPersonNamedOwner(a, transcript)
	if a.Actions[0].Owner != "合成丁" {
		t.Fatalf("first-person owner not resolved: %q", a.Actions[0].Owner)
	}
	if !strings.Contains(a.Summary, "合成丁") || strings.Contains(a.Summary, "spk_0") {
		t.Fatalf("summary still uses the bare source label: %q", a.Summary)
	}
}

func TestFirstPersonOwnerSurvivesSourceValidationReDecode(t *testing.T) {
	raw := `{"summary":"现在唯一确认的任务是spk_0在2027年4月14日18:10前给合成戊发送7份电子规格表。","speakers":["spk_0"],"actions":[{"speaker":"spk_0","owner":"","action":"发送电子规格表","details":"在2027年4月14日18:10前向合成戊发送7份电子规格表"}],"constraints":[],"risks":[]}`
	a := &Analysis{Raw: json.RawMessage(raw)}
	if err := json.Unmarshal([]byte(raw), a); err != nil {
		t.Fatal(err)
	}
	transcript := "合成丁说：原定寄20个样品的安排取消了。现在唯一确认的任务是我在2027年4月14日18:10前给合成戊发送7份电子规格表。合成戊只接收，不负责发送。"
	repairFirstPersonNamedOwner(a, transcript)
	if err := validateAnalysisSources(a, []string{"spk_0"}); err != nil {
		t.Fatal(err)
	}
	repairFirstPersonNamedOwner(a, transcript)
	if a.Actions[0].Owner != "合成丁" {
		t.Fatalf("owner lost after source re-decode: %q", a.Actions[0].Owner)
	}
}

func TestRepairProgressDenialAndCountBeforeRecord(t *testing.T) {
	out := "## 今日概览\n测试乙正在核对库存，当前进度为第8项，剩余4项。无实际任务安排、行动进展或风险提示。\n\n## 待办\n- 核对库存"
	repaired := repairDailyProgressDenial(out)
	if strings.Contains(repaired, "无实际任务安排") || !strings.Contains(repaired, "具体进度") {
		t.Fatalf("global progress denial survived: %s", repaired)
	}
	records := repairUnsupportedRecordStatistics("记录01：巡检正常。记录90：巡检正常。", "共涉及48条独立记录，均正常。")
	if strings.Contains(records, "48条独立记录") || !strings.Contains(records, "多条记录") {
		t.Fatalf("count-before-record statistic survived: %s", records)
	}
}
