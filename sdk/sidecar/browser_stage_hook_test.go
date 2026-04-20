package sidecar

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/leef-l/brain/sdk/loop"
	"github.com/leef-l/brain/sdk/tool"
)

// stubTool is a minimal tool.Tool for hook filtering tests — name + schema
// are enough; Execute is never called by the hook.
type stubTool struct{ name string }

func (s stubTool) Name() string { return s.name }
func (s stubTool) Schema() tool.Schema {
	return tool.Schema{Name: s.name, Description: "stub", InputSchema: json.RawMessage(`{"type":"object"}`)}
}
func (s stubTool) Risk() tool.Risk { return tool.RiskSafe }
func (s stubTool) Execute(_ context.Context, _ json.RawMessage) (*tool.Result, error) {
	return &tool.Result{Output: json.RawMessage(`"ok"`)}, nil
}

func buildStubBrowserRegistry() tool.Registry {
	reg := tool.NewMemRegistry()
	for _, n := range []string{
		"browser.snapshot", "browser.understand", "browser.sitemap",
		"browser.pattern_match", "browser.pattern_exec", "browser.pattern_list",
		"browser.click", "browser.type", "browser.drag",
		"browser.screenshot", "browser.visual_inspect", "browser.eval",
		"human.request_takeover",
	} {
		_ = reg.Register(stubTool{name: n})
	}
	return reg
}

func hookState(t *testing.T, ctx context.Context, reg tool.Registry) *loop.PreTurnState {
	t.Helper()
	hook := newBrowserStageHook(reg)
	state, err := hook(ctx, &loop.Run{}, 1)
	if err != nil {
		t.Fatalf("hook err: %v", err)
	}
	if state == nil {
		t.Fatal("hook returned nil state")
	}
	return state
}

func hookNameSet(t *testing.T, ctx context.Context, reg tool.Registry) map[string]bool {
	t.Helper()
	state := hookState(t, ctx, reg)
	names := map[string]bool{}
	for _, tl := range state.Tools {
		names[tl.Name] = true
	}
	return names
}

func TestBrowserStageHookFirstTurnNewPage(t *testing.T) {
	// No recorder bound → no signals → new_page stage, but tools remain full-set.
	reg := buildStubBrowserRegistry()
	state := hookState(t, context.Background(), reg)
	names := map[string]bool{}
	for _, tl := range state.Tools {
		names[tl.Name] = true
	}
	for _, must := range []string{
		"browser.snapshot", "browser.drag", "browser.visual_inspect",
		"browser.eval", "human.request_takeover",
	} {
		if !names[must] {
			t.Errorf("full toolset should include %s; got %v", must, names)
		}
	}
	if len(names) != len(reg.List()) {
		t.Fatalf("tool count = %d, want %d", len(names), len(reg.List()))
	}
	if state.ToolChoice != "browser.open" {
		t.Fatalf("first browser turn tool_choice=%q, want browser.open", state.ToolChoice)
	}
}

func TestBrowserStageHookHighScoreKnownFlow(t *testing.T) {
	reg := buildStubBrowserRegistry()
	ctx := context.Background()
	tool.BindRecorder(ctx, "r1", "browser", "demo")
	defer func() { _ = tool.FinishRecorder(ctx, "success") }()
	tool.RecordPatternMatchScore(ctx, 0.9)

	names := hookNameSet(t, ctx, reg)
	for _, must := range []string{
		"browser.pattern_match", "browser.pattern_exec", "browser.drag",
		"human.request_takeover", "browser.sitemap",
	} {
		if !names[must] {
			t.Errorf("known_flow should still expose %s; got %v", must, names)
		}
	}
}

func TestBrowserStageHookConsecutiveErrorsFallback(t *testing.T) {
	reg := buildStubBrowserRegistry()
	ctx := context.Background()
	tool.BindRecorder(ctx, "r2", "browser", "demo")
	defer func() { _ = tool.FinishRecorder(ctx, "success") }()
	tool.RecordPatternMatchScore(ctx, 0.95)
	tool.RecordTurnOutcome(ctx, "error")
	tool.RecordTurnOutcome(ctx, "error")

	names := hookNameSet(t, ctx, reg)
	for _, must := range []string{
		"browser.eval", "browser.visual_inspect", "browser.drag",
		"human.request_takeover", "browser.sitemap",
	} {
		if !names[must] {
			t.Errorf("fallback should still expose %s; got %v", must, names)
		}
	}
}

func TestBrowserStageHookKeepsFullTurnRegistry(t *testing.T) {
	reg := buildStubBrowserRegistry()
	ctx := context.Background()
	tool.BindRecorder(ctx, "r4", "browser", "demo")
	defer func() { _ = tool.FinishRecorder(ctx, "success") }()
	tool.RecordPatternMatchScore(ctx, 0.9)

	state := hookState(t, ctx, reg)
	if state.Registry == nil {
		t.Fatal("expected turn registry override")
	}
	for _, original := range reg.List() {
		if _, ok := state.Registry.Lookup(original.Name()); !ok {
			t.Fatalf("turn registry missing %s", original.Name())
		}
	}
	if len(state.Registry.List()) != len(state.Tools) {
		t.Fatalf("turn registry/schema mismatch: registry=%d tools=%d", len(state.Registry.List()), len(state.Tools))
	}
	if len(state.Registry.List()) != len(reg.List()) {
		t.Fatalf("turn registry count = %d, want %d", len(state.Registry.List()), len(reg.List()))
	}
}

func TestBrowserStageHookStickyOnEmptyDecision(t *testing.T) {
	reg := buildStubBrowserRegistry()
	ctx := context.Background()
	tool.BindRecorder(ctx, "r3", "browser", "demo")
	defer func() { _ = tool.FinishRecorder(ctx, "success") }()

	hook := newBrowserStageHook(reg)

	// Turn 1: high score → known_flow.
	tool.RecordPatternMatchScore(ctx, 0.9)
	first, _ := hook(ctx, &loop.Run{}, 1)
	firstSet := map[string]bool{}
	for _, tl := range first.Tools {
		firstSet[tl.Name] = true
	}
	if !firstSet["browser.pattern_match"] {
		t.Fatalf("expected known_flow on first turn; got %v", firstSet)
	}

	// Turn 2: mid-score (0.5) → decider returns "" (keep previous). Hook
	// should reuse lastStage=known_flow, but tool set remains the same full set.
	tool.RecordPatternMatchScore(ctx, 0.5)
	tool.RecordTurnOutcome(ctx, "ok")
	second, _ := hook(ctx, &loop.Run{}, 2)
	secondSet := map[string]bool{}
	for _, tl := range second.Tools {
		secondSet[tl.Name] = true
	}
	for name := range firstSet {
		if !secondSet[name] {
			t.Errorf("sticky stage should keep %q on turn 2; got %v", name, secondSet)
		}
	}
	for name := range secondSet {
		if !firstSet[name] {
			t.Errorf("sticky stage produced new tool %q not present on turn 1", name)
		}
	}
	if second.ToolChoice != "" {
		t.Fatalf("second browser turn tool_choice=%q, want empty", second.ToolChoice)
	}
}

// Compile-time check: hook signature matches Runner.PreTurnHook.
var _ = func() {
	var r loop.Runner
	r.PreTurnStateHook = newBrowserStageHook(buildStubBrowserRegistry())
}
