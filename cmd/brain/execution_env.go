package main

import (
	"github.com/leef-l/brain/cmd/brain/env"
	"github.com/leef-l/brain/sdk/tool"
)

type executionEnvironment = env.Environment
type toolClass = env.ToolClass
type approvalKind = env.ApprovalKind
type approvalRequest = env.ApprovalRequest
type approvalPrompter = env.ApprovalPrompter
type permissionMode = env.PermissionMode
type chatMode = env.PermissionMode

const (
	toolClassRead     = env.ToolClassRead
	toolClassEdit     = env.ToolClassEdit
	toolClassDelete   = env.ToolClassDelete
	toolClassCommand  = env.ToolClassCommand
	toolClassExternal = env.ToolClassExternal
	approvalSandbox   = env.ApprovalSandbox
	approvalTool      = env.ApprovalTool

	modePlan              = env.ModePlan
	modeDefault           = env.ModeDefault
	modeAcceptEdits       = env.ModeAcceptEdits
	modeAuto              = env.ModeAuto
	modeRestricted        = env.ModeRestricted
	modeBypassPermissions = env.ModeBypassPermissions
)

var allModes = env.AllModes

func parsePermissionMode(s string) (permissionMode, error) {
	return env.ParsePermissionMode(s)
}

func parseChatMode(s string) (chatMode, error) {
	return env.ParsePermissionMode(s)
}

func cycleMode(m permissionMode) permissionMode {
	return env.CycleMode(m)
}

func resolvePermissionMode(flagValue string, cfg *brainConfig, preferChatMode ...bool) (permissionMode, error) {
	if flagValue != "" {
		return parsePermissionMode(flagValue)
	}
	chatFirst := len(preferChatMode) > 0 && preferChatMode[0]
	if cfg != nil {
		if chatFirst && cfg.ChatMode != "" {
			return parsePermissionMode(cfg.ChatMode)
		}
		if cfg.PermissionMode != "" {
			return parsePermissionMode(cfg.PermissionMode)
		}
		if cfg.ChatMode != "" {
			return parsePermissionMode(cfg.ChatMode)
		}
	}
	return modeDefault, nil
}

func newExecutionEnvironment(workdir string, mode permissionMode, cfg *brainConfig, approver approvalPrompter, interactive bool) *executionEnvironment {
	var sbCfg *tool.SandboxConfig
	if cfg != nil && cfg.Sandbox != nil {
		sbCfg = &tool.SandboxConfig{
			Enabled:           cfg.Sandbox.Enabled,
			AllowWrite:        append([]string(nil), cfg.Sandbox.AllowWrite...),
			DenyRead:          append([]string(nil), cfg.Sandbox.DenyRead...),
			AllowNet:          append([]string(nil), cfg.Sandbox.AllowNet...),
			FailIfUnavailable: cfg.Sandbox.FailIfUnavailable,
		}
	}
	return env.New(workdir, mode, sbCfg, approver, interactive)
}

func newFilePolicy(root string, input *filePolicyInput) (*filePolicy, error) {
	return env.NewFilePolicy(root, input)
}

func applyFilePolicy(e *executionEnvironment, input *filePolicyInput) error {
	return env.ApplyFilePolicy(e, input)
}

// manageTool is a convenience bridge that injects the local wrapConfirm adapter
// into env.ManageTool, preserving the original 2-arg call pattern used everywhere.
func manageTool(e *executionEnvironment, t tool.Tool, class toolClass) tool.Tool {
	return e.ManageTool(t, class, wrapConfirm)
}

func resolveFilePolicyInput(cfg *brainConfig, input *filePolicyInput) *filePolicyInput {
	if input != nil {
		return input
	}
	if cfg == nil || cfg.FilePolicy == nil {
		return nil
	}
	return env.ResolveFilePolicyInput(cfg.FilePolicy, input)
}
