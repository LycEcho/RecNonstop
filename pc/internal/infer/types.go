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
	Text     string    `json:"text"`
	Segments []Segment `json:"segments"`
	Language string    `json:"language"`
	Duration float64   `json:"duration"`
}

type Action struct {
	Speaker string `json:"speaker"`
	Action  string `json:"action"`
	Details string `json:"details"`
}

type Analysis struct {
	Summary  string          `json:"summary"`
	Speakers []string        `json:"speakers"`
	Actions  []Action        `json:"actions"`
	Risks    []string        `json:"risks"`
	Raw      json.RawMessage `json:"-"`
}

type ASR interface {
	Transcribe(ctx context.Context, audioPath string) (*Transcript, error)
}

type Analyzer interface {
	Analyze(ctx context.Context, t *Transcript) (*Analysis, error)
}
