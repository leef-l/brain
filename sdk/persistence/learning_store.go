package persistence

import (
	"context"
	"encoding/json"
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

// InteractionAction is one tool call within an InteractionSequence.
type InteractionAction struct {
	Tool        string `json:"tool"`
	Params      string `json:"params,omitempty"`        // JSON-encoded
	ElementRole string `json:"element_role,omitempty"`
	ElementName string `json:"element_name,omitempty"`
	ElementType string `json:"element_type,omitempty"`
	Result      string `json:"result,omitempty"`
}

// InteractionSequence is a per-brain, per-run tool-call trace. It is the
// learning input for brain-specific pattern miners (e.g. browser UI pattern
// clustering in sdk/tool/ui_pattern_learn.go). Sits alongside LearningSequence
// (which is the coarse-grained L2 record at the delegate level).
type InteractionSequence struct {
	ID         int64               `json:"id"`
	RunID      string              `json:"run_id"`
	BrainKind  string              `json:"brain_kind"` // "browser", "code", ...
	Goal       string              `json:"goal"`
	Site       string              `json:"site,omitempty"`
	URL        string              `json:"url,omitempty"`
	Outcome    string              `json:"outcome"` // "success" | "failure"
	DurationMs int64               `json:"duration_ms"`
	StartedAt  time.Time           `json:"started_at"`
	Actions    []InteractionAction `json:"actions"`
}

// DailySummary is the output of the per-day conversation summarizer daemon
// (Task #15). Keyed by YYYY-MM-DD; rewriting is allowed so the daemon can
// refine the same day as more runs complete.
type DailySummary struct {
	Date         string    `json:"date"` // YYYY-MM-DD
	BrainCounts  string    `json:"brain_counts"`   // JSON {"browser":12,"code":3,...}
	RunsTotal    int       `json:"runs_total"`
	RunsFailed   int       `json:"runs_failed"`
	SummaryText  string    `json:"summary_text"`   // Markdown, LLM-generated
	UpdatedAt    time.Time `json:"updated_at"`
}

// LearningPreference is the serializable form of a user preference (L3).
type LearningPreference struct {
	Category  string    `json:"category"`
	Value     string    `json:"value"`
	Weight    float64   `json:"weight"`
	UpdatedAt time.Time `json:"updated_at"`
}

// AnomalyTemplate is a cross-site recurring anomaly signature with its
// canonical recovery action sequence. P3.1 (异常模板库) persists mined
// signatures here so Browser Brain can reuse recoveries across sites.
type AnomalyTemplate struct {
	ID                int64           `json:"id"`
	SignatureType     string          `json:"signature_type"`
	SignatureSubtype  string          `json:"signature_subtype"`
	SignatureSite     string          `json:"signature_site,omitempty"`
	SignatureSeverity string          `json:"signature_severity,omitempty"`
	RecoveryActions   json.RawMessage `json:"recovery_actions"` // JSON-encoded action list
	MatchCount        int             `json:"match_count"`
	SuccessCount      int             `json:"success_count"`
	FailureCount      int             `json:"failure_count"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
}

// SiteAnomalyProfile aggregates per-site anomaly frequency and recovery stats.
// P3.1 uses this to detect sites that need elevated defense (high frequency)
// or model retraining (low recovery success rate).
type SiteAnomalyProfile struct {
	SiteOrigin           string    `json:"site_origin"`
	AnomalyType          string    `json:"anomaly_type"`
	AnomalySubtype       string    `json:"anomaly_subtype"`
	Frequency            int       `json:"frequency"`
	AvgDurationMs        int64     `json:"avg_duration_ms"`
	RecoverySuccessRate  float64   `json:"recovery_success_rate"`
	LastSeenAt           time.Time `json:"last_seen_at"`
}

// PatternFailureSample records a single failure of a UI pattern — site,
// anomaly subtype, failing step index, and page fingerprint. P3.2 clusters
// these samples to decide when to split a pattern into site-specific variants.
type PatternFailureSample struct {
	ID              int64           `json:"id"`
	PatternID       string          `json:"pattern_id"`
	SiteOrigin      string          `json:"site_origin"`
	AnomalySubtype  string          `json:"anomaly_subtype,omitempty"`
	FailureStep     int             `json:"failure_step"`
	PageFingerprint json.RawMessage `json:"page_fingerprint"` // JSON
	FailedAt        time.Time       `json:"failed_at"`
}

// HumanDemoSequence captures a sequence of RecordedActions taken by a human
// during a takeover session. P3.3 surfaces these for review and, once
// approved, feeds them back into the pattern library as a new UI pattern seed.
type HumanDemoSequence struct {
	ID         int64           `json:"id"`
	RunID      string          `json:"run_id"`
	BrainKind  string          `json:"brain_kind"`
	Goal       string          `json:"goal"`
	Site       string          `json:"site,omitempty"`
	URL        string          `json:"url,omitempty"`
	Actions    json.RawMessage `json:"actions"` // JSON-encoded RecordedAction list
	Approved   bool            `json:"approved"`
	RecordedAt time.Time       `json:"recorded_at"`
}

// SitemapSnapshot caches the result of a crawled site map so repeat planner
// invocations within a TTL do not re-walk the whole tree. P3.4 reads/writes
// this table; PurgeSitemapSnapshots evicts stale rows.
type SitemapSnapshot struct {
	ID          int64           `json:"id"`
	SiteOrigin  string          `json:"site_origin"`
	Depth       int             `json:"depth"`
	URLs        json.RawMessage `json:"urls"` // JSON-encoded URL list
	CollectedAt time.Time       `json:"collected_at"`
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

	// L2 — per-brain interaction traces (browser action flows etc.)
	SaveInteractionSequence(ctx context.Context, seq *InteractionSequence) error
	ListInteractionSequences(ctx context.Context, brainKind string, limit int) ([]*InteractionSequence, error)

	// L3
	SavePreference(ctx context.Context, pref *LearningPreference) error
	GetPreference(ctx context.Context, category string) (*LearningPreference, error)
	ListPreferences(ctx context.Context) ([]*LearningPreference, error)

	// Daily summary (Task #15). Upsert by Date.
	SaveDailySummary(ctx context.Context, s *DailySummary) error
	GetDailySummary(ctx context.Context, date string) (*DailySummary, error)
	ListDailySummaries(ctx context.Context, limit int) ([]*DailySummary, error)

	// P3.1 — Anomaly templates (cross-site recurring signatures + recovery).
	SaveAnomalyTemplate(ctx context.Context, tpl *AnomalyTemplate) error
	GetAnomalyTemplate(ctx context.Context, id int64) (*AnomalyTemplate, error)
	ListAnomalyTemplates(ctx context.Context) ([]*AnomalyTemplate, error)
	DeleteAnomalyTemplate(ctx context.Context, id int64) error

	// P3.1 — Per-site anomaly aggregation. Upsert by (site, type, subtype).
	UpsertSiteAnomalyProfile(ctx context.Context, p *SiteAnomalyProfile) error
	ListSiteAnomalyProfiles(ctx context.Context, site string) ([]*SiteAnomalyProfile, error)

	// P3.2 — Pattern failure samples for clustering / pattern splitting.
	SavePatternFailureSample(ctx context.Context, s *PatternFailureSample) error
	ListPatternFailureSamples(ctx context.Context, patternID string) ([]*PatternFailureSample, error)

	// P3.3 — Human-demo sequences recorded during takeover. approvedOnly
	// filters to rows that a reviewer has approved for pattern ingestion.
	SaveHumanDemoSequence(ctx context.Context, seq *HumanDemoSequence) error
	ListHumanDemoSequences(ctx context.Context, approvedOnly bool) ([]*HumanDemoSequence, error)
	GetHumanDemoSequence(ctx context.Context, id int64) (*HumanDemoSequence, error)
	ApproveHumanDemoSequence(ctx context.Context, id int64) error
	DeleteHumanDemoSequence(ctx context.Context, id int64) error
	PurgeHumanDemoSequences(ctx context.Context, olderThan time.Time) (int64, error)

	// P3.4 — Sitemap snapshot cache. GetSitemapSnapshot returns the most
	// recent entry for (site, depth) or nil if absent.
	SaveSitemapSnapshot(ctx context.Context, snap *SitemapSnapshot) error
	GetSitemapSnapshot(ctx context.Context, siteOrigin string, depth int) (*SitemapSnapshot, error)
	PurgeSitemapSnapshots(ctx context.Context, olderThan time.Time) (int64, error)
}
