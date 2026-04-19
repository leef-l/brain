package main

import (
	"github.com/leef-l/brain/cmd/brain/config"
	"github.com/leef-l/brain/sdk/tool"
	"github.com/leef-l/brain/sdk/toolpolicy"
)

var globalAdaptivePolicy *toolpolicy.DefaultAdaptivePolicy

func initAdaptiveToolPolicy(cfg *brainConfig) {
	policyCfg := config.PolicyConfig(cfg)
	globalAdaptivePolicy = toolpolicy.NewAdaptivePolicy(policyCfg)
}

func filterRegistryWithConfig(reg tool.Registry, cfg *brainConfig, scopes ...string) tool.Registry {
	if globalAdaptivePolicy != nil {
		return globalAdaptivePolicy.Evaluate(toolpolicy.EvalRequest{Scopes: scopes}, reg)
	}
	return toolpolicy.FilterRegistry(reg, config.PolicyConfig(cfg), scopes...)
}

func adaptiveRecordToolOutcome(toolName, taskType string, success bool) {
	if globalAdaptivePolicy != nil {
		globalAdaptivePolicy.RecordOutcome(toolName, taskType, success)
	}
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
