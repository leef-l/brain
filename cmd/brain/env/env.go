package env

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/leef-l/brain/sdk/executionpolicy"
	"github.com/leef-l/brain/sdk/runtimeaudit"
	"github.com/leef-l/brain/sdk/tool"
	"github.com/leef-l/brain/sdk/toolguard"
)

type ToolClass string

const (
	ToolClassRead     ToolClass = "read"
	ToolClassEdit     ToolClass = "edit"
	ToolClassDelete   ToolClass = "delete"
	ToolClassCommand  ToolClass = "command"
	ToolClassExternal ToolClass = "external"
)

type PermissionMode string

const (
	ModePlan              PermissionMode = "plan"
	ModeDefault           PermissionMode = "default"
	ModeAcceptEdits       PermissionMode = "accept-edits"
	ModeAuto              PermissionMode = "auto"
	ModeRestricted        PermissionMode = "restricted"
	ModeBypassPermissions PermissionMode = "bypass-permissions"
)

var AllModes = []PermissionMode{
	ModePlan, ModeDefault, ModeAcceptEdits, ModeAuto, ModeRestricted, ModeBypassPermissions,
}

func ParsePermissionMode(s string) (PermissionMode, error) {
	switch strings.ToLower(s) {
	case "plan":
		return ModePlan, nil
	case "default":
		return ModeDefault, nil
	case "accept-edits", "acceptedits":
		return ModeAcceptEdits, nil
	case "auto":
		return ModeAuto, nil
	case "restricted":
		return ModeRestricted, nil
	case "acceptedits+sandbox":
		return ModeAcceptEdits, nil
	case "bypasspermissions+sandbox":
		return ModeBypassPermissions, nil
	case "bypass-permissions", "bypasspermissions", "bypass":
		return ModeBypassPermissions, nil
	default:
		return "", fmt.Errorf("unknown mode %q (use plan, default, accept-edits, auto, restricted, or bypass-permissions)", s)
	}
}

func (m PermissionMode) Label() string {
	switch m {
	case ModePlan:
		return "plan (read-only)"
	case ModeDefault:
		return "default (always confirm)"
	case ModeAcceptEdits:
		return "accept-edits (auto-approve edits)"
	case ModeAuto:
		return "auto (sandboxed auto-approve)"
	case ModeRestricted:
		return "restricted (file-policy enforced)"
	case ModeBypassPermissions:
		return "bypass-permissions (no confirmation, sandbox still enforced)"
	default:
		return string(m)
	}
}

func (m PermissionMode) StyledLabel() string {
	switch m {
	case ModePlan:
		return "\033[1;33m>\033[0m plan"
	case ModeDefault:
		return "\033[1;36m>\033[0m default"
	case ModeAcceptEdits:
		return "\033[1;32m>\033[0m accept-edits"
	case ModeAuto:
		return "\033[1;35m>\033[0m auto"
	case ModeRestricted:
		return "\033[1;34m>\033[0m restricted"
	case ModeBypassPermissions:
		return "\033[1;31m>\033[0m bypass"
	default:
		return m.Label()
	}
}

func CycleMode(m PermissionMode) PermissionMode {
	for i, mode := range AllModes {
		if mode == m {
			return AllModes[(i+1)%len(AllModes)]
		}
	}
	return ModeDefault
}

type ApprovalKind string

const (
	ApprovalSandbox ApprovalKind = "sandbox"
	ApprovalTool    ApprovalKind = "tool"
)

type ApprovalRequest struct {
	Kind       ApprovalKind
	ToolName   string
	ToolRisk   tool.Risk
	Args       json.RawMessage
	OutsideDir string
	AnswerCh   chan bool
}

type ApprovalPrompter func(ctx context.Context, req ApprovalRequest) bool

type FilePolicyInput = executionpolicy.FilePolicySpec
type FilePolicy = executionpolicy.FilePolicy

type Environment struct {
	Mode           PermissionMode
	Workdir        string
	Sandbox        *tool.Sandbox
	SandboxCfg     *tool.SandboxConfig
	FilePolicy     *FilePolicy
	FilePolicySpec *FilePolicyInput
	CmdSandbox     tool.CommandSandbox
	Approver       ApprovalPrompter
	Interactive    bool
}

type SandboxCfg struct {
	Enabled           bool
	AllowWrite        []string
	DenyRead          []string
	AllowNet          []string
	FailIfUnavailable bool
}

func New(workdir string, mode PermissionMode, sbCfg *tool.SandboxConfig, approver ApprovalPrompter, interactive bool) *Environment {
	if workdir == "" {
		workdir, _ = os.Getwd()
	}
	if sbCfg == nil {
		sbCfg = &tool.SandboxConfig{Enabled: true}
	}

	sb := tool.NewSandbox(workdir)

	return &Environment{
		Mode:        mode,
		Workdir:     sb.Primary(),
		Sandbox:     sb,
		SandboxCfg:  sbCfg,
		CmdSandbox:  tool.NewCommandSandbox(sb, sbCfg),
		Approver:    approver,
		Interactive: interactive,
	}
}

func (e *Environment) AutoApprove(class ToolClass) bool {
	switch e.Mode {
	case ModePlan:
		return class == ToolClassRead
	case ModeDefault:
		return class == ToolClassRead
	case ModeAcceptEdits:
		return class == ToolClassRead || class == ToolClassEdit || class == ToolClassDelete
	case ModeAuto:
		return class == ToolClassRead || class == ToolClassEdit || class == ToolClassDelete || class == ToolClassCommand
	case ModeRestricted:
		return class == ToolClassRead || class == ToolClassEdit || class == ToolClassDelete || class == ToolClassCommand
	case ModeBypassPermissions:
		return true
	default:
		return false
	}
}

func (e *Environment) WrapPathChecks(t tool.Tool) tool.Tool {
	var sbApprover tool.SandboxApprover
	if e.Approver != nil {
		sbApprover = func(ctx context.Context, toolName string, absPath string, dir string) bool {
			req := ApprovalRequest{
				Kind:       ApprovalSandbox,
				ToolName:   toolName,
				OutsideDir: dir,
			}
			runtimeaudit.Emit(ctx, runtimeaudit.Event{
				Type:    "approval.requested",
				Message: "sandbox escape approval requested",
				Data:    json.RawMessage(FmtJSON(map[string]interface{}{"kind": req.Kind, "tool": req.ToolName, "outside_dir": req.OutsideDir})),
			})
			allowed := e.Approver(ctx, req)
			eventType := "approval.denied"
			message := "sandbox escape denied"
			if allowed {
				eventType = "approval.allowed"
				message = "sandbox escape approved"
			}
			runtimeaudit.Emit(ctx, runtimeaudit.Event{
				Type:    eventType,
				Message: message,
				Data:    json.RawMessage(FmtJSON(map[string]interface{}{"kind": req.Kind, "tool": req.ToolName, "outside_dir": req.OutsideDir})),
			})
			return allowed
		}
	}
	return tool.WrapSandboxWithApprover(t, e.Sandbox, sbApprover)
}

func (e *Environment) WrapApproval(t tool.Tool, class ToolClass, wrapConfirm func(tool.Tool, *tool.Sandbox, ApprovalPrompter) tool.Tool) tool.Tool {
	if e.AutoApprove(class) {
		return t
	}
	return wrapConfirm(t, e.Sandbox, e.Approver)
}

func (e *Environment) ManageTool(t tool.Tool, class ToolClass, wrapConfirm func(tool.Tool, *tool.Sandbox, ApprovalPrompter) tool.Tool) tool.Tool {
	t = e.WrapPathChecks(t)
	if class == ToolClassRead {
		t = toolguard.WrapReadPolicy(t, e.FilePolicy)
	}
	if class == ToolClassEdit {
		t = e.WrapFilePolicy(t)
	}
	if class == ToolClassDelete {
		t = toolguard.WrapDeletePolicy(t, e.FilePolicy)
	}
	t = e.WrapApproval(t, class, wrapConfirm)
	return t
}

func (e *Environment) CommandReady() bool {
	return e.CmdSandbox != nil && e.CmdSandbox.Available()
}

func (e *Environment) WrapFilePolicy(t tool.Tool) tool.Tool {
	if e.FilePolicy == nil || !e.FilePolicy.Enabled() {
		return t
	}
	return toolguard.WrapFilePolicy(t, e.FilePolicy)
}

func (e *Environment) AllowsDelegation() bool {
	return e == nil || e.FilePolicy == nil || e.FilePolicy.AllowsDelegation()
}

func (e *Environment) ExecutionSpec() *executionpolicy.ExecutionSpec {
	if e == nil {
		return nil
	}
	spec := &executionpolicy.ExecutionSpec{Workdir: e.Workdir}
	if e.FilePolicySpec != nil {
		fp := *e.FilePolicySpec
		fp.AllowRead = append([]string(nil), e.FilePolicySpec.AllowRead...)
		fp.AllowCreate = append([]string(nil), e.FilePolicySpec.AllowCreate...)
		fp.AllowEdit = append([]string(nil), e.FilePolicySpec.AllowEdit...)
		fp.AllowDelete = append([]string(nil), e.FilePolicySpec.AllowDelete...)
		fp.Deny = append([]string(nil), e.FilePolicySpec.Deny...)
		if e.FilePolicySpec.AllowCommands != nil {
			v := *e.FilePolicySpec.AllowCommands
			fp.AllowCommands = &v
		}
		if e.FilePolicySpec.AllowDelegate != nil {
			v := *e.FilePolicySpec.AllowDelegate
			fp.AllowDelegate = &v
		}
		spec.FilePolicy = &fp
	}
	return spec
}

func FmtJSON(v interface{}) string {
	data, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(data)
}
