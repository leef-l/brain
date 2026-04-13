package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewFaultHandler_AppliesDelegateToolPolicy(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	data := `{
  "tool_profiles": {
    "safe_faults": {
      "include": ["fault.*"],
      "exclude": ["fault.kill_process"]
    }
  },
  "active_tools": {
    "delegate.fault": "safe_faults"
  }
}`
	if err := os.WriteFile(configPath, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BRAIN_CONFIG", configPath)

	h := newFaultHandler()
	if _, ok := h.registry.Lookup("fault.kill_process"); ok {
		t.Fatalf("fault.kill_process should be filtered out")
	}
	if _, ok := h.registry.Lookup("fault.inject_latency"); !ok {
		t.Fatalf("fault.inject_latency should remain available")
	}
}
