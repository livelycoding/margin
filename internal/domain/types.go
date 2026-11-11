package domain

import "context"

const MaxText = 150000

type Voice struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	ShortName  string `json:"short_name"`
	Accent     string `json:"accent"`
	Gender     string `json:"gender"`
	PreviewURL string `json:"preview_url,omitempty"`
}

type EngineStatus struct {
	Ready   bool   `json:"ready"`
	Name    string `json:"name"`
	Message string `json:"message"`
}

type Engine interface {
	Status() EngineStatus
	Voices() []Voice
	Synthesize(context.Context, string, string, float64) ([]float32, int, error)
	Close() error
}

type Segment struct {
	Text  string  `json:"text"`
	Pause float64 `json:"pause"`
}

type Cue struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Text  string  `json:"text"`
}

type Episode struct {
	ID              string   `json:"id"`
	Title           string   `json:"title"`
	Text            string   `json:"text"`
	Voice           string   `json:"voice"`
	Speed           float64  `json:"speed"`
	SourceURL       string   `json:"source_url"`
	CreatedAt       string   `json:"created_at"`
	Status          string   `json:"status"`
	Progress        int      `json:"progress"`
	Duration        float64  `json:"duration"`
	WordCount       int      `json:"word_count"`
	Cues            []Cue    `json:"cues"`
	Error           *string  `json:"error"`
	ETASeconds      *float64 `json:"eta_seconds"`
	GenerationPhase *string  `json:"generation_phase"`
	CancelRequested bool     `json:"cancel_requested,omitempty"`
	RecastOf        string   `json:"recast_of,omitempty"`
}

type Article struct {
	Title     string `json:"title"`
	Text      string `json:"text"`
	SourceURL string `json:"url"`
}
