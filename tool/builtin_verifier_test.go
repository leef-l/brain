package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/leef-l/brain/executionpolicy"
	"github.com/leef-l/brain/protocol"
)

// ---------------------------------------------------------------------------
// verifier.read_file tests
// ---------------------------------------------------------------------------

func TestVerifierReadFile_Basic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "code.go")
	os.WriteFile(path, []byte("package main\nfunc main() {}\n"), 0644)

	tool := NewVerifierReadFileTool()
	args, _ := json.Marshal(map[string]string{"path": path})
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Output)
	}
	if tool.Name() != "verifier.read_file" {
		t.Errorf("Name()=%q", tool.Name())
	}
	if tool.Risk() != RiskSafe {
		t.Errorf("Risk()=%v", tool.Risk())
	}
}

func TestVerifierReadFile_NotFound(t *testing.T) {
	tool := NewVerifierReadFileTool()
	args, _ := json.Marshal(map[string]string{"path": "/nonexistent"})
	result, _ := tool.Execute(context.Background(), args)
	if !result.IsError {
		t.Error("expected error")
	}
}

// ---------------------------------------------------------------------------
// verifier.run_tests tests
// ---------------------------------------------------------------------------

func TestRunTests_Pass(t *testing.T) {
	tool := NewRunTestsTool()
	args, _ := json.Marshal(map[string]string{"command": "echo PASS"})
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}

	var out runTestsOutput
	json.Unmarshal(result.Output, &out)
	if !out.Passed {
		t.Error("expected passed=true")
	}
	if out.ExitCode != 0 {
		t.Errorf("exit_code=%d", out.ExitCode)
	}
	if result.IsError {
		t.Error("passed test should not be IsError")
	}
}

func TestRunTests_Fail(t *testing.T) {
	tool := NewRunTestsTool()
	args, _ := json.Marshal(map[string]string{"command": "exit 1"})
	result, _ := tool.Execute(context.Background(), args)

	var out runTestsOutput
	json.Unmarshal(result.Output, &out)
	if out.Passed {
		t.Error("expected passed=false")
	}
	if out.ExitCode != 1 {
		t.Errorf("exit_code=%d, want 1", out.ExitCode)
	}
	if !result.IsError {
		t.Error("failed test should be IsError")
	}
}

func TestRunTests_Timeout(t *testing.T) {
	tool := NewRunTestsTool()
	args, _ := json.Marshal(map[string]interface{}{"command": "sleep 10", "timeout_seconds": 1})
	result, _ := tool.Execute(context.Background(), args)

	var out runTestsOutput
	json.Unmarshal(result.Output, &out)
	if !out.TimedOut {
		t.Error("expected timed_out=true")
	}
	if out.Passed {
		t.Error("timed out should not be passed")
	}
}

func TestRunTests_EmptyCommand(t *testing.T) {
	tool := NewRunTestsTool()
	args, _ := json.Marshal(map[string]string{"command": ""})
	result, _ := tool.Execute(context.Background(), args)
	if !result.IsError {
		t.Error("expected error for empty command")
	}
}

func TestRunTests_Name(t *testing.T) {
	tool := NewRunTestsTool()
	if tool.Name() != "verifier.run_tests" {
		t.Errorf("Name()=%q", tool.Name())
	}
	if tool.Risk() != RiskMedium {
		t.Errorf("Risk()=%v", tool.Risk())
	}
}

type mockKernelCaller struct {
	method string
	params interface{}
	err    error
}

func (m *mockKernelCaller) CallKernel(_ context.Context, method string, params interface{}, result interface{}) error {
	m.method = method
	m.params = params
	if m.err != nil {
		return m.err
	}
	if out, ok := result.(*protocol.ToolCallResult); ok {
		*out = protocol.ToolCallResult{
			Tool:    "browser.screenshot",
			Output:  json.RawMessage(`{"status":"ok"}`),
			Content: []protocol.ToolCallContent{{Type: "text", Text: `{"status":"ok"}`}},
		}
	}
	return nil
}

func TestBrowserActionTool_UsesSpecialistCallTool(t *testing.T) {
	caller := &mockKernelCaller{}
	tool := NewBrowserActionTool()
	tool.SetKernelCaller(caller)
	tool.SetExecutionSpec(&executionpolicy.ExecutionSpec{
		Workdir: "/tmp/verifier",
		FilePolicy: &executionpolicy.FilePolicySpec{
			AllowRead: []string{"ui/**/*.png"},
		},
	})

	args, _ := json.Marshal(map[string]interface{}{
		"action": "screenshot",
		"params": map[string]interface{}{"selector": "#chart"},
	})
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Output)
	}
	if string(result.Output) != `{"status":"ok"}` {
		t.Fatalf("output=%s, want underlying tool JSON", result.Output)
	}
	if caller.method != protocol.MethodSpecialistCallTool {
		t.Fatalf("method=%q, want %q", caller.method, protocol.MethodSpecialistCallTool)
	}

	req, ok := caller.params.(protocol.SpecialistToolCallRequest)
	if !ok {
		t.Fatalf("params type=%T, want protocol.SpecialistToolCallRequest", caller.params)
	}
	if req.TargetKind != "browser" || req.ToolName != "browser.screenshot" {
		t.Fatalf("request=%+v", req)
	}
	if req.Execution == nil || req.Execution.Workdir != "/tmp/verifier" {
		t.Fatalf("execution=%+v", req.Execution)
	}
}

// ---------------------------------------------------------------------------
// verifier.check_output tests
// ---------------------------------------------------------------------------

func TestCheckOutput_Exact_Match(t *testing.T) {
	tool := NewCheckOutputTool()
	args, _ := json.Marshal(map[string]string{"actual": "hello", "expected": "hello"})
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}

	var out checkOutputOutput
	json.Unmarshal(result.Output, &out)
	if !out.Match {
		t.Error("expected match=true")
	}
	if result.IsError {
		t.Error("match should not be IsError")
	}
}

func TestCheckOutput_Exact_Mismatch(t *testing.T) {
	tool := NewCheckOutputTool()
	args, _ := json.Marshal(map[string]string{"actual": "hello", "expected": "world"})
	result, _ := tool.Execute(context.Background(), args)

	var out checkOutputOutput
	json.Unmarshal(result.Output, &out)
	if out.Match {
		t.Error("expected match=false")
	}
	if out.Diff == "" {
		t.Error("expected non-empty diff")
	}
	if !result.IsError {
		t.Error("mismatch should be IsError")
	}
}

func TestCheckOutput_Contains(t *testing.T) {
	tool := NewCheckOutputTool()
	args, _ := json.Marshal(map[string]string{"actual": "hello world", "expected": "world", "mode": "contains"})
	result, _ := tool.Execute(context.Background(), args)

	var out checkOutputOutput
	json.Unmarshal(result.Output, &out)
	if !out.Match {
		t.Error("expected match=true for contains")
	}
}

func TestCheckOutput_Contains_Miss(t *testing.T) {
	tool := NewCheckOutputTool()
	args, _ := json.Marshal(map[string]string{"actual": "hello", "expected": "xyz", "mode": "contains"})
	result, _ := tool.Execute(context.Background(), args)

	var out checkOutputOutput
	json.Unmarshal(result.Output, &out)
	if out.Match {
		t.Error("expected match=false")
	}
}

func TestCheckOutput_Regex(t *testing.T) {
	tool := NewCheckOutputTool()
	args, _ := json.Marshal(map[string]string{"actual": "error at line 42", "expected": `error at line \d+`, "mode": "regex"})
	result, _ := tool.Execute(context.Background(), args)

	var out checkOutputOutput
	json.Unmarshal(result.Output, &out)
	if !out.Match {
		t.Error("expected regex match")
	}
}

func TestCheckOutput_InvalidRegex(t *testing.T) {
	tool := NewCheckOutputTool()
	args, _ := json.Marshal(map[string]string{"actual": "x", "expected": "[invalid", "mode": "regex"})
	result, _ := tool.Execute(context.Background(), args)
	if !result.IsError {
		t.Error("expected error for invalid regex")
	}
}

func TestCheckOutput_UnknownMode(t *testing.T) {
	tool := NewCheckOutputTool()
	args, _ := json.Marshal(map[string]string{"actual": "x", "expected": "x", "mode": "fuzzy"})
	result, _ := tool.Execute(context.Background(), args)
	if !result.IsError {
		t.Error("expected error for unknown mode")
	}
}

func TestCheckOutput_Name(t *testing.T) {
	tool := NewCheckOutputTool()
	if tool.Name() != "verifier.check_output" {
		t.Errorf("Name()=%q", tool.Name())
	}
	if tool.Risk() != RiskSafe {
		t.Errorf("Risk()=%v", tool.Risk())
	}
}
