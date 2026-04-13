package main

import (
	"github.com/leef-l/brain/tool"
	"github.com/leef-l/brain/toolguard"
)

func newManagedShellTool(brainKind string, env *executionEnvironment) tool.Tool {
	st := tool.NewShellExecTool(brainKind, env.sandbox)
	if env.cmdSandbox != nil {
		st.SetCommandSandbox(env.cmdSandbox)
	}
	managed := env.wrapPathChecks(st)
	managed = toolguard.WrapCommandPolicy(managed, env.cmdSandbox, env.sandboxCfg, env.filePolicy)
	return env.wrapApproval(managed, toolClassCommand)
}

func newManagedRunTestsTool(env *executionEnvironment) tool.Tool {
	rt := tool.NewRunTestsTool()
	rt.SetSandbox(env.sandbox)
	if env.cmdSandbox != nil {
		rt.SetCommandSandbox(env.cmdSandbox)
	}
	managed := env.wrapPathChecks(rt)
	managed = toolguard.WrapCommandPolicy(managed, env.cmdSandbox, env.sandboxCfg, env.filePolicy)
	return env.wrapApproval(managed, toolClassCommand)
}

func newManagedBrowserActionTool(env *executionEnvironment) tool.Tool {
	return env.wrapApproval(tool.NewBrowserActionTool(), toolClassExternal)
}

func registerManagedRealTools(reg tool.Registry, env *executionEnvironment) {
	// Code brain tools
	reg.Register(env.manageTool(tool.NewReadFileTool("code"), toolClassRead))
	reg.Register(env.manageTool(tool.NewWriteFileTool("code"), toolClassEdit))
	reg.Register(env.manageTool(tool.NewDeleteFileTool("code"), toolClassDelete))
	reg.Register(env.manageTool(tool.NewSearchTool("code"), toolClassRead))
	reg.Register(newManagedShellTool("code", env))

	// Verifier brain tools
	reg.Register(env.manageTool(tool.NewVerifierReadFileTool(), toolClassRead))
	reg.Register(newManagedRunTestsTool(env))
	reg.Register(tool.NewCheckOutputTool())
	reg.Register(newManagedBrowserActionTool(env))
}

func buildManagedRegistry(cfg *brainConfig, env *executionEnvironment, brainKind string, registerExtra func(tool.Registry)) tool.Registry {
	reg := tool.NewMemRegistry()
	registerManagedRealTools(reg, env)
	if registerExtra != nil {
		registerExtra(reg)
	}
	return filterRegistryWithConfig(reg, cfg, toolScopesForRun(brainKind)...)
}
