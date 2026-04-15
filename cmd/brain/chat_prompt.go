package main

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/leef-l/brain/sdk/agent"
	"github.com/leef-l/brain/sdk/kernel"
	"github.com/leef-l/brain/sdk/tool"
)

// buildSystemPrompt constructs the L1 system prompt for the given mode.
func buildSystemPrompt(mode chatMode, sb *tool.Sandbox) string {
	base := "You are a coding assistant powered by BrainKernel.\n"
	base += fmt.Sprintf("Today's date: %s\n", time.Now().Format("2006-01-02"))

	if sb != nil {
		base += fmt.Sprintf("\nCurrent working directory: %s\n", sb.Primary())
		base += "All file paths are relative to this directory. " +
			"You already know the current directory — do NOT use shell_exec to run `pwd`.\n" +
			"File operations are sandboxed to this directory. " +
			"If you need files outside it, tell the user and they can authorize the directory.\n"
	}

	switch mode {
	case modePlan:
		return base +
			"\nYou are in PLAN mode (read-only). You can read files and search code " +
			"to understand the codebase, but you CANNOT modify files or execute commands. " +
			"When the user asks you to make changes, provide a detailed plan with " +
			"file paths and specific code changes, but do NOT use write_file or " +
			"shell_exec tools."
	case modeDefault:
		return base +
			"\nYou have access to tools for reading files, writing files, " +
			"searching code, and executing shell commands. ALL tool operations " +
			"will require the user's explicit confirmation before proceeding. " +
			"Explain what you plan to do before using a tool."
	case modeAcceptEdits:
		return base +
			"\nYou have access to tools for reading files, writing files, " +
			"searching code, and executing shell commands. File edits are " +
			"auto-approved, but shell commands will require confirmation. " +
			"Explain what you plan to do before using a tool."
	case modeAuto:
		return base +
			"\nYou have access to tools for reading files, writing files, " +
			"searching code, and executing shell commands. Safe operations " +
			"are auto-approved. Use them freely. " +
			"Be concise. Briefly explain what you're doing before using a tool."
	case modeRestricted:
		return base +
			"\nYou are in RESTRICTED mode. All file reads, searches, creates, edits, deletes, " +
			"and command-produced mutations are enforced by file policy. " +
			"Only operate within the explicitly allowed files and operations. " +
			"Be concise. Briefly explain what you're doing before using a tool."
	case modeBypassPermissions:
		return base +
			"\nYou have access to tools for reading files, writing files, " +
			"searching code, and executing shell commands. All operations " +
			"proceed without confirmation. Use them freely. " +
			"Be concise. Briefly explain what you're doing before using a tool."
	}
	return base
}

// brainDescriptions maps brain kinds to their natural-language descriptions.
// Used by buildOrchestratorPrompt to dynamically generate delegation instructions.
var brainDescriptions = map[agent.Kind]string{
	"code":     "For writing, editing, and debugging code. Delegate coding tasks to this brain.",
	"browser":  "For web browsing, UI testing, and interacting with web pages. Can fully simulate human browser operations (click, type, scroll, drag, hover, screenshot, etc.).",
	"verifier": "For running tests, verifying code changes, and checking output. This brain is read-only and independent — it does not participate in implementation.",
	"fault":    "For chaos engineering and fault injection testing.",
	"data":     "For real-time market data: instrument discovery, prices, order books, features (192-dim vectors), data quality. Delegate data queries to this brain.",
	"quant":    "For quantitative trading: account balances, positions, trade history, daily/monthly P&L, strategy stats, risk status. Delegate trading queries and operations to this brain.",
}

// buildOrchestratorPrompt appends delegation instructions when specialist
// brains are available. Descriptions are generated dynamically based on
// which brains are actually discovered — no hardcoded brain list.
func buildOrchestratorPrompt(orch *kernel.Orchestrator, reg tool.Registry) string {
	if orch == nil || !registryHasTool(reg, "central.delegate") {
		return ""
	}

	kinds := orch.AvailableKinds()
	if len(kinds) == 0 {
		return ""
	}

	// Sort for stable output.
	sort.Slice(kinds, func(i, j int) bool { return kinds[i] < kinds[j] })

	names := make([]string, len(kinds))
	for i, k := range kinds {
		names[i] = string(k)
	}

	prompt := "\n\n## Specialist Brain Delegation\n\n"
	prompt += "You have access to specialist brains that can handle specific tasks. "
	prompt += fmt.Sprintf("Available specialists: %s.\n\n", strings.Join(names, ", "))
	prompt += "Use the `central.delegate` tool to delegate tasks to the appropriate specialist:\n"

	for _, kind := range kinds {
		desc, ok := brainDescriptions[kind]
		if !ok {
			desc = fmt.Sprintf("Specialist brain for %s tasks.", kind)
		}
		prompt += fmt.Sprintf("- **%s**: %s\n", kind, desc)
	}

	// Check which specialist tools are directly available as bridge tools.
	hasQuantTools := registryHasTool(reg, "quant.global_portfolio")
	hasDataTools := registryHasTool(reg, "data.get_snapshot")

	if hasQuantTools || hasDataTools {
		prompt += "\n### Direct specialist tools\n\n"
		prompt += "You can call these specialist tools directly (no delegation needed):\n\n"
		if hasQuantTools {
			prompt += "**Quant tools** — for trading data queries:\n"
			prompt += "- `quant.global_portfolio` — all accounts equity, positions, health\n"
			prompt += "- `quant.account_status` — single account balance and positions\n"
			prompt += "- `quant.daily_pnl` — today's P&L per trading unit\n"
			prompt += "- `quant.trade_history` — historical trades for a unit\n"
			prompt += "- `quant.trace_query` — signal audit trail\n"
			prompt += "- `quant.strategy_weights` — strategy configuration\n"
			prompt += "- `quant.global_risk_status` — risk limits and usage\n"
			prompt += "- `quant.pause_trading` / `quant.resume_trading` — emergency controls\n"
			prompt += "- `quant.account_pause` / `quant.account_resume` — per-account controls\n"
			prompt += "- `quant.account_close_all` / `quant.force_close` — position closure (dangerous)\n"
			prompt += "- `quant.backtest_start` — run backtest on historical data\n\n"
		}
		if hasDataTools {
			prompt += "**Data tools** — for market data queries:\n"
			prompt += "- `data.get_snapshot` — real-time price, spread, orderbook imbalance\n"
			prompt += "- `data.get_candles` — historical K-line data\n"
			prompt += "- `data.get_feature_vector` — 192-dim feature vector with regime detection\n"
			prompt += "- `data.provider_health` — data source health and latency\n"
			prompt += "- `data.validation_stats` — data quality metrics\n"
			prompt += "- `data.active_instruments` — active trading instruments\n"
			prompt += "- `data.backfill_status` — historical backfill progress\n"
			prompt += "- `data.replay_start` / `data.replay_stop` — historical replay (backtest mode)\n\n"
		}
	}

	prompt += "\nWhen you receive a task:\n"
	prompt += "1. For trading/data queries, use the specialist tools directly\n"
	prompt += "2. For complex multi-step tasks, use `central.delegate` to delegate to specialists\n"
	prompt += "3. After code changes, delegate verification to the verifier brain\n"
	prompt += "4. Summarize the results to the user\n\n"
	prompt += "If a tool call fails (specialist unavailable), try `central.delegate` as fallback.\n"

	// Add degradation notice if any specialists are missing.
	if notice := orch.DegradationNotice(); notice != "" {
		prompt += "\n" + notice + "\n"
	}

	return prompt
}

func registryHasTool(reg tool.Registry, name string) bool {
	if reg == nil {
		return false
	}
	_, ok := reg.Lookup(name)
	return ok
}
