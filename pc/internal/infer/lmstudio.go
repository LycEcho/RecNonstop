package infer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

type LMStudio struct {
	BaseURL         string
	APIKey          string
	Model           string
	ReasoningEffort string
	MaxTokens       int
	Temperature     float64
	JSONMode        bool
	WakeURL         string
	WakeToken       string
	TouchURL        string
	HC              *http.Client

	mu         sync.Mutex
	jsonModeOK bool
}

func NewLMStudio(baseURL, apiKey, model, reasoningEffort string, maxTokens int,
	temperature float64, timeout time.Duration, jsonMode bool) *LMStudio {
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	if maxTokens <= 0 {
		maxTokens = 1024
	}
	return &LMStudio{
		BaseURL:         strings.TrimRight(baseURL, "/"),
		APIKey:          apiKey,
		Model:           model,
		ReasoningEffort: reasoningEffort,
		MaxTokens:       maxTokens,
		Temperature:     temperature,
		JSONMode:        jsonMode,
		jsonModeOK:      true,
		HC:              &http.Client{Timeout: timeout},
	}
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model           string        `json:"model"`
	Messages        []chatMessage `json:"messages"`
	MaxTokens       int           `json:"max_tokens,omitempty"`
	Temperature     float64       `json:"temperature"`
	ReasoningEffort string        `json:"reasoning_effort,omitempty"`
	ResponseFormat  any           `json:"response_format,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

const analyzeSystemPrompt = "你是录音分析助手。只输出 JSON，不要解释，不要 markdown。"

func (c *LMStudio) Analyze(ctx context.Context, t *Transcript) (*Analysis, error) {
	hostWake(ctx, c.WakeURL, c.WakeToken)
	stopAlive := hostKeepAlive(ctx, c.TouchURL, c.WakeToken)
	defer stopAlive()
	chunks := chunkTranscript(t, 3000)
	if len(chunks) == 0 {
		return c.analyzePrompt(ctx, "转写为空，输出空分析。", true)
	}
	if len(chunks) == 1 {
		return c.analyzePrompt(ctx, buildAnalyzePrompt(chunks[0]), true)
	}

	summaries := make([]string, 0, len(chunks))
	for _, ch := range chunks {
		a, err := c.analyzePrompt(ctx, buildAnalyzePrompt(ch), true)
		if err != nil {
			return nil, err
		}
		summaries = append(summaries, a.Summary)
	}
	final := "以下是分段分析摘要，请合并成一份整体分析：\n\n" + strings.Join(summaries, "\n")
	return c.analyzePrompt(ctx, final, true)
}

func buildAnalyzePrompt(transcript string) string {
	return `请根据下面的录音转写输出 JSON，结构必须是：
{"summary":"","speakers":[],"actions":[{"speaker":"","action":"","details":""}],"risks":[]}

要求：
- summary 必须是一句到三句中文概括，不能为空。
- speakers 是说话人列表，没有就空数组。
- actions 是明确待办，没有就空数组。
- risks 是风险/异常，没有就空数组。
- 只输出 JSON，不要 markdown。

转写：
` + transcript
}

func (c *LMStudio) analyzePrompt(ctx context.Context, userPrompt string, allowRetry bool) (*Analysis, error) {
	content, err := c.chat(ctx, analyzeSystemPrompt, userPrompt, c.MaxTokens)
	if err != nil {
		return nil, err
	}
	content = stripCodeFence(content)
	a := parseAnalysis(content)
	if a.Summary == "" && allowRetry {
		// 模型有时会返回 {"analysis": "..."} 或直接一段话。再要一次严格 JSON。
		retryPrompt := userPrompt + "\n\n上一次输出缺少 summary 字段。请只输出合法 JSON，且 summary 不能为空。"
		content2, err2 := c.chat(ctx, analyzeSystemPrompt, retryPrompt, c.MaxTokens)
		if err2 == nil {
			if a2 := parseAnalysis(stripCodeFence(content2)); a2.Summary != "" {
				return a2, nil
			}
		}
	}
	return a, nil
}

func parseAnalysis(content string) *Analysis {
	content = strings.TrimSpace(content)
	if !json.Valid([]byte(content)) {
		// 非 JSON：保留原文当摘要，但 Raw 必须为 nil，否则整条 job 落不了盘。
		return &Analysis{Summary: content}
	}

	a := &Analysis{Raw: json.RawMessage(content)}
	// 只强解 summary/speakers；actions/risks 的字段形状经常漂移
	//（比如 risks 从字符串数组变成对象数组），不能让它把整条分析打挂。
	var probe struct {
		Summary  string   `json:"summary"`
		Speakers []string `json:"speakers"`
	}
	_ = json.Unmarshal([]byte(content), &probe)
	a.Summary = strings.TrimSpace(probe.Summary)
	a.Speakers = probe.Speakers

	if a.Summary == "" {
		// 兼容 {"analysis": "..."} / {"text": "..."} 这类漂移。
		var m map[string]any
		if json.Unmarshal([]byte(content), &m) == nil {
			for _, k := range []string{"summary", "analysis", "text", "result"} {
				if s, ok := m[k].(string); ok && strings.TrimSpace(s) != "" {
					a.Summary = s
					break
				}
			}
		}
	}
	return a
}

func (c *LMStudio) chat(ctx context.Context, system, user string, maxTokens int) (string, error) {
	var last error
	for attempt := 0; attempt < 2; attempt++ {
		out, err := c.chatOnce(ctx, system, user, maxTokens, c.useJSONMode())
		if err == nil {
			return out, nil
		}
		last = err
		if strings.Contains(err.Error(), "response_format") || strings.Contains(err.Error(), "400") {
			c.disableJSONMode()
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

func (c *LMStudio) useJSONMode() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.JSONMode && c.jsonModeOK
}

func (c *LMStudio) disableJSONMode() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.jsonModeOK {
		log.Printf("lmstudio: server rejected response_format, falling back to prompt-only JSON")
	}
	c.jsonModeOK = false
}

func (c *LMStudio) chatOnce(ctx context.Context, system, user string, maxTokens int, jsonMode bool) (string, error) {
	reqBody := chatRequest{
		Model: c.Model,
		Messages: []chatMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
		MaxTokens:       maxTokens,
		Temperature:     c.Temperature,
		ReasoningEffort: c.ReasoningEffort,
	}
	if jsonMode {
		reqBody.ResponseFormat = map[string]string{"type": "json_object"}
	}
	b, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.BaseURL+"/chat/completions", bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}

	resp, err := c.HC.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var eb bytes.Buffer
		_, _ = eb.ReadFrom(resp.Body)
		return "", fmt.Errorf("lmstudio status %d: %s", resp.StatusCode, eb.String())
	}

	var out chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("lmstudio returned no choices")
	}
	return out.Choices[0].Message.Content, nil
}

func chunkTranscript(t *Transcript, maxRunes int) []string {
	var chunks []string
	var b strings.Builder
	n := 0
	for _, s := range t.Segments {
		line := fmt.Sprintf("[%.2f-%.2f] %s: %s\n", s.Start, s.End, s.Speaker, s.Text)
		ln := len([]rune(line))
		if n > 0 && n+ln > maxRunes {
			chunks = append(chunks, b.String())
			b.Reset()
			n = 0
		}
		b.WriteString(line)
		n += ln
	}
	if b.Len() > 0 {
		chunks = append(chunks, b.String())
	}
	return chunks
}

func stripCodeFence(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```json")
		s = strings.TrimPrefix(s, "```JSON")
		s = strings.TrimPrefix(s, "```")
		if i := strings.Index(s, "\n"); i >= 0 && !strings.HasPrefix(s, "{") {
			s = s[i+1:]
		}
		s = strings.TrimSuffix(strings.TrimSpace(s), "```")
	}
	return strings.TrimSpace(s)
}
