package toolguard

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/leef-l/brain/sdk/executionpolicy"
	"github.com/leef-l/brain/sdk/runtimeaudit"
	"github.com/leef-l/brain/sdk/tool"
)

// --- stubTool for testing ---

type stubTool struct {
	name   string
	result *tool.Result
	err    error
}

func (s *stubTool) Name() string        { return s.name }
func (s *stubTool) Schema() tool.Schema { return tool.Schema{Name: s.name} }
func (s *stubTool) Risk() tool.Risk     { return tool.RiskSafe }
func (s *stubTool) Execute(_ context.Context, _ json.RawMessage) (*tool.Result, error) {
	return s.result, s.err
}

func okResult() *tool.Result {
	return &tool.Result{Output: json.RawMessage(`"ok"`), IsError: false}
}

func tempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"src/main.go", "src/lib.go", "data/secret.txt"} {
		path := filepath.Join(dir, name)
		os.MkdirAll(filepath.Dir(path), 0o755)
		os.WriteFile(path, []byte("test"), 0o644)
	}
	return dir
}

func boolPtr(v bool) *bool { return &v }

// --- NewBoundaries ---

func TestNewBoundaries_Nil(t *testing.T) {
	b, err := NewBoundaries(nil)
	if err != nil {
		t.Fatal(err)
	}
	if b == nil {
		t.Fatal("expected non-nil Boundaries")
	}
	if b.Workdir == "" {
		t.Error("Workdir should default to cwd")
	}
	if b.Sandbox == nil {
		t.Error("Sandbox should be non-nil")
	}
}

func TestNewBoundaries_WithSpec(t *testing.T) {
	dir := tempDir(t)
	b, err := NewBoundaries(&executionpolicy.ExecutionSpec{
		Workdir: dir,
		FilePolicy: &executionpolicy.FilePolicySpec{
			AllowRead: []string{"src/**"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if b.FilePolicy == nil {
		t.Error("FilePolicy should be set")
	}
	if b.Workdir != dir {
		t.Errorf("Workdir = %q, want %q", b.Workdir, dir)
	}
}

func TestNewBoundaries_InvalidPattern(t *testing.T) {
	_, err := NewBoundaries(&executionpolicy.ExecutionSpec{
		Workdir: "/tmp",
		FilePolicy: &executionpolicy.FilePolicySpec{
			AllowRead: []string{"[invalid"},
		},
	})
	if err == nil {
		t.Error("expected error for invalid pattern")
	}
}

// --- WrapFilePolicy ---

func TestWrapFilePolicy_Nil(t *testing.T) {
	inner := &stubTool{name: "test.write", result: okResult()}
	wrapped := WrapFilePolicy(inner, nil)
	if wrapped != inner {
		t.Error("nil policy should return original tool")
	}
}

func TestWrapFilePolicy_Allowed(t *testing.T) {
	dir := tempDir(t)
	policy, _ := executionpolicy.NewFilePolicy(dir, &executionpolicy.FilePolicySpec{
		AllowEdit: []string{"src/**"},
	})
	inner := &stubTool{name: "code.write_file", result: okResult()}
	wrapped := WrapFilePolicy(inner, policy)

	args, _ := json.Marshal(map[string]string{"path": filepath.Join(dir, "src/main.go")})
	result, err := wrapped.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Errorf("expected success, got error: %s", result.Output)
	}
}

func TestWrapFilePolicy_Denied(t *testing.T) {
	dir := tempDir(t)
	policy, _ := executionpolicy.NewFilePolicy(dir, &executionpolicy.FilePolicySpec{
		AllowEdit: []string{"src/**"},
	})
	inner := &stubTool{name: "code.write_file", result: okResult()}
	wrapped := WrapFilePolicy(inner, policy)

	args, _ := json.Marshal(map[string]string{"path": filepath.Join(dir, "data/secret.txt")})
	result, err := wrapped.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Error("expected policy denial error")
	}
}

func TestWrapFilePolicy_Name(t *testing.T) {
	dir := tempDir(t)
	policy, _ := executionpolicy.NewFilePolicy(dir, &executionpolicy.FilePolicySpec{
		AllowEdit: []string{"**"},
	})
	inner := &stubTool{name: "code.write_file", result: okResult()}
	wrapped := WrapFilePolicy(inner, policy)
	if wrapped.Name() != "code.write_file" {
		t.Errorf("Name() = %q", wrapped.Name())
	}
}

// --- WrapDeletePolicy ---

func TestWrapDeletePolicy_Allowed(t *testing.T) {
	dir := tempDir(t)
	policy, _ := executionpolicy.NewFilePolicy(dir, &executionpolicy.FilePolicySpec{
		AllowDelete: []string{"data/**"},
	})
	inner := &stubTool{name: "code.delete_file", result: okResult()}
	wrapped := WrapDeletePolicy(inner, policy)

	args, _ := json.Marshal(map[string]string{"path": filepath.Join(dir, "data/secret.txt")})
	result, err := wrapped.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Errorf("expected success: %s", result.Output)
	}
}

func TestWrapDeletePolicy_Denied(t *testing.T) {
	dir := tempDir(t)
	policy, _ := executionpolicy.NewFilePolicy(dir, &executionpolicy.FilePolicySpec{
		AllowDelete: []string{"data/**"},
	})
	inner := &stubTool{name: "code.delete_file", result: okResult()}
	wrapped := WrapDeletePolicy(inner, policy)

	args, _ := json.Marshal(map[string]string{"path": filepath.Join(dir, "src/main.go")})
	result, err := wrapped.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Error("expected policy denial")
	}
}

// --- WrapReadPolicy ---

func TestWrapReadPolicy_Allowed(t *testing.T) {
	dir := tempDir(t)
	policy, _ := executionpolicy.NewFilePolicy(dir, &executionpolicy.FilePolicySpec{
		AllowRead: []string{"src/**"},
	})
	inner := &stubTool{name: "code.read_file", result: okResult()}
	wrapped := WrapReadPolicy(inner, policy)

	args, _ := json.Marshal(map[string]string{"path": filepath.Join(dir, "src/main.go")})
	result, err := wrapped.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Errorf("expected success: %s", result.Output)
	}
}

func TestWrapReadPolicy_Denied(t *testing.T) {
	dir := tempDir(t)
	policy, _ := executionpolicy.NewFilePolicy(dir, &executionpolicy.FilePolicySpec{
		AllowRead: []string{"src/**"},
	})
	inner := &stubTool{name: "code.read_file", result: okResult()}
	wrapped := WrapReadPolicy(inner, policy)

	args, _ := json.Marshal(map[string]string{"path": filepath.Join(dir, "data/secret.txt")})
	result, err := wrapped.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Error("expected policy denial")
	}
}

// --- WrapCommandPolicy ---

func TestWrapCommandPolicy_CommandsDenied(t *testing.T) {
	dir := tempDir(t)
	policy, _ := executionpolicy.NewFilePolicy(dir, &executionpolicy.FilePolicySpec{
		AllowRead:     []string{"**"},
		AllowCommands: boolPtr(false),
	})
	inner := &stubTool{name: "code.shell_exec", result: okResult()}
	sandbox := &stubCommandSandbox{available: true}
	cfg := &tool.SandboxConfig{Enabled: true}
	wrapped := WrapCommandPolicy(inner, sandbox, cfg, policy)

	result, err := wrapped.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Error("expected command denial")
	}
}

func TestWrapCommandPolicy_SandboxUnavailable(t *testing.T) {
	inner := &stubTool{name: "code.shell_exec", result: okResult()}
	sandbox := &stubCommandSandbox{available: false}
	cfg := &tool.SandboxConfig{Enabled: true, FailIfUnavailable: true}
	wrapped := WrapCommandPolicy(inner, sandbox, cfg, nil)

	result, err := wrapped.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Error("expected error when sandbox unavailable")
	}
}

type stubCommandSandbox struct {
	available bool
}

func (s *stubCommandSandbox) Available() bool { return s.available }
func (s *stubCommandSandbox) Run(_ context.Context, _ string, _ string, _, _ io.Writer) (int, error) {
	return 0, nil
}

// --- Audit events ---

func TestEmitPolicyDenied(t *testing.T) {
	var events []runtimeaudit.Event
	sink := runtimeaudit.SinkFunc(func(_ context.Context, ev runtimeaudit.Event) {
		events = append(events, ev)
	})
	ctx := runtimeaudit.WithSink(context.Background(), sink)

	dir := tempDir(t)
	policy, _ := executionpolicy.NewFilePolicy(dir, &executionpolicy.FilePolicySpec{
		AllowRead: []string{"src/**"},
	})
	inner := &stubTool{name: "code.read_file", result: okResult()}
	wrapped := WrapReadPolicy(inner, policy)

	args, _ := json.Marshal(map[string]string{"path": filepath.Join(dir, "data/secret.txt")})
	wrapped.Execute(ctx, args)

	if len(events) != 1 {
		t.Fatalf("expected 1 audit event, got %d", len(events))
	}
	if events[0].Type != "policy.denied" {
		t.Errorf("event type = %q", events[0].Type)
	}
}

// --- Interface compliance ---

func TestInterfaceCompliance(t *testing.T) {
	var _ tool.Tool = (*commandGuardTool)(nil)
	var _ tool.Tool = (*writePolicyTool)(nil)
	var _ tool.Tool = (*deletePolicyTool)(nil)
	var _ tool.Tool = (*readPolicyTool)(nil)
}

// --- extractPathArg ---

func TestExtractPathArg(t *testing.T) {
	args := json.RawMessage(`{"path":"/tmp/file.go","content":"hello"}`)
	path, err := extractPathArg(args)
	if err != nil {
		t.Fatal(err)
	}
	if path != "/tmp/file.go" {
		t.Errorf("path = %q", path)
	}
}

func TestExtractPathArg_NoPath(t *testing.T) {
	args := json.RawMessage(`{"content":"hello"}`)
	path, err := extractPathArg(args)
	if err != nil {
		t.Fatal(err)
	}
	if path != "" {
		t.Errorf("path = %q, want empty", path)
	}
}

func TestExtractPathArg_InvalidJSON(t *testing.T) {
	_, err := extractPathArg(json.RawMessage(`invalid`))
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

// --- jsonError ---

func TestJsonError(t *testing.T) {
	result := jsonError("test error")
	if !result.IsError {
		t.Error("expected IsError=true")
	}
	if result.Output == nil {
		t.Error("expected non-nil output")
	}
}
