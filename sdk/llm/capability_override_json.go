// Package llm — capability_override_json.go
//
// Custom JSON shape for CapabilitiesOverride so the user-facing
// config.json keeps a friendly string vocabulary for tool_choice
// ("none" / "auto" / "required" / "specific") instead of leaking the
// internal ToolChoiceMode int.
//
// Without this custom marshalling the user would have to write:
//
//   "capabilities": {"tool_choice_support": 2}     // ToolChoiceRequired
//
// — which is not discoverable. With it they write:
//
//   "capabilities": {"tool_choice": "required"}
//
// All other fields use vanilla pointer-to-T struct tag JSON.

package llm

import (
	"encoding/json"
	"fmt"
	"strings"
)

// MarshalJSON renders a CapabilitiesOverride into the user-facing JSON
// shape. tool_choice is rendered as the human-readable string;
// nil/empty fields are omitted entirely.
func (o CapabilitiesOverride) MarshalJSON() ([]byte, error) {
	type alias struct {
		Family                  *string `json:"family,omitempty"`
		NativeToolCall          *bool   `json:"native_tool_call,omitempty"`
		ToolChoice              *string `json:"tool_choice,omitempty"`
		Reasoner                *bool   `json:"reasoner,omitempty"`
		EmitsReasoningContent   *bool   `json:"emits_reasoning_content,omitempty"`
		PrefersStructuredOutput *bool   `json:"prefers_structured_output,omitempty"`
		MaxParallelTools        *int    `json:"max_parallel_tools,omitempty"`
	}
	a := alias{
		Family:                  o.Family,
		NativeToolCall:          o.NativeToolCall,
		Reasoner:                o.Reasoner,
		EmitsReasoningContent:   o.EmitsReasoningContent,
		PrefersStructuredOutput: o.PrefersStructuredOutput,
		MaxParallelTools:        o.MaxParallelTools,
	}
	if o.ToolChoiceSupport != nil {
		s := o.ToolChoiceSupport.String()
		a.ToolChoice = &s
	} else if o.ToolChoiceRaw != nil {
		a.ToolChoice = o.ToolChoiceRaw
	}
	return json.Marshal(a)
}

// UnmarshalJSON parses the user-facing JSON shape into the internal
// pointer-based struct. Accepts:
//
//   "tool_choice": "none" | "auto" | "required" | "specific"
//
// Unrecognized values produce a clear error rather than silently
// defaulting — typos in user config should fail loudly so the user can
// fix them, not silently degrade to ToolChoiceNone.
func (o *CapabilitiesOverride) UnmarshalJSON(data []byte) error {
	type alias struct {
		Family                  *string `json:"family,omitempty"`
		NativeToolCall          *bool   `json:"native_tool_call,omitempty"`
		ToolChoice              *string `json:"tool_choice,omitempty"`
		Reasoner                *bool   `json:"reasoner,omitempty"`
		EmitsReasoningContent   *bool   `json:"emits_reasoning_content,omitempty"`
		PrefersStructuredOutput *bool   `json:"prefers_structured_output,omitempty"`
		MaxParallelTools        *int    `json:"max_parallel_tools,omitempty"`
	}
	var a alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	o.Family = a.Family
	o.NativeToolCall = a.NativeToolCall
	o.Reasoner = a.Reasoner
	o.EmitsReasoningContent = a.EmitsReasoningContent
	o.PrefersStructuredOutput = a.PrefersStructuredOutput
	o.MaxParallelTools = a.MaxParallelTools
	o.ToolChoiceRaw = a.ToolChoice

	if a.ToolChoice != nil {
		mode, err := parseToolChoiceMode(*a.ToolChoice)
		if err != nil {
			return err
		}
		o.ToolChoiceSupport = &mode
	}
	return nil
}

// parseToolChoiceMode is the inverse of ToolChoiceMode.String(). Used
// only at the user-facing JSON edge.
func parseToolChoiceMode(s string) (ToolChoiceMode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "none":
		return ToolChoiceNone, nil
	case "auto":
		return ToolChoiceAuto, nil
	case "required":
		return ToolChoiceRequired, nil
	case "specific":
		return ToolChoiceSpecific, nil
	default:
		return ToolChoiceNone, fmt.Errorf(
			"capabilities.tool_choice: invalid value %q "+
				"(allowed: none, auto, required, specific)", s)
	}
}
