package cliruntime

import (
	"os"

	"github.com/leef-l/brain/cmd/brain/env"
	"github.com/leef-l/brain/sdk/tool"
	"github.com/leef-l/brain/sdk/toolguard"
	"github.com/leef-l/brain/sdk/toolpolicy"
)

func NewManagedShellTool(brainKind string, e *env.Environment) tool.Tool {
	st := tool.NewShellExecTool(brainKind, e.Sandbox)
	if e.CmdSandbox != nil {
		st.SetCommandSandbox(e.CmdSandbox)
	}
	// 默认不把 shell stdout/stderr 直接打到终端：那会污染 chat UI（spinner / todo 框 / LLM 文本被打断）。
	// /verbose 模式下设回 os.Stderr，让用户看 long-running 命令的实时输出（npm install 等）。
	if VerboseShellStream() {
		st.StreamTo = os.Stderr
	}
	managed := e.WrapPathChecks(st)
	managed = toolguard.WrapCommandPolicy(managed, e.CmdSandbox, e.SandboxCfg, e.FilePolicy)
	return e.WrapApproval(managed, env.ToolClassCommand, env.WrapConfirm)
}

func NewManagedRunTestsTool(e *env.Environment) tool.Tool {
	rt := tool.NewRunTestsTool()
	rt.SetSandbox(e.Sandbox)
	if e.CmdSandbox != nil {
		rt.SetCommandSandbox(e.CmdSandbox)
	}
	if VerboseShellStream() {
		rt.StreamTo = os.Stderr
	}
	managed := e.WrapPathChecks(rt)
	managed = toolguard.WrapCommandPolicy(managed, e.CmdSandbox, e.SandboxCfg, e.FilePolicy)
	return e.WrapApproval(managed, env.ToolClassCommand, env.WrapConfirm)
}

func NewManagedBrowserActionTool(e *env.Environment) tool.Tool {
	return e.WrapApproval(tool.NewBrowserActionTool(), env.ToolClassExternal, env.WrapConfirm)
}

// ManageTool wraps a tool with path checks and approval.
func ManageTool(e *env.Environment, t tool.Tool, class env.ToolClass) tool.Tool {
	return e.ManageTool(t, class, env.WrapConfirm)
}

func RegisterManagedRealTools(reg tool.Registry, e *env.Environment, brainKind string) {
	// MACCS Wave 7+ 工具白名单(中央大脑铁律 — 只动嘴不动手):
	//   - 中央大脑(brainKind=="central"):只注册 read/list/search/note + 编排工具
	//     物理上无 write/edit/delete/shell_exec → LLM 必须 delegate code brain
	//   - 其他 brain(code/verifier/browser/...):正常注册全套工具
	//
	// 历史:之前 CentralAwareWrite/Shell 是"软警告",返回 executed:false JSON 让 LLM 看,
	// 但 LLM 误以为成功,反复绕过提示 → 项目集成崩。本次改为"硬剥夺",
	// 工具从 registry 中直接不出现,LLM 看不到就不会调,被迫 delegate。
	isCentral := brainKind == "central"

	// 只读工具(所有 brain 都注册)
	reg.Register(ManageTool(e, tool.NewReadFileTool("code"), env.ToolClassRead))
	reg.Register(ManageTool(e, tool.NewListFilesTool("code"), env.ToolClassRead))
	reg.Register(ManageTool(e, tool.NewSearchTool("code"), env.ToolClassRead))
	reg.Register(tool.NewNoteTool("code"))

	// 写入类工具(中央大脑跳过,只有非中央才注册)
	if !isCentral {
		reg.Register(ManageTool(e, tool.NewWriteFileTool("code"), env.ToolClassEdit))
		reg.Register(ManageTool(e, tool.NewEditFileTool("code"), env.ToolClassEdit))
		reg.Register(ManageTool(e, tool.NewDeleteFileTool("code"), env.ToolClassDelete))
		reg.Register(NewManagedShellTool("code", e))
	}

	// verifier 类只读工具(所有 brain 都注册,verifier 主用)
	reg.Register(ManageTool(e, tool.NewVerifierReadFileTool(), env.ToolClassRead))
	reg.Register(NewManagedRunTestsTool(e))
	reg.Register(tool.NewCheckOutputTool())
	if brainKind == "verifier" {
		reg.Register(NewManagedBrowserActionTool(e))
	}

	// Task #16: 人类接管工具,brain 无关,可被所有 brain 调用。
	reg.Register(tool.NewHumanRequestTakeoverTool())
}

// BuildManagedRegistry creates a filtered tool registry for non-interactive runs.
//
// 最外层会用 tool.WrapWithFailureLog 装饰每个工具：返回 IsError 时把 args + detail
// 结构化追加到 ~/.brain/logs/tool-failures.log，便于事后分析 LLM 误调用。
func BuildManagedRegistry(cfg *toolpolicy.Config, e *env.Environment, brainKind string, registerExtra func(tool.Registry)) tool.Registry {
	reg := tool.NewMemRegistry()
	RegisterManagedRealTools(reg, e, brainKind)
	if registerExtra != nil {
		registerExtra(reg)
	}
	filtered := toolpolicy.FilterRegistry(reg, cfg, toolpolicy.ToolScopesForRun(brainKind)...)
	return wrapAllWithFailureLog(filtered)
}

// wrapAllWithFailureLog 给 registry 中所有工具包一层失败日志装饰器。
// 返回新的 registry（原 registry 不变）。
func wrapAllWithFailureLog(reg tool.Registry) tool.Registry {
	if reg == nil {
		return reg
	}
	out := tool.NewMemRegistry()
	for _, t := range reg.List() {
		_ = out.Register(tool.WrapWithFailureLog(t))
	}
	return out
}
