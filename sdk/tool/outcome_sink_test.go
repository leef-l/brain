package tool

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
)

// M6 学习闭环回调测试:anomalyInjectingTool 每次 Execute 完应当把
// (toolName, taskType, success) 通过 OutcomeSink 回写给 AdaptivePolicy。
// taskType 从 ctx 绑定的 recorder 派生(优先 brainKind)。

type fakeOutcomeSink struct {
	mu      sync.Mutex
	records []outcomeRecord
}

type outcomeRecord struct {
	tool     string
	taskType string
	success  bool
}

func (f *fakeOutcomeSink) RecordOutcome(toolName, taskType string, success bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.records = append(f.records, outcomeRecord{toolName, taskType, success})
}

func (f *fakeOutcomeSink) snapshot() []outcomeRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]outcomeRecord, len(f.records))
	copy(out, f.records)
	return out
}

// fakeInnerTool returns a configurable Result without touching CDP.
type fakeInnerTool struct {
	name    string
	isError bool
	out     json.RawMessage
}

func (f *fakeInnerTool) Name() string   { return f.name }
func (f *fakeInnerTool) Risk() Risk     { return RiskSafe }
func (f *fakeInnerTool) Schema() Schema { return Schema{Name: f.name} }
func (f *fakeInnerTool) Execute(ctx context.Context, args json.RawMessage) (*Result, error) {
	out := f.out
	if out == nil {
		out = json.RawMessage(`{}`)
	}
	return &Result{Output: out, IsError: f.isError}, nil
}

func TestAnomalyInjectingToolReportsOutcome_Success(t *testing.T) {
	sink := &fakeOutcomeSink{}
	SetOutcomeSink(sink)
	t.Cleanup(func() { SetOutcomeSink(nil) })

	ctx := context.Background()
	BindRecorder(ctx, "run-1", "browser", "log into Gitea")
	t.Cleanup(func() { _ = FinishRecorder(ctx, "success") })

	inner := &fakeInnerTool{name: "browser.click"}
	tool := &anomalyInjectingTool{inner: inner, holder: newBrowserSessionHolder()}

	if _, err := tool.Execute(ctx, json.RawMessage(`{"id":42}`)); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	recs := sink.snapshot()
	if len(recs) != 1 {
		t.Fatalf("expected 1 outcome, got %d", len(recs))
	}
	if recs[0].tool != "browser.click" {
		t.Errorf("tool = %q, want browser.click", recs[0].tool)
	}
	if recs[0].taskType != "browser" {
		t.Errorf("taskType = %q, want browser (brainKind)", recs[0].taskType)
	}
	if !recs[0].success {
		t.Errorf("success = false, want true (IsError=false)")
	}
}

func TestAnomalyInjectingToolReportsOutcome_Failure(t *testing.T) {
	sink := &fakeOutcomeSink{}
	SetOutcomeSink(sink)
	t.Cleanup(func() { SetOutcomeSink(nil) })

	ctx := context.Background()
	BindRecorder(ctx, "run-2", "browser", "goal")
	t.Cleanup(func() { _ = FinishRecorder(ctx, "failure") })

	inner := &fakeInnerTool{name: "browser.type", isError: true}
	tool := &anomalyInjectingTool{inner: inner, holder: newBrowserSessionHolder()}

	if _, err := tool.Execute(ctx, json.RawMessage(`{"id":1,"text":"x"}`)); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	recs := sink.snapshot()
	if len(recs) != 1 {
		t.Fatalf("expected 1 outcome, got %d", len(recs))
	}
	if recs[0].success {
		t.Errorf("success = true, want false (IsError=true)")
	}
}

func TestAnomalyInjectingToolWithoutSinkIsNoop(t *testing.T) {
	SetOutcomeSink(nil) // 确保干净

	ctx := context.Background()
	BindRecorder(ctx, "run-3", "browser", "")
	t.Cleanup(func() { _ = FinishRecorder(ctx, "success") })

	inner := &fakeInnerTool{name: "browser.navigate"}
	tool := &anomalyInjectingTool{inner: inner, holder: newBrowserSessionHolder()}

	if _, err := tool.Execute(ctx, json.RawMessage(`{"url":"x"}`)); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// 无 sink 注入应不爆,也不影响 inner 返回。
}

func TestAnomalyInjectingToolOutcomeWithoutRecorder(t *testing.T) {
	// 没 BindRecorder 时,taskType 应为空字符串,sink 自己归到 "_default"。
	sink := &fakeOutcomeSink{}
	SetOutcomeSink(sink)
	t.Cleanup(func() { SetOutcomeSink(nil) })

	ctx := context.Background()
	inner := &fakeInnerTool{name: "browser.click"}
	tool := &anomalyInjectingTool{inner: inner, holder: newBrowserSessionHolder()}

	if _, err := tool.Execute(ctx, json.RawMessage(`{"id":1}`)); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	recs := sink.snapshot()
	if len(recs) != 1 {
		t.Fatalf("expected 1 outcome, got %d", len(recs))
	}
	if recs[0].taskType != "" {
		t.Errorf("taskType = %q, want empty (no recorder bound)", recs[0].taskType)
	}
}

func TestDeriveTaskTypeFromCtxFallbackToGoal(t *testing.T) {
	ctx := context.Background()
	BindRecorder(ctx, "run-4", "", "login flow step")
	t.Cleanup(func() { _ = FinishRecorder(ctx, "success") })

	got := deriveTaskTypeFromCtx(ctx)
	if got != "login" {
		t.Errorf("deriveTaskTypeFromCtx = %q, want %q (first token of goal)", got, "login")
	}
}
