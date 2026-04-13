package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewCodeHandler_AppliesDelegateToolPolicy(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	data := `{
  "tool_profiles": {
    "no_shell": {
      "include": ["code.*"],
      "exclude": ["code.shell_exec"]
    }
  },
  "active_tools": {
    "delegate.code": "no_shell"
  }
}`
	if err := os.WriteFile(configPath, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BRAIN_CONFIG", configPath)

	h := newCodeHandler()
	if _, ok := h.registry.Lookup("code.shell_exec"); ok {
		t.Fatalf("code.shell_exec should be filtered out")
	}
	if _, ok := h.registry.Lookup("code.read_file"); !ok {
		t.Fatalf("code.read_file should remain available")
	}
	for _, name := range h.Tools() {
		if name == "code.shell_exec" {
			t.Fatalf("Tools() should reflect filtered registry")
		}
	}
}
