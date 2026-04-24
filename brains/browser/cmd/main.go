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
	"net/url"
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
	"github.com/leef-l/brain/sdk/protocol"
	"github.com/leef-l/brain/sdk/sidecar"
	"github.com/leef-l/brain/sdk/tool"
	"github.com/leef-l/brain/sdk/toolpolicy"
	"github.com/leef-l/brain/sdk/tool/cdp"
	"github.com/leef-l/brain/sdk/toolguard"
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

var runBrowserAgentLoop = sidecar.RunAgentLoopFull

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
	if lib := tool.SharedPatternLibrary(); lib != nil {
		return lib.Reload(ctx)
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
	ensureCriticalBrowserTools(reg, browserTools)
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
	// 初始化 sidecar→host 的进度通知通道,后续 tool call / turn 通过
	// sidecar.EmitProgress 推到 kernel,再由 chat REPL 流式打印。
	sidecar.SetProgressContext(caller, string(agent.KindBrowser))
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

	// 用户意图探测优先使用结构化旁路字段，其次才回退到改写后的 instruction。
	// 这样 central 重写 delegation instruction 时，不会丢掉用户“我要看到”的原始诉求。
	if wantsHeadedBrowser(req.Subtask, req.Instruction) {
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

	// 启动整 run 的 DOM 事件录制。覆盖 AI 自己做的 type/click + 人类在
	// takeover 期间做的 drag/click。run 结束时按"最终是否真的成功"决定
	// 是否落盘:成功才写 pattern,失败轨迹丢弃。这就是用户要的"整个流程
	// 成功才学习"语义。
	//
	// 只对 sensitive / slider 类任务开录制(登录/支付/有滑块),避免所有
	// 普通浏览场景都录制增加开销。
	var fullRunRec *tool.FullRunRecorder
	if isSensitiveFormTask(req.Instruction) || hasSliderKeyword(req.Instruction) {
		fullRunRec = tool.StartFullRunRecorder(ctx, "browser-run", "browser", req.Instruction, "")
	}

	start := time.Now()
	result := h.executeWithPerception(ctx, &req, registry)

	// 判定本次 run 是否"真的成功":
	//  1. result.Status == "completed"
	//  2. AND 对登录类任务,最终 summary 不再停在登录页(用 stillOnLoginPage
	//     反向判断)
	// 满足才把录制写盘成 UIPattern,下次同域名任务能直接重放。
	truelySuccess := result.Status == "completed"
	if truelySuccess && isSensitiveFormTask(req.Instruction) && stillOnLoginPage(result.Summary) {
		truelySuccess = false
	}
	if fullRunRec != nil {
		fullRunRec.FinalizePersist(ctx, truelySuccess, false)
		if truelySuccess {
			diaglog.Logf("browser", "full run demo persisted: learning this successful flow")
		} else {
			diaglog.Logf("browser", "full run discarded: task not truly successful")
		}
	}

	h.learner.RecordOutcome(ctx, kernel.TaskOutcome{
		TaskType:  "browser.execute",
		Success:   truelySuccess,
		Duration:  time.Since(start),
		ToolCalls: result.Turns,
	})

	return result, nil
}

// executeWithPerception implements the two-tier execution strategy.
func (h *browserHandler) executeWithPerception(ctx context.Context, req *sidecar.ExecuteRequest, registry tool.Registry) *sidecar.ExecuteResult {
	diaglog.Logf("browser", "executeWithPerception: instruction=%s caller=%v sensitive=%v slider=%v",
		req.Instruction, h.caller != nil,
		isSensitiveFormTask(req.Instruction), hasSliderKeyword(req.Instruction))

	h.anchorToInstructionURL(ctx, registry, req.Instruction)

	if isSensitiveFormTask(req.Instruction) {
		if fast := h.tryReusableSensitivePattern(ctx, req, registry); fast != nil {
			return fast
		}
	}

	// Step 1: Try pattern_match on current page (if already navigated).
	// 登录/敏感表单任务跳过 pattern 复用:旧 pattern 可能固化了旧账号密码
	// 或空值,会导致"报告成功但实际没填写"的假象。每次登录都走 LLM planner
	// 生成新 plan,把用户给的具体值原样传进 browser.type。
	matchTool, hasMatch := registry.Lookup("browser.pattern_match")
	if hasMatch && isSensitiveFormTask(req.Instruction) {
		diaglog.Logf("browser", "skip pattern_match: instruction looks like a login/sensitive form task")
		hasMatch = false
	}
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

	// 登录 / 敏感表单 / 含滑块的任务跳过 planner 直接走 agent loop。
	// 理由:planner 是"一次规划固定 plan",不适合需要"试 → 看结果 → 调整"
	// 的场景(滑块可能一次拖不过,需要 LLM 看 snapshot 决定调 drag 重试
	// 还是 human.request_takeover)。agent loop 每轮都能看到工具清单
	// 和最新 snapshot,也能发 human.request_takeover 这种要阻塞的 tool
	// call。
	if isSensitiveFormTask(req.Instruction) || hasSliderKeyword(req.Instruction) {
		diaglog.Logf("browser", "sensitive/slider task, going straight to agent loop")
		return h.fallbackAgentLoop(ctx, req, registry, "")
	}

	diaglog.Logf("browser", "no pattern matched, calling LLM planner, snapshot_len=%d", len(pageSnapshot))
	plan, err := h.planWithLLM(ctx, req.Instruction, pageSnapshot, "", registry)
	if err == nil {
		diaglog.Logf("browser", "LLM plan ok: url=%s steps=%d", plan.URL, len(plan.Steps))
		result := h.executeLLMPlan(ctx, registry, plan, req.Instruction)
		if result.Status == "completed" {
			return result
		}
		feedback := extractRecentInteractionFeedback(result)
		diaglog.Logf("browser", "LLM plan execution failed: %s, falling back to agent loop (feedback=%q)", result.Error, feedback)
		return h.fallbackAgentLoop(ctx, req, registry, feedback)
	} else {
		diaglog.Logf("browser", "LLM planning failed: %v, falling back to agent loop", err)
	}

	// Step 3 (fallback): Multi-turn agent loop as last resort.
	return h.fallbackAgentLoop(ctx, req, registry, "")
}

// hasSliderKeyword 识别 instruction 是否提到滑块/验证码等需要多轮交互
// 判断的任务。这类任务跳过 planner,直接走 agent loop。
func hasSliderKeyword(instruction string) bool {
	s := strings.ToLower(instruction)
	needles := []string{
		"滑块", "拖动", "拖拽", "验证码", "人机验证", "safety check",
		"captcha", "slider", "slide to verify", "drag to verify",
	}
	for _, n := range needles {
		if strings.Contains(s, strings.ToLower(n)) {
			return true
		}
	}
	return false
}

func (h *browserHandler) anchorToInstructionURL(ctx context.Context, registry tool.Registry, instruction string) {
	targetURL := extractInstructionURL(instruction)
	if targetURL == "" {
		return
	}
	if sameHost(currentBrowserURL(ctx), targetURL) {
		return
	}
	openTool, ok := registry.Lookup("browser.open")
	if !ok {
		return
	}
	args, _ := json.Marshal(map[string]string{"url": targetURL})
	_, _ = openTool.Execute(ctx, args)
}

func (h *browserHandler) tryReusableSensitivePattern(ctx context.Context, req *sidecar.ExecuteRequest, registry tool.Registry) *sidecar.ExecuteResult {
	vars := extractVariables(req.Instruction)
	if !hasCredentialVariables(vars) {
		return nil
	}
	sess, ok := tool.CurrentSharedBrowserSession()
	if !ok || sess == nil {
		return nil
	}
	lib := tool.SharedPatternLibrary()
	if lib == nil {
		return nil
	}
	matches, err := tool.MatchPatterns(ctx, sess, lib, "auth")
	if err != nil {
		return nil
	}
	for _, m := range matches {
		if m == nil || m.Pattern == nil || !isSafeReusableAuthPattern(m.Pattern) {
			continue
		}
		exec := tool.ExecutePattern(ctx, sess, registry, m.Pattern, vars)
		_ = lib.RecordExecution(ctx, m.Pattern.ID, exec.Success, exec.DurationMS)
		if !exec.Success {
			continue
		}
		summary := buildVerifiedPageSummary(ctx, registry, 8000)
		if summary == "" {
			summary = "reusable auth pattern succeeded"
		}
		return &sidecar.ExecuteResult{
			Status:  "completed",
			Summary: summary,
			Turns:   0,
		}
	}
	return nil
}

func isSafeReusableAuthPattern(p *tool.UIPattern) bool {
	if p == nil || p.Category != "auth" || !p.Enabled {
		return false
	}
	hasCredentialPlaceholder := false
	credentialRoles := map[string]bool{
		"username_field": true, "password_field": true, "email_field": true,
	}
	for _, step := range p.ActionSequence {
		if step.Tool != "browser.type" {
			continue
		}
		text, _ := step.Params["text"].(string)
		if strings.Contains(text, "$credentials.") {
			hasCredentialPlaceholder = true
			continue
		}
		if text != "" && credentialRoles[step.TargetRole] {
			return false
		}
	}
	return hasCredentialPlaceholder
}

func buildVerifiedPageSummary(ctx context.Context, registry tool.Registry, maxChars int) string {
	snapshotTool, ok := registry.Lookup("browser.snapshot")
	if !ok {
		return ""
	}
	args, _ := json.Marshal(map[string]interface{}{"mode": "text", "max_chars": maxChars})
	res, err := snapshotTool.Execute(ctx, args)
	if err != nil || res == nil || res.IsError || len(res.Output) == 0 {
		return ""
	}
	var parsed struct {
		Title   string `json:"title"`
		URL     string `json:"url"`
		Content string `json:"content"`
	}
	if json.Unmarshal(res.Output, &parsed) == nil && (parsed.URL != "" || parsed.Title != "" || parsed.Content != "") {
		return fmt.Sprintf("Title: %s\nURL: %s\n\n%s", parsed.Title, parsed.URL, parsed.Content)
	}
	return string(res.Output)
}

func extractInstructionURL(instruction string) string {
	re := regexp.MustCompile(`https?://[^\s"'<>]+`)
	return strings.TrimSpace(re.FindString(instruction))
}

func currentBrowserURL(ctx context.Context) string {
	sess, ok := tool.CurrentSharedBrowserSession()
	if !ok || sess == nil {
		return ""
	}
	var out struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
	}
	if err := sess.Exec(ctx, "Runtime.evaluate", map[string]interface{}{
		"expression":    "location.href",
		"returnByValue": true,
	}, &out); err != nil {
		return ""
	}
	var pageURL string
	_ = json.Unmarshal(out.Result.Value, &pageURL)
	return pageURL
}

func hostOfURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u == nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

func sameHost(a, b string) bool {
	ha := hostOfURL(a)
	hb := hostOfURL(b)
	return ha != "" && hb != "" && ha == hb
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

	if result == nil {
		return &sidecar.ExecuteResult{Status: "failed", Error: "pattern_exec: nil result"}
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
	URL      string        `json:"url"`
	Category string        `json:"category"`
	Steps    []plannedStep `json:"steps"`
}

// planWithLLM calls LLM once to generate a full execution plan.
func (h *browserHandler) planWithLLM(ctx context.Context, instruction, pageSnapshot, feedback string, registry tool.Registry) (*llmPlan, error) {
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
- browser.geometry: {"id":123} or {"selector":"..."} — read an element's bounding box / edges / center. Useful for debugging or explicit geometry inspection.
- browser.drag: {"from_id"| "from_selector"|coords, "to_id"| "to_selector"|coords, "strategy":"auto|center|slider"} — press and hold then drag. USE THIS for slider CAPTCHA (滑块验证), slider captchas, drag-and-drop puzzles, range sliders. Human-like trajectory (easeInOut + jitter) is enabled by default. With strategy="auto" (default), if the target looks like a slider track the tool computes a more realistic end coordinate automatically.
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
- For slider CAPTCHA (滑块验证 / 拖动滑块 / slide-to-verify): FIRST take a snapshot with mode="interactive" to locate the slider handle and the slider track. THEN use browser.drag with the handle as source and the track as target; the tool can auto-compute a realistic end coordinate for slider-like targets. Use browser.geometry only when you need to inspect/debug the boxes explicitly. Typical selectors: ".slider-button", ".captcha-slider", ".nc_iconfont", ".verify-move-block", "[name='captcha-action']". DO NOT use browser.click on the slider — a click is not a drag and the captcha will not pass.
- If the captcha page is still present after the drag attempt, call human.request_takeover with reason="slider_failed" and guidance="请手动完成滑块验证，完成后点击 resume". DO NOT keep retrying drag blindly.
- The "url" field and any browser.open step MUST use the EXACT domain from the user's instruction. NEVER substitute a different website. If the instruction says "打开 https://example.com", the url MUST be on example.com, not any other domain.
- Output ONLY the JSON object, no explanation.`

	userMsg := "Instruction: " + instruction
	if pageSnapshot != "" {
		userMsg += "\n\nCurrent page state:\n" + pageSnapshot
	}
	if strings.TrimSpace(feedback) != "" {
		userMsg += "\n\nRecent interaction feedback:\n" + feedback
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

	// Sensitive form (login/payment/2FA) task 必须至少包含一个 browser.type
	// 步骤。有些 LLM 会生成只有 open+snapshot 的空 plan,然后返回"已完成"
	// 骗用户。这种 plan 直接视为失败,让上层 Agent 走 fallbackAgentLoop
	// (多轮 Agent 有 snapshot → 看到输入框 → 决定 type 的机会)。
	if isSensitiveFormTask(instruction) && !stepHasTool(plan, "browser.type") {
		diaglog.Logf("browser", "sensitive task but plan has no browser.type step; falling through to agent loop")
		return &sidecar.ExecuteResult{
			Status: "failed",
			Error:  "plan_missing_type_step",
		}
	}

	// Open URL if LLM specified one.
	if plan.URL != "" {
		if targetURL := extractInstructionURL(instruction); targetURL != "" {
			if !sameHost(plan.URL, targetURL) {
				diaglog.Logf("browser", "executeLLMPlan: plan URL %q does not match instruction URL %q, overriding", plan.URL, targetURL)
				plan.URL = targetURL
			}
		}
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
	instructionHost := hostOfURL(extractInstructionURL(instruction))
	var lastDragPostCheck map[string]interface{}
	for i, step := range plan.Steps {
		if step.Tool == "browser.open" && instructionHost != "" {
			if stepURL, _ := step.Params["url"].(string); stepURL != "" {
				if !sameHost(stepURL, "https://"+instructionHost) {
					diaglog.Logf("browser", "plan step %d: browser.open URL %q mismatches instruction host %q, overriding", i+1, stepURL, instructionHost)
					step.Params["url"] = extractInstructionURL(instruction)
				}
			}
		}
		diaglog.Logf("browser", "plan step %d/%d: %s params=%v", i+1, len(plan.Steps), step.Tool, step.Params)
		argsRaw, _ := json.Marshal(step.Params)
		sidecar.EmitProgress(ctx, sidecar.ProgressEvent{
			Kind:     "tool_start",
			ToolName: step.Tool,
			Args:     string(argsRaw),
		})
		t, ok := registry.Lookup(step.Tool)
		if !ok {
			sidecar.EmitProgress(ctx, sidecar.ProgressEvent{
				Kind: "tool_end", ToolName: step.Tool, OK: false,
				Detail: "tool not found",
			})
			return &sidecar.ExecuteResult{
				Status: "failed",
				Error:  fmt.Sprintf("step %d: tool %s not found", i+1, step.Tool),
				Turns:  0,
			}
		}
		result, err := t.Execute(ctx, argsRaw)
		if err != nil {
			sidecar.EmitProgress(ctx, sidecar.ProgressEvent{
				Kind: "tool_end", ToolName: step.Tool, OK: false,
				Detail: err.Error(),
			})
			return &sidecar.ExecuteResult{
				Status: "failed",
				Error:  fmt.Sprintf("step %d (%s): %v", i+1, step.Tool, err),
				Turns:  0,
			}
		}
		if result != nil {
			lastOutput = result.Output
			diaglog.Logf("browser", "plan step %d/%d result: isError=%v output=%.200s", i+1, len(plan.Steps), result.IsError, string(result.Output))
			okFlag := !result.IsError
			detail := ""
			if len(result.Output) > 0 {
				s := string(result.Output)
				if len(s) > 160 {
					s = s[:160] + "…"
				}
				detail = s
			}
			sidecar.EmitProgress(ctx, sidecar.ProgressEvent{
				Kind: "tool_end", ToolName: step.Tool, OK: okFlag, Detail: detail,
			})
			if result.IsError {
				return &sidecar.ExecuteResult{
					Status: "failed",
					Error:  fmt.Sprintf("step %d (%s): %s", i+1, step.Tool, string(result.Output)),
					Turns:  0,
				}
			}
			if step.Tool == "browser.drag" {
				var payload struct {
					PostCheck map[string]interface{} `json:"post_check"`
				}
				if json.Unmarshal(result.Output, &payload) == nil && payload.PostCheck != nil {
					lastDragPostCheck = payload.PostCheck
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
	if dragPostCheckNeedsAttention(lastDragPostCheck) {
		summary += "\n\n[Drag post-check: verification not confirmed after drag]"
		if detail := formatDragPostCheckFeedback(lastDragPostCheck); detail != "" {
			summary += "\n" + detail
		}
	}
	// 兜底:如果 plan 里做过拖动/点击登录等交互,但页面仍停留在验证码
	// 特征页上(URL 含 captcha/verify,或 title 含"安全验证"/"验证码"),
	// 自动调 human.request_takeover 挂起等人类处理,避免 AI 反复失败
	// 消耗上下文。已经请求过接管的调用走正常返回路径,由 coordinator 决定
	// resume/abort。
	// 登录任务的 post-check:plan 跑完了但最终页面仍然像是登录页
	// (URL 含 login/signin/auth,或 summary 里还能看到"密码"/"password"
	// 输入框文字),说明登录实际失败了。把 shouldRequestTakeover 触发
	// 条件扩大一份,让 human takeover 至少被考虑一次。
	if isSensitiveFormTask(instruction) && stillOnLoginPage(summary) {
		diaglog.Logf("browser", "sensitive task post-check: still on login/auth page, forcing takeover request")
	}
	if (shouldRequestTakeover(plan, summary, lastDragPostCheck) || (isSensitiveFormTask(instruction) && stillOnLoginPage(summary))) && !stepHasTool(plan, "human.request_takeover") {
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
				// takeover 回来后重新采集页面状态,让下面的 stillOnLoginPage
				// 判断用最新 snapshot。
				if snapshotTool, ok := registry.Lookup("browser.snapshot"); ok {
					snapArgs, _ := json.Marshal(map[string]interface{}{"mode": "text", "max_chars": 2000})
					if sr, err := snapshotTool.Execute(ctx, snapArgs); err == nil && sr != nil {
						var parsed struct {
							Title   string `json:"title"`
							URL     string `json:"url"`
							Content string `json:"content"`
						}
						if json.Unmarshal(sr.Output, &parsed) == nil && parsed.Content != "" {
							summary = fmt.Sprintf("Title: %s\nURL: %s\n\n%s",
								parsed.Title, parsed.URL, parsed.Content)
						}
					}
				}
			}
		}
	}

	// 最终判 failed:登录任务做完(也许带 takeover)后页面仍是登录页
	// 说明登录没真正成功,返回 failed 触发 executeWithPerception 的
	// fallbackAgentLoop —— 多轮 LLM Agent 会基于最新 snapshot 重新规划。
	if isSensitiveFormTask(instruction) && stillOnLoginPage(summary) {
		diaglog.Logf("browser", "executeLLMPlan: still on login page after plan+takeover, returning failed")
		return &sidecar.ExecuteResult{
			Status: "failed",
			Summary: summary,
			Error:  "login did not succeed; still on login/auth page after plan execution",
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
func shouldRequestTakeover(plan *llmPlan, summary string, dragPostCheck map[string]interface{}) bool {
	interacted := false
	dragAttempted := false
	for _, s := range plan.Steps {
		switch s.Tool {
		case "browser.drag", "browser.click", "browser.press_key", "browser.type":
			interacted = true
			if s.Tool == "browser.drag" {
				dragAttempted = true
			}
		}
	}
	if !interacted {
		return false
	}
	if dragAttempted && dragPostCheckNeedsAttention(dragPostCheck) {
		return true
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

func dragPostCheckNeedsAttention(post map[string]interface{}) bool {
	if len(post) == 0 {
		return false
	}
	if verified, ok := post["verified"].(bool); ok {
		return !verified
	}
	return false
}

func formatDragPostCheckFeedback(post map[string]interface{}) string {
	if len(post) == 0 {
		return ""
	}
	var parts []string
	if verified, ok := post["verified"].(bool); ok {
		parts = append(parts, fmt.Sprintf("verified=%t", verified))
	}
	if moved, ok := post["source_moved"].(bool); ok {
		parts = append(parts, fmt.Sprintf("source_moved=%t", moved))
	}
	if dist, ok := numericField(post, "movement_distance"); ok {
		parts = append(parts, fmt.Sprintf("movement_distance=%.1f", dist))
	}
	if dist, ok := numericField(post, "distance_to_expected"); ok {
		parts = append(parts, fmt.Sprintf("distance_to_expected=%.1f", dist))
	}
	if hint, ok := post["success_hint"].(bool); ok {
		parts = append(parts, fmt.Sprintf("success_hint=%t", hint))
	}
	if text, ok := post["success_text"].(string); ok && strings.TrimSpace(text) != "" {
		parts = append(parts, "success_text="+strings.TrimSpace(text))
	}
	if len(parts) == 0 {
		return ""
	}
	return "The last browser.drag did not confirm success (" + strings.Join(parts, ", ") + "). Avoid repeating the same drag blindly; inspect the slider/track again, adjust the drag target/strategy, or request human.request_takeover if still blocked."
}

func numericField(m map[string]interface{}, key string) (float64, bool) {
	v, ok := m[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

func extractRecentInteractionFeedback(result *sidecar.ExecuteResult) string {
	if result == nil {
		return ""
	}
	if note := feedbackFromSummary(result.Summary); note != "" {
		return note
	}
	if strings.Contains(strings.ToLower(result.Error), "drag post-check") {
		return strings.TrimSpace(result.Error)
	}
	return ""
}

func feedbackFromSummary(summary string) string {
	if !strings.Contains(summary, "[Drag post-check:") {
		return ""
	}
	return "The last browser.drag did not confirm success. The latest page snapshot still suggested the verification/login flow was incomplete. Do not repeat the same drag blindly; inspect the current slider state first, then adjust the drag attempt or request human.request_takeover."
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
//
// 这些词出现在 instruction 里就切到有头模式。宁可多开窗口,也不要
// 让用户看不到自己明确要求的操作过程。
func wantsVisibleBrowser(instruction string) bool {
	s := strings.ToLower(instruction)
	needles := []string{
		"我要看到", "给我看", "让我看", "我要能看到", "可见浏览器",
		"可视化", "看得到", "看到操作", "看到你的操作", "看到浏览器", "浏览器窗口", "打开浏览器",
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

func wantsHeadedBrowser(subtask *protocol.SubtaskContext, instruction string) bool {
	if subtask != nil {
		switch strings.ToLower(strings.TrimSpace(subtask.RenderMode)) {
		case "headed":
			return true
		case "headless":
			return false
		}
		if wantsVisibleBrowser(subtask.UserUtterance) {
			return true
		}
	}
	return wantsVisibleBrowser(instruction)
}

// stillOnLoginPage 根据最终 summary(text snapshot 的 Title+URL+Content)
// 粗略判断 plan 跑完后页面是否仍停在登录/认证页。典型特征:
//   - URL 路径含 /login /signin /auth
//   - 页面有"密码"/"password"/"登录"/"Sign in"之类的提示文本
//   - 页面有"登录失败"/"invalid password" 等错误提示
//
// 命中任一就当登录失败,交给 takeover 路径让用户接管。
func stillOnLoginPage(summary string) bool {
	if summary == "" {
		return false
	}
	pageURL := extractSummaryPageURL(summary)
	lowURL := strings.ToLower(pageURL)
	urlSignals := []string{
		"/login", "/signin", "/sign-in", "/auth/login", "/account/login",
		"login.html", "login.php",
	}
	for _, s := range urlSignals {
		if strings.Contains(lowURL, s) {
			return true
		}
	}
	low := strings.ToLower(summary)
	textSignals := []string{
		"登录失败", "账号错误", "账户错误", "密码错误", "密码不正确",
		"请输入密码", "请输入账号", "password is required",
		"please log in", "please sign in", "invalid credentials",
		"incorrect password", "incorrect username", "login failed",
	}
	for _, s := range textSignals {
		if strings.Contains(low, strings.ToLower(s)) {
			return true
		}
	}
	return false
}

func extractSummaryPageURL(summary string) string {
	for _, line := range strings.Split(summary, "\n") {
		if strings.HasPrefix(line, "URL: ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "URL: "))
		}
	}
	return ""
}

// isSensitiveFormTask 粗略判断 instruction 是否涉及登录/敏感表单。
// 命中这些关键词的任务不走 pattern_match 复用,强制重新规划,避免旧
// pattern 里的旧账号密码或空值导致"报告成功但其实没填"。
func isSensitiveFormTask(instruction string) bool {
	s := strings.ToLower(instruction)
	needles := []string{
		"登录", "登陆", "注册", "账号", "帐号", "账户", "密码",
		"口令", "验证码", "短信验证", "支付", "付款", "转账",
		"login", "log in", "sign in", "register", "signup",
		"sign up", "password", "passwd", "payment", "pay ",
		"username", "user name", "otp", "verify",
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

	vars := extractVariables(instruction)
	actionSeq := make([]tool.ActionStep, 0, len(plan.Steps))
	for _, s := range plan.Steps {
		actionSeq = append(actionSeq, tool.ActionStep{
			Tool:   s.Tool,
			Params: parameterizePlanStepParams(s.Params, vars),
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

	planURL := plan.URL
	if planURL == "" {
		planURL = currentBrowserURL(ctx)
	}
	if planURL == "" {
		diaglog.Logf("browser", "skip saving learned pattern: no URL in plan or browser")
		return
	}
	pat.AppliesWhen = tool.MatchCondition{
		URLPattern: extractDomainPattern(planURL),
	}

	if err := lib.Upsert(ctx, pat); err != nil {
		diaglog.Logf("browser", "failed to save learned pattern: %v", err)
	} else {
		diaglog.Logf("browser", "learned pattern %s (category=%s) from: %s", pat.ID, category, instruction)
	}
}

func parameterizePlanStepParams(params map[string]interface{}, vars map[string]interface{}) map[string]interface{} {
	if len(params) == 0 {
		return params
	}
	out := make(map[string]interface{}, len(params))
	creds, _ := vars["credentials"].(map[string]interface{})
	userValue := fmt.Sprint(creds["username"])
	if userValue == "<nil>" || userValue == "" {
		userValue = fmt.Sprint(creds["email"])
		if userValue == "<nil>" {
			userValue = ""
		}
	}
	passValue := fmt.Sprint(creds["password"])
	if passValue == "<nil>" {
		passValue = ""
	}
	for k, v := range params {
		s, ok := v.(string)
		if !ok {
			out[k] = v
			continue
		}
		trimmed := strings.TrimSpace(s)
		switch {
		case userValue != "" && strings.EqualFold(trimmed, userValue):
			out[k] = "$credentials.username"
		case passValue != "" && trimmed == passValue:
			out[k] = "$credentials.password"
		default:
			out[k] = v
		}
	}
	return out
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
func (h *browserHandler) fallbackAgentLoop(ctx context.Context, req *sidecar.ExecuteRequest, registry tool.Registry, feedback string) *sidecar.ExecuteResult {
	if h.caller == nil {
		return &sidecar.ExecuteResult{Status: "failed", Error: "no LLM proxy available"}
	}
	systemPrompt := `You are a browser specialist. You control a real browser.

Tools:
  - browser.snapshot(mode=interactive|text|html): read page structure or content.
      mode=interactive → clickable/typable element list with ids
      mode=text        → document.body.innerText (use for reading page content)
      mode=html        → full outerHTML
  - browser.open(url)
  - browser.click(id|selector)
  - browser.type(id|selector, text): type text into an input
  - browser.press_key(key): e.g. "Enter", "Escape"
  - browser.wait(condition=load|visible|idle)
  - browser.drag(from_id|from_selector|from_x,from_y, to_id|to_selector|to_x,to_y, strategy):
      press-and-drag. USE THIS for slider CAPTCHA (滑块验证), drag-and-drop
      puzzles, range sliders. strategy="auto" (default) auto-detects slider-like
      targets and computes a more realistic end coordinate.
      DO NOT use browser.click on a slider — slider must be dragged.
  - browser.geometry(id|selector): read bounding box / edges / center for an element.
      Useful for explicit geometry inspection when browser.drag auto mode is not enough.
  - browser.eval(expression): run JavaScript to read/extract data.
  - browser.screenshot(full_page=true): save a PNG (user visible file).
  - human.request_takeover(reason, guidance): HAND OFF to a human operator.
      CALL THIS — IT IS A REAL TOOL, NOT A METAPHOR. You MUST emit a tool_use
      block with name="human.request_takeover", not a text reply describing it.
      CALL THIS IMMEDIATELY WHEN (do NOT waste turns trying):
        * A slider CAPTCHA is still present AFTER you used browser.drag and
          made no progress.
        * SMS / phone verification / 2FA prompt appears
        * Image CAPTCHA ("select all crosswalks" etc.) appears
        * You tried 3+ distinct strategies with no progress
        * You cannot locate the element you need to interact with
      The agent PAUSES until the human /resumes. During the pause, CDP hooks
      record the human's clicks / inputs / drags into a demo sequence that
      becomes a learned UIPattern for future runs. THIS RECORDING ONLY HAPPENS
      IF YOU ACTUALLY CALL THIS TOOL.

  FORBIDDEN BEHAVIORS:
    - Do NOT navigate to a different website/domain than the one specified in
      the user's instruction. If the instruction says "打开 https://example.com",
      you MUST only open example.com, never a different domain. If a page
      redirects you to a different domain, report it rather than continuing.
    - Do not produce a text reply saying "please finish the CAPTCHA yourself"
      or "I will observe your operations" without ALSO calling
      human.request_takeover. Text is not recording — only the tool call is.
    - Do not claim "已完成" / "Done" / "Succeeded" unless a final
      browser.snapshot confirms you're on a different URL/title than the
      starting login page.
    - Do not pretend to have dragged the slider if browser.drag returned an
      error or the next snapshot still shows the slider.

Pass ALL user-provided values (username, password, URLs, queries, phone numbers)
VERBATIM to browser.type — never replace them with placeholders like $username
or ${password}. The type tool will enter whatever string you give it literally.

Be efficient: perceive, act, verify (snapshot), report. Do not take unnecessary actions.`

	budget := req.Budget
	if budget == nil {
		budget = &sidecar.ExecuteBudget{MaxTurns: 30}
	} else if budget.MaxTurns <= 0 {
		budget.MaxTurns = 30
	}
	return h.runFallbackAgentLoop(ctx, req, registry, systemPrompt, budget, true, feedback)
}

func (h *browserHandler) runFallbackAgentLoop(ctx context.Context, req *sidecar.ExecuteRequest, registry tool.Registry, systemPrompt string, budget *sidecar.ExecuteBudget, allowAutoTakeover bool, feedback string) *sidecar.ExecuteResult {
	result := runBrowserAgentLoop(ctx, h.caller, registry, systemPrompt, req.Instruction, budget, mergeLoopContextText(req.Context, feedback))
	result = normalizeBrowserLoopResult(ctx, registry, result)
	// budget 耗尽 = LLM 卡在循环里还没调 takeover,我们兜底自动调一次让
	// 用户接管。关键：/resume 后必须在同一个 delegated run 里继续执行，
	// 不能把“已恢复接管”伪装成 completed 然后把问题抛回 central。
	if !allowAutoTakeover || result == nil || result.Status != "failed" ||
		!strings.Contains(result.Error, "budget.turns_exhausted") {
		return result
	}

	diaglog.Logf("browser", "fallbackAgentLoop budget exhausted; auto-triggering human.request_takeover")
	takeoverTool, ok := registry.Lookup("human.request_takeover")
	if !ok {
		return result
	}

	tReq, _ := json.Marshal(map[string]interface{}{
		"reason":   "agent_exhausted",
		"guidance": "自动化尝试多次仍未通过,请手动完成当前步骤(拖滑块/登录等),完成后 /resume 继续。",
	})
	tRes, _ := takeoverTool.Execute(ctx, tReq)
	outcome, note := parseTakeoverOutcome(tRes)
	switch outcome {
	case "resumed":
		diaglog.Logf("browser", "human takeover resumed; continuing same delegated run")
		resumeBudget := &sidecar.ExecuteBudget{MaxTurns: max(6, min(12, budget.MaxTurns/2))}
		resumed := h.runFallbackAgentLoop(ctx, req, registry, systemPrompt, resumeBudget, false, "")
		if resumed == nil {
			return &sidecar.ExecuteResult{
				Status: "failed",
				Error:  "takeover_resumed_but_agent_returned_nil",
			}
		}
		resumed.Summary = appendTakeoverNote(resumed.Summary, outcome, note)
		return resumed
	case "aborted", "no_coordinator":
		if result.Summary == "" {
			result.Summary = appendTakeoverNote("", outcome, note)
		} else {
			result.Summary = appendTakeoverNote(result.Summary, outcome, note)
		}
		return result
	default:
		if tRes != nil && !tRes.IsError {
			result.Summary = appendTakeoverNote(result.Summary, "unknown", string(tRes.Output))
		}
		return result
	}
}

func mergeLoopContextText(existing json.RawMessage, feedback string) json.RawMessage {
	feedback = strings.TrimSpace(feedback)
	if feedback == "" {
		return existing
	}
	text := "Recent interaction feedback:\n" + feedback
	if base := strings.TrimSpace(summarizeLoopContext(existing)); base != "" {
		text = base + "\n\n" + text
	}
	raw, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return existing
	}
	return raw
}

func normalizeBrowserLoopResult(ctx context.Context, registry tool.Registry, result *sidecar.ExecuteResult) *sidecar.ExecuteResult {
	if result == nil {
		return nil
	}
	verified := buildVerifiedPageSummary(ctx, registry, 4000)
	if verified == "" {
		return result
	}
	switch {
	case strings.TrimSpace(result.Summary) == "":
		result.Summary = verified
	default:
		result.Summary = verified + "\n\n[Agent summary]\n" + result.Summary
	}
	return result
}

func summarizeLoopContext(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		return asString
	}
	var asMap map[string]interface{}
	if err := json.Unmarshal(raw, &asMap); err == nil {
		if s, ok := asMap["text"].(string); ok && s != "" {
			return s
		}
		if s, ok := asMap["summary"].(string); ok && s != "" {
			return s
		}
	}
	return string(raw)
}

func parseTakeoverOutcome(res *tool.Result) (outcome string, note string) {
	if res == nil || res.IsError || len(res.Output) == 0 {
		return "", ""
	}
	var payload struct {
		Outcome string `json:"outcome"`
		Note    string `json:"note"`
	}
	if err := json.Unmarshal(res.Output, &payload); err != nil {
		return "", ""
	}
	return strings.TrimSpace(payload.Outcome), strings.TrimSpace(payload.Note)
}

func appendTakeoverNote(summary, outcome, note string) string {
	entry := "[Human takeover outcome: " + strings.TrimSpace(outcome)
	if strings.TrimSpace(note) != "" {
		entry += "; note: " + strings.TrimSpace(note)
	}
	entry += "]"
	if strings.TrimSpace(summary) == "" {
		return entry
	}
	return summary + "\n\n" + entry
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
	var credentials = map[string]interface{}{}

	if v := extractCredentialValue(instruction, []string{"账号", "帐号", "账户", "用户名", "user name", "username", "account"}); v != "" {
		credentials["email"] = v
		credentials["username"] = v
	}
	if v := extractCredentialValue(instruction, []string{"密码", "password", "passwd"}); v != "" {
		credentials["password"] = v
	}
	if len(credentials) > 0 {
		vars["credentials"] = credentials
	}

	if url := extractInstructionURL(instruction); url != "" {
		vars["url"] = url
	}

	searchKeywords := []string{"搜索", "查询", "查找"}
	for _, kw := range searchKeywords {
		if idx := strings.Index(instruction, kw); idx >= 0 {
			rest := strings.TrimSpace(instruction[idx+len(kw):])
			rest = strings.TrimLeft(rest, ":： ")
			if cutIdx := strings.IndexAny(rest, "，。；！？,.;!?\n"); cutIdx > 0 {
				rest = rest[:cutIdx]
			}
			clauseBreakers := []string{" 然后", " 并", " 再", " 之后", " 接着"}
			for _, br := range clauseBreakers {
				if ci := strings.Index(rest, br); ci > 0 {
					rest = rest[:ci]
				}
			}
			rest = strings.TrimSpace(rest)
			if rest != "" {
				vars["query"] = rest
			}
			break
		}
	}
	return vars
}

var (
	credQuoteClass    = `["\x{201c}\x{201d}\x{2018}\x{2019}']`
	credNonQuoteClass = `[^"\x{201c}\x{201d}\x{2018}\x{2019}']+`
)

func extractCredentialValue(instruction string, labels []string) string {
	for _, label := range labels {
		quotedRe := regexp.MustCompile("(?i)" + regexp.QuoteMeta(label) + `\s*[:：]?\s*` + credQuoteClass + `\s*(` + credNonQuoteClass + `)\s*` + credQuoteClass)
		m := quotedRe.FindStringSubmatch(instruction)
		if len(m) > 1 {
			return strings.TrimSpace(m[1])
		}
		bareRe := regexp.MustCompile("(?i)" + regexp.QuoteMeta(label) + `\s*[:：]\s*([^\s,，。；;！!？?\])]+)`)
		m = bareRe.FindStringSubmatch(instruction)
		if len(m) > 1 {
			return strings.TrimSpace(strings.Trim(m[1], "\"'\u201c\u201d\u2018\u2019"))
		}
	}
	return ""
}

func hasCredentialVariables(vars map[string]interface{}) bool {
	if len(vars) == 0 {
		return false
	}
	credentials, ok := vars["credentials"].(map[string]interface{})
	if !ok {
		return false
	}
	_, hasUser := credentials["username"]
	_, hasEmail := credentials["email"]
	_, hasPass := credentials["password"]
	return (hasUser || hasEmail) && hasPass
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
		fmt.Fprintf(os.Stderr, "brain-browser: toolpolicy load: %v\n", err)
	} else {
		reg = toolpolicy.FilterRegistry(reg, cfg, toolpolicy.ToolScopesForDelegate(string(agent.KindBrowser))...)
	}
	ensureCriticalBrowserTools(reg, h.browserTools)
	return reg, nil
}

func ensureCriticalBrowserTools(reg tool.Registry, browserTools []tool.Tool) {
	if reg == nil {
		return
	}
	if _, ok := reg.Lookup("browser.drag"); !ok {
		for _, t := range browserTools {
			if t != nil && t.Name() == "browser.drag" {
				reg.Register(t)
				break
			}
		}
	}
	if _, ok := reg.Lookup("human.request_takeover"); !ok {
		reg.Register(tool.NewHumanRequestTakeoverTool())
	}
}
