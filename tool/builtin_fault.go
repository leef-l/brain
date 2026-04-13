package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// 1. fault.inject_error — Inject an error into a file or command
// ---------------------------------------------------------------------------

type InjectErrorTool struct{}

func NewInjectErrorTool() *InjectErrorTool { return &InjectErrorTool{} }

func (t *InjectErrorTool) Name() string { return "fault.inject_error" }
func (t *InjectErrorTool) Risk() Risk   { return RiskHigh }

func (t *InjectErrorTool) Schema() Schema {
	return Schema{
		Name: t.Name(),
		Description: "Inject an error condition for chaos testing. Supports: " +
			"'file_corrupt' (corrupt bytes in a file copy), " +
			"'env_poison' (set an env var to a bad value for a command), " +
			"'exit_code' (run a command and override its exit code interpretation), " +
			"'disk_full' (simulate disk full by filling a temp file).",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "type": { "type": "string", "description": "Error type: file_corrupt, env_poison, exit_code, disk_full" },
    "target": { "type": "string", "description": "Target file path or command" },
    "params": { "type": "object", "description": "Type-specific params (e.g. {\"bytes\": 10} for file_corrupt, {\"key\": \"DB_HOST\", \"value\": \"invalid\"} for env_poison)" }
  },
  "required": ["type"]
}`),
		OutputSchema: faultInjectErrorOutputSchema,
		Brain:        "fault",
	}
}

func (t *InjectErrorTool) Execute(ctx context.Context, args json.RawMessage) (*Result, error) {
	var input struct {
		Type   string          `json:"type"`
		Target string          `json:"target"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return faultErr("invalid arguments: %v", err), nil
	}

	switch input.Type {
	case "file_corrupt":
		return injectFileCorrupt(input.Target, input.Params)
	case "env_poison":
		return injectEnvPoison(ctx, input.Target, input.Params)
	case "exit_code":
		return injectExitCode(ctx, input.Target, input.Params)
	case "disk_full":
		return injectDiskFull(input.Params)
	default:
		return faultErr("unknown error type: %s", input.Type), nil
	}
}

func injectFileCorrupt(target string, params json.RawMessage) (*Result, error) {
	if target == "" {
		return faultErr("target file path required"), nil
	}

	var p struct {
		Bytes  int    `json:"bytes"`
		Output string `json:"output"`
	}
	if params != nil {
		json.Unmarshal(params, &p)
	}
	if p.Bytes <= 0 {
		p.Bytes = 10
	}

	// Read original file.
	data, err := os.ReadFile(target)
	if err != nil {
		return faultErr("read %s: %v", target, err), nil
	}

	// Create corrupted copy (never corrupt original).
	corrupted := make([]byte, len(data))
	copy(corrupted, data)

	for i := 0; i < p.Bytes && i < len(corrupted); i++ {
		pos := rand.Intn(len(corrupted))
		corrupted[pos] = byte(rand.Intn(256))
	}

	outPath := p.Output
	if outPath == "" {
		outPath = target + ".corrupted"
	}

	if err := os.WriteFile(outPath, corrupted, 0644); err != nil {
		return faultErr("write corrupted: %v", err), nil
	}

	return faultOK(map[string]interface{}{
		"type":            "file_corrupt",
		"original":        target,
		"corrupted_file":  outPath,
		"bytes_corrupted": p.Bytes,
		"original_size":   len(data),
	}), nil
}

func injectEnvPoison(ctx context.Context, command string, params json.RawMessage) (*Result, error) {
	if command == "" {
		return faultErr("target command required"), nil
	}

	var p struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	if params != nil {
		json.Unmarshal(params, &p)
	}
	if p.Key == "" {
		return faultErr("params.key required"), nil
	}

	// Run the command with the poisoned env var.
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Env = append(os.Environ(), fmt.Sprintf("%s=%s", p.Key, p.Value))

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
	}

	return faultOK(map[string]interface{}{
		"type":      "env_poison",
		"command":   command,
		"env_key":   p.Key,
		"env_value": p.Value,
		"exit_code": exitCode,
		"stdout":    truncate(stdout.String(), 2000),
		"stderr":    truncate(stderr.String(), 2000),
	}), nil
}

func injectExitCode(ctx context.Context, command string, params json.RawMessage) (*Result, error) {
	if command == "" {
		return faultErr("target command required"), nil
	}

	// Run the command normally.
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	realCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			realCode = exitErr.ExitCode()
		}
	}

	return faultOK(map[string]interface{}{
		"type":           "exit_code",
		"command":        command,
		"real_exit_code": realCode,
		"stdout":         truncate(stdout.String(), 2000),
		"stderr":         truncate(stderr.String(), 2000),
		"note":           "real exit code captured; use this to simulate what happens when the command fails",
	}), nil
}

func injectDiskFull(params json.RawMessage) (*Result, error) {
	var p struct {
		SizeMB int    `json:"size_mb"`
		Path   string `json:"path"`
	}
	if params != nil {
		json.Unmarshal(params, &p)
	}
	if p.SizeMB <= 0 {
		p.SizeMB = 100
	}
	if p.SizeMB > 1024 {
		p.SizeMB = 1024 // cap at 1GB
	}

	dir := p.Path
	if dir == "" {
		dir = os.TempDir()
	}

	path := fmt.Sprintf("%s/brain-fault-diskfull-%d", dir, time.Now().UnixNano())
	f, err := os.Create(path)
	if err != nil {
		return faultErr("create fill file: %v", err), nil
	}
	defer f.Close()

	// Write zeros in chunks.
	chunk := make([]byte, 1024*1024) // 1MB
	written := 0
	for written < p.SizeMB {
		n, err := f.Write(chunk)
		if err != nil {
			// Disk full — that's the point!
			return faultOK(map[string]interface{}{
				"type":       "disk_full",
				"file":       path,
				"written_mb": written,
				"error":      err.Error(),
				"simulated":  true,
			}), nil
		}
		_ = n
		written++
	}

	return faultOK(map[string]interface{}{
		"type":       "disk_full",
		"file":       path,
		"written_mb": written,
		"note":       "clean up with: rm " + path,
	}), nil
}

// ---------------------------------------------------------------------------
// 2. fault.inject_latency — Add artificial latency
// ---------------------------------------------------------------------------

type InjectLatencyTool struct{}

func NewInjectLatencyTool() *InjectLatencyTool { return &InjectLatencyTool{} }

func (t *InjectLatencyTool) Name() string { return "fault.inject_latency" }
func (t *InjectLatencyTool) Risk() Risk   { return RiskMedium }

func (t *InjectLatencyTool) Schema() Schema {
	return Schema{
		Name: t.Name(),
		Description: "Inject artificial latency into a command execution. " +
			"Runs the command after a specified delay, or wraps it with a " +
			"network latency simulation using tc (Linux traffic control).",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "command": { "type": "string", "description": "Command to execute with injected latency" },
    "delay_ms": { "type": "integer", "description": "Delay in milliseconds before/during execution (default: 1000)" },
    "jitter_ms": { "type": "integer", "description": "Random jitter range in ms (default: 0)" },
    "type": { "type": "string", "description": "Latency type: 'startup' (delay before exec) or 'network' (tc netem simulation, requires root)" }
  },
  "required": ["command"]
}`),
		OutputSchema: faultInjectLatencyOutputSchema,
		Brain:        "fault",
	}
}

func (t *InjectLatencyTool) Execute(ctx context.Context, args json.RawMessage) (*Result, error) {
	var input struct {
		Command  string `json:"command"`
		DelayMS  int    `json:"delay_ms"`
		JitterMS int    `json:"jitter_ms"`
		Type     string `json:"type"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return faultErr("invalid arguments: %v", err), nil
	}
	if input.Command == "" {
		return faultErr("command is required"), nil
	}
	if input.DelayMS <= 0 {
		input.DelayMS = 1000
	}
	if input.Type == "" {
		input.Type = "startup"
	}

	actualDelay := input.DelayMS
	if input.JitterMS > 0 {
		actualDelay += rand.Intn(input.JitterMS)
	}

	start := time.Now()

	switch input.Type {
	case "startup":
		// Simple delay before execution.
		time.Sleep(time.Duration(actualDelay) * time.Millisecond)

		cmd := exec.CommandContext(ctx, "sh", "-c", input.Command)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err := cmd.Run()

		exitCode := 0
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				exitCode = exitErr.ExitCode()
			}
		}

		return faultOK(map[string]interface{}{
			"type":              "startup_latency",
			"injected_delay_ms": actualDelay,
			"total_elapsed_ms":  time.Since(start).Milliseconds(),
			"exit_code":         exitCode,
			"stdout":            truncate(stdout.String(), 2000),
			"stderr":            truncate(stderr.String(), 2000),
		}), nil

	case "network":
		// Report what tc command would be needed.
		return faultOK(map[string]interface{}{
			"type":      "network_latency",
			"delay_ms":  actualDelay,
			"jitter_ms": input.JitterMS,
			"tc_add":    fmt.Sprintf("tc qdisc add dev eth0 root netem delay %dms %dms", input.DelayMS, input.JitterMS),
			"tc_remove": "tc qdisc del dev eth0 root netem",
			"note":      "network latency injection requires root. Run the tc commands manually or use startup type.",
		}), nil

	default:
		return faultErr("unknown type: %s (use startup or network)", input.Type), nil
	}
}

// ---------------------------------------------------------------------------
// 3. fault.kill_process — Kill a process by name or PID
// ---------------------------------------------------------------------------

type KillProcessTool struct{}

func NewKillProcessTool() *KillProcessTool { return &KillProcessTool{} }

func (t *KillProcessTool) Name() string { return "fault.kill_process" }
func (t *KillProcessTool) Risk() Risk   { return RiskHigh }

func (t *KillProcessTool) Schema() Schema {
	return Schema{
		Name: t.Name(),
		Description: "Kill a process by PID or name for chaos testing. " +
			"Supports SIGTERM (graceful), SIGKILL (force), and SIGSTOP (freeze).",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "pid": { "type": "integer", "description": "Process ID to kill" },
    "name": { "type": "string", "description": "Process name to find and kill (uses pgrep)" },
    "signal": { "type": "string", "description": "Signal: TERM (default), KILL, STOP, CONT, HUP, USR1, USR2" }
  }
}`),
		OutputSchema: faultKillProcessOutputSchema,
		Brain:        "fault",
	}
}

func (t *KillProcessTool) Execute(ctx context.Context, args json.RawMessage) (*Result, error) {
	var input struct {
		PID    *int   `json:"pid"`
		Name   string `json:"name"`
		Signal string `json:"signal"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return faultErr("invalid arguments: %v", err), nil
	}

	sig := resolveSignal(input.Signal)
	signalName := strings.ToUpper(strings.TrimSpace(input.Signal))
	if signalName == "" {
		signalName = "TERM"
	}

	var pids []int

	if input.PID != nil {
		pids = append(pids, *input.PID)
	} else if input.Name != "" {
		// Find PIDs by name.
		out, err := exec.CommandContext(ctx, "pgrep", "-f", input.Name).Output()
		if err != nil {
			return faultErr("pgrep %s: no matching processes", input.Name), nil
		}
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if pid, err := strconv.Atoi(strings.TrimSpace(line)); err == nil {
				// Don't kill ourselves.
				if pid != os.Getpid() {
					pids = append(pids, pid)
				}
			}
		}
		if len(pids) == 0 {
			return faultErr("no matching processes for %q", input.Name), nil
		}
	} else {
		return faultErr("provide pid or name"), nil
	}

	var results []map[string]interface{}
	for _, pid := range pids {
		proc, err := os.FindProcess(pid)
		if err != nil {
			results = append(results, map[string]interface{}{
				"pid": pid, "status": "not_found", "error": err.Error(),
			})
			continue
		}
		err = proc.Signal(sig)
		if err != nil {
			results = append(results, map[string]interface{}{
				"pid": pid, "status": "failed", "error": err.Error(),
			})
		} else {
			results = append(results, map[string]interface{}{
				"pid": pid, "status": "signaled", "signal": signalName,
			})
		}
	}

	return faultOK(map[string]interface{}{
		"type":      "kill_process",
		"signal":    signalName,
		"processes": results,
	}), nil
}

// ---------------------------------------------------------------------------
// 4. fault.corrupt_response — Corrupt a command's output
// ---------------------------------------------------------------------------

type CorruptResponseTool struct{}

func NewCorruptResponseTool() *CorruptResponseTool { return &CorruptResponseTool{} }

func (t *CorruptResponseTool) Name() string { return "fault.corrupt_response" }
func (t *CorruptResponseTool) Risk() Risk   { return RiskHigh }

func (t *CorruptResponseTool) Schema() Schema {
	return Schema{
		Name: t.Name(),
		Description: "Run a command and corrupt its output in various ways for testing. " +
			"Modes: 'truncate' (cut output), 'shuffle' (shuffle lines), " +
			"'replace' (replace strings), 'noise' (add random chars), 'empty' (discard output).",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "command": { "type": "string", "description": "Command to execute" },
    "mode": { "type": "string", "description": "Corruption mode: truncate, shuffle, replace, noise, empty" },
    "params": { "type": "object", "description": "Mode params: {\"percent\": 50} for truncate, {\"find\": \"ok\", \"replace\": \"error\"} for replace, {\"density\": 0.1} for noise" }
  },
  "required": ["command", "mode"]
}`),
		OutputSchema: faultCorruptResponseOutputSchema,
		Brain:        "fault",
	}
}

func (t *CorruptResponseTool) Execute(ctx context.Context, args json.RawMessage) (*Result, error) {
	var input struct {
		Command string          `json:"command"`
		Mode    string          `json:"mode"`
		Params  json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return faultErr("invalid arguments: %v", err), nil
	}
	if input.Command == "" {
		return faultErr("command is required"), nil
	}

	// Run the command.
	cmd := exec.CommandContext(ctx, "sh", "-c", input.Command)
	out, err := cmd.CombinedOutput()
	original := string(out)
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
	}

	// Corrupt the output.
	var corrupted string
	switch input.Mode {
	case "truncate":
		pct := 50
		if input.Params != nil {
			var p struct {
				Percent int `json:"percent"`
			}
			json.Unmarshal(input.Params, &p)
			if p.Percent > 0 {
				pct = p.Percent
			}
		}
		cutAt := len(original) * pct / 100
		corrupted = original[:cutAt]

	case "shuffle":
		lines := strings.Split(original, "\n")
		rand.Shuffle(len(lines), func(i, j int) { lines[i], lines[j] = lines[j], lines[i] })
		corrupted = strings.Join(lines, "\n")

	case "replace":
		var p struct {
			Find    string `json:"find"`
			Replace string `json:"replace"`
		}
		if input.Params != nil {
			json.Unmarshal(input.Params, &p)
		}
		if p.Find == "" {
			return faultErr("params.find required for replace mode"), nil
		}
		corrupted = strings.ReplaceAll(original, p.Find, p.Replace)

	case "noise":
		density := 0.05
		if input.Params != nil {
			var p struct {
				Density float64 `json:"density"`
			}
			json.Unmarshal(input.Params, &p)
			if p.Density > 0 {
				density = p.Density
			}
		}
		runes := []rune(original)
		var result []rune
		for _, r := range runes {
			result = append(result, r)
			if rand.Float64() < density {
				result = append(result, rune(rand.Intn(94)+33)) // printable ASCII
			}
		}
		corrupted = string(result)

	case "empty":
		corrupted = ""

	default:
		return faultErr("unknown mode: %s", input.Mode), nil
	}

	return faultOK(map[string]interface{}{
		"type":            "corrupt_response",
		"mode":            input.Mode,
		"exit_code":       exitCode,
		"original_length": len(original),
		"corrupted":       truncate(corrupted, 4000),
		"original":        truncate(original, 2000),
	}), nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func faultErr(format string, a ...interface{}) *Result {
	msg := fmt.Sprintf(format, a...)
	return &Result{Output: jsonStr(msg), IsError: true}
}

func faultOK(v interface{}) *Result {
	data, _ := json.Marshal(v)
	return &Result{Output: data}
}

func truncate(s string, max int) string {
	if len(s) > max {
		return s[:max] + "...[truncated]"
	}
	return s
}
