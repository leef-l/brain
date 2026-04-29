package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/leef-l/brain/sdk/agent"
	"github.com/leef-l/brain/sdk/shared"
	"github.com/leef-l/brain/sdk/tool"
)

func TestRunBrain_CodeHandlerExists(t *testing.T) {
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

	reg := shared.RegisterWithPolicy(agent.KindCode,
		tool.NewReadFileTool("code"),
		tool.NewWriteFileTool("code"),
		tool.NewEditFileTool("code"),
		tool.NewDeleteFileTool("code"),
		tool.NewListFilesTool("code"),
		tool.NewSearchTool("code"),
		tool.NewShellExecTool("code", nil),
		tool.NewNoteTool("code"),
	)
	if _, ok := reg.Lookup("code.shell_exec"); ok {
		t.Fatalf("code.shell_exec should be filtered out")
	}
	if _, ok := reg.Lookup("code.read_file"); !ok {
		t.Fatalf("code.read_file should remain available")
	}
}
