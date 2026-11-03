package infer

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type WorkerASR struct {
	BaseURL   string
	WakeURL   string
	WakeToken string
	TouchURL  string
	HC        *http.Client
}

// WorkerHTTPError keeps transport failures distinct from invalid input or JSON.
type WorkerHTTPError struct {
	StatusCode int
	Body       string
}

func (e *WorkerHTTPError) Error() string {
	return fmt.Sprintf("worker status %d: %s", e.StatusCode, e.Body)
}

// IsRetryableASRError only permits temporary backend/connection failures.
// Bad requests, authentication failures and malformed successful replies must
// not hold a queue worker until the job deadline.
func IsRetryableASRError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	var response *WorkerHTTPError
	if errors.As(err, &response) {
		switch response.StatusCode {
		case http.StatusTooManyRequests, http.StatusBadGateway,
			http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			return true
		default:
			return false
		}
	}
	var timeout net.Error
	if errors.As(err, &timeout) && timeout.Timeout() {
		return true
	}
	var connection *net.OpError
	if errors.As(err, &connection) {
		return connection.Op == "dial" || connection.Op == "read" || connection.Op == "write"
	}
	var dns *net.DNSError
	return errors.As(err, &dns)
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
	Text        string           `json:"text"`
	Sentences   []workerSentence `json:"sentences"`
	Language    string           `json:"language"`
	Duration    float64          `json:"duration"`
	AudioEvents json.RawMessage  `json:"audio_events"`
}

// Optional event data must not discard a valid transcript when an older or
// incompatible worker sends malformed tags. Keep only valid windows and tags.
func parseAudioEvents(raw json.RawMessage, duration float64) []AudioEvent {
	var rows []json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &rows) != nil {
		return nil
	}
	var events []AudioEvent
	for _, row := range rows {
		var input struct {
			Start *float64          `json:"start"`
			End   *float64          `json:"end"`
			Tags  []json.RawMessage `json:"tags"`
		}
		if json.Unmarshal(row, &input) != nil || input.Start == nil || input.End == nil || input.Tags == nil {
			continue
		}
		start, end := *input.Start, *input.End
		if math.IsNaN(start) || math.IsNaN(end) || math.IsInf(start, 0) || math.IsInf(end, 0) || start < 0 || end <= start {
			continue
		}
		// The worker may round its total duration to milliseconds.
		if duration > 0 && end > duration+0.01 {
			continue
		}
		tags := make([]AudioEventTag, 0, len(input.Tags))
		for _, rawTag := range input.Tags {
			var tag struct {
				Label string   `json:"label"`
				Score *float64 `json:"score"`
			}
			if json.Unmarshal(rawTag, &tag) != nil || tag.Score == nil || strings.IndexFunc(tag.Label, unicode.IsControl) >= 0 {
				continue
			}
			label, score := strings.TrimSpace(tag.Label), *tag.Score
			if label == "" || utf8.RuneCountInString(label) > 128 || math.IsNaN(score) || math.IsInf(score, 0) || score < 0 || score > 1 {
				continue
			}
			tags = append(tags, AudioEventTag{Label: label, Score: score})
		}
		if len(input.Tags) > 0 && len(tags) == 0 {
			continue // Invalid tags do not prove that a window had no sound events.
		}
		events = append(events, AudioEvent{Start: start, End: end, Tags: tags})
	}
	return events
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
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		statusErr := &WorkerHTTPError{StatusCode: resp.StatusCode, Body: string(body)}
		if readErr != nil {
			return nil, errors.Join(statusErr, readErr)
		}
		return nil, statusErr
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
		Text:        wr.Text,
		Segments:    segs,
		Language:    wr.Language,
		Duration:    wr.Duration,
		AudioEvents: parseAudioEvents(wr.AudioEvents, wr.Duration),
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
