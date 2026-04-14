package loop

import "time"

// Run is the top-level state of a single BrainKernel execution. A Run owns a
// Budget, transitions through a fixed State machine, and produces a sequence
// of Turns. The Run is the unit of persistence (see persistence.BrainPlan)
// and the unit of billing / tracing. See 22-Agent-Loop规格.md §4.
type Run struct {
	// ID is the globally unique identifier of this Run. It is the primary
	// key in mvp_brain_plan and the W3C trace_id root for all Turns.
	// See 22-Agent-Loop规格.md §4.1.
	ID string

	// BrainID is the kernel-level identifier of the BrainKernel instance
	// that owns this Run (see 20-BrainKernel-Overview). Multiple Runs of
	// the same BrainID share the same tool registry and agent contract.
	// See 22-Agent-Loop规格.md §4.1.
	BrainID string

	// State is the current position of the Run in the State machine.
	// Legal transitions are defined in 22-Agent-Loop规格.md §4.2.
	State State

	// Budget is the Run-level resource envelope; the Runner MUST check
	// and update it on every Turn. See 22-Agent-Loop规格.md §5.
	Budget Budget

	// StartedAt is the wall-clock timestamp when the Run was accepted and
	// moved out of StatePending. See 22-Agent-Loop规格.md §4.3.
	StartedAt time.Time

	// EndedAt is the wall-clock timestamp of the Run's terminal state
	// transition (StateCompleted / StateFailed / StateCanceled /
	// StateCrashed). nil while the Run is still live.
	// See 22-Agent-Loop规格.md §4.3.
	EndedAt *time.Time

	// CurrentTurn is the 1-based index of the Turn currently executing,
	// or the index of the last completed Turn when State is terminal.
	// See 22-Agent-Loop规格.md §4.3 and §6.
	CurrentTurn int
}

// State is the Run state-machine label defined in 22-Agent-Loop规格.md §4.2.
// Only the StateXxx constants below are legal values; unknown states MUST
// be rejected by the Runner.
type State string

// Run state constants. The legal transition graph is defined in
// 22-Agent-Loop规格.md §4.2 — consult the spec before adding new states.
const (
	// StatePending is the initial state after CreateRun but before the
	// first Turn has been scheduled. See 22-Agent-Loop规格.md §4.2.
	StatePending State = "pending"

	// StateRunning is the active execution state: the Runner is driving
	// Turns and may invoke the llm.Provider at any time.
	// See 22-Agent-Loop规格.md §4.2.
	StateRunning State = "running"

	// StateWaitingTool is entered when the current Turn is blocked on an
	// asynchronous tool.Tool execution that has not yet produced a Result.
	// See 22-Agent-Loop规格.md §4.2.
	StateWaitingTool State = "waiting_tool"

	// StatePaused is entered when a human operator has explicitly paused
	// the Run; the Runner MUST NOT start new Turns until StateRunning is
	// restored. See 22-Agent-Loop规格.md §4.2 and §11 (Control Plane).
	StatePaused State = "paused"

	// StateCompleted is the terminal success state; the Runner has
	// delivered a final assistant message and Budget headroom remained.
	// See 22-Agent-Loop规格.md §4.2.
	StateCompleted State = "completed"

	// StateFailed is the terminal failure state; a *errors.BrainError has
	// been attached to the last Turn and the Run cannot recover.
	// See 22-Agent-Loop规格.md §4.2.
	StateFailed State = "failed"

	// StateCanceled is the terminal state reached when the Run was
	// canceled by an external control-plane command.
	// See 22-Agent-Loop规格.md §4.2.
	StateCanceled State = "canceled"

	// StateCrashed is the terminal state reached when the host process
	// or sidecar crashed mid-Turn and the Run cannot be resumed via the
	// usual persistence path. See 22-Agent-Loop规格.md §4.2 and §12
	// (Crash Recovery).
	StateCrashed State = "crashed"
)
