// Package llm — capability_resolver.go
//
// # Why this file exists
//
// Capability data has four sources in brain v3:
//
//   1. User config (`~/.brain/config.json` active_provider.capabilities)
//   2. Builtin table (sdk/llm/builtin_capabilities.go)
//   3. Heuristic inference (InferCapabilities — generic fallback for
//      unknown models / proxies)
//   4. DefaultCapabilities (zero-knowledge safe profile)
//
// Without a single composition entry point each call site would need to
// reimplement "config wins over builtin wins over inference wins over
// default" plus the field-level merge logic (config might only set one
// field; the rest must be inherited from lower layers). Phase 7 collapses
// that into ResolveCapabilities — every caller (provider装配 / future
// `brain capability show` command / tests) goes through this single
// function.
//
// # Field-level merge semantics
//
// User config typically declares 1-2 fields, not the entire Capabilities
// struct. We MUST NOT zero out every field they didn't mention. The
// MergeCapabilities helper treats a "zero value" as "no opinion" and
// keeps the lower layer's value. Concrete rules:
//
//   - Family:                  override iff non-empty
//   - NativeToolCall:          always inherit from lower layer (always true today)
//   - ToolChoiceSupport:       override iff value != ToolChoiceNone OR
//                              an explicit "*set*" sentinel is used
//                              (we use a *ToolChoiceMode pointer in the
//                              user-facing override struct to disambiguate
//                              "not set" from "explicitly None")
//   - Reasoner:                pointer-only override (see CapabilitiesOverride)
//   - EmitsReasoningContent:   pointer-only override
//   - PrefersStructuredOutput: pointer-only override
//   - MaxParallelTools:        override iff > 0
//
// The pointer-based override pattern lives in CapabilitiesOverride below
// so JSON unmarshalling distinguishes "field absent" from "field=false".

package llm

// CapabilitiesOverride is the wire shape for user-supplied capability
// overrides (config.json, CLI flags, future probe results). Every field
// is a pointer so JSON unmarshalling knows the difference between
// "user didn't mention this" (nil) and "user explicitly set this to
// the zero value" (non-nil pointer to zero).
//
// Use Apply() to merge an override on top of a baseline Capabilities.
type CapabilitiesOverride struct {
	Family                  *string         `json:"family,omitempty"`
	NativeToolCall          *bool           `json:"native_tool_call,omitempty"`
	ToolChoiceSupport       *ToolChoiceMode `json:"-"` // see UnmarshalJSON below — accepts string in JSON
	Reasoner                *bool           `json:"reasoner,omitempty"`
	EmitsReasoningContent   *bool           `json:"emits_reasoning_content,omitempty"`
	PrefersStructuredOutput *bool           `json:"prefers_structured_output,omitempty"`
	MaxParallelTools        *int            `json:"max_parallel_tools,omitempty"`

	// raw "tool_choice" JSON value, used by UnmarshalJSON to drive
	// ToolChoiceSupport. Lives here so Marshal also round-trips it.
	ToolChoiceRaw *string `json:"tool_choice,omitempty"`
}

// Apply merges this override on top of a baseline. Fields where the
// override is nil keep the baseline's value; non-nil pointer fields
// replace the baseline's value verbatim.
//
// Returns a new Capabilities; baseline is not mutated.
func (o *CapabilitiesOverride) Apply(baseline Capabilities) Capabilities {
	if o == nil {
		return baseline
	}
	out := baseline
	if o.Family != nil {
		out.Family = *o.Family
	}
	if o.NativeToolCall != nil {
		out.NativeToolCall = *o.NativeToolCall
	}
	if o.ToolChoiceSupport != nil {
		out.ToolChoiceSupport = *o.ToolChoiceSupport
	}
	if o.Reasoner != nil {
		out.Reasoner = *o.Reasoner
	}
	if o.EmitsReasoningContent != nil {
		out.EmitsReasoningContent = *o.EmitsReasoningContent
	}
	if o.PrefersStructuredOutput != nil {
		out.PrefersStructuredOutput = *o.PrefersStructuredOutput
	}
	if o.MaxParallelTools != nil {
		out.MaxParallelTools = *o.MaxParallelTools
	}
	return out
}

// IsEmpty returns true when no override field is set — used by callers
// that want to skip Apply() entirely for the common "user didn't write
// any capabilities block" path.
func (o *CapabilitiesOverride) IsEmpty() bool {
	if o == nil {
		return true
	}
	return o.Family == nil &&
		o.NativeToolCall == nil &&
		o.ToolChoiceSupport == nil &&
		o.Reasoner == nil &&
		o.EmitsReasoningContent == nil &&
		o.PrefersStructuredOutput == nil &&
		o.MaxParallelTools == nil
}

// ResolveCapabilities composes all four capability sources into a single
// Capabilities value, in priority order (later sources take precedence
// at field level via MergeCapabilities semantics, then user override is
// applied last).
//
//   default  →  inferred  →  builtin  →  user override
//
// Layers higher in the chain only set fields they have an opinion on;
// the lower layer's values survive unless overwritten. See file doc for
// the full merge rules.
//
// `userOverride` MAY be nil (no user-supplied capabilities block).
func ResolveCapabilities(baseURL, model string, userOverride *CapabilitiesOverride) Capabilities {
	caps := DefaultCapabilities()

	// Layer 3 — heuristic inference (generic keyword fallback for
	// unknown models / proxies). Always overlays default.
	caps = MergeCapabilities(caps, InferCapabilities(baseURL, model))

	// Layer 2 — builtin table (主流 model 精确数据). Only applied when
	// the table actually has a row for this (baseURL, model); otherwise
	// caps stays at the inferred values.
	if b, ok := LookupBuiltin(baseURL, model); ok {
		caps = MergeCapabilities(caps, b)
	}

	// Layer 1 — user override (config.json / CLI / probe). Always last
	// so user主权 wins, but field-level so a single-field override
	// doesn't wipe the rest.
	caps = userOverride.Apply(caps)
	return caps
}

// MergeCapabilities overlays src onto base with field-level semantics.
// Only fields where src has an "opinion" are copied; the rest of base
// survives. Used internally by ResolveCapabilities to compose the
// builtin / inferred / default layers.
//
// "Opinion" is defined as:
//
//   - Non-empty string for Family
//   - True for any bool (we never want to demote a true to false via a
//     lower-priority layer; only the explicit user override can do that
//     via CapabilitiesOverride pointer semantics)
//   - Non-zero ToolChoiceSupport (ToolChoiceNone is the zero value, so
//     a layer claiming None means "default safe" not "actively None" —
//     a layer that knows "None" should set it via builtin table where
//     the row exists explicitly, and the merge keeps it via the
//     non-zero shortcut. The combination of ordering (layer order) +
//     non-zero check works because we only call MergeCapabilities top-
//     down: builtin (which knows None means None) overlays inferred
//     (which may or may not have a value); the True precedence rule
//     here protects against a less-specific layer demoting a more-
//     specific layer's positive claim.
//   - Positive MaxParallelTools
func MergeCapabilities(base, src Capabilities) Capabilities {
	out := base
	if src.Family != "" {
		out.Family = src.Family
	}
	if src.NativeToolCall {
		out.NativeToolCall = true
	}
	if src.ToolChoiceSupport != ToolChoiceNone {
		out.ToolChoiceSupport = src.ToolChoiceSupport
	}
	if src.Reasoner {
		out.Reasoner = true
	}
	if src.EmitsReasoningContent {
		out.EmitsReasoningContent = true
	}
	if src.PrefersStructuredOutput {
		out.PrefersStructuredOutput = true
	}
	if src.MaxParallelTools > 0 {
		out.MaxParallelTools = src.MaxParallelTools
	}
	return out
}
