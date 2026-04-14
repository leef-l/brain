package toolguard

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/leef-l/brain/sdk/executionpolicy"
	"github.com/leef-l/brain/sdk/runtimeaudit"
	"github.com/leef-l/brain/sdk/tool"
)

type Boundaries struct {
	Workdir        string
	Sandbox        *tool.Sandbox
	SandboxConfig  *tool.SandboxConfig
	FilePolicy     *executionpolicy.FilePolicy
	CommandSandbox tool.CommandSandbox
}

func NewBoundaries(spec *executionpolicy.ExecutionSpec) (*Boundaries, error) {
	workdir := ""
	if spec != nil {
		workdir = strings.TrimSpace(spec.Workdir)
	}
	if workdir == "" {
		var err error
		workdir, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("resolve workdir: %w", err)
		}
	}

	sandboxCfg := &tool.SandboxConfig{Enabled: true}
	var filePolicy *executionpolicy.FilePolicy
	var err error
	if spec != nil && spec.FilePolicy != nil {
		filePolicy, err = executionpolicy.NewFilePolicy(workdir, spec.FilePolicy)
		if err != nil {
			return nil, err
		}
	}

	sb := tool.NewSandbox(workdir)
	return &Boundaries{
		Workdir:        sb.Primary(),
		Sandbox:        sb,
		SandboxConfig:  sandboxCfg,
		FilePolicy:     filePolicy,
		CommandSandbox: tool.NewCommandSandbox(sb, sandboxCfg),
	}, nil
}

func WrapFilePolicy(t tool.Tool, policy *executionpolicy.FilePolicy) tool.Tool {
	if policy == nil || !policy.Enabled() {
		return t
	}
	return &writePolicyTool{inner: t, policy: policy}
}

func WrapDeletePolicy(t tool.Tool, policy *executionpolicy.FilePolicy) tool.Tool {
	if policy == nil || !policy.Enabled() {
		return t
	}
	return &deletePolicyTool{inner: t, policy: policy}
}

func WrapReadPolicy(t tool.Tool, policy *executionpolicy.FilePolicy) tool.Tool {
	if policy == nil || !policy.Enabled() {
		return t
	}
	return &readPolicyTool{inner: t, policy: policy}
}

func WrapCommandPolicy(t tool.Tool, cmdSandbox tool.CommandSandbox, cfg *tool.SandboxConfig, policy *executionpolicy.FilePolicy) tool.Tool {
	return &commandGuardTool{
		inner:      t,
		cmdSandbox: cmdSandbox,
		cfg:        cfg,
		policy:     policy,
	}
}

type commandGuardTool struct {
	inner      tool.Tool
	cmdSandbox tool.CommandSandbox
	cfg        *tool.SandboxConfig
	policy     *executionpolicy.FilePolicy
}

func (t *commandGuardTool) Name() string        { return t.inner.Name() }
func (t *commandGuardTool) Schema() tool.Schema { return t.inner.Schema() }
func (t *commandGuardTool) Risk() tool.Risk     { return t.inner.Risk() }

func (t *commandGuardTool) Execute(ctx context.Context, args json.RawMessage) (*tool.Result, error) {
	if t.policy != nil && t.policy.Enabled() && !t.policy.AllowsCommands() {
		msg := "command execution denied by file policy"
		runtimeaudit.Emit(ctx, runtimeaudit.Event{
			Type:    "policy.command.denied",
			Message: msg,
			Data:    json.RawMessage(`{"reason":"allow_commands=false"}`),
		})
		return jsonError(msg), nil
	}
	if t.cmdSandbox != nil && t.cmdSandbox.Available() {
		if t.policy != nil && t.policy.Enabled() {
			switch {
			case strings.HasSuffix(t.inner.Name(), ".shell_exec"), strings.HasSuffix(t.inner.Name(), ".run_tests"):
				return executeRestrictedCommand(ctx, t.inner.Name(), args, t.policy, t.cfg)
			default:
				root := t.policy.Root()
				return validateCommandMutations(ctx, root, t.policy, func(runCtx context.Context) (*tool.Result, error) {
					return t.inner.Execute(runCtx, args)
				})
			}
		}
		return t.inner.Execute(ctx, args)
	}

	msg := "command execution denied: OS-level command sandbox is unavailable"
	if t.cfg != nil && t.cfg.FailIfUnavailable {
		msg += " and fail_if_unavailable is enabled"
	}
	runtimeaudit.Emit(ctx, runtimeaudit.Event{
		Type:    "policy.command.denied",
		Message: msg,
		Data:    json.RawMessage(`{"reason":"sandbox_unavailable"}`),
	})

	raw, _ := json.Marshal(msg)
	return &tool.Result{Output: raw, IsError: true}, nil
}

type writePolicyTool struct {
	inner  tool.Tool
	policy *executionpolicy.FilePolicy
}

func (t *writePolicyTool) Name() string        { return t.inner.Name() }
func (t *writePolicyTool) Schema() tool.Schema { return t.inner.Schema() }
func (t *writePolicyTool) Risk() tool.Risk     { return t.inner.Risk() }

func (t *writePolicyTool) Execute(ctx context.Context, args json.RawMessage) (*tool.Result, error) {
	path, err := extractPathArg(args)
	if err != nil {
		return jsonError("file policy: failed to parse tool arguments"), nil
	}
	if strings.TrimSpace(path) != "" {
		if err := t.policy.CheckWrite(path); err != nil {
			emitPolicyDenied(ctx, "write", path, err)
			return jsonError(err.Error()), nil
		}
	}
	return t.inner.Execute(ctx, args)
}

type deletePolicyTool struct {
	inner  tool.Tool
	policy *executionpolicy.FilePolicy
}

func (t *deletePolicyTool) Name() string        { return t.inner.Name() }
func (t *deletePolicyTool) Schema() tool.Schema { return t.inner.Schema() }
func (t *deletePolicyTool) Risk() tool.Risk     { return t.inner.Risk() }

func (t *deletePolicyTool) Execute(ctx context.Context, args json.RawMessage) (*tool.Result, error) {
	path, err := extractPathArg(args)
	if err != nil {
		return jsonError("file policy: failed to parse tool arguments"), nil
	}
	if strings.TrimSpace(path) != "" {
		if err := t.policy.CheckDelete(path); err != nil {
			emitPolicyDenied(ctx, "delete", path, err)
			return jsonError(err.Error()), nil
		}
	}
	return t.inner.Execute(ctx, args)
}

type readPolicyTool struct {
	inner  tool.Tool
	policy *executionpolicy.FilePolicy
}

func (t *readPolicyTool) Name() string        { return t.inner.Name() }
func (t *readPolicyTool) Schema() tool.Schema { return t.inner.Schema() }
func (t *readPolicyTool) Risk() tool.Risk     { return t.inner.Risk() }

func (t *readPolicyTool) Execute(ctx context.Context, args json.RawMessage) (*tool.Result, error) {
	if strings.HasSuffix(t.inner.Name(), ".search") {
		return t.executeSearch(ctx, args)
	}

	path, err := extractPathArg(args)
	if err != nil {
		return jsonError("file policy: failed to parse tool arguments"), nil
	}
	if strings.TrimSpace(path) != "" {
		if err := t.policy.CheckRead(path); err != nil {
			emitPolicyDenied(ctx, "read", path, err)
			return jsonError(err.Error()), nil
		}
	}
	return t.inner.Execute(ctx, args)
}

func (t *readPolicyTool) executeSearch(ctx context.Context, args json.RawMessage) (*tool.Result, error) {
	searchPath := "."
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(args, &fields); err != nil {
		return jsonError("file policy: failed to parse tool arguments"), nil
	}
	if rawPath, ok := fields["path"]; ok {
		_ = json.Unmarshal(rawPath, &searchPath)
	}

	if err := t.policy.CheckSearchPath(searchPath); err != nil {
		emitPolicyDenied(ctx, "search", searchPath, err)
		return jsonError(err.Error()), nil
	}

	result, err := t.inner.Execute(ctx, args)
	if err != nil || result == nil || result.IsError {
		return result, err
	}

	filtered, filterErr := filterSearchOutput(result.Output, t.policy.Root(), searchPath, t.policy)
	if filterErr != nil {
		return jsonError(filterErr.Error()), nil
	}
	result.Output = filtered
	return result, nil
}

func extractPathArg(args json.RawMessage) (string, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(args, &fields); err != nil {
		return "", err
	}
	rawPath, ok := fields["path"]
	if !ok {
		return "", nil
	}
	var path string
	if err := json.Unmarshal(rawPath, &path); err != nil {
		return "", err
	}
	return path, nil
}

func filterSearchOutput(raw json.RawMessage, root, searchPath string, policy *executionpolicy.FilePolicy) (json.RawMessage, error) {
	type match struct {
		File string `json:"file"`
		Line int    `json:"line"`
		Text string `json:"text"`
	}
	type searchOutput struct {
		Matches   []match `json:"matches"`
		Total     int     `json:"total"`
		Truncated bool    `json:"truncated"`
	}

	var out searchOutput
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}

	baseAbs := searchPath
	if !filepath.IsAbs(baseAbs) {
		baseAbs = filepath.Join(root, searchPath)
	}
	baseAbs = filepath.Clean(baseAbs)
	if info, err := os.Stat(baseAbs); err == nil && !info.IsDir() {
		baseAbs = filepath.Dir(baseAbs)
	}

	filtered := out.Matches[:0]
	for _, item := range out.Matches {
		abs := item.File
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(baseAbs, item.File)
		}
		if policy.CheckRead(abs) == nil {
			filtered = append(filtered, item)
		}
	}
	out.Matches = filtered
	out.Total = len(filtered)
	return json.Marshal(out)
}

func jsonError(msg string) *tool.Result {
	raw, _ := json.Marshal(msg)
	return &tool.Result{Output: raw, IsError: true}
}

func emitPolicyDenied(ctx context.Context, operation, path string, err error) {
	data, _ := json.Marshal(map[string]string{
		"operation": operation,
		"path":      path,
		"error":     err.Error(),
	})
	runtimeaudit.Emit(ctx, runtimeaudit.Event{
		Type:    "policy.denied",
		Message: err.Error(),
		Data:    data,
	})
}

var (
	_ tool.Tool = (*commandGuardTool)(nil)
	_ tool.Tool = (*writePolicyTool)(nil)
	_ tool.Tool = (*deletePolicyTool)(nil)
	_ tool.Tool = (*readPolicyTool)(nil)
)
