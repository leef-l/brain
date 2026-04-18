package kernel

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/leef-l/brain/sdk/loop"
)

// ---------------------------------------------------------------------------
// ExecutionState — 12 状态枚举
// ---------------------------------------------------------------------------

type ExecutionState string

const (
	StatePending      ExecutionState = "pending"
	StateRunning      ExecutionState = "running"
	StateWaitingTool  ExecutionState = "waiting_tool"
	StateWaitingEvent ExecutionState = "waiting_event"
	StatePaused       ExecutionState = "paused"
	StateDraining     ExecutionState = "draining"
	StateInterrupted  ExecutionState = "interrupted"
	StateRestarting   ExecutionState = "restarting"
	StateCompleted    ExecutionState = "completed"
	StateFailed       ExecutionState = "failed"
	StateCanceled     ExecutionState = "canceled"
	StateCrashed      ExecutionState = "crashed"
)

func (s ExecutionState) IsTerminal() bool {
	switch s {
	case StateCompleted, StateFailed, StateCanceled, StateCrashed:
		return true
	}
	return false
}

func (s ExecutionState) IsActive() bool {
	switch s {
	case StateRunning, StateWaitingTool, StateWaitingEvent, StatePaused, StateDraining, StateInterrupted:
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// ExecutionMode / LifecyclePolicy / RestartPolicy
// ---------------------------------------------------------------------------

type ExecutionMode string

const (
	ModeInteractive ExecutionMode = "interactive"
	ModeBackground  ExecutionMode = "background"
)

type LifecyclePolicy string

const (
	LifecycleOneshot LifecyclePolicy = "oneshot"
	LifecycleDaemon  LifecyclePolicy = "daemon"
	LifecycleWatch   LifecyclePolicy = "watch"
)

type RestartPolicy string

const (
	RestartNever     RestartPolicy = "never"
	RestartOnFailure RestartPolicy = "on-failure"
	RestartAlways    RestartPolicy = "always"
)

// ---------------------------------------------------------------------------
// 合法状态转移表
// ---------------------------------------------------------------------------

var validTransitions = map[ExecutionState]map[ExecutionState]bool{
	StatePending: {
		StateRunning:  true,
		StateCanceled: true,
	},
	StateRunning: {
		StateWaitingTool:  true,
		StateWaitingEvent: true,
		StateCompleted:    true,
		StatePaused:       true,
		StateDraining:     true,
		StateFailed:       true,
		StateCrashed:      true,
		StateInterrupted:  true,
	},
	StateWaitingTool: {
		StateRunning:     true,
		StateFailed:      true,
		StateDraining:    true,
		StateCanceled:    true,
		StateInterrupted: true,
	},
	StateWaitingEvent: {
		StateRunning:     true,
		StateCompleted:   true,
		StateDraining:    true,
		StateInterrupted: true,
	},
	StatePaused: {
		StateRunning:  true,
		StateCanceled: true,
		StateFailed:   true,
	},
	StateDraining: {
		StateCanceled: true,
		StateCrashed:  true,
	},
	StateInterrupted: {
		StateRestarting: true,
		StateFailed:     true,
	},
	StateRestarting: {
		StatePending: true,
		StateFailed:  true,
		StateCrashed: true,
	},
	StateFailed: {
		StateRestarting: true,
	},
	StateCrashed: {
		StateRestarting: true,
	},
}

func canTransition(from, to ExecutionState) bool {
	targets, ok := validTransitions[from]
	if !ok {
		return false
	}
	return targets[to]
}

// ---------------------------------------------------------------------------
// TaskExecution
// ---------------------------------------------------------------------------

var executionIDCounter atomic.Uint64

func nextExecutionID() string {
	n := executionIDCounter.Add(1)
	return fmt.Sprintf("exec-%d", n)
}

type TaskExecution struct {
	ID        string          `json:"id"`
	ParentID  string          `json:"parent_id,omitempty"`
	BrainID   string          `json:"brain_id"`
	Mode      ExecutionMode   `json:"mode"`
	Lifecycle LifecyclePolicy `json:"lifecycle"`
	Restart   RestartPolicy   `json:"restart"`
	State     ExecutionState  `json:"state"`
	CreatedAt time.Time       `json:"created_at"`
	EndedAt   *time.Time      `json:"ended_at,omitempty"`

	Budget loop.Budget `json:"budget"`

	RestartCount    int `json:"restart_count,omitempty"`
	MaxRestartCount int `json:"max_restart_count,omitempty"`

	run    *loop.Run
	cancel context.CancelFunc
	mu     sync.Mutex
}

type TaskExecutionConfig struct {
	BrainID   string
	ParentID  string
	Mode      ExecutionMode
	Lifecycle LifecyclePolicy
	Restart   RestartPolicy
	Budget    loop.Budget
	MaxRestart int
}

func NewTaskExecution(cfg TaskExecutionConfig) *TaskExecution {
	if cfg.Mode == "" {
		cfg.Mode = ModeInteractive
	}
	if cfg.Lifecycle == "" {
		cfg.Lifecycle = LifecycleOneshot
	}
	if cfg.Restart == "" {
		cfg.Restart = RestartNever
	}
	if cfg.MaxRestart == 0 {
		cfg.MaxRestart = 3
	}

	return &TaskExecution{
		ID:              nextExecutionID(),
		ParentID:        cfg.ParentID,
		BrainID:         cfg.BrainID,
		Mode:            cfg.Mode,
		Lifecycle:       cfg.Lifecycle,
		Restart:         cfg.Restart,
		State:           StatePending,
		CreatedAt:       time.Now().UTC(),
		Budget:          cfg.Budget,
		MaxRestartCount: cfg.MaxRestart,
	}
}

func (te *TaskExecution) Transition(to ExecutionState) error {
	te.mu.Lock()
	defer te.mu.Unlock()

	if !canTransition(te.State, to) {
		return fmt.Errorf("invalid state transition: %s → %s", te.State, to)
	}

	te.State = to

	if to.IsTerminal() {
		now := time.Now().UTC()
		te.EndedAt = &now
	}

	return nil
}

func (te *TaskExecution) ShouldRestart() bool {
	te.mu.Lock()
	defer te.mu.Unlock()

	switch te.Restart {
	case RestartNever:
		return false
	case RestartOnFailure:
		return (te.State == StateFailed || te.State == StateCrashed || te.State == StateInterrupted) &&
			te.RestartCount < te.MaxRestartCount
	case RestartAlways:
		return (te.State == StateFailed || te.State == StateCrashed || te.State == StateInterrupted) &&
			te.RestartCount < te.MaxRestartCount
	}
	return false
}

func (te *TaskExecution) IncrementRestart() {
	te.mu.Lock()
	defer te.mu.Unlock()
	te.RestartCount++
}

func (te *TaskExecution) SetRun(r *loop.Run) {
	te.mu.Lock()
	defer te.mu.Unlock()
	te.run = r
}

func (te *TaskExecution) Run() *loop.Run {
	te.mu.Lock()
	defer te.mu.Unlock()
	return te.run
}

func (te *TaskExecution) SetCancel(cancel context.CancelFunc) {
	te.mu.Lock()
	defer te.mu.Unlock()
	te.cancel = cancel
}

func (te *TaskExecution) Cancel() {
	te.mu.Lock()
	cancel := te.cancel
	te.mu.Unlock()

	if cancel != nil {
		cancel()
	}
}

func (te *TaskExecution) Snapshot() TaskExecutionSnapshot {
	te.mu.Lock()
	defer te.mu.Unlock()

	snap := TaskExecutionSnapshot{
		ID:           te.ID,
		ParentID:     te.ParentID,
		BrainID:      te.BrainID,
		Mode:         te.Mode,
		Lifecycle:    te.Lifecycle,
		Restart:      te.Restart,
		State:        te.State,
		CreatedAt:    te.CreatedAt,
		EndedAt:      te.EndedAt,
		RestartCount: te.RestartCount,
	}
	return snap
}

// TaskExecutionSnapshot 是 TaskExecution 的只读快照，用于 API 响应。
type TaskExecutionSnapshot struct {
	ID           string          `json:"id"`
	ParentID     string          `json:"parent_id,omitempty"`
	BrainID      string          `json:"brain_id"`
	Mode         ExecutionMode   `json:"mode"`
	Lifecycle    LifecyclePolicy `json:"lifecycle"`
	Restart      RestartPolicy   `json:"restart"`
	State        ExecutionState  `json:"state"`
	CreatedAt    time.Time       `json:"created_at"`
	EndedAt      *time.Time      `json:"ended_at,omitempty"`
	RestartCount int             `json:"restart_count,omitempty"`
}
