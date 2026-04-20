package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	brainerrors "github.com/leef-l/brain/sdk/errors"
	"github.com/leef-l/brain/sdk/kernel"
	"github.com/leef-l/brain/sdk/persistence"
	"github.com/leef-l/brain/sdk/protocol"
	"github.com/leef-l/brain/sdk/sidecar"
	"github.com/leef-l/brain/sdk/tool"
)

type staticResultTool struct {
	name   string
	result *tool.Result
}

func (t staticResultTool) Name() string        { return t.name }
func (t staticResultTool) Risk() tool.Risk     { return tool.RiskSafe }
func (t staticResultTool) Schema() tool.Schema { return tool.Schema{Name: t.name} }
func (t staticResultTool) Execute(context.Context, json.RawMessage) (*tool.Result, error) {
	return t.result, nil
}

func TestNewBrowserHandler_KeepsFullBrowserToolset(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	data := `{
  "tool_profiles": {
    "no_eval": {
      "include": ["browser.*"],
      "exclude": ["browser.eval"]
    }
  },
  "active_tools": {
    "delegate.browser": "no_eval"
  }
}`
	if err := os.WriteFile(configPath, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BRAIN_CONFIG", configPath)

	h := newBrowserHandler(nil)
	if _, ok := h.registry.Lookup("browser.eval"); !ok {
		t.Fatalf("browser.eval should remain available")
	}
	if _, ok := h.registry.Lookup("browser.open"); !ok {
		t.Fatalf("browser.open should remain available")
	}
	if _, ok := h.registry.Lookup("browser.drag"); !ok {
		t.Fatalf("browser.drag should remain available for slider CAPTCHA flows")
	}
	if _, ok := h.registry.Lookup("human.request_takeover"); !ok {
		t.Fatalf("human.request_takeover should remain available")
	}
}

func TestNewBrowserHandler_UsesPreconfiguredBrowserFeatureGate(t *testing.T) {
	prev := tool.CurrentBrowserFeatureGateConfig()
	t.Cleanup(func() { tool.SetBrowserFeatureGate(&prev) })

	tool.SetBrowserFeatureGate(&tool.BrowserFeatureGateConfig{
		Enabled:  true,
		Features: map[string]bool{},
	})

	h := newBrowserHandler(nil)
	impl, ok := h.registry.Lookup("browser.pattern_match")
	if !ok {
		t.Fatal("browser.pattern_match missing from browser handler registry")
	}

	res, err := impl.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute() err = %v, want nil", err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("Execute() = %+v, want gated error result", res)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(res.Output, &payload); err != nil {
		t.Fatalf("unmarshal gated result: %v", err)
	}
	if got := payload["error_code"]; got != brainerrors.CodeLicenseFeatureNotAllowed {
		t.Fatalf("error_code = %v, want %q", got, brainerrors.CodeLicenseFeatureNotAllowed)
	}
}

func TestNewBrowserHandler_LoadsBrowserFeatureGateFromEnv(t *testing.T) {
	prev := tool.CurrentBrowserFeatureGateConfig()
	t.Cleanup(func() { tool.SetBrowserFeatureGate(&prev) })
	tool.SetBrowserFeatureGate(nil)

	t.Setenv("BRAIN_BROWSER_FEATURE_GATE", "1")
	t.Setenv("BRAIN_BROWSER_FEATURES", "")

	h := newBrowserHandler(nil)
	impl, ok := h.registry.Lookup("browser.visual_inspect")
	if !ok {
		t.Fatal("browser.visual_inspect missing from browser handler registry")
	}

	res, err := impl.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute() err = %v, want nil", err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("Execute() = %+v, want gated error result from env-configured gate", res)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(res.Output, &payload); err != nil {
		t.Fatalf("unmarshal gated result: %v", err)
	}
	if got := payload["error_code"]; got != brainerrors.CodeLicenseFeatureNotAllowed {
		t.Fatalf("error_code = %v, want %q", got, brainerrors.CodeLicenseFeatureNotAllowed)
	}
}

func TestNewBrowserHumanEventSourceFactory_DegradesWithoutSession(t *testing.T) {
	tools := tool.NewBrowserTools()
	defer tool.CloseBrowserSession(tools)

	src, err := newBrowserHumanEventSourceFactory()(context.Background())
	if err != nil {
		t.Fatalf("factory err = %v, want nil", err)
	}
	if src != nil {
		t.Fatalf("factory source = %#v, want nil before session creation", src)
	}
}

func TestWantsHeadedBrowser_PrefersStructuredSubtaskIntent(t *testing.T) {
	if !wantsHeadedBrowser(&protocol.SubtaskContext{
		UserUtterance: "我要能看到你的操作",
	}, "启动浏览器大脑实例，准备接受后续网页任务") {
		t.Fatal("expected structured user utterance to force headed mode")
	}

	if wantsHeadedBrowser(&protocol.SubtaskContext{
		RenderMode: "headless",
	}, "给我看浏览器") {
		t.Fatal("explicit headless render mode should override fallback keyword matching")
	}

	if !wantsHeadedBrowser(nil, "给我看浏览器操作过程") {
		t.Fatal("expected legacy instruction keyword fallback to remain available")
	}
}

func TestEnsureCriticalBrowserTools_ReaddsCriticalBrowserTools(t *testing.T) {
	reg := tool.NewMemRegistry()
	reg.Register(tool.NewNoteTool("browser"))
	browserTools := tool.NewBrowserTools()
	defer tool.CloseBrowserSession(browserTools)

	ensureCriticalBrowserTools(reg, browserTools)

	if _, ok := reg.Lookup("browser.drag"); !ok {
		t.Fatal("browser.drag missing after ensureCriticalBrowserTools")
	}

	if _, ok := reg.Lookup("human.request_takeover"); !ok {
		t.Fatal("human.request_takeover missing after ensureCriticalBrowserTools")
	}
}

func TestRunFallbackAgentLoop_ContinuesAfterHumanResume(t *testing.T) {
	prev := runBrowserAgentLoop
	t.Cleanup(func() { runBrowserAgentLoop = prev })

	callCount := 0
	runBrowserAgentLoop = func(_ context.Context, _ sidecar.KernelCaller, _ tool.Registry, _ string, _ string, _ int, _ json.RawMessage) *sidecar.ExecuteResult {
		callCount++
		if callCount == 1 {
			return &sidecar.ExecuteResult{
				Status: "failed",
				Error:  "budget.turns_exhausted",
				Turns:  30,
			}
		}
		return &sidecar.ExecuteResult{
			Status:  "completed",
			Summary: "login succeeded",
			Turns:   3,
		}
	}

	reg := tool.NewMemRegistry()
	takeoverPayload := map[string]string{
		"outcome": "resumed",
		"note":    "slider done",
	}
	raw, _ := json.Marshal(takeoverPayload)
	reg.Register(staticResultTool{
		name: "human.request_takeover",
		result: &tool.Result{
			Output: raw,
		},
	})

	h := &browserHandler{}
	req := &sidecar.ExecuteRequest{Instruction: "登录后台并完成滑块"}
	got := h.runFallbackAgentLoop(context.Background(), req, reg, "browser prompt", 30, true)
	if got == nil {
		t.Fatal("runFallbackAgentLoop() = nil")
	}
	if got.Status != "completed" {
		t.Fatalf("status = %q, want completed", got.Status)
	}
	if callCount != 2 {
		t.Fatalf("runBrowserAgentLoop callCount = %d, want 2", callCount)
	}
	if !strings.Contains(got.Summary, "login succeeded") {
		t.Fatalf("summary = %q, want continued run summary", got.Summary)
	}
	if !strings.Contains(got.Summary, "slider done") {
		t.Fatalf("summary = %q, want takeover resume note", got.Summary)
	}
}

func TestConfigureBrowserRuntime_LoadsPersistenceBackedWiring(t *testing.T) {
	prevLib := tool.SharedAnomalyTemplateLibrary()
	prevGate := tool.CurrentBrowserFeatureGateConfig()
	t.Cleanup(func() {
		tool.SetSharedAnomalyTemplateLibrary(prevLib)
		tool.SetBrowserFeatureGate(&prevGate)
		tool.SetSitemapCache(nil)
		tool.SetHumanDemoSink(nil)
		tool.SetPatternFailureStore(nil)
		tool.SetHumanEventSourceFactory(nil)
	})

	home := t.TempDir()
	t.Setenv("HOME", home)

	stores, err := persistence.Open("sqlite", "")
	if err != nil {
		t.Fatalf("persistence.Open: %v", err)
	}
	defer stores.Close()

	rawRecovery := tool.EncodeRecoveryActions([]tool.AnomalyTemplateRecoveryAction{
		{Kind: "retry", MaxRetries: 2, BackoffMS: 100},
	})
	if err := stores.LearningStore.SaveAnomalyTemplate(context.Background(), &persistence.AnomalyTemplate{
		SignatureType:    "captcha",
		SignatureSubtype: "image",
		RecoveryActions:  rawRecovery,
		MatchCount:       3,
		SuccessCount:     2,
		FailureCount:     1,
		CreatedAt:        time.Now().UTC(),
		UpdatedAt:        time.Now().UTC(),
	}); err != nil {
		t.Fatalf("SaveAnomalyTemplate: %v", err)
	}
	if err := stores.Close(); err != nil {
		t.Fatalf("stores.Close: %v", err)
	}

	runtimeStores, _, err := configureBrowserRuntime(context.Background())
	if err != nil {
		t.Fatalf("configureBrowserRuntime: %v", err)
	}
	if runtimeStores == nil || runtimeStores.LearningStore == nil {
		t.Fatal("configureBrowserRuntime returned nil learning stores")
	}
	defer runtimeStores.Close()

	match := tool.SharedAnomalyTemplateLibrary().Match("captcha", "image", "https://example.com", "")
	if match == nil {
		t.Fatal("expected persisted anomaly template to be loaded into browser runtime library")
	}

	if err := tool.RecordPatternFailure(context.Background(), "login_flow", "https://example.com", "captcha", 2, "https://example.com/login"); err != nil {
		t.Fatalf("RecordPatternFailure: %v", err)
	}

	verifyStores, err := persistence.Open("sqlite", "")
	if err != nil {
		t.Fatalf("persistence.Open verify: %v", err)
	}
	defer verifyStores.Close()

	samples, err := verifyStores.LearningStore.ListPatternFailureSamples(context.Background(), "login_flow")
	if err != nil {
		t.Fatalf("ListPatternFailureSamples: %v", err)
	}
	if len(samples) != 1 {
		t.Fatalf("pattern failure samples = %d, want 1", len(samples))
	}
}

func TestBrowserRuntimeReloader_SeesCrossProcessUpdates(t *testing.T) {
	prevLib := tool.SharedAnomalyTemplateLibrary()
	t.Cleanup(func() {
		tool.SetSharedAnomalyTemplateLibrary(prevLib)
		tool.SetSitemapCache(nil)
		tool.SetHumanDemoSink(nil)
		tool.SetPatternFailureStore(nil)
		tool.SetHumanEventSourceFactory(nil)
	})

	home := t.TempDir()
	t.Setenv("HOME", home)

	runtimeStores, reloader, err := configureBrowserRuntime(context.Background())
	if err != nil {
		t.Fatalf("configureBrowserRuntime: %v", err)
	}
	if reloader == nil {
		t.Fatal("configureBrowserRuntime() reloader = nil")
	}
	defer runtimeStores.Close()

	writer, err := persistence.Open("sqlite", "")
	if err != nil {
		t.Fatalf("persistence.Open writer: %v", err)
	}
	defer writer.Close()

	rawRecovery := tool.EncodeRecoveryActions([]tool.AnomalyTemplateRecoveryAction{
		{Kind: "retry", MaxRetries: 1, BackoffMS: 50},
	})
	if err := writer.LearningStore.SaveAnomalyTemplate(context.Background(), &persistence.AnomalyTemplate{
		SignatureType:    "session_expired",
		SignatureSubtype: "login_wall",
		RecoveryActions:  rawRecovery,
		CreatedAt:        time.Now().UTC(),
		UpdatedAt:        time.Now().UTC(),
	}); err != nil {
		t.Fatalf("SaveAnomalyTemplate: %v", err)
	}

	patternWriter, err := tool.NewPatternLibrary("")
	if err != nil {
		t.Fatalf("NewPatternLibrary writer: %v", err)
	}
	defer patternWriter.Close()
	if err := patternWriter.Upsert(context.Background(), &tool.UIPattern{
		ID:       "cross_process_pattern",
		Category: "auth",
		Source:   "user",
		AppliesWhen: tool.MatchCondition{
			SiteHost: "example.com",
		},
		ElementRoles: map[string]tool.ElementDescriptor{
			"submit": {Role: "button", Name: "Login"},
		},
		ActionSequence: []tool.ActionStep{{Tool: "browser.click", TargetRole: "submit"}},
	}); err != nil {
		t.Fatalf("patternWriter.Upsert: %v", err)
	}

	reloader.lastCheckedAt = time.Now().Add(-2 * time.Second)
	if err := reloader.MaybeRefresh(context.Background()); err != nil {
		t.Fatalf("MaybeRefresh: %v", err)
	}

	match := tool.SharedAnomalyTemplateLibrary().Match("session_expired", "login_wall", "https://example.com", "")
	if match == nil {
		t.Fatal("expected anomaly template written by another process to become visible")
	}

	lib := tool.SharedPatternLibrary()
	if lib == nil {
		t.Fatal("SharedPatternLibrary() = nil")
	}
	if got := lib.GetAny("cross_process_pattern"); got == nil {
		t.Fatal("expected pattern written by another process to become visible")
	}
}

func TestConfigureBrowserRuntime_UsesCustomBrainDBPath(t *testing.T) {
	prevLib := tool.SharedAnomalyTemplateLibrary()
	prevGate := tool.CurrentBrowserFeatureGateConfig()
	t.Cleanup(func() {
		tool.SetSharedAnomalyTemplateLibrary(prevLib)
		tool.SetBrowserFeatureGate(&prevGate)
		tool.SetSitemapCache(nil)
		tool.SetHumanDemoSink(nil)
		tool.SetPatternFailureStore(nil)
		tool.SetHumanEventSourceFactory(nil)
	})

	customDB := filepath.Join(t.TempDir(), "custom-brain.db")
	t.Setenv(envBrainDBPath, customDB)

	stores, err := persistence.Open("sqlite", customDB)
	if err != nil {
		t.Fatalf("persistence.Open custom: %v", err)
	}

	rawRecovery := tool.EncodeRecoveryActions([]tool.AnomalyTemplateRecoveryAction{
		{Kind: "fallback_pattern", FallbackID: "login_manual"},
	})
	if err := stores.LearningStore.SaveAnomalyTemplate(context.Background(), &persistence.AnomalyTemplate{
		SignatureType:    "login_blocked",
		SignatureSubtype: "captcha",
		RecoveryActions:  rawRecovery,
		MatchCount:       1,
		SuccessCount:     1,
		CreatedAt:        time.Now().UTC(),
		UpdatedAt:        time.Now().UTC(),
	}); err != nil {
		t.Fatalf("SaveAnomalyTemplate custom: %v", err)
	}
	if err := stores.Close(); err != nil {
		t.Fatalf("custom stores.Close: %v", err)
	}

	runtimeStores, _, err := configureBrowserRuntime(context.Background())
	if err != nil {
		t.Fatalf("configureBrowserRuntime custom: %v", err)
	}
	if runtimeStores == nil || runtimeStores.LearningStore == nil {
		t.Fatal("configureBrowserRuntime custom returned nil learning stores")
	}
	defer runtimeStores.Close()

	match := tool.SharedAnomalyTemplateLibrary().Match("login_blocked", "captcha", "https://example.com", "")
	if match == nil {
		t.Fatal("expected custom BRAIN_DB_PATH anomaly template to be loaded")
	}

	if err := tool.RecordPatternFailure(context.Background(), "checkout_flow", "https://example.com", "captcha", 3, "https://example.com/checkout"); err != nil {
		t.Fatalf("RecordPatternFailure custom: %v", err)
	}

	verifyStores, err := persistence.Open("sqlite", customDB)
	if err != nil {
		t.Fatalf("persistence.Open verify custom: %v", err)
	}
	defer verifyStores.Close()

	samples, err := verifyStores.LearningStore.ListPatternFailureSamples(context.Background(), "checkout_flow")
	if err != nil {
		t.Fatalf("ListPatternFailureSamples custom: %v", err)
	}
	if len(samples) != 1 {
		t.Fatalf("custom pattern failure samples = %d, want 1", len(samples))
	}
}

func TestBrowserRuntimeReloader_MaybeRefreshReloadsAnomalyTemplates(t *testing.T) {
	prevLib := tool.SharedAnomalyTemplateLibrary()
	t.Cleanup(func() {
		tool.SetSharedAnomalyTemplateLibrary(prevLib)
		tool.SetSitemapCache(nil)
		tool.SetHumanDemoSink(nil)
		tool.SetPatternFailureStore(nil)
		tool.SetHumanEventSourceFactory(nil)
	})

	customDB := filepath.Join(t.TempDir(), "reload-brain.db")
	customPatternDB := filepath.Join(t.TempDir(), "reload-ui-patterns.db")
	syncFile := filepath.Join(filepath.Dir(customDB), "browser-runtime.sync.json")
	t.Setenv(envBrainDBPath, customDB)
	t.Setenv("BRAIN_UI_PATTERN_DB_PATH", customPatternDB)
	t.Setenv(envBrowserRuntimeSyncFile, syncFile)

	stores, err := persistence.Open("sqlite", customDB)
	if err != nil {
		t.Fatalf("persistence.Open reload: %v", err)
	}
	if err := stores.LearningStore.SaveAnomalyTemplate(context.Background(), &persistence.AnomalyTemplate{
		SignatureType:    "session_expired",
		SignatureSubtype: "login",
		RecoveryActions:  tool.EncodeRecoveryActions([]tool.AnomalyTemplateRecoveryAction{{Kind: "retry"}}),
		CreatedAt:        time.Now().UTC(),
		UpdatedAt:        time.Now().UTC(),
	}); err != nil {
		t.Fatalf("SaveAnomalyTemplate initial: %v", err)
	}
	if err := stores.Close(); err != nil {
		t.Fatalf("stores.Close reload: %v", err)
	}
	if err := kernel.WriteBrowserRuntimeProjectionFile(syncFile, kernel.BrowserRuntimeProjection{
		Version:           1,
		BrainDBPath:       customDB,
		UIPatternDBPath:   customPatternDB,
		PersistenceDriver: "sqlite",
		PersistenceDSN:    customDB,
		SyncFile:          syncFile,
		UpdatedAt:         time.Now().UTC(),
	}); err != nil {
		t.Fatalf("WriteBrowserRuntimeProjectionFile initial: %v", err)
	}

	runtimeStores, reloader, err := configureBrowserRuntime(context.Background())
	if err != nil {
		t.Fatalf("configureBrowserRuntime reload: %v", err)
	}
	defer runtimeStores.Close()
	if reloader == nil {
		t.Fatal("expected non-nil browser runtime reloader")
	}

	if err := tool.RefreshSharedPatternLibraryIfChanged(); err != nil {
		t.Fatalf("RefreshSharedPatternLibraryIfChanged initial: %v", err)
	}

	patternWriter, err := tool.NewPatternLibrary("")
	if err != nil {
		t.Fatalf("NewPatternLibrary pattern writer: %v", err)
	}
	if err := patternWriter.Upsert(context.Background(), &tool.UIPattern{
		ID:             "reloaded_variant",
		Category:       "auth",
		Source:         "learned",
		AppliesWhen:    tool.MatchCondition{Has: []string{"input[type=\"password\"]"}},
		ActionSequence: []tool.ActionStep{{Tool: "browser.click", Params: map[string]interface{}{"selector": "button"}}},
		Enabled:        true,
		Pending:        true,
	}); err != nil {
		patternWriter.Close()
		t.Fatalf("pattern Upsert added: %v", err)
	}
	if err := patternWriter.Close(); err != nil {
		t.Fatalf("patternWriter.Close: %v", err)
	}

	writer, err := persistence.Open("sqlite", customDB)
	if err != nil {
		t.Fatalf("persistence.Open reload writer: %v", err)
	}
	defer writer.Close()
	if err := writer.LearningStore.SaveAnomalyTemplate(context.Background(), &persistence.AnomalyTemplate{
		SignatureType:    "captcha",
		SignatureSubtype: "image",
		RecoveryActions:  tool.EncodeRecoveryActions([]tool.AnomalyTemplateRecoveryAction{{Kind: "human_intervention"}}),
		CreatedAt:        time.Now().UTC(),
		UpdatedAt:        time.Now().UTC(),
	}); err != nil {
		t.Fatalf("SaveAnomalyTemplate added: %v", err)
	}
	if err := kernel.WriteBrowserRuntimeProjectionFile(syncFile, kernel.BrowserRuntimeProjection{
		Version:           2,
		BrainDBPath:       customDB,
		UIPatternDBPath:   customPatternDB,
		PersistenceDriver: "sqlite",
		PersistenceDSN:    customDB,
		SyncFile:          syncFile,
		UpdatedAt:         time.Now().UTC(),
	}); err != nil {
		t.Fatalf("WriteBrowserRuntimeProjectionFile updated: %v", err)
	}

	reloader.mu.Lock()
	reloader.lastCheckedAt = time.Time{}
	reloader.mu.Unlock()
	tool.ForceSharedPatternLibraryRefreshForTest()

	if err := reloader.MaybeRefresh(context.Background()); err != nil {
		t.Fatalf("MaybeRefresh: %v", err)
	}
	if tool.SharedAnomalyTemplateLibrary().Match("captcha", "image", "", "") == nil {
		t.Fatal("expected newly added anomaly template to be visible after refresh")
	}
	if tool.SharedPatternLibrary().GetAny("reloaded_variant") == nil {
		t.Fatal("expected shared pattern library to refresh newly added variant")
	}
}

func TestBrowserRuntimeReloader_AppliesFeatureGateFromSyncFile(t *testing.T) {
	prevGate := tool.CurrentBrowserFeatureGateConfig()
	t.Cleanup(func() {
		tool.SetBrowserFeatureGate(&prevGate)
		tool.SetSitemapCache(nil)
		tool.SetHumanDemoSink(nil)
		tool.SetPatternFailureStore(nil)
		tool.SetHumanEventSourceFactory(nil)
	})

	home := t.TempDir()
	t.Setenv("HOME", home)

	syncFile := filepath.Join(home, ".brain", "browser-runtime.sync.json")
	t.Setenv(envBrowserRuntimeSyncFile, syncFile)
	if err := kernel.WriteBrowserRuntimeProjectionFile(syncFile, kernel.BrowserRuntimeProjection{
		BrainDBPath:        filepath.Join(home, ".brain", "brain.db"),
		UIPatternDBPath:    filepath.Join(home, ".brain", "ui_patterns.db"),
		PersistenceDriver:  "sqlite",
		PersistenceDSN:     filepath.Join(home, ".brain", "brain.db"),
		FeatureGateEnabled: true,
		Features:           map[string]bool{tool.BrowserFeatureIntelligence: true},
		SyncFile:           syncFile,
	}); err != nil {
		t.Fatalf("WriteBrowserRuntimeProjectionFile: %v", err)
	}

	runtimeStores, reloader, err := configureBrowserRuntime(context.Background())
	if err != nil {
		t.Fatalf("configureBrowserRuntime: %v", err)
	}
	defer runtimeStores.Close()

	cfg := tool.CurrentBrowserFeatureGateConfig()
	if !cfg.Enabled || !cfg.Features[tool.BrowserFeatureIntelligence] {
		t.Fatalf("feature gate after configure = %+v, want projected intelligence feature", cfg)
	}

	if err := kernel.WriteBrowserRuntimeProjectionFile(syncFile, kernel.BrowserRuntimeProjection{
		BrainDBPath:        filepath.Join(home, ".brain", "brain.db"),
		UIPatternDBPath:    filepath.Join(home, ".brain", "ui_patterns.db"),
		PersistenceDriver:  "sqlite",
		PersistenceDSN:     filepath.Join(home, ".brain", "brain.db"),
		FeatureGateEnabled: true,
		Features:           map[string]bool{"browser-pro.evidence": true},
		SyncFile:           syncFile,
	}); err != nil {
		t.Fatalf("WriteBrowserRuntimeProjectionFile update: %v", err)
	}

	reloader.mu.Lock()
	reloader.lastCheckedAt = time.Time{}
	reloader.mu.Unlock()
	if err := reloader.MaybeRefresh(context.Background()); err != nil {
		t.Fatalf("MaybeRefresh: %v", err)
	}

	cfg = tool.CurrentBrowserFeatureGateConfig()
	if cfg.Features[tool.BrowserFeatureIntelligence] {
		t.Fatalf("feature gate still contains stale intelligence feature: %+v", cfg)
	}
	if !cfg.Features["browser-pro.evidence"] {
		t.Fatalf("feature gate missing refreshed browser-pro.evidence: %+v", cfg)
	}
}
