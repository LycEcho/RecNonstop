package infer

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

type WorkerASR struct {
	BaseURL   string
	WakeURL   string
	WakeToken string
	TouchURL  string
	HC        *http.Client
}

type workerSentence struct {
	Spk        int     `json:"spk"`
	Start      float64 `json:"start"`
	End        float64 `json:"end"`
	Text       string  `json:"text"`
	Person     string  `json:"person,omitempty"`
	PersonName string  `json:"person_name,omitempty"`
}

type workerResponse struct {
	Text      string           `json:"text"`
	Sentences []workerSentence `json:"sentences"`
	Language  string           `json:"language"`
	Duration  float64          `json:"duration"`
}

func NewWorkerASR(baseURL string) *WorkerASR {
	return &WorkerASR{
		BaseURL: baseURL,
		HC:      &http.Client{Timeout: 30 * time.Minute},
	}
}

func (w *WorkerASR) Transcribe(ctx context.Context, audioPath string) (*Transcript, error) {
	hostWake(ctx, w.WakeURL, w.WakeToken)
	stopAlive := hostKeepAlive(ctx, w.TouchURL, w.WakeToken)
	defer stopAlive()
	payload := map[string]string{
		"audio_path": audioPath,
		"audio_name": filepath.Base(audioPath),
	}
	if b, err := os.ReadFile(audioPath); err == nil {
		payload["audio_b64"] = base64.StdEncoding.EncodeToString(b)
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.BaseURL+"/v1/transcribe", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := w.HC.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(resp.Body)
		return nil, fmt.Errorf("worker status %d: %s", resp.StatusCode, buf.String())
	}

	var wr workerResponse
	if err := json.NewDecoder(resp.Body).Decode(&wr); err != nil {
		return nil, err
	}

	segs := make([]Segment, 0, len(wr.Sentences))
	for _, s := range wr.Sentences {
		segs = append(segs, Segment{
			Start:      s.Start,
			End:        s.End,
			Speaker:    fmt.Sprintf("spk_%d", s.Spk),
			Person:     s.Person,
			PersonName: s.PersonName,
			Text:       s.Text,
		})
	}
	if len(segs) == 0 && wr.Text != "" {
		segs = append(segs, Segment{Start: 0, End: wr.Duration, Speaker: "spk_0", Text: wr.Text})
	}

	return &Transcript{
		Text:     wr.Text,
		Segments: segs,
		Language: wr.Language,
		Duration: wr.Duration,
	}, nil
}

type enrollSegment struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

type enrollRequest struct {
	AudioPath string          `json:"audio_path"`
	AudioName string          `json:"audio_name,omitempty"`
	AudioB64  string          `json:"audio_b64,omitempty"`
	Name      string          `json:"name"`
	Segments  []enrollSegment `json:"segments"`
}

// EnrollSpeaker asks the local worker to extract voiceprints for the marked
// speaker. Label persistence must not depend on this succeeding: if the voice
// service is disabled, the caller keeps the human-readable name.
func (w *WorkerASR) EnrollSpeaker(ctx context.Context, audioPath, name string, segs []Segment) (bool, error) {
	hostWake(ctx, w.WakeURL, w.WakeToken)
	stopAlive := hostKeepAlive(ctx, w.TouchURL, w.WakeToken)
	defer stopAlive()
	reqSegs := make([]enrollSegment, 0, len(segs))
	for _, s := range segs {
		reqSegs = append(reqSegs, enrollSegment{Start: s.Start, End: s.End})
	}
	reqObj := enrollRequest{AudioPath: audioPath, AudioName: filepath.Base(audioPath), Name: name, Segments: reqSegs}
	if b, err := os.ReadFile(audioPath); err == nil {
		reqObj.AudioB64 = base64.StdEncoding.EncodeToString(b)
	}
	body, _ := json.Marshal(reqObj)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.BaseURL+"/v1/voices/enroll", bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := w.HC.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotImplemented || resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if resp.StatusCode != http.StatusOK {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(resp.Body)
		return false, fmt.Errorf("voice worker status %d: %s", resp.StatusCode, buf.String())
	}
	return true, nil
}
