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

	tool.CloseBrowserSession(h.browserTools)
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
				return result
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
- browser.snapshot: {} — get page structure (accessibility tree, NOT screenshot)
- browser.type: {"selector": "...", "text": "..."} — type text
- browser.click: {"selector": "..."} — click element
- browser.press_key: {"key": "Enter"} — press a key
- browser.wait: {"condition": "network_idle"} — wait for page load
- browser.eval: {"expression": "..."} — run JavaScript to extract data

RULES:
- Use browser.snapshot (NOT screenshot) to read page content.
- End with browser.snapshot or browser.eval to capture the result.
- Keep the plan SHORT — typically 4-8 steps.
- The "url" field is for the initial page — do NOT include browser.open in steps if you set url.
- The "category" field helps the system learn and reuse this plan for similar tasks.
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
			openArgs, _ := json.Marshal(map[string]string{"url": plan.URL})
			if _, err := openTool.Execute(ctx, openArgs); err != nil {
				return &sidecar.ExecuteResult{Status: "failed", Error: fmt.Sprintf("open %s: %v", plan.URL, err)}
			}
		}
	}

	var lastOutput json.RawMessage
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
			if result.IsError {
				return &sidecar.ExecuteResult{
					Status: "failed",
					Error:  fmt.Sprintf("step %d (%s): %s", i+1, step.Tool, string(result.Output)),
					Turns:  0,
				}
			}
		}
	}

	summary := string(lastOutput)
	if len(summary) > 4096 {
		summary = summary[:4096] + "...[truncated]"
	}

	// Learn: persist the successful plan as a new pattern.
	h.learnFromPlan(ctx, plan, instruction)

	return &sidecar.ExecuteResult{
		Status:  "completed",
		Summary: summary,
		Turns:   1,
	}
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
	if plan.URL != "" {
		pat.AppliesWhen = tool.MatchCondition{
			URLPattern: extractDomainPattern(plan.URL),
		}
	}

	if err := lib.Upsert(ctx, pat); err != nil {
		diaglog.Logf("browser", "failed to save learned pattern: %v", err)
	} else {
		diaglog.Logf("browser", "learned pattern %s (category=%s) from: %s", pat.ID, category, instruction)
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
