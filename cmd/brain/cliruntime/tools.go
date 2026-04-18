package cliruntime

import (
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

func RegisterManagedRealTools(reg tool.Registry, e *env.Environment) {
	reg.Register(ManageTool(e, tool.NewReadFileTool("code"), env.ToolClassRead))
	reg.Register(ManageTool(e, tool.NewWriteFileTool("code"), env.ToolClassEdit))
	reg.Register(ManageTool(e, tool.NewDeleteFileTool("code"), env.ToolClassDelete))
	reg.Register(ManageTool(e, tool.NewSearchTool("code"), env.ToolClassRead))
	reg.Register(NewManagedShellTool("code", e))

	reg.Register(ManageTool(e, tool.NewVerifierReadFileTool(), env.ToolClassRead))
	reg.Register(NewManagedRunTestsTool(e))
	reg.Register(tool.NewCheckOutputTool())
	reg.Register(NewManagedBrowserActionTool(e))
}

// BuildManagedRegistry creates a filtered tool registry for non-interactive runs.
func BuildManagedRegistry(cfg *toolpolicy.Config, e *env.Environment, brainKind string, registerExtra func(tool.Registry)) tool.Registry {
	reg := tool.NewMemRegistry()
	RegisterManagedRealTools(reg, e)
	if registerExtra != nil {
		registerExtra(reg)
	}
	return toolpolicy.FilterRegistry(reg, cfg, toolpolicy.ToolScopesForRun(brainKind)...)
}
