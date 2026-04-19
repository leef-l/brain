package persistence

import (
	"context"
	"time"
)

// LearningProfile is the serializable form of a brain capability profile (L1).
type LearningProfile struct {
	BrainKind string    `json:"brain_kind"`
	ColdStart bool      `json:"cold_start"`
	UpdatedAt time.Time `json:"updated_at"`
}

// LearningTaskScore is the serializable form of a task type score within a profile.
type LearningTaskScore struct {
	BrainKind       string  `json:"brain_kind"`
	TaskType        string  `json:"task_type"`
	SampleCount     int     `json:"sample_count"`
	AccuracyValue   float64 `json:"accuracy_value"`
	AccuracyAlpha   float64 `json:"accuracy_alpha"`
	SpeedValue      float64 `json:"speed_value"`
	SpeedAlpha      float64 `json:"speed_alpha"`
	CostValue       float64 `json:"cost_value"`
	CostAlpha       float64 `json:"cost_alpha"`
	StabilityValue  float64 `json:"stability_value"`
	StabilityAlpha  float64 `json:"stability_alpha"`
}

// LearningSequence is the serializable form of a task sequence record (L2).
type LearningSequence struct {
	ID         int64                `json:"id"`
	SequenceID string              `json:"sequence_id"`
	TotalScore float64             `json:"total_score"`
	RecordedAt time.Time           `json:"recorded_at"`
	Steps      []LearningSeqStep   `json:"steps"`
}

// LearningSeqStep is a single step within a sequence record.
type LearningSeqStep struct {
	BrainKind  string  `json:"brain_kind"`
	TaskType   string  `json:"task_type"`
	DurationMs int64   `json:"duration_ms"`
	Score      float64 `json:"score"`
}

// LearningPreference is the serializable form of a user preference (L3).
type LearningPreference struct {
	Category  string    `json:"category"`
	Value     string    `json:"value"`
	Weight    float64   `json:"weight"`
	UpdatedAt time.Time `json:"updated_at"`
}

// LearningStore persists L1/L2/L3 learning data.
type LearningStore interface {
	// L1
	SaveProfile(ctx context.Context, profile *LearningProfile) error
	SaveTaskScore(ctx context.Context, score *LearningTaskScore) error
	ListProfiles(ctx context.Context) ([]*LearningProfile, error)
	ListTaskScores(ctx context.Context, brainKind string) ([]*LearningTaskScore, error)

	// L2
	SaveSequence(ctx context.Context, seq *LearningSequence) error
	ListSequences(ctx context.Context, limit int) ([]*LearningSequence, error)

	// L3
	SavePreference(ctx context.Context, pref *LearningPreference) error
	GetPreference(ctx context.Context, category string) (*LearningPreference, error)
	ListPreferences(ctx context.Context) ([]*LearningPreference, error)
}
