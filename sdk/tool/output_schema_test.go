package tool

import (
	"encoding/json"
	"testing"
)

func TestBuiltinToolsDeclareValidOutputSchema(t *testing.T) {
	var tools []Tool

	tools = append(tools,
		NewReadFileTool("code"),
		NewWriteFileTool("code"),
		NewDeleteFileTool("code"),
		NewSearchTool("code"),
		NewShellExecTool("code", nil),
		NewEchoTool("central"),
		NewRejectTaskTool("central", nil),
		NewVerifierReadFileTool(),
		NewRunTestsTool(),
		NewCheckOutputTool(),
		NewBrowserActionTool(),
		NewInjectErrorTool(),
		NewInjectLatencyTool(),
		NewKillProcessTool(),
		NewCorruptResponseTool(),
	)
	tools = append(tools, NewBrowserTools()...)

	if got, want := len(tools), 49; got != want {
		t.Fatalf("unexpected builtin tool count: got %d want %d", got, want)
	}

	for _, builtin := range tools {
		schema := builtin.Schema()
		if len(schema.OutputSchema) == 0 {
			t.Fatalf("%s missing OutputSchema", schema.Name)
		}
		if !json.Valid(schema.OutputSchema) {
			t.Fatalf("%s has invalid OutputSchema JSON: %s", schema.Name, string(schema.OutputSchema))
		}
		var decoded interface{}
		if err := json.Unmarshal(schema.OutputSchema, &decoded); err != nil {
			t.Fatalf("%s OutputSchema unmarshal failed: %v", schema.Name, err)
		}
	}
}
