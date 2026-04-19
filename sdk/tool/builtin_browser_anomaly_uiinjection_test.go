package tool

import (
	"testing"
)

// TestUIInjection_KeywordOverlay verifies that a JS-side hit combining
// fixed position + high z-index + urgency keyword gets mapped to an
// AnomalyUIInjection record with severity=high and auto_resolvable=false.
func TestUIInjection_KeywordOverlay(t *testing.T) {
	hits := []uiInjectionHit{{
		Text:          "立即行动!限时验证,倒计时 00:59",
		ZIndex:        99999,
		Position:      "fixed",
		Width:         320,
		Height:        80,
		Keyword:       true,
		Recent:        false,
		InsertedAgoMs: -1,
	}}

	out := uiInjectionHitsToAnomalies(hits, 1_700_000_000_000)
	if len(out) != 1 {
		t.Fatalf("expected 1 anomaly, got %d", len(out))
	}
	a := out[0]
	if a.Type != AnomalyUIInjection {
		t.Errorf("type = %q, want ui_injection", a.Type)
	}
	if a.Severity != SeverityHigh {
		t.Errorf("severity = %q, want high", a.Severity)
	}
	if a.AutoResolvable == nil || *a.AutoResolvable {
		t.Errorf("auto_resolvable = %v, want false", a.AutoResolvable)
	}
	if a.DetectedAt != 1_700_000_000_000 {
		t.Errorf("detected_at = %d", a.DetectedAt)
	}
	// M4 contract: suggested_actions must mention request_human so the loop /
	// Agent routes this to human.request_takeover.
	foundHumanHint := false
	for _, s := range a.Suggested {
		if containsAny(s, []string{"request_human", "human.request_takeover"}) {
			foundHumanHint = true
			break
		}
	}
	if !foundHumanHint {
		t.Errorf("suggested_actions missing request_human hint: %+v", a.Suggested)
	}
}

// TestUIInjection_RecentInsertion verifies that a freshly-inserted overlay
// (Recent=true) is still flagged even without a keyword match — this covers
// the MutationObserver-based "inserted in last 5s" heuristic.
func TestUIInjection_RecentInsertion(t *testing.T) {
	hits := []uiInjectionHit{{
		Text:          "generic bait content",
		ZIndex:        5000,
		Position:      "sticky",
		Width:         300,
		Height:        100,
		Keyword:       false,
		Recent:        true,
		InsertedAgoMs: 1200,
	}}

	out := uiInjectionHitsToAnomalies(hits, 0) // 0 → fills via time.Now
	if len(out) != 1 {
		t.Fatalf("expected 1 anomaly, got %d", len(out))
	}
	if out[0].Subtype != "recently_inserted" {
		t.Errorf("subtype = %q, want recently_inserted", out[0].Subtype)
	}
	if out[0].DetectedAt <= 0 {
		t.Errorf("detected_at must default to now when ts=0")
	}
}

// TestUIInjection_RecentPlusKeyword covers the strongest signal — overlay
// that was freshly inserted AND contains urgency wording. Subtype should
// reflect both signals.
func TestUIInjection_RecentPlusKeyword(t *testing.T) {
	hits := []uiInjectionHit{{
		Text:          "Verify your account urgently",
		ZIndex:        10000,
		Position:      "fixed",
		Width:         400,
		Height:        120,
		Keyword:       true,
		Recent:        true,
		InsertedAgoMs: 300,
	}}
	out := uiInjectionHitsToAnomalies(hits, 12345)
	if len(out) != 1 {
		t.Fatalf("expected 1 anomaly, got %d", len(out))
	}
	if out[0].Subtype != "recent_urgency_overlay" {
		t.Errorf("subtype = %q, want recent_urgency_overlay", out[0].Subtype)
	}
}

// TestUIInjection_TextClipped guards against leaking enormous overlay text
// into downstream tool_result payloads. The clip budget is 300 chars.
func TestUIInjection_TextClipped(t *testing.T) {
	long := make([]byte, 1000)
	for i := range long {
		long[i] = 'x'
	}
	hits := []uiInjectionHit{{
		Text:     string(long),
		ZIndex:   99999,
		Position: "fixed",
		Width:    200,
		Height:   50,
		Keyword:  true,
	}}
	out := uiInjectionHitsToAnomalies(hits, 0)
	if len(out[0].Text) > 310 { // 300 + "..." ellipsis
		t.Errorf("text not clipped: len=%d", len(out[0].Text))
	}
}

// TestUIInjection_EmptyHitsEmptyOutput — no hits → no anomalies.
func TestUIInjection_EmptyHitsEmptyOutput(t *testing.T) {
	out := uiInjectionHitsToAnomalies(nil, 0)
	if len(out) != 0 {
		t.Errorf("expected 0, got %d", len(out))
	}
}

// TestUIInjection_ConstantDeclared guards against a future refactor losing
// the AnomalyUIInjection enum string. The value is part of the public
// AnomalyReport contract consumed by the main loop and Agent.
func TestUIInjection_ConstantDeclared(t *testing.T) {
	if string(AnomalyUIInjection) != "ui_injection" {
		t.Errorf("AnomalyUIInjection = %q, want ui_injection", string(AnomalyUIInjection))
	}
}
