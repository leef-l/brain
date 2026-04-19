package kernel

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/leef-l/brain/sdk/events"
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

	// Task #19: 每次 Transition 若 bus != nil 就发一条 task.state.<to>
	// 事件,外部 hook(daemon/录制/外部进程)通过 Subscribe 即可接入。
	bus events.Publisher
}

type TaskExecutionConfig struct {
	BrainID    string
	ParentID   string
	Mode       ExecutionMode
	Lifecycle  LifecyclePolicy
	Restart    RestartPolicy
	Budget     loop.Budget
	MaxRestart int

	// Bus 可选,设置后每次 Transition 自动发事件;零值时 hook 机制 no-op。
	Bus events.Publisher
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
		bus:             cfg.Bus,
	}
}

// SetEventBus 允许构造后补注入 EventBus。主要给"先建 TaskExecution 再加入
// 注册表"的场景。已设置时以新值覆盖。
func (te *TaskExecution) SetEventBus(bus events.Publisher) {
	te.mu.Lock()
	defer te.mu.Unlock()
	te.bus = bus
}

func (te *TaskExecution) Transition(to ExecutionState) error {
	te.mu.Lock()

	if !canTransition(te.State, to) {
		te.mu.Unlock()
		return fmt.Errorf("invalid state transition: %s → %s", te.State, to)
	}

	from := te.State
	te.State = to

	if to.IsTerminal() {
		now := time.Now().UTC()
		te.EndedAt = &now
	}

	bus := te.bus
	execID := te.ID
	brainID := te.BrainID
	te.mu.Unlock()

	if bus != nil {
		payload, _ := json.Marshal(map[string]interface{}{
			"execution_id": execID,
			"brain_id":     brainID,
			"from":         string(from),
			"to":           string(to),
			"terminal":     to.IsTerminal(),
		})
		bus.Publish(context.Background(), events.Event{
			ExecutionID: execID,
			Type:        "task.state." + string(to),
			Timestamp:   time.Now().UTC(),
			Data:        payload,
		})
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
