package infer

import (
	"regexp"
	"sort"
	"strings"
)

var (
	eventSpacedDay       = regexp.MustCompile(`([0-9零〇一二三四五六七八九十])\s+([日号])`)
	eventMetadataValue   = regexp.MustCompile(`^[0-9零〇一二三四五六七八九十年月日号时分秒\s./:：–—~至-]+$`)
	eventSourceLabel     = regexp.MustCompile(`^\[(?:[0-9]{4}-)?[0-9]{2}-[0-9]{2} [0-9]{2}:[0-9]{2}:[0-9]{2}(?:\.[0-9]{3})? · [^\]\r\n]+\]`)
	eventDateVerb        = regexp.MustCompile(`^\s*(?:日|号)?\s*(?:(?:正式|首次|已经|已|刚刚|刚)\s*)?(?:发布|发生)`)
	eventDateNotAsserted = regexp.MustCompile(`(?:不能确认|无法确认|尚未确认|未确认|未说|未提及|没有说明|不能证实|无法证实|没有证据|无证据|不确定|并非|不是|是否|并未在|并未于|未在|未于|没有在|没有于|不在|不于|假设|假如|如果|或许|可能|计划|预计|拟)[^。！？；\n，,]{0,32}$`)
	eventDateRejected    = regexp.MustCompile(`^(?:过)?(?:的)?(?:说法|消息|判断|日期)?(?:尚未证实|未获证实|未确认|不成立|没有依据|缺乏依据)`)
	eventOwnRecord       = regexp.MustCompile(`(?:本|该|这份|本次)?(?:日报|报告|录音|记录)(?:本身)?(?:于|在)?\s*$`)
)

// unsupportedEventDates catches a narrow provenance error: a calendar date
// found only in framework timestamps is attached directly to 发布/发生. It is
// not event matching or relative-date resolution. A date anywhere in the source
// body is sufficient to leave that assertion to the other grounding checks.
// Source must exclude instructions and repair feedback.
func unsupportedEventDates(source, output string) []string {
	metadata, body := eventDateSources(source)
	if len(metadata) == 0 {
		return nil
	}
	bodyDates := explicitCalendarDates(eventSpacedDay.ReplaceAllString(body, "$1$2"))
	issues := make(map[[3]int]string)
	var protection dailySourceProtection
	for _, raw := range strings.Split(output, "\n") {
		line := strings.TrimSpace(raw)
		protected := protection.protected(line)
		protection.advance(line)
		if protected || protection.quote != 0 || strings.HasPrefix(line, ">") || strings.HasPrefix(raw, "    ") || strings.HasPrefix(raw, "\t") {
			continue
		}
		line = dailyRiskQuoted.ReplaceAllString(line, "")
		line = strings.NewReplacer("**", "", "__", "").Replace(line)
		for _, clause := range strings.FieldsFunc(line, func(r rune) bool {
			return strings.ContainsRune("。！？；;!?，,", r)
		}) {
			for _, span := range calendarDatePattern.FindAllStringIndex(clause, -1) {
				phrase := clause[span[0]:span[1]]
				prefix, tail := clause[:span[0]], clause[span[1]:]
				verb := eventDateVerb.FindStringIndex(tail)
				if verb == nil || eventDateNotAsserted.MatchString(prefix) || eventOwnRecord.MatchString(prefix) || eventDateRejected.MatchString(strings.TrimSpace(tail[verb[1]:])) {
					continue
				}
				for date := range explicitCalendarDates(phrase) {
					if _, onlyMetadata := metadata[date]; !onlyMetadata {
						continue
					}
					if _, explicit := bodyDates[date]; !explicit {
						issues[date] = "录音或报告日期被用作事件日期：" + phrase + "；原文正文未明示该日，不能据录音时间补出发布或发生日期，请保留原文时间说法"
					}
				}
			}
		}
	}
	out := make([]string, 0, len(issues))
	for _, issue := range issues {
		out = append(out, issue)
	}
	sort.Strings(out)
	return out
}

// Only recognizable framework locations are stripped. Apparent metadata inside
// quoted or multiline original speech remains evidence, avoiding destructive
// guesses about plain text. Sound hints never supply dates.
func eventDateSources(source string) (map[[3]int]string, string) {
	metadata := make(map[[3]int]string)
	var body strings.Builder
	header := true
	var protection dailySourceProtection
	addMetadata := func(text string) {
		for date, phrase := range explicitCalendarDates(eventSpacedDay.ReplaceAllString(text, "$1$2")) {
			metadata[date] = phrase
		}
	}
	for _, raw := range strings.Split(source, "\n") {
		line := strings.TrimSpace(raw)
		protected := protection.protected(line)
		protection.advance(line)
		if !protected {
			if strings.HasPrefix(line, "自动声音线索：") {
				continue
			}
			_, value, field := strings.Cut(line, "：")
			dateField := header && field && (strings.HasPrefix(line, "日期：") || strings.HasPrefix(line, "时间段：")) && eventMetadataValue.MatchString(strings.TrimSpace(value))
			if dateField || dailyRecordingHeading.MatchString(line) {
				addMetadata(line)
				continue
			}
			if header && strings.HasPrefix(line, "录音：") {
				continue
			}
			if label := eventSourceLabel.FindString(line); label != "" {
				addMetadata(label)
				line = strings.TrimSpace(strings.TrimPrefix(line, label))
			}
		}
		if line != "" {
			header = false
		}
		body.WriteString(line)
		body.WriteByte('\n')
	}
	return metadata, body.String()
}
