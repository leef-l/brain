package chat

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/leef-l/brain/cmd/brain/env"
	"github.com/leef-l/brain/cmd/brain/term"
	"github.com/leef-l/brain/sdk/agent"
)

func HandleSlashCommand(input string, state *State) (bool, bool) {
	cmd := strings.ToLower(strings.TrimSpace(input))

	switch {
	case cmd == "/help":
		fmt.Println("  Key bindings:")
		fmt.Println(term.KeybindingsHelp(state.KB))
		fmt.Println()
		fmt.Println("  Slash commands:")
		fmt.Println("  /help              Show this help")
		fmt.Println("  /clear             Clear conversation history")
		fmt.Println("  /history           Show conversation turn count")
		fmt.Println("  /mode              Show current mode")
		fmt.Println("  /mode <name>       Switch mode (plan, default, accept-edits, auto, restricted, bypass-permissions)")
		fmt.Println("  /tools             List available tools")
		fmt.Println("  /sandbox           Show sandbox (allowed directories)")
		fmt.Println("  /sandbox <dir>     Authorize an additional directory")
		fmt.Println("  /brain             List specialist brains and status")
		fmt.Println("  /brain start <kind> Start a specialist brain sidecar")
		fmt.Println("  /brain stop <kind>  Stop a running sidecar (or 'all')")
		fmt.Println("  /keys              Show keybindings config path")
		fmt.Println("  /exit              Exit chat")
		fmt.Println()
		return true, false

	case cmd == "/clear":
		state.Messages = nil
		state.TurnCount = 0
		fmt.Println("  Conversation cleared.")
		fmt.Println()
		return true, false

	case cmd == "/history":
		userCount := 0
		for _, m := range state.Messages {
			if m.Role == "user" {
				userCount++
			}
		}
		fmt.Printf("  %d messages (%d user turns)\n\n", len(state.Messages), userCount)
		return true, false

	case cmd == "/mode":
		fmt.Printf("  Current mode: %s\n\n", state.Mode.StyledLabel())
		return true, false

	case strings.HasPrefix(cmd, "/mode "):
		newModeStr := strings.TrimSpace(cmd[6:])
		newMode, err := env.ParsePermissionMode(newModeStr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  %v\n\n", err)
			return true, false
		}
		state.SwitchMode(newMode)
		fmt.Printf("  Switched to: %s\n\n", newMode.StyledLabel())
		return true, false

	case cmd == "/tools":
		fmt.Println("  可用工具:")
		for _, ts := range state.Opts.Tools {
			riskTag := ""
			if t, ok := state.Registry.Lookup(ts.Name); ok {
				riskTag = RiskLabel(t.Risk())
			}
			desc := ts.Description
			if len(desc) > 50 {
				desc = desc[:47] + "..."
			}
			if desc != "" {
				fmt.Printf("    %-34s %s  %s\n", ts.Name, riskTag, desc)
			} else {
				fmt.Printf("    %-34s %s\n", ts.Name, riskTag)
			}
		}
		fmt.Println()
		return true, false

	case cmd == "/sandbox":
		if state.Sandbox != nil {
			dirs := state.Sandbox.Allowed()
			fmt.Printf("  \033[1mSandbox directories:\033[0m\n")
			for i, d := range dirs {
				tag := "authorized"
				if i == 0 {
					tag = "primary"
				}
				fmt.Printf("    %s \033[2m(%s)\033[0m\n", d, tag)
			}
		} else {
			fmt.Println("  Sandbox: disabled")
		}
		fmt.Println()
		return true, false

	case strings.HasPrefix(cmd, "/sandbox "):
		arg := strings.TrimSpace(cmd[9:])
		if state.Sandbox != nil && arg != "" {
			abs, _ := filepath.Abs(arg)
			added := state.Sandbox.Authorize(abs)
			fmt.Printf("  \033[32m> Authorized: %s\033[0m\n\n", added)
		}
		return true, false

	case cmd == "/keys":
		fmt.Printf("  Keybindings config: %s\n", term.KeybindingsPath())
		fmt.Println("  Edit this file to customize key bindings, then restart chat.")
		fmt.Println()
		return true, false

	case cmd == "/brain", cmd == "/brain list", cmd == "/brain ls":
		handleBrainList(state)
		return true, false

	case strings.HasPrefix(cmd, "/brain start "):
		kind := strings.TrimSpace(cmd[len("/brain start "):])
		handleBrainStart(state, kind)
		return true, false

	case strings.HasPrefix(cmd, "/brain stop "):
		kind := strings.TrimSpace(cmd[len("/brain stop "):])
		handleBrainStop(state, kind)
		return true, false

	case cmd == "/brain help":
		fmt.Println("  /brain              List specialist brains and status")
		fmt.Println("  /brain start <kind> Start a specialist brain sidecar")
		fmt.Println("  /brain start all    Start all available sidecars")
		fmt.Println("  /brain stop <kind>  Stop a specialist brain sidecar")
		fmt.Println("  /brain stop all     Stop all running sidecars")
		fmt.Println()
		return true, false

	case cmd == "/exit" || cmd == "/quit" || cmd == "/q":
		return true, true
	}

	return false, false
}

func handleBrainList(state *State) {
	if state.Orchestrator == nil {
		fmt.Println("  No orchestrator (solo mode, no specialist brains)")
		fmt.Println()
		return
	}
	brains := state.Orchestrator.ListBrains()
	if len(brains) == 0 {
		fmt.Println("  No specialist brains available")
		fmt.Println()
		return
	}
	sort.Slice(brains, func(i, j int) bool { return brains[i].Kind < brains[j].Kind })
	fmt.Println("  Specialist brains:")
	for _, b := range brains {
		status := "\033[2m●\033[0m stopped"
		if b.Running {
			status = "\033[32m●\033[0m running"
		}
		fmt.Printf("    %-12s %s", b.Kind, status)
		if b.Binary != "" {
			fmt.Printf("  \033[2m(%s)\033[0m", b.Binary)
		}
		fmt.Println()
	}
	fmt.Println()
}

func handleBrainStart(state *State, kind string) {
	if state.Orchestrator == nil {
		fmt.Println("  \033[1;31m! No orchestrator available\033[0m")
		fmt.Println()
		return
	}
	if kind == "all" {
		brains := state.Orchestrator.ListBrains()
		if len(brains) == 0 {
			fmt.Println("  No specialist brains available")
			fmt.Println()
			return
		}
		fmt.Println("  Starting all sidecars...")
		home, _ := os.UserHomeDir()
		for _, b := range brains {
			if b.Running {
				fmt.Printf("  \033[2m- %s already running\033[0m\n", b.Kind)
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			if err := state.Orchestrator.StartBrain(ctx, agent.Kind(b.Kind)); err != nil {
				fmt.Printf("  \033[1;31m! %s failed: %v\033[0m\n", b.Kind, err)
			} else {
				fmt.Printf("  \033[32m✓ %s started\033[0m\n", b.Kind)
			}
			cancel()
		}
		fmt.Printf("  \033[2mLogs: %s/.brain/logs/\033[0m\n\n", home)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	fmt.Printf("  Starting %s sidecar...\n", kind)
	if err := state.Orchestrator.StartBrain(ctx, agent.Kind(kind)); err != nil {
		fmt.Printf("  \033[1;31m! Failed: %v\033[0m\n\n", err)
		return
	}
	home, _ := os.UserHomeDir()
	fmt.Printf("  \033[32m✓ %s sidecar started\033[0m\n", kind)
	fmt.Printf("  \033[2mLog: %s/.brain/logs/%s.log\033[0m\n\n", home, kind)
}

func handleBrainStop(state *State, kind string) {
	if state.Orchestrator == nil {
		fmt.Println("  \033[1;31m! No orchestrator available\033[0m")
		fmt.Println()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if kind == "all" {
		fmt.Println("  Stopping all sidecars...")
		if err := state.Orchestrator.Shutdown(ctx); err != nil {
			fmt.Printf("  \033[1;31m! Error: %v\033[0m\n\n", err)
			return
		}
		fmt.Println("  \033[32m✓ All sidecars stopped\033[0m")
		fmt.Println()
		return
	}
	fmt.Printf("  Stopping %s sidecar...\n", kind)
	if err := state.Orchestrator.StopBrain(ctx, agent.Kind(kind)); err != nil {
		fmt.Printf("  \033[1;31m! Failed: %v\033[0m\n\n", err)
		return
	}
	fmt.Printf("  \033[32m✓ %s sidecar stopped\033[0m\n\n", kind)
}

func RiskLabel[T ~string](r T) string {
	switch string(r) {
	case "safe":
		return "\033[32m[安全]\033[0m"
	case "low":
		return "\033[32m[低危]\033[0m"
	case "med":
		return "\033[33m[中危]\033[0m"
	case "high":
		return "\033[1;31m[高危]\033[0m"
	case "critical":
		return "\033[1;35m[危险]\033[0m"
	default:
		return fmt.Sprintf("[%s]", string(r))
	}
}
