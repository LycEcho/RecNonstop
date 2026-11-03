package infer

import (
	"regexp"
	"strconv"
	"strings"
)

const dailyUserAttributionIssue = "原文未明确支持将这些录音表达归属于用户或录音者本人；请保留具体信息与原来源，来源不明时写录音中提到或表达，不把背景、引用或不同录音的说话人合并为用户经历。原文明示的本人陈述仍须保留，不新增身份核实任务。"

var (
	dailyPersonalAttribution = regexp.MustCompile(`(?:用户|录音者)(?:本人)?(?:在录音中)?(?:明确)?(?:表达|表示|说|提到|自述|讲述|描述|倾诉|感到|感觉|经历)`)
	dailyExplicitSelf        = regexp.MustCompile(`(?:用户|录音者)本人(?:明确)?(?:说|表示|自述|表达|讲述|描述|录制)|已确认(?:本段)?(?:说话人|发言者|声音|录音来源)(?:是|为)(?:用户|录音者)本人`)
	dailyPersonalDenial      = regexp.MustCompile(`(?:不能|无法|不可|不应|不要|不得)(?:认定|确认|推断|断言|认为|写成|说)?[^。！？；\n]{0,8}$|(?:没有|缺乏|无)(?:证据|依据)(?:说明|表明|证明)?$|(?:不代表|并非|不是)$`)
	dailyPersonalRejected    = regexp.MustCompile(`^(?:的)?(?:说法|归属|判断)?(?:不成立|未获证实|没有依据|缺乏依据)`)
	dailySelfUnconfirmed     = regexp.MustCompile(`(?:的说法|的判断|的归属|本人身份)(?:尚|仍|目前)?(?:未(?:获|被)?证实|没有依据|缺乏依据|无法确认|未确认)`)
	dailyGenericUsers        = regexp.MustCompile(`(?:产品|软件|平台|应用|网站|部分|一些|许多|不少|多数|普通|多名|多位|广大|所有|其他|某位|有些)(?:的)?$|(?:调查|问卷|用户反馈|用户评价)[^。！？；\n]{0,8}$`)
	dailyProductUserContext  = regexp.MustCompile(`产品|软件|平台|应用|网站|功能|界面|使用体验|使用者|用户调查|用户反馈`)
	dailyAttributionScope    = regexp.MustCompile(`录音\s*([0-9]+)(?:\s*/\s*spk_[A-Za-z0-9_]+)?`)
	dailySourceUncertain     = regexp.MustCompile(`(?:来源|身份|第一人称身份)(?:仍|尚|无法|不能|未|未经|不明|不详)[^。！？；\n]{0,8}|(?:不能|无法)确认[^。！？；\n]{0,12}(?:用户|录音者)|没有证据说明[^。！？；\n]{0,12}(?:用户|录音者)`)
	dailyAttributionSplit    = regexp.MustCompile(`[。！？；;\n，,]|但是|但|然而|不过`)
)

func attributionScope(text string) string {
	match := dailyAttributionScope.FindStringSubmatch(text)
	if match == nil {
		return ""
	}
	n, err := strconv.Atoi(match[1])
	if err != nil {
		return ""
	}
	return strconv.Itoa(n)
}

func explicitPersonalSource(text string) bool {
	// Quoting the words "用户本人说" does not identify a real speaker.
	text = dailyRiskQuoted.ReplaceAllString(text, "")
	if dailySourceUncertain.MatchString(text) {
		return false
	}
	for _, clause := range dailyAttributionSplit.Split(text, -1) {
		for _, span := range dailyExplicitSelf.FindAllStringIndex(clause, -1) {
			if !dailyPersonalDenial.MatchString(clause[:span[0]]) && !dailySelfUnconfirmed.MatchString(clause[span[1]:]) {
				return true
			}
		}
	}
	return false
}

// unsupportedPersonalAttribution checks only explicit attribution of recorded
// speech/experience to the user. It does not infer scenes, identify speakers,
// or judge the truth of a statement. Source must exclude instructions/feedback.
func unsupportedPersonalAttribution(source, output string) bool {
	blocks := map[string]string{"": ""}
	current := ""
	for _, line := range strings.Split(source, "\n") {
		// A recording mentioned inside speech is not a source boundary.
		label := dailyUtteranceLabel.FindString(strings.TrimSpace(line))
		if dailyRecordingHeading.MatchString(strings.TrimSpace(line)) {
			label = line
		}
		if scope := attributionScope(label); scope != "" {
			current = scope
		}
		blocks[current] += line + "\n"
	}
	supported := make(map[string]bool)
	for scope, block := range blocks {
		if scope != "" || len(blocks) == 1 {
			supported[scope] = explicitPersonalSource(block)
		}
	}
	// Unscoped output cannot borrow a confirmed identity from just one of
	// several recordings. Every possible source must explicitly support it.
	unscopedSupported := len(supported) > 0
	for _, known := range supported {
		unscopedSupported = unscopedSupported && known
	}
	current, fence := "", ""
	for _, raw := range strings.Split(output, "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") {
			if fence == "" {
				fence = line[:3]
			} else if strings.HasPrefix(line, fence) {
				fence = ""
			}
			continue
		}
		if fence != "" || strings.HasPrefix(line, ">") {
			continue
		}
		if strings.HasPrefix(line, "#") {
			current = attributionScope(line)
		}
		line = dailyRiskQuoted.ReplaceAllString(line, "")
		for _, clause := range dailyAttributionSplit.Split(line, -1) {
			for _, span := range dailyPersonalAttribution.FindAllStringIndex(clause, -1) {
				prefix, suffix := clause[:span[0]], clause[span[1]:]
				if dailyPersonalDenial.MatchString(prefix) || dailyPersonalRejected.MatchString(suffix) || dailyGenericUsers.MatchString(prefix) {
					continue
				}
				// Repeating the same product-user claim does not newly identify
				// the recording owner. An unrelated use of 用户 grants nothing.
				claim := strings.TrimSpace(clause[span[0]:])
				if len([]rune(strings.TrimSpace(suffix))) >= 4 && dailyProductUserContext.MatchString(claim) && strings.Contains(dailyRiskQuoted.ReplaceAllString(source, ""), claim) {
					continue
				}
				scope := attributionScope(clause)
				if scope == "" {
					scope = current
				}
				if scope == "" && !unscopedSupported || scope != "" && !supported[scope] {
					return true
				}
			}
		}
	}
	return false
}
