package loop

import (
	"context"
	"sync"
)

// MemLoopDetector is the in-process LoopDetector implementation from
// 22-Agent-Loop规格.md §11. It observes LoopEvents fed in by the Runner
// after each Turn and decides whether the Run is stuck in one of the
// five degenerate patterns from §11.1:
//
//  1. Repeated tool_call — the same tool signature 3 times in a row.
//  2. Repeated error — the same error fingerprint 3 times.
//  3. No-progress turn — 5 consecutive Turns whose Thought + tool_call
//     content hash is identical.
//  4. Thought explosion — 3 consecutive Turns that emit Thought only
//     and no tool_call.
//  5. (Budget hole is checked by the Runner itself against Budget;
//     it is not a pattern the detector can see from events alone.)
//
// The detector is stdlib-only per brain骨架实施计划.md §4.6 and safe
// for concurrent calls across multiple Runs — per-Run state is kept
// under a sync.Mutex keyed by Run.ID.
//
// The escalation ladder from §11.2 (first hit → hint, second hit →
// forced tool_choice=required, third hit → Fail Run) is the Runner's
// responsibility; the detector exposes the raw strike counter so the
// Runner can pick the right action. IsLoop is set to true only on the
// third strike — that is the contract the Runner cares about.
type MemLoopDetector struct {
	// RepeatedToolCallThreshold is the strike count at which a
	// repeated tool-call pattern trips. Defaults to 3 per §11.1.
	RepeatedToolCallThreshold int

	// NoProgressTurnThreshold is the consecutive-identical-turn count
	// at which the no-progress pattern trips. Defaults to 5 per §11.1.
	NoProgressTurnThreshold int

	// ThoughtOnlyTurnThreshold is the consecutive-thought-only count
	// at which the thought-explosion pattern trips. Defaults to 3
	// per §11.1.
	ThoughtOnlyTurnThreshold int

	mu    sync.Mutex
	state map[string]*runLoopState
}

// runLoopState is the per-Run bookkeeping owned by MemLoopDetector.
// All fields are guarded by the parent's mu.
type runLoopState struct {
	// lastToolSignature is the most recent tool_call signature
	// observed on this Run; repeatCount counts consecutive repeats.
	lastToolSignature string
	repeatCount       int

	// lastContentHash is the most recent content hash observed
	// (Thought + tool_call payload); noProgressCount counts the
	// streak of identical Turn hashes.
	lastContentHash string
	noProgressCount int

	// thoughtOnlyCount counts consecutive Turns that emitted a
	// "content" event without any "tool_call" event in between.
	thoughtOnlyCount int
}

// NewMemLoopDetector builds a detector with the §11.1 defaults. Callers
// that want different thresholds (e.g. the C-L-11 compliance test)
// should construct the struct literal directly.
func NewMemLoopDetector() *MemLoopDetector {
	return &MemLoopDetector{
		RepeatedToolCallThreshold: 3,
		NoProgressTurnThreshold:   5,
		ThoughtOnlyTurnThreshold:  3,
		state:                     make(map[string]*runLoopState),
	}
}

// Observe ingests a single LoopEvent and returns the detector's
// verdict. IsLoop is true only when one of the §11.1 patterns has
// reached its strike threshold; the Runner MUST then fail the Run with
// a CodeAgentLoopDetected BrainError per §11.2.
//
// The method is safe to call concurrently across multiple Runs: Run-
// level bookkeeping is demultiplexed under the receiver's mutex.
func (d *MemLoopDetector) Observe(ctx context.Context, run *Run, event LoopEvent) (LoopVerdict, error) {
	if err := ctx.Err(); err != nil {
		return LoopVerdict{}, wrapLoopCtxErr(err)
	}
	if d == nil || run == nil {
		return LoopVerdict{}, nil
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	s := d.state[run.ID]
	if s == nil {
		s = &runLoopState{}
		d.state[run.ID] = s
	}

	switch event.Type {
	case "tool_call":
		// Pattern 1: repeated identical tool_call signature.
		sig := event.ToolName + "|" + event.ContentHash
		if sig == s.lastToolSignature && sig != "" {
			s.repeatCount++
		} else {
			s.lastToolSignature = sig
			s.repeatCount = 1
		}
		// A tool_call breaks any thought-only streak.
		s.thoughtOnlyCount = 0

		// Pattern 3: no-progress turn hash streak.
		// We fold the tool_call's ContentHash into the per-Turn
		// progress hash as well: a Turn that produced the same
		// (thought, tool_call) hash as the previous Turn counts
		// toward the no-progress streak.
		d.bumpNoProgress(s, event.ContentHash)

		if d.RepeatedToolCallThreshold > 0 && s.repeatCount >= d.RepeatedToolCallThreshold {
			return LoopVerdict{
				IsLoop:     true,
				Pattern:    "repeated_tool_call",
				Confidence: 1.0,
			}, nil
		}
		if d.NoProgressTurnThreshold > 0 && s.noProgressCount >= d.NoProgressTurnThreshold {
			return LoopVerdict{
				IsLoop:     true,
				Pattern:    "no_progress_turn",
				Confidence: 0.9,
			}, nil
		}
		return LoopVerdict{}, nil

	case "content":
		// Pattern 4: thought-only Turns piling up.
		s.thoughtOnlyCount++
		// Content events break the repeated-tool-call streak
		// because the repetition is no longer consecutive.
		s.lastToolSignature = ""
		s.repeatCount = 0

		d.bumpNoProgress(s, event.ContentHash)

		if d.ThoughtOnlyTurnThreshold > 0 && s.thoughtOnlyCount >= d.ThoughtOnlyTurnThreshold {
			return LoopVerdict{
				IsLoop:     true,
				Pattern:    "thought_explosion",
				Confidence: 0.85,
			}, nil
		}
		if d.NoProgressTurnThreshold > 0 && s.noProgressCount >= d.NoProgressTurnThreshold {
			return LoopVerdict{
				IsLoop:     true,
				Pattern:    "no_progress_turn",
				Confidence: 0.9,
			}, nil
		}
		return LoopVerdict{}, nil

	case "llm_call":
		// llm_call events are informational: they reset nothing and
		// do not participate in any §11.1 pattern directly. The
		// detector still returns a clean verdict so the Runner can
		// call Observe uniformly on every event type.
		return LoopVerdict{}, nil

	default:
		// Unknown event types are ignored — future spec versions
		// may add new signals and older detectors MUST remain
		// forward-compatible per 22 §11.2.
		return LoopVerdict{}, nil
	}
}

// Forget removes all bookkeeping for runID. The Runner SHOULD call
// Forget when a Run reaches a terminal state so the detector does not
// retain per-Run state forever.
func (d *MemLoopDetector) Forget(runID string) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.state, runID)
}

// bumpNoProgress folds a Turn-level content hash into the no-progress
// streak counter. An empty hash is ignored so the detector cannot be
// tricked by events without a ContentHash field.
func (d *MemLoopDetector) bumpNoProgress(s *runLoopState, hash string) {
	if hash == "" {
		return
	}
	if hash == s.lastContentHash {
		s.noProgressCount++
		return
	}
	s.lastContentHash = hash
	s.noProgressCount = 1
}
