package main

import (
	"github.com/leef-l/brain/kernel"
	"github.com/leef-l/brain/tool"
)

func registerDelegateToolIfAvailable(reg tool.Registry, orch *kernel.Orchestrator, env *executionEnvironment) {
	if reg == nil || orch == nil || len(orch.AvailableKinds()) == 0 {
		return
	}
	_ = reg.Register(newDelegateTool(orch, env))
}

func registerDelegateToolForEnvironment(reg tool.Registry, orch *kernel.Orchestrator, env *executionEnvironment) {
	if env != nil && !env.allowsDelegation() {
		return
	}
	registerDelegateToolIfAvailable(reg, orch, env)
}
