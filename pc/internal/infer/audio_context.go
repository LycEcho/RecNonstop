package infer

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode/utf8"
)

type audioTimeRange struct{ start, end float64 }

type transcriptAudioChunk struct {
	text     string
	context  string
	speakers []string
}

// Keep the original chunking and recover the source segment spans, including
// chunks cut inside one long utterance. A segment's whole window is the finest
// alignment available here; never invent word-level audio timestamps.
func chunkTranscriptWithAudio(t *Transcript, maxRunes int) []transcriptAudioChunk {
	type sourceSpan struct {
		start, end int
		audioTimeRange
		speaker string
	}
	var source strings.Builder
	var spans []sourceSpan
	for _, s := range t.Segments {
		start := source.Len()
		line := fmt.Sprintf("[%.2f-%.2f] %s: %s\n", s.Start, s.End, s.Speaker, s.Text)
		source.WriteString(normalizeTranscriptNewlines(line))
		spans = append(spans, sourceSpan{start, source.Len(), audioTimeRange{s.Start, s.End}, strings.TrimSpace(s.Speaker)})
	}
	if source.Len() == 0 {
		source.WriteString(normalizeTranscriptNewlines(t.Text))
	}
	text := strings.TrimSuffix(source.String(), "\n")
	if strings.TrimSpace(text) == "" {
		return nil
	}
	var result []transcriptAudioChunk
	offset := 0
	for _, chunk := range chunkText(text, maxRunes) {
		// chunkText may insert a final newline, or omit one after an exact
		// multiple of the limit. Match the content in order, never by timestamp
		// text, which may itself occur inside an utterance.
		content := strings.TrimSuffix(chunk, "\n")
		at := strings.Index(text[offset:], content)
		var ranges []audioTimeRange
		var speakers []string
		seenSpeakers := make(map[string]bool)
		mapped := at >= 0 && content != "" && len(spans) > 0
		if at >= 0 {
			start, end := offset+at, offset+at+len(content)
			for _, span := range spans {
				if span.start < end && span.end > start {
					if span.speaker != "" && !seenSpeakers[span.speaker] {
						speakers = append(speakers, span.speaker)
						seenSpeakers[span.speaker] = true
					}
					if !validAudioRange(span.audioTimeRange.start, span.audioTimeRange.end) {
						mapped = false
					}
					ranges = append(ranges, span.audioTimeRange)
				}
			}
			offset = end
		}
		context := audioContext(t.AudioEvents)
		if mapped && len(ranges) > 0 {
			context = formatAudioContext(audioLabels(overlappingAudioEvents(t.AudioEvents, ranges)), true)
		}
		result = append(result, transcriptAudioChunk{text: chunk, context: context, speakers: speakers})
	}
	return result
}

func normalizeTranscriptNewlines(s string) string {
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], "\r")
	}
	return strings.Join(lines, "\n")
}

func validAudioRange(start, end float64) bool {
	return !math.IsNaN(start) && !math.IsNaN(end) && !math.IsInf(start, 0) && !math.IsInf(end, 0) && start >= 0 && end > start
}

func overlappingAudioEvents(events []AudioEvent, ranges []audioTimeRange) []AudioEvent {
	var result []AudioEvent
	for _, event := range events {
		if !validAudioRange(event.Start, event.End) {
			continue
		}
		for _, span := range ranges {
			if validAudioRange(span.start, span.end) && event.Start < span.end && event.End > span.start {
				result = append(result, event)
				break
			}
		}
	}
	return result
}

// AudioLabelsForRange returns at most four strong labels from windows that
// overlap the original utterance. Missing/invalid timing yields no local hint.
func AudioLabelsForRange(events []AudioEvent, start, end float64) []string {
	return audioLabels(overlappingAudioEvents(events, []audioTimeRange{{start, end}}))
}

// Sound labels describe audio, not the truth of words or the listener's scene.
// Weak classifier scores are retained in stored results but omitted here.
func audioContext(events []AudioEvent) string {
	return formatAudioContext(audioLabels(events), false)
}

func audioLabels(events []AudioEvent) []string {
	scores := make(map[string]float64)
	for _, event := range events {
		for _, tag := range event.Tags {
			label := strings.TrimSpace(tag.Label)
			if label == "" || utf8.RuneCountInString(label) > 40 || strings.ContainsAny(label, "\r\n") || math.IsNaN(tag.Score) || tag.Score < 0.5 || tag.Score > 1 {
				continue
			}
			if tag.Score > scores[label] {
				scores[label] = tag.Score
			}
		}
	}
	labels := make([]string, 0, len(scores))
	for label := range scores {
		labels = append(labels, label)
	}
	sort.Slice(labels, func(i, j int) bool {
		if scores[labels[i]] == scores[labels[j]] {
			return labels[i] < labels[j]
		}
		return scores[labels[i]] > scores[labels[j]]
	})
	if len(labels) == 0 {
		return nil
	}
	if len(labels) > 4 {
		labels = labels[:4]
	}
	return labels
}

func formatAudioContext(labels []string, localized bool) string {
	if len(labels) == 0 {
		return ""
	}
	scope := "整段声音线索（未定位到本部分原文，不能作为逐句线索）"
	if localized {
		scope = "对应转写发言时段的声音线索（不是逐字定位）"
	}
	return "\n\n" + scope + "（自动识别，可能有误，仅供辅助）：" + strings.Join(labels, "、") + "。这些标签不能确定会议、电话、影视等场景，也不能证明身份、来源或事件真实性；不能据此添加事实或省略原文明示的任务、数字和条件。"
}
