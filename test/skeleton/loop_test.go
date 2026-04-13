package skeleton

import (
	"testing"
	"time"

	"github.com/leef-l/brain/loop"
)

// ---------------------------------------------------------------------------
// Run 状态机 — 合法转换
// ---------------------------------------------------------------------------

func TestRunLegalTransitions(t *testing.T) {
	now := time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		fn   func(r *loop.Run) error
		want loop.State
	}{
		{"pending→running", func(r *loop.Run) error { return r.Start(now) }, loop.StateRunning},
		{"running→completed", func(r *loop.Run) error {
			r.Start(now)
			return r.Complete(now)
		}, loop.StateCompleted},
		{"running→failed", func(r *loop.Run) error {
			r.Start(now)
			return r.Fail(now)
		}, loop.StateFailed},
		{"running→paused", func(r *loop.Run) error {
			r.Start(now)
			return r.Pause()
		}, loop.StatePaused},
		{"paused→running", func(r *loop.Run) error {
			r.Start(now)
			r.Pause()
			return r.Resume()
		}, loop.StateRunning},
		{"running→canceled", func(r *loop.Run) error {
			r.Start(now)
			return r.Cancel(now)
		}, loop.StateCanceled},
		{"pending→canceled", func(r *loop.Run) error {
			return r.Cancel(now)
		}, loop.StateCanceled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := loop.NewRun("r-1", "central", loop.Budget{MaxTurns: 10})
			if err := tt.fn(r); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if r.State != tt.want {
				t.Errorf("State = %q, want %q", r.State, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Run 状态机 — 非法转换
// ---------------------------------------------------------------------------

func TestRunIllegalTransitions(t *testing.T) {
	now := time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		fn   func(r *loop.Run) error
	}{
		{"running→Start", func(r *loop.Run) error {
			r.Start(now)
			return r.Start(now)
		}},
		{"completed→Complete", func(r *loop.Run) error {
			r.Start(now)
			r.Complete(now)
			return r.Complete(now)
		}},
		{"completed→Start", func(r *loop.Run) error {
			r.Start(now)
			r.Complete(now)
			return r.Start(now)
		}},
		{"failed→Fail", func(r *loop.Run) error {
			r.Start(now)
			r.Fail(now)
			return r.Fail(now)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := loop.NewRun("r-1", "central", loop.Budget{MaxTurns: 10})
			if err := tt.fn(r); err == nil {
				t.Error("expected error for illegal transition")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Run 时间戳
// ---------------------------------------------------------------------------

func TestRunTimestamps(t *testing.T) {
	now := time.Date(2026, 4, 12, 10, 30, 0, 0, time.UTC)
	r := loop.NewRun("r-1", "central", loop.Budget{})
	r.Start(now)
	if !r.StartedAt.Equal(now) {
		t.Errorf("StartedAt = %v, want %v", r.StartedAt, now)
	}
	if r.EndedAt != nil {
		t.Error("EndedAt should be nil while running")
	}

	end := now.Add(5 * time.Minute)
	r.Complete(end)
	if r.EndedAt == nil {
		t.Fatal("EndedAt should be set after complete")
	}
	if !r.EndedAt.Equal(end) {
		t.Errorf("EndedAt = %v, want %v", *r.EndedAt, end)
	}
}

// ---------------------------------------------------------------------------
// NewRun 默认值
// ---------------------------------------------------------------------------

func TestNewRun(t *testing.T) {
	budget := loop.Budget{MaxTurns: 20, MaxCostUSD: 1.5}
	r := loop.NewRun("run-abc", "code", budget)
	if r.ID != "run-abc" {
		t.Errorf("ID = %q", r.ID)
	}
	if r.BrainID != "code" {
		t.Errorf("BrainID = %q", r.BrainID)
	}
	if r.State != loop.StatePending {
		t.Errorf("State = %q, want pending", r.State)
	}
	if r.CurrentTurn != 0 {
		t.Errorf("CurrentTurn = %d, want 0", r.CurrentTurn)
	}
}

// ---------------------------------------------------------------------------
// Budget — CheckTurn 耗尽优先级
// ---------------------------------------------------------------------------

func TestBudgetCheckTurnExhaustion(t *testing.T) {
	tests := []struct {
		name     string
		budget   loop.Budget
		wantCode string
	}{
		{
			name:     "turns exhausted",
			budget:   loop.Budget{MaxTurns: 5, UsedTurns: 5},
			wantCode: "budget.turns_exhausted",
		},
		{
			name:     "cost exhausted",
			budget:   loop.Budget{MaxCostUSD: 1.0, UsedCostUSD: 1.0},
			wantCode: "budget.cost_exhausted",
		},
		{
			name:     "llm calls exhausted",
			budget:   loop.Budget{MaxLLMCalls: 10, UsedLLMCalls: 10},
			wantCode: "budget.llm_calls_exhausted",
		},
		{
			name:     "tool calls exhausted",
			budget:   loop.Budget{MaxToolCalls: 5, UsedToolCalls: 5},
			wantCode: "budget.tool_calls_exhausted",
		},
		{
			name:     "timeout exhausted",
			budget:   loop.Budget{MaxDuration: time.Minute, ElapsedTime: time.Minute},
			wantCode: "budget.timeout_exhausted",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.budget.CheckTurn()
			if err == nil {
				t.Fatal("expected budget error")
			}
			if !containsString(err.Error(), tt.wantCode) {
				t.Errorf("error %q does not contain %q", err.Error(), tt.wantCode)
			}
		})
	}
}

func TestBudgetCheckTurnOK(t *testing.T) {
	b := loop.Budget{MaxTurns: 10, UsedTurns: 5}
	if err := b.CheckTurn(); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestBudgetNilCheckTurn(t *testing.T) {
	var b *loop.Budget
	if err := b.CheckTurn(); err != nil {
		t.Errorf("nil budget CheckTurn should return nil: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Budget — Remaining
// ---------------------------------------------------------------------------

func TestBudgetRemaining(t *testing.T) {
	b := loop.Budget{
		MaxTurns:    10,
		UsedTurns:   3,
		MaxCostUSD:  5.0,
		UsedCostUSD: 1.5,
		MaxLLMCalls: 20,
		UsedLLMCalls: 8,
	}
	snap := b.Remaining()
	if snap.TurnsRemaining != 7 {
		t.Errorf("TurnsRemaining = %d, want 7", snap.TurnsRemaining)
	}
	if snap.CostUSDRemaining != 3.5 {
		t.Errorf("CostUSDRemaining = %f, want 3.5", snap.CostUSDRemaining)
	}
	if snap.TokensRemaining != 12 {
		t.Errorf("TokensRemaining = %d, want 12", snap.TokensRemaining)
	}
}

func TestBudgetRemainingNoNegative(t *testing.T) {
	b := loop.Budget{MaxTurns: 5, UsedTurns: 10}
	snap := b.Remaining()
	if snap.TurnsRemaining < 0 {
		t.Errorf("TurnsRemaining = %d, should not be negative", snap.TurnsRemaining)
	}
}

func TestBudgetNilRemaining(t *testing.T) {
	var b *loop.Budget
	snap := b.Remaining()
	if snap.TurnsRemaining != 0 {
		t.Errorf("nil budget TurnsRemaining = %d, want 0", snap.TurnsRemaining)
	}
}

// ---------------------------------------------------------------------------
// Budget — CheckCost
// ---------------------------------------------------------------------------

func TestBudgetCheckCost(t *testing.T) {
	b := loop.Budget{MaxCostUSD: 1.0, UsedCostUSD: 1.0}
	if err := b.CheckCost(); err == nil {
		t.Error("expected cost exhausted error")
	}
	b2 := loop.Budget{MaxCostUSD: 1.0, UsedCostUSD: 0.5}
	if err := b2.CheckCost(); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 辅助
// ---------------------------------------------------------------------------

func containsString(s, sub string) bool {
	return len(s) >= len(sub) && searchString(s, sub)
}

func searchString(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
