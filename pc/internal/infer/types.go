package infer

import (
	"context"
	"encoding/json"
)

type Segment struct {
	Start      float64 `json:"start"`
	End        float64 `json:"end"`
	Speaker    string  `json:"speaker"`
	Person     string  `json:"person,omitempty"`
	PersonName string  `json:"person_name,omitempty"`
	Text       string  `json:"text"`
}

type Transcript struct {
	Text        string       `json:"text"`
	Segments    []Segment    `json:"segments"`
	Language    string       `json:"language"`
	Duration    float64      `json:"duration"`
	AudioEvents []AudioEvent `json:"audio_events,omitempty"`
}

// AudioEvent describes optional acoustic tags within a time window in seconds.
// Scores are model confidence values, not proof of a semantic scene or activity.
type AudioEvent struct {
	Start float64         `json:"start"`
	End   float64         `json:"end"`
	Tags  []AudioEventTag `json:"tags"`
}

type AudioEventTag struct {
	Label string  `json:"label"`
	Score float64 `json:"score"`
}

type Action struct {
	Speaker string `json:"speaker"`
	Owner   string `json:"owner,omitempty"`
	Action  string `json:"action"`
	Details string `json:"details"`
}

type Analysis struct {
	Summary     string          `json:"summary"`
	Speakers    []string        `json:"speakers"`
	Actions     []Action        `json:"actions"`
	Constraints []string        `json:"constraints"`
	Risks       []string        `json:"risks"`
	Raw         json.RawMessage `json:"-"`
}

type ASR interface {
	Transcribe(ctx context.Context, audioPath string) (*Transcript, error)
}

type Analyzer interface {
	Analyze(ctx context.Context, t *Transcript) (*Analysis, error)
}
