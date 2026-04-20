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
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"time"

	brain "github.com/leef-l/brain"
	"github.com/leef-l/brain/sdk/agent"
	"github.com/leef-l/brain/sdk/diaglog"
	"github.com/leef-l/brain/sdk/executionpolicy"
	"github.com/leef-l/brain/sdk/kernel"
	"github.com/leef-l/brain/sdk/license"
	"github.com/leef-l/brain/sdk/llm"
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

	// 启动时扫描一次 approved 但还没转 ui_patterns 的 human demo,
	// 自动转化成可重放 pattern。best-effort:失败只打日志不拦启动。
	if n, err := backfillApprovedDemosToPatterns(ctx, stores.LearningStore); err == nil && n > 0 {
		diaglog.Logf("browser", "backfill: converted %d approved human demo(s) into ui_patterns", n)
	}

	return stores, reloader, nil
}

// backfillApprovedDemosToPatterns 把已 approved 但 pattern 库里还没有
// 对应条目的 human demo 序列补转成 UIPattern。幂等:pattern.ID 用确定
// 性规则(human_demo_<run_id>_<recorded_ts>),重复 upsert 不会造成多份。
func backfillApprovedDemosToPatterns(ctx context.Context, store persistence.LearningStore) (int, error) {
	if store == nil {
		return 0, nil
	}
	lib := tool.SharedPatternLibrary()
	if lib == nil {
		return 0, nil
	}
	demos, err := store.ListHumanDemoSequences(ctx, true)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, seq := range demos {
		if seq == nil || len(seq.Actions) == 0 {
			continue
		}
		var actions []tool.RecordedAction
		if err := json.Unmarshal(seq.Actions, &actions); err != nil {
			continue
		}
		p := tool.ConvertDemoToPattern(seq, actions)
		if p == nil {
			continue
		}
		// ConvertDemoToPattern 已经用 seq.RecordedAt 作为 id 来源,
		// backfill 多次扫到同一条会 upsert 到同一 pattern,幂等。
		if err := lib.Upsert(ctx, p); err == nil {
			n++
		}
	}
	return n, nil
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
	// 把 sidecar 里 human.request_takeover 的本地 coord 替换成反向 RPC 桥,
	// 求助信号会一路透传到 kernel 进程的协调器(serve HTTP / chat slash)。
	tool.SetHumanTakeoverCoordinator(sidecar.NewHumanTakeoverBridge(caller))
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

// handleExecute runs browser tasks using the perception-first architecture:
//  1. Try pattern_match → pattern_exec (0 LLM calls)
//  2. Fallback: 1 LLM call to plan an action sequence → execute it
func (h *browserHandler) handleExecute(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var req sidecar.ExecuteRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return &sidecar.ExecuteResult{
			Status: "failed",
			Error:  fmt.Sprintf("parse request: %v", err),
		}, nil
	}

	// 用户意图探测:指令里包含"看到/给我看/可见浏览器"这类表达时,
	// 切换到有头模式让用户观察操作过程。
	// 已存在的 headless session 会被关闭,下次 get() 会用新的 env 重启
	// 一个有头的 Chrome 窗口。
	if wantsVisibleBrowser(req.Instruction) {
		diaglog.Logf("browser", "detected visible-browser intent, switching to headed mode")
		os.Setenv("BROWSER_HEADED", "1")
		tool.CloseBrowserSession(h.browserTools)
	}

	registry, err := h.buildRegistry(req.Execution)
	if err != nil {
		return &sidecar.ExecuteResult{
			Status: "failed",
			Error:  err.Error(),
		}, nil
	}

	start := time.Now()
	result := h.executeWithPerception(ctx, &req, registry)

	h.learner.RecordOutcome(ctx, kernel.TaskOutcome{
		TaskType:  "browser.execute",
		Success:   result.Status == "completed",
		Duration:  time.Since(start),
		ToolCalls: result.Turns,
	})

	return result, nil
}

// executeWithPerception implements the two-tier execution strategy.
func (h *browserHandler) executeWithPerception(ctx context.Context, req *sidecar.ExecuteRequest, registry tool.Registry) *sidecar.ExecuteResult {
	diaglog.Logf("browser", "executeWithPerception: instruction=%s caller=%v", req.Instruction, h.caller != nil)

	// Step 1: Try pattern_match on current page (if already navigated).
	matchTool, hasMatch := registry.Lookup("browser.pattern_match")
	if hasMatch {
		matchArgs, _ := json.Marshal(map[string]interface{}{"limit": 3})
		matchResult, err := matchTool.Execute(ctx, matchArgs)
		outputLen := 0
		if matchResult != nil {
			outputLen = len(matchResult.Output)
		}
		diaglog.Logf("browser", "pattern_match: err=%v isError=%v output_len=%d", err, matchResult != nil && matchResult.IsError, outputLen)
		if err == nil && matchResult != nil && !matchResult.IsError {
			patternID := extractTopPatternID(matchResult.Output)
			diaglog.Logf("browser", "pattern_match: patternID=%q", patternID)
			if patternID != "" {
				result := h.executePattern(ctx, registry, patternID, req)
				diaglog.Logf("browser", "pattern_exec result: status=%s error=%s", result.Status, result.Error)
				if result.Status == "completed" {
					return result
				}
				h.deleteLearnedPattern(ctx, patternID)
			}
		}
	}

	// Step 2: No pattern matched — ask LLM once to plan.
	if h.caller == nil {
		return &sidecar.ExecuteResult{
			Status: "failed",
			Error:  "no pattern matched and no LLM proxy available",
		}
	}

	// Get page snapshot for LLM context (if a page is already open).
	var pageSnapshot string
	if snapshotTool, ok := registry.Lookup("browser.snapshot"); ok {
		snapArgs, _ := json.Marshal(map[string]interface{}{"mode": "accessibility"})
		snapResult, err := snapshotTool.Execute(ctx, snapArgs)
		if err == nil && snapResult != nil {
			pageSnapshot = string(snapResult.Output)
		}
	}

	diaglog.Logf("browser", "no pattern matched, calling LLM planner, snapshot_len=%d", len(pageSnapshot))
	plan, err := h.planWithLLM(ctx, req.Instruction, pageSnapshot, registry)
	if err == nil {
		diaglog.Logf("browser", "LLM plan ok: url=%s steps=%d", plan.URL, len(plan.Steps))
		result := h.executeLLMPlan(ctx, registry, plan, req.Instruction)
		if result.Status == "completed" {
			return result
		}
		diaglog.Logf("browser", "LLM plan execution failed: %s, falling back to agent loop", result.Error)
	} else {
		diaglog.Logf("browser", "LLM planning failed: %v, falling back to agent loop", err)
	}

	// Step 3 (fallback): Multi-turn agent loop as last resort.
	return h.fallbackAgentLoop(ctx, req, registry)
}

// executePattern runs a matched pattern via pattern_exec (0 LLM calls).
func (h *browserHandler) executePattern(ctx context.Context, registry tool.Registry, patternID string, req *sidecar.ExecuteRequest) *sidecar.ExecuteResult {
	execTool, ok := registry.Lookup("browser.pattern_exec")
	if !ok {
		return &sidecar.ExecuteResult{Status: "failed", Error: "pattern_exec tool not available"}
	}
	vars := extractVariables(req.Instruction)
	execArgs, _ := json.Marshal(map[string]interface{}{
		"pattern_id": patternID,
		"variables":  vars,
	})
	result, err := execTool.Execute(ctx, execArgs)
	if err != nil {
		return &sidecar.ExecuteResult{Status: "failed", Error: fmt.Sprintf("pattern_exec: %v", err)}
	}

	// Read final page state for summary.
	summary := string(result.Output)
	snapshotTool, ok := registry.Lookup("browser.snapshot")
	if ok {
		snapArgs, _ := json.Marshal(map[string]interface{}{"mode": "text"})
		snapResult, err := snapshotTool.Execute(ctx, snapArgs)
		if err == nil && snapResult != nil {
			summary = string(snapResult.Output)
		}
	}

	status := "completed"
	if result.IsError {
		status = "failed"
	}
	return &sidecar.ExecuteResult{
		Status:  status,
		Summary: summary,
		Turns:   0,
	}
}

// plannedStep is a single action in the LLM-generated plan.
type plannedStep struct {
	Tool   string                 `json:"tool"`
	Params map[string]interface{} `json:"params"`
}

// llmPlan is the full plan output from a single LLM call.
type llmPlan struct {
	URL      string       `json:"url"`
	Category string       `json:"category"`
	Steps    []plannedStep `json:"steps"`
}

// planWithLLM calls LLM once to generate a full execution plan.
func (h *browserHandler) planWithLLM(ctx context.Context, instruction, pageSnapshot string, registry tool.Registry) (*llmPlan, error) {
	toolList := sidecar.RegistryToolNames(registry)

	systemPrompt := `You are a browser operation planner. Given a user instruction and the current page state, output a JSON plan.

Output format (JSON object, NOT array):
{
  "url": "<target URL to open, or empty if already on the right page>",
  "category": "<what kind of task: search/auth/commerce/form/nav/read/general>",
  "steps": [
    {"tool": "<tool_name>", "params": {<tool_params>}},
    ...
  ]
}

Available tools: ` + fmt.Sprintf("%v", toolList) + `

Key tools:
- browser.open: {"url": "..."} — open a URL
- browser.snapshot: {"mode": "interactive|text|html"} — read page.
    mode="interactive" (default): list of clickable/typable elements (for UI operations).
    mode="text": page body innerText. USE THIS to extract article / search result / product text content.
    mode="html": full outerHTML. Use when you need structured attributes (e.g. price tags, hidden data).
- browser.type: {"selector": "...", "text": "..."} — type text
- browser.click: {"selector": "..."} — click element
- browser.press_key: {"key": "Enter"} — press a key
- browser.wait: {"condition": "load"} — wait for page load (valid: visible, hidden, load, idle, js). Prefer "load" over "idle" as idle may timeout on pages with continuous network activity.
- browser.eval: {"expression": "..."} — run JavaScript to extract data
- browser.screenshot: {"full_page": true} — save a PNG to ~/.brain/screenshots/. Only use when the user explicitly asks for a visual/screenshot; do NOT use screenshot to read text content.
- browser.drag: {"from_selector":"...","to_selector":"..."} or {"from_x":..,"from_y":..,"to_x":..,"to_y":..} — press and hold then drag. USE THIS for slider CAPTCHA (滑块验证), slider captchas, drag-and-drop puzzles, range sliders. Human-like trajectory (easeInOut + jitter) is enabled by default.
- browser.hover: {"selector": "..."} — hover an element (triggers tooltips, dropdown menus).
- browser.scroll: {"direction":"down","amount":500} — scroll the page.
- human.request_takeover: {"reason":"captcha|slider_failed|session_expired|payment|other", "guidance":"text for the human"} — HAND OFF to a human operator when automation can't proceed. Agent pauses until the human resumes or aborts via WebUI/CLI. USE THIS as the final step when: (a) a slider/image/click CAPTCHA keeps failing after browser.drag, (b) you hit phone SMS / payment / 2FA, (c) you tried 3+ distinct strategies with no progress. The human's actions are recorded and become a learned pattern for future runs.

RULES:
- To read page content (search results, article text, prices, numbers), use browser.snapshot with mode="text".
- To find clickable targets or form fields, use browser.snapshot with mode="interactive".
- The LAST step MUST be browser.snapshot (mode=text for reading content), browser.eval, or browser.screenshot (only when the user explicitly asks for a screenshot).
- Keep the plan SHORT — typically 3-6 steps.
- The "url" field is for the initial page — do NOT include browser.open in steps if you set url.
- Do NOT add browser.wait after setting the "url" field — the URL open already waits for page load.
- The "category" field helps the system learn and reuse this plan for similar tasks.
- For search: type the query into the search box, then use browser.press_key with key "Enter" to submit (more reliable than clicking the search button). After pressing Enter, add browser.wait with condition "load" to wait for results.
- After any click or key press that triggers navigation, add browser.wait with condition "load" before taking a snapshot.
- For LOGIN tasks: ALWAYS include browser.type steps for every credential the user provided. Typical plan for "账号/密码/登录":
    1) browser.open (url)
    2) browser.snapshot (mode=interactive to locate input fields)
    3) browser.type (selector of username field, text="<user-provided username>")
    4) browser.type (selector of password field, text="<user-provided password>")
    5) browser.click (selector of login button) OR browser.press_key (Enter)
    6) browser.wait (condition=load)
    7) browser.snapshot (mode=text)
  NEVER skip the type steps — the user EXPECTS login to be attempted. Pass the exact string the user gave you; never replace it with placeholders like $username, ${password}, <admin>, etc.
- For slider CAPTCHA (滑块验证 / 拖动滑块 / slide-to-verify): FIRST take a snapshot with mode="interactive" to locate the slider handle and the slider track's end coordinates. THEN use browser.drag with from_selector/to_selector (or coordinates). Typical selectors: ".slider-button", ".captcha-slider", ".nc_iconfont", ".verify-move-block". DO NOT use browser.click on the slider — a click is not a drag and the captcha will not pass.
- If the captcha page is still present after the drag attempt, call human.request_takeover with reason="slider_failed" and guidance="请手动完成滑块验证，完成后点击 resume". DO NOT keep retrying drag blindly.
- Output ONLY the JSON object, no explanation.`

	userMsg := "Instruction: " + instruction
	if pageSnapshot != "" {
		userMsg += "\n\nCurrent page state:\n" + pageSnapshot
	}

	provider := sidecar.NewKernelLLMProvider(h.caller, "browser-planner")
	resp, err := provider.Complete(ctx, &llm.ChatRequest{
		System: []llm.SystemBlock{{Text: systemPrompt}},
		Messages: []llm.Message{{
			Role: "user",
			Content: []llm.ContentBlock{{
				Type: "text",
				Text: userMsg,
			}},
		}},
		MaxTokens: 2048,
	})
	if err != nil {
		return nil, err
	}

	// Extract text from response.
	for _, block := range resp.Content {
		if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
			return parseLLMPlanText(block.Text)
		}
	}
	return nil, fmt.Errorf("LLM returned no text content")
}

// parseLLMPlanText extracts the plan from LLM text output.
func parseLLMPlanText(text string) (*llmPlan, error) {
	text = strings.TrimSpace(text)
	text = strings.TrimPrefix(text, "```json")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(text, "```")
	text = strings.TrimSpace(text)

	var plan llmPlan
	if err := json.Unmarshal([]byte(text), &plan); err != nil {
		return nil, fmt.Errorf("parse plan JSON: %w", err)
	}
	if len(plan.Steps) == 0 {
		return nil, fmt.Errorf("plan has no steps")
	}
	return &plan, nil
}

// executeLLMPlan opens the URL (if specified) and runs the planned steps.
// On success, the plan is persisted as a learned pattern for future reuse.
func (h *browserHandler) executeLLMPlan(ctx context.Context, registry tool.Registry, plan *llmPlan, instruction string) *sidecar.ExecuteResult {
	diaglog.Logf("browser", "LLM plan: url=%s category=%s steps=%d", plan.URL, plan.Category, len(plan.Steps))

	// Open URL if LLM specified one.
	if plan.URL != "" {
		openTool, ok := registry.Lookup("browser.open")
		if ok {
			diaglog.Logf("browser", "executeLLMPlan: opening url=%s", plan.URL)
			openArgs, _ := json.Marshal(map[string]string{"url": plan.URL})
			result, err := openTool.Execute(ctx, openArgs)
			if err != nil {
				diaglog.Logf("browser", "executeLLMPlan: open failed: %v", err)
				return &sidecar.ExecuteResult{Status: "failed", Error: fmt.Sprintf("open %s: %v", plan.URL, err)}
			}
			diaglog.Logf("browser", "executeLLMPlan: open result isError=%v", result.IsError)
			if result.IsError {
				return &sidecar.ExecuteResult{Status: "failed", Error: fmt.Sprintf("open %s: %s", plan.URL, string(result.Output))}
			}
		} else {
			diaglog.Logf("browser", "executeLLMPlan: browser.open tool not found in registry")
		}
	} else {
		diaglog.Logf("browser", "executeLLMPlan: no URL in plan")
	}

	var lastOutput json.RawMessage
	var screenshotPaths []string
	for i, step := range plan.Steps {
		diaglog.Logf("browser", "plan step %d/%d: %s params=%v", i+1, len(plan.Steps), step.Tool, step.Params)
		t, ok := registry.Lookup(step.Tool)
		if !ok {
			return &sidecar.ExecuteResult{
				Status: "failed",
				Error:  fmt.Sprintf("step %d: tool %s not found", i+1, step.Tool),
				Turns:  0,
			}
		}
		args, _ := json.Marshal(step.Params)
		result, err := t.Execute(ctx, args)
		if err != nil {
			return &sidecar.ExecuteResult{
				Status: "failed",
				Error:  fmt.Sprintf("step %d (%s): %v", i+1, step.Tool, err),
				Turns:  0,
			}
		}
		if result != nil {
			lastOutput = result.Output
			diaglog.Logf("browser", "plan step %d/%d result: isError=%v output=%.200s", i+1, len(plan.Steps), result.IsError, string(result.Output))
			if result.IsError {
				return &sidecar.ExecuteResult{
					Status: "failed",
					Error:  fmt.Sprintf("step %d (%s): %s", i+1, step.Tool, string(result.Output)),
					Turns:  0,
				}
			}
			// 截图步骤实时持久化,避免被后续步骤的 output 覆盖丢失。
			if step.Tool == "browser.screenshot" {
				if path, ok := saveScreenshotOutput(result.Output); ok {
					screenshotPaths = append(screenshotPaths, path)
					diaglog.Logf("browser", "screenshot saved: %s", path)
				}
			}
		}
	}

	summary := string(lastOutput)

	lastTool := ""
	if n := len(plan.Steps); n > 0 {
		lastTool = plan.Steps[n-1].Tool
	}

	// 只在 plan 里有可能触发页面变化的交互(click/press_key/drag/type/
	// submit)时才等待动态内容渲染,缩到 500ms。纯读页面(open+snapshot
	// 这种)不用 sleep,直接出结果。
	if planHasNavigationStep(plan) {
		time.Sleep(500 * time.Millisecond)
	}

	// fast path:最后一步已经是 browser.snapshot mode=text,直接复用
	// lastOutput 不再多调一次工具。
	if lastTool == "browser.snapshot" {
		var lastParsed struct {
			Mode    string `json:"mode"`
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal(lastOutput, &lastParsed); err == nil &&
			lastParsed.Mode == "text" && lastParsed.Content != "" {
			summary = fmt.Sprintf("Title: %s\nURL: %s\n\n%s",
				lastParsed.Title, lastParsed.URL, lastParsed.Content)
			goto summaryDone
		}
	}

	// 根据 plan 的最后一步类型决定 summary 策略:
	//   screenshot / eval:保留原始输出(截图 base64 或 JS 返回值)
	//   其他:追加 text-mode snapshot,让 central 拿到人类可读的页面文本
	if lastTool != "browser.eval" && lastTool != "browser.screenshot" {
		// 非 eval / screenshot 场景:追加 text-mode snapshot,让 central
		// 拿到人类可读的页面文本而不是原始 JSON。
		if snapshotTool, ok := registry.Lookup("browser.snapshot"); ok {
			snapArgs, _ := json.Marshal(map[string]interface{}{"mode": "text", "max_chars": 8000})
			snapResult, snapErr := snapshotTool.Execute(ctx, snapArgs)
			if snapErr == nil && snapResult != nil && len(snapResult.Output) > 0 {
				var parsed struct {
					Title   string `json:"title"`
					URL     string `json:"url"`
					Content string `json:"content"`
				}
				if err := json.Unmarshal(snapResult.Output, &parsed); err == nil && parsed.Content != "" {
					summary = fmt.Sprintf("Title: %s\nURL: %s\n\n%s",
						parsed.Title, parsed.URL, parsed.Content)
				} else {
					summary = string(snapResult.Output)
				}
			}
		}
	} else if lastTool == "browser.screenshot" {
		// 最后一步是截图:用已保存的文件路径 + 页面元信息替代 base64。
		pageURL, pageTitle := "", ""
		if snapshotTool, ok := registry.Lookup("browser.snapshot"); ok {
			sa, _ := json.Marshal(map[string]interface{}{"mode": "text", "max_chars": 200})
			if sr, err := snapshotTool.Execute(ctx, sa); err == nil && sr != nil {
				var meta struct {
					Title string `json:"title"`
					URL   string `json:"url"`
				}
				json.Unmarshal(sr.Output, &meta)
				pageURL, pageTitle = meta.URL, meta.Title
			}
		}
		if len(screenshotPaths) > 0 {
			summary = fmt.Sprintf(
				"Screenshot saved to: %s\nPage URL: %s\nPage Title: %s",
				screenshotPaths[len(screenshotPaths)-1], pageURL, pageTitle)
		} else {
			summary = fmt.Sprintf("Screenshot step ran but no file was saved.\nPage URL: %s\nPage Title: %s", pageURL, pageTitle)
		}
	}

summaryDone:
	// 如果 plan 中间步骤截图了(但最后一步不是 screenshot),在 summary 末尾
	// 附加截图路径提示,避免截图被"悄悄丢失"。
	if lastTool != "browser.screenshot" && len(screenshotPaths) > 0 {
		summary += "\n\n[Screenshots saved: " + strings.Join(screenshotPaths, ", ") + "]"
	}
	// 兜底:如果 plan 里做过拖动/点击登录等交互,但页面仍停留在验证码
	// 特征页上(URL 含 captcha/verify,或 title 含"安全验证"/"验证码"),
	// 自动调 human.request_takeover 挂起等人类处理,避免 AI 反复失败
	// 消耗上下文。已经请求过接管的调用走正常返回路径,由 coordinator 决定
	// resume/abort。
	if shouldRequestTakeover(plan, summary) && !stepHasTool(plan, "human.request_takeover") {
		if takeoverTool, ok := registry.Lookup("human.request_takeover"); ok {
			// 附一张截图给人类看。
			var shotB64 string
			if screenshotTool, ok := registry.Lookup("browser.screenshot"); ok {
				sa, _ := json.Marshal(map[string]interface{}{})
				if sr, err := screenshotTool.Execute(ctx, sa); err == nil && sr != nil {
					var s struct {
						Data string `json:"data"`
					}
					json.Unmarshal(sr.Output, &s)
					shotB64 = s.Data
				}
			}
			pageURL, _ := extractPageMetaFromSummary(summary)
			tReq, _ := json.Marshal(map[string]interface{}{
				"reason":     detectTakeoverReason(summary),
				"guidance":   "自动化无法通过当前页面(验证码/登录),请手动完成后点击 Resume 继续。",
				"url":        pageURL,
				"screenshot": shotB64,
			})
			tRes, tErr := takeoverTool.Execute(ctx, tReq)
			diaglog.Logf("browser", "auto human takeover: err=%v output=%.200s", tErr, func() string {
				if tRes != nil {
					return string(tRes.Output)
				}
				return ""
			}())
			if tRes != nil && !tRes.IsError {
				summary += "\n\n[Human takeover: " + string(tRes.Output) + "]"
			}
		}
	}

	diaglog.Logf("browser", "executeLLMPlan: lastTool=%s summary len=%d preview=%.200s", lastTool, len(summary), summary)
	if len(summary) > 8192 {
		summary = summary[:8192] + "...[truncated]"
	}

	// Learn: persist the successful plan as a new pattern.
	h.learnFromPlan(ctx, plan, instruction)

	return &sidecar.ExecuteResult{
		Status:  "completed",
		Summary: summary,
		Turns:   1,
	}
}

// planHasNavigationStep 判断 plan 里是否可能触发页面变化,用来决定
// 收尾处要不要等 500ms 让动态内容渲染完再做 snapshot。
// 纯读页面(open 后直接 snapshot)不用等,节省时间。
func planHasNavigationStep(plan *llmPlan) bool {
	for _, s := range plan.Steps {
		switch s.Tool {
		case "browser.click", "browser.press_key", "browser.drag",
			"browser.type", "browser.submit":
			return true
		}
	}
	return false
}

// shouldRequestTakeover 判断本次 plan 执行后是否应自动请求人类接管。
// 典型场景:plan 里调过 drag / click / press_key(说明尝试了交互),
// 但页面最终停在验证码/安全验证页上。
func shouldRequestTakeover(plan *llmPlan, summary string) bool {
	interacted := false
	for _, s := range plan.Steps {
		switch s.Tool {
		case "browser.drag", "browser.click", "browser.press_key", "browser.type":
			interacted = true
		}
	}
	if !interacted {
		return false
	}
	low := strings.ToLower(summary)
	signals := []string{
		"captcha", "wappass.baidu", "/verify", "/sorry/", "recaptcha",
		"安全验证", "验证码", "滑块验证", "人机验证", "拖动滑块", "行为验证",
	}
	for _, sig := range signals {
		if strings.Contains(low, strings.ToLower(sig)) {
			return true
		}
	}
	return false
}

// detectTakeoverReason 把页面特征粗分为 reason 枚举,供 coordinator/日志用。
func detectTakeoverReason(summary string) string {
	low := strings.ToLower(summary)
	switch {
	case strings.Contains(low, "滑块") || strings.Contains(low, "slider"):
		return "slider_failed"
	case strings.Contains(low, "captcha") || strings.Contains(low, "recaptcha") || strings.Contains(low, "验证码"):
		return "captcha"
	case strings.Contains(low, "登录") || strings.Contains(low, "login") || strings.Contains(low, "session"):
		return "session_expired"
	default:
		return "other"
	}
}

// stepHasTool 判断 plan 是否已经显式带了某个工具(避免重复调用)。
func stepHasTool(plan *llmPlan, tool string) bool {
	for _, s := range plan.Steps {
		if s.Tool == tool {
			return true
		}
	}
	return false
}

// extractPageMetaFromSummary 从我们组装的 "Title: x\nURL: y\n\n..." 里
// 抽出 URL,供 takeover 请求用。
func extractPageMetaFromSummary(summary string) (url, title string) {
	for _, line := range strings.SplitN(summary, "\n", 3) {
		if strings.HasPrefix(line, "URL: ") {
			url = strings.TrimPrefix(line, "URL: ")
		}
		if strings.HasPrefix(line, "Page URL: ") {
			url = strings.TrimPrefix(line, "Page URL: ")
		}
		if strings.HasPrefix(line, "Title: ") {
			title = strings.TrimPrefix(line, "Title: ")
		}
		if strings.HasPrefix(line, "Page Title: ") {
			title = strings.TrimPrefix(line, "Page Title: ")
		}
	}
	return
}

// wantsVisibleBrowser 粗略匹配用户"想看到浏览器操作"的意图。
// 命中的场景:
//   - 中文:"我要看到"/"给我看"/"让我看"/"打开浏览器"/"可见"/"看得到"
//   - 英文:"visible"/"watch"/"show me"/"headed"/"not headless"
// 这些词出现在 instruction 里就切到有头模式。宁可多开窗口,也不要
// 让用户看不到自己明确要求的操作过程。
func wantsVisibleBrowser(instruction string) bool {
	s := strings.ToLower(instruction)
	needles := []string{
		"我要看到", "给我看", "让我看", "我要能看到", "可见浏览器",
		"可视化", "看得到", "看到操作", "看到你的操作", "打开浏览器",
		"visible browser", "not headless", "non-headless", "headed",
		"show me the browser", "watch the browser", "show browser",
	}
	for _, n := range needles {
		if strings.Contains(s, strings.ToLower(n)) {
			return true
		}
	}
	return false
}

// saveScreenshotOutput decodes a browser.screenshot tool result and persists
// the PNG/JPEG bytes to ~/.brain/screenshots/. Returns the file path on success.
func saveScreenshotOutput(output json.RawMessage) (string, bool) {
	var shot struct {
		Format string `json:"format"`
		Data   string `json:"data"`
	}
	if err := json.Unmarshal(output, &shot); err != nil || shot.Data == "" {
		return "", false
	}
	ext := shot.Format
	if ext == "" {
		ext = "png"
	}
	raw, decErr := base64.StdEncoding.DecodeString(shot.Data)
	if decErr != nil {
		return "", false
	}
	dir := filepath.Join(os.Getenv("HOME"), ".brain", "screenshots")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", false
	}
	path := filepath.Join(dir, fmt.Sprintf("screenshot-%d.%s", time.Now().UnixNano(), ext))
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		return "", false
	}
	return path, true
}

// learnFromPlan converts a successful LLM plan into a UIPattern and saves it.
// The LLM provides category and URL — no hardcoded keyword lists.
func (h *browserHandler) learnFromPlan(ctx context.Context, plan *llmPlan, instruction string) {
	lib := tool.SharedPatternLibrary()
	if lib == nil {
		return
	}

	actionSeq := make([]tool.ActionStep, 0, len(plan.Steps))
	for _, s := range plan.Steps {
		actionSeq = append(actionSeq, tool.ActionStep{
			Tool:   s.Tool,
			Params: s.Params,
		})
	}

	category := plan.Category
	if category == "" {
		category = "general"
	}

	pat := &tool.UIPattern{
		ID:             fmt.Sprintf("learned_%d", time.Now().UnixMilli()),
		Category:       category,
		Description:    instruction,
		ActionSequence: actionSeq,
		Source:         "learned",
		Enabled:        true,
		Pending:        true,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}

	// Use the URL as matching condition so similar URLs hit this pattern next time.
	if plan.URL == "" {
		diaglog.Logf("browser", "skip saving learned pattern: no URL in plan")
		return
	}
	pat.AppliesWhen = tool.MatchCondition{
		URLPattern: extractDomainPattern(plan.URL),
	}

	if err := lib.Upsert(ctx, pat); err != nil {
		diaglog.Logf("browser", "failed to save learned pattern: %v", err)
	} else {
		diaglog.Logf("browser", "learned pattern %s (category=%s) from: %s", pat.ID, category, instruction)
	}
}

func (h *browserHandler) deleteLearnedPattern(ctx context.Context, patternID string) {
	lib := tool.SharedPatternLibrary()
	if lib == nil {
		return
	}
	p := lib.Get(patternID)
	if p == nil || p.Source != "learned" {
		return
	}
	if err := lib.Delete(ctx, patternID); err != nil {
		diaglog.Logf("browser", "failed to delete learned pattern %s: %v", patternID, err)
	} else {
		diaglog.Logf("browser", "deleted failed learned pattern %s", patternID)
	}
}

// extractDomainPattern turns "https://www.baidu.com/foo" into a regex
// matching that domain, so the learned pattern applies to all pages on
// the same site.
func extractDomainPattern(rawURL string) string {
	if idx := strings.Index(rawURL, "://"); idx >= 0 {
		rest := rawURL[idx+3:]
		if slash := strings.Index(rest, "/"); slash >= 0 {
			rest = rest[:slash]
		}
		return `(?i)^https?://` + regexp.QuoteMeta(rest)
	}
	return ""
}

// fallbackAgentLoop is the last-resort strategy: multi-turn LLM agent loop.
// Only used when both pattern_exec and single-shot LLM planning fail.
func (h *browserHandler) fallbackAgentLoop(ctx context.Context, req *sidecar.ExecuteRequest, registry tool.Registry) *sidecar.ExecuteResult {
	if h.caller == nil {
		return &sidecar.ExecuteResult{Status: "failed", Error: "no LLM proxy available"}
	}
	systemPrompt := `You are a browser specialist. You control a real browser.
Use browser.snapshot (NOT screenshot) to perceive pages — it returns structured text.
Use browser.open, browser.click, browser.type, browser.press_key, browser.wait, browser.eval.
Be efficient: get the answer and stop. Do not take unnecessary actions.`

	maxTurns := 15
	if req.Budget != nil && req.Budget.MaxTurns > 0 {
		maxTurns = req.Budget.MaxTurns
	}
	return sidecar.RunAgentLoopWithContext(ctx, h.caller, registry, systemPrompt, req.Instruction, maxTurns, req.Context)
}

// extractTopPatternID gets the best pattern_id from pattern_match result.
func extractTopPatternID(output json.RawMessage) string {
	var result struct {
		Matches []struct {
			PatternID string `json:"pattern_id"`
		} `json:"matches"`
	}
	if json.Unmarshal(output, &result) == nil && len(result.Matches) > 0 {
		return result.Matches[0].PatternID
	}
	return ""
}

// extractVariables pulls query/search terms from the instruction for pattern variables.
func extractVariables(instruction string) map[string]interface{} {
	vars := map[string]interface{}{}
	searchKeywords := []string{"搜索", "查询", "查找", "搜", "找"}
	for _, kw := range searchKeywords {
		if idx := strings.Index(instruction, kw); idx >= 0 {
			query := strings.TrimSpace(instruction[idx+len(kw):])
			query = strings.TrimRight(query, "，。！？,.!?")
			if query != "" {
				vars["query"] = query
			}
			break
		}
	}
	return vars
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
