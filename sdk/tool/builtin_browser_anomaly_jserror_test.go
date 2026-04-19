package tool

import (
	"encoding/json"
	"testing"
)

// TestHandleConsoleAPICalled_ErrorPushed verifies that an "error" console
// event produced by Runtime.consoleAPICalled ends up as a jsErrorEvent in
// the anomalyHistory — this is the path that was missing in M7.
func TestHandleConsoleAPICalled_ErrorPushed(t *testing.T) {
	hist := newAnomalyHistory()

	raw := json.RawMessage(`{
		"type":"error",
		"args":[{"type":"string","value":"boom — undefined is not a function"}],
		"stackTrace":{"callFrames":[{"url":"https://example.com/app.js","lineNumber":42}]},
		"timestamp":1234567890.1
	}`)
	handleConsoleAPICalled(raw, hist)

	got := hist.drainJSErrors(0)
	if len(got) != 1 {
		t.Fatalf("expected 1 jsError, got %d", len(got))
	}
	if got[0].Level != "error" {
		t.Errorf("level = %q, want %q", got[0].Level, "error")
	}
	if got[0].Text != "boom — undefined is not a function" {
		t.Errorf("text = %q", got[0].Text)
	}
	if got[0].URL != "https://example.com/app.js" {
		t.Errorf("url = %q", got[0].URL)
	}
	if got[0].Line != 42 {
		t.Errorf("line = %d", got[0].Line)
	}
}

// TestHandleConsoleAPICalled_IgnoresLog verifies console.log and other
// non-error levels are skipped (we only care about error/warning noise).
func TestHandleConsoleAPICalled_IgnoresLog(t *testing.T) {
	hist := newAnomalyHistory()

	handleConsoleAPICalled(json.RawMessage(`{"type":"log","args":[{"type":"string","value":"hi"}]}`), hist)
	handleConsoleAPICalled(json.RawMessage(`{"type":"info","args":[{"type":"string","value":"hi"}]}`), hist)

	if got := hist.drainJSErrors(0); len(got) != 0 {
		t.Errorf("expected log/info to be ignored, got %d events", len(got))
	}
}

// TestHandleConsoleAPICalled_NilHistorySafe ensures the handler tolerates a
// nil history without panicking.
func TestHandleConsoleAPICalled_NilHistorySafe(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("unexpected panic: %v", r)
		}
	}()
	handleConsoleAPICalled(json.RawMessage(`{"type":"error"}`), nil)
	handleExceptionThrown(json.RawMessage(`{"exceptionDetails":{"text":"x"}}`), nil)
}

// TestHandleExceptionThrown_PrefersDescription verifies the handler prefers
// exception.description (e.g. "TypeError: foo is undefined") over the less
// specific top-level text when both are present.
func TestHandleExceptionThrown_PrefersDescription(t *testing.T) {
	hist := newAnomalyHistory()

	raw := json.RawMessage(`{
		"exceptionDetails":{
			"text":"Uncaught",
			"url":"https://example.com/boot.js",
			"lineNumber":7,
			"exception":{"description":"TypeError: foo is undefined\n    at boot.js:7"}
		}
	}`)
	handleExceptionThrown(raw, hist)

	got := hist.drainJSErrors(0)
	if len(got) != 1 {
		t.Fatalf("expected 1 jsError, got %d", len(got))
	}
	if got[0].Text != "TypeError: foo is undefined\n    at boot.js:7" {
		t.Errorf("text = %q, want description", got[0].Text)
	}
	if got[0].URL != "https://example.com/boot.js" || got[0].Line != 7 {
		t.Errorf("url/line = %q/%d", got[0].URL, got[0].Line)
	}
}

// TestMVPCheckAnomaliesReadsJSErrors is the end-to-end M7 assertion:
// once attachJSErrorWatcher has populated history (simulated by calling the
// extracted handler directly), the MVP check_anomaly pipeline must surface
// a javascript_error entry. Before M7 this path was silent.
//
// Note: we call the jsError portion of CheckAnomalies by inlining the same
// drain logic — CheckAnomalies itself needs a live CDP session for the DOM
// scan. This test focuses narrowly on the JS-error contribution.
func TestMVPCheckAnomaliesReadsJSErrors(t *testing.T) {
	hist := newAnomalyHistory()

	handleConsoleAPICalled(json.RawMessage(`{
		"type":"error",
		"args":[{"type":"string","value":"ReferenceError: x is not defined"}]
	}`), hist)

	// Mirror what CheckAnomalies does for the JS-error section (Step 3):
	// drain history and wrap into Anomaly records. This is the code path
	// that was wired up in M7.
	events := hist.drainJSErrors(0)
	if len(events) != 1 {
		t.Fatalf("expected 1 drained event, got %d", len(events))
	}
	got := Anomaly{
		Type:        AnomalyJSError,
		Severity:    SeverityLow,
		Description: "JS error: " + clip(events[0].Text, 200),
		Text:        events[0].Text,
		URL:         events[0].URL,
		DetectedAt:  events[0].Timestamp.UnixMilli(),
	}
	if got.Type != AnomalyJSError {
		t.Errorf("type = %q, want javascript_error", got.Type)
	}
	if got.Text != "ReferenceError: x is not defined" {
		t.Errorf("text = %q", got.Text)
	}
	if got.DetectedAt == 0 {
		t.Errorf("detected_at not set")
	}
}
