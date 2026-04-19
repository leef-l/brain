package toolpolicy

import "testing"

func TestDecideBrowserStageNoData(t *testing.T) {
	got := DecideBrowserStage(DecisionInput{}, DecisionThresholds{})
	if got != BrowserStageNewPage {
		t.Errorf("empty input -> %q, want %q", got, BrowserStageNewPage)
	}
}

func TestDecideBrowserStageHighScoreKnownFlow(t *testing.T) {
	in := DecisionInput{
		RecentPatternScores: []float64{0.5, 0.85, 0.6},
		RecentTurnOutcomes:  []string{"ok", "ok"},
	}
	got := DecideBrowserStage(in, DecisionThresholds{})
	if got != BrowserStageKnownFlow {
		t.Errorf("high-score max=0.85 -> %q, want %q", got, BrowserStageKnownFlow)
	}
}

func TestDecideBrowserStageLowScoreNewPage(t *testing.T) {
	in := DecisionInput{
		RecentPatternScores: []float64{0.2, 0.15, 0.05},
		RecentTurnOutcomes:  []string{"ok"},
	}
	got := DecideBrowserStage(in, DecisionThresholds{})
	if got != BrowserStageNewPage {
		t.Errorf("low-score max=0.2 -> %q, want %q", got, BrowserStageNewPage)
	}
}

func TestDecideBrowserStageMidScoreStable(t *testing.T) {
	in := DecisionInput{
		RecentPatternScores: []float64{0.4, 0.5, 0.6},
		RecentTurnOutcomes:  []string{"ok"},
	}
	got := DecideBrowserStage(in, DecisionThresholds{})
	if got != "" {
		t.Errorf("mid-score should return \"\" (keep previous), got %q", got)
	}
}

func TestDecideBrowserStageConsecutiveErrorsFallback(t *testing.T) {
	in := DecisionInput{
		RecentPatternScores: []float64{0.9}, // 即使有高分模式,卡住就切 fallback
		RecentTurnOutcomes:  []string{"error", "error", "ok"},
	}
	got := DecideBrowserStage(in, DecisionThresholds{})
	if got != BrowserStageFallback {
		t.Errorf("two errors in window -> %q, want %q", got, BrowserStageFallback)
	}
}

func TestDecideBrowserStageDestructiveHardWins(t *testing.T) {
	// 即使有高分模式 + 卡住窗口,destructive 是硬约束,必须先保护。
	in := DecisionInput{
		RecentPatternScores:  []float64{0.95},
		RecentTurnOutcomes:   []string{"error", "error"},
		PendingApprovalClass: "control-plane",
	}
	got := DecideBrowserStage(in, DecisionThresholds{})
	if got != BrowserStageDestructive {
		t.Errorf("destructive approval -> %q, want %q", got, BrowserStageDestructive)
	}
}

func TestDecideBrowserStageReadonlyNotDestructive(t *testing.T) {
	in := DecisionInput{
		RecentPatternScores:  []float64{0.9},
		PendingApprovalClass: "readonly",
	}
	got := DecideBrowserStage(in, DecisionThresholds{})
	if got != BrowserStageKnownFlow {
		t.Errorf("readonly should not trigger destructive; got %q", got)
	}
}

func TestDecideBrowserStageWorkspaceWriteNotDestructive(t *testing.T) {
	// workspace-write 被审批系统视为中等级别,不触发 destructive stage。
	in := DecisionInput{
		RecentPatternScores:  []float64{0.9},
		PendingApprovalClass: "workspace-write",
	}
	got := DecideBrowserStage(in, DecisionThresholds{})
	if got != BrowserStageKnownFlow {
		t.Errorf("workspace-write should not trigger destructive; got %q", got)
	}
}

func TestDecideBrowserStageExecCapableIsDestructive(t *testing.T) {
	// exec-capable 和以上都归 destructive。
	for _, c := range []string{"exec-capable", "external-network"} {
		got := DecideBrowserStage(DecisionInput{PendingApprovalClass: c}, DecisionThresholds{})
		if got != BrowserStageDestructive {
			t.Errorf("class=%s -> %q, want %q", c, got, BrowserStageDestructive)
		}
	}
}

func TestDecideBrowserStageCustomThresholds(t *testing.T) {
	// 提高 HighScoreThreshold 让 0.85 不足以 known_flow。
	in := DecisionInput{RecentPatternScores: []float64{0.85}}
	got := DecideBrowserStage(in, DecisionThresholds{HighScoreThreshold: 0.9, LowScoreThreshold: 0.3})
	if got != "" {
		t.Errorf("score below custom high but above custom low -> %q, want \"\"", got)
	}
}

func TestIsDestructiveApprovalTable(t *testing.T) {
	cases := map[string]bool{
		"readonly":          false,
		"workspace-write":   false,
		"exec-capable":      true,
		"control-plane":     true,
		"external-network":  true,
		"":                  false,
		"unknown-class":     false,
	}
	for k, want := range cases {
		if got := IsDestructiveApproval(k); got != want {
			t.Errorf("IsDestructiveApproval(%q) = %v, want %v", k, got, want)
		}
	}
}
