// Package loop — clarification.go
//
// # Why this file exists
//
// The Agent Loop has always relied on a single "nudge" message to recover
// when the LLM emits text but no tool_use block. The legacy implementation
// (announcementNudgeMessageFor) sends one canned reminder, then on the
// next 0-tool turn gives up and marks the run completed. This was good
// enough for native-tool-call models (Claude / GPT-4) where 1 nudge is
// almost always sufficient, but it fails on:
//
//   - DeepSeek-V4 / Mimo / Qwen text models, which silently ignore
//     tool_choice=required and routinely produce announcement-only text.
//     They need targeted, repeated coaching — not one generic reminder.
//   - Reasoner-class models (deepseek-reasoner, mimo-reasoner, qwen-r),
//     which legitimately spend turn 1 in pure thinking. The legacy nudge
//     wastes a turn telling them to "stop thinking and act" when the
//     correct behavior is to wait one more turn.
//
// Phase 5 introduces Clarifier: a structured replacement for the single
// nudge that:
//
//  1. Diagnoses *why* the LLM produced 0 tool_use blocks (thinking only,
//     announcement text, question to user, or generic text), then issues
//     a message tailored to that diagnosis.
//  2. Tracks attempts per-run with a configurable cap (MaxAttempts), so
//     pathological models cannot pin the loop forever.
//  3. Exposes a Reasoner mode that grants one extra grace turn before
//     starting to nudge, and uses shorter messages to save tokens on
//     reasoner providers (which are billed per thinking token).
//
// The runner falls back to the legacy nudge when r.Clarifier is nil, so
// callers that haven't opted in are unaffected.
//
// # Wire contract
//
// The runner calls Clarifier.NextMessage at the moment it detects a
// 0-tool-use response that should normally trigger a nudge. The Clarifier
// returns:
//
//   - (msg, true)  → append `msg` to history, run another turn.
//   - (msg, false) → max attempts reached; runner SHOULD complete the run
//                    with whatever the LLM has already produced (which is
//                    typically a polite refusal or a question for the user).
//
// State (attempt count) is held inside ClarifierState, which the runner
// allocates per-run in the Execute closure. Nothing about the Clarifier
// itself is mutated, so a single instance is safe to share across runs
// and goroutines.

package loop

import (
	"strings"

	"github.com/leef-l/brain/sdk/llm"
)

// ClarifyKind enumerates the diagnoses Clarifier.classify can return.
// Each kind maps to a tailored reminder template; adding a new kind only
// requires adding a classify rule and a template branch.
type ClarifyKind int

const (
	// KindGenericText — text output that doesn't fit any other bucket.
	// Falls back to the legacy nudge message.
	KindGenericText ClarifyKind = iota

	// KindThinkingOnly — the response contains only `thinking` blocks
	// (no text, no tool_use). Common on reasoner models when the first
	// turn budget runs out mid-plan. Reminder asks the model to commit
	// the plan into a tool_use call.
	KindThinkingOnly

	// KindAnnouncementText — the LLM announced an action ("I'll write
	// game.html", "Let me create the file", "现在开始写代码") but did
	// not emit a tool_use block to back it up. The classic
	// announce-without-act failure on DeepSeek/Mimo. Reminder is direct:
	// "you said you'd do X, now actually emit the tool_use".
	KindAnnouncementText

	// KindQuestion — the LLM ended the turn with a question for the user
	// (text ends with "?" / starts with "Could you" / "Do you want" etc).
	// This is a legitimate response — the runner should not just nudge,
	// it should suggest task_complete with the question as the answer.
	KindQuestion
)

// ClarifyContext bundles the per-turn inputs Clarifier needs to pick a
// message. We pass it as a struct so adding new signals (e.g. the failed
// tool's schema, the target file path) does not break callers.
type ClarifyContext struct {
	// Content is the LLM's last response (the one with no tool_use blocks).
	Content []llm.ContentBlock

	// IsCentral mirrors RunOptions.ChatCentralBrain — the central brain
	// gets workflow-oriented hints (delegate/submit_workflow), while
	// specialist sub-agents get action-oriented hints (write_file/shell_exec).
	IsCentral bool

	// TurnIndex is the 1-based turn number the response came from.
	// Reasoner mode uses this to grant one extra grace turn at TurnIndex==1.
	TurnIndex int
}

// ClarifierState tracks per-run progress through the clarification budget.
// The runner owns one ClarifierState per Run and passes it back into each
// NextMessage call. Stateless Clarifier + per-run state means a single
// Clarifier instance is safe to share across all concurrent runs.
type ClarifierState struct {
	// Attempts counts how many clarification messages have been issued so
	// far in this run. Compared against Clarifier.MaxAttempts.
	Attempts int
}

// Clarifier produces tailored clarification messages and enforces an
// upper bound on attempts per run. Zero value is usable: it behaves like
// the legacy single-nudge mechanism with MaxAttempts=1 and no reasoner
// indulgence.
type Clarifier struct {
	// MaxAttempts caps the number of clarification rounds per run.
	// Defaults to 2 when zero — one targeted reminder, then one final
	// "you must act now" before letting the run complete. Setting this
	// to 1 reproduces the legacy nudge behavior; setting it >2 is
	// rarely useful (LLMs that don't comply by attempt 2 won't comply
	// by attempt 5 either, and burning the wallclock is worse than
	// surfacing the failure to the user).
	MaxAttempts int

	// Reasoner enables reasoner-aware behavior:
	//   - Turn 1 with only thinking blocks does NOT count against
	//     MaxAttempts (the reasoner gets one free grace turn).
	//   - Generated messages are shorter to save tokens on per-thinking-
	//     token billing (deepseek-reasoner / mimo / qwen-r).
	//
	// Set this to llm.CapabilitiesOf(provider).Reasoner from the assembly
	// site (typically sidecar / chat factory).
	Reasoner bool
}

// NextMessage diagnoses the LLM response, picks a tailored reminder, and
// updates ClarifierState. Returns (msg, true) when the runner should
// inject `msg` and run another turn. Returns (zero, false) when the
// attempt budget is exhausted — the runner SHOULD complete the run.
//
// When c is nil, the runner is expected to fall back to its legacy
// announcementNudgeMessageFor + single-shot nudgedAnnouncement gate.
func (c *Clarifier) NextMessage(state *ClarifierState, cc ClarifyContext) (llm.Message, bool) {
	if c == nil || state == nil {
		return llm.Message{}, false
	}
	max := c.MaxAttempts
	if max <= 0 {
		max = 2
	}

	kind := c.classify(cc.Content)

	// Reasoner grace: turn 1 + thinking-only does not consume budget.
	// Without this, reasoner models that spend the whole first turn in
	// thinking get nudged immediately, which both wastes the thinking
	// tokens already paid for and jolts the model out of its plan.
	if c.Reasoner && cc.TurnIndex <= 1 && kind == KindThinkingOnly {
		// Issue a soft prompt without incrementing attempts.
		return c.message(kind, cc.IsCentral, /*soft*/ true), true
	}

	if state.Attempts >= max {
		return llm.Message{}, false
	}
	state.Attempts++

	return c.message(kind, cc.IsCentral, /*soft*/ false), true
}

// classify inspects the content blocks and decides which clarification
// template to use. The order of checks matters — more-specific signals
// (question marks, announcement phrases) win over more-general ones
// (any thinking block at all).
func (c *Clarifier) classify(blocks []llm.ContentBlock) ClarifyKind {
	var (
		hasText     bool
		hasThinking bool
		text        strings.Builder
	)
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if strings.TrimSpace(b.Text) != "" {
				hasText = true
				text.WriteString(b.Text)
				text.WriteByte(' ')
			}
		case "thinking":
			if strings.TrimSpace(b.Text) != "" {
				hasThinking = true
			}
		}
	}

	t := strings.ToLower(strings.TrimSpace(text.String()))

	// 1. Question to the user — text ends with "?" or starts with a
	//    common interrogative. Treat as a legitimate response.
	if hasText && looksLikeQuestion(t) {
		return KindQuestion
	}

	// 2. Announce-without-act — text contains explicit "I'll do X" /
	//    "我要做 X" / "let me X" phrasing. These are the failure mode
	//    DeepSeek / Mimo produce most often.
	if hasText && looksLikeAnnouncement(t) {
		return KindAnnouncementText
	}

	// 3. Pure thinking with no text at all — typical reasoner first-turn
	//    pattern when the plan is long.
	if hasThinking && !hasText {
		return KindThinkingOnly
	}

	// 4. Default — generic text response that doesn't match any pattern.
	return KindGenericText
}

// message renders the user-role reminder for the given diagnosis.
// `soft=true` is used for the reasoner grace turn — a gentler "go ahead
// and act" pat instead of a corrective nudge.
func (c *Clarifier) message(kind ClarifyKind, isCentral bool, soft bool) llm.Message {
	body := c.body(kind, isCentral, soft)
	return llm.Message{
		Role: "user",
		Content: []llm.ContentBlock{{
			Type: "text",
			Text: body,
		}},
	}
}

func (c *Clarifier) body(kind ClarifyKind, isCentral bool, soft bool) string {
	hint := actionHint(isCentral)

	if c.Reasoner {
		// Shorter messages on reasoner providers — every token is paid for
		// at the thinking-token rate and we don't want to bury the action
		// hint under boilerplate.
		switch kind {
		case KindThinkingOnly:
			if soft {
				return "Plan looks ready. Now emit a tool_use block to act on it.\n" + hint
			}
			return "[reminder] Thinking only is not work. Emit a tool_use block now.\n" + hint
		case KindAnnouncementText:
			return "[reminder] You announced an action but emitted no tool_use. Send the tool_use block in this turn.\n" + hint
		case KindQuestion:
			return "[reminder] If you need user input, call task_complete with the question. Otherwise emit a tool_use block.\n" + hint
		default:
			return "[reminder] Emit a tool_use block — text alone changes nothing.\n" + hint
		}
	}

	// Non-reasoner: longer, more explicit messages. The legacy nudge
	// template lives here so behavior with MaxAttempts=1 + Reasoner=false
	// is byte-identical to announcementNudgeMessageFor.
	switch kind {
	case KindThinkingOnly:
		if soft {
			return "Your plan is in the thinking block — go ahead and act on it now by emitting a tool_use call.\n" + hint
		}
		return "[system reminder] Your previous response was thinking-only. " +
			"Thinking is not work in this system; only tool_use blocks change anything. " +
			"Emit a tool_use block in this turn.\n" + hint
	case KindAnnouncementText:
		return "[system reminder] Your previous response announced an action (\"I'll write…\", \"Let me create…\") " +
			"but contained no tool_use block. In this system, the announcement does nothing — only the tool_use call " +
			"actually performs the work. Emit the matching tool_use block in THIS turn.\n" + hint
	case KindQuestion:
		return "[system reminder] Your previous response asked the user a question without emitting a tool_use block. " +
			"If you genuinely need clarification, call task_complete with your question as the summary. " +
			"Otherwise emit a tool_use block to make progress.\n" + hint
	default:
		return "[system reminder] Your previous response had no tool_use block — only text. " +
			"In this system, text alone changes nothing; the user sees text but no work happens. " +
			"You MUST emit a tool_use block in this turn. Choose one:\n" + hint + "\n" +
			"Do not write another planning paragraph. Emit a tool_use block — that is the only way work gets done."
	}
}

// actionHint returns the role-specific bullet list of the most common
// tools, mirroring announcementNudgeMessageFor so the wording stays
// consistent with the legacy path.
func actionHint(isCentral bool) string {
	if isCentral {
		return "  • If the user wants you to do/build/make something: call submit_workflow (multi-step) or delegate (one-shot) NOW with concrete arguments.\n" +
			"  • If you need to read context first: call read_file / list_files / search.\n" +
			"  • If the request is unclear or impossible: call note to record your question, then briefly explain to the user.\n" +
			"  • If genuinely done: call task_complete with a summary."
	}
	return "  • If the user wants you to write/create code: call write_file / edit_file with the actual content.\n" +
		"  • If you need to inspect first: call read_file / list_files / search.\n" +
		"  • If you need to run a command: call shell_exec.\n" +
		"  • If genuinely done: call task_complete with a summary."
}

// looksLikeQuestion returns true when the lowercased trimmed text reads
// like a question to the user. Conservative on purpose — false positives
// here mean we suggest task_complete to a model that is just rambling,
// which is harmless; false negatives mean we treat a real question as
// announce-without-act, which is also harmless (the model will likely
// repeat the question).
func looksLikeQuestion(text string) bool {
	if text == "" {
		return false
	}
	if strings.HasSuffix(text, "?") || strings.HasSuffix(text, "？") {
		return true
	}
	starters := []string{
		"could you ", "would you ", "do you want ", "should i ", "shall i ",
		"can you clarify", "please clarify",
		"请问", "请告诉我", "想问一下", "需要你确认",
	}
	for _, s := range starters {
		if strings.HasPrefix(text, s) {
			return true
		}
	}
	return false
}

// looksLikeAnnouncement returns true when the text contains an explicit
// "I'll do X" style declaration. Kept conservative — we only want to
// catch the most common DeepSeek/Mimo failure mode, not flag every
// sentence with a verb.
func looksLikeAnnouncement(text string) bool {
	if text == "" {
		return false
	}
	phrases := []string{
		"i'll ", "i will ", "let me ", "let's ",
		"i'm going to ", "i am going to ",
		"now i will", "now i'll", "next, i", "first, i",
		"我要", "我将", "我来", "我会", "我现在",
		"接下来我", "下面我", "现在我", "让我",
		"立即", "立刻", "马上",
	}
	for _, p := range phrases {
		if strings.Contains(text, p) {
			return true
		}
	}
	return false
}
