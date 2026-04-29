package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

const (
	defaultShellTimeout    = 60
	defaultRunTestsTimeout = 120
	maxCommandTimeout      = 300
	maxCommandOutputBytes  = 100 * 1024
)

type CommandRequest struct {
	Command        string `json:"command"`
	WorkingDir     string `json:"working_dir"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

type CommandOutcome struct {
	Stdout        string
	Stderr        string
	ExitCode      int
	TimedOut      bool
	Sandboxed     bool
	StdoutDropped int // stdout 因 maxCommandOutputBytes 丢弃的字节数
	StderrDropped int // stderr 同上
}

func ParseCommandRequest(args json.RawMessage) (CommandRequest, error) {
	var req CommandRequest
	if err := json.Unmarshal(args, &req); err != nil {
		return CommandRequest{}, err
	}
	return req, nil
}

func ValidateCommandRequest(req CommandRequest) error {
	if strings.TrimSpace(req.Command) == "" {
		return fmt.Errorf("command is required")
	}
	return nil
}

func NormalizeCommandRequest(toolName string, req CommandRequest) CommandRequest {
	if req.TimeoutSeconds <= 0 {
		req.TimeoutSeconds = defaultTimeoutForCommandTool(toolName)
	}
	if req.TimeoutSeconds > maxCommandTimeout {
		req.TimeoutSeconds = maxCommandTimeout
	}
	return req
}

func ExecuteCommandRequest(ctx context.Context, req CommandRequest, sb *Sandbox, cmdSandbox CommandSandbox) (CommandOutcome, error) {
	return ExecuteCommandRequestWithStreams(ctx, req, sb, cmdSandbox, nil, nil)
}

// ExecuteCommandRequestWithStreams is like ExecuteCommandRequest but also streams
// stdout/stderr to the provided writers in real time (e.g. os.Stderr for CLI visibility).
func ExecuteCommandRequestWithStreams(ctx context.Context, req CommandRequest, sb *Sandbox, cmdSandbox CommandSandbox, stdoutStream, stderrStream io.Writer) (CommandOutcome, error) {
	var (
		stdout bytes.Buffer
		stderr bytes.Buffer
	)

	execCtx, cancel := context.WithTimeout(ctx, time.Duration(req.TimeoutSeconds)*time.Second)
	defer cancel()

	workDir := strings.TrimSpace(req.WorkingDir)
	if workDir == "" && sb != nil {
		workDir = sb.Primary()
	}

	stdoutW := &limitWriter{buf: &stdout, max: maxCommandOutputBytes}
	stderrW := &limitWriter{buf: &stderr, max: maxCommandOutputBytes}

	// If stream writers are provided, tee output so the user can see progress.
	var stdoutOut io.Writer = stdoutW
	var stderrOut io.Writer = stderrW
	if stdoutStream != nil {
		stdoutOut = io.MultiWriter(stdoutW, stdoutStream)
	}
	if stderrStream != nil {
		stderrOut = io.MultiWriter(stderrW, stderrStream)
	}

	var (
		exitCode  int
		sandboxed bool
	)

	if cmdSandbox != nil && cmdSandbox.Available() {
		code, runErr := cmdSandbox.Run(execCtx, req.Command, workDir, stdoutOut, stderrOut)
		if runErr != nil {
			return CommandOutcome{}, runErr
		}
		exitCode = code
		sandboxed = true
	} else {
		cmd := exec.CommandContext(execCtx, shellName(), shellFlag(), req.Command)
		if workDir != "" {
			cmd.Dir = workDir
		}
		cmd.Stdout = stdoutOut
		cmd.Stderr = stderrOut

		err := cmd.Run()
		if err != nil {
			switch {
			case execCtx.Err() == context.DeadlineExceeded:
				exitCode = -1
			default:
				if exitErr, ok := err.(*exec.ExitError); ok {
					exitCode = exitErr.ExitCode()
				} else {
					return CommandOutcome{}, err
				}
			}
		}
	}

	stdoutStr := strings.TrimRight(stdout.String(), "\n")
	stderrStr := strings.TrimRight(stderr.String(), "\n")
	// 明确告诉 LLM 输出被截断了多少字节，避免它以为自己看到了完整输出
	if note := stdoutW.TruncatedNote(); note != "" {
		stdoutStr = stdoutStr + "\n" + note
	}
	if note := stderrW.TruncatedNote(); note != "" {
		stderrStr = stderrStr + "\n" + note
	}
	return CommandOutcome{
		Stdout:         stdoutStr,
		Stderr:         stderrStr,
		ExitCode:       exitCode,
		TimedOut:       execCtx.Err() == context.DeadlineExceeded,
		Sandboxed:      sandboxed,
		StdoutDropped:  stdoutW.dropped,
		StderrDropped:  stderrW.dropped,
	}, nil
}

func ResultForCommandTool(toolName string, outcome CommandOutcome) *Result {
	payload := map[string]interface{}{
		"stdout":    outcome.Stdout,
		"stderr":    outcome.Stderr,
		"exit_code": outcome.ExitCode,
		"timed_out": outcome.TimedOut,
	}
	if outcome.StdoutDropped > 0 {
		payload["stdout_dropped_bytes"] = outcome.StdoutDropped
	}
	if outcome.StderrDropped > 0 {
		payload["stderr_dropped_bytes"] = outcome.StderrDropped
	}

	switch {
	case strings.HasSuffix(toolName, ".run_tests"):
		payload["passed"] = outcome.ExitCode == 0 && !outcome.TimedOut
		raw, _ := json.Marshal(payload)
		return &Result{
			Output:  raw,
			IsError: outcome.ExitCode != 0 || outcome.TimedOut,
		}
	default:
		payload["sandboxed"] = outcome.Sandboxed
		raw, _ := json.Marshal(payload)
		return &Result{
			Output:  raw,
			IsError: outcome.ExitCode != 0,
		}
	}
}

func defaultTimeoutForCommandTool(toolName string) int {
	switch {
	case strings.HasSuffix(toolName, ".run_tests"):
		return defaultRunTestsTimeout
	default:
		return defaultShellTimeout
	}
}

type limitWriter struct {
	buf     *bytes.Buffer
	max     int
	written int
	dropped int // 被截断丢弃的字节数；供外层构造 "truncated 提示" 使用
}

func (w *limitWriter) Write(p []byte) (int, error) {
	remaining := w.max - w.written
	if remaining <= 0 {
		w.dropped += len(p)
		return len(p), nil
	}
	if len(p) > remaining {
		w.dropped += len(p) - remaining
		p = p[:remaining]
	}
	n, _ := w.buf.Write(p)
	w.written += n
	return len(p), nil
}

// TruncatedNote 返回对 LLM 友好的截断提示。未截断时返回空串。
func (w *limitWriter) TruncatedNote() string {
	if w.dropped == 0 {
		return ""
	}
	return fmt.Sprintf("[... truncated: %d bytes dropped; raise your command's verbosity or pipe to less if you need more]", w.dropped)
}
