package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// handleSlashCommand processes a /command and returns (handled, shouldQuit).
func handleSlashCommand(input string, state *chatState) (bool, bool) {
	cmd := strings.ToLower(strings.TrimSpace(input))

	switch {
	case cmd == "/help":
		fmt.Println("  Key bindings:")
		fmt.Println(keybindingsHelp(state.kb))
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
		fmt.Println("  /keys              Show keybindings config path")
		fmt.Println("  /exit              Exit chat")
		fmt.Println()
		return true, false

	case cmd == "/clear":
		state.messages = nil
		state.turnCount = 0
		fmt.Println("  Conversation cleared.")
		fmt.Println()
		return true, false

	case cmd == "/history":
		userCount := 0
		for _, m := range state.messages {
			if m.Role == "user" {
				userCount++
			}
		}
		fmt.Printf("  %d messages (%d user turns)\n\n", len(state.messages), userCount)
		return true, false

	case cmd == "/mode":
		fmt.Printf("  Current mode: %s\n\n", state.mode.styledLabel())
		return true, false

	case strings.HasPrefix(cmd, "/mode "):
		newModeStr := strings.TrimSpace(cmd[6:])
		newMode, err := parseChatMode(newModeStr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  %v\n\n", err)
			return true, false
		}
		state.switchMode(newMode)
		fmt.Printf("  Switched to: %s\n\n", newMode.styledLabel())
		return true, false

	case cmd == "/tools":
		fmt.Println("  Available tools:")
		for _, ts := range state.opts.Tools {
			riskTag := ""
			if t, ok := state.registry.Lookup(ts.Name); ok {
				riskTag = fmt.Sprintf(" [%s]", t.Risk())
			}
			fmt.Printf("    %-30s%s\n", ts.Name, riskTag)
		}
		fmt.Println()
		return true, false

	case cmd == "/sandbox":
		if state.sandbox != nil {
			dirs := state.sandbox.Allowed()
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
		if state.sandbox != nil && arg != "" {
			abs, _ := filepath.Abs(arg)
			added := state.sandbox.Authorize(abs)
			fmt.Printf("  \033[32m> Authorized: %s\033[0m\n\n", added)
		}
		return true, false

	case cmd == "/keys":
		fmt.Printf("  Keybindings config: %s\n", keybindingsPath())
		fmt.Println("  Edit this file to customize key bindings, then restart chat.")
		fmt.Println()
		return true, false

	case cmd == "/exit" || cmd == "/quit" || cmd == "/q":
		return true, true
	}

	return false, false
}
