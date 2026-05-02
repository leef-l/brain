package chat

import (
	"encoding/json"
	"path/filepath"

	"github.com/leef-l/brain/cmd/brain/cliruntime"
	"github.com/leef-l/brain/cmd/brain/env"
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

	// MACCS 工具策略:central 套智能分级包装 (CentralAware*),其他 brain 全注册。
	// CentralAwareWrite:write_file content > 5000 字符 / edit / delete 自动拦截改建议 delegate。
	// CentralAwareShell:非只读命令(rm/git push/build)自动拦截改建议 delegate。
	// 这样中央仍能写设计文档 / 跑 ls cat 等"小事",但碰到真代码 / 改文件 / 跑构建必走 delegate。
	// 之前 chat 模式漏掉这层包装,中央可以无限制写代码、跑命令,绕过 MACCS 编排层。
	writeFile := cliruntime.CentralAwareWrite(tool.NewWriteFileTool(brainKind), brainKind)
	editFile := cliruntime.CentralAwareWrite(tool.NewEditFileTool(brainKind), brainKind)
	deleteFile := cliruntime.CentralAwareWrite(tool.NewDeleteFileTool(brainKind), brainKind)
	reg.Register(wrap(cliruntime.ManageTool(&e, writeFile, env.ToolClassEdit)))
	reg.Register(wrap(cliruntime.ManageTool(&e, editFile, env.ToolClassEdit)))
	reg.Register(wrap(cliruntime.ManageTool(&e, deleteFile, env.ToolClassDelete)))

	shellTool := cliruntime.NewManagedShellTool(brainKind, &e)
	if brainKind == "central" {
		// shell 也走 central 智能包装。NewManagedShellTool 已经是 wrapped tool.Tool,
		// 我们在最外层再套一层 CentralAwareShell — 顺序无关紧要,Execute 都会被代理。
		shellTool = cliruntime.CentralAwareShell(shellTool, brainKind)
	}
	reg.Register(wrap(shellTool))
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
