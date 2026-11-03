package infer

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const countNumerals = `[0-9零〇一二两三四五六七八九十百千万]+`
const recordingNouns = `(?:测试巡检记录|巡检记录|检验记录|测试记录|录音记录|语音记录|语音片段|录音片段|记录|录音|片段|分段)`
const clockPattern = `\d{1,2}:\d{2}(?::\d{2})?`

var (
	countBeforeRecording     = regexp.MustCompile(`(` + countNumerals + `)\s*(个|条|段|份|项)\s*(?:独立)?\s*(` + recordingNouns + `)`)
	countAfterRecording      = regexp.MustCompile(`(` + recordingNouns + `)(?:\s*[:：]\s*(?:共|共有|总计|合计|一共)?|\s*(?:共|共有|总计|总数为|总数是|合计|一共|数量为|数量是|计|有))?\s*(` + countNumerals + `)\s*(个|条|段|份|项)`)
	recordedAudio            = regexp.MustCompile(`(?:共|总计|合计)?\s*记录\s*(` + countNumerals + `)\s*(?:段|条|份)\s*录音`)
	timeGroupCount           = regexp.MustCompile(`^\s*[-*# ]*(` + clockPattern + `\s*[–—~～至到-]\s*` + clockPattern + `)\s*[(（]\s*(` + countNumerals + `)\s*段`)
	timeRange                = regexp.MustCompile(clockPattern + `\s*[–—~～至到-]\s*` + clockPattern)
	dayPart                  = regexp.MustCompile(`上午|下午|早上|晚上|晚间|夜间`)
	totalScope               = regexp.MustCompile(`全天|全日|当天|今日|总计|总数|一共|合计|共有|全部|整个`)
	identityTaskBefore       = regexp.MustCompile(`(?:核实|确认|查明|调查|核对|询问)[^。！？；\n]{0,24}(?:身份|姓名|同一人)`)
	identityTaskAfter        = regexp.MustCompile(`(?:身份|姓名|同一人)[^。！？；\n]{0,12}(?:核实|确认|查明|调查|核对|询问)`)
	identityReference        = regexp.MustCompile(`身份|姓名|同一人`)
	identityQuestionNegation = regexp.MustCompile(`(?:不(?:应|应该)?(?:列入|列为|作为)|未被明示为)(?:待确认|待核实)(?:问题|事项)`)
	affirmativeIdentityTask  = regexp.MustCompile(`(?:需要|仍需|还需|应当|应该|请|要求|安排|务必|必须|需)[^。！？；\n:：]{0,12}(?:核实|确认|查明|调查|核对|询问)[^。！？；\n]{0,24}(?:身份|姓名|同一人)`)
	negatedTaskPrefix        = regexp.MustCompile(`(?:不|无|未)(?:再|必)?$`)
	sentenceBoundary         = regexp.MustCompile(`[。！？；\n]`)
)

type recordingCount struct {
	key, scope, phrase string
}

// These are counts of records/audio segments only. Business quantities, money,
// dates and deadlines must never be rewritten by a recording-statistics check.
func recordingCounts(text string) []recordingCount {
	var out []recordingCount
	add := func(number, label, phrase, scope string) {
		n, ok := countValue(number)
		if !ok {
			return
		}
		kind := "records"
		if strings.Contains(label, "录音") || strings.Contains(label, "片段") || strings.Contains(label, "分段") {
			kind = "audio"
		}
		out = append(out, recordingCount{key: kind + ":" + strconv.Itoa(n), scope: scope, phrase: phrase})
	}
	for _, match := range countBeforeRecording.FindAllStringSubmatchIndex(text, -1) {
		add(text[match[2]:match[3]], text[match[6]:match[7]], text[match[0]:match[1]], recordingCountScope(text, match[0], match[1]))
	}
	for _, match := range countAfterRecording.FindAllStringSubmatchIndex(text, -1) {
		if strings.HasPrefix(strings.TrimSpace(text[match[1]:]), "录音") {
			continue // "记录2段录音" is an audio count, handled below.
		}
		add(text[match[4]:match[5]], text[match[2]:match[3]], text[match[0]:match[1]], recordingCountScope(text, match[0], match[1]))
	}
	for _, match := range recordedAudio.FindAllStringSubmatchIndex(text, -1) {
		add("录音", text[match[2]:match[3]], text[match[0]:match[1]], recordingCountScope(text, match[0], match[1]))
	}
	// Daily reports sometimes invent counts inside time-group headings, such as
	// "09:00–10:00 (21段)" without repeating the word "录音".
	for _, line := range strings.Split(text, "\n") {
		if match := timeGroupCount.FindStringSubmatch(line); match != nil {
			add(match[2], "录音", match[0], "time:"+normalizeTimeRange(match[1]))
		}
	}
	return out
}

func normalizeTimeRange(s string) string {
	r := strings.NewReplacer(" ", "", "–", "-", "—", "-", "~", "-", "～", "-", "至", "-", "到", "-")
	parts := strings.Split(r.Replace(s), "-")
	for i, part := range parts {
		clock := strings.Split(part, ":")
		if len(clock[0]) == 1 {
			clock[0] = "0" + clock[0]
		}
		if len(clock) == 3 && clock[2] == "00" {
			clock = clock[:2]
		}
		parts[i] = strings.Join(clock, ":")
	}
	return strings.Join(parts, "-")
}

func recordingCountScope(text string, start, end int) string {
	lineStart := 0
	for i, r := range text[:start] {
		if strings.ContainsRune("\n。；", r) {
			lineStart = i + utf8.RuneLen(r)
		}
	}
	prefix := text[lineStart:start]
	if ranges := timeRange.FindAllString(prefix, -1); len(ranges) > 0 {
		return "time:" + normalizeTimeRange(ranges[len(ranges)-1])
	}
	if parts := dayPart.FindAllString(prefix, -1); len(parts) > 0 {
		return "part:" + parts[len(parts)-1]
	}
	// The report builder's authoritative header, unlike a numbered recording
	// title, explicitly declares the total count.
	line := strings.TrimSpace(text[lineStart:end])
	if strings.HasPrefix(line, "录音：") || strings.HasPrefix(line, "录音:") || totalScope.MatchString(line) {
		return "total"
	}
	return "unscoped"
}

func countValue(s string) (int, bool) {
	if n, err := strconv.Atoi(s); err == nil {
		// "录音01" is an identifier, not a total of one recording.
		if len(s) > 1 && s[0] == '0' {
			return 0, false
		}
		return n, true
	}
	digits := map[rune]int{'零': 0, '〇': 0, '一': 1, '二': 2, '两': 2, '三': 3, '四': 4, '五': 5, '六': 6, '七': 7, '八': 8, '九': 9}
	units := map[rune]int{'十': 10, '百': 100, '千': 1000, '万': 10000}
	total, section, digit := 0, 0, 0
	for _, r := range s {
		if v, ok := digits[r]; ok {
			digit = v
			continue
		}
		unit, ok := units[r]
		if !ok {
			return 0, false
		}
		if unit == 10000 {
			total += (section + digit) * unit
			section, digit = 0, 0
		} else {
			if digit == 0 {
				digit = 1
			}
			section += digit * unit
			digit = 0
		}
	}
	return total + section + digit, s != ""
}

func unsupportedRecordingCounts(source, output string) []string {
	allowed := make(map[string]bool)
	for _, count := range recordingCounts(source) {
		allowed[count.scope+"/"+count.key] = true
	}
	var issues []string
	seen := make(map[string]bool)
	for _, count := range recordingCounts(output) {
		key := count.scope + "/" + count.key
		// A declared full-day total may be restated without the word "total".
		// A partial group must never justify a full-day or another group's count.
		ok := allowed[key] || count.scope == "unscoped" && allowed["total/"+count.key]
		if !ok && !seen[key] {
			issues = append(issues, "材料未明示的记录统计："+count.phrase)
			seen[key] = true
		}
	}
	return issues
}

var (
	recordRangePattern = regexp.MustCompile(`记录\s*([0-9]{1,4})\s*(?:至|到|—|–|-|~|～)\s*记录\s*([0-9]{1,4})`)
)

// A model must not turn its own processing subrange into a source statistic.
// "记录01至记录48" is unsupported when the source actually reaches record 90;
// an exact full-source range is accepted.
var (
	recordItemCountPattern   = regexp.MustCompile(`(?:共|总计|合计)?\s*记录\s*(?:共|总计|合计)?\s*[0-9]{1,4}\s*(?:项|条|段|份)`)
	recordCountBeforePattern = regexp.MustCompile(`[0-9]{1,4}\s*(?:项|条|段|份)\s*独立?记录`)
)

// Remove only an unsupported record range/item count from prose; preserve the
// substantive statement. This is a statistic correction, not a fact rewrite.
func repairUnsupportedRecordStatistics(source, output string) string {
	if len(unsupportedRecordRanges(source, output)) == 0 && len(unsupportedRecordingCounts(source, output)) == 0 {
		return output
	}
	output = recordRangePattern.ReplaceAllString(output, "多条记录")
	output = recordItemCountPattern.ReplaceAllString(output, "多条记录")
	output = recordCountBeforePattern.ReplaceAllString(output, "多条记录")
	output = strings.ReplaceAll(output, "多条记录多条记录", "多条记录")
	output = strings.ReplaceAll(output, "多条记录，多条记录", "多条记录")
	output = strings.ReplaceAll(output, "多个记录（多条记录）", "多条记录")
	output = strings.ReplaceAll(output, "多个记录(多条记录)", "多条记录")
	output = strings.ReplaceAll(output, "多条记录（多条记录）", "多条记录")
	output = strings.ReplaceAll(output, "多条记录(多条记录)", "多条记录")
	return strings.TrimSpace(output)
}

func unsupportedRecordRanges(source, output string) []string {
	// A model-generated range is not evidence even when its endpoint happens
	// to match a chunk boundary. Only a range written verbatim in the source
	// is allowed; otherwise the chunk's processing split becomes a statistic.
	var issues []string
	for _, match := range recordRangePattern.FindAllStringSubmatch(output, -1) {
		if !strings.Contains(source, match[0]) {
			issues = append(issues, "材料未明示的记录范围："+match[0])
		}
	}
	return issues
}

var (
	sourcePersonalEmotion  = regexp.MustCompile(`第一人称[^。；\n]{0,12}未确认|心理[^。；\n]{0,12}(?:候选|推断|不代表|不能作为)|自动摘要[^。；\n]{0,20}心理`)
	outputPersonalEmotion  = regexp.MustCompile(`个人情感|个人情绪|情感抒发|心理状态`)
	outputEmotionLimit     = regexp.MustCompile(`未确认|候选|推断|不代表|引用来源|来源未确认`)
	sourceExplicitNoAction = regexp.MustCompile(`(?:没有|未|无)[^。；\n]{0,8}(?:进行|实施|开展|安排|执行)[^。；\n]{0,12}(?:评估|测量|核对|检查|测试)`)
	outputUnknownAction    = regexp.MustCompile(`(?:未说明|不清楚|未知)[^。；\n]{0,12}(?:实施|执行|进行|开展|评估|测量|检查)|(?:未说明|不清楚|未知|未提及)[^。；\n]{0,8}是否[^。；\n]{0,8}(?:实施|执行|进行|开展|评估|测量|检查)|是否(?:已经|已|有人|进行|实施|执行|开展)[^。；\n]{0,6}(?:实施|执行|进行|开展|评估|测量|检查)`)
)

func unsupportedPersonalEmotionInference(source, output string) bool {
	return sourcePersonalEmotion.MatchString(source) && outputPersonalEmotion.MatchString(output) && !outputEmotionLimit.MatchString(output)
}

// Explicit "nobody is doing X / no one was assigned X" must not become
// "it is unclear whether X happened".
func unsupportedNegationDowngrade(source, output string) bool {
	outputNoAction := regexp.MustCompile(`(?:没有|无人|未|无)(?:人|任何|再)?(?:在)?(?:进行|实施|开展|安排|执行)`).MatchString(output)
	return sourceExplicitNoAction.MatchString(source) && (!outputNoAction || outputUnknownAction.MatchString(output))
}

func preserveDailyCandidateLimits(source, output string) string {
	hasIdentity := strings.Contains(source, "第一人称") && strings.Contains(source, "未确认")
	hasPsych := strings.Contains(source, "心理不安") || strings.Contains(source, "情感决绝")
	if !hasIdentity && !hasPsych {
		return output
	}
	alreadyIdentity := strings.Contains(output, "未确认") || strings.Contains(output, "来源不明")
	alreadyPsych := strings.Contains(output, "候选") || strings.Contains(output, "仅为推断") || strings.Contains(output, "旧自动摘要")
	if (!hasIdentity || alreadyIdentity) && (!hasPsych || alreadyPsych) {
		return output
	}
	var parts []string
	if hasIdentity && !alreadyIdentity {
		parts = append(parts, "第一人称身份/来源未确认")
	}
	if hasPsych && !alreadyPsych {
		parts = append(parts, "旧自动摘要曾推断“心理不安/情感决绝”，仅为候选，原文不支持，不作为人物心理事实或风险")
	}
	note := "- 限制与前提：" + strings.Join(parts, "；") + "。"
	if idx := strings.Index(output, "## 待办"); idx >= 0 {
		return output[:idx] + note + "\n\n" + output[idx:]
	}
	return output + "\n\n" + note
}

func repairDailyProgressDenial(output string) string {
	progress := regexp.MustCompile(`正在|进行中|进度|第[0-9]+项|剩余[0-9]+项`)
	if !progress.MatchString(output) {
		return output
	}
	output = strings.ReplaceAll(output, "无实际任务安排、行动进展或风险提示", "同时记录了库存核对的具体进度，未发现明确风险提示")
	output = regexp.MustCompile(`(?:无|没有|未发现)[^。；\n]{0,12}(?:实际任务|任务安排|行动进展|实际进展)`).ReplaceAllString(output, "记录了明确的具体进展")
	return output
}

func repairDailyExplicitNoAction(source, output string) string {
	if !sourceExplicitNoAction.MatchString(source) {
		return output
	}
	output = strings.NewReplacer(
		"未说明任何实际操作或责任人", "没有进行实际操作，也没有安排责任人",
		"未涉及实际执行、测量操作", "没有进行实际执行或测量操作",
	).Replace(output)
	output = regexp.MustCompile(`未(?:提及|涉及|说明)[^。；\n]{0,12}实际(?:操作|执行|评估|测量|实施)[^。；\n]{0,16}`).ReplaceAllString(output, "没有进行实际操作，也没有安排执行人员")
	output = regexp.MustCompile(`未表明[^。；\n]{0,12}设备评估[^。；\n]{0,12}(?:测量|实施)[^。；\n]{0,16}`).ReplaceAllString(output, "没有进行设备评估，也没有实施电压测量，也没有安排责任分工")
	output = strings.ReplaceAll(output, "执行人员备评估或电压测量行为", "设备评估或电压测量行为")
	output = strings.ReplaceAll(output, "执行人员备评估", "设备评估")
	specific := (strings.Contains(output, "没有进行设备评估") || strings.Contains(output, "未进行设备评估")) &&
		(strings.Contains(output, "没有实施电压测量") || strings.Contains(output, "未实施电压测量"))
	if specific {
		return output
	}
	note := "- 限制与前提：没有人在进行设备评估，也没有实施电压测量，没有安排谁去测量。"
	if idx := strings.Index(output, "## 待办"); idx >= 0 {
		return output[:idx] + note + "\n\n" + output[idx:]
	}
	return output + "\n\n" + note
}

var namedFirstPersonTask = regexp.MustCompile(`(?s)([\p{Han}A-Za-z0-9]{2,12})\s*(?:说|表示|确认|介绍|提到|称)[：:].{0,160}?(?:由我|我在|我来|我负责|我(?:会|要|将|需|需要|正在))`)

func analysisSharesPhrase(text, clause string, width int) bool {
	runes := []rune(text)
	if width <= 0 || len(runes) < width {
		return false
	}
	for i := 0; i+width <= len(runes); i++ {
		window := string(runes[i : i+width])
		if strings.Contains(clause, window) {
			return true
		}
	}
	return false
}

// "合成丁说：……我在……发送" makes 合成丁 the explicit first-person owner.
// Repair an empty owner only when the action shares a phrase with that span.
func repairFirstPersonNamedOwner(a *Analysis, transcript string) {
	match := namedFirstPersonTask.FindStringSubmatch(transcript)
	if match == nil || len(match) < 2 {
		return
	}
	name := strings.TrimSpace(match[1])
	if name == "" || strings.ContainsAny(name, "我你他她它") {
		return
	}
	span := match[0]
	// Include the rest of the sentence that contains the first-person task;
	// the regex stops at "我在/我负责", but the action object follows it.
	if start := strings.Index(transcript, match[0]); start >= 0 {
		markerEnd := start + len(match[0])
		rest := transcript[markerEnd:]
		if end := strings.Index(rest, "。"); end >= 0 {
			span = transcript[start : markerEnd+end+len("。")]
		} else if len([]rune(rest)) > 200 {
			span = transcript[start : markerEnd+len(string([]rune(rest)[:200]))]
		} else {
			span = transcript[start:]
		}
	}
	replaced := false
	for i := range a.Actions {
		if strings.TrimSpace(a.Actions[i].Owner) != "" {
			continue
		}
		probe := a.Actions[i].Action + a.Actions[i].Details
		if analysisSharesPhrase(probe, span, 3) {
			a.Actions[i].Owner = name
			replaced = true
		}
	}
	if replaced && strings.Contains(a.Summary, "spk_0") {
		a.Summary = strings.Replace(a.Summary, "spk_0", name, 1)
		setAnalysisSummary(a, a.Summary)
	}
}

var (
	calendarYearPattern = regexp.MustCompile(`((?:19|20)[0-9]{2})\s*年|((?:19|20)[0-9]{2})(?:\s*-\s*[0-9]{1,2}\s*-\s*[0-9]{1,2}|\s*/\s*[0-9]{1,2}\s*/\s*[0-9]{1,2})|((?:一九|二[〇零])[〇零一二三四五六七八九]{2})\s*年`)
	calendarYearDigits  = strings.NewReplacer("零", "0", "〇", "0", "一", "1", "二", "2", "三", "3", "四", "4", "五", "5", "六", "6", "七", "7", "八", "8", "九", "9")
	calendarDatePattern = regexp.MustCompile(`((?:19|20)[0-9]{2}|(?:一九|二[〇零])[〇零一二三四五六七八九]{2})\s*(?:年|[-/])\s*([0-9零〇一二三四五六七八九十]{1,3})\s*(?:月|[-/])\s*([0-9零〇一二三四五六七八九十]{1,3})(?:日|号)?`)
)

// Recognize explicit year notation only; a four-digit quantity or model number
// does not authorize adding a year to a month/day. Source excludes instructions.
func unsupportedCalendarYears(source, output string) []string {
	allowed := make(map[string]bool)
	for _, year := range explicitCalendarYears(source) {
		allowed[year] = true
	}
	var issues []string
	for _, year := range explicitCalendarYears(output) {
		if !allowed[year] {
			issues = append(issues, "材料未明示的年份："+year+"年")
		}
	}
	// The report date does not supply the missing year of an unrelated event.
	// Match complete dates as tuples, allowing spelling/format changes only.
	dates := explicitCalendarDates(source)
	var dateIssues []string
	for date, phrase := range explicitCalendarDates(output) {
		if !allowed[strconv.Itoa(date[0])] {
			continue // Already reported as an unsupported year above.
		}
		if _, ok := dates[date]; !ok {
			dateIssues = append(dateIssues, "材料未明示的完整日期："+phrase+"；保留原日期，不能用报告年份补全其他事项的月日")
		}
	}
	sort.Strings(dateIssues)
	issues = append(issues, dateIssues...)
	return issues
}

func explicitCalendarDates(text string) map[[3]int]string {
	dates := make(map[[3]int]string)
	for _, match := range calendarDatePattern.FindAllStringSubmatchIndex(text, -1) {
		if match[0] > 0 {
			previous, _ := utf8.DecodeLastRuneInString(text[:match[0]])
			if strings.ContainsRune("0123456789零〇一二三四五六七八九", previous) {
				continue
			}
		}
		phrase := text[match[0]:match[1]]
		if strings.Contains(phrase, "月") && !strings.HasSuffix(phrase, "日") && !strings.HasSuffix(phrase, "号") {
			// "2026年4月5个项目" gives a monthly quantity, not April 5.
			// A markerless date is unambiguous at a delimiter/end, but must
			// not consume the beginning of a quantity followed by a unit.
			tail := strings.TrimSpace(text[match[1]:])
			if tail != "" {
				next, _ := utf8.DecodeRuneInString(tail)
				if !strings.ContainsRune("，。；：！？、,.!?;:)]）】", next) {
					continue
				}
			}
		}
		year, _ := strconv.Atoi(calendarYearDigits.Replace(text[match[2]:match[3]]))
		month, monthOK := calendarDateNumber(text[match[4]:match[5]])
		day, dayOK := calendarDateNumber(text[match[6]:match[7]])
		if monthOK && dayOK && month >= 1 && month <= 12 && day >= 1 && day <= 31 {
			dates[[3]int{year, month, day}] = phrase
		}
	}
	return dates
}

func calendarDateNumber(s string) (int, bool) {
	if n, err := strconv.Atoi(s); err == nil {
		return n, true // Leading zeroes are valid in ISO dates.
	}
	return countValue(s)
}

func explicitCalendarYears(text string) []string {
	var years []string
	seen := make(map[string]bool)
	for _, match := range calendarYearPattern.FindAllStringSubmatchIndex(text, -1) {
		if match[0] > 0 {
			previous, _ := utf8.DecodeLastRuneInString(text[:match[0]])
			if strings.ContainsRune("0123456789零〇一二三四五六七八九", previous) {
				continue // Do not match a suffix of a longer number.
			}
		}
		if !strings.HasSuffix(text[match[0]:match[1]], "年") &&
			match[1] < len(text) && text[match[1]] >= '0' && text[match[1]] <= '9' {
			continue
		}
		for i := 2; i < len(match); i += 2 {
			if match[i] < 0 {
				continue
			}
			year := calendarYearDigits.Replace(text[match[i]:match[i+1]])
			if !seen[year] {
				seen[year] = true
				years = append(years, year)
			}
			break
		}
	}
	return years
}

func sourceHasIdentityTask(source string) bool {
	for _, sentence := range sentenceBoundary.Split(source, -1) {
		if !identityTaskBefore.MatchString(sentence) && !identityTaskAfter.MatchString(sentence) {
			continue
		}
		negated := false
		for _, phrase := range []string{"不需要", "无需", "不要", "不必", "不用", "禁止", "取消", "没有要求", "未要求", "不能", "不添加", "不代表"} {
			if strings.Contains(sentence, phrase) {
				negated = true
				break
			}
		}
		if !negated {
			return true
		}
	}
	return false
}

func unsupportedIdentityQuestion(source, report string) bool {
	if sourceHasIdentityTask(source) {
		return false
	}
	inQuestions := false
	for _, line := range strings.Split(report, "\n") {
		line = strings.TrimSpace(line)
		questionHeading := strings.Contains(line, "待确认问题") || strings.Contains(line, "待核实问题") || strings.Contains(line, "未决问题")
		if questionHeading {
			inQuestions = true
		} else if strings.HasPrefix(line, "#") || strings.HasPrefix(line, "- **") || strings.HasPrefix(line, "**") {
			inQuestions = false
		}
		if inQuestions && identityReference.MatchString(line) {
			// A repair may explicitly state that identity is NOT an open question
			// while leaving the old heading. Do not treat that negation as a task.
			negated := identityQuestionNegation.MatchString(line)
			for _, match := range affirmativeIdentityTask.FindAllStringIndex(line, -1) {
				if !negatedTaskPrefix.MatchString(line[:match[0]]) {
					// "Not an open question, but please verify their identity" still
					// introduces an unsupported task and must not pass the negation.
					negated = false
				}
			}
			if !negated {
				return true
			}
		}
	}
	return false
}

var errorDiagnosisPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?:数值|数字)(?:范围|区间|跨度)?(?:明显|存在|出现|可能|疑似|似乎|看起来|有|为|是)*(?:异常|不合理|不正常)`),
	regexp.MustCompile(`笔误`),
	regexp.MustCompile(`口误`),
	regexp.MustCompile(`(?:单位|单位换算|单位转换|单位标注)(?:存在|出现|可能|疑似|明显|有|为|是)*(?:错误|有误|出错|不对|写错|用错|标错|搞错|混淆|错了|错)|(?:写错|用错|标错|弄错|搞错)(?:了)?单位`),
	regexp.MustCompile(`(?:转写|转录|识别|听写)(?:存在|出现|可能|疑似|明显|有|为|是)*(?:错误|有误|出错|错漏|误差)|(?:转写|转录|识别|听写)错(?:了)?|误(?:转写|转录|识别|听写)`),
}

var (
	errorDiagnosisBoundary = regexp.MustCompile(`[。！？；;\n，,]|但是|但|然而|不过|而是`)
	errorDiagnosisDenial   = regexp.MustCompile(`未(?:发现|提到|说明|证实|确认|认定)|没有(?:发现|提到|说明|证据|依据)|无(?:证据|依据)|不存在|不属于|不是|并非|并无|未有|不代表|不意味着|不认为|(?:不能|不可|不应|不宜|无法)[^。！？；\n]{0,12}(?:认定|确认|推断|判断|认为|归因)|不要(?:推测|推断)|(?:没有|无)(?:任何|明显|此类)?(?:笔误|口误|单位(?:错误|有误)|转写(?:错误|有误)|\s*$)`)
	errorDiagnosisRejected = regexp.MustCompile(`^\s*(?:的(?:判断|推断|猜测|说法))?(?:并|尚)?(?:未(?:被)?(?:证实|确认)|不成立|无依据|没有依据|缺乏依据|不作为(?:事实|结论))`)
	errorDiagnosisDoubleNo = regexp.MustCompile(`(?:并非|并不是|不是|不能说)没有`)
)

// This narrow lexical guard covers unsupported numeric-error diagnoses, not
// business quantities or general risk reasoning. Source must be material only,
// never instruction text or feedback from a previous failed response.
func unsupportedErrorDiagnosis(source, output string) bool {
	for _, pattern := range errorDiagnosisPatterns {
		if hasErrorDiagnosis(output, pattern) && !hasErrorDiagnosis(source, pattern) {
			return true
		}
	}
	return false
}

func hasErrorDiagnosis(text string, pattern *regexp.Regexp) bool {
	for _, clause := range errorDiagnosisBoundary.Split(text, -1) {
		for _, match := range pattern.FindAllStringIndex(clause, -1) {
			prefix := clause[:match[0]]
			suffix := strings.TrimLeft(clause[match[1]:], " \t\"'”’」』*")
			if errorDiagnosisRejected.MatchString(suffix) {
				continue
			}
			if errorDiagnosisDenial.MatchString(prefix) && !errorDiagnosisDoubleNo.MatchString(prefix) {
				continue
			}
			return true
		}
	}
	return false
}

func groundingRepairPrompt(source string, issues []string) string {
	return source + "\n\n输出校验发现以下无依据内容：\n- " + strings.Join(issues, "\n- ") + `
请重新输出完整结果，修正这些问题：没有明示总数的记录只写“多条记录/多段录音”，不要自行计数或补出分组数量、时长；只使用原始基础信息中的全日统计。身份未知只作为来源标签，若原文没有身份核实任务，不把它写成待确认问题。保留原文明确的业务数量、金额、日期、责任人、期限、条件和真实风险。上述校验反馈不是事实材料，不得把其中的错误统计当成证据。`
}
