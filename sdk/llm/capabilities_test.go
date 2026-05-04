package llm

import (
	"context"
	"testing"
)

// fakeProviderNoCaps 不实现 CapabilityAware,验证 fallback 到 DefaultCapabilities。
type fakeProviderNoCaps struct{}

func (fakeProviderNoCaps) Name() string { return "fake-no-caps" }
func (fakeProviderNoCaps) Complete(_ context.Context, _ *ChatRequest) (*ChatResponse, error) {
	return nil, nil
}
func (fakeProviderNoCaps) Stream(_ context.Context, _ *ChatRequest) (StreamReader, error) {
	return nil, nil
}

// fakeProviderWithCaps 实现 CapabilityAware,返回自定义 Capabilities。
type fakeProviderWithCaps struct {
	caps Capabilities
}

func (p fakeProviderWithCaps) Name() string { return "fake-with-caps" }
func (p fakeProviderWithCaps) Complete(_ context.Context, _ *ChatRequest) (*ChatResponse, error) {
	return nil, nil
}
func (p fakeProviderWithCaps) Stream(_ context.Context, _ *ChatRequest) (StreamReader, error) {
	return nil, nil
}
func (p fakeProviderWithCaps) Capabilities() Capabilities { return p.caps }

func TestCapabilitiesOf_FallbackToDefault(t *testing.T) {
	got := CapabilitiesOf(fakeProviderNoCaps{})
	want := DefaultCapabilities()
	if got != want {
		t.Errorf("CapabilitiesOf(no-caps) = %+v, want default %+v", got, want)
	}
}

func TestCapabilitiesOf_HonorsAware(t *testing.T) {
	want := Capabilities{
		Family:                  "test-family",
		NativeToolCall:          true,
		ToolChoiceSupport:       ToolChoiceRequired,
		Reasoner:                true,
		MaxParallelTools:        4,
		EmitsReasoningContent:   true,
		PrefersStructuredOutput: true,
	}
	got := CapabilitiesOf(fakeProviderWithCaps{caps: want})
	if got != want {
		t.Errorf("CapabilitiesOf(aware) = %+v, want %+v", got, want)
	}
}

func TestDefaultCapabilities(t *testing.T) {
	d := DefaultCapabilities()
	if !d.NativeToolCall {
		t.Error("DefaultCapabilities.NativeToolCall should be true")
	}
	if d.ToolChoiceSupport != ToolChoiceNone {
		t.Errorf("DefaultCapabilities.ToolChoiceSupport = %v, want None", d.ToolChoiceSupport)
	}
	if d.Reasoner {
		t.Error("DefaultCapabilities.Reasoner should be false")
	}
	if d.MaxParallelTools != 1 {
		t.Errorf("DefaultCapabilities.MaxParallelTools = %d, want 1", d.MaxParallelTools)
	}
}

func TestToolChoiceMode_String(t *testing.T) {
	cases := []struct {
		mode ToolChoiceMode
		want string
	}{
		{ToolChoiceNone, "none"},
		{ToolChoiceAuto, "auto"},
		{ToolChoiceRequired, "required"},
		{ToolChoiceSpecific, "specific"},
		{ToolChoiceMode(99), "unknown"},
	}
	for _, c := range cases {
		if got := c.mode.String(); got != c.want {
			t.Errorf("ToolChoiceMode(%d).String() = %q, want %q", c.mode, got, c.want)
		}
	}
}

// TestResolveCapabilities_VendorMatrix verifies the full resolver chain
// returns the right family/capability for representative models from each
// supported vendor. Originally lived in TestInferCapabilities — Phase 7
// moved vendor-specific data from InferCapabilities into the builtin
// table, so this test now exercises ResolveCapabilities (the composition
// entry point) which is what production code uses.
//
// To extend: add a case to builtin_capabilities_test.go's TestLookupBuiltin_Family
// for unit-level coverage; add a case here only if you want to verify the
// resolver chain itself behaves correctly for that vendor (e.g. when the
// vendor data comes from inference rather than the builtin table).
func TestResolveCapabilities_VendorMatrix(t *testing.T) {
	cases := []struct {
		name        string
		baseURL     string
		model       string
		wantFamily  string
		wantTC      ToolChoiceMode
		wantRsnr    bool
		wantEmitsRC bool
	}{
		// Anthropic family — builtin table claims Specific (more accurate
		// than the previous Required claim of the inference path).
		{"claude-opus", "https://api.anthropic.com", "claude-opus-4-7", "anthropic-claude", ToolChoiceSpecific, false, false},
		{"claude-sonnet", "", "claude-sonnet-4-20250514", "anthropic-claude", ToolChoiceSpecific, false, false},

		// OpenAI family
		{"gpt-4o", "https://api.openai.com", "gpt-4o", "openai-gpt", ToolChoiceSpecific, false, false},
		{"gpt-4-turbo", "", "gpt-4-turbo", "openai-gpt", ToolChoiceSpecific, false, false},

		// DeepSeek non-reasoner
		{"deepseek-v4-pro", "https://api.deepseek.com", "deepseek-v4-pro", "deepseek", ToolChoiceNone, false, false},
		{"deepseek-chat", "", "deepseek-chat", "deepseek", ToolChoiceNone, false, false},

		// DeepSeek reasoner
		{"deepseek-reasoner", "", "deepseek-reasoner", "deepseek-reasoner", ToolChoiceNone, true, true},
		{"deepseek-r1", "", "deepseek-r1", "deepseek-reasoner", ToolChoiceNone, true, true},

		// Mimo
		{"mimo-v25", "https://token-plan-cn.xiaomimimo.com", "mimo-v2.5-pro", "mimo", ToolChoiceNone, true, true},

		// Qwen — builtin table now claims Auto for the non-reasoner family
		{"qwen-plus", "", "qwen-plus", "qwen", ToolChoiceAuto, false, false},
		{"qwen-reasoner", "", "qwen3-reasoner-235b", "qwen-reasoner", ToolChoiceNone, true, true},
		{"qwq", "", "qwen-qwq-32b", "qwen-reasoner", ToolChoiceNone, true, true},

		// GLM
		{"glm-5", "", "glm-5", "glm", ToolChoiceAuto, false, false},

		// Doubao
		{"doubao-pro", "https://ark.volces.com", "doubao-pro-32k", "doubao", ToolChoiceNone, false, false},

		// Unknown — falls all the way through to default (heuristic gives
		// no opinion on a totally unknown model, default is None+non-reasoner).
		{"unknown", "", "weird-model-7b", "", ToolChoiceNone, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ResolveCapabilities(c.baseURL, c.model, nil)
			if got.Family != c.wantFamily {
				t.Errorf("Family = %q, want %q", got.Family, c.wantFamily)
			}
			if got.ToolChoiceSupport != c.wantTC {
				t.Errorf("ToolChoiceSupport = %v, want %v", got.ToolChoiceSupport, c.wantTC)
			}
			if got.Reasoner != c.wantRsnr {
				t.Errorf("Reasoner = %v, want %v", got.Reasoner, c.wantRsnr)
			}
			if got.EmitsReasoningContent != c.wantEmitsRC {
				t.Errorf("EmitsReasoningContent = %v, want %v", got.EmitsReasoningContent, c.wantEmitsRC)
			}
			if !got.NativeToolCall {
				t.Error("NativeToolCall should be true for all known and unknown providers")
			}
		})
	}
}
