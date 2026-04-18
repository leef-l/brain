package main

import (
	"github.com/leef-l/brain/cmd/brain/config"
	"github.com/leef-l/brain/sdk/tool"
	"github.com/leef-l/brain/sdk/toolpolicy"
)

func filterRegistryWithConfig(reg tool.Registry, cfg *brainConfig, scopes ...string) tool.Registry {
	return toolpolicy.FilterRegistry(reg, config.PolicyConfig(cfg), scopes...)
}

func toolPolicyConfig(cfg *brainConfig) *toolpolicy.Config {
	return config.PolicyConfig(cfg)
}

func toolScopesForChat(brainKind string, mode chatMode) []string {
	return toolpolicy.ToolScopesForChat(brainKind, string(mode))
}

func toolScopesForRun(brainKind string) []string {
	return toolpolicy.ToolScopesForRun(brainKind)
}
