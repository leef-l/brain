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

	// 用 wrap 把每个工具包一层失败日志装饰器（写 ~/.brain/logs/tool-failures.log）。
	// chat 模式不经过 BuildManagedRegistry，必须在这里手动包装，否则失败日志拿不到。
	wrap := func(t tool.Tool) tool.Tool { return tool.WrapWithFailureLog(t) }

	reg.Register(wrap(cliruntime.ManageTool(&e, tool.NewReadFileTool(brainKind), env.ToolClassRead)))
	reg.Register(wrap(cliruntime.ManageTool(&e, tool.NewListFilesTool(brainKind), env.ToolClassRead)))
	reg.Register(wrap(cliruntime.ManageTool(&e, tool.NewSearchTool(brainKind), env.ToolClassRead)))
	reg.Register(wrap(tool.NewNoteTool(brainKind)))
	reg.Register(wrap(tool.NewCheckOutputTool()))
	reg.Register(wrap(tool.NewTaskCompleteTool(brainKind)))

	if mode == env.ModePlan {
		return
	}

	reg.Register(wrap(cliruntime.ManageTool(&e, tool.NewWriteFileTool(brainKind), env.ToolClassEdit)))
	reg.Register(wrap(cliruntime.ManageTool(&e, tool.NewEditFileTool(brainKind), env.ToolClassEdit)))
	reg.Register(wrap(cliruntime.ManageTool(&e, tool.NewDeleteFileTool(brainKind), env.ToolClassDelete)))
	reg.Register(wrap(cliruntime.NewManagedShellTool(brainKind, &e)))
	reg.Register(wrap(cliruntime.NewManagedRunTestsTool(&e)))
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
