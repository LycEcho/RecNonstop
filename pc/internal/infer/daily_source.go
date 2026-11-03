package infer

import (
	"encoding/json"
	"regexp"
	"strings"
)

var dailyRecordingHeading = regexp.MustCompile(`^###[ \t]*录音[ \t]+[0-9]+(?:[ \t].*)?$`)

// dailyEvidenceMaterial returns an analysis-only copy. A recording with usable
// original text does not need its old model interpretations as parallel evidence.
// Unmarked multiline speech is ambiguous: keep apparent metadata inside it.
func dailyEvidenceMaterial(material string) string {
	lines := strings.SplitAfter(material, "\n")
	var out strings.Builder
	start := -1
	preserveBlock := false
	var protection dailySourceProtection
	for i, line := range lines {
		top := strings.TrimRight(line, "\r\n")
		if protection.fence == "" && dailyRecordingHeading.MatchString(top) {
			if start < 0 {
				out.WriteString(strings.Join(lines[:i], ""))
			} else if preserveBlock {
				out.WriteString(strings.Join(lines[start:i], ""))
			} else {
				out.WriteString(dailyEvidenceRecording(lines[start:i]))
			}
			start = i
			// An unfinished source quotation must not join two recordings and
			// let one recording's original text delete the other's references.
			preserveBlock = protection.quote != 0
		}
		protection.advance(top)
	}
	if start < 0 {
		return material
	}
	if preserveBlock {
		out.WriteString(strings.Join(lines[start:], ""))
	} else {
		out.WriteString(dailyEvidenceRecording(lines[start:]))
	}
	return out.String()
}

func dailyEvidenceRecording(lines []string) string {
	const (
		outside = iota
		awaitingOriginal
		inlineOriginal
		labeledOriginal
		unmarkedOriginal
	)
	mode := outside
	hasOriginal := false
	remove := make([]bool, len(lines))
	var protection dailySourceProtection
	for i, line := range lines {
		top := strings.TrimRight(line, "\r\n")
		protected := protection.protected(top)
		metadata := !protected && dailyAutomaticMetadata(top)
		original, marker := dailyOriginalMarker(top)
		if !protected && marker {
			mode = awaitingOriginal
			if strings.TrimSpace(original) != "" {
				mode = inlineOriginal
				if dailyOriginalHasText(original) {
					hasOriginal = true
				}
			}
		} else if metadata {
			// In an unmarked, multiline transcript this exact-looking line may
			// itself have been spoken. There is no reliable boundary to delete it.
			remove[i] = mode != unmarkedOriginal
		} else if mode != outside && strings.TrimSpace(top) != "" {
			if !protected && dailyMaterialBoundary(top) {
				mode = outside
			} else if dailyUtteranceLabel.MatchString(top) {
				mode = labeledOriginal
				if dailyOriginalHasText(top) {
					hasOriginal = true
				}
			} else {
				if mode != labeledOriginal {
					mode = unmarkedOriginal
				}
				hasOriginal = true
			}
		}
		protection.advance(top)
	}
	if !hasOriginal {
		return strings.Join(lines, "")
	}
	var out strings.Builder
	for i, line := range lines {
		if !remove[i] {
			out.WriteString(line)
		}
	}
	return out.String()
}

func dailyOriginalMarker(line string) (string, bool) {
	for _, marker := range []string{"原始转写（主证据）", "原始转写节选", "原始转写"} {
		for _, colon := range []string{"：", ":"} {
			if rest, ok := strings.CutPrefix(line, marker+colon); ok {
				return rest, true
			}
		}
	}
	return "", false
}

func dailyOriginalHasText(line string) bool {
	line = strings.TrimSpace(line)
	if label := dailyUtteranceLabel.FindString(line); label != "" {
		line = strings.TrimSpace(strings.TrimPrefix(line, label))
	}
	return line != ""
}

func dailyAutomaticMetadata(line string) bool {
	// Match only complete framework field names at column zero. In particular,
	// ordinary 摘要/风险/待办 prose, quotes and timestamped speech never match.
	for _, name := range []string{
		"历史自动摘要", "自动摘要",
		"分段模型提取的待办候选", "分段模型提取的候选待办",
		"分段模型提取的风险候选", "分段模型提取的候选风险",
		"分段模型提取的限制候选", "分段模型提取的候选限制",
		"模型待办候选", "模型风险候选", "模型限制候选",
	} {
		if _, ok := dailyMetadataValue(line, name); ok {
			return true
		}
	}
	// Older framework fields did not say "automatic", but their values were
	// serialized arrays. Plain-language task/risk lines are not these fields.
	for _, name := range []string{"明确待办", "有依据的风险", "有依据的限制"} {
		if value, ok := dailyMetadataValue(line, name); ok {
			value = strings.TrimSpace(value)
			if strings.HasPrefix(value, "[") && json.Valid([]byte(value)) {
				return true
			}
		}
	}
	return false
}

func dailyMetadataValue(line, name string) (string, bool) {
	rest, ok := strings.CutPrefix(line, name)
	if !ok {
		return "", false
	}
	for _, pair := range [][2]string{{"（", "）"}, {"(", ")"}} {
		if strings.HasPrefix(rest, pair[0]) {
			end := strings.Index(rest, pair[1])
			if end < 0 {
				return "", false
			}
			rest = rest[end+len(pair[1]):]
			break
		}
	}
	for _, colon := range []string{"：", ":"} {
		if value, ok := strings.CutPrefix(rest, colon); ok {
			return value, true
		}
	}
	return "", false
}

// Respect explicit quoting across lines. Ambiguous/unclosed quotation keeps
// content rather than trying to repair the source or remove apparent metadata.
type dailySourceProtection struct {
	fence string
	quote rune
}

func (p dailySourceProtection) protected(line string) bool {
	return p.fence != "" || p.quote != 0 || strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~")
}

func (p *dailySourceProtection) advance(line string) {
	if p.fence != "" {
		if strings.HasPrefix(line, p.fence) {
			p.fence = ""
		}
		return
	}
	if p.quote == 0 && (strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~")) {
		p.fence = line[:3]
		return
	}
	escaped := false
	for _, r := range line {
		if escaped {
			escaped = false
			continue
		}
		if r == '\\' {
			escaped = true
			continue
		}
		if p.quote != 0 {
			if r == p.quote {
				p.quote = 0
			}
			continue
		}
		switch r {
		case '“':
			p.quote = '”'
		case '‘':
			p.quote = '’'
		case '"':
			p.quote = '"'
		}
	}
}
