package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/leef-l/brain/cmd/brain/cliruntime"
	"github.com/leef-l/brain/sdk/kernel"
	brainlicense "github.com/leef-l/brain/sdk/license"
	"github.com/leef-l/brain/sdk/llm"
	"github.com/leef-l/brain/sdk/persistence"
	"github.com/leef-l/brain/sdk/tool"
)

type stubPatternFailureStore struct{}

func (stubPatternFailureStore) SavePatternFailureSample(context.Context, *persistence.PatternFailureSample) error {
	return nil
}

func (stubPatternFailureStore) ListPatternFailureSamples(context.Context, string) ([]*persistence.PatternFailureSample, error) {
	return nil, nil
}

type stubAnomalyTemplateStore struct {
	templates []*persistence.AnomalyTemplate
}

func (stubAnomalyTemplateStore) SaveProfile(context.Context, *persistence.LearningProfile) error {
	return nil
}
func (stubAnomalyTemplateStore) SaveTaskScore(context.Context, *persistence.LearningTaskScore) error {
	return nil
}
func (stubAnomalyTemplateStore) ListProfiles(context.Context) ([]*persistence.LearningProfile, error) {
	return nil, nil
}
func (stubAnomalyTemplateStore) ListTaskScores(context.Context, string) ([]*persistence.LearningTaskScore, error) {
	return nil, nil
}
func (stubAnomalyTemplateStore) SaveSequence(context.Context, *persistence.LearningSequence) error {
	return nil
}
func (stubAnomalyTemplateStore) ListSequences(context.Context, int) ([]*persistence.LearningSequence, error) {
	return nil, nil
}
func (stubAnomalyTemplateStore) SaveInteractionSequence(context.Context, *persistence.InteractionSequence) error {
	return nil
}
func (stubAnomalyTemplateStore) ListInteractionSequences(context.Context, string, int) ([]*persistence.InteractionSequence, error) {
	return nil, nil
}
func (stubAnomalyTemplateStore) SavePreference(context.Context, *persistence.LearningPreference) error {
	return nil
}
func (stubAnomalyTemplateStore) GetPreference(context.Context, string) (*persistence.LearningPreference, error) {
	return nil, nil
}
func (stubAnomalyTemplateStore) ListPreferences(context.Context) ([]*persistence.LearningPreference, error) {
	return nil, nil
}
func (stubAnomalyTemplateStore) SaveDailySummary(context.Context, *persistence.DailySummary) error {
	return nil
}
func (stubAnomalyTemplateStore) GetDailySummary(context.Context, string) (*persistence.DailySummary, error) {
	return nil, nil
}
func (stubAnomalyTemplateStore) ListDailySummaries(context.Context, int) ([]*persistence.DailySummary, error) {
	return nil, nil
}
func (s stubAnomalyTemplateStore) SaveAnomalyTemplate(context.Context, *persistence.AnomalyTemplate) error {
	return nil
}
func (s stubAnomalyTemplateStore) GetAnomalyTemplate(_ context.Context, id int64) (*persistence.AnomalyTemplate, error) {
	for _, tpl := range s.templates {
		if tpl != nil && tpl.ID == id {
			return tpl, nil
		}
	}
	return nil, nil
}
func (s stubAnomalyTemplateStore) ListAnomalyTemplates(context.Context) ([]*persistence.AnomalyTemplate, error) {
	return s.templates, nil
}
func (stubAnomalyTemplateStore) DeleteAnomalyTemplate(context.Context, int64) error {
	return nil
}
func (stubAnomalyTemplateStore) UpsertSiteAnomalyProfile(context.Context, *persistence.SiteAnomalyProfile) error {
	return nil
}
func (stubAnomalyTemplateStore) ListSiteAnomalyProfiles(context.Context, string) ([]*persistence.SiteAnomalyProfile, error) {
	return nil, nil
}
func (stubAnomalyTemplateStore) SavePatternFailureSample(context.Context, *persistence.PatternFailureSample) error {
	return nil
}
func (stubAnomalyTemplateStore) ListPatternFailureSamples(context.Context, string) ([]*persistence.PatternFailureSample, error) {
	return nil, nil
}
func (stubAnomalyTemplateStore) SaveHumanDemoSequence(context.Context, *persistence.HumanDemoSequence) error {
	return nil
}
func (stubAnomalyTemplateStore) ListHumanDemoSequences(context.Context, bool) ([]*persistence.HumanDemoSequence, error) {
	return nil, nil
}
func (stubAnomalyTemplateStore) SaveSitemapSnapshot(context.Context, *persistence.SitemapSnapshot) error {
	return nil
}
func (stubAnomalyTemplateStore) GetSitemapSnapshot(context.Context, string, int) (*persistence.SitemapSnapshot, error) {
	return nil, nil
}
func (stubAnomalyTemplateStore) PurgeSitemapSnapshots(context.Context, time.Time) (int64, error) {
	return 0, nil
}
func (stubAnomalyTemplateStore) GetHumanDemoSequence(context.Context, int64) (*persistence.HumanDemoSequence, error) {
	return nil, nil
}
func (stubAnomalyTemplateStore) ApproveHumanDemoSequence(context.Context, int64) error {
	return nil
}
func (stubAnomalyTemplateStore) DeleteHumanDemoSequence(context.Context, int64) error {
	return nil
}
func (stubAnomalyTemplateStore) PurgeHumanDemoSequences(context.Context, time.Time) (int64, error) {
	return 0, nil
}

func TestSharedBrowserHumanEventSourceFactory_DegradesWithoutSession(t *testing.T) {
	if sess, ok := tool.CurrentSharedBrowserSession(); ok || sess != nil {
		t.Skip("shared browser session already initialized in this process")
	}

	src, err := newSharedBrowserHumanEventSourceFactory()(context.Background())
	if err != nil {
		t.Fatalf("factory returned error without session: %v", err)
	}
	if src != nil {
		t.Fatalf("factory source=%T, want nil without session", src)
	}
	if sess, ok := tool.CurrentSharedBrowserSession(); ok || sess != nil {
		t.Fatal("factory initialized shared browser session unexpectedly")
	}
}

func TestRunPatternSplitScanOnce_NoStoreNoops(t *testing.T) {
	n, err := runPatternSplitScanOnce(context.Background(), nil)
	if err != nil {
		t.Fatalf("runPatternSplitScanOnce(nil): %v", err)
	}
	if n != 0 {
		t.Fatalf("runPatternSplitScanOnce(nil) = %d, want 0", n)
	}
}

func TestRunPatternSplitScanOnce_UsesSharedLibraryAndStore(t *testing.T) {
	prevLib := patternSplitSharedLibrary
	prevScan := patternSplitScan
	t.Cleanup(func() {
		patternSplitSharedLibrary = prevLib
		patternSplitScan = prevScan
	})

	lib := &tool.PatternLibrary{}
	called := false
	patternSplitSharedLibrary = func() *tool.PatternLibrary { return lib }
	patternSplitScan = func(ctx context.Context, got *tool.PatternLibrary, store tool.PatternFailureStore) ([]string, error) {
		called = true
		if got != lib {
			t.Fatalf("scan lib = %p, want %p", got, lib)
		}
		if _, ok := store.(stubPatternFailureStore); !ok {
			t.Fatalf("scan store type = %T, want stubPatternFailureStore", store)
		}
		return []string{"v1", "v2"}, nil
	}

	n, err := runPatternSplitScanOnce(context.Background(), stubPatternFailureStore{})
	if err != nil {
		t.Fatalf("runPatternSplitScanOnce: %v", err)
	}
	if !called {
		t.Fatal("pattern split scan was not called")
	}
	if n != 2 {
		t.Fatalf("runPatternSplitScanOnce count = %d, want 2", n)
	}
}

func TestRunPatternSplitScanOnce_PropagatesScanError(t *testing.T) {
	prevLib := patternSplitSharedLibrary
	prevScan := patternSplitScan
	t.Cleanup(func() {
		patternSplitSharedLibrary = prevLib
		patternSplitScan = prevScan
	})

	wantErr := errors.New("boom")
	patternSplitSharedLibrary = func() *tool.PatternLibrary { return &tool.PatternLibrary{} }
	patternSplitScan = func(context.Context, *tool.PatternLibrary, tool.PatternFailureStore) ([]string, error) {
		return nil, wantErr
	}

	n, err := runPatternSplitScanOnce(context.Background(), stubPatternFailureStore{})
	if !errors.Is(err, wantErr) {
		t.Fatalf("runPatternSplitScanOnce error = %v, want %v", err, wantErr)
	}
	if n != 0 {
		t.Fatalf("runPatternSplitScanOnce count = %d, want 0 on error", n)
	}
}

func TestLoadSharedAnomalyTemplateLibrary_LoadsPersistedTemplates(t *testing.T) {
	prev := tool.SharedAnomalyTemplateLibrary()
	t.Cleanup(func() { tool.SetSharedAnomalyTemplateLibrary(prev) })

	learner := kernel.NewLearningEngineWithStore(stubAnomalyTemplateStore{
		templates: []*persistence.AnomalyTemplate{
			{
				ID:                7,
				SignatureType:     "captcha",
				SignatureSubtype:  "hcaptcha",
				SignatureSite:     `example\.com`,
				SignatureSeverity: "high",
				RecoveryActions:   json.RawMessage(`[{"kind":"retry","max_retries":2,"backoff_ms":500}]`),
				MatchCount:        11,
				SuccessCount:      8,
				FailureCount:      3,
				CreatedAt:         time.Unix(100, 0).UTC(),
				UpdatedAt:         time.Unix(200, 0).UTC(),
			},
		},
	})

	if err := loadSharedAnomalyTemplateLibrary(context.Background(), learner); err != nil {
		t.Fatalf("loadSharedAnomalyTemplateLibrary: %v", err)
	}

	lib := tool.SharedAnomalyTemplateLibrary()
	got := lib.Match("captcha", "hcaptcha", "https://example.com/login", "high")
	if got == nil {
		t.Fatal("persisted template not loaded into shared library")
	}
	if got.ID != 7 {
		t.Fatalf("template id=%d, want 7", got.ID)
	}
	if len(got.Recovery) != 1 || got.Recovery[0].Kind != "retry" || got.Recovery[0].MaxRetries != 2 {
		t.Fatalf("recovery=%+v, want retry max_retries=2", got.Recovery)
	}
	if got.Stats.MatchCount != 11 || got.Stats.SuccessCount != 8 || got.Stats.FailureCount != 3 {
		t.Fatalf("stats=%+v, want 11/8/3", got.Stats)
	}
}

func TestPublishBrowserRuntimeProjection_WritesSyncFile(t *testing.T) {
	prev := tool.CurrentBrowserFeatureGateConfig()
	t.Cleanup(func() { tool.SetBrowserFeatureGate(&prev) })

	runtimeDir := t.TempDir()
	tool.SetBrowserFeatureGate(&tool.BrowserFeatureGateConfig{
		Enabled:  true,
		Features: map[string]bool{tool.BrowserFeatureIntelligence: true},
	})

	if err := publishBrowserRuntimeProjection(runtimeDir); err != nil {
		t.Fatalf("publishBrowserRuntimeProjection: %v", err)
	}

	path := filepath.Join(runtimeDir, "browser-runtime.sync.json")
	projection, err := kernel.ReadBrowserRuntimeProjectionFile(path)
	if err != nil {
		t.Fatalf("ReadBrowserRuntimeProjectionFile: %v", err)
	}
	if projection == nil {
		t.Fatal("projection = nil")
	}
	if projection.BrainDBPath != filepath.Join(runtimeDir, "brain.db") {
		t.Fatalf("BrainDBPath = %q, want %q", projection.BrainDBPath, filepath.Join(runtimeDir, "brain.db"))
	}
	if !projection.FeatureGateEnabled || !projection.Features[tool.BrowserFeatureIntelligence] {
		t.Fatalf("projection features = %+v, want projected intelligence gate", projection.Features)
	}
}

func TestLoadSharedAnomalyTemplateLibrary_RejectsBrokenRecoveryJSON(t *testing.T) {
	prev := tool.SharedAnomalyTemplateLibrary()
	t.Cleanup(func() { tool.SetSharedAnomalyTemplateLibrary(prev) })

	tool.SetSharedAnomalyTemplateLibrary(tool.NewAnomalyTemplateLibrary())
	tool.SharedAnomalyTemplateLibrary().Upsert(&tool.AnomalyTemplate{
		ID:        99,
		Signature: tool.AnomalyTemplateSignature{Type: "existing"},
		Recovery:  []tool.AnomalyTemplateRecoveryAction{{Kind: "retry"}},
	})

	learner := kernel.NewLearningEngineWithStore(stubAnomalyTemplateStore{
		templates: []*persistence.AnomalyTemplate{
			{
				ID:              8,
				SignatureType:   "captcha",
				RecoveryActions: json.RawMessage(`{"broken":true}`),
			},
		},
	})

	err := loadSharedAnomalyTemplateLibrary(context.Background(), learner)
	if err == nil {
		t.Fatal("expected decode error for broken recovery json")
	}

	if got := tool.SharedAnomalyTemplateLibrary().Match("existing", "", "", ""); got == nil {
		t.Fatal("shared anomaly template library should not be replaced on load error")
	}
}

func TestConfigureBrowserFeatureGate_FallsBackToEnvWhenLicenseMissing(t *testing.T) {
	prevVerify := browserLicenseVerifyOptionsFromEnv
	prevCheck := browserLicenseCheckSidecar
	t.Cleanup(func() {
		browserLicenseVerifyOptionsFromEnv = prevVerify
		browserLicenseCheckSidecar = prevCheck
	})

	t.Setenv("BRAIN_BROWSER_FEATURES", "")
	tool.SetBrowserFeatureGate(&tool.BrowserFeatureGateConfig{
		Enabled:  true,
		Features: map[string]bool{tool.BrowserFeatureIntelligence: true},
	})
	t.Cleanup(func() { tool.SetBrowserFeatureGate(nil) })

	browserLicenseVerifyOptionsFromEnv = func(opts brainlicense.VerifyOptions) (brainlicense.VerifyOptions, error) {
		return opts, nil
	}
	browserLicenseCheckSidecar = func(string, brainlicense.VerifyOptions) (*brainlicense.Result, error) {
		return nil, nil
	}

	if err := configureBrowserFeatureGate(); err != nil {
		t.Fatalf("configureBrowserFeatureGate() err = %v, want nil", err)
	}

	got := tool.CurrentBrowserFeatureGateConfig()
	if got.Enabled {
		t.Fatalf("feature gate enabled = true, want false when env unset")
	}
}

func TestConfigureBrowserFeatureGate_UsesLicenseBeforeEnv(t *testing.T) {
	prevVerify := browserLicenseVerifyOptionsFromEnv
	prevCheck := browserLicenseCheckSidecar
	t.Cleanup(func() {
		browserLicenseVerifyOptionsFromEnv = prevVerify
		browserLicenseCheckSidecar = prevCheck
	})

	t.Setenv("BRAIN_BROWSER_FEATURES", "other.flag")
	tool.SetBrowserFeatureGate(nil)
	t.Cleanup(func() { tool.SetBrowserFeatureGate(nil) })

	browserLicenseVerifyOptionsFromEnv = func(opts brainlicense.VerifyOptions) (brainlicense.VerifyOptions, error) {
		return opts, nil
	}
	browserLicenseCheckSidecar = func(string, brainlicense.VerifyOptions) (*brainlicense.Result, error) {
		return &brainlicense.Result{
			Features: map[string]bool{
				tool.BrowserFeatureIntelligence: true,
			},
		}, nil
	}

	if err := configureBrowserFeatureGate(); err != nil {
		t.Fatalf("configureBrowserFeatureGate() err = %v, want nil", err)
	}

	got := tool.CurrentBrowserFeatureGateConfig()
	if !got.Enabled {
		t.Fatal("feature gate enabled = false, want true")
	}
	if !got.Features[tool.BrowserFeatureIntelligence] {
		t.Fatalf("missing feature %q in %+v", tool.BrowserFeatureIntelligence, got.Features)
	}
	if got.Features["other.flag"] {
		t.Fatalf("env feature should not override license projection: %+v", got.Features)
	}
}

func TestExecuteRun_CancelledRunStaysCancelled(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(300 * time.Millisecond):
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"id":          "msg_test_001",
			"type":        "message",
			"model":       "claude-sonnet-4-20250514",
			"role":        "assistant",
			"stop_reason": "end_turn",
			"content": []map[string]string{
				{"type": "text", "text": "done"},
			},
			"usage": map[string]int{
				"input_tokens":                1,
				"output_tokens":               1,
				"cache_read_input_tokens":     0,
				"cache_creation_input_tokens": 0,
			},
		})
	}))
	defer provider.Close()

	runtime, err := (&cliruntime.FileBackend{DataDir: t.TempDir()}).Open("central")
	if err != nil {
		t.Fatalf("open runtime: %v", err)
	}
	runRec, err := runtime.RunStore.Create("central", "sleep", string(modeDefault), t.TempDir())
	if err != nil {
		t.Fatalf("create run record: %v", err)
	}

	mgr := &runManager{store: runtime.RunStore, rootCtx: context.Background()}
	ctx, cancel := context.WithCancel(context.Background())
	entry := &runEntry{
		ID:        runRec.ID,
		Status:    "running",
		Brain:     "central",
		Prompt:    "sleep",
		CreatedAt: time.Now().UTC(),
		taskExec:  kernel.NewTaskExecution(kernel.TaskExecutionConfig{}),
		cancel:    cancel,
	}
	mgr.runs.Store(entry.ID, entry)

	done := make(chan struct{})
	go func() {
		defer close(done)
		executeRun(ctx, entry, mgr, runtime, providerSession{
			Provider: llm.NewAnthropicProvider(provider.URL, "test-key", "claude-sonnet-4-20250514"),
			Name:     "anthropic",
			Model:    "claude-sonnet-4-20250514",
		}, createRunRequest{
			Prompt:   "sleep and then maybe cancel",
			Brain:    "central",
			MaxTurns: 2,
		}, runRec, nil, modeDefault)
	}()

	time.Sleep(50 * time.Millisecond)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/v1/runs/"+entry.ID, nil)
	handleCancelRun(rec, req, mgr, entry.ID)

	if rec.Code != http.StatusOK {
		t.Fatalf("cancel status=%d, want 200", rec.Code)
	}

	var cancelResp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &cancelResp); err != nil {
		t.Fatalf("decode cancel response: %v", err)
	}
	if got := cancelResp["status"]; got != "cancelled" {
		t.Fatalf("cancel response status=%q, want cancelled", got)
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("executeRun did not finish after cancellation")
	}

	snap := entry.snapshot()
	if snap.Status != "cancelled" {
		t.Fatalf("final status=%q, want cancelled", snap.Status)
	}
}

func TestHandleCreateRun_RestrictedRejectDoesNotPersistRun(t *testing.T) {
	runtime, err := (&cliruntime.FileBackend{DataDir: t.TempDir()}).Open("central")
	if err != nil {
		t.Fatalf("open runtime: %v", err)
	}

	mgr := &runManager{store: runtime.RunStore, rootCtx: context.Background()}
	body := bytes.NewBufferString(`{"prompt":"hello","brain":"central","model_config":{"provider":"mock"}}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/runs", body)
	rec := httptest.NewRecorder()

	handleCreateRun(rec, req, mgr, runtime, nil, 1, modeRestricted, t.TempDir(), serveWorkdirPolicyConfined)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", rec.Code)
	}
	if runs := runtime.RunStore.List(0, "all"); len(runs) != 0 {
		t.Fatalf("unexpected persisted runs after rejected request: %d", len(runs))
	}
}

func TestHandleCreateRun_ConcurrencyRejectDoesNotPersistRun(t *testing.T) {
	runtime, err := (&cliruntime.FileBackend{DataDir: t.TempDir()}).Open("central")
	if err != nil {
		t.Fatalf("open runtime: %v", err)
	}

	mgr := &runManager{store: runtime.RunStore, rootCtx: context.Background()}
	block := make(chan struct{})
	if !mgr.reserveSlot(1) {
		t.Fatal("failed to reserve initial slot")
	}
	mgr.launchReserved(&runEntry{
		ID:        "existing-run",
		Status:    "running",
		Brain:     "central",
		Prompt:    "hold",
		CreatedAt: time.Now().UTC(),
	}, func() {
		<-block
	})
	defer func() {
		close(block)
		waitCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := mgr.wait(waitCtx); err != nil {
			t.Fatalf("wait running entry: %v", err)
		}
	}()

	body := bytes.NewBufferString(`{"prompt":"hello","brain":"central","model_config":{"provider":"mock"}}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/runs", body)
	rec := httptest.NewRecorder()

	handleCreateRun(rec, req, mgr, runtime, nil, 1, modeDefault, t.TempDir(), serveWorkdirPolicyConfined)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status=%d, want 429", rec.Code)
	}
	if runs := runtime.RunStore.List(0, "all"); len(runs) != 0 {
		t.Fatalf("unexpected persisted runs after concurrency rejection: %d", len(runs))
	}
}
