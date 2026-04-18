package main

import (
	"github.com/leef-l/brain/cmd/brain/cliruntime"
	"github.com/leef-l/brain/cmd/brain/config"
	"github.com/leef-l/brain/sdk/tool"
)

var (
	newManagedShellTool       = cliruntime.NewManagedShellTool
	newManagedRunTestsTool    = cliruntime.NewManagedRunTestsTool
	newManagedBrowserActionTool = cliruntime.NewManagedBrowserActionTool
	registerManagedRealTools  = cliruntime.RegisterManagedRealTools
)

func buildManagedRegistry(cfg *brainConfig, env *executionEnvironment, brainKind string, registerExtra func(tool.Registry)) tool.Registry {
	return cliruntime.BuildManagedRegistry(config.PolicyConfig(cfg), env, brainKind, registerExtra)
}
