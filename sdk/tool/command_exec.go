package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	Stdout    string
	Stderr    string
	ExitCode  int
	TimedOut  bool
	Sandboxed bool
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

	var (
		exitCode  int
		sandboxed bool
	)

	if cmdSandbox != nil && cmdSandbox.Available() {
		code, runErr := cmdSandbox.Run(execCtx, req.Command, workDir, stdoutW, stderrW)
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
		cmd.Stdout = stdoutW
		cmd.Stderr = stderrW

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

	return CommandOutcome{
		Stdout:    strings.TrimRight(stdout.String(), "\n"),
		Stderr:    strings.TrimRight(stderr.String(), "\n"),
		ExitCode:  exitCode,
		TimedOut:  execCtx.Err() == context.DeadlineExceeded,
		Sandboxed: sandboxed,
	}, nil
}

func ResultForCommandTool(toolName string, outcome CommandOutcome) *Result {
	switch {
	case strings.HasSuffix(toolName, ".run_tests"):
		raw, _ := json.Marshal(map[string]interface{}{
			"stdout":    outcome.Stdout,
			"stderr":    outcome.Stderr,
			"exit_code": outcome.ExitCode,
			"passed":    outcome.ExitCode == 0 && !outcome.TimedOut,
			"timed_out": outcome.TimedOut,
		})
		return &Result{
			Output:  raw,
			IsError: outcome.ExitCode != 0 || outcome.TimedOut,
		}
	default:
		raw, _ := json.Marshal(map[string]interface{}{
			"stdout":    outcome.Stdout,
			"stderr":    outcome.Stderr,
			"exit_code": outcome.ExitCode,
			"timed_out": outcome.TimedOut,
			"sandboxed": outcome.Sandboxed,
		})
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
}

func (w *limitWriter) Write(p []byte) (int, error) {
	remaining := w.max - w.written
	if remaining <= 0 {
		return len(p), nil
	}
	if len(p) > remaining {
		p = p[:remaining]
	}
	n, _ := w.buf.Write(p)
	w.written += n
	return len(p), nil
}
