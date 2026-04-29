package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/leef-l/brain/sdk/agent"
	"github.com/leef-l/brain/sdk/shared"
	"github.com/leef-l/brain/sdk/tool"
)

func TestRunBrain_VerifierRegistryFiltered(t *testing.T) {
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

	reg := shared.RegisterWithPolicy(agent.KindVerifier,
		tool.NewVerifierReadFileTool(),
		tool.NewRunTestsTool(),
		tool.NewCheckOutputTool(),
		tool.NewBrowserActionTool(),
		tool.NewNoteTool("verifier"),
	)
	if _, ok := reg.Lookup("verifier.browser_action"); ok {
		t.Fatalf("verifier.browser_action should be filtered out")
	}
	if _, ok := reg.Lookup("verifier.read_file"); !ok {
		t.Fatalf("verifier.read_file should remain available")
	}
}
