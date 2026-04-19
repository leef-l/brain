package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/leef-l/brain/sdk/persistence"
)

// Pattern learning layer — derive new UIPatterns from successful browser
// interaction sequences recorded via LearningEngine.RecordInteractionSequence.
//
// See sdk/docs/40-Browser-Brain语义理解架构.md §3.3 "Learning Data Source".
//
// Clustering rule (v1): group sequences by (site_origin, goal_hash). A cluster
// with N ≥ 3 successes and internal action similarity ≥ 0.7 becomes a new
// pattern with source="learned".

// LearnedSequence is the in-memory form of an interaction trace used by the
// clustering logic below. It's converted from persistence.InteractionSequence
// on read.
type LearnedSequence struct {
	RunID      string           `json:"run_id"`
	Goal       string           `json:"goal"`
	Site       string           `json:"site"` // origin (scheme://host)
	URL        string           `json:"url"`
	Actions    []RecordedAction `json:"actions"`
	Outcome    string           `json:"outcome"` // "success" / "failure"
	DurationMS int64            `json:"duration_ms"`
	StartedAt  time.Time        `json:"started_at"`
}

// RecordedAction is one tool call within a sequence.
type RecordedAction struct {
	Tool        string                 `json:"tool"`
	Params      map[string]interface{} `json:"params,omitempty"`
	ElementRole string                 `json:"element_role,omitempty"`
	ElementName string                 `json:"element_name,omitempty"`
	ElementType string                 `json:"element_type,omitempty"`
	Result      string                 `json:"result,omitempty"`
}

// InteractionSource provides read access to persisted interaction sequences.
// In production this is LearningEngine.ListInteractionSequences (see
// sdk/kernel/learning.go). Tests inject an in-memory stub.
type InteractionSource interface {
	ListInteractionSequences(ctx context.Context, brainKind string, limit int) ([]*persistence.InteractionSequence, error)
}

// LearnFromSequences reads recent browser interaction sequences from the
// LearningStore, clusters successful runs, and inserts new UIPatterns into the
// library. Returns the count of newly minted patterns.
//
// Passing limit=0 means "use default" (currently 500 most recent).
func LearnFromSequences(ctx context.Context, lib *PatternLibrary, source InteractionSource, limit int) (int, error) {
	if source == nil || lib == nil {
		return 0, nil
	}
	if limit <= 0 {
		limit = 500
	}

	stored, err := source.ListInteractionSequences(ctx, "browser", limit)
	if err != nil {
		return 0, fmt.Errorf("list sequences: %w", err)
	}
	if len(stored) == 0 {
		return 0, nil
	}

	sequences := make([]*LearnedSequence, 0, len(stored))
	for _, s := range stored {
		sequences = append(sequences, convertToLearnedSequence(s))
	}

	type clusterKey struct {
		site     string
		goalHash string
	}
	clusters := map[clusterKey][]*LearnedSequence{}
	for _, s := range sequences {
		if s.Outcome != "success" {
			continue
		}
		k := clusterKey{site: s.Site, goalHash: goalHash(s.Goal)}
		clusters[k] = append(clusters[k], s)
	}

	newCount := 0
	for key, seqs := range clusters {
		if len(seqs) < 3 {
			continue
		}
		if similarity := sequenceSimilarity(seqs); similarity < 0.7 {
			continue
		}
		pattern := synthesizePattern(key.site, seqs)
		if pattern == nil {
			continue
		}
		if existing := lib.Get(pattern.ID); existing != nil {
			continue
		}
		if err := lib.Upsert(ctx, pattern); err != nil {
			continue
		}
		newCount++
	}
	return newCount, nil
}

// convertToLearnedSequence turns a persistence record into the clustering-ready
// form. Action params are stored as JSON strings on disk and re-parsed here.
func convertToLearnedSequence(s *persistence.InteractionSequence) *LearnedSequence {
	out := &LearnedSequence{
		RunID:      s.RunID,
		Goal:       s.Goal,
		Site:       s.Site,
		URL:        s.URL,
		Outcome:    s.Outcome,
		DurationMS: s.DurationMs,
		StartedAt:  s.StartedAt,
	}
	for _, a := range s.Actions {
		var params map[string]interface{}
		if a.Params != "" {
			_ = json.Unmarshal([]byte(a.Params), &params)
		}
		out.Actions = append(out.Actions, RecordedAction{
			Tool:        a.Tool,
			Params:      params,
			ElementRole: a.ElementRole,
			ElementName: a.ElementName,
			ElementType: a.ElementType,
			Result:      a.Result,
		})
	}
	return out
}

// sequenceSimilarity is a rough Jaccard of action-tool sequences across cluster members.
// 1.0 = identical shapes; 0.0 = no overlap.
func sequenceSimilarity(seqs []*LearnedSequence) float64 {
	if len(seqs) < 2 {
		return 1.0
	}
	first := actionSignature(seqs[0])
	total := 0.0
	count := 0
	for i := 1; i < len(seqs); i++ {
		other := actionSignature(seqs[i])
		total += jaccard(first, other)
		count++
	}
	if count == 0 {
		return 1.0
	}
	return total / float64(count)
}

func actionSignature(s *LearnedSequence) []string {
	out := make([]string, 0, len(s.Actions))
	for _, a := range s.Actions {
		out = append(out, a.Tool)
	}
	return out
}

func jaccard(a, b []string) float64 {
	seen := map[string]bool{}
	for _, x := range a {
		seen[x] = true
	}
	inter := 0
	union := len(seen)
	for _, y := range b {
		if seen[y] {
			inter++
		} else {
			union++
		}
	}
	if union == 0 {
		return 1.0
	}
	return float64(inter) / float64(union)
}

// synthesizePattern builds a draft UIPattern from a success cluster.
func synthesizePattern(site string, seqs []*LearnedSequence) *UIPattern {
	if len(seqs) == 0 {
		return nil
	}
	representative := seqs[0]
	id := fmt.Sprintf("learned_%s_%s", sanitizeID(site), goalHash(representative.Goal))

	var steps []ActionStep
	for _, act := range representative.Actions {
		step := ActionStep{
			Tool:   act.Tool,
			Params: cloneParams(act.Params),
		}
		steps = append(steps, step)
	}
	return &UIPattern{
		ID:             id,
		Category:       "learned",
		Source:         "learned",
		ActionSequence: steps,
		Stats: PatternStats{
			MatchCount:   len(seqs),
			SuccessCount: len(seqs),
		},
	}
}

func cloneParams(in map[string]interface{}) map[string]interface{} {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]interface{}, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func sanitizeID(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= '0' && r <= '9',
			r == '_' || r == '-':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + 32)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}
