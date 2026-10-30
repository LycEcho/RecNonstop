package infer

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const dailySystemPrompt = "你是录音日报助手。只输出中文 Markdown，不要输出 JSON，不要用代码块，不要编造材料里没有的信息。"

// GenerateDailyReport 把当天的分段摘要/转写整理成一份 Markdown 日报。
// 材料较长时先分块提炼，再合并成完整日报，避免一次请求塞爆上下文。
func (c *LMStudio) GenerateDailyReport(ctx context.Context, material string) (string, error) {
	material = strings.TrimSpace(material)
	if material == "" {
		return "", nil
	}

	finalTokens := c.MaxTokens
	if finalTokens < 1800 {
		finalTokens = 1800
	}
	chunkTokens := finalTokens / 2
	if chunkTokens < 700 {
		chunkTokens = 700
	}

	chunks := chunkText(material, 6000)
	if len(chunks) <= 1 {
		return c.dailyChat(ctx, dailyReportPrompt(material), finalTokens)
	}

	notes := make([]string, 0, len(chunks))
	for i, ch := range chunks {
		note, err := c.dailyChat(ctx, dailyChunkPrompt(i+1, len(chunks), ch), chunkTokens)
		if err != nil {
			return "", err
		}
		notes = append(notes, note)
	}
	return c.dailyChat(ctx, dailyMergePrompt(material, strings.Join(notes, "\n\n")), finalTokens)
}

func (c *LMStudio) dailyChat(ctx context.Context, user string, maxTokens int) (string, error) {
	var last error
	for attempt := 0; attempt < 2; attempt++ {
		out, err := c.chatOnce(ctx, dailySystemPrompt, user, maxTokens, false)
		if err == nil {
			out = stripCodeFence(out)
			if strings.TrimSpace(out) != "" {
				return strings.TrimSpace(out), nil
			}
			last = fmt.Errorf("empty daily report response")
		} else {
			last = err
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return "", last
}

func dailyReportPrompt(material string) string {
	return `请根据下面的当天录音记录，写一份中文 Markdown 日报，必须包含这些二级标题：

## 今日概览
## 主要事项
## 待办
## 风险与异常

要求：
- 今日概览用 2~3 句话概括当天整体情况，并保留材料里的段数和总时长。
- 主要事项按时间或主题列点，重要时间点保留 HH:MM。
- 待办只写材料里明确出现的事项，没有就写“未发现明确待办”。
- 风险与异常只写材料里有依据的内容，没有就写“未发现”。
- 不要编造人名、数字、地点、承诺或结论。
- 不要输出 JSON、代码块或额外说明。

当天录音记录：
` + material
}

func dailyChunkPrompt(index, total int, chunk string) string {
	return fmt.Sprintf(`这是当天录音记录的第 %d/%d 部分。请提炼这一部分的关键内容，用简洁中文列点，保留明确的时间、人物、决定、待办和风险/异常。不要编造，不要写总结以外的内容。

%s`, index, total, chunk)
}

func dailyMergePrompt(material, notes string) string {
	return `下面是当天录音的分段要点。请把它们合并成一份中文 Markdown 日报，必须包含这些二级标题：

## 今日概览
## 主要事项
## 待办
## 风险与异常

要求：
- 今日概览用 2~3 句话概括当天整体情况。
- 主要事项按时间或主题归类，重要时间点保留 HH:MM。
- 待办、风险与异常只保留有依据的内容，没有就写“未发现”。
- 不要编造人名、数字、地点、承诺或结论。
- 不要输出 JSON、代码块或额外说明。

当天基础信息：
` + firstLines(material, 6) + `

分段要点：
` + notes
}

func firstLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}

// chunkText 按行切分，尽量不把一条记录从中间截断。
func chunkText(s string, maxRunes int) []string {
	if maxRunes <= 0 {
		maxRunes = 6000
	}
	var chunks []string
	var b strings.Builder
	n := 0
	flush := func() {
		if b.Len() > 0 {
			chunks = append(chunks, b.String())
			b.Reset()
			n = 0
		}
	}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimRight(line, "\r")
		add := line + "\n"
		ln := len([]rune(add))
		if ln > maxRunes {
			flush()
			rs := []rune(line)
			for len(rs) > maxRunes {
				chunks = append(chunks, string(rs[:maxRunes]))
				rs = rs[maxRunes:]
			}
			if len(rs) > 0 {
				b.WriteString(string(rs))
				b.WriteString("\n")
				n = len(rs) + 1
			}
			continue
		}
		if n > 0 && n+ln > maxRunes {
			flush()
		}
		b.WriteString(add)
		n += ln
	}
	flush()
	return chunks
}
