package main

import (
	"context"
	"encoding/json"
	"os"

	"github.com/leef-l/brain/sdk/executionpolicy"
	"github.com/leef-l/brain/sdk/runtimeaudit"
	"github.com/leef-l/brain/sdk/tool"
	"github.com/leef-l/brain/sdk/toolguard"
)

type toolClass string

const (
	toolClassRead     toolClass = "read"
	toolClassEdit     toolClass = "edit"
	toolClassDelete   toolClass = "delete"
	toolClassCommand  toolClass = "command"
	toolClassExternal toolClass = "external"
)

type executionEnvironment struct {
	mode           permissionMode
	workdir        string
	sandbox        *tool.Sandbox
	sandboxCfg     *tool.SandboxConfig
	filePolicy     *filePolicy
	filePolicySpec *filePolicyInput
	cmdSandbox     tool.CommandSandbox
	approver       approvalPrompter
	interactive    bool
}

// resolvePermissionMode resolves the effective permission mode.
// preferChatMode should be true for interactive commands (chat, run) so that
// chat_mode takes precedence; false for serve where permission_mode governs.
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
	if workdir == "" {
		workdir, _ = os.Getwd()
	}

	var sbCfg *tool.SandboxConfig
	if cfg != nil && cfg.Sandbox != nil {
		sbCfg = &tool.SandboxConfig{
			Enabled:           cfg.Sandbox.Enabled,
			AllowWrite:        append([]string(nil), cfg.Sandbox.AllowWrite...),
			DenyRead:          append([]string(nil), cfg.Sandbox.DenyRead...),
			AllowNet:          append([]string(nil), cfg.Sandbox.AllowNet...),
			FailIfUnavailable: cfg.Sandbox.FailIfUnavailable,
		}
	} else {
		sbCfg = &tool.SandboxConfig{Enabled: true}
	}

	sb := tool.NewSandbox(workdir)

	return &executionEnvironment{
		mode:        mode,
		workdir:     sb.Primary(),
		sandbox:     sb,
		sandboxCfg:  sbCfg,
		cmdSandbox:  tool.NewCommandSandbox(sb, sbCfg),
		approver:    approver,
		interactive: interactive,
	}
}

func (e *executionEnvironment) autoApprove(class toolClass) bool {
	switch e.mode {
	case modePlan:
		return class == toolClassRead
	case modeDefault:
		return class == toolClassRead
	case modeAcceptEdits:
		return class == toolClassRead || class == toolClassEdit || class == toolClassDelete
	case modeAuto:
		return class == toolClassRead || class == toolClassEdit || class == toolClassDelete || class == toolClassCommand
	case modeRestricted:
		return class == toolClassRead || class == toolClassEdit || class == toolClassDelete || class == toolClassCommand
	case modeBypassPermissions:
		return true
	default:
		return false
	}
}

func (e *executionEnvironment) wrapPathChecks(t tool.Tool) tool.Tool {
	var sbApprover tool.SandboxApprover
	if e.approver != nil {
		sbApprover = func(ctx context.Context, toolName string, absPath string, dir string) bool {
			req := approvalRequest{
				kind:       approvalSandbox,
				toolName:   toolName,
				outsideDir: dir,
			}
			runtimeaudit.Emit(ctx, runtimeaudit.Event{
				Type:    "approval.requested",
				Message: "sandbox escape approval requested",
				Data:    json.RawMessage(fmtJSON(map[string]interface{}{"kind": req.kind, "tool": req.toolName, "outside_dir": req.outsideDir})),
			})
			allowed := e.approver(ctx, req)
			eventType := "approval.denied"
			message := "sandbox escape denied"
			if allowed {
				eventType = "approval.allowed"
				message = "sandbox escape approved"
			}
			runtimeaudit.Emit(ctx, runtimeaudit.Event{
				Type:    eventType,
				Message: message,
				Data:    json.RawMessage(fmtJSON(map[string]interface{}{"kind": req.kind, "tool": req.toolName, "outside_dir": req.outsideDir})),
			})
			return allowed
		}
	}
	return tool.WrapSandboxWithApprover(t, e.sandbox, sbApprover)
}

func (e *executionEnvironment) wrapApproval(t tool.Tool, class toolClass) tool.Tool {
	if e.autoApprove(class) {
		return t
	}
	return wrapConfirm(t, e.sandbox, e.approver)
}

func (e *executionEnvironment) manageTool(t tool.Tool, class toolClass) tool.Tool {
	t = e.wrapPathChecks(t)
	if class == toolClassRead {
		t = toolguard.WrapReadPolicy(t, e.filePolicy)
	}
	if class == toolClassEdit {
		t = e.wrapFilePolicy(t)
	}
	if class == toolClassDelete {
		t = toolguard.WrapDeletePolicy(t, e.filePolicy)
	}
	t = e.wrapApproval(t, class)
	return t
}

func (e *executionEnvironment) commandReady() bool {
	return e.cmdSandbox != nil && e.cmdSandbox.Available()
}

func (e *executionEnvironment) wrapFilePolicy(t tool.Tool) tool.Tool {
	if e.filePolicy == nil || !e.filePolicy.Enabled() {
		return t
	}
	return toolguard.WrapFilePolicy(t, e.filePolicy)
}

func (e *executionEnvironment) allowsDelegation() bool {
	return e == nil || e.filePolicy == nil || e.filePolicy.AllowsDelegation()
}

func (e *executionEnvironment) executionSpec() *executionpolicy.ExecutionSpec {
	if e == nil {
		return nil
	}
	spec := &executionpolicy.ExecutionSpec{Workdir: e.workdir}
	if e.filePolicySpec != nil {
		fp := *e.filePolicySpec
		fp.AllowRead = append([]string(nil), e.filePolicySpec.AllowRead...)
		fp.AllowCreate = append([]string(nil), e.filePolicySpec.AllowCreate...)
		fp.AllowEdit = append([]string(nil), e.filePolicySpec.AllowEdit...)
		fp.AllowDelete = append([]string(nil), e.filePolicySpec.AllowDelete...)
		fp.Deny = append([]string(nil), e.filePolicySpec.Deny...)
		if e.filePolicySpec.AllowCommands != nil {
			v := *e.filePolicySpec.AllowCommands
			fp.AllowCommands = &v
		}
		if e.filePolicySpec.AllowDelegate != nil {
			v := *e.filePolicySpec.AllowDelegate
			fp.AllowDelegate = &v
		}
		spec.FilePolicy = &fp
	}
	return spec
}
