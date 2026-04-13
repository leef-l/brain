package main

import (
	"os"
	"path/filepath"
	"testing"
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
