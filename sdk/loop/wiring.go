// Package loop — wiring.go
//
// # Why this file exists
//
// Phase 4 (IntentChain) and Phase 5 (Clarifier) are surgical additions to
// the Runner: each adds one nullable field that, when set, swaps in the
// new behavior and, when nil, leaves the legacy path untouched. That
// design kept the runner refactor minimal but pushed a question onto
// every assembly site:
//
//   - Should I create a Chain? With which parsers?
//   - Should I create a Clarifier? With what MaxAttempts? Reasoner true
//     or false (which depends on the provider)?
//
// Three production assembly sites currently construct loop.Runner
// (sdk/sidecar/loop.go for sub-agents, cmd/brain/agentpipe/invocation.go
// for the central brain, cmd/brain/command/resume.go for resume).
// Without this file each would need to repeat the same provider-aware
// switch. With this file each call site is one line:
//
//     runner.IntentChain, runner.Clarifier = loop.DefaultRecoveryFor(provider)
//
// All three behaviors of Phase 1's Capabilities feed in here:
//
//   - Reasoner=true → Clarifier.Reasoner=true (grace turn + short msgs)
//   - ToolChoiceSupport == None → Chain is the only line of defense, so
//     enable the full parser chain and bump Clarifier.MaxAttempts to 2.
//   - ToolChoiceSupport ≥ Required → native tool_choice already pins
//     the model; Chain still helps for occasional non-native outputs but
//     1 clarification attempt is plenty (model usually complies on retry).
//
// Tests can still construct bare Runner{Provider: mp} and skip wiring;
// they will get the legacy single-shot nudge path, which is what the
// existing compliance suite expects.

package loop

import (
	"github.com/leef-l/brain/sdk/llm"
	"github.com/leef-l/brain/sdk/loop/intent"
)

// DefaultRecoveryFor returns the (IntentChain, Clarifier) pair appropriate
// for the given provider. Both returned values are non-nil — callers that
// want the legacy path should leave the corresponding Runner fields nil
// instead of calling this helper.
//
// The choices are derived from llm.CapabilitiesOf(p):
//
//   - All providers get the default 6-parser IntentChain. Native models
//     (Claude / GPT-4) won't trigger it because they emit native tool_use,
//     but it costs nothing to have it ready when the rare malformed
//     response slips through.
//   - Reasoner providers (deepseek-reasoner, mimo, qwen-r) get
//     Clarifier.Reasoner=true so the first thinking-only turn is granted
//     a grace pass and subsequent reminders use the shorter template.
//   - Providers with ToolChoiceSupport == ToolChoiceNone (deepseek-chat,
//     mimo non-reasoner via openai shim) get MaxAttempts=2: tool_choice
//     can't coerce them, so the runner needs a second swing if the first
//     reminder doesn't produce a tool_use.
//   - Native providers with ToolChoiceSupport ≥ Required get MaxAttempts=1:
//     they almost always comply on the first nudge; further attempts are
//     just wasted wallclock.
func DefaultRecoveryFor(p llm.Provider) (*intent.Chain, *Clarifier) {
	caps := llm.CapabilitiesOf(p)

	chain := intent.NewDefaultChain()

	clar := &Clarifier{Reasoner: caps.Reasoner}
	switch caps.ToolChoiceSupport {
	case llm.ToolChoiceNone:
		// Provider ignores tool_choice; runner is the last line of defense.
		clar.MaxAttempts = 2
	default:
		// Native tool_choice handles 99% of "no tool" cases; one nudge
		// is enough for the residual.
		clar.MaxAttempts = 1
	}

	return chain, clar
}

// AttachDefaultRecovery is a one-liner convenience for assembly sites that
// have a *Runner in hand. Equivalent to:
//
//	runner.IntentChain, runner.Clarifier = DefaultRecoveryFor(runner.Provider)
//
// Returns the runner so it can be chained in a builder-style assembly
// expression. No-op when runner is nil or runner.Provider is nil
// (the latter happens in some test setups before Provider is wired in).
func AttachDefaultRecovery(runner *Runner) *Runner {
	if runner == nil || runner.Provider == nil {
		return runner
	}
	runner.IntentChain, runner.Clarifier = DefaultRecoveryFor(runner.Provider)
	return runner
}
