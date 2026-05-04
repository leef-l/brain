// Package llm — Capabilities models per-provider behavioral characteristics
// that the upper layers (loop.Runner, IntentParser, ClarificationLoop)
// MUST adapt to in order to remain stable across vendors.
//
// Why a separate concept (not just provider name):
//   - Provider name (e.g. "openai") covers many wire-compatible vendors
//     (DeepSeek, Mimo, Qwen, Doubao, ...) that diverge on whether they
//     accept tool_choice, whether they emit reasoning_content, and
//     whether their finetune leans into "announce-without-act" patterns.
//   - Model name alone is not enough either: deepseek-chat and
//     deepseek-reasoner share the same wire but have very different
//     behavioral envelopes (reasoner 在思考阶段不发任何字节,纯 text
//     响应频率高)。
//   - Capabilities are the orthogonal dimension that runner cares about.
//
// Capabilities are *declarative*: the value is set when the Provider is
// constructed (often inferred from baseURL + model by the assembling
// layer in cmd/brain/provider) and stays immutable for the Provider's
// lifetime. The runner reads them via the optional CapabilityAware
// interface — providers that don't implement it fall back to the
// conservative DefaultCapabilities (treat as "openai-compatible no
// tool_choice, non-reasoner, native tool_use").
package llm

import "strings"

// ToolChoiceMode enumerates the levels of tool_choice support a provider
// reports. Higher value implies more flexibility for the runner to force
// behavior.
type ToolChoiceMode int

const (
	// ToolChoiceNone means the provider IGNORES the tool_choice request
	// field. Runner MUST NOT rely on tool_choice="required" — fall back
	// to IntentParser + Clarification at the loop level.
	// Examples: DeepSeek, Mimo, Qwen (most v1/v2 endpoints).
	ToolChoiceNone ToolChoiceMode = iota

	// ToolChoiceAuto means the provider accepts tool_choice="auto" / "none"
	// but does NOT enforce "required". Runner can hint, not coerce.
	ToolChoiceAuto

	// ToolChoiceRequired means the provider honors tool_choice="required"
	// (or its provider-specific equivalent: Anthropic "any") and will
	// reliably emit a tool_use block when set. Runner SHOULD use this
	// for sub-agent first turn to root out announce-without-act patterns.
	// Examples: Anthropic claude-*, OpenAI gpt-4*, Azure OpenAI.
	ToolChoiceRequired

	// ToolChoiceSpecific means the provider also honors a specific tool
	// name in tool_choice. Implies ToolChoiceRequired.
	ToolChoiceSpecific
)

// String renders ToolChoiceMode for debug/logging.
func (m ToolChoiceMode) String() string {
	switch m {
	case ToolChoiceNone:
		return "none"
	case ToolChoiceAuto:
		return "auto"
	case ToolChoiceRequired:
		return "required"
	case ToolChoiceSpecific:
		return "specific"
	default:
		return "unknown"
	}
}

// Capabilities is the immutable behavioral profile a Provider exposes to
// the runner. All fields are advisory — the runner uses them to pick
// strategies but MUST still defend against any actual response shape.
//
// JSON tags exist so this struct can travel over the sidecar↔host RPC
// channel (protocol.MethodLLMCapabilities). ToolChoiceSupport is
// transmitted as the underlying int value of the iota; both ends use the
// same Go type so the int is a stable wire shape.
type Capabilities struct {
	// Family identifies the model lineage for telemetry and routing
	// decisions. Examples: "anthropic-claude", "openai-gpt", "deepseek",
	// "deepseek-reasoner", "mimo", "qwen", "qwen-reasoner". Empty string
	// means "unknown / default openai-compatible".
	Family string `json:"family,omitempty"`

	// NativeToolCall reports whether the provider can emit structured
	// tool_use blocks (Anthropic style) or tool_calls (OpenAI style)
	// out-of-the-box. When false the runner MUST rely entirely on
	// IntentParser to extract tool intents from free-form text.
	//
	// All current production providers report true; this field exists
	// to document the contract and to allow future "text-only" providers
	// (e.g. raw completion endpoints) to plug in without changes upstream.
	NativeToolCall bool `json:"native_tool_call"`

	// ToolChoiceSupport declares how strongly the provider honors the
	// tool_choice request field. See ToolChoiceMode for the levels.
	ToolChoiceSupport ToolChoiceMode `json:"tool_choice_support"`

	// Reasoner reports whether the model has a separate "thinking" phase
	// that frequently produces text-only responses on the first turn
	// before deciding to call tools. The runner relaxes its
	// announce-without-act heuristics for reasoners (allowing the first
	// turn to be pure thinking without immediate nudge).
	//
	// Examples: deepseek-reasoner, mimo-v2.5-pro, qwen-reasoner.
	Reasoner bool `json:"reasoner,omitempty"`

	// MaxParallelTools is the maximum number of tool_use blocks the
	// provider reliably emits in a single response. 0 means "unknown,
	// assume 1". This is a hint for BatchPlanner, not a hard contract.
	MaxParallelTools int `json:"max_parallel_tools,omitempty"`

	// EmitsReasoningContent reports whether the provider returns a
	// separate reasoning_content / thinking field that must be preserved
	// across turns (DeepSeek-Reasoner specific quirk). When true, the
	// provider's request builder MUST round-trip thinking blocks.
	EmitsReasoningContent bool `json:"emits_reasoning_content,omitempty"`

	// PrefersStructuredOutput is a hint that the model leans on tool_use
	// rather than text. False means the model often answers with prose
	// even when tools are available — runner should be more aggressive
	// with IntentParser for these.
	PrefersStructuredOutput bool `json:"prefers_structured_output,omitempty"`
}

// CapabilityAware is an optional interface a Provider can implement to
// expose its Capabilities to the runner. Providers that do NOT implement
// this interface are treated as DefaultCapabilities (conservative).
//
// We chose an optional interface instead of adding Capabilities() to
// Provider so older providers (incl. third-party ones built against
// the v1 contract) continue to compile and work.
type CapabilityAware interface {
	Capabilities() Capabilities
}

// DefaultCapabilities returns the conservative capability profile used
// when a Provider does not implement CapabilityAware. The defaults are
// intentionally pessimistic: NativeToolCall=true (every supported
// provider has it) but ToolChoiceSupport=None (don't risk an HTTP 400
// from a vendor that ignores the field).
func DefaultCapabilities() Capabilities {
	return Capabilities{
		Family:                  "",
		NativeToolCall:          true,
		ToolChoiceSupport:       ToolChoiceNone,
		Reasoner:                false,
		MaxParallelTools:        1,
		EmitsReasoningContent:   false,
		PrefersStructuredOutput: false,
	}
}

// CapabilitiesOf reads the Capabilities from a Provider, falling back to
// DefaultCapabilities() if the provider does not implement CapabilityAware.
// This is the only function callers should use to obtain capabilities —
// it abstracts the optional-interface pattern.
func CapabilitiesOf(p Provider) Capabilities {
	if ca, ok := p.(CapabilityAware); ok {
		return ca.Capabilities()
	}
	return DefaultCapabilities()
}

// InferCapabilities applies generic, vendor-agnostic heuristics to guess
// a Capabilities profile when the builtin table (sdk/llm/builtin_capabilities.go)
// has no entry for this (baseURL, model). It is the safety net BELOW the
// builtin table in the resolution chain — the builtin table holds the
// precise data for known model families, while this function only looks
// for generic signals that work across any vendor:
//
//   1. Reasoner keywords in the model name — "r1" / "reasoner" / "thinking"
//      / "qwq" / "o1" / "o3" / "o4" → Reasoner=true. Any reasoner is
//      worth the runner's grace turn.
//   2. Local deployment baseURL — "localhost" / "127.0.0.1" / known local
//      ports → ToolChoiceSupport=None. Local inference engines (ollama,
//      llama.cpp, vLLM, lmstudio) almost never honor tool_choice.
//
// Anything not matching the above stays at DefaultCapabilities — the
// safest profile (NativeToolCall=true, ToolChoiceNone, non-reasoner).
//
// IMPORTANT: This function MUST stay generic. Vendor-specific data lives
// in builtinTable. Adding a model-name switch case here would defeat the
// purpose of the table — the table is the authoritative, reviewable
// source of "what we know about model X".
func InferCapabilities(baseURL, model string) Capabilities {
	caps := DefaultCapabilities()
	caps.NativeToolCall = true

	bl := strings.ToLower(baseURL)
	ml := strings.ToLower(model)

	// Generic reasoner detection — any model whose name contains a
	// reasoner-class keyword is treated as one. Catches new reasoners
	// from any vendor before we have time to add them to builtinTable.
	reasonerKeywords := []string{
		"reasoner", "thinking",
		"-r1", "-r2", "-r3",
		"qwq",
		"o1-", "o3-", "o4-",
	}
	for _, kw := range reasonerKeywords {
		if strings.Contains(ml, kw) {
			caps.Reasoner = true
			caps.ToolChoiceSupport = ToolChoiceNone
			caps.MaxParallelTools = 1
			caps.Family = "unknown-reasoner"
			break
		}
	}

	// Generic local-deployment detection — any baseURL pointing at a
	// local inference server gets tool_choice=None regardless of model.
	// Local engines (ollama / llama.cpp server / lmstudio / vLLM) rarely
	// honor tool_choice and may even 400 on it.
	localKeywords := []string{
		"localhost", "127.0.0.1", "0.0.0.0",
		":11434", // ollama default
		":8080",  // common llama.cpp / vLLM default
		":1234",  // lmstudio default
		"ollama",
	}
	for _, kw := range localKeywords {
		if strings.Contains(bl, kw) {
			caps.ToolChoiceSupport = ToolChoiceNone
			if caps.Family == "" {
				caps.Family = "local-inference"
			}
			break
		}
	}

	return caps
}
