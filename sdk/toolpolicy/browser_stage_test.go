package toolpolicy

import (
	"testing"

	"github.com/leef-l/brain/sdk/tool"
)

func makeBrowserRegistry() tool.Registry {
	reg := tool.NewMemRegistry()
	for _, name := range []string{
		"browser.open", "browser.navigate", "browser.click", "browser.type",
		"browser.drag",
		"browser.snapshot", "browser.understand", "browser.sitemap",
		"browser.check_anomaly", "browser.wait", "browser.wait_network_idle",
		"browser.network", "browser.pattern_match", "browser.pattern_exec",
		"browser.pattern_list", "browser.visual_inspect", "browser.screenshot",
		"browser.eval", "browser.press_key", "browser.scroll", "browser.hover",
		"browser.fill_form", "browser.storage", "human.request_takeover",
	} {
		reg.Register(&stubTool{name: name})
	}
	return reg
}

func TestBrowserStageNewPageExcludesVisualInspect(t *testing.T) {
	cfg := &Config{}
	MergeBrowserStageProfiles(cfg)
	if err := ValidateConfig(cfg); err != nil {
		t.Fatalf("validate: %v", err)
	}
	p := NewAdaptivePolicy(cfg)

	result := p.Evaluate(EvalRequest{
		Mode:         "run",
		BrainKind:    "browser",
		BrowserStage: BrowserStageNewPage,
	}, makeBrowserRegistry())

	names := toolNameSet(result)
	if !names["browser.snapshot"] || !names["browser.understand"] {
		t.Errorf("new_page must include snapshot/understand, got %v", names)
	}
	if !names["browser.drag"] {
		t.Errorf("new_page must include browser.drag for slider CAPTCHA, got %v", names)
	}
	if !names["human.request_takeover"] {
		t.Errorf("new_page must include human.request_takeover, got %v", names)
	}
	if names["browser.visual_inspect"] {
		t.Errorf("new_page must NOT include visual_inspect (fallback-only)")
	}
	if names["browser.screenshot"] {
		t.Errorf("new_page must NOT include screenshot (fallback-only)")
	}
	if names["browser.eval"] {
		t.Errorf("new_page must NOT include eval")
	}
}

func TestBrowserStageKnownFlowPreferPattern(t *testing.T) {
	cfg := &Config{}
	MergeBrowserStageProfiles(cfg)
	p := NewAdaptivePolicy(cfg)

	result := p.Evaluate(EvalRequest{
		Mode:         "run",
		BrainKind:    "browser",
		BrowserStage: BrowserStageKnownFlow,
	}, makeBrowserRegistry())

	names := toolNameSet(result)
	for _, must := range []string{"browser.pattern_match", "browser.pattern_exec", "browser.pattern_list"} {
		if !names[must] {
			t.Errorf("known_flow must include %s", must)
		}
	}
	if !names["browser.drag"] {
		t.Errorf("known_flow must include browser.drag, got %v", names)
	}
	if !names["human.request_takeover"] {
		t.Errorf("known_flow must include human.request_takeover, got %v", names)
	}
	if names["browser.understand"] {
		t.Errorf("known_flow should not pay for understand — pattern matches are enough")
	}
}

func TestBrowserStageFallbackAllowsAll(t *testing.T) {
	cfg := &Config{}
	MergeBrowserStageProfiles(cfg)
	p := NewAdaptivePolicy(cfg)

	result := p.Evaluate(EvalRequest{
		Mode:         "run",
		BrainKind:    "browser",
		BrowserStage: BrowserStageFallback,
	}, makeBrowserRegistry())

	names := toolNameSet(result)
	for _, must := range []string{"browser.visual_inspect", "browser.eval", "browser.screenshot", "browser.snapshot", "browser.drag", "human.request_takeover"} {
		if !names[must] {
			t.Errorf("fallback must include %s", must)
		}
	}
}

func TestBrowserStageUnknownIsNoop(t *testing.T) {
	cfg := &Config{}
	MergeBrowserStageProfiles(cfg)
	p := NewAdaptivePolicy(cfg)

	result := p.Evaluate(EvalRequest{
		Mode:         "run",
		BrainKind:    "browser",
		BrowserStage: "banana",
	}, makeBrowserRegistry())

	// 没 profile 绑定 run.browser 这个 scope — 所有工具通过
	if len(result.List()) != 24 {
		t.Errorf("unknown stage should keep all 24 tools, got %d", len(result.List()))
	}
}

func TestMergeBrowserStageProfilesIdempotent(t *testing.T) {
	cfg := &Config{
		ToolProfiles: map[string]*Profile{
			"browser_new_page": {Include: []string{"custom.*"}}, // 用户自定义
		},
	}
	MergeBrowserStageProfiles(cfg)
	MergeBrowserStageProfiles(cfg) // 第二次应该完全幂等

	if got := cfg.ToolProfiles["browser_new_page"].Include[0]; got != "custom.*" {
		t.Errorf("merge must not overwrite user profile, got %q", got)
	}
	if cfg.ToolProfiles["browser_known_flow"] == nil {
		t.Errorf("other default profiles must still be merged")
	}
}

func toolNameSet(r tool.Registry) map[string]bool {
	out := map[string]bool{}
	for _, t := range r.List() {
		out[t.Name()] = true
	}
	return out
}
