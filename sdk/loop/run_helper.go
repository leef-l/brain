package loop

import (
	"fmt"
	"time"

	brainerrors "github.com/leef-l/brain/sdk/errors"
)

// NewRun constructs a Run in StatePending. The id, brainID, and budget are
// supplied by the caller; all timestamps are zero-value until the Run is
// started via Start. CurrentTurn is initialised to 0.
//
// See 22-Agent-Loop规格.md §4.1.
func NewRun(id, brainID string, budget Budget) *Run {
	return &Run{
		ID:      id,
		BrainID: brainID,
		State:   StatePending,
		Budget:  budget,
	}
}

// Start transitions the Run from StatePending to StateRunning and stamps
// StartedAt with now. It returns a *brainerrors.BrainError with
// CodeInvariantViolated if the current State does not allow this transition.
//
// Legal precondition: StatePending.
// See 22-Agent-Loop规格.md §4.2.
func (r *Run) Start(now time.Time) error {
	if r.State != StatePending {
		return brainerrors.New(brainerrors.CodeInvariantViolated,
			brainerrors.WithMessage(fmt.Sprintf(
				"Run.Start: illegal state transition %s → running", r.State,
			)),
		)
	}
	r.State = StateRunning
	r.StartedAt = now
	return nil
}

// Complete transitions the Run from StateRunning or StateWaitingTool to
// StateCompleted and stamps EndedAt with now. It returns a
// *brainerrors.BrainError with CodeInvariantViolated on an illegal transition.
//
// Legal preconditions: StateRunning, StateWaitingTool.
// See 22-Agent-Loop规格.md §4.2.
func (r *Run) Complete(now time.Time) error {
	switch r.State {
	case StateRunning, StateWaitingTool:
	default:
		return brainerrors.New(brainerrors.CodeInvariantViolated,
			brainerrors.WithMessage(fmt.Sprintf(
				"Run.Complete: illegal state transition %s → completed", r.State,
			)),
		)
	}
	r.State = StateCompleted
	t := now
	r.EndedAt = &t
	return nil
}

// Fail transitions the Run to StateFailed from any non-terminal,
// non-pending state and stamps EndedAt with now. It returns a
// *brainerrors.BrainError with CodeInvariantViolated on an illegal transition.
//
// Legal preconditions: StateRunning, StateWaitingTool, StatePaused.
// See 22-Agent-Loop规格.md §4.2.
func (r *Run) Fail(now time.Time) error {
	switch r.State {
	case StateRunning, StateWaitingTool, StatePaused:
	default:
		return brainerrors.New(brainerrors.CodeInvariantViolated,
			brainerrors.WithMessage(fmt.Sprintf(
				"Run.Fail: illegal state transition %s → failed", r.State,
			)),
		)
	}
	r.State = StateFailed
	t := now
	r.EndedAt = &t
	return nil
}

// Pause transitions the Run from StateRunning or StateWaitingTool to
// StatePaused. The Run may be resumed later via Resume. It returns a
// *brainerrors.BrainError with CodeInvariantViolated on an illegal transition.
//
// Legal preconditions: StateRunning, StateWaitingTool.
// See 22-Agent-Loop规格.md §4.2 and §11 (Control Plane).
func (r *Run) Pause() error {
	switch r.State {
	case StateRunning, StateWaitingTool:
	default:
		return brainerrors.New(brainerrors.CodeInvariantViolated,
			brainerrors.WithMessage(fmt.Sprintf(
				"Run.Pause: illegal state transition %s → paused", r.State,
			)),
		)
	}
	r.State = StatePaused
	return nil
}

// Resume transitions the Run from StatePaused back to StateRunning. It
// returns a *brainerrors.BrainError with CodeInvariantViolated on an illegal
// transition.
//
// Legal precondition: StatePaused.
// See 22-Agent-Loop规格.md §4.2 and §11 (Control Plane).
func (r *Run) Resume() error {
	if r.State != StatePaused {
		return brainerrors.New(brainerrors.CodeInvariantViolated,
			brainerrors.WithMessage(fmt.Sprintf(
				"Run.Resume: illegal state transition %s → running", r.State,
			)),
		)
	}
	r.State = StateRunning
	return nil
}

// Cancel transitions the Run to StateCanceled from any non-terminal state
// and stamps EndedAt with now. It returns a *brainerrors.BrainError with
// CodeInvariantViolated if the Run is already in a terminal state.
//
// Legal preconditions: StatePending, StateRunning, StateWaitingTool,
// StatePaused.
// See 22-Agent-Loop规格.md §4.2 and §11 (Control Plane).
func (r *Run) Cancel(now time.Time) error {
	switch r.State {
	case StatePending, StateRunning, StateWaitingTool, StatePaused:
	default:
		return brainerrors.New(brainerrors.CodeInvariantViolated,
			brainerrors.WithMessage(fmt.Sprintf(
				"Run.Cancel: illegal state transition %s → canceled", r.State,
			)),
		)
	}
	r.State = StateCanceled
	t := now
	r.EndedAt = &t
	return nil
}
