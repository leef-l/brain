// Command brain-browser is the BrowserBrain sidecar binary.
//
// BrowserBrain is a specialist brain that fully simulates human browser
// interaction — clicking, typing, scrolling, dragging, hovering, file
// uploads, screenshots, JavaScript evaluation, and waiting for conditions.
//
// It runs its own Agent Loop internally, calling llm.complete via reverse
// RPC to the Kernel, and executing CDP-based browser tools locally.
//
// The browser tools use a zero-dependency CDP client (Chrome DevTools Protocol)
// built on Go's standard library. It supports any Chromium-based browser:
// Chrome, Chromium, Edge, Brave, Opera, Vivaldi.
//
// See 02-BrainKernel设计.md §3.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"time"

	brain "github.com/leef-l/brain"
	"github.com/leef-l/brain/sdk/agent"
	"github.com/leef-l/brain/sdk/executionpolicy"
	"github.com/leef-l/brain/sdk/kernel"
	"github.com/leef-l/brain/sdk/license"
	"github.com/leef-l/brain/sdk/persistence"
	"github.com/leef-l/brain/sdk/sidecar"
	"github.com/leef-l/brain/sdk/tool"
	"github.com/leef-l/brain/sdk/tool/cdp"
	"github.com/leef-l/brain/sdk/toolguard"
	"github.com/leef-l/brain/sdk/toolpolicy"
)

const envBrainDBPath = "BRAIN_DB_PATH"
const envBrowserRuntimeSyncFile = "BRAIN_BROWSER_RUNTIME_SYNC_FILE"

type browserHandler struct {
	registry     tool.Registry
	caller       sidecar.KernelCaller
	browserTools []tool.Tool
	learner      *kernel.DefaultBrainLearner
	reloader     *browserRuntimeReloader
}

type browserLearningStoreSitemapCache struct {
	store persistence.LearningStore
}

type browserRuntimeReloader struct {
	mu            sync.Mutex
	store         persistence.LearningStore
	lastCheckedAt time.Time
	syncFile      string
	lastVersion   int64
	stopCh        chan struct{}
}

func (c browserLearningStoreSitemapCache) Save(ctx context.Context, snap *persistence.SitemapSnapshot) error {
	if c.store == nil {
		return nil
	}
	return c.store.SaveSitemapSnapshot(ctx, snap)
}

func (c browserLearningStoreSitemapCache) Get(ctx context.Context, siteOrigin string, depth int) (*persistence.SitemapSnapshot, error) {
	if c.store == nil {
		return nil, nil
	}
	return c.store.GetSitemapSnapshot(ctx, siteOrigin, depth)
}

func (c browserLearningStoreSitemapCache) Purge(ctx context.Context, olderThan time.Time) (int64, error) {
	if c.store == nil {
		return 0, nil
	}
	return c.store.PurgeSitemapSnapshots(ctx, olderThan)
}

func newBrowserHumanEventSourceFactory() tool.HumanEventSourceFactory {
	return func(context.Context) (cdp.EventSource, error) {
		sess, ok := tool.CurrentSharedBrowserSession()
		if !ok || sess == nil {
			return nil, nil
		}
		return cdp.NewCDPEventSource(sess), nil
	}
}

func loadBrowserAnomalyTemplateLibrary(ctx context.Context, store persistence.LearningStore) error {
	lib := tool.NewAnomalyTemplateLibrary()
	if store == nil {
		tool.SetSharedAnomalyTemplateLibrary(lib)
		return nil
	}

	templates, err := store.ListAnomalyTemplates(ctx)
	if err != nil {
		return err
	}
	for _, tpl := range templates {
		if tpl == nil {
			continue
		}
		recovery, err := tool.DecodeRecoveryActions(tpl.RecoveryActions)
		if err != nil {
			return fmt.Errorf("decode anomaly template %d recovery: %w", tpl.ID, err)
		}
		lib.Upsert(&tool.AnomalyTemplate{
			ID: tpl.ID,
			Signature: tool.AnomalyTemplateSignature{
				Type:        tpl.SignatureType,
				Subtype:     tpl.SignatureSubtype,
				SitePattern: tpl.SignatureSite,
				Severity:    tpl.SignatureSeverity,
			},
			Recovery: recovery,
			Stats: tool.AnomalyTemplateStats{
				MatchCount:   tpl.MatchCount,
				SuccessCount: tpl.SuccessCount,
				FailureCount: tpl.FailureCount,
				UpdatedAt:    tpl.UpdatedAt,
			},
			CreatedAt: tpl.CreatedAt,
			UpdatedAt: tpl.UpdatedAt,
			Source:    "persisted",
		})
	}

	tool.SetSharedAnomalyTemplateLibrary(lib)
	return nil
}

func (r *browserRuntimeReloader) MaybeRefresh(ctx context.Context) error {
	if r == nil {
		return tool.RefreshSharedPatternLibraryIfChanged()
	}

	r.mu.Lock()
	now := time.Now()
	if now.Sub(r.lastCheckedAt) < time.Second {
		r.mu.Unlock()
		return nil
	}
	r.lastCheckedAt = now
	syncFile := r.syncFile
	lastVersion := r.lastVersion
	store := r.store
	r.mu.Unlock()

	if syncFile != "" {
		projection, err := kernel.ReadBrowserRuntimeProjectionFile(syncFile)
		switch {
		case err == nil && projection != nil && projection.Version != lastVersion:
			if err := applyBrowserRuntimeProjectionFromFile(syncFile); err != nil {
				return err
			}
			r.mu.Lock()
			if projection.Version != 0 {
				r.lastVersion = projection.Version
			}
			r.mu.Unlock()
		case err != nil && !os.IsNotExist(err):
			return err
		}
	}

	if store != nil {
		if err := loadBrowserAnomalyTemplateLibrary(ctx, store); err != nil {
			return err
		}
	}
	return tool.RefreshSharedPatternLibraryIfChanged()
}

func (r *browserRuntimeReloader) Start(ctx context.Context) {
	if r == nil || r.stopCh != nil {
		return
	}
	r.stopCh = make(chan struct{})
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-r.stopCh:
				return
			case <-ticker.C:
				if err := r.MaybeRefresh(context.Background()); err != nil {
					fmt.Fprintf(os.Stderr, "brain-browser: background runtime refresh: %v\n", err)
				}
			}
		}
	}()
}

func (r *browserRuntimeReloader) Stop() {
	if r == nil || r.stopCh == nil {
		return
	}
	close(r.stopCh)
	r.stopCh = nil
}

func applyBrowserRuntimeProjectionFromFile(path string) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	projection, err := kernel.ReadBrowserRuntimeProjectionFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if projection == nil {
		return nil
	}
	if projection.BrainDBPath != "" {
		_ = os.Setenv(envBrainDBPath, projection.BrainDBPath)
	}
	if projection.UIPatternDBPath != "" {
		_ = os.Setenv("BRAIN_UI_PATTERN_DB_PATH", projection.UIPatternDBPath)
	}
	tool.SetBrowserFeatureGate(&tool.BrowserFeatureGateConfig{
		Enabled:  projection.FeatureGateEnabled,
		Features: cloneProjectionFeatures(projection.Features),
	})
	return nil
}

func cloneProjectionFeatures(in map[string]bool) map[string]bool {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]bool, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func resolvedBrainDBPath() string {
	if path := strings.TrimSpace(os.Getenv(envBrainDBPath)); path != "" {
		return filepath.Clean(path)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.TempDir()
	}
	return filepath.Join(home, ".brain", "brain.db")
}

func configureBrowserRuntime(ctx context.Context) (*persistence.ClosableStores, *browserRuntimeReloader, error) {
	syncFile := strings.TrimSpace(os.Getenv(envBrowserRuntimeSyncFile))
	if syncFile == "" {
		syncFile = kernel.BrowserRuntimeProjectionForDataDir(filepath.Dir(resolvedBrainDBPath()), false, nil).SyncFile
	}
	if err := applyBrowserRuntimeProjectionFromFile(syncFile); err != nil {
		return nil, nil, err
	}
	tool.SetHumanEventSourceFactory(newBrowserHumanEventSourceFactory())
	tool.SetSitemapCache(nil)
	tool.SetHumanDemoSink(nil)
	tool.SetPatternFailureStore(nil)
	if err := loadBrowserAnomalyTemplateLibrary(ctx, nil); err != nil {
		return nil, nil, err
	}

	brainDBPath := resolvedBrainDBPath()
	stores, err := persistence.Open("sqlite", brainDBPath)
	if err != nil {
		return nil, nil, err
	}
	reloader := &browserRuntimeReloader{syncFile: syncFile}
	if projection, err := kernel.ReadBrowserRuntimeProjectionFile(syncFile); err == nil && projection != nil {
		reloader.lastVersion = projection.Version
	}
	if stores == nil || stores.LearningStore == nil {
		return stores, reloader, nil
	}

	tool.SetSitemapCache(browserLearningStoreSitemapCache{store: stores.LearningStore})
	tool.SetHumanDemoSink(stores.LearningStore)
	tool.SetPatternFailureStore(stores.LearningStore)
	if err := loadBrowserAnomalyTemplateLibrary(ctx, stores.LearningStore); err != nil {
		return stores, reloader, err
	}
	reloader.store = stores.LearningStore
	return stores, reloader, nil
}

func newBrowserHandler(reloader *browserRuntimeReloader) *browserHandler {
	var reg tool.Registry = tool.NewMemRegistry()
	browserTools := tool.NewBrowserTools()
	for _, t := range browserTools {
		reg.Register(t)
	}
	reg.Register(tool.NewNoteTool("browser"))

	if cfg, err := toolpolicy.Load(""); err != nil {
		fmt.Fprintf(os.Stderr, "brain-browser: load tool policy: %v\n", err)
	} else {
		reg = toolpolicy.FilterRegistry(reg, cfg, toolpolicy.ToolScopesForDelegate(string(agent.KindBrowser))...)
	}
	return &browserHandler{
		registry:     reg,
		browserTools: browserTools,
		learner:      kernel.NewDefaultBrainLearner(agent.KindBrowser),
		reloader:     reloader,
	}
}

func (h *browserHandler) Kind() agent.Kind { return agent.KindBrowser }
func (h *browserHandler) Version() string  { return brain.SDKVersion }
func (h *browserHandler) Tools() []string  { return sidecar.RegistryToolNames(h.registry) }
func (h *browserHandler) ToolSchemas() []tool.Schema {
	return sidecar.RegistryToolSchemas(h.registry)
}

// SetKernelCaller implements sidecar.RichBrainHandler.
func (h *browserHandler) SetKernelCaller(caller sidecar.KernelCaller) {
	h.caller = caller
}

func (h *browserHandler) HandleMethod(ctx context.Context, method string, params json.RawMessage) (interface{}, error) {
	if h.reloader != nil {
		if err := h.reloader.MaybeRefresh(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "brain-browser: runtime refresh: %v\n", err)
		}
	}
	switch method {
	case "tools/call":
		return h.handleToolsCall(ctx, params)
	case "brain/execute":
		return h.handleExecute(ctx, params)
	case "brain/metrics":
		return h.learner.ExportMetrics(), nil
	default:
		return nil, sidecar.ErrMethodNotFound
	}
}

// handleExecute runs the Browser Brain's Agent Loop for a delegated task.
func (h *browserHandler) handleExecute(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var req sidecar.ExecuteRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return &sidecar.ExecuteResult{
			Status: "failed",
			Error:  fmt.Sprintf("parse request: %v", err),
		}, nil
	}

	if h.caller == nil {
		return &sidecar.ExecuteResult{
			Status:  "completed",
			Summary: "browser brain ready (no LLM proxy available)",
			Turns:   0,
		}, nil
	}

	systemPrompt := `You are a specialist browser brain — a top-tier UI interaction specialist
that fully simulates human browser operations.

You control a real browser via Chrome DevTools Protocol. You can:

**Navigation:**
- browser.open: Open any URL (creates browser session automatically)
- browser.navigate: Go back/forward/refresh, switch/close tabs

**Clicking:**
- browser.click: Single click (by CSS selector or x,y coordinates)
- browser.double_click: Double click
- browser.right_click: Right click (context menu)

**Text Input:**
- browser.type: Type text into focused element (supports clear first)
- browser.press_key: Press keys/combos (Enter, Tab, Ctrl+A, Ctrl+C, etc.)

**Mouse Operations:**
- browser.scroll: Scroll page or element (up/down/left/right or precise pixels)
- browser.hover: Hover over element (triggers CSS :hover, tooltips, dropdowns)
- browser.drag: Drag and drop (from element A to B, with smooth movement)

**Forms:**
- browser.select: Choose dropdown option (by value, text, or index)
- browser.upload_file: Upload files to file input elements

**Visual Perception:**
- browser.screenshot: Capture screenshot (full page, viewport, or element)
  Returns base64-encoded image for analysis
- browser.eval: Execute JavaScript to query DOM, read state, interact with APIs

**Waiting:**
- browser.wait: Wait for element visible/hidden, page load, or JS condition

WORKFLOW:
1. Always start by opening a URL with browser.open
2. Take screenshots to understand the page layout
3. Use selectors when possible (more reliable than coordinates)
4. After interactions, take screenshots to verify results
5. Wait for elements/page load when needed

BE PRECISE: Click the right elements, type in the right fields, verify with screenshots.
When done, summarize what you observed and did.`

	maxTurns := 15
	if req.Budget != nil && req.Budget.MaxTurns > 0 {
		maxTurns = req.Budget.MaxTurns
	}

	registry, err := h.buildRegistry(req.Execution)
	if err != nil {
		return &sidecar.ExecuteResult{
			Status: "failed",
			Error:  err.Error(),
		}, nil
	}

	start := time.Now()
	result := sidecar.RunAgentLoopWithContext(ctx, h.caller, registry, systemPrompt, req.Instruction, maxTurns, req.Context)

	h.learner.RecordOutcome(ctx, kernel.TaskOutcome{
		TaskType:  "browser.execute",
		Success:   result.Status == "completed",
		Duration:  time.Since(start),
		ToolCalls: result.Turns,
	})

	// Clean up browser when task completes.
	tool.CloseBrowserSession(h.browserTools)

	return result, nil
}

func (h *browserHandler) handleToolsCall(ctx context.Context, params json.RawMessage) (interface{}, error) {
	return sidecar.DispatchToolCall(ctx, params, h.registry, h.buildRegistry)
}

func main() {
	listen := ""
	for i, arg := range os.Args[1:] {
		if arg == "--listen" && i+1 < len(os.Args[1:]) {
			listen = os.Args[i+2]
		}
	}

	verifyOpts, err := license.VerifyOptionsFromEnv(license.VerifyOptions{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "brain-browser: license config: %v\n", err)
		os.Exit(1)
	}
	res, err := license.CheckSidecar("brain-browser", verifyOpts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "brain-browser: license: %v\n", err)
		os.Exit(1)
	}
	tool.ConfigureBrowserFeatureGate(res)
	runtimeStores, runtimeReloader, err := configureBrowserRuntime(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "brain-browser: runtime wiring: %v\n", err)
	}
	if runtimeStores != nil {
		defer runtimeStores.Close()
	}
	if runtimeReloader != nil {
		runtimeReloader.Start(context.Background())
		defer runtimeReloader.Stop()
		if err := runtimeReloader.MaybeRefresh(context.Background()); err != nil {
			fmt.Fprintf(os.Stderr, "brain-browser: runtime refresh: %v\n", err)
		}
	}

	handler := newBrowserHandler(runtimeReloader)
	if listen != "" {
		err = sidecar.ListenAndServe(listen, handler)
	} else {
		err = sidecar.Run(handler)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "brain-browser: %v\n", err)
		os.Exit(1)
	}
}

// buildRegistry 在 ExecutionSpec 约束下构建一份 registry。
// 关键：复用 handler 的 h.browserTools（共享同一份 BrowserSession 持有），
// 不每次重建；否则每次 tools/call 都会创建新的 Chromium 进程并泄漏旧会话。
func (h *browserHandler) buildRegistry(spec *executionpolicy.ExecutionSpec) (tool.Registry, error) {
	if err := tool.RefreshSharedPatternLibraryIfChanged(); err != nil {
		fmt.Fprintf(os.Stderr, "brain-browser: refresh pattern library: %v\n", err)
	}
	bounds, err := toolguard.NewBoundaries(spec)
	if err != nil {
		return nil, err
	}

	var reg tool.Registry = tool.NewMemRegistry()
	for _, t := range h.browserTools {
		reg.Register(toolguard.WrapReadPolicy(tool.WrapSandbox(t, bounds.Sandbox), bounds.FilePolicy))
	}
	reg.Register(tool.NewNoteTool("browser"))

	if cfg, err := toolpolicy.Load(""); err != nil {
		fmt.Fprintf(os.Stderr, "brain-browser: load tool policy: %v\n", err)
	} else {
		reg = toolpolicy.FilterRegistry(reg, cfg, toolpolicy.ToolScopesForDelegate(string(agent.KindBrowser))...)
	}
	return reg, nil
}
