package tool

import (
	"context"
	"encoding/json"
	"fmt"
)

// ShellExecTool executes a shell command and returns its output.
// When a CommandSandbox is set, commands run inside an OS-level isolation
// container: bwrap on Linux, sandbox-exec on macOS, Job Objects on Windows.
type ShellExecTool struct {
	brainKind  string
	sandbox    *Sandbox       // path-level sandbox for default workdir
	cmdSandbox CommandSandbox // OS-level isolation; nil = no isolation
}

// NewShellExecTool constructs a ShellExecTool.
// sandbox may be nil. OS-level sandbox is set via SetCommandSandbox.
func NewShellExecTool(brainKind string, sandbox *Sandbox) *ShellExecTool {
	return &ShellExecTool{brainKind: brainKind, sandbox: sandbox}
}

// SetCommandSandbox attaches an OS-level command sandbox.
func (t *ShellExecTool) SetCommandSandbox(cs CommandSandbox) {
	t.cmdSandbox = cs
}

func (t *ShellExecTool) Name() string { return t.brainKind + ".shell_exec" }

func (t *ShellExecTool) Schema() Schema {
	return Schema{
		Name:        t.Name(),
		Description: "Execute a shell command and return stdout, stderr, and exit code. Use for running tests, builds, git commands, etc.",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "command": {
      "type": "string",
      "description": "The shell command to execute (passed to sh -c)"
    },
    "working_dir": {
      "type": "string",
      "description": "Working directory for the command. Default: current directory"
    },
    "timeout_seconds": {
      "type": "integer",
      "description": "Maximum execution time in seconds. Default: 60, max: 300"
    }
  },
  "required": ["command"]
}`),
		OutputSchema: shellExecOutputSchema,
		Brain:        t.brainKind,
		Concurrency: &ToolConcurrencySpec{
			Capability:          "code.execute",
			ResourceKeyTemplate: "shell:{{command}}",
			AccessMode:          "exclusive-write",
			Scope:               "turn",
			ApprovalClass:       "exec-capable",
		},
	}
}

func (t *ShellExecTool) Risk() Risk { return RiskHigh }

type shellExecOutput struct {
	Stdout    string `json:"stdout"`
	Stderr    string `json:"stderr"`
	ExitCode  int    `json:"exit_code"`
	TimedOut  bool   `json:"timed_out"`
	Sandboxed bool   `json:"sandboxed"`
}

func (t *ShellExecTool) Execute(ctx context.Context, args json.RawMessage) (*Result, error) {
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
