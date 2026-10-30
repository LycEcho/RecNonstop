package pipeline

import (
	"strings"
	"unicode"

	"warehouse-audio/internal/infer"
	"warehouse-audio/internal/job"
)

// mergeGap 是同一说话人相邻句子的合并阈值：间隔不超过 1 秒就并成一段。
// 目的只是减少“每句一两个词”的碎片感，不改变说话人切换。
const mergeGap = 1.0

// normalizeSegments 清理 ASR 输出里的空片段/纯标点，并把同一说话人、
// 间隔很短的连续片段合并。时间戳保持原样，只扩展合并段的 end。
func normalizeSegments(in []infer.Segment) []job.Segment {
	out := make([]job.Segment, 0, len(in))
	for _, s := range in {
		text := trimPunctSpace(s.Text)
		if !hasContent(text) {
			continue
		}
		if len(out) > 0 {
			last := &out[len(out)-1]
			gap := s.Start - last.End
			if last.Speaker == s.Speaker && gap >= -0.2 && gap <= mergeGap {
				joiner := "，"
				if endsWithPunct(last.Text) {
					joiner = ""
				}
				last.Text += joiner + text
				if s.End > last.End {
					last.End = s.End
				}
				continue
			}
		}
		out = append(out, job.Segment{
			Start:      s.Start,
			End:        s.End,
			Speaker:    s.Speaker,
			Person:     s.Person,
			PersonName: s.PersonName,
			Text:       text,
		})
	}
	return out
}

func trimPunctSpace(s string) string {
	s = strings.TrimSpace(s)
	// 去掉 ASR 常见的开头标点/空白；结尾的句号、问号保留，
	// 只清掉逗号/分号这类连接符，避免合并后出现“，，”。
	s = strings.TrimLeft(s, ".。，,、；;：:!?！？…·")
	s = strings.TrimRight(s, " \t\r\n，,、；;：:·")
	return strings.TrimSpace(s)
}

func endsWithPunct(s string) bool {
	if s == "" {
		return false
	}
	r := []rune(s)
	return strings.ContainsRune("。！？!?，,；;：:、…", r[len(r)-1])
}

func hasContent(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return true
		}
	}
	return false
}
