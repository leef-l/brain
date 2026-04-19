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
		"browser.click", "browser.type",
		"browser.screenshot", "browser.visual_inspect", "browser.eval",
	} {
		_ = reg.Register(stubTool{name: n})
	}
	return reg
}

func hookNameSet(t *testing.T, ctx context.Context, reg tool.Registry) map[string]bool {
	t.Helper()
	hook := newBrowserStageHook(reg)
	tools, err := hook(ctx, &loop.Run{}, 1)
	if err != nil {
		t.Fatalf("hook err: %v", err)
	}
	names := map[string]bool{}
	for _, tl := range tools {
		names[tl.Name] = true
	}
	return names
}

func TestBrowserStageHookFirstTurnNewPage(t *testing.T) {
	// No recorder bound → no signals → new_page stage.
	reg := buildStubBrowserRegistry()
	names := hookNameSet(t, context.Background(), reg)
	if !names["browser.snapshot"] {
		t.Errorf("new_page should include browser.snapshot; got %v", names)
	}
	if names["browser.visual_inspect"] {
		t.Errorf("new_page should NOT include browser.visual_inspect; got %v", names)
	}
}

func TestBrowserStageHookHighScoreKnownFlow(t *testing.T) {
	reg := buildStubBrowserRegistry()
	ctx := context.Background()
	tool.BindRecorder(ctx, "r1", "browser", "demo")
	defer func() { _ = tool.FinishRecorder(ctx, "success") }()
	tool.RecordPatternMatchScore(ctx, 0.9)

	names := hookNameSet(t, ctx, reg)
	if !names["browser.pattern_match"] || !names["browser.pattern_exec"] {
		t.Errorf("known_flow should keep pattern_* tools; got %v", names)
	}
	if names["browser.sitemap"] {
		t.Errorf("known_flow profile should drop browser.sitemap; got %v", names)
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
	if !names["browser.eval"] || !names["browser.visual_inspect"] {
		t.Errorf("fallback should open eval + visual_inspect; got %v", names)
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
	for _, tl := range first {
		firstSet[tl.Name] = true
	}
	if !firstSet["browser.pattern_match"] {
		t.Fatalf("expected known_flow on first turn; got %v", firstSet)
	}

	// Turn 2: mid-score (0.5) → decider returns "" (keep previous). Hook
	// should reuse lastStage=known_flow, yielding the same filtered set.
	tool.RecordPatternMatchScore(ctx, 0.5)
	tool.RecordTurnOutcome(ctx, "ok")
	second, _ := hook(ctx, &loop.Run{}, 2)
	secondSet := map[string]bool{}
	for _, tl := range second {
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
}

// Compile-time check: hook signature matches Runner.PreTurnHook.
var _ = func() {
	var r loop.Runner
	r.PreTurnHook = newBrowserStageHook(buildStubBrowserRegistry())
}
