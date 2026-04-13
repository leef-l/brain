package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewBrowserHandler_AppliesDelegateToolPolicy(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	data := `{
  "tool_profiles": {
    "no_eval": {
      "include": ["browser.*"],
      "exclude": ["browser.eval"]
    }
  },
  "active_tools": {
    "delegate.browser": "no_eval"
  }
}`
	if err := os.WriteFile(configPath, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BRAIN_CONFIG", configPath)

	h := newBrowserHandler()
	if _, ok := h.registry.Lookup("browser.eval"); ok {
		t.Fatalf("browser.eval should be filtered out")
	}
	if _, ok := h.registry.Lookup("browser.open"); !ok {
		t.Fatalf("browser.open should remain available")
	}
}
