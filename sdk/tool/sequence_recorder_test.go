package tool

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/leef-l/brain/sdk/persistence"
)

type memInteractionSink struct {
	seqs []*persistence.InteractionSequence
}

func (m *memInteractionSink) RecordInteractionSequence(_ context.Context, seq *persistence.InteractionSequence) error {
	m.seqs = append(m.seqs, seq)
	return nil
}

func (m *memInteractionSink) ListInteractionSequences(_ context.Context, brainKind string, _ int) ([]*persistence.InteractionSequence, error) {
	var out []*persistence.InteractionSequence
	for _, s := range m.seqs {
		if brainKind == "" || s.BrainKind == brainKind {
			out = append(out, s)
		}
	}
	return out, nil
}

func TestSequenceRecorderPersistsViaSink(t *testing.T) {
	sink := &memInteractionSink{}
	SetInteractionSink(sink)
	t.Cleanup(func() { SetInteractionSink(nil) })

	ctx := context.Background()
	BindRecorder(ctx, "run-abc", "browser", "log into Gitea")

	recordInteractionForLearning(ctx, "browser.navigate",
		json.RawMessage(`{"url":"https://demo.gitea.com/user/login"}`), &Result{})
	recordInteractionForLearning(ctx, "browser.type",
		json.RawMessage(`{"id":17,"text":"tester"}`), &Result{})

	if err := FinishRecorder(ctx, "success"); err != nil {
		t.Fatalf("FinishRecorder: %v", err)
	}
	// idempotent
	if err := FinishRecorder(ctx, "success"); err != nil {
		t.Fatalf("second FinishRecorder: %v", err)
	}

	if len(sink.seqs) != 1 {
		t.Fatalf("expected 1 persisted sequence, got %d", len(sink.seqs))
	}
	seq := sink.seqs[0]
	if seq.BrainKind != "browser" {
		t.Errorf("brain_kind = %q", seq.BrainKind)
	}
	if seq.Outcome != "success" {
		t.Errorf("outcome = %q", seq.Outcome)
	}
	if seq.Site != "https://demo.gitea.com" {
		t.Errorf("site = %q", seq.Site)
	}
	if len(seq.Actions) != 2 {
		t.Errorf("actions = %d", len(seq.Actions))
	}
}

func TestSequenceRecorderEmptyNoSave(t *testing.T) {
	sink := &memInteractionSink{}
	SetInteractionSink(sink)
	t.Cleanup(func() { SetInteractionSink(nil) })

	ctx := context.Background()
	BindRecorder(ctx, "run-empty", "browser", "nothing")
	if err := FinishRecorder(ctx, "success"); err != nil {
		t.Fatalf("FinishRecorder: %v", err)
	}
	if len(sink.seqs) != 0 {
		t.Errorf("empty recorder should not flush, got %d", len(sink.seqs))
	}
}

func TestFinishRecorderWithoutSinkIsNoop(t *testing.T) {
	SetInteractionSink(nil)
	ctx := context.Background()
	BindRecorder(ctx, "run-no-sink", "browser", "goal")
	recordInteractionForLearning(ctx, "browser.click", json.RawMessage(`{"id":1}`), &Result{})
	if err := FinishRecorder(ctx, "success"); err != nil {
		t.Fatalf("should not error without sink: %v", err)
	}
}

func TestRecordInteractionUnboundCtxIsSilent(t *testing.T) {
	SetInteractionSink(nil)
	recordInteractionForLearning(context.Background(), "browser.click", json.RawMessage(`{"id":1}`), &Result{})
}

func TestLearnFromSequencesFromSource(t *testing.T) {
	sink := &memInteractionSink{}
	now := time.Now().UTC()
	for i := 0; i < 3; i++ {
		sink.seqs = append(sink.seqs, &persistence.InteractionSequence{
			RunID:     "run-" + string(rune('a'+i)),
			BrainKind: "browser",
			Goal:      "login to demo",
			Site:      "https://demo.gitea.com",
			Outcome:   "success",
			StartedAt: now,
			Actions: []persistence.InteractionAction{
				{Tool: "browser.navigate", Params: `{"url":"https://demo.gitea.com/user/login"}`},
				{Tool: "browser.type", Params: `{"id":17,"text":"tester"}`},
				{Tool: "browser.click", Params: `{"id":18}`},
			},
		})
	}

	dsn := filepath.Join(t.TempDir(), "patterns.db")
	lib, err := NewPatternLibrary(dsn)
	if err != nil {
		t.Fatalf("NewPatternLibrary: %v", err)
	}
	defer lib.Close()

	n, err := LearnFromSequences(context.Background(), lib, sink, 100)
	if err != nil {
		t.Fatalf("LearnFromSequences: %v", err)
	}
	if n < 1 {
		t.Fatalf("expected ≥1 new pattern, got %d", n)
	}
}
