package loop

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	brainerrors "github.com/leef-l/brain/errors"
	"github.com/leef-l/brain/llm"
)

// ── helpers ──────────────────────────────────────────────────────────────────

func mustBrainError(t *testing.T, err error) *brainerrors.BrainError {
	t.Helper()
	if err == nil {
		t.Fatal("expected *BrainError, got nil")
	}
	be, ok := err.(*brainerrors.BrainError)
	if !ok {
		t.Fatalf("expected *BrainError, got %T: %v", err, err)
	}
	return be
}

func assertNoErr(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ── Run state machine ─────────────────────────────────────────────────────────

func TestRunStateMachine_LegalTransitions(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name string
		fn   func(r *Run) error
		want State
	}{
		{
			name: "pending → running via Start",
			fn:   func(r *Run) error { return r.Start(now) },
			want: StateRunning,
		},
		{
			name: "running → completed via Complete",
			fn: func(r *Run) error {
				r2 := NewRun("x", "b", Budget{})
				_ = r2.Start(now)
				*r = *r2
				return r.Complete(now)
			},
			want: StateCompleted,
		},
		{
			name: "waiting_tool → completed via Complete",
			fn: func(r *Run) error {
				r.State = StateWaitingTool
				return r.Complete(now)
			},
			want: StateCompleted,
		},
		{
			name: "running → failed via Fail",
			fn: func(r *Run) error {
				r.State = StateRunning
				return r.Fail(now)
			},
			want: StateFailed,
		},
		{
			name: "waiting_tool → failed via Fail",
			fn: func(r *Run) error {
				r.State = StateWaitingTool
				return r.Fail(now)
			},
			want: StateFailed,
		},
		{
			name: "paused → failed via Fail",
			fn: func(r *Run) error {
				r.State = StatePaused
				return r.Fail(now)
			},
			want: StateFailed,
		},
		{
			name: "running → paused via Pause",
			fn: func(r *Run) error {
				r.State = StateRunning
				return r.Pause()
			},
			want: StatePaused,
		},
		{
			name: "waiting_tool → paused via Pause",
			fn: func(r *Run) error {
				r.State = StateWaitingTool
				return r.Pause()
			},
			want: StatePaused,
		},
		{
			name: "paused → running via Resume",
			fn: func(r *Run) error {
				r.State = StatePaused
				return r.Resume()
			},
			want: StateRunning,
		},
		{
			name: "pending → canceled via Cancel",
			fn: func(r *Run) error {
				r.State = StatePending
				return r.Cancel(now)
			},
			want: StateCanceled,
		},
		{
			name: "running → canceled via Cancel",
			fn: func(r *Run) error {
				r.State = StateRunning
				return r.Cancel(now)
			},
			want: StateCanceled,
		},
		{
			name: "paused → canceled via Cancel",
			fn: func(r *Run) error {
				r.State = StatePaused
				return r.Cancel(now)
			},
			want: StateCanceled,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := NewRun("id", "brain", Budget{})
			err := tc.fn(r)
			assertNoErr(t, err)
			if r.State != tc.want {
				t.Fatalf("state = %q, want %q", r.State, tc.want)
			}
		})
	}
}

func TestRunStateMachine_IllegalTransitions(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name     string
		setup    func(r *Run)
		fn       func(r *Run) error
		wantCode string
	}{
		{
			name:     "running → Start (already running)",
			setup:    func(r *Run) { r.State = StateRunning },
			fn:       func(r *Run) error { return r.Start(now) },
			wantCode: brainerrors.CodeInvariantViolated,
		},
		{
			name:     "completed → Start",
			setup:    func(r *Run) { r.State = StateCompleted },
			fn:       func(r *Run) error { return r.Start(now) },
			wantCode: brainerrors.CodeInvariantViolated,
		},
		{
			name:     "pending → Complete",
			setup:    func(r *Run) { r.State = StatePending },
			fn:       func(r *Run) error { return r.Complete(now) },
			wantCode: brainerrors.CodeInvariantViolated,
		},
		{
			name:     "completed → Complete",
			setup:    func(r *Run) { r.State = StateCompleted },
			fn:       func(r *Run) error { return r.Complete(now) },
			wantCode: brainerrors.CodeInvariantViolated,
		},
		{
			name:     "pending → Fail",
			setup:    func(r *Run) { r.State = StatePending },
			fn:       func(r *Run) error { return r.Fail(now) },
			wantCode: brainerrors.CodeInvariantViolated,
		},
		{
			name:     "completed → Fail",
			setup:    func(r *Run) { r.State = StateCompleted },
			fn:       func(r *Run) error { return r.Fail(now) },
			wantCode: brainerrors.CodeInvariantViolated,
		},
		{
			name:     "pending → Pause",
			setup:    func(r *Run) { r.State = StatePending },
			fn:       func(r *Run) error { return r.Pause() },
			wantCode: brainerrors.CodeInvariantViolated,
		},
		{
			name:     "completed → Pause",
			setup:    func(r *Run) { r.State = StateCompleted },
			fn:       func(r *Run) error { return r.Pause() },
			wantCode: brainerrors.CodeInvariantViolated,
		},
		{
			name:     "running → Resume (not paused)",
			setup:    func(r *Run) { r.State = StateRunning },
			fn:       func(r *Run) error { return r.Resume() },
			wantCode: brainerrors.CodeInvariantViolated,
		},
		{
			name:     "completed → Cancel (terminal)",
			setup:    func(r *Run) { r.State = StateCompleted },
			fn:       func(r *Run) error { return r.Cancel(now) },
			wantCode: brainerrors.CodeInvariantViolated,
		},
		{
			name:     "failed → Cancel (terminal)",
			setup:    func(r *Run) { r.State = StateFailed },
			fn:       func(r *Run) error { return r.Cancel(now) },
			wantCode: brainerrors.CodeInvariantViolated,
		},
		{
			name:     "crashed → Cancel (terminal)",
			setup:    func(r *Run) { r.State = StateCrashed },
			fn:       func(r *Run) error { return r.Cancel(now) },
			wantCode: brainerrors.CodeInvariantViolated,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := NewRun("id", "brain", Budget{})
			tc.setup(r)
			err := tc.fn(r)
			be := mustBrainError(t, err)
			if be.ErrorCode != tc.wantCode {
				t.Fatalf("ErrorCode = %q, want %q", be.ErrorCode, tc.wantCode)
			}
		})
	}
}

func TestRunStart_SetsStartedAt(t *testing.T) {
	r := NewRun("r1", "b", Budget{})
	ts := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	assertNoErr(t, r.Start(ts))
	if !r.StartedAt.Equal(ts) {
		t.Fatalf("StartedAt = %v, want %v", r.StartedAt, ts)
	}
	if r.EndedAt != nil {
		t.Fatal("EndedAt should be nil after Start")
	}
}

func TestRunComplete_SetsEndedAt(t *testing.T) {
	r := NewRun("r1", "b", Budget{})
	ts := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)
	_ = r.Start(ts)
	end := ts.Add(5 * time.Second)
	assertNoErr(t, r.Complete(end))
	if r.EndedAt == nil || !r.EndedAt.Equal(end) {
		t.Fatalf("EndedAt = %v, want %v", r.EndedAt, end)
	}
}

// ── Budget.CheckTurn dimension order ─────────────────────────────────────────

func TestBudget_CheckTurn_ExhaustionOrder(t *testing.T) {
	// All five dimensions are simultaneously at or over their limit.
	// CheckTurn must return the turns exhausted code first.
	b := &Budget{
		MaxTurns:     1,
		UsedTurns:    1,
		MaxCostUSD:   1.0,
		UsedCostUSD:  1.0,
		MaxLLMCalls:  1,
		UsedLLMCalls: 1,
		MaxToolCalls: 1,
		UsedToolCalls: 1,
		MaxDuration:  time.Second,
		ElapsedTime:  2 * time.Second,
	}
	err := b.CheckTurn()
	be := mustBrainError(t, err)
	if be.ErrorCode != brainerrors.CodeBudgetTurnsExhausted {
		t.Fatalf("want turns_exhausted first, got %q", be.ErrorCode)
	}

	// Remove turns exhaustion → cost fires next.
	b.MaxTurns = 0
	err = b.CheckTurn()
	be = mustBrainError(t, err)
	if be.ErrorCode != brainerrors.CodeBudgetCostExhausted {
		t.Fatalf("want cost_exhausted second, got %q", be.ErrorCode)
	}

	// Remove cost exhaustion → LLM calls fires next.
	b.MaxCostUSD = 0
	err = b.CheckTurn()
	be = mustBrainError(t, err)
	if be.ErrorCode != brainerrors.CodeBudgetLLMCallsExhausted {
		t.Fatalf("want llm_calls_exhausted third, got %q", be.ErrorCode)
	}

	// Remove LLM calls → tool calls fires next.
	b.MaxLLMCalls = 0
	err = b.CheckTurn()
	be = mustBrainError(t, err)
	if be.ErrorCode != brainerrors.CodeBudgetToolCallsExhausted {
		t.Fatalf("want tool_calls_exhausted fourth, got %q", be.ErrorCode)
	}

	// Remove tool calls → timeout fires next.
	b.MaxToolCalls = 0
	err = b.CheckTurn()
	be = mustBrainError(t, err)
	if be.ErrorCode != brainerrors.CodeBudgetTimeoutExhausted {
		t.Fatalf("want timeout_exhausted fifth, got %q", be.ErrorCode)
	}

	// Remove timeout → no error.
	b.MaxDuration = 0
	assertNoErr(t, b.CheckTurn())
}

// ── Budget.Remaining boundaries ──────────────────────────────────────────────

func TestBudget_Remaining_NoNegative(t *testing.T) {
	b := &Budget{
		MaxTurns:     2,
		UsedTurns:    5, // over budget
		MaxCostUSD:   1.0,
		UsedCostUSD:  3.0, // over budget
		MaxLLMCalls:  2,
		UsedLLMCalls: 10, // over budget
	}
	snap := b.Remaining()
	if snap.TurnsRemaining < 0 {
		t.Fatalf("TurnsRemaining = %d, must not be negative", snap.TurnsRemaining)
	}
	if snap.CostUSDRemaining < 0 {
		t.Fatalf("CostUSDRemaining = %f, must not be negative", snap.CostUSDRemaining)
	}
	if snap.TokensRemaining < 0 {
		t.Fatalf("TokensRemaining = %d, must not be negative", snap.TokensRemaining)
	}
}

func TestBudget_Remaining_Exact(t *testing.T) {
	b := &Budget{
		MaxTurns:     10,
		UsedTurns:    3,
		MaxCostUSD:   5.0,
		UsedCostUSD:  1.5,
		MaxLLMCalls:  8,
		UsedLLMCalls: 2,
	}
	snap := b.Remaining()
	if snap.TurnsRemaining != 7 {
		t.Fatalf("TurnsRemaining = %d, want 7", snap.TurnsRemaining)
	}
	if snap.CostUSDRemaining != 3.5 {
		t.Fatalf("CostUSDRemaining = %.2f, want 3.5", snap.CostUSDRemaining)
	}
	if snap.TokensRemaining != 6 {
		t.Fatalf("TokensRemaining = %d, want 6", snap.TokensRemaining)
	}
}

func TestBudget_Remaining_NilReceiver(t *testing.T) {
	var b *Budget
	snap := b.Remaining()
	if snap.TurnsRemaining != 0 || snap.CostUSDRemaining != 0 || snap.TokensRemaining != 0 {
		t.Fatal("nil Budget.Remaining should return zero snapshot")
	}
}

// ── MemCacheBuilder ───────────────────────────────────────────────────────────

func TestMemCacheBuilder_BuildL1System(t *testing.T) {
	cb := NewMemCacheBuilder()

	t.Run("only cached blocks produce points", func(t *testing.T) {
		sys := []llm.SystemBlock{
			{Text: "a", Cache: false},
			{Text: "b", Cache: true},
			{Text: "c", Cache: false},
			{Text: "d", Cache: true},
		}
		pts := cb.BuildL1System(sys)
		if len(pts) != 2 {
			t.Fatalf("want 2 CachePoints, got %d", len(pts))
		}
		if pts[0].Layer != "L1_system" || pts[0].Index != 1 {
			t.Fatalf("pts[0] = %+v, want L1_system/1", pts[0])
		}
		if pts[1].Layer != "L1_system" || pts[1].Index != 3 {
			t.Fatalf("pts[1] = %+v, want L1_system/3", pts[1])
		}
	})

	t.Run("empty input returns nil", func(t *testing.T) {
		if pts := cb.BuildL1System(nil); pts != nil {
			t.Fatalf("want nil, got %v", pts)
		}
	})

	t.Run("no cached blocks returns nil", func(t *testing.T) {
		sys := []llm.SystemBlock{{Text: "x", Cache: false}}
		if pts := cb.BuildL1System(sys); pts != nil {
			t.Fatalf("want nil, got %v", pts)
		}
	})

	t.Run("pure function — same input same output", func(t *testing.T) {
		sys := []llm.SystemBlock{{Text: "s", Cache: true}}
		p1 := cb.BuildL1System(sys)
		p2 := cb.BuildL1System(sys)
		if len(p1) != len(p2) || p1[0] != p2[0] {
			t.Fatal("BuildL1System is not pure")
		}
	})
}

func TestMemCacheBuilder_BuildL2Task(t *testing.T) {
	cb := NewMemCacheBuilder()
	msgs := []llm.Message{
		{Role: "user"},
		{Role: "assistant"},
		{Role: "user"},
	}

	t.Run("in-range boundary", func(t *testing.T) {
		pts := cb.BuildL2Task(msgs, 1)
		if len(pts) != 1 || pts[0].Layer != "L2_task" || pts[0].Index != 1 {
			t.Fatalf("want L2_task/1, got %+v", pts)
		}
	})

	t.Run("boundary == last index", func(t *testing.T) {
		pts := cb.BuildL2Task(msgs, 2)
		if len(pts) != 1 || pts[0].Index != 2 {
			t.Fatalf("want index 2, got %+v", pts)
		}
	})

	t.Run("out of range returns nil", func(t *testing.T) {
		if pts := cb.BuildL2Task(msgs, 3); pts != nil {
			t.Fatalf("want nil for out-of-range, got %v", pts)
		}
		if pts := cb.BuildL2Task(msgs, -1); pts != nil {
			t.Fatalf("want nil for negative, got %v", pts)
		}
	})

	t.Run("empty messages returns nil", func(t *testing.T) {
		if pts := cb.BuildL2Task(nil, 0); pts != nil {
			t.Fatalf("want nil for empty msgs, got %v", pts)
		}
	})
}

func TestMemCacheBuilder_BuildL3History(t *testing.T) {
	cb := NewMemCacheBuilder()

	t.Run("last user message", func(t *testing.T) {
		msgs := []llm.Message{
			{Role: "user"},
			{Role: "assistant"},
			{Role: "user"},
			{Role: "assistant"},
		}
		pts := cb.BuildL3History(msgs)
		if len(pts) != 1 || pts[0].Layer != "L3_history" || pts[0].Index != 2 {
			t.Fatalf("want L3_history/2, got %+v", pts)
		}
	})

	t.Run("last tool message wins over earlier user", func(t *testing.T) {
		msgs := []llm.Message{
			{Role: "user"},
			{Role: "tool"},
			{Role: "assistant"},
		}
		pts := cb.BuildL3History(msgs)
		if len(pts) != 1 || pts[0].Index != 1 {
			t.Fatalf("want index 1 (tool), got %+v", pts)
		}
	})

	t.Run("no user or tool returns nil", func(t *testing.T) {
		msgs := []llm.Message{
			{Role: "assistant"},
			{Role: "assistant"},
		}
		if pts := cb.BuildL3History(msgs); pts != nil {
			t.Fatalf("want nil, got %v", pts)
		}
	})

	t.Run("empty input returns nil", func(t *testing.T) {
		if pts := cb.BuildL3History(nil); pts != nil {
			t.Fatalf("want nil, got %v", pts)
		}
	})
}

// ── MemStreamConsumer ─────────────────────────────────────────────────────────

func TestMemStreamConsumer_SingleTurn(t *testing.T) {
	ctx := context.Background()
	sc := NewMemStreamConsumer()
	run := &Run{ID: "r1"}
	turn := &Turn{Index: 1, RunID: "r1"}

	sc.OnMessageStart(ctx, run, turn)
	sc.OnContentDelta(ctx, run, turn, "hello")
	sc.OnContentDelta(ctx, run, turn, " world")
	sc.OnToolCallDelta(ctx, run, turn, "my_tool", `{"k":`)
	sc.OnToolCallDelta(ctx, run, turn, "", `"v"}`)
	sc.OnMessageDelta(ctx, run, turn, json.RawMessage(`{"stop_reason":"end_turn"}`))
	sc.OnMessageEnd(ctx, run, turn, llm.Usage{InputTokens: 10, OutputTokens: 20, CostUSD: 0.001})

	snap, ok := sc.Snapshot("r1", 1)
	if !ok {
		t.Fatal("Snapshot not found")
	}
	if snap.Content.String() != "hello world" {
		t.Fatalf("Content = %q, want %q", snap.Content.String(), "hello world")
	}
	if !snap.Finished {
		t.Fatal("Finished should be true after OnMessageEnd")
	}
	if snap.FinalUsage.InputTokens != 10 || snap.FinalUsage.OutputTokens != 20 {
		t.Fatalf("FinalUsage = %+v, want {10,20}", snap.FinalUsage)
	}
	if len(snap.ToolCalls) != 1 {
		t.Fatalf("want 1 tool call, got %d", len(snap.ToolCalls))
	}
	if snap.ToolCalls[0].ToolName != "my_tool" {
		t.Fatalf("ToolName = %q, want %q", snap.ToolCalls[0].ToolName, "my_tool")
	}
	wantArgs := `{"k":"v"}`
	if snap.ToolCalls[0].ArgsPartial.String() != wantArgs {
		t.Fatalf("ArgsPartial = %q, want %q", snap.ToolCalls[0].ArgsPartial.String(), wantArgs)
	}
	if len(snap.MessageDeltas) != 1 {
		t.Fatalf("want 1 MessageDelta, got %d", len(snap.MessageDeltas))
	}
	// Event counts
	if snap.EventCounts["message_start"] != 1 {
		t.Fatalf("message_start count = %d, want 1", snap.EventCounts["message_start"])
	}
	if snap.EventCounts["content_delta"] != 2 {
		t.Fatalf("content_delta count = %d, want 2", snap.EventCounts["content_delta"])
	}
	if snap.EventCounts["tool_call_delta"] != 2 {
		t.Fatalf("tool_call_delta count = %d, want 2", snap.EventCounts["tool_call_delta"])
	}
	if snap.EventCounts["message_delta"] != 1 {
		t.Fatalf("message_delta count = %d, want 1", snap.EventCounts["message_delta"])
	}
	if snap.EventCounts["message_end"] != 1 {
		t.Fatalf("message_end count = %d, want 1", snap.EventCounts["message_end"])
	}
}

func TestMemStreamConsumer_MissingSnapshot(t *testing.T) {
	sc := NewMemStreamConsumer()
	_, ok := sc.Snapshot("nonexistent", 99)
	if ok {
		t.Fatal("Snapshot should return false for unknown key")
	}
}

func TestMemStreamConsumer_ConcurrentWrites(t *testing.T) {
	ctx := context.Background()
	sc := NewMemStreamConsumer()
	const goroutines = 20
	const deltasPerGoroutine = 50

	var wg sync.WaitGroup
	run := &Run{ID: "concurrent_run"}
	turn := &Turn{Index: 1, RunID: "concurrent_run"}

	// OnMessageStart once
	sc.OnMessageStart(ctx, run, turn)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < deltasPerGoroutine; j++ {
				sc.OnContentDelta(ctx, run, turn, "x")
			}
		}()
	}
	wg.Wait()

	snap, ok := sc.Snapshot("concurrent_run", 1)
	if !ok {
		t.Fatal("Snapshot not found after concurrent writes")
	}
	wantCount := goroutines * deltasPerGoroutine
	if snap.EventCounts["content_delta"] != wantCount {
		t.Fatalf("content_delta count = %d, want %d", snap.EventCounts["content_delta"], wantCount)
	}
	// Content should be exactly wantCount "x" characters
	if len(snap.Content.String()) != wantCount {
		t.Fatalf("Content len = %d, want %d", len(snap.Content.String()), wantCount)
	}
}

// ── NewTurn UUID format ───────────────────────────────────────────────────────

func TestNewTurn_UUID(t *testing.T) {
	now := time.Now()
	turn := NewTurn("run1", 1, now)

	if turn.RunID != "run1" {
		t.Fatalf("RunID = %q, want %q", turn.RunID, "run1")
	}
	if turn.Index != 1 {
		t.Fatalf("Index = %d, want 1", turn.Index)
	}
	if !turn.StartedAt.Equal(now) {
		t.Fatalf("StartedAt = %v, want %v", turn.StartedAt, now)
	}
	if turn.EndedAt != nil {
		t.Fatal("EndedAt should be nil for a new Turn")
	}

	// UUID must be exactly 32 hex chars (16 bytes → 32 nibbles).
	uuid := turn.UUID
	if len(uuid) != 32 {
		t.Fatalf("UUID len = %d, want 32; UUID = %q", len(uuid), uuid)
	}
	lower := strings.ToLower(uuid)
	for _, ch := range lower {
		if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f')) {
			t.Fatalf("UUID contains non-hex char %q; UUID = %q", ch, uuid)
		}
	}

	// Two distinct calls should yield distinct UUIDs.
	turn2 := NewTurn("run1", 2, now)
	if turn.UUID == turn2.UUID {
		t.Fatal("two NewTurn calls produced the same UUID")
	}
}

func TestTurn_End(t *testing.T) {
	now := time.Now()
	turn := NewTurn("r", 1, now)
	end := now.Add(time.Second)
	turn.End(end)
	if turn.EndedAt == nil || !turn.EndedAt.Equal(end) {
		t.Fatalf("EndedAt = %v, want %v", turn.EndedAt, end)
	}
	// Second End call updates timestamp.
	end2 := end.Add(time.Second)
	turn.End(end2)
	if !turn.EndedAt.Equal(end2) {
		t.Fatalf("second End did not update EndedAt; got %v, want %v", turn.EndedAt, end2)
	}
}
