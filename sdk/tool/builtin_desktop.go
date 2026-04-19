package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"

	brainerrors "github.com/leef-l/brain/sdk/errors"
)

// Task #21 — Desktop Brain 工具:OS 级自动化,覆盖浏览器外的窗口/文件/快捷键。
//
// 一步到位:不自造窗口管理器,直接桥接 OS 既有命令:
//   - Linux:   xdg-open / xdotool / wmctrl
//   - macOS:   open / osascript
//   - Windows: start / powershell (目前 MVP 只在 Linux/macOS 可用)
//
// 设计原则:外部命令缺失时返回结构化错误(用 brain-v3 的 sdk/errors 分类),
// Agent 读到 error_code=tool_not_found 时知道该在此主机上放弃 desktop 任务。

// ---------------------------------------------------------------------------
// desktop.open_path — 用默认程序打开文件/目录/URL
// ---------------------------------------------------------------------------

type DesktopOpenPathTool struct{}

func NewDesktopOpenPathTool() *DesktopOpenPathTool { return &DesktopOpenPathTool{} }

func (t *DesktopOpenPathTool) Name() string { return "desktop.open_path" }
func (t *DesktopOpenPathTool) Risk() Risk   { return RiskMedium }

func (t *DesktopOpenPathTool) Schema() Schema {
	return Schema{
		Name: t.Name(),
		Description: `Open a file / directory / URL with the OS default handler.
Uses xdg-open on Linux, open on macOS, start on Windows.

When to use:
  - Surface a local PDF / image / log file the user should inspect
  - Hand off to a native app (GIMP / Excel) the agent can't drive itself

When NOT to use:
  - Any URL that browser.navigate can handle — prefer browser brain
  - Destructive actions (rm, mv) — use code.execute_shell with approval`,
		InputSchema: json.RawMessage(`{
  "type": "object",
  "required": ["target"],
  "properties": {
    "target": { "type": "string", "description": "Absolute path or URL" }
  }
}`),
		OutputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "opened": { "type": "boolean" },
    "command": { "type": "string" }
  }
}`),
		Brain: string(desktopKind),
		Concurrency: &ToolConcurrencySpec{
			Capability:          "desktop.open",
			ResourceKeyTemplate: "desktop:open:{{target}}",
			AccessMode:          "exclusive",
			Scope:               "turn",
			ApprovalClass:       "external-network",
		},
	}
}

func (t *DesktopOpenPathTool) Execute(ctx context.Context, args json.RawMessage) (*Result, error) {
	var in struct {
		Target string `json:"target"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return ErrorResult(brainerrors.CodeToolInputInvalid, "invalid arguments: %v", err), nil
	}
	if strings.TrimSpace(in.Target) == "" {
		return ErrorResult(brainerrors.CodeToolInputInvalid, "target is required"), nil
	}
	cmdName, err := resolveOpener()
	if err != nil {
		return ErrorResult(brainerrors.CodeToolExecutionFailed, "no opener available: %v", err), nil
	}
	runCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(runCtx, cmdName, in.Target)
	if out, err := cmd.CombinedOutput(); err != nil {
		return ErrorResult(brainerrors.CodeToolExecutionFailed, "%s %s: %v (%s)", cmdName, in.Target, err, strings.TrimSpace(string(out))), nil
	}
	return okResult(map[string]interface{}{
		"opened":  true,
		"command": cmdName + " " + in.Target,
	}), nil
}

// ---------------------------------------------------------------------------
// desktop.list_windows — 枚举当前窗口
// ---------------------------------------------------------------------------

type DesktopListWindowsTool struct{}

func NewDesktopListWindowsTool() *DesktopListWindowsTool { return &DesktopListWindowsTool{} }

func (t *DesktopListWindowsTool) Name() string { return "desktop.list_windows" }
func (t *DesktopListWindowsTool) Risk() Risk   { return RiskLow }

func (t *DesktopListWindowsTool) Schema() Schema {
	return Schema{
		Name: t.Name(),
		Description: `List visible top-level windows with their title and PID.

Linux requires wmctrl (apt install wmctrl).
macOS uses osascript.
Windows is not yet supported.

Returns [{ "id": "...", "title": "...", "pid": 1234 }, ...].`,
		InputSchema:  json.RawMessage(`{"type":"object"}`),
		OutputSchema: json.RawMessage(`{"type":"object","properties":{"windows":{"type":"array"}}}`),
		Brain:        string(desktopKind),
		Concurrency: &ToolConcurrencySpec{
			Capability:          "desktop.read",
			ResourceKeyTemplate: "desktop:windows",
			AccessMode:          "shared-read",
			Scope:               "turn",
			ApprovalClass:       "readonly",
		},
	}
}

func (t *DesktopListWindowsTool) Execute(ctx context.Context, _ json.RawMessage) (*Result, error) {
	windows, err := listWindows(ctx)
	if err != nil {
		return ErrorResult(brainerrors.CodeToolExecutionFailed, "list windows: %v", err), nil
	}
	return okResult(map[string]interface{}{"windows": windows, "count": len(windows)}), nil
}

// ---------------------------------------------------------------------------
// desktop.send_hotkey — 发送键盘组合键
// ---------------------------------------------------------------------------

type DesktopSendHotkeyTool struct{}

func NewDesktopSendHotkeyTool() *DesktopSendHotkeyTool { return &DesktopSendHotkeyTool{} }

func (t *DesktopSendHotkeyTool) Name() string { return "desktop.send_hotkey" }
func (t *DesktopSendHotkeyTool) Risk() Risk   { return RiskMedium }

func (t *DesktopSendHotkeyTool) Schema() Schema {
	return Schema{
		Name: t.Name(),
		Description: `Send a keyboard shortcut to the focused window.

Key format uses xdotool notation: "ctrl+c", "super+l", "F11".
Linux requires xdotool. macOS uses osascript key code mappings (subset).

When to use:
  - Drive native apps that have no HTTP API (DE settings, terminal emulator)
  - Copy/paste clipboard between GUI apps

When NOT to use:
  - Anything browser.press_key can do — prefer browser brain`,
		InputSchema: json.RawMessage(`{
  "type": "object",
  "required": ["keys"],
  "properties": {
    "keys":       { "type": "string", "description": "Key combo, e.g. 'ctrl+shift+t'" },
    "delay_ms":   { "type": "integer", "description": "Delay before sending (ms, default 0)" }
  }
}`),
		OutputSchema: json.RawMessage(`{"type":"object","properties":{"sent":{"type":"boolean"}}}`),
		Brain:        string(desktopKind),
		Concurrency: &ToolConcurrencySpec{
			Capability:          "desktop.input",
			ResourceKeyTemplate: "desktop:focused_window",
			AccessMode:          "exclusive",
			Scope:               "turn",
			ApprovalClass:       "exec-capable",
		},
	}
}

func (t *DesktopSendHotkeyTool) Execute(ctx context.Context, args json.RawMessage) (*Result, error) {
	var in struct {
		Keys    string `json:"keys"`
		DelayMS int    `json:"delay_ms"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return ErrorResult(brainerrors.CodeToolInputInvalid, "invalid arguments: %v", err), nil
	}
	if strings.TrimSpace(in.Keys) == "" {
		return ErrorResult(brainerrors.CodeToolInputInvalid, "keys is required"), nil
	}
	if in.DelayMS > 0 {
		select {
		case <-ctx.Done():
			return ErrorResult(brainerrors.CodeDeadlineExceeded, "canceled: %v", ctx.Err()), nil
		case <-time.After(time.Duration(in.DelayMS) * time.Millisecond):
		}
	}
	if err := sendHotkey(ctx, in.Keys); err != nil {
		return ErrorResult(brainerrors.CodeToolExecutionFailed, "send hotkey: %v", err), nil
	}
	return okResult(map[string]interface{}{"sent": true, "keys": in.Keys}), nil
}

// ---------------------------------------------------------------------------
// helpers — 平台桥接
// ---------------------------------------------------------------------------

const desktopKind = "desktop"

func resolveOpener() (string, error) {
	switch runtime.GOOS {
	case "darwin":
		return "open", nil
	case "linux":
		if _, err := exec.LookPath("xdg-open"); err == nil {
			return "xdg-open", nil
		}
		return "", fmt.Errorf("xdg-open not installed")
	case "windows":
		return "start", nil
	}
	return "", fmt.Errorf("unsupported platform %q", runtime.GOOS)
}

type desktopWindow struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	PID   int    `json:"pid,omitempty"`
}

func listWindows(ctx context.Context) ([]desktopWindow, error) {
	switch runtime.GOOS {
	case "linux":
		if _, err := exec.LookPath("wmctrl"); err != nil {
			return nil, fmt.Errorf("wmctrl not installed")
		}
		runCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		out, err := exec.CommandContext(runCtx, "wmctrl", "-l", "-p").Output()
		if err != nil {
			return nil, err
		}
		return parseWmctrl(string(out)), nil
	case "darwin":
		runCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		script := `tell application "System Events" to get {title, unix id} of every process whose visible is true`
		out, err := exec.CommandContext(runCtx, "osascript", "-e", script).Output()
		if err != nil {
			return nil, err
		}
		return parseOsascriptProcs(string(out)), nil
	}
	return nil, fmt.Errorf("unsupported platform %q", runtime.GOOS)
}

func parseWmctrl(raw string) []desktopWindow {
	var out []desktopWindow
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// wmctrl -l -p: <id> <desktop> <pid> <hostname> <title>
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		pid := 0
		fmt.Sscanf(fields[2], "%d", &pid)
		title := strings.Join(fields[4:], " ")
		out = append(out, desktopWindow{ID: fields[0], Title: title, PID: pid})
	}
	return out
}

func parseOsascriptProcs(raw string) []desktopWindow {
	// osascript 返回形如 "Finder, 123, Safari, 456, ..."
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	fields := strings.Split(raw, ", ")
	var out []desktopWindow
	for i := 0; i+1 < len(fields); i += 2 {
		pid := 0
		fmt.Sscanf(fields[i+1], "%d", &pid)
		out = append(out, desktopWindow{ID: fields[i+1], Title: fields[i], PID: pid})
	}
	return out
}

func sendHotkey(ctx context.Context, keys string) error {
	runCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	switch runtime.GOOS {
	case "linux":
		if _, err := exec.LookPath("xdotool"); err != nil {
			return fmt.Errorf("xdotool not installed")
		}
		out, err := exec.CommandContext(runCtx, "xdotool", "key", "--clearmodifiers", keys).CombinedOutput()
		if err != nil {
			return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	case "darwin":
		// 将 "cmd+shift+t" 转 AppleScript key down 序列
		script := buildAppleScriptKeyDown(keys)
		if script == "" {
			return fmt.Errorf("unsupported key combo %q on macOS", keys)
		}
		out, err := exec.CommandContext(runCtx, "osascript", "-e", script).CombinedOutput()
		if err != nil {
			return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	return fmt.Errorf("unsupported platform %q", runtime.GOOS)
}

// buildAppleScriptKeyDown 是简化映射,覆盖最常见 5 个修饰键 + 单字符。
func buildAppleScriptKeyDown(keys string) string {
	parts := strings.Split(strings.ToLower(keys), "+")
	if len(parts) == 0 {
		return ""
	}
	mods := []string{}
	var final string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		switch p {
		case "ctrl", "control":
			mods = append(mods, "control down")
		case "shift":
			mods = append(mods, "shift down")
		case "alt", "option":
			mods = append(mods, "option down")
		case "cmd", "command", "super":
			mods = append(mods, "command down")
		default:
			final = p
		}
	}
	if final == "" {
		return ""
	}
	modStr := ""
	if len(mods) > 0 {
		modStr = " using {" + strings.Join(mods, ", ") + "}"
	}
	return fmt.Sprintf(`tell application "System Events" to keystroke %q%s`, final, modStr)
}
