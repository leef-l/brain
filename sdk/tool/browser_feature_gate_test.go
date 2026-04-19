package tool

import (
	"context"
	"encoding/json"
	"testing"

	brainerrors "github.com/leef-l/brain/sdk/errors"
	brainlicense "github.com/leef-l/brain/sdk/license"
)

type stubFeatureTool struct {
	name   string
	called bool
}

func (t *stubFeatureTool) Name() string { return t.name }
func (t *stubFeatureTool) Risk() Risk   { return RiskSafe }
func (t *stubFeatureTool) Schema() Schema {
	return Schema{
		Name:        t.name,
		Description: "stub feature gate tool",
		InputSchema: json.RawMessage(`{"type":"object"}`),
		OutputSchema: json.RawMessage(`{
  "type":"object",
  "properties":{"ok":{"type":"boolean"}}
}`),
		Brain: "browser",
	}
}

func (t *stubFeatureTool) Execute(_ context.Context, _ json.RawMessage) (*Result, error) {
	t.called = true
	return &Result{Output: json.RawMessage(`{"ok":true}`)}, nil
}

func TestBrowserFeatureGateDisabledByDefault(t *testing.T) {
	prev := currentBrowserFeatureGate()
	t.Cleanup(func() { SetBrowserFeatureGate(&prev) })
	SetBrowserFeatureGate(nil)

	inner := &stubFeatureTool{name: "browser.understand"}
	gated := &browserFeatureGatedTool{inner: inner, feature: BrowserFeatureIntelligence}

	res, err := gated.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute() err = %v, want nil", err)
	}
	if !inner.called {
		t.Fatal("inner tool was not called when gate is disabled")
	}
	if res == nil || res.IsError {
		t.Fatalf("Execute() = %+v, want success result", res)
	}
}

func TestBrowserFeatureGateBlocksMissingFeature(t *testing.T) {
	prev := currentBrowserFeatureGate()
	t.Cleanup(func() { SetBrowserFeatureGate(&prev) })
	SetBrowserFeatureGate(&BrowserFeatureGateConfig{
		Enabled:  true,
		Features: map[string]bool{},
	})

	inner := &stubFeatureTool{name: "browser.understand"}
	gated := &browserFeatureGatedTool{inner: inner, feature: BrowserFeatureIntelligence}

	res, err := gated.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute() err = %v, want nil", err)
	}
	if inner.called {
		t.Fatal("inner tool should not be called when feature is missing")
	}
	if res == nil || !res.IsError {
		t.Fatalf("Execute() = %+v, want license error result", res)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(res.Output, &payload); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if got := payload["error_code"]; got != brainerrors.CodeLicenseFeatureNotAllowed {
		t.Fatalf("error_code = %v, want %q", got, brainerrors.CodeLicenseFeatureNotAllowed)
	}
	if got := payload["feature"]; got != BrowserFeatureIntelligence {
		t.Fatalf("feature = %v, want %q", got, BrowserFeatureIntelligence)
	}
}

func TestNewBrowserToolsAppliesFeatureGateToHeavyTools(t *testing.T) {
	prev := currentBrowserFeatureGate()
	t.Cleanup(func() { SetBrowserFeatureGate(&prev) })
	SetBrowserFeatureGate(&BrowserFeatureGateConfig{
		Enabled:  true,
		Features: map[string]bool{},
	})

	var understand Tool
	var patternMatch Tool
	var visualInspect Tool
	for _, candidate := range NewBrowserTools() {
		switch candidate.Name() {
		case "browser.understand":
			understand = candidate
		case "browser.pattern_match":
			patternMatch = candidate
		case "browser.visual_inspect":
			visualInspect = candidate
		}
	}

	for _, tc := range []struct {
		name string
		tool Tool
	}{
		{name: "browser.understand", tool: understand},
		{name: "browser.pattern_match", tool: patternMatch},
		{name: "browser.visual_inspect", tool: visualInspect},
	} {
		if tc.tool == nil {
			t.Fatalf("%s missing from NewBrowserTools()", tc.name)
		}
		res, err := tc.tool.Execute(context.Background(), json.RawMessage(`{}`))
		if err != nil {
			t.Fatalf("%s Execute() err = %v, want nil", tc.name, err)
		}
		if res == nil || !res.IsError {
			t.Fatalf("%s Execute() = %+v, want gated error result", tc.name, res)
		}
		var payload map[string]interface{}
		if err := json.Unmarshal(res.Output, &payload); err != nil {
			t.Fatalf("%s result unmarshal: %v", tc.name, err)
		}
		if got := payload["error_code"]; got != brainerrors.CodeLicenseFeatureNotAllowed {
			t.Fatalf("%s error_code = %v, want %q", tc.name, got, brainerrors.CodeLicenseFeatureNotAllowed)
		}
	}
}

func TestNewBrowserToolsLoadsFeatureGateFromEnv(t *testing.T) {
	prev := currentBrowserFeatureGate()
	t.Cleanup(func() { SetBrowserFeatureGate(&prev) })
	SetBrowserFeatureGate(nil)

	t.Setenv(envBrowserFeatureGateEnabled, "1")
	t.Setenv(envBrowserFeatureGateFeatures, BrowserFeatureIntelligence)

	_ = NewBrowserTools()

	cfg := currentBrowserFeatureGate()
	if !cfg.Enabled {
		t.Fatal("feature gate should be enabled from env")
	}
	if !cfg.Features[BrowserFeatureIntelligence] {
		t.Fatalf("feature %q missing after env load: %+v", BrowserFeatureIntelligence, cfg.Features)
	}
}

func TestNewBrowserToolsBlocksHeavyToolsFromEnvWhenFeatureMissing(t *testing.T) {
	prev := currentBrowserFeatureGate()
	t.Cleanup(func() { SetBrowserFeatureGate(&prev) })
	SetBrowserFeatureGate(nil)

	t.Setenv(envBrowserFeatureGateEnabled, "1")
	t.Setenv(envBrowserFeatureGateFeatures, "")

	var patternMatch Tool
	for _, candidate := range NewBrowserTools() {
		if candidate.Name() == "browser.pattern_match" {
			patternMatch = candidate
			break
		}
	}
	if patternMatch == nil {
		t.Fatal("browser.pattern_match missing from NewBrowserTools()")
	}

	res, err := patternMatch.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute() err = %v, want nil", err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("Execute() = %+v, want gated error result", res)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(res.Output, &payload); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if got := payload["error_code"]; got != brainerrors.CodeLicenseFeatureNotAllowed {
		t.Fatalf("error_code = %v, want %q", got, brainerrors.CodeLicenseFeatureNotAllowed)
	}
}

func TestConfigureBrowserFeatureGateFromLicense_UsesBrowserProFeatures(t *testing.T) {
	prev := currentBrowserFeatureGate()
	t.Cleanup(func() { SetBrowserFeatureGate(&prev) })

	ConfigureBrowserFeatureGateFromLicense(&brainlicense.Result{
		Features: map[string]bool{
			"browser-pro.intelligence": true,
			"browser-pro.evidence":     true,
			"other.feature":            true,
		},
	})

	cfg := CurrentBrowserFeatureGateConfig()
	if !cfg.Enabled {
		t.Fatal("feature gate should be enabled from license")
	}
	if !cfg.Features["browser-pro.intelligence"] {
		t.Fatalf("missing browser-pro.intelligence in %+v", cfg.Features)
	}
	if !cfg.Features["browser-pro.evidence"] {
		t.Fatalf("missing browser-pro.evidence in %+v", cfg.Features)
	}
	if cfg.Features["other.feature"] {
		t.Fatalf("unexpected non-browser feature leaked into gate: %+v", cfg.Features)
	}
}

func TestConfigureBrowserFeatureGate_FallsBackToEnvWithoutLicense(t *testing.T) {
	prev := currentBrowserFeatureGate()
	t.Cleanup(func() { SetBrowserFeatureGate(&prev) })
	SetBrowserFeatureGate(nil)

	t.Setenv(envBrowserFeatureGateEnabled, "1")
	t.Setenv(envBrowserFeatureGateFeatures, BrowserFeatureIntelligence+", other.flag")

	ConfigureBrowserFeatureGate(nil)

	cfg := CurrentBrowserFeatureGateConfig()
	if !cfg.Enabled {
		t.Fatal("feature gate should be enabled from env fallback")
	}
	if !cfg.Features[BrowserFeatureIntelligence] {
		t.Fatalf("missing %q in %+v", BrowserFeatureIntelligence, cfg.Features)
	}
	if !cfg.Features["other.flag"] {
		t.Fatalf("missing env feature %q in %+v", "other.flag", cfg.Features)
	}
}

func TestConfigureBrowserFeatureGate_PrefersLicenseOverEnv(t *testing.T) {
	prev := currentBrowserFeatureGate()
	t.Cleanup(func() { SetBrowserFeatureGate(&prev) })
	SetBrowserFeatureGate(nil)

	t.Setenv(envBrowserFeatureGateEnabled, "1")
	t.Setenv(envBrowserFeatureGateFeatures, "other.flag")

	ConfigureBrowserFeatureGate(&brainlicense.Result{
		Features: map[string]bool{
			BrowserFeatureIntelligence: true,
		},
	})

	cfg := CurrentBrowserFeatureGateConfig()
	if !cfg.Enabled {
		t.Fatal("feature gate should be enabled from license")
	}
	if !cfg.Features[BrowserFeatureIntelligence] {
		t.Fatalf("missing %q in %+v", BrowserFeatureIntelligence, cfg.Features)
	}
	if cfg.Features["other.flag"] {
		t.Fatalf("env fallback should not leak when license result exists: %+v", cfg.Features)
	}
}
