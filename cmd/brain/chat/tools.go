package chat

import (
	"encoding/json"
	"path/filepath"

	"github.com/leef-l/brain/cmd/brain/env"
	"github.com/leef-l/brain/cmd/brain/cliruntime"
	"github.com/leef-l/brain/sdk/tool"
)

// RegisterToolsForMode populates a registry with tools appropriate for
// the given permission mode.
func RegisterToolsForMode(reg tool.Registry, mode env.PermissionMode, brainKind string, baseEnv *env.Environment, prompt env.ApprovalPrompter) {
	e := *baseEnv
	e.Mode = mode
	e.Approver = prompt
	e.Interactive = true
	if e.CmdSandbox == nil {
		e.CmdSandbox = tool.NewCommandSandbox(e.Sandbox, e.SandboxCfg)
	}

	reg.Register(cliruntime.ManageTool(&e, tool.NewReadFileTool(brainKind), env.ToolClassRead))
	reg.Register(cliruntime.ManageTool(&e, tool.NewSearchTool(brainKind), env.ToolClassRead))
	reg.Register(tool.NewCheckOutputTool())

	if mode == env.ModePlan {
		return
	}

	reg.Register(cliruntime.ManageTool(&e, tool.NewWriteFileTool(brainKind), env.ToolClassEdit))
	reg.Register(cliruntime.ManageTool(&e, tool.NewDeleteFileTool(brainKind), env.ToolClassDelete))
	reg.Register(cliruntime.NewManagedShellTool(brainKind, &e))
	reg.Register(cliruntime.NewManagedRunTestsTool(&e))
}

// ExtractOutsidePath checks if tool args contain a path outside the sandbox.
func ExtractOutsidePath(args json.RawMessage, sb *tool.Sandbox) string {
	var fields map[string]json.RawMessage
	if json.Unmarshal(args, &fields) != nil {
		return ""
	}
	for _, key := range []string{"path", "working_dir"} {
		raw, ok := fields[key]
		if !ok {
			continue
		}
		var pathVal string
		if json.Unmarshal(raw, &pathVal) != nil || pathVal == "" {
			continue
		}
		abs, err := sb.Check(pathVal)
		if err != nil {
			return filepath.Dir(abs)
		}
	}
	return ""
}
