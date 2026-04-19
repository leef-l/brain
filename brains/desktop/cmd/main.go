// Command brain-desktop-sidecar is the Desktop Brain sidecar binary.
//
// Desktop Brain is a specialist brain for OS-level automation outside the
// browser: opening local files, listing windows, sending keyboard shortcuts.
//
// See Task #21 — OS-level automation as an optional standalone brain. Follows
// the same sidecar pattern as brains/fault for consistency.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	brain "github.com/leef-l/brain"
	"github.com/leef-l/brain/sdk/agent"
	"github.com/leef-l/brain/sdk/executionpolicy"
	"github.com/leef-l/brain/sdk/kernel"
	"github.com/leef-l/brain/sdk/license"
	"github.com/leef-l/brain/sdk/sidecar"
	"github.com/leef-l/brain/sdk/tool"
	"github.com/leef-l/brain/sdk/toolpolicy"
)

type desktopHandler struct {
	registry tool.Registry
	caller   sidecar.KernelCaller
	learner  *kernel.DefaultBrainLearner
}

func newDesktopHandler() *desktopHandler {
	var reg tool.Registry = tool.NewMemRegistry()
	reg.Register(tool.NewDesktopOpenPathTool())
	reg.Register(tool.NewDesktopListWindowsTool())
	reg.Register(tool.NewDesktopSendHotkeyTool())
	reg.Register(tool.NewNoteTool("desktop"))

	if cfg, err := toolpolicy.Load(""); err != nil {
		fmt.Fprintf(os.Stderr, "brain-desktop: load tool policy: %v\n", err)
	} else {
		reg = toolpolicy.FilterRegistry(reg, cfg, toolpolicy.ToolScopesForDelegate(string(agent.KindDesktop))...)
	}
	return &desktopHandler{
		registry: reg,
		learner:  kernel.NewDefaultBrainLearner(agent.KindDesktop),
	}
}

func (h *desktopHandler) Kind() agent.Kind         { return agent.KindDesktop }
func (h *desktopHandler) Version() string          { return brain.SDKVersion }
func (h *desktopHandler) Tools() []string          { return sidecar.RegistryToolNames(h.registry) }
func (h *desktopHandler) ToolSchemas() []tool.Schema {
	return sidecar.RegistryToolSchemas(h.registry)
}

func (h *desktopHandler) SetKernelCaller(caller sidecar.KernelCaller) {
	h.caller = caller
}

func (h *desktopHandler) HandleMethod(ctx context.Context, method string, params json.RawMessage) (interface{}, error) {
	switch method {
	case "tools/call":
		return h.handleToolsCall(ctx, params)
	case "brain/execute":
		return h.handleExecute(ctx, params)
	case "brain/metrics":
		return h.learner.ExportMetrics(), nil
	default:
		return nil, sidecar.ErrMethodNotFound
	}
}

func (h *desktopHandler) handleExecute(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var req sidecar.ExecuteRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return &sidecar.ExecuteResult{
			Status: "failed",
			Error:  fmt.Sprintf("parse request: %v", err),
		}, nil
	}

	if h.caller == nil {
		return &sidecar.ExecuteResult{
			Status:  "completed",
			Summary: "desktop brain ready (no LLM proxy available)",
			Turns:   0,
		}, nil
	}

	systemPrompt := `You are a specialist Desktop Brain for OS-level automation that happens
outside the browser: opening local files, listing windows, sending keyboard
shortcuts to the focused app.

Your tools:
  - desktop.open_path:    Open a file / dir / URL with the OS default handler
  - desktop.list_windows: Enumerate visible top-level windows (Linux/macOS only)
  - desktop.send_hotkey:  Send a key combination to the focused window

IMPORTANT:
- Many of these tools rely on system binaries (xdg-open / wmctrl / xdotool /
  osascript). When they're not installed, you'll get error_code =
  tool_execution_failed; surface this to the user instead of retrying blindly.
- Prefer Browser Brain for anything web-based. Use this brain only when the
  task clearly needs a native app (file manager, terminal, office suite).
- Don't send destructive shortcuts (e.g. Ctrl+Q) without explicit confirmation
  from the user.

WORKFLOW:
1. Understand whether the task really needs desktop automation
2. List windows if you need to find the target app
3. Execute the single action (open / hotkey)
4. Report what was done and any observable effect`

	maxTurns := 6
	if req.Budget != nil && req.Budget.MaxTurns > 0 {
		maxTurns = req.Budget.MaxTurns
	}

	start := time.Now()
	result := sidecar.RunAgentLoopWithContext(ctx, h.caller, h.registry, systemPrompt, req.Instruction, maxTurns, req.Context)
	h.learner.RecordOutcome(ctx, kernel.TaskOutcome{
		TaskType:  "desktop.execute",
		Success:   result.Status == "completed",
		Duration:  time.Since(start),
		ToolCalls: result.Turns,
	})
	return result, nil
}

func (h *desktopHandler) handleToolsCall(ctx context.Context, params json.RawMessage) (interface{}, error) {
	return sidecar.DispatchToolCall(ctx, params, h.registry, func(_ *executionpolicy.ExecutionSpec) (tool.Registry, error) {
		return h.registry, nil
	})
}

func main() {
	listen := ""
	for i, arg := range os.Args[1:] {
		if arg == "--listen" && i+1 < len(os.Args[1:]) {
			listen = os.Args[i+2]
		}
	}

	if _, err := license.CheckSidecar("brain-desktop", license.VerifyOptions{}); err != nil {
		fmt.Fprintf(os.Stderr, "brain-desktop: license: %v\n", err)
		os.Exit(1)
	}

	handler := newDesktopHandler()
	var err error
	if listen != "" {
		err = sidecar.ListenAndServe(listen, handler)
	} else {
		err = sidecar.Run(handler)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "brain-desktop: %v\n", err)
		os.Exit(1)
	}
}
