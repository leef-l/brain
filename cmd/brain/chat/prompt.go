package chat

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/leef-l/brain/cmd/brain/env"
	"github.com/leef-l/brain/sdk/agent"
	"github.com/leef-l/brain/sdk/kernel"
	"github.com/leef-l/brain/sdk/tool"
)

// BuildSystemPrompt 输出 chat 模式下的核心 system 提示。
// 设计原则:仅声明角色与契约,具体工具行为约束写在各 tool 的 Schema.Description 里。
func BuildSystemPrompt(mode env.PermissionMode, sb *tool.Sandbox) string {
	var b strings.Builder
	b.WriteString("You are a coding assistant powered by BrainKernel.\n")
	b.WriteString(fmt.Sprintf("Today's date: %s\n", time.Now().Format("2006-01-02")))
	if sb != nil {
		b.WriteString(fmt.Sprintf("Working directory: %s (all paths relative; sandboxed).\n", sb.Primary()))
	}
	b.WriteString("Style: skip preambles ('好的,我来...', 'I'll help you...', 'Let me...'). Skip restating the request and listing what you'll do. Just call the tool or give the answer. Each sentence must add information.\n")

	switch mode {
	case env.ModePlan:
		b.WriteString("\nPLAN mode (read-only). Provide concrete file paths and code changes; do NOT call write/shell tools.\n")
	case env.ModeDefault:
		b.WriteString("\nAll tool ops require user confirmation. Briefly state intent before each tool call.\n")
	case env.ModeAcceptEdits:
		b.WriteString("\nFile edits auto-approved; shell needs confirmation.\n")
		b.WriteString("Self-check: if your text describes an action you'll take, emit the tool_use in this same turn. Text alone does nothing.\n")
	case env.ModeAuto:
		b.WriteString("\nSafe ops auto-approved.\n")
		b.WriteString("Self-check: if your text describes an action, emit tool_use in this same turn. When fully done, call task_complete or reply with text — don't keep checking.\n")
	case env.ModeRestricted:
		b.WriteString("\nRESTRICTED mode. Operate only within explicitly allowed files/operations.\n")
	case env.ModeBypassPermissions:
		b.WriteString("\nAll tool ops auto-approved.\n")
	}
	return b.String()
}

var BrainDescriptions = map[agent.Kind]string{
	"code":     "writes/edits/debugs code",
	"browser":  "web browse, click, type, screenshot — full human browser ops",
	"verifier": "runs tests, checks compile/output (read-only)",
	"fault":    "chaos / fault injection",
	"data":     "market data, prices, orderbooks, features, quality",
	"quant":    "trading: balances, positions, P&L, risk, strategies",
}

// BuildOrchestratorPrompt 输出 MACCS 中央大脑的角色契约。
// 设计原则:仅声明契约,具体行为约束写在各 tool 的 Schema.Description 里。
// 目标 token 预算 ≤ 500;单个工具描述天然随工具列表传给 LLM,零 prompt 增量。
func BuildOrchestratorPrompt(orch *kernel.Orchestrator, reg tool.Registry) string {
	if orch == nil || (!RegistryHasTool(reg, "central.delegate") && !RegistryHasTool(reg, "central.brain_manage") && !RegistryHasTool(reg, "central.start_human_demo")) {
		return ""
	}

	kinds := orch.AvailableKinds()
	if len(kinds) == 0 {
		return ""
	}
	sort.Slice(kinds, func(i, j int) bool { return kinds[i] < kinds[j] })

	specialists := make([]string, 0, len(kinds))
	for _, k := range kinds {
		desc := BrainDescriptions[k]
		if desc == "" {
			desc = fmt.Sprintf("%s tasks", k)
		}
		specialists = append(specialists, fmt.Sprintf("%s (%s)", k, desc))
	}

	var b strings.Builder
	b.WriteString("\n\n## Role: MACCS Central Orchestrator\n")
	b.WriteString("You PLAN and DELEGATE — specialists do the work. You cannot write/edit/delete files or run shell directly.\n\n")
	b.WriteString("Specialists: " + strings.Join(specialists, "; ") + ".\n\n")

	b.WriteString("Decision rule:\n")
	b.WriteString("- Multi-step / multi-file / will be tested → submit_workflow (one full DAG, depends_on for order, same-layer parallel; engine auto-injects _contract).\n")
	b.WriteString("- One-shot specialist task → delegate.\n")
	b.WriteString("- Read for your own understanding → read_file / list_files / search.\n")
	b.WriteString("- Done → task_complete with summary, then stop.\n\n")

	b.WriteString("Self-check before ending a turn: if your text describes an action you intend to take (in any wording), the tool_use block must be in this same response. Text alone changes nothing.\n")

	b.WriteString("Hard rules: never substitute shell_exec curl/wget for browser delegation; pass user-supplied values verbatim (no $placeholders); set render_mode=headed when the user wants to see the browser.\n")

	if RegistryHasTool(reg, "central.metacognition") {
		b.WriteString("Before non-trivial DAGs, query central.metacognition (brain_status / complexity / budget / memory) — cheaper than a failed workflow.\n")
	}
	if RegistryHasTool(reg, "central.brain_manage") {
		b.WriteString("Brain lifecycle (start/stop/status) → central.brain_manage, not delegate.\n")
	}
	if RegistryHasTool(reg, "central.start_human_demo") {
		b.WriteString("Explicit human-demo / takeover requests → central.start_human_demo.\n")
	}

	if notice := orch.DegradationNotice(); notice != "" {
		b.WriteString("\n" + notice + "\n")
	}
	if learner := orch.Learner(); learner != nil {
		if pref := learner.GetPreference("chat_feedback"); pref != nil {
			b.WriteString(fmt.Sprintf("\nUser feedback trend: %s (w=%.2f)\n", pref.Value, pref.Weight))
		}
	}

	return b.String()
}

func RegistryHasTool(reg tool.Registry, name string) bool {
	if reg == nil {
		return false
	}
	_, ok := reg.Lookup(name)
	return ok
}
