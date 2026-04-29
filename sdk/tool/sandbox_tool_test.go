package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type captureTool struct {
	lastArgs json.RawMessage
}

func (t *captureTool) Name() string { return "test.capture" }

func (t *captureTool) Schema() Schema {
	return Schema{Name: t.Name()}
}

func (t *captureTool) Risk() Risk { return RiskSafe }

func (t *captureTool) Execute(_ context.Context, args json.RawMessage) (*Result, error) {
	t.lastArgs = append(json.RawMessage(nil), args...)
	return &Result{Output: jsonStr("ok")}, nil
}

func TestWrapSandbox_NilPassthrough(t *testing.T) {
	inner := NewEchoTool("test")
	wrapped := WrapSandbox(inner, nil)

	// With nil sandbox, should return the inner tool directly.
	if wrapped != inner {
		t.Error("WrapSandbox(t, nil) should return t unchanged")
	}
}

func TestSandboxTool_AllowedPath(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "hello.txt")
	os.WriteFile(filePath, []byte("content\n"), 0644)

	sb := NewSandbox(dir)
	inner := NewReadFileTool("code")
	wrapped := WrapSandbox(inner, sb)

	args, _ := json.Marshal(map[string]string{"path": filePath})
	result, err := wrapped.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success for path inside sandbox, got error: %s", result.Output)
	}
}

func TestSandboxTool_BlockedPath(t *testing.T) {
	dir := t.TempDir()
	sb := NewSandbox(dir)
	inner := NewReadFileTool("code")
	wrapped := WrapSandbox(inner, sb)

	// Try to read a path outside the sandbox.
	blockedDir := t.TempDir()
	blockedFile := filepath.Join(blockedDir, "blocked.txt")
	os.WriteFile(blockedFile, []byte("blocked"), 0644)
	args, _ := json.Marshal(map[string]string{"path": blockedFile})
	result, err := wrapped.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected sandbox error for path outside sandbox")
	}
	if !strings.Contains(string(result.Output), "sandbox") {
		t.Errorf("error should mention sandbox: %s", result.Output)
	}
}

func TestSandboxTool_BlockedWorkingDir(t *testing.T) {
	dir := t.TempDir()
	sb := NewSandbox(dir)
	inner := NewShellExecTool("code", nil)
	wrapped := WrapSandbox(inner, sb)

	// Try to use a working_dir outside the sandbox.
	blockedDir := t.TempDir()
	args, _ := json.Marshal(map[string]interface{}{
		"command":     "echo hello",
		"working_dir": blockedDir,
	})
	result, err := wrapped.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected sandbox error for working_dir outside sandbox")
	}
	if !strings.Contains(string(result.Output), "sandbox") {
		t.Errorf("error should mention sandbox: %s", result.Output)
	}
}

func TestSandboxTool_AllowedWorkingDir(t *testing.T) {
	dir := t.TempDir()
	sb := NewSandbox(dir)
	inner := NewShellExecTool("code", nil)
	wrapped := WrapSandbox(inner, sb)

	args, _ := json.Marshal(map[string]interface{}{
		"command":     "echo ok",
		"working_dir": dir,
	})
	result, err := wrapped.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success for working_dir inside sandbox, got error: %s", result.Output)
	}
}

func TestSandboxTool_AuthorizedExtraDir(t *testing.T) {
	dir := t.TempDir()
	extraDir := t.TempDir()
	extraFile := filepath.Join(extraDir, "extra.txt")
	os.WriteFile(extraFile, []byte("extra content\n"), 0644)

	sb := NewSandbox(dir)
	sb.Authorize(extraDir)

	inner := NewReadFileTool("code")
	wrapped := WrapSandbox(inner, sb)

	args, _ := json.Marshal(map[string]string{"path": extraFile})
	result, err := wrapped.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success for authorized extra dir, got error: %s", result.Output)
	}
}

func TestSandboxTool_RelativePath(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "sub", "file.txt")
	os.MkdirAll(filepath.Join(dir, "sub"), 0755)
	os.WriteFile(filePath, []byte("hi\n"), 0644)

	sb := NewSandbox(dir)
	inner := NewReadFileTool("code")
	wrapped := WrapSandbox(inner, sb)

	// Relative paths are resolved against the sandbox primary dir.
	// Since we can't control cwd in tests easily, use an absolute path that IS inside.
	args, _ := json.Marshal(map[string]string{"path": filePath})
	result, err := wrapped.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Output)
	}
}

func TestSandboxTool_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	sb := NewSandbox(dir)
	inner := NewEchoTool("test")
	wrapped := WrapSandbox(inner, sb)

	// Pass invalid JSON — should be rejected.
	result, err := wrapped.Execute(context.Background(), json.RawMessage(`not json`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected error for invalid JSON args")
	}
	if !strings.Contains(string(result.Output), "sandbox") {
		t.Errorf("error should mention sandbox: %s", result.Output)
	}
}

func TestSandboxTool_EmptyPathUsesSandboxPrimary(t *testing.T) {
	dir := t.TempDir()
	sb := NewSandbox(dir)
	inner := &captureTool{}
	wrapped := WrapSandbox(inner, sb)

	args, _ := json.Marshal(map[string]interface{}{"pattern": "foo", "path": ""})
	result, err := wrapped.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Output)
	}

	var got map[string]string
	if err := json.Unmarshal(inner.lastArgs, &got); err != nil {
		t.Fatalf("decode forwarded args: %v", err)
	}
	if got["path"] != sb.Primary() {
		t.Fatalf("forwarded path=%q, want sandbox primary %q", got["path"], sb.Primary())
	}
}

func TestSandboxTool_PreservesNameSchemaRisk(t *testing.T) {
	dir := t.TempDir()
	sb := NewSandbox(dir)
	inner := NewReadFileTool("code")
	wrapped := WrapSandbox(inner, sb)

	if wrapped.Name() != inner.Name() {
		t.Errorf("Name() = %q, want %q", wrapped.Name(), inner.Name())
	}
	if wrapped.Schema().Name != inner.Schema().Name {
		t.Errorf("Schema().Name = %q, want %q", wrapped.Schema().Name, inner.Schema().Name)
	}
	if wrapped.Risk() != inner.Risk() {
		t.Errorf("Risk() = %v, want %v", wrapped.Risk(), inner.Risk())
	}
}
