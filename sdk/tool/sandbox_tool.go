package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
)

// sandboxPathKeys lists the JSON field names that contain single filesystem
// paths and must be validated against the sandbox.
var sandboxPathKeys = []string{"path", "working_dir"}

// sandboxMultiPathKeys lists JSON array fields that contain filesystem paths.
var sandboxMultiPathKeys = []string{"files"}

// SandboxApprover is called when a tool tries to access a path outside the
// sandbox. It receives the absolute path and the directory that needs
// authorization. If it returns true, the directory is authorized and
// execution proceeds. If false, execution is denied.
type SandboxApprover func(ctx context.Context, toolName string, absPath string, dir string) bool

// SandboxTool wraps a Tool and enforces sandbox path restrictions.
// Before delegating to the inner tool, it checks that any path-related
// arguments stay within the sandbox boundaries.
//
// When an Approver is set and a path escapes the sandbox, the Approver is
// called to let the user decide. Without an Approver, access is denied.
type SandboxTool struct {
	Inner    Tool
	Sandbox  *Sandbox
	Approver SandboxApprover
}

// WrapSandbox wraps a tool with sandbox path enforcement.
// If sandbox is nil, the tool is returned unwrapped (backward compatible).
func WrapSandbox(t Tool, sb *Sandbox) Tool {
	if sb == nil {
		return t
	}
	return &SandboxTool{Inner: t, Sandbox: sb}
}

// WrapSandboxWithApprover wraps a tool with sandbox enforcement plus an
// interactive approver for out-of-sandbox paths.
func WrapSandboxWithApprover(t Tool, sb *Sandbox, approver SandboxApprover) Tool {
	if sb == nil {
		return t
	}
	return &SandboxTool{Inner: t, Sandbox: sb, Approver: approver}
}

func (s *SandboxTool) Name() string   { return s.Inner.Name() }
func (s *SandboxTool) Schema() Schema { return s.Inner.Schema() }
func (s *SandboxTool) Risk() Risk     { return s.Inner.Risk() }

func (s *SandboxTool) Execute(ctx context.Context, args json.RawMessage) (*Result, error) {
	// Extract path fields from the arguments and validate them against the sandbox.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(args, &fields); err != nil {
		// If we cannot parse the arguments at all, reject — we cannot guarantee safety.
		return &Result{
			Output:  jsonStr("sandbox: failed to parse tool arguments"),
			IsError: true,
		}, nil
	}

	rewritten := false
	for _, key := range sandboxPathKeys {
		raw, ok := fields[key]
		if !ok {
			// If the "path" field is absent entirely, inject the sandbox
			// workdir so that inner tools default to workdir instead of cwd.
			if key == "path" {
				quoted, _ := json.Marshal(s.Sandbox.Primary())
				fields[key] = quoted
				rewritten = true
			}
			continue
		}
		var pathVal string
		if json.Unmarshal(raw, &pathVal) != nil {
			continue
		}
		if pathVal == "" && key == "path" {
			quoted, _ := json.Marshal(s.Sandbox.Primary())
			fields[key] = quoted
			rewritten = true
			continue
		}
		if pathVal == "" {
			continue
		}

		absPath, checkErr := s.Sandbox.Check(pathVal)
		if checkErr != nil {
			if sbErr, ok := checkErr.(*SandboxError); ok {
				dir := filepath.Dir(sbErr.Path)
				// If we have an approver, ask the user.
				if s.Approver != nil && s.Approver(ctx, s.Inner.Name(), absPath, dir) {
					s.Sandbox.Authorize(dir)
					// Rewrite relative path to absolute so inner tool uses workdir, not cwd.
					if !filepath.IsAbs(pathVal) {
						quoted, _ := json.Marshal(absPath)
						fields[key] = quoted
						rewritten = true
					}
					continue
				}
				return &Result{
					Output: jsonStr(fmt.Sprintf(
						"sandbox: path %q is outside the allowed directories.\n"+
							"Working directory: %s\n"+
							"To access %q, the user must authorize it first.",
						sbErr.Path, s.Sandbox.Primary(), dir,
					)),
					IsError: true,
				}, nil
			}
			// Non-SandboxError (shouldn't happen, but be safe).
			return &Result{
				Output:  jsonStr(fmt.Sprintf("sandbox: path check failed: %v", checkErr)),
				IsError: true,
			}, nil
		}
		// Rewrite relative path to absolute so inner tool resolves against
		// the sandbox workdir rather than the process cwd.
		if !filepath.IsAbs(pathVal) {
			quoted, _ := json.Marshal(absPath)
			fields[key] = quoted
			rewritten = true
		}
	}

	for _, key := range sandboxMultiPathKeys {
		raw, ok := fields[key]
		if !ok {
			continue
		}
		var paths []string
		if json.Unmarshal(raw, &paths) != nil {
			continue
		}
		changed := false
		for i, pathVal := range paths {
			if pathVal == "" {
				continue
			}
			absPath, checkErr := s.Sandbox.Check(pathVal)
			if checkErr != nil {
				if sbErr, ok := checkErr.(*SandboxError); ok {
					dir := filepath.Dir(sbErr.Path)
					if s.Approver != nil && s.Approver(ctx, s.Inner.Name(), absPath, dir) {
						s.Sandbox.Authorize(dir)
						if !filepath.IsAbs(pathVal) {
							paths[i] = absPath
							changed = true
						}
						continue
					}
					return &Result{
						Output: jsonStr(fmt.Sprintf(
							"sandbox: path %q is outside the allowed directories.\n"+
								"Working directory: %s\n"+
								"To access %q, the user must authorize it first.",
							sbErr.Path, s.Sandbox.Primary(), dir,
						)),
						IsError: true,
					}, nil
				}
				return &Result{
					Output:  jsonStr(fmt.Sprintf("sandbox: path check failed: %v", checkErr)),
					IsError: true,
				}, nil
			}
			if !filepath.IsAbs(pathVal) {
				paths[i] = absPath
				changed = true
			}
		}
		if changed {
			quoted, _ := json.Marshal(paths)
			fields[key] = quoted
			rewritten = true
		}
	}

	// If any paths were rewritten, re-marshal the arguments so the inner
	// tool sees absolute paths based on the sandbox workdir.
	finalArgs := args
	if rewritten {
		finalArgs, _ = json.Marshal(fields)
	}
	return s.Inner.Execute(ctx, finalArgs)
}
