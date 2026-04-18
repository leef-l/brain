package main

import (
	cmds "github.com/leef-l/brain/cmd/brain/command"
	"github.com/leef-l/brain/sdk/tool"
)

func runTool(args []string) int {
	return cmds.RunTool(args, cmds.ToolDeps{
		LoadConfigOrEmpty: loadConfigOrEmpty,
		BuildRegistry:     buildToolCommandRegistry,
		FilterRegistry: func(reg tool.Registry, cfg *brainConfig, scopes ...string) tool.Registry {
			return filterRegistryWithConfig(reg, cfg, scopes...)
		},
	})
}

func buildToolCommandRegistry(cfg *brainConfig) tool.Registry {
	reg := newBaseToolRegistry(cfg)
	orch := buildOrchestrator(orchestratorConfig{cfg: cfg})
	registerDelegateToolIfAvailable(reg, orch, nil)
	return reg
}
