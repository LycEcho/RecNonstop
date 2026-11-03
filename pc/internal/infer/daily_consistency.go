package infer

import (
	"regexp"
	"strings"
)

const dailyRiskContradictionIssue = "风险与异常章节同时列出具体风险并声称未发现明确风险；请依据原文逐项核对，保留有依据的风险及其条件，删除无依据的候选。正常前提、取消或未承诺本身不是风险；有具体风险时不得再写无限定的全局无风险结论。"

var (
	dailyRiskHeading = regexp.MustCompile(`^(#{1,6})\s+(.+?)\s*#*\s*$`)
	dailyRiskList    = regexp.MustCompile(`^(?:[-+*]\s+|[0-9]+[.)、]\s*)`)
	dailyRiskQuoted  = regexp.MustCompile("“[^”]*”|‘[^’]*’|「[^」]*」|『[^』]*』|\"[^\"]*\"|'[^']*'|`[^`]*`")
	dailyNoRisk      = regexp.MustCompile(`^(?:(?:总体而言|总体|整体|综上)[，,:：]?\s*)?(?:未发现|未识别到|没有发现|没有|暂无|无)(?:明确|任何|实际|新增)?风险(?:与异常|或异常)?$`)
	// This only recognizes explicit risk assertions, not every bullet placed
	// under the heading. A normal prerequisite alone is not a positive risk.
	// Explicit consequences and uncertainty are also candidate assertions in
	// this section, even when the model omits the word "risk". This only asks
	// for a source recheck; it does not declare those candidates supported.
	dailyRiskSignal         = regexp.MustCompile(`风险|库存不足|缺货|质检失败|设备故障|零件损坏|(?:将(?:会)?|可能)(?:导致|造成)|存在[^，。；：:]{0,12}不确定性|可能(?:导致|造成|发生)?(?:延期|延误|受伤|损失)`)
	dailyRiskDeniedBefore   = regexp.MustCompile(`(?:(?:未|没有|没)(?:发现|识别到|识别|报告|说明|确认|出现|发生|提及)[^，。；：:]{0,8}|(?:没有|无|不存在|不构成|并非|不属于|不是|不代表|已排除|已消除)[^，。；：:]{0,8}|(?:不应|不能|不得|不要)[^，。；：:]{0,16})$`)
	dailyRiskDeniedAfter    = regexp.MustCompile(`^\s*(?:[:：]\s*)?(?:不存在|已(?:经)?排除|已(?:经)?消除|不成立|未发生|尚未发生|未发现|没有|无|为零|排查|评估|核查|识别)`)
	dailyRiskDenialClause   = regexp.MustCompile(`^\s*(?:目前|当前|现阶段|暂时|尚|并|本次)*(?:未(?:发现|识别到|确认|报告|出现|发生)|没有|不存在|无|已(?:经)?排除|已(?:经)?消除)`)
	dailyRiskClauseSplit    = regexp.MustCompile(`[，,]|但(?:是)?|然而|不过`)
	dailyRiskResolved       = regexp.MustCompile(`^(?:目前|当前|现在|现)?(?:已(?:经)?(?:全部|完全)?(?:修复|解除)|(?:已(?:经)?)?恢复正常)(?:完成|完毕|了)?(?:并(?:已(?:经)?)?恢复正常)?$`)
	dailyRiskResolvedBefore = regexp.MustCompile(`已(?:经)?(?:全部|完全)?(?:修复|解除)(?:了|的)?$`)
	dailyRiskResolvedLabel  = regexp.MustCompile(`已(?:经)?(?:全部|完全)?解除(?:了|的)?(?:延期|交付|交货|安全|设备|质量|损失|受伤)(?:相关|方面|的)?$`)
	dailyRiskEmptyLabel     = regexp.MustCompile(`^\s*[:：]?\s*(?:(?:剧情)?原话|引用|引文|示例|模板文字)?\s*[:：]?\s*$`)
)

// contradictoryDailyRisks detects a narrow internal contradiction, not whether
// a model's risk is supported by reality or even by the source. The repair must
// recheck source support rather than simply retaining every listed candidate.
func contradictoryDailyRisks(output string) bool {
	inSection, positive, noRisk := false, false, false
	fence := ""
	for _, raw := range strings.Split(output, "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") {
			marker := line[:3]
			if fence == "" {
				fence = marker
			} else if fence == marker {
				fence = ""
			}
			continue
		}
		if fence != "" || strings.HasPrefix(line, ">") || strings.HasPrefix(raw, "    ") || strings.HasPrefix(raw, "\t") {
			continue
		}
		if heading := dailyRiskHeading.FindStringSubmatch(line); heading != nil {
			if len(heading[1]) <= 2 {
				if inSection && positive && noRisk {
					return true
				}
				inSection = len(heading[1]) == 2 && strings.TrimSpace(heading[2]) == "风险与异常"
				positive, noRisk = false, false
			}
			continue
		}
		if !inSection {
			continue
		}
		listed := dailyRiskList.MatchString(line)
		line = dailyRiskList.ReplaceAllString(line, "")
		if strings.HasPrefix(line, ">") {
			continue
		}
		// Quoted statements and code literals are not assertions by the report.
		line = dailyRiskQuoted.ReplaceAllString(line, "")
		line = strings.NewReplacer("**", "", "__", "").Replace(line)
		for _, sentence := range strings.FieldsFunc(line, func(r rune) bool {
			return strings.ContainsRune("。！？；;!?", r)
		}) {
			sentence = strings.TrimSpace(sentence)
			if dailyNoRisk.MatchString(sentence) {
				noRisk = true
			}
			if listed && affirmativeDailyRisk(sentence) {
				positive = true
			}
		}
	}
	return inSection && positive && noRisk
}

func affirmativeDailyRisk(sentence string) bool {
	clauses := dailyRiskClauseSplit.Split(sentence, -1)
	for i, clause := range clauses {
		clause = strings.TrimSpace(clause)
		for _, span := range dailyRiskSignal.FindAllStringIndex(clause, -1) {
			prefix := clause[:span[0]]
			// In "没有备用机且设备故障尚未修复", the negation concerns
			// the spare machine, not the new assertion immediately after 且.
			separateAssertion := strings.HasSuffix(strings.TrimSpace(prefix), "且")
			if !separateAssertion && (dailyRiskDenialClause.MatchString(prefix) || dailyRiskDeniedBefore.MatchString(prefix)) || dailyRiskDeniedAfter.MatchString(clause[span[1]:]) {
				continue
			}
			// A completed repair belongs only to this assertion. Do not skip
			// another risk elsewhere in the same item or after a contrast.
			after := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(clause[span[1]:]), "风险"))
			after = strings.TrimSpace(strings.TrimLeft(after, ":："))
			resolved := dailyRiskResolved.MatchString(after) || dailyRiskResolvedBefore.MatchString(strings.TrimSpace(clause[:span[0]]))
			if clause[span[0]:span[1]] == "风险" && dailyRiskResolvedLabel.MatchString(strings.TrimSpace(clause[:span[0]])) {
				resolved = true
			}
			// "设备故障，现已修复" is resolved too. Only accept a bare
			// completion clause, not a clause naming another object or risk.
			if !resolved && i+1 < len(clauses) && strings.TrimSpace(clause[span[1]:]) == "" {
				resolved = dailyRiskResolved.MatchString(strings.TrimSpace(clauses[i+1]))
			}
			if resolved {
				continue
			}
			// A generic "risk:" label does not turn an otherwise quoted or
			// empty item into a positive assertion. Concrete signals later in
			// the item can still qualify independently.
			if clause[span[0]:span[1]] == "风险" && strings.TrimSpace(prefix) == "" && dailyRiskEmptyLabel.MatchString(clause[span[1]:]) {
				continue
			}
			return true
		}
	}
	return false
}
