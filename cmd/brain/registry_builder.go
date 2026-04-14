package main

import "github.com/leef-l/brain/sdk/tool"

func newBaseToolRegistry(cfg *brainConfig) tool.Registry {
	reg := tool.NewMemRegistry()
	env := newExecutionEnvironment("", modeAuto, cfg, nil, false)
	registerManagedRealTools(reg, env)
	return reg
}
