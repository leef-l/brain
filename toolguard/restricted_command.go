package toolguard

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/leef-l/brain/executionpolicy"
	"github.com/leef-l/brain/runtimeaudit"
	"github.com/leef-l/brain/tool"
)

type fileSnapshotEntry struct {
	Kind       string
	Mode       fs.FileMode
	Hash       string
	Content    []byte
	LinkTarget string
}

type fileMutation struct {
	Kind string `json:"kind"`
	Path string `json:"path"`
}

type restrictedCommandWorkspace struct {
	realRoot    string
	tempRoot    string
	tempWorkdir string
	tempSandbox *tool.Sandbox
	cmdSandbox  tool.CommandSandbox
	before      map[string]fileSnapshotEntry
}

func validateCommandMutations(ctx context.Context, root string, policy *executionpolicy.FilePolicy, exec func(context.Context) (*tool.Result, error)) (*tool.Result, error) {
	if policy == nil || !policy.Enabled() {
		return exec(ctx)
	}

	before, err := snapshotTree(root)
	if err != nil {
		return nil, err
	}
	result, execErr := exec(ctx)
	after, snapErr := snapshotTree(root)
	if snapErr != nil {
		return nil, snapErr
	}

	if violations := diffViolations(root, policy, before, after); len(violations) > 0 {
		rolledBack, rollbackErr := rollbackViolations(root, before, after, violations)
		payload, _ := json.Marshal(map[string]interface{}{
			"error":       "command mutated files outside restricted policy",
			"violations":  violations,
			"rolled_back": rolledBack,
		})
		if rollbackErr != nil {
			payload, _ = json.Marshal(map[string]interface{}{
				"error":          "command mutated files outside restricted policy",
				"violations":     violations,
				"rolled_back":    rolledBack,
				"rollback_error": rollbackErr.Error(),
			})
		}
		runtimeaudit.Emit(ctx, runtimeaudit.Event{
			Type:    "policy.command.rollback",
			Message: "command produced unauthorized file mutations",
			Data:    payload,
		})
		return &tool.Result{Output: payload, IsError: true}, nil
	}
	return result, execErr
}

func executeRestrictedCommand(ctx context.Context, toolName string, args json.RawMessage, policy *executionpolicy.FilePolicy, cfg *tool.SandboxConfig) (*tool.Result, error) {
	if policy == nil || !policy.Enabled() {
		return nil, fmt.Errorf("restricted command execution requires an enabled file policy")
	}

	req, err := tool.ParseCommandRequest(args)
	if err != nil {
		return jsonError(fmt.Sprintf("invalid arguments: %v", err)), nil
	}
	if err := tool.ValidateCommandRequest(req); err != nil {
		return jsonError(err.Error()), nil
	}
	req = tool.NormalizeCommandRequest(toolName, req)

	workspace, err := prepareRestrictedCommandWorkspace(policy, req.WorkingDir, cfg)
	if err != nil {
		payload, _ := json.Marshal(map[string]interface{}{
			"reason": "restricted_workspace",
			"error":  err.Error(),
		})
		runtimeaudit.Emit(ctx, runtimeaudit.Event{
			Type:    "policy.command.denied",
			Message: "command execution denied by restricted workspace policy",
			Data:    payload,
		})
		return jsonError(err.Error()), nil
	}
	defer os.RemoveAll(workspace.tempRoot)

	req.WorkingDir = workspace.tempWorkdir
	outcome, execErr := tool.ExecuteCommandRequest(ctx, req, workspace.tempSandbox, workspace.cmdSandbox)
	if execErr != nil {
		prefix := "exec error"
		if workspace.cmdSandbox != nil && workspace.cmdSandbox.Available() {
			prefix = "sandbox error"
		}
		raw, _ := json.Marshal(fmt.Sprintf("%s: %v", prefix, execErr))
		return &tool.Result{Output: raw, IsError: true}, nil
	}

	after, err := snapshotTree(workspace.tempRoot)
	if err != nil {
		return nil, err
	}
	if violations := diffViolations(workspace.realRoot, policy, workspace.before, after); len(violations) > 0 {
		payload, _ := json.Marshal(map[string]interface{}{
			"error":       "command mutated files outside restricted policy",
			"violations":  violations,
			"rolled_back": true,
		})
		runtimeaudit.Emit(ctx, runtimeaudit.Event{
			Type:    "policy.command.rollback",
			Message: "command produced unauthorized file mutations",
			Data:    payload,
		})
		return &tool.Result{Output: payload, IsError: true}, nil
	}

	if err := syncAllowedMutations(workspace.realRoot, workspace.before, after); err != nil {
		return nil, err
	}

	return tool.ResultForCommandTool(toolName, outcome), nil
}

func prepareRestrictedCommandWorkspace(policy *executionpolicy.FilePolicy, requestedWorkdir string, cfg *tool.SandboxConfig) (*restrictedCommandWorkspace, error) {
	realRoot := policy.Root()
	tempRoot, err := os.MkdirTemp("", "brain-restricted-*")
	if err != nil {
		return nil, fmt.Errorf("create restricted workspace: %w", err)
	}
	cleanup := func(err error) (*restrictedCommandWorkspace, error) {
		_ = os.RemoveAll(tempRoot)
		return nil, err
	}

	if err := materializeRestrictedWorkspace(tempRoot, realRoot, policy); err != nil {
		return cleanup(err)
	}

	tempWorkdir, err := resolveRestrictedCommandWorkdir(realRoot, tempRoot, requestedWorkdir)
	if err != nil {
		return cleanup(err)
	}
	if err := os.MkdirAll(tempWorkdir, 0o755); err != nil {
		return cleanup(fmt.Errorf("create restricted working_dir: %w", err))
	}

	before, err := snapshotTree(tempRoot)
	if err != nil {
		return cleanup(err)
	}

	tempSandbox := tool.NewSandbox(tempRoot)
	cmdSandbox := tool.NewCommandSandbox(tempSandbox, cfg)
	if cmdSandbox == nil || !cmdSandbox.Available() {
		return cleanup(fmt.Errorf("OS-level command sandbox is unavailable"))
	}

	return &restrictedCommandWorkspace{
		realRoot:    realRoot,
		tempRoot:    tempRoot,
		tempWorkdir: tempWorkdir,
		tempSandbox: tempSandbox,
		cmdSandbox:  cmdSandbox,
		before:      before,
	}, nil
}

func materializeRestrictedWorkspace(tempRoot, realRoot string, policy *executionpolicy.FilePolicy) error {
	return filepath.WalkDir(realRoot, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == realRoot {
			return nil
		}

		readable := policy.CheckRead(path) == nil
		editable := policy.CheckWrite(path) == nil
		deletable := policy.CheckDelete(path) == nil
		if !readable && !editable && !deletable {
			return nil
		}

		rel, err := filepath.Rel(realRoot, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		tempPath := filepath.Join(tempRoot, filepath.FromSlash(rel))

		switch {
		case d.IsDir():
			return os.MkdirAll(tempPath, 0o755)
		case d.Type()&os.ModeSymlink != 0:
			if readable || editable {
				return fmt.Errorf("restricted command workspace does not expose symlink path: %s", rel)
			}
			return writePlaceholderFile(tempPath, 0o644)
		case d.Type().IsRegular():
			info, err := d.Info()
			if err != nil {
				return err
			}
			if readable {
				return copyWorkspaceFile(path, tempPath, info.Mode().Perm())
			}
			mode := info.Mode().Perm()
			if mode == 0 {
				mode = 0o644
			}
			return writePlaceholderFile(tempPath, mode)
		default:
			if readable || editable {
				return fmt.Errorf("restricted command workspace does not expose special file: %s", rel)
			}
			return writePlaceholderFile(tempPath, 0o644)
		}
	})
}

func resolveRestrictedCommandWorkdir(realRoot, tempRoot, requested string) (string, error) {
	abs := strings.TrimSpace(requested)
	if abs == "" {
		return tempRoot, nil
	}
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(realRoot, abs)
	}
	abs = filepath.Clean(abs)

	resolved := abs
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		resolved = real
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("resolve command working_dir: %w", err)
	} else {
		parent := filepath.Dir(abs)
		if realParent, parentErr := filepath.EvalSymlinks(parent); parentErr == nil {
			resolved = filepath.Join(realParent, filepath.Base(abs))
		}
	}

	rel, err := filepath.Rel(realRoot, resolved)
	if err != nil {
		return "", fmt.Errorf("resolve command working_dir: %w", err)
	}
	rel = filepath.ToSlash(rel)
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return "", fmt.Errorf("file policy denied: %q is outside workdir %s", abs, realRoot)
	}
	if rel == "." {
		return tempRoot, nil
	}
	return filepath.Join(tempRoot, filepath.FromSlash(rel)), nil
}

func copyWorkspaceFile(src, dst string, mode fs.FileMode) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if mode == 0 {
		mode = 0o644
	}
	return os.WriteFile(dst, data, mode)
}

func writePlaceholderFile(path string, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if mode == 0 {
		mode = 0o644
	}
	return os.WriteFile(path, nil, mode)
}

func syncAllowedMutations(root string, before, after map[string]fileSnapshotEntry) error {
	keys := make(map[string]struct{}, len(before)+len(after))
	for path := range before {
		keys[path] = struct{}{}
	}
	for path := range after {
		keys[path] = struct{}{}
	}

	paths := make([]string, 0, len(keys))
	for path := range keys {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	for _, rel := range paths {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		prev, hadPrev := before[rel]
		next, hasNext := after[rel]

		switch {
		case !hadPrev && hasNext:
			if err := restoreSnapshotEntry(abs, next); err != nil {
				return err
			}
		case hadPrev && !hasNext:
			if err := os.RemoveAll(abs); err != nil && !os.IsNotExist(err) {
				return err
			}
			cleanupEmptyParents(filepath.Dir(abs), root)
		case hadPrev && hasNext && !snapshotEqual(prev, next):
			if err := restoreSnapshotEntry(abs, next); err != nil {
				return err
			}
		}
	}

	return nil
}

func rollbackViolations(root string, before, after map[string]fileSnapshotEntry, violations []fileMutation) (bool, error) {
	if len(violations) == 0 {
		return false, nil
	}

	seen := make(map[string]fileMutation, len(violations))
	for _, item := range violations {
		seen[item.Path] = item
	}

	paths := make([]string, 0, len(seen))
	for path := range seen {
		paths = append(paths, path)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(paths)))

	for _, rel := range paths {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		prev, hadPrev := before[rel]
		_, hasNext := after[rel]

		switch {
		case !hadPrev && hasNext:
			if err := os.RemoveAll(abs); err != nil && !os.IsNotExist(err) {
				return false, err
			}
			cleanupEmptyParents(filepath.Dir(abs), root)
		case hadPrev && !hasNext:
			if err := restoreSnapshotEntry(abs, prev); err != nil {
				return false, err
			}
		case hadPrev && hasNext:
			if err := restoreSnapshotEntry(abs, prev); err != nil {
				return false, err
			}
		}
	}

	return true, nil
}

func snapshotTree(root string) (map[string]fileSnapshotEntry, error) {
	out := make(map[string]fileSnapshotEntry)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)

		info, err := d.Info()
		if err != nil {
			return err
		}

		if d.Type()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			sum := sha256.Sum256([]byte(target))
			out[rel] = fileSnapshotEntry{
				Kind:       "symlink",
				Mode:       info.Mode(),
				Hash:       hex.EncodeToString(sum[:]),
				LinkTarget: target,
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		out[rel] = fileSnapshotEntry{
			Kind:    "file",
			Mode:    info.Mode(),
			Hash:    hex.EncodeToString(sum[:]),
			Content: append([]byte(nil), data...),
		}
		return nil
	})
	return out, err
}

func diffViolations(root string, policy *executionpolicy.FilePolicy, before, after map[string]fileSnapshotEntry) []fileMutation {
	var violations []fileMutation

	appendViolation := func(kind, rel string, err error) {
		if err == nil {
			return
		}
		violations = append(violations, fileMutation{
			Kind: kind,
			Path: rel,
		})
	}

	keys := make(map[string]struct{}, len(before)+len(after))
	for path := range before {
		keys[path] = struct{}{}
	}
	for path := range after {
		keys[path] = struct{}{}
	}

	sorted := make([]string, 0, len(keys))
	for path := range keys {
		sorted = append(sorted, path)
	}
	sort.Strings(sorted)

	for _, rel := range sorted {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		prev, hadPrev := before[rel]
		next, hasNext := after[rel]
		switch {
		case !hadPrev && hasNext:
			if next.Kind != "file" {
				appendViolation("create", rel, fmt.Errorf("unsupported file type"))
				continue
			}
			appendViolation("create", rel, policy.CheckWrite(abs))
		case hadPrev && !hasNext:
			appendViolation("delete", rel, policy.CheckDelete(abs))
		case hadPrev && hasNext && !snapshotEqual(prev, next):
			if prev.Kind != "file" || next.Kind != "file" {
				appendViolation("edit", rel, fmt.Errorf("unsupported file type"))
				continue
			}
			appendViolation("edit", rel, policy.CheckWrite(abs))
		}
	}

	return violations
}

func snapshotEqual(a, b fileSnapshotEntry) bool {
	return a.Kind == b.Kind &&
		a.Mode == b.Mode &&
		a.Hash == b.Hash &&
		a.LinkTarget == b.LinkTarget &&
		bytes.Equal(a.Content, b.Content)
}

func restoreSnapshotEntry(path string, entry fileSnapshotEntry) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	if entry.Kind == "symlink" {
		_ = os.Remove(path)
		return os.Symlink(entry.LinkTarget, path)
	}

	mode := entry.Mode.Perm()
	if mode == 0 {
		mode = 0o644
	}
	if err := os.WriteFile(path, entry.Content, mode); err != nil {
		return err
	}
	if entry.Mode != 0 {
		if err := os.Chmod(path, entry.Mode.Perm()); err != nil {
			return err
		}
	}
	return nil
}

func cleanupEmptyParents(dir, root string) {
	root = filepath.Clean(root)
	for {
		dir = filepath.Clean(dir)
		if dir == root || dir == "." || dir == string(filepath.Separator) {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) > 0 {
			return
		}
		if err := os.Remove(dir); err != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}
