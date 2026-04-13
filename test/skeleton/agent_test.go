package skeleton

import (
	"testing"

	"github.com/leef-l/brain/agent"
)

// ---------------------------------------------------------------------------
// agent.Kind 常量完整性
// ---------------------------------------------------------------------------

func TestAgentKindConstants(t *testing.T) {
	kinds := map[agent.Kind]string{
		agent.KindCentral:  "central",
		agent.KindCode:     "code",
		agent.KindBrowser:  "browser",
		agent.KindVerifier: "verifier",
	}
	for k, want := range kinds {
		if string(k) != want {
			t.Errorf("Kind %q string = %q, want %q", k, string(k), want)
		}
	}
}

// ---------------------------------------------------------------------------
// agent.LLMAccessMode 常量完整性
// ---------------------------------------------------------------------------

func TestLLMAccessModeConstants(t *testing.T) {
	modes := map[agent.LLMAccessMode]string{
		agent.LLMAccessProxied: "proxied",
		agent.LLMAccessDirect:  "direct",
		agent.LLMAccessHybrid:  "hybrid",
	}
	for m, want := range modes {
		if string(m) != want {
			t.Errorf("LLMAccessMode %q string = %q, want %q", m, string(m), want)
		}
	}
}

// ---------------------------------------------------------------------------
// agent.Descriptor 字段填充
// ---------------------------------------------------------------------------

func TestDescriptorFields(t *testing.T) {
	d := agent.Descriptor{
		Kind:           agent.KindCode,
		Version:        "1.2.3",
		LLMAccess:      agent.LLMAccessDirect,
		SupportedTools: []string{"code.edit", "code.run"},
		Capabilities:   map[string]bool{"streaming": true, "multi-turn": false},
	}
	if d.Kind != agent.KindCode {
		t.Errorf("Kind = %q, want %q", d.Kind, agent.KindCode)
	}
	if d.Version != "1.2.3" {
		t.Errorf("Version = %q, want %q", d.Version, "1.2.3")
	}
	if d.LLMAccess != agent.LLMAccessDirect {
		t.Errorf("LLMAccess = %q, want %q", d.LLMAccess, agent.LLMAccessDirect)
	}
	if len(d.SupportedTools) != 2 {
		t.Fatalf("SupportedTools len = %d, want 2", len(d.SupportedTools))
	}
	if !d.Capabilities["streaming"] {
		t.Error("Capabilities[streaming] should be true")
	}
	if d.Capabilities["multi-turn"] {
		t.Error("Capabilities[multi-turn] should be false")
	}
}

// ---------------------------------------------------------------------------
// agent.Descriptor 零值安全
// ---------------------------------------------------------------------------

func TestDescriptorZeroValue(t *testing.T) {
	var d agent.Descriptor
	if d.Kind != "" {
		t.Errorf("zero Descriptor.Kind = %q, want empty", d.Kind)
	}
	if d.SupportedTools != nil {
		t.Error("zero Descriptor.SupportedTools should be nil")
	}
	if d.Capabilities != nil {
		t.Error("zero Descriptor.Capabilities should be nil")
	}
}
