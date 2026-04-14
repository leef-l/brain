package main

import (
	"github.com/leef-l/brain/sdk/tool"
	"github.com/leef-l/brain/sdk/toolpolicy"
)

type toolProfileConfig = toolpolicy.Profile

func filterRegistryWithConfig(reg tool.Registry, cfg *brainConfig, scopes ...string) tool.Registry {
	return toolpolicy.FilterRegistry(reg, toolPolicyConfig(cfg), scopes...)
}

func toolPolicyConfig(cfg *brainConfig) *toolpolicy.Config {
	if cfg == nil {
		return nil
	}
	return &toolpolicy.Config{
		ToolProfiles: cfg.ToolProfiles,
		ActiveTools:  cfg.ActiveTools,
	}
}

func toolScopesForChat(brainKind string, mode chatMode) []string {
	return toolpolicy.ToolScopesForChat(brainKind, string(mode))
}

func toolScopesForRun(brainKind string) []string {
	return toolpolicy.ToolScopesForRun(brainKind)
}
