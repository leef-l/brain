package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/leef-l/brain/executionpolicy"
	"github.com/leef-l/brain/protocol"
)

// --- verifier.read_file ---

// VerifierReadFileTool is a read-only file reader for the VerifierBrain.
// It reuses ReadFileCore from builtin_read_file.go.
type VerifierReadFileTool struct{}

func NewVerifierReadFileTool() *VerifierReadFileTool { return &VerifierReadFileTool{} }

func (t *VerifierReadFileTool) Name() string { return "verifier.read_file" }

func (t *VerifierReadFileTool) Schema() Schema {
	return Schema{
		Name:        t.Name(),
		Description: "Read a file for verification purposes. Read-only, no side effects.",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "path": { "type": "string", "description": "Path to the file to read" },
    "offset": { "type": "integer", "description": "Start line (0-based). Default: 0" },
    "limit": { "type": "integer", "description": "Max lines. Default: 2000" }
  },
  "required": ["path"]
}`),
		OutputSchema: readFileOutputSchema,
		Brain:        "verifier",
	}
}

func (t *VerifierReadFileTool) Risk() Risk { return RiskSafe }

func (t *VerifierReadFileTool) Execute(ctx context.Context, args json.RawMessage) (*Result, error) {
	var input struct {
		Path   string `json:"path"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return &Result{Output: jsonStr(fmt.Sprintf("invalid arguments: %v", err)), IsError: true}, nil
	}
	if input.Path == "" {
		return &Result{Output: jsonStr("path is required"), IsError: true}, nil
	}
	if input.Limit <= 0 {
		input.Limit = 2000
	}
	return ReadFileCore(ctx, input.Path, input.Offset, input.Limit)
}

// --- verifier.run_tests ---

// RunTestsTool executes a test command and reports pass/fail.
type RunTestsTool struct {
	sandbox    *Sandbox
	cmdSandbox CommandSandbox
}

func NewRunTestsTool() *RunTestsTool { return &RunTestsTool{} }

func (t *RunTestsTool) SetSandbox(sb *Sandbox) {
	t.sandbox = sb
}

func (t *RunTestsTool) SetCommandSandbox(cs CommandSandbox) {
	t.cmdSandbox = cs
}

func (t *RunTestsTool) Name() string { return "verifier.run_tests" }

func (t *RunTestsTool) Schema() Schema {
	return Schema{
		Name:        t.Name(),
		Description: "Execute a test command (e.g. 'go test ./...') and report whether tests passed. Returns stdout, stderr, exit code, and a passed boolean.",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "command": { "type": "string", "description": "Test command to execute (passed to sh -c)" },
    "working_dir": { "type": "string", "description": "Working directory. Default: current directory" },
    "timeout_seconds": { "type": "integer", "description": "Max execution time. Default: 120, max: 300" }
  },
  "required": ["command"]
}`),
		OutputSchema: runTestsOutputSchema,
		Brain:        "verifier",
	}
}

func (t *RunTestsTool) Risk() Risk { return RiskMedium }

type runTestsOutput struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
	Passed   bool   `json:"passed"`
	TimedOut bool   `json:"timed_out"`
}

func (t *RunTestsTool) Execute(ctx context.Context, args json.RawMessage) (*Result, error) {
	req, err := ParseCommandRequest(args)
	if err != nil {
		return &Result{Output: jsonStr(fmt.Sprintf("invalid arguments: %v", err)), IsError: true}, nil
	}
	if err := ValidateCommandRequest(req); err != nil {
		return &Result{Output: jsonStr("command is required"), IsError: true}, nil
	}
	req = NormalizeCommandRequest(t.Name(), req)

	outcome, err := ExecuteCommandRequest(ctx, req, t.sandbox, t.cmdSandbox)
	if err != nil {
		prefix := "exec error"
		if t.cmdSandbox != nil && t.cmdSandbox.Available() {
			prefix = "sandbox error"
		}
		return &Result{Output: jsonStr(fmt.Sprintf("%s: %v", prefix, err)), IsError: true}, nil
	}
	return ResultForCommandTool(t.Name(), outcome), nil
}

// --- verifier.check_output ---

// CheckOutputTool compares actual output against expected, supporting
// exact, contains, and regex matching modes.
type CheckOutputTool struct{}

func NewCheckOutputTool() *CheckOutputTool { return &CheckOutputTool{} }

func (t *CheckOutputTool) Name() string { return "verifier.check_output" }

func (t *CheckOutputTool) Schema() Schema {
	return Schema{
		Name:        t.Name(),
		Description: "Check whether actual output matches expected output. Supports exact, contains, and regex modes.",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "actual": { "type": "string", "description": "The actual output to check" },
    "expected": { "type": "string", "description": "The expected output or pattern" },
    "mode": { "type": "string", "description": "Match mode: 'exact' (default), 'contains', or 'regex'" }
  },
  "required": ["actual", "expected"]
}`),
		OutputSchema: checkOutputResultSchema,
		Brain:        "verifier",
	}
}

func (t *CheckOutputTool) Risk() Risk { return RiskSafe }

type checkOutputInput struct {
	Actual   string `json:"actual"`
	Expected string `json:"expected"`
	Mode     string `json:"mode"`
}

type checkOutputOutput struct {
	Match bool   `json:"match"`
	Mode  string `json:"mode"`
	Diff  string `json:"diff,omitempty"`
}

func (t *CheckOutputTool) Execute(ctx context.Context, args json.RawMessage) (*Result, error) {
	var input checkOutputInput
	if err := json.Unmarshal(args, &input); err != nil {
		return &Result{Output: jsonStr(fmt.Sprintf("invalid arguments: %v", err)), IsError: true}, nil
	}

	if input.Mode == "" {
		input.Mode = "exact"
	}

	var match bool
	var diff string

	switch input.Mode {
	case "exact":
		match = input.Actual == input.Expected
		if !match {
			diff = simpleDiff(input.Expected, input.Actual)
		}
	case "contains":
		match = strings.Contains(input.Actual, input.Expected)
		if !match {
			diff = fmt.Sprintf("actual does not contain expected substring")
		}
	case "regex":
		re, err := regexp.Compile(input.Expected)
		if err != nil {
			return &Result{Output: jsonStr(fmt.Sprintf("invalid regex: %v", err)), IsError: true}, nil
		}
		match = re.MatchString(input.Actual)
		if !match {
			diff = fmt.Sprintf("actual does not match regex pattern")
		}
	default:
		return &Result{Output: jsonStr(fmt.Sprintf("unknown mode: %q (use exact, contains, or regex)", input.Mode)), IsError: true}, nil
	}

	out := checkOutputOutput{
		Match: match,
		Mode:  input.Mode,
		Diff:  diff,
	}
	raw, _ := json.Marshal(out)
	return &Result{Output: raw, IsError: !match}, nil
}

// simpleDiff returns a basic diff description for mismatched strings.
func simpleDiff(expected, actual string) string {
	if len(expected) > 100 {
		expected = expected[:100] + "..."
	}
	if len(actual) > 100 {
		actual = actual[:100] + "..."
	}
	return fmt.Sprintf("expected: %q\nactual:   %q", expected, actual)
}

// --- verifier.browser_action ---

// KernelCaller allows tools to make outbound RPC calls to the Kernel.
// This is a tool-package-local interface mirroring sidecar.KernelCaller
// to avoid import cycles.
type KernelCaller interface {
	CallKernel(ctx context.Context, method string, params interface{}, result interface{}) error
}

// BrowserActionTool allows the Verifier to request browser operations
// (click, type, screenshot, etc.) via the Kernel. The Kernel delegates
// to Browser Brain — Verifier never controls the browser directly.
//
// This keeps Verifier read-only in spirit: it requests actions through
// the Kernel rather than running browser tools itself.
type BrowserActionTool struct {
	caller    KernelCaller
	execution *executionpolicy.ExecutionSpec
}

func NewBrowserActionTool() *BrowserActionTool { return &BrowserActionTool{} }

// SetKernelCaller injects the outbound RPC capability. Must be called
// before Execute — typically by the sidecar runtime after RPC setup.
func (t *BrowserActionTool) SetKernelCaller(caller KernelCaller) {
	t.caller = caller
}

func (t *BrowserActionTool) SetExecutionSpec(spec *executionpolicy.ExecutionSpec) {
	if spec == nil {
		t.execution = nil
		return
	}
	cloned := *spec
	if spec.FilePolicy != nil {
		fp := *spec.FilePolicy
		fp.AllowRead = append([]string(nil), spec.FilePolicy.AllowRead...)
		fp.AllowCreate = append([]string(nil), spec.FilePolicy.AllowCreate...)
		fp.AllowEdit = append([]string(nil), spec.FilePolicy.AllowEdit...)
		fp.AllowDelete = append([]string(nil), spec.FilePolicy.AllowDelete...)
		fp.Deny = append([]string(nil), spec.FilePolicy.Deny...)
		if spec.FilePolicy.AllowCommands != nil {
			v := *spec.FilePolicy.AllowCommands
			fp.AllowCommands = &v
		}
		if spec.FilePolicy.AllowDelegate != nil {
			v := *spec.FilePolicy.AllowDelegate
			fp.AllowDelegate = &v
		}
		cloned.FilePolicy = &fp
	}
	t.execution = &cloned
}

func (t *BrowserActionTool) Name() string { return "verifier.browser_action" }

func (t *BrowserActionTool) Schema() Schema {
	return Schema{
		Name: t.Name(),
		Description: "Request a browser action for UI verification. The action is invoked " +
			"deterministically on the Browser Brain via the Kernel without running the " +
			"Browser Brain's Agent Loop. Supported actions: open, click, double_click, " +
			"right_click, type, press_key, scroll, hover, drag, select, upload_file, " +
			"navigate, screenshot, eval, wait. Returns the underlying browser tool result.",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "action": {
      "type": "string",
      "description": "Browser action to perform: open, click, double_click, right_click, type, press_key, scroll, hover, drag, select, upload_file, navigate, screenshot, eval, wait"
    },
    "params": {
      "type": "object",
      "description": "Action-specific parameters (e.g. {\"url\": \"...\"} for open, {\"selector\": \"...\"} for click, {\"text\": \"...\"} for type)"
    },
    "description": {
      "type": "string",
      "description": "Optional human-readable description for audit/logging; not used for AI reasoning"
    }
  },
  "required": ["action"]
}`),
		OutputSchema: dynamicJSONOutputSchema,
		Brain:        "verifier",
	}
}

func (t *BrowserActionTool) Risk() Risk { return RiskMedium }

func (t *BrowserActionTool) Execute(ctx context.Context, args json.RawMessage) (*Result, error) {
	if t.caller == nil {
		return &Result{
			Output:  jsonStr("browser_action unavailable: no Kernel connection (KernelCaller not set)"),
			IsError: true,
		}, nil
	}

	var input struct {
		Action      string          `json:"action"`
		Params      json.RawMessage `json:"params,omitempty"`
		Description string          `json:"description"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return &Result{Output: jsonStr(fmt.Sprintf("invalid arguments: %v", err)), IsError: true}, nil
	}
	if input.Action == "" {
		return &Result{Output: jsonStr("action is required"), IsError: true}, nil
	}
	toolName, ok := verifierBrowserToolNames[input.Action]
	if !ok {
		return &Result{Output: jsonStr(fmt.Sprintf("unknown browser action: %s", input.Action)), IsError: true}, nil
	}

	callReq := protocol.SpecialistToolCallRequest{
		TargetKind: "browser",
		ToolName:   toolName,
		Arguments:  input.Params,
		Execution:  t.execution,
	}
	var result protocol.ToolCallResult
	if err := t.caller.CallKernel(ctx, protocol.MethodSpecialistCallTool, callReq, &result); err != nil {
		return &Result{
			Output:  jsonStr(fmt.Sprintf("browser tool call failed: %v", err)),
			IsError: true,
		}, nil
	}
	return &Result{
		Output:  result.OutputOrEnvelope(),
		IsError: result.IsError,
	}, nil
}

var verifierBrowserToolNames = map[string]string{
	"open":         "browser.open",
	"click":        "browser.click",
	"double_click": "browser.double_click",
	"right_click":  "browser.right_click",
	"type":         "browser.type",
	"press_key":    "browser.press_key",
	"scroll":       "browser.scroll",
	"hover":        "browser.hover",
	"drag":         "browser.drag",
	"select":       "browser.select",
	"upload_file":  "browser.upload_file",
	"navigate":     "browser.navigate",
	"screenshot":   "browser.screenshot",
	"eval":         "browser.eval",
	"wait":         "browser.wait",
}
