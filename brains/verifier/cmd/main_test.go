package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/leef-l/brain/sdk/sidecar"
)

func TestNewVerifierHandler_AppliesDelegateToolPolicy(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	data := `{
  "tool_profiles": {
    "no_browser": {
      "include": ["verifier.*"],
      "exclude": ["verifier.browser_action"]
    }
  },
  "active_tools": {
    "delegate.verifier": "no_browser"
  }
}`
	if err := os.WriteFile(configPath, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BRAIN_CONFIG", configPath)

	h := newVerifierHandler()
	if _, ok := h.registry.Lookup("verifier.browser_action"); ok {
		t.Fatalf("verifier.browser_action should be filtered out")
	}
	if _, ok := h.registry.Lookup("verifier.read_file"); !ok {
		t.Fatalf("verifier.read_file should remain available")
	}
}

func TestApplyVerifierVerdict_FailMarksExecuteResultFailed(t *testing.T) {
	result := &sidecar.ExecuteResult{
		Status:  "completed",
		Summary: "Found regression.\nVERDICT: FAIL - output mismatch",
	}

	applyVerifierVerdict(result)

	if result.Status != "failed" {
		t.Fatalf("status=%q, want failed", result.Status)
	}
	if result.Error != "output mismatch" {
		t.Fatalf("error=%q, want output mismatch", result.Error)
	}
	if result.Verification == nil || result.Verification.Passed == nil || *result.Verification.Passed {
		t.Fatalf("verification=%+v, want passed=false", result.Verification)
	}
}

func TestApplyVerifierVerdict_PassPopulatesStructuredVerification(t *testing.T) {
	result := &sidecar.ExecuteResult{
		Status:  "completed",
		Summary: "Everything looks good.\nVERDICT: PASS - tests and UI checks passed",
	}

	applyVerifierVerdict(result)

	if result.Status != "completed" {
		t.Fatalf("status=%q, want completed", result.Status)
	}
	if result.Verification == nil || result.Verification.Passed == nil || !*result.Verification.Passed {
		t.Fatalf("verification=%+v, want passed=true", result.Verification)
	}
	if result.Verification.SourceTool != "verifier.verdict" {
		t.Fatalf("source_tool=%q, want verifier.verdict", result.Verification.SourceTool)
	}
}
