package infer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

type RemoteASR struct {
	BaseURL string
	APIKey  string
	Model   string
	HC      *http.Client
}

func NewRemoteASR(baseURL, apiKey, model string) *RemoteASR {
	return &RemoteASR{
		BaseURL: baseURL,
		APIKey:  apiKey,
		Model:   model,
		HC:      &http.Client{Timeout: 30 * time.Minute},
	}
}

func (r *RemoteASR) Transcribe(ctx context.Context, audioPath string) (*Transcript, error) {
	f, err := os.Open(audioPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	// 用 io.Pipe 流式上传，避免把整个音频读进内存（以前 bytes.Buffer 会吃满 RAM）。
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		defer pw.Close()
		part, err := mw.CreateFormFile("file", filepath.Base(audioPath))
		if err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		if _, err := io.Copy(part, f); err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		if r.Model != "" {
			_ = mw.WriteField("model", r.Model)
		}
		_ = mw.WriteField("response_format", "verbose_json")
		if err := mw.Close(); err != nil {
			_ = pw.CloseWithError(err)
		}
	}()
	defer pr.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		r.BaseURL+"/audio/transcriptions", pr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if r.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+r.APIKey)
	}

	resp, err := r.HC.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var eb bytes.Buffer
		_, _ = io.Copy(&eb, io.LimitReader(resp.Body, 64*1024))
		return nil, fmt.Errorf("remote asr status %d: %s", resp.StatusCode, eb.String())
	}

	var out struct {
		Text     string  `json:"text"`
		Language string  `json:"language"`
		Duration float64 `json:"duration"`
		Segments []struct {
			Start   float64 `json:"start"`
			End     float64 `json:"end"`
			Speaker string  `json:"speaker"`
			Text    string  `json:"text"`
		} `json:"segments"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}

	segs := make([]Segment, 0, len(out.Segments))
	for _, s := range out.Segments {
		spk := s.Speaker
		if spk == "" {
			spk = "spk_0"
		}
		segs = append(segs, Segment{Start: s.Start, End: s.End, Speaker: spk, Text: s.Text})
	}
	if len(segs) == 0 && out.Text != "" {
		segs = append(segs, Segment{Start: 0, End: out.Duration, Speaker: "spk_0", Text: out.Text})
	}

	return &Transcript{
		Text:     out.Text,
		Segments: segs,
		Language: out.Language,
		Duration: out.Duration,
	}, nil
}
