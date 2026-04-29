package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/leef-l/brain/sdk/runtimeaudit"
	"github.com/leef-l/brain/sdk/tool"
	"github.com/leef-l/brain/sdk/toolguard"
)

func normalizeRoot(t *testing.T, root string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", root, err)
	}
	return resolved
}

func probeReadCommand() string {
	if runtime.GOOS == "windows" {
		return `if exist blocked.txt (echo visible) else (echo missing)`
	}
	return `if [ -e blocked.txt ]; then echo visible; else echo missing; fi`
}

func probeEditCommand() string {
	if runtime.GOOS == "windows" {
		// Use PowerShell with -EncodedCommand to avoid cmd quote-escaping issues.
		// Script: 'hidden'; [System.IO.File]::WriteAllText('hidden.txt', 'rewritten')
		return `if exist hidden.txt (findstr /M "." hidden.txt >nul && echo visible || echo hidden) else (echo hidden) & powershell -NoProfile -EncodedCommand WwBTAHkAcwB0AGUAbQAuAEkATwAuAEYAaQBsAGUAXQA6ADoAVwByAGkAdABlAEEAbABsAFQAZQB4AHQAKAAnAGgAaQBkAGQAZQBuAC4AdAB4AHQAJwAsACAAJwByAGUAdwByAGkAdAB0AGUAbgAnACkA`
	}
	return `if [ -s hidden.txt ]; then echo visible; else echo hidden; fi; printf rewritten > hidden.txt`
}

func normalizeStdout(s string) string {
	return strings.TrimSuffix(s, "\r")
}

func TestFilePolicy_AllowsEditButBlocksCreate(t *testing.T) {
	root := normalizeRoot(t, t.TempDir())
	existing := filepath.Join(root, "allowed.go")
	if err := os.WriteFile(existing, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	policy, err := newFilePolicy(root, &filePolicyInput{
		AllowEdit:   []string{"*.go"},
		AllowCreate: []string{"docs/*.md"},
	})
	if err != nil {
		t.Fatalf("newFilePolicy: %v", err)
	}
	if err := policy.CheckWrite(existing); err != nil {
		t.Fatalf("edit existing: %v", err)
	}
	if err := policy.CheckWrite(filepath.Join(root, "new.txt")); err == nil {
		t.Fatal("expected create denial for new.txt")
	}
}

func TestFilePolicy_ValidatesCommandDiffs(t *testing.T) {
	root := normalizeRoot(t, t.TempDir())
	env := newExecutionEnvironment(root, modeAuto, nil, nil, false)
	if err := applyFilePolicy(env, &filePolicyInput{AllowEdit: []string{"allowed.txt"}}); err != nil {
		t.Fatalf("applyFilePolicy: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "blocked.txt"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	st := tool.NewShellExecTool("code", env.Sandbox)
	st.SetCommandSandbox(env.CmdSandbox)
	cmd := toolguard.WrapCommandPolicy(tool.WrapSandbox(st, env.Sandbox), env.CmdSandbox, env.SandboxCfg, env.FilePolicy)
	var eventTypes []string
	ctx := runtimeaudit.WithSink(context.Background(), runtimeaudit.SinkFunc(func(_ context.Context, ev runtimeaudit.Event) {
		eventTypes = append(eventTypes, ev.Type)
	}))
	result, err := cmd.Execute(ctx, json.RawMessage(`{"command":"echo hacked > blocked.txt"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected command diff violation when file policy is active")
	}
	got, err := os.ReadFile(filepath.Join(root, "blocked.txt"))
	if err != nil {
		t.Fatalf("read blocked.txt after rollback: %v", err)
	}
	if string(got) != "old" {
		t.Fatalf("blocked.txt=%q, want original content", string(got))
	}
	if len(eventTypes) == 0 || eventTypes[len(eventTypes)-1] != "policy.command.rollback" {
		t.Fatalf("expected rollback audit event, got %v", eventTypes)
	}
}

func TestFilePolicy_ReadDeleteAndCommandFlags(t *testing.T) {
	root := normalizeRoot(t, t.TempDir())
	allowed := filepath.Join(root, "allowed.txt")
	blocked := filepath.Join(root, "blocked.txt")
	if err := os.WriteFile(allowed, []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blocked, []byte("no"), 0o644); err != nil {
		t.Fatal(err)
	}

	allowCommands := false
	env := newExecutionEnvironment(root, modeRestricted, nil, nil, false)
	if err := applyFilePolicy(env, &filePolicyInput{
		AllowRead:     []string{"allowed.txt"},
		AllowDelete:   []string{"allowed.txt"},
		AllowCommands: &allowCommands,
	}); err != nil {
		t.Fatalf("applyFilePolicy: %v", err)
	}

	readTool := manageTool(env, tool.NewReadFileTool("code"), toolClassRead)
	if res, err := readTool.Execute(context.Background(), json.RawMessage(fmtJSON(map[string]string{"path": allowed}))); err != nil || res.IsError {
		t.Fatalf("allowed read: res=%v err=%v", res, err)
	}
	if res, err := readTool.Execute(context.Background(), json.RawMessage(fmtJSON(map[string]string{"path": blocked}))); err != nil {
		t.Fatalf("blocked read err: %v", err)
	} else if !res.IsError {
		t.Fatal("expected blocked read to be denied")
	}

	deleteTool := manageTool(env, tool.NewDeleteFileTool("code"), toolClassDelete)
	if res, err := deleteTool.Execute(context.Background(), json.RawMessage(fmtJSON(map[string]string{"path": blocked}))); err != nil {
		t.Fatalf("blocked delete err: %v", err)
	} else if !res.IsError {
		t.Fatal("expected blocked delete to be denied")
	}
	if _, err := os.Stat(blocked); err != nil {
		t.Fatalf("blocked.txt should still exist: %v", err)
	}

	st := tool.NewShellExecTool("code", env.Sandbox)
	st.SetCommandSandbox(env.CmdSandbox)
	cmd := toolguard.WrapCommandPolicy(tool.WrapSandbox(st, env.Sandbox), env.CmdSandbox, env.SandboxCfg, env.FilePolicy)
	var eventTypes []string
	ctx := runtimeaudit.WithSink(context.Background(), runtimeaudit.SinkFunc(func(_ context.Context, ev runtimeaudit.Event) {
		eventTypes = append(eventTypes, ev.Type)
	}))
	result, err := cmd.Execute(ctx, json.RawMessage(`{"command":"echo ok"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !result.IsError || !strings.Contains(string(result.Output), "denied by file policy") {
		t.Fatalf("expected allow_commands denial, got %s", result.Output)
	}
	if len(eventTypes) == 0 || eventTypes[0] != "policy.command.denied" {
		t.Fatalf("expected command denial audit event, got %v", eventTypes)
	}
}

func TestFilePolicy_RestrictsCommandReadSurface(t *testing.T) {
	root := normalizeRoot(t, t.TempDir())
	if err := os.WriteFile(filepath.Join(root, "allowed.txt"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "blocked.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}

	allowCommands := true
	env := newExecutionEnvironment(root, modeRestricted, nil, nil, false)
	if err := applyFilePolicy(env, &filePolicyInput{
		AllowRead:     []string{"allowed.txt"},
		AllowCommands: &allowCommands,
	}); err != nil {
		t.Fatalf("applyFilePolicy: %v", err)
	}

	st := tool.NewShellExecTool("code", env.Sandbox)
	st.SetCommandSandbox(env.CmdSandbox)
	cmd := toolguard.WrapCommandPolicy(tool.WrapSandbox(st, env.Sandbox), env.CmdSandbox, env.SandboxCfg, env.FilePolicy)

	result, err := cmd.Execute(context.Background(), json.RawMessage(fmtJSON(map[string]string{"command": probeReadCommand()})))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected restricted command read probe to succeed, got %s", result.Output)
	}

	var out struct {
		Stdout string `json:"stdout"`
	}
	if err := json.Unmarshal(result.Output, &out); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	if normalizeStdout(out.Stdout) != "missing" {
		t.Fatalf("stdout=%q, want missing", out.Stdout)
	}
}

func TestFilePolicy_CommandCanBlindEditWithoutRead(t *testing.T) {
	root := normalizeRoot(t, t.TempDir())
	target := filepath.Join(root, "hidden.txt")
	if err := os.WriteFile(target, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}

	allowCommands := true
	env := newExecutionEnvironment(root, modeRestricted, nil, nil, false)
	if err := applyFilePolicy(env, &filePolicyInput{
		AllowEdit:     []string{"hidden.txt"},
		AllowCommands: &allowCommands,
	}); err != nil {
		t.Fatalf("applyFilePolicy: %v", err)
	}

	st := tool.NewShellExecTool("code", env.Sandbox)
	st.SetCommandSandbox(env.CmdSandbox)
	cmd := toolguard.WrapCommandPolicy(tool.WrapSandbox(st, env.Sandbox), env.CmdSandbox, env.SandboxCfg, env.FilePolicy)

	result, err := cmd.Execute(context.Background(), json.RawMessage(fmtJSON(map[string]string{"command": probeEditCommand()})))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected blind edit command to succeed, got %s", result.Output)
	}

	var out struct {
		Stdout string `json:"stdout"`
	}
	if err := json.Unmarshal(result.Output, &out); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	if normalizeStdout(out.Stdout) != "hidden" {
		t.Fatalf("stdout=%q, want hidden", out.Stdout)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read hidden.txt: %v", err)
	}
	if string(got) != "rewritten" {
		t.Fatalf("hidden.txt=%q, want rewritten", string(got))
	}
}

func TestRegisterToolsForMode_AppliesFilePolicyToChatWrites(t *testing.T) {
	root := normalizeRoot(t, t.TempDir())
	env := newExecutionEnvironment(root, modeAcceptEdits, nil, nil, true)
	if err := applyFilePolicy(env, &filePolicyInput{AllowEdit: []string{"*.go"}}); err != nil {
		t.Fatalf("applyFilePolicy: %v", err)
	}

	reg := tool.NewMemRegistry()
	registerToolsForMode(reg, modeAcceptEdits, "code", env, nil)

	writeTool, ok := reg.Lookup("code.write_file")
	if !ok {
		t.Fatal("code.write_file not registered")
	}

	result, err := writeTool.Execute(context.Background(), json.RawMessage(`{"path":"notes.txt","content":"hello"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected file creation denial for notes.txt")
	}
}

func TestRegisterDelegateToolForEnvironment_RespectsAllowDelegateFlag(t *testing.T) {
	orch := testOrchestratorWithCodeSidecar(t)
	env := newExecutionEnvironment(t.TempDir(), modeAcceptEdits, nil, nil, false)
	allowDelegate := false
	if err := applyFilePolicy(env, &filePolicyInput{
		AllowEdit:     []string{"*.go"},
		AllowDelegate: &allowDelegate,
	}); err != nil {
		t.Fatalf("applyFilePolicy: %v", err)
	}

	reg := tool.NewMemRegistry()
	registerDelegateToolForEnvironment(reg, orch, env)
	if _, ok := reg.Lookup("central.delegate"); ok {
		t.Fatal("central.delegate should not be registered when allow_delegate=false")
	}
}
