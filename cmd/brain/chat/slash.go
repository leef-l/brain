package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/leef-l/brain/cmd/brain/env"
	"github.com/leef-l/brain/cmd/brain/term"
	"github.com/leef-l/brain/sdk/agent"
	"github.com/leef-l/brain/sdk/kernel"
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
		fmt.Println("  /takeover          Enter manual takeover mode (you operate the browser)")
		fmt.Println("  /resume [note]     Signal the waiting agent to continue after human takeover")
		fmt.Println("  /abort  [note]     Tell the waiting agent to give up this step")
		fmt.Println("  /like  [note]      Mark the last turn as helpful (L3 learning)")
		fmt.Println("  /dislike [note]    Mark the last turn as unhelpful (L3 learning)")
		fmt.Println("  /keys              Show keybindings config path")
		fmt.Println("  /exit              Exit chat")
		fmt.Println()
		return true, false

	case cmd == "/takeover" || strings.HasPrefix(cmd, "/takeover "):
		// 手动触发 takeover:即便 LLM 没主动举手,用户也能强制挂起当前
		// 运行的 browser 任务让自己操作。需要当前有 running browser
		// task + ChatHumanCoordinator 可用。
		if state.HumanCoord == nil {
			fmt.Println("  \033[33mHuman coordinator not initialized.\033[0m")
			fmt.Println()
			return true, false
		}
		// 直接 emit 一个 fake requested 事件,让 chat 屏幕提示用户操作。
		// 这个不走 sidecar 反向 RPC,只是 UI 提示;真正挂起 agent 需要
		// sidecar LLM 发出 human.request_takeover tool call。
		// 如果有 pending 那是真挂起,否则只是手动进"人工模式"等 agent
		// 下一次检查页面时识别状态。
		if _, ok := state.HumanCoord.Pending(); ok {
			fmt.Println("  \033[33mA takeover is already pending. Use /resume or /abort.\033[0m")
		} else {
			fmt.Println("  \033[1;33m✋ Manual takeover mode\033[0m")
			fmt.Println("     No agent is currently waiting. You can operate the browser yourself.")
			fmt.Println("     Agent will continue next turn based on the new page state.")
		}
		fmt.Println()
		return true, false

	case cmd == "/resume" || strings.HasPrefix(cmd, "/resume "):
		if state.HumanCoord == nil {
			fmt.Println("  \033[33mNo human takeover in progress.\033[0m")
			fmt.Println()
			return true, false
		}
		if _, ok := state.HumanCoord.Pending(); !ok {
			fmt.Println("  \033[33mNo human takeover is waiting for resume.\033[0m")
			fmt.Println()
			return true, false
		}
		note := ""
		if len(input) > len("/resume") {
			note = strings.TrimSpace(input[len("/resume"):])
		}
		if state.HumanCoord.Resume(note) {
			fmt.Println("  \033[32m✓ Resumed — agent will continue.\033[0m")
		} else {
			fmt.Println("  \033[33mFailed to deliver resume signal.\033[0m")
		}
		fmt.Println()
		return true, false

	case cmd == "/abort" || strings.HasPrefix(cmd, "/abort "):
		if state.HumanCoord == nil {
			fmt.Println("  \033[33mNo human takeover in progress.\033[0m")
			fmt.Println()
			return true, false
		}
		if _, ok := state.HumanCoord.Pending(); !ok {
			fmt.Println("  \033[33mNo human takeover is waiting.\033[0m")
			fmt.Println()
			return true, false
		}
		note := ""
		if len(input) > len("/abort") {
			note = strings.TrimSpace(input[len("/abort"):])
		}
		if state.HumanCoord.Abort(note) {
			fmt.Println("  \033[33m✗ Aborted — agent will give up this step.\033[0m")
		} else {
			fmt.Println("  \033[33mFailed to deliver abort signal.\033[0m")
		}
		fmt.Println()
		return true, false

	case cmd == "/like" || strings.HasPrefix(cmd, "/like "):
		note := ""
		if len(input) > len("/like") {
			note = strings.TrimSpace(input[len("/like"):])
		}
		if state.Orchestrator != nil && state.Orchestrator.Learner() != nil {
			state.Orchestrator.Learner().RecordUserFeedback("chat_feedback", "like", 1.0)
			fmt.Println("  \033[32m✓ Feedback recorded: like\033[0m")
			if note != "" {
				fmt.Printf("  Note: %s\n", note)
			}
		} else {
			fmt.Println("  \033[33mLearning engine not available.\033[0m")
		}
		fmt.Println()
		return true, false

	case cmd == "/dislike" || strings.HasPrefix(cmd, "/dislike "):
		note := ""
		if len(input) > len("/dislike") {
			note = strings.TrimSpace(input[len("/dislike"):])
		}
		if state.Orchestrator != nil && state.Orchestrator.Learner() != nil {
			state.Orchestrator.Learner().RecordUserFeedback("chat_feedback", "dislike", 1.0)
			fmt.Println("  \033[31m✓ Feedback recorded: dislike\033[0m")
			if note != "" {
				fmt.Printf("  Note: %s\n", note)
			}
		} else {
			fmt.Println("  \033[33mLearning engine not available.\033[0m")
		}
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

	case cmd == "/workflow" || cmd == "/workflow help":
		fmt.Println("  /workflow <file.json>     Load and execute a workflow from file")
		fmt.Println("  /workflow '{\"nodes\":...}' Execute inline workflow JSON")
		fmt.Println()
		return true, false

	case strings.HasPrefix(cmd, "/workflow "):
		arg := strings.TrimSpace(cmd[len("/workflow "):])
		var data []byte
		var err error
		if strings.HasPrefix(arg, "{") {
			data = []byte(arg)
		} else {
			data, err = os.ReadFile(arg)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  \033[1;31m! Failed to read file: %v\033[0m\n\n", err)
				return true, false
			}
		}
		var wf kernel.Workflow
		if err := json.Unmarshal(data, &wf); err != nil {
			fmt.Fprintf(os.Stderr, "  \033[1;31m! Invalid workflow JSON: %v\033[0m\n\n", err)
			return true, false
		}
		if len(wf.Nodes) == 0 {
			fmt.Fprintln(os.Stderr, "  \033[1;31m! Workflow has no nodes\033[0m")
			return true, false
		}
		if wf.ID == "" {
			wf.ID = fmt.Sprintf("wf-chat-%d", time.Now().UnixNano())
		}
		if state.Orchestrator == nil {
			fmt.Fprintln(os.Stderr, "  \033[1;31m! No orchestrator available\033[0m")
			return true, false
		}
		fmt.Printf("  Executing workflow %s (%d nodes)...\n", wf.ID, len(wf.Nodes))
		reporter := func(eventType, nodeID, status, output, errMsg string) {
			switch eventType {
			case "workflow.node.started":
				fmt.Printf("  \033[2m→ node %s started\033[0m\n", nodeID)
			case "workflow.node.completed":
				fmt.Printf("  \033[32m✓ node %s completed\033[0m\n", nodeID)
			case "workflow.node.failed":
				fmt.Printf("  \033[1;31m✗ node %s failed: %s\033[0m\n", nodeID, errMsg)
			}
		}
		ctx := context.Background()
		result, err := state.Orchestrator.ExecuteWorkflow(ctx, &wf, reporter)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  \033[1;31m! Workflow failed: %v\033[0m\n\n", err)
			return true, false
		}
		fmt.Printf("  Workflow %s: %s\n", wf.ID, result.State)
		for nid, nr := range result.Nodes {
			icon := "\033[32m✓\033[0m"
			if nr.State != kernel.StateCompleted {
				icon = "\033[1;31m✗\033[0m"
			}
			fmt.Printf("    %s %s: %s", icon, nid, nr.State)
			if nr.Error != "" {
				fmt.Printf("  (%s)", nr.Error)
			}
			fmt.Println()
		}
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
