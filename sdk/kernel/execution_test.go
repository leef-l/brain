package kernel

import (
	"testing"
)

func TestNewTaskExecution_DefaultValues(t *testing.T) {
	te := NewTaskExecution(TaskExecutionConfig{BrainID: "central"})

	if te.State != StatePending {
		t.Errorf("expected pending, got %s", te.State)
	}
	if te.Mode != ModeInteractive {
		t.Errorf("expected interactive, got %s", te.Mode)
	}
	if te.Lifecycle != LifecycleOneshot {
		t.Errorf("expected oneshot, got %s", te.Lifecycle)
	}
	if te.Restart != RestartNever {
		t.Errorf("expected never, got %s", te.Restart)
	}
	if te.ID == "" {
		t.Error("expected non-empty ID")
	}
}

func TestTransition_HappyPath(t *testing.T) {
	te := NewTaskExecution(TaskExecutionConfig{BrainID: "central"})

	steps := []ExecutionState{
		StateRunning,
		StateWaitingTool,
		StateRunning,
		StateCompleted,
	}

	for _, target := range steps {
		if err := te.Transition(target); err != nil {
			t.Fatalf("transition to %s failed: %v", target, err)
		}
	}

	if !te.State.IsTerminal() {
		t.Error("expected terminal state")
	}
	if te.EndedAt == nil {
		t.Error("expected EndedAt to be set")
	}
}

func TestTransition_InvalidRejected(t *testing.T) {
	te := NewTaskExecution(TaskExecutionConfig{BrainID: "central"})

	// pending → completed is invalid
	if err := te.Transition(StateCompleted); err == nil {
		t.Error("expected error for pending → completed")
	}

	// pending → waiting_tool is invalid
	if err := te.Transition(StateWaitingTool); err == nil {
		t.Error("expected error for pending → waiting_tool")
	}
}

func TestTransition_TerminalBlocks(t *testing.T) {
	te := NewTaskExecution(TaskExecutionConfig{BrainID: "central"})
	_ = te.Transition(StateRunning)
	_ = te.Transition(StateCompleted)

	if err := te.Transition(StateRunning); err == nil {
		t.Error("expected error transitioning from terminal state")
	}
}

func TestTransition_DaemonDraining(t *testing.T) {
	te := NewTaskExecution(TaskExecutionConfig{
		BrainID:   "quant",
		Lifecycle: LifecycleDaemon,
	})

	_ = te.Transition(StateRunning)
	_ = te.Transition(StateDraining)

	if err := te.Transition(StateCanceled); err != nil {
		t.Fatalf("draining → canceled failed: %v", err)
	}
}

func TestShouldRestart_Never(t *testing.T) {
	te := NewTaskExecution(TaskExecutionConfig{
		BrainID: "central",
		Restart: RestartNever,
	})
	_ = te.Transition(StateRunning)
	_ = te.Transition(StateFailed)

	if te.ShouldRestart() {
		t.Error("RestartNever should not restart")
	}
}

func TestShouldRestart_OnFailure(t *testing.T) {
	te := NewTaskExecution(TaskExecutionConfig{
		BrainID:    "quant",
		Restart:    RestartOnFailure,
		MaxRestart: 2,
	})
	_ = te.Transition(StateRunning)
	_ = te.Transition(StateFailed)

	if !te.ShouldRestart() {
		t.Error("expected restart on failure")
	}

	te.IncrementRestart()
	te.IncrementRestart()

	if te.ShouldRestart() {
		t.Error("should not restart after max retries")
	}
}

func TestTransition_CrashRestartCycle(t *testing.T) {
	te := NewTaskExecution(TaskExecutionConfig{
		BrainID:    "data",
		Restart:    RestartOnFailure,
		MaxRestart: 2,
	})

	_ = te.Transition(StateRunning)
	_ = te.Transition(StateCrashed)

	if !te.ShouldRestart() {
		t.Fatal("expected restart after crash")
	}

	te.IncrementRestart()
	_ = te.Transition(StateRestarting)
	_ = te.Transition(StatePending)
	_ = te.Transition(StateRunning)
	_ = te.Transition(StateCompleted)

	if te.State != StateCompleted {
		t.Errorf("expected completed, got %s", te.State)
	}
}

func TestTransition_Interrupted(t *testing.T) {
	te := NewTaskExecution(TaskExecutionConfig{
		BrainID: "quant",
		Restart: RestartOnFailure,
	})

	_ = te.Transition(StateRunning)
	_ = te.Transition(StateInterrupted)

	if !te.ShouldRestart() {
		t.Error("expected restart after interrupt")
	}

	_ = te.Transition(StateRestarting)
	_ = te.Transition(StatePending)

	if te.State != StatePending {
		t.Errorf("expected pending, got %s", te.State)
	}
}

func TestSnapshot(t *testing.T) {
	te := NewTaskExecution(TaskExecutionConfig{
		BrainID:  "central",
		ParentID: "parent-1",
	})

	snap := te.Snapshot()
	if snap.ID != te.ID {
		t.Error("snapshot ID mismatch")
	}
	if snap.ParentID != "parent-1" {
		t.Error("snapshot ParentID mismatch")
	}
	if snap.State != StatePending {
		t.Errorf("snapshot state expected pending, got %s", snap.State)
	}
}

func TestUniqueIDs(t *testing.T) {
	ids := make(map[string]bool)
	for i := 0; i < 100; i++ {
		te := NewTaskExecution(TaskExecutionConfig{BrainID: "central"})
		if ids[te.ID] {
			t.Fatalf("duplicate ID: %s", te.ID)
		}
		ids[te.ID] = true
	}
}

func TestWatchLifecycle_WaitingEvent(t *testing.T) {
	te := NewTaskExecution(TaskExecutionConfig{
		BrainID:   "browser",
		Lifecycle: LifecycleWatch,
	})

	_ = te.Transition(StateRunning)

	if err := te.Transition(StateWaitingEvent); err != nil {
		t.Fatalf("running → waiting_event failed: %v", err)
	}

	if err := te.Transition(StateRunning); err != nil {
		t.Fatalf("waiting_event → running failed: %v", err)
	}
}
