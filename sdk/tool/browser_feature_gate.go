package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	brainerrors "github.com/leef-l/brain/sdk/errors"
	brainlicense "github.com/leef-l/brain/sdk/license"
)

const (
	// BrowserFeatureIntelligence gates the paid "intelligence" package in P4.
	// Current heavy tools wired behind it are:
	//   - browser.understand
	//   - browser.pattern_match
	//   - browser.visual_inspect
	BrowserFeatureIntelligence = "browser-pro.intelligence"

	envBrowserFeatureGateEnabled  = "BRAIN_BROWSER_FEATURE_GATE"
	envBrowserFeatureGateFeatures = "BRAIN_BROWSER_FEATURES"
)

// BrowserFeatureGateConfig controls whether selected browser tools require
// explicit license features. When Enabled is false, every tool is allowed and
// the current free runtime behavior stays unchanged.
type BrowserFeatureGateConfig struct {
	Enabled  bool
	Features map[string]bool
}

var (
	browserFeatureGateMu  sync.RWMutex
	browserFeatureGateCfg = &BrowserFeatureGateConfig{}
)

// SetBrowserFeatureGate swaps the process-wide browser feature gate config.
// Passing nil resets it to "disabled", which preserves today's free behavior.
func SetBrowserFeatureGate(cfg *BrowserFeatureGateConfig) {
	if cfg == nil {
		cfg = &BrowserFeatureGateConfig{}
	}

	cloned := &BrowserFeatureGateConfig{
		Enabled:  cfg.Enabled,
		Features: make(map[string]bool, len(cfg.Features)),
	}
	for name, allowed := range cfg.Features {
		cloned.Features[name] = allowed
	}

	browserFeatureGateMu.Lock()
	browserFeatureGateCfg = cloned
	browserFeatureGateMu.Unlock()
}

func currentBrowserFeatureGate() BrowserFeatureGateConfig {
	browserFeatureGateMu.RLock()
	defer browserFeatureGateMu.RUnlock()

	out := BrowserFeatureGateConfig{
		Enabled:  browserFeatureGateCfg.Enabled,
		Features: make(map[string]bool, len(browserFeatureGateCfg.Features)),
	}
	for name, allowed := range browserFeatureGateCfg.Features {
		out.Features[name] = allowed
	}
	return out
}

// CurrentBrowserFeatureGateConfig returns a copy of the active browser feature
// gate configuration. It is primarily intended for host-side diagnostics and
// tests.
func CurrentBrowserFeatureGateConfig() BrowserFeatureGateConfig {
	return currentBrowserFeatureGate()
}

// ConfigureBrowserFeatureGate applies browser runtime gating from the highest
// priority source available:
//  1. validated license result
//  2. explicit environment overrides
//  3. disabled gate (free/default runtime behavior)
func ConfigureBrowserFeatureGate(res *brainlicense.Result) {
	if cfg, ok := browserFeatureGateFromLicense(res); ok {
		SetBrowserFeatureGate(cfg)
		return
	}
	if cfg, ok := browserFeatureGateFromEnv(); ok {
		SetBrowserFeatureGate(cfg)
		return
	}
	SetBrowserFeatureGate(nil)
}

// ConfigureBrowserFeatureGateFromLicense projects a validated paid-brain
// license result onto the process-wide browser feature gate.
//
// The current P4 mapping is intentionally narrow:
//   - if no relevant browser-pro feature is present, the gate stays disabled
//   - if any browser-pro feature is present, the gate becomes enabled and only
//     explicitly allowed features pass
func ConfigureBrowserFeatureGateFromLicense(res *brainlicense.Result) {
	cfg, ok := browserFeatureGateFromLicense(res)
	if !ok {
		SetBrowserFeatureGate(nil)
		return
	}
	SetBrowserFeatureGate(cfg)
}

func browserFeatureGateFromLicense(res *brainlicense.Result) (*BrowserFeatureGateConfig, bool) {
	if res == nil || len(res.Features) == 0 {
		return nil, false
	}

	features := map[string]bool{}
	for name, allowed := range res.Features {
		if !allowed || !strings.HasPrefix(name, "browser-pro.") {
			continue
		}
		features[name] = true
	}
	if len(features) == 0 {
		return nil, false
	}

	return &BrowserFeatureGateConfig{
		Enabled:  true,
		Features: features,
	}, true
}

func applyBrowserFeatureGateEnvDefaults() {
	cfg := currentBrowserFeatureGate()
	if cfg.Enabled || len(cfg.Features) > 0 {
		return
	}

	envCfg, ok := browserFeatureGateFromEnv()
	if !ok {
		return
	}
	SetBrowserFeatureGate(envCfg)
}

func browserFeatureGateFromEnv() (*BrowserFeatureGateConfig, bool) {
	enabledRaw := strings.TrimSpace(os.Getenv(envBrowserFeatureGateEnabled))
	featuresRaw := strings.TrimSpace(os.Getenv(envBrowserFeatureGateFeatures))
	if enabledRaw == "" && featuresRaw == "" {
		return nil, false
	}

	cfg := &BrowserFeatureGateConfig{
		Enabled:  envEnabled(enabledRaw) || featuresRaw != "",
		Features: map[string]bool{},
	}
	for _, part := range strings.Split(featuresRaw, ",") {
		name := strings.TrimSpace(part)
		if name == "" {
			continue
		}
		cfg.Features[name] = true
	}
	return cfg, true
}

func envEnabled(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func browserFeatureForTool(toolName string) string {
	switch toolName {
	case "browser.understand", "browser.pattern_match", "browser.visual_inspect":
		return BrowserFeatureIntelligence
	default:
		return ""
	}
}

type browserFeatureGatedTool struct {
	inner   Tool
	feature string
}

func (t *browserFeatureGatedTool) Name() string   { return t.inner.Name() }
func (t *browserFeatureGatedTool) Risk() Risk     { return t.inner.Risk() }
func (t *browserFeatureGatedTool) Schema() Schema { return t.inner.Schema() }

func (t *browserFeatureGatedTool) Execute(ctx context.Context, args json.RawMessage) (*Result, error) {
	cfg := currentBrowserFeatureGate()
	if !cfg.Enabled || cfg.Features[t.feature] {
		return t.inner.Execute(ctx, args)
	}

	out, err := json.Marshal(map[string]interface{}{
		"error":      fmt.Sprintf("license feature %q is required for %s", t.feature, t.Name()),
		"message":    fmt.Sprintf("license feature %q is required for %s", t.feature, t.Name()),
		"error_code": brainerrors.CodeLicenseFeatureNotAllowed,
		"feature":    t.feature,
		"tool":       t.Name(),
	})
	if err != nil {
		return &Result{
			IsError: true,
			Output: json.RawMessage(fmt.Sprintf(`{"error_code":%q,"tool":%q}`,
				brainerrors.CodeLicenseFeatureNotAllowed, t.Name())),
		}, nil
	}
	return &Result{IsError: true, Output: out}, nil
}
