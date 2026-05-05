package llm

import (
	"encoding/json"
	"testing"
)

// TestLookupBuiltin_Family covers one representative model per family in
// the builtin table. New entries to builtinTable SHOULD add a case here
// — if a row exists for the family but no test verifies its match, the
// row can drift over time without anyone noticing.
func TestLookupBuiltin_Family(t *testing.T) {
	cases := []struct {
		name        string
		baseURL     string
		model       string
		wantFamily  string
		wantTC      ToolChoiceMode
		wantReason  bool
		wantEmitsRC bool
	}{
		// reasoners
		{"openai o1", "https://api.openai.com/v1", "o1-preview", "openai-reasoner", ToolChoiceRequired, true, false},
		{"openai o3", "https://api.openai.com/v1", "o3-mini", "openai-reasoner", ToolChoiceRequired, true, false},
		{"deepseek-r1", "https://api.deepseek.com", "deepseek-reasoner", "deepseek-reasoner", ToolChoiceNone, true, true},
		{"qwq", "https://dashscope.aliyuncs.com", "qwq-plus", "qwen-reasoner", ToolChoiceNone, true, true},
		{"mimo", "https://api.xiaomi.com", "mimo-v2.5-pro", "mimo", ToolChoiceNone, true, true},

		// 国外
		{"claude opus", "https://api.anthropic.com", "claude-opus-4-7", "anthropic-claude", ToolChoiceSpecific, false, false},
		{"gpt-4o", "https://api.openai.com/v1", "gpt-4o", "openai-gpt", ToolChoiceSpecific, false, false},
		{"gemini", "https://generativelanguage.googleapis.com", "gemini-2.0-pro", "google-gemini", ToolChoiceRequired, false, false},
		{"mistral", "https://api.mistral.ai", "mistral-large", "mistral", ToolChoiceAuto, false, false},
		{"cohere", "https://api.cohere.com", "command-r-plus", "cohere", ToolChoiceRequired, false, false},
		{"llama via together", "https://api.together.ai", "meta-llama/Llama-3.1-70B", "meta-llama", ToolChoiceAuto, false, false},

		// 国内非 reasoner
		{"deepseek chat", "https://api.deepseek.com", "deepseek-chat", "deepseek", ToolChoiceNone, false, false},
		{"deepseek v4", "https://api.deepseek.com", "deepseek-v4-pro", "deepseek", ToolChoiceNone, false, false},
		{"qwen base", "https://dashscope.aliyuncs.com", "qwen3-coder-plus", "qwen", ToolChoiceAuto, false, false},
		{"glm", "https://open.bigmodel.cn", "glm-4.6", "glm", ToolChoiceAuto, false, false},
		{"doubao", "https://ark.cn-beijing.volces.com", "doubao-pro-32k", "doubao", ToolChoiceNone, false, false},
		{"kimi", "https://api.moonshot.cn", "kimi-k2", "moonshot", ToolChoiceRequired, false, false},
		{"yi", "https://api.lingyiwanwu.com", "yi-large", "yi", ToolChoiceAuto, false, false},
		{"step", "https://api.stepfun.com", "step-2-16k", "step", ToolChoiceAuto, false, false},
		{"hunyuan", "https://hunyuan.tencentcloudapi.com", "hunyuan-pro", "hunyuan", ToolChoiceAuto, false, false},
		{"ernie", "https://aip.baidubce.com", "ernie-4.0-turbo", "ernie", ToolChoiceNone, false, false},
		{"spark", "https://spark-api.xf-yun.com", "spark-max", "spark", ToolChoiceNone, false, false},

		// 平台兜底
		{"ollama localhost", "http://localhost:11434", "some-unknown-model", "local-deploy", ToolChoiceNone, false, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			caps, ok := LookupBuiltin(tc.baseURL, tc.model)
			if !ok {
				t.Fatalf("expected hit for %q/%q", tc.baseURL, tc.model)
			}
			if caps.Family != tc.wantFamily {
				t.Errorf("Family: got %q want %q", caps.Family, tc.wantFamily)
			}
			if caps.ToolChoiceSupport != tc.wantTC {
				t.Errorf("ToolChoiceSupport: got %v want %v", caps.ToolChoiceSupport, tc.wantTC)
			}
			if caps.Reasoner != tc.wantReason {
				t.Errorf("Reasoner: got %v want %v", caps.Reasoner, tc.wantReason)
			}
			if caps.EmitsReasoningContent != tc.wantEmitsRC {
				t.Errorf("EmitsReasoningContent: got %v want %v", caps.EmitsReasoningContent, tc.wantEmitsRC)
			}
		})
	}
}

// TestLookupBuiltin_ExclusionGuard confirms that generic entries don't
// steal traffic from their reasoner siblings — this is the most error-
// prone aspect of the table.
func TestLookupBuiltin_ExclusionGuard(t *testing.T) {
	// "deepseek-reasoner" must hit the reasoner row, NOT the generic
	// "deepseek" row that comes later.
	caps, ok := LookupBuiltin("", "deepseek-reasoner")
	if !ok || !caps.Reasoner {
		t.Fatalf("deepseek-reasoner must match reasoner row, got reasoner=%v family=%q", caps.Reasoner, caps.Family)
	}

	// "qwen-r1" → reasoner row
	caps, ok = LookupBuiltin("", "qwen-r1-7b")
	if !ok || !caps.Reasoner {
		t.Fatalf("qwen-r1-* must match qwen-reasoner row, got reasoner=%v family=%q", caps.Reasoner, caps.Family)
	}

	// "qwen-coder" → generic qwen row, NOT reasoner
	caps, ok = LookupBuiltin("", "qwen3-coder")
	if !ok {
		t.Fatalf("qwen3-coder must hit a row")
	}
	if caps.Reasoner {
		t.Errorf("qwen3-coder must NOT be reasoner")
	}
	if caps.Family != "qwen" {
		t.Errorf("qwen3-coder family: got %q want qwen", caps.Family)
	}
}

// TestLookupBuiltin_Miss confirms truly unknown models return ok=false
// so the resolver chain can fall through to InferCapabilities.
func TestLookupBuiltin_Miss(t *testing.T) {
	if _, ok := LookupBuiltin("https://random-vendor.example.com", "totally-new-model-2030"); ok {
		t.Fatalf("expected miss for unknown vendor+model")
	}
}

// TestMergeCapabilities_FieldLevel exercises the field-by-field merge
// semantics — e.g. a positive Reasoner from src must NOT be demoted by
// a base that has Reasoner=false.
func TestMergeCapabilities_FieldLevel(t *testing.T) {
	base := DefaultCapabilities()
	base.Family = "base-family"

	src := Capabilities{
		Family:           "src-family",
		Reasoner:         true,
		MaxParallelTools: 4,
	}
	merged := MergeCapabilities(base, src)

	if merged.Family != "src-family" {
		t.Errorf("Family: got %q want src-family", merged.Family)
	}
	if !merged.Reasoner {
		t.Errorf("Reasoner: got false, src had true")
	}
	if merged.MaxParallelTools != 4 {
		t.Errorf("MaxParallelTools: got %d want 4", merged.MaxParallelTools)
	}
	// Base preserved for fields src didn't set
	if !merged.NativeToolCall {
		t.Errorf("NativeToolCall: got false, base had true")
	}
}

// TestCapabilitiesOverride_Apply exercises the user-override layer —
// pointer semantics must let "field=false" override "base=true".
func TestCapabilitiesOverride_Apply(t *testing.T) {
	baseline := Capabilities{
		Family:                "deepseek",
		NativeToolCall:        true,
		ToolChoiceSupport:     ToolChoiceNone,
		Reasoner:              false,
		MaxParallelTools:      4,
		EmitsReasoningContent: false,
	}

	// User claims this deepseek model is actually a reasoner that
	// supports tool_choice (hypothetical custom finetune).
	yes := true
	required := ToolChoiceRequired
	one := 1
	override := &CapabilitiesOverride{
		Reasoner:          &yes,
		ToolChoiceSupport: &required,
		MaxParallelTools:  &one,
	}
	out := override.Apply(baseline)

	if !out.Reasoner {
		t.Errorf("Reasoner override failed")
	}
	if out.ToolChoiceSupport != ToolChoiceRequired {
		t.Errorf("ToolChoiceSupport override failed: got %v", out.ToolChoiceSupport)
	}
	if out.MaxParallelTools != 1 {
		t.Errorf("MaxParallelTools override failed: got %d", out.MaxParallelTools)
	}
	// Untouched
	if out.Family != "deepseek" {
		t.Errorf("Family must inherit baseline: got %q", out.Family)
	}
}

// TestCapabilitiesOverride_NilSafe and IsEmpty
func TestCapabilitiesOverride_NilAndEmpty(t *testing.T) {
	var nilOv *CapabilitiesOverride
	base := DefaultCapabilities()
	if got := nilOv.Apply(base); got != base {
		t.Errorf("nil Apply must be no-op")
	}
	if !nilOv.IsEmpty() {
		t.Errorf("nil IsEmpty must be true")
	}

	empty := &CapabilitiesOverride{}
	if !empty.IsEmpty() {
		t.Errorf("zero-value IsEmpty must be true")
	}
}

// TestCapabilitiesOverride_JSON exercises the user-facing JSON wire
// shape — string tool_choice, partial fields, error on typo.
func TestCapabilitiesOverride_JSON(t *testing.T) {
	t.Run("partial parse", func(t *testing.T) {
		raw := `{"tool_choice":"required","reasoner":true}`
		var ov CapabilitiesOverride
		if err := json.Unmarshal([]byte(raw), &ov); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if ov.ToolChoiceSupport == nil || *ov.ToolChoiceSupport != ToolChoiceRequired {
			t.Errorf("tool_choice not parsed")
		}
		if ov.Reasoner == nil || !*ov.Reasoner {
			t.Errorf("reasoner not parsed")
		}
		if ov.MaxParallelTools != nil {
			t.Errorf("absent field must be nil")
		}
	})

	t.Run("invalid tool_choice surfaces error", func(t *testing.T) {
		raw := `{"tool_choice":"REQUIRED!"}`
		var ov CapabilitiesOverride
		err := json.Unmarshal([]byte(raw), &ov)
		if err == nil {
			t.Fatalf("expected error on typo")
		}
	})

	t.Run("roundtrip", func(t *testing.T) {
		req := ToolChoiceRequired
		yes := true
		ov := CapabilitiesOverride{
			ToolChoiceSupport: &req,
			Reasoner:          &yes,
		}
		bs, err := json.Marshal(ov)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		s := string(bs)
		if !contains(s, `"tool_choice":"required"`) {
			t.Errorf("marshal must use string form: %s", s)
		}
		if !contains(s, `"reasoner":true`) {
			t.Errorf("marshal must include reasoner: %s", s)
		}
	})
}

// TestResolveCapabilities_PriorityChain proves the full chain order:
// default → infer → builtin → user override.
func TestResolveCapabilities_PriorityChain(t *testing.T) {
	t.Run("known model uses builtin", func(t *testing.T) {
		caps := ResolveCapabilities("https://api.deepseek.com", "deepseek-reasoner", nil)
		if caps.Family != "deepseek-reasoner" {
			t.Errorf("Family: got %q want deepseek-reasoner", caps.Family)
		}
		if !caps.Reasoner {
			t.Errorf("Reasoner: should be true from builtin")
		}
	})

	t.Run("unknown model falls back to inference", func(t *testing.T) {
		// "novel-thinking-2030" should hit the reasoner-keyword heuristic
		caps := ResolveCapabilities("https://novel-vendor.example", "novel-thinking-2030", nil)
		if !caps.Reasoner {
			t.Errorf("Reasoner: heuristic should detect 'thinking' keyword")
		}
		if caps.Family != "unknown-reasoner" {
			t.Errorf("Family: got %q want unknown-reasoner", caps.Family)
		}
	})

	t.Run("user override wins", func(t *testing.T) {
		// Builtin says deepseek = ToolChoiceNone; user overrides to Required.
		req := ToolChoiceRequired
		caps := ResolveCapabilities("", "deepseek-chat", &CapabilitiesOverride{
			ToolChoiceSupport: &req,
		})
		if caps.ToolChoiceSupport != ToolChoiceRequired {
			t.Errorf("override must win, got %v", caps.ToolChoiceSupport)
		}
		// But user didn't touch Family — must inherit from builtin
		if caps.Family != "deepseek" {
			t.Errorf("Family must inherit from builtin, got %q", caps.Family)
		}
	})

	t.Run("totally unknown returns safe defaults", func(t *testing.T) {
		caps := ResolveCapabilities("https://nothing.example", "x123", nil)
		if caps.Reasoner {
			t.Errorf("Reasoner: must be false for unknown")
		}
		if caps.ToolChoiceSupport != ToolChoiceNone {
			t.Errorf("ToolChoice: must be None for unknown, got %v", caps.ToolChoiceSupport)
		}
		if !caps.NativeToolCall {
			t.Errorf("NativeToolCall: must be true (default)")
		}
	})
}

// TestInferCapabilities_LocalDeploy verifies the heuristic catches local
// inference URLs even when the model name is unknown.
func TestInferCapabilities_LocalDeploy(t *testing.T) {
	urls := []string{
		"http://localhost:11434/v1",
		"http://127.0.0.1:8080",
		"http://0.0.0.0:1234/v1",
		"http://my-server:11434",
	}
	for _, u := range urls {
		caps := InferCapabilities(u, "some-model")
		if caps.ToolChoiceSupport != ToolChoiceNone {
			t.Errorf("%s: ToolChoice should be None for local deploy", u)
		}
	}
}

// TestBuiltinMatch_RejectsExcludesOnly is the regression test for the P1
// bug fix where `BuiltinMatch{ModelExcludes: ["foo"]}` (no positive
// matchers) accidentally matched any input. After the fix, such an entry
// must reject everything to surface the misconfiguration loudly.
func TestBuiltinMatch_RejectsExcludesOnly(t *testing.T) {
	m := BuiltinMatch{ModelExcludes: []string{"reasoner"}}
	cases := []struct {
		baseURL, model string
	}{
		{"", ""},
		{"https://example.com/v1", "some-model"},
		{"https://api.deepseek.com", "deepseek-chat"},
		{"http://localhost:11434", "llama-3.1"},
	}
	for _, tc := range cases {
		if m.matches(tc.baseURL, tc.model) {
			t.Errorf("excludes-only entry must reject (%q, %q) but matched",
				tc.baseURL, tc.model)
		}
	}

	// Also confirm: model containing the excluded keyword is still rejected.
	if m.matches("", "deepseek-reasoner") {
		t.Errorf("excludes-only entry matched 'deepseek-reasoner' — should always reject")
	}
}

// TestBuiltinTable_NoPanicOnMatch_All ensures every entry's match
// function works without panicking on representative input. Cheap
// fuzz-style guard.
func TestBuiltinTable_NoPanicOnMatch_All(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panic during match: %v", r)
		}
	}()
	for _, e := range BuiltinEntries() {
		_ = e.Match.matches("", "")
		_ = e.Match.matches("https://example.com/v1", "some-model")
	}
}

// helper
func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
