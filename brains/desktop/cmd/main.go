// Command brain-desktop-sidecar is the Desktop Brain sidecar binary.
//
// Desktop Brain is a specialist brain for OS-level automation outside the
// browser: opening local files, listing windows, sending keyboard shortcuts.
package main

import (
	"fmt"
	"os"

	"github.com/leef-l/brain/sdk/agent"
	"github.com/leef-l/brain/sdk/executionpolicy"
	"github.com/leef-l/brain/sdk/shared"
	"github.com/leef-l/brain/sdk/tool"
	"github.com/leef-l/brain/sdk/toolguard"
	"github.com/leef-l/brain/sdk/toolpolicy"
)

func main() {
	learner := NewDesktopBrainLearner()

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

	tb := shared.NewThinBrain(agent.KindDesktop, reg, systemPrompt, 6).
		WithLearner(learner).
		WithRegistryBuilder(buildRegistry)

	shared.MustRun(tb)
}

func buildRegistry(spec *executionpolicy.ExecutionSpec) (tool.Registry, error) {
	bounds, err := toolguard.NewBoundaries(spec)
	if err != nil {
		return nil, err
	}

	var reg tool.Registry = tool.NewMemRegistry()
	reg.Register(tool.WrapSandbox(tool.NewDesktopOpenPathTool(), bounds.Sandbox))
	reg.Register(tool.NewDesktopListWindowsTool())
	reg.Register(tool.NewDesktopSendHotkeyTool())
	reg.Register(tool.NewNoteTool("desktop"))
	return reg, nil
}
