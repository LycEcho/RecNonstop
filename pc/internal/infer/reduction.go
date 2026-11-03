package infer

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"
)

// reduceNotes repeatedly groups notes within the same bound used for source
// text. Never truncate an oversized set of notes or send an unbounded final
// prompt. A model that does not condense its input fails explicitly, allowing
// the caller to retain the original transcript/fallback report.
func reduceNotes(ctx context.Context, notes []string, reduce func(context.Context, string) (string, error)) (string, error) {
	return reduceNotesWithRetry(ctx, notes, reduce, nil)
}

// A daily report may retry a non-shrinking level once with a more targeted
// compression prompt. Retry the original bounded chunks, never the expanded
// output, so the evidence is unchanged and each input chunk stays bounded.
func reduceNotesWithRetry(ctx context.Context, notes []string,
	reduce, retry func(context.Context, string) (string, error)) (string, error) {
	material := strings.TrimSpace(strings.Join(notes, "\n\n"))
	for level := 0; utf8.RuneCountInString(material) > summaryMaterialRunes; level++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if level >= 12 {
			return "", fmt.Errorf("summary reduction exceeded 12 levels")
		}
		chunks := chunkText(material, summaryMaterialRunes)
		reducer := reduce
		for attempt := 0; ; attempt++ {
			next := make([]string, 0, len(chunks))
			for _, chunk := range chunks {
				if err := ctx.Err(); err != nil {
					return "", err
				}
				note, err := reducer(ctx, chunk)
				if err != nil {
					return "", err
				}
				if strings.TrimSpace(note) == "" {
					return "", fmt.Errorf("summary reduction returned empty notes")
				}
				next = append(next, strings.TrimSpace(note))
			}
			if err := ctx.Err(); err != nil {
				return "", err
			}
			merged := strings.Join(next, "\n\n")
			if utf8.RuneCountInString(merged) < utf8.RuneCountInString(material) {
				material = merged
				break
			}
			if retry == nil || attempt > 0 {
				return "", fmt.Errorf("summary reduction did not shrink notes; original material retained")
			}
			reducer = retry
		}
	}
	return material, nil
}
