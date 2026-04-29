package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/leef-l/brain/sdk/agent"
	"github.com/leef-l/brain/sdk/shared"
	"github.com/leef-l/brain/sdk/tool"
)

func TestRunBrain_FaultRegistryFiltered(t *testing.T) {
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

	reg := shared.RegisterWithPolicy(agent.KindFault,
		tool.NewInjectErrorTool(),
		tool.NewInjectLatencyTool(),
		tool.NewKillProcessTool(),
		tool.NewCorruptResponseTool(),
		tool.NewNoteTool("fault"),
	)
	if _, ok := reg.Lookup("fault.kill_process"); ok {
		t.Fatalf("fault.kill_process should be filtered out")
	}
	if _, ok := reg.Lookup("fault.inject_latency"); !ok {
		t.Fatalf("fault.inject_latency should remain available")
	}
}
