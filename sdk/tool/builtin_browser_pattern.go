package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"

	brainerrors "github.com/leef-l/brain/sdk/errors"
)

// M2 低语义置信度降级阈值。命中的 SemanticCache 条目里只要有任一条
// Confidence < lowConfidenceThreshold 或 Quality == "low_confidence",
// pattern_match 会在 match 结果上标 _degrade_reason,pattern_exec 直接
// 拒绝执行并要求 Agent 回落到 browser.snapshot + LLM 推理。
//
// 0.7 来自 Phase 0 实验:understand 的置信度校准在 0.7 以上准确率显著
// 高,0.7 以下错标率陡升。调整此阈值可改变降级激进度。
const lowConfidenceThreshold = 0.7

// semanticDegrade 描述一次降级判定的结果,nil 表示不降级。
type semanticDegrade struct {
	// Reason 面向 Agent 的人类可读说明。pattern_exec 错误里会用这个
	// 文本,pattern_match 的 _degrade_reason 字段也取这个值。
	Reason string
	// MinConfidence 触发降级的最低 confidence(仅当因 confidence 触发时填)。
	MinConfidence float64
	// Quality 触发降级的 quality 值(仅当因 quality 触发时填)。
	Quality string
}

// evaluateSemanticConfidence 对一批 SemanticCache lookup 结果做统一的降级
// 判定。没有命中条目时返回 nil(不降级 —— 认为页面还没被 understand 过,
// 交给 Agent 自决)。任一命中条目的 Confidence < 阈值或 Quality 标为
// "low_confidence" 即触发降级,Reason 提示 Agent 回落到 snapshot + LLM。
// 纯函数,便于单测。
func evaluateSemanticConfidence(entries map[string]*SemanticEntry) *semanticDegrade {
	if len(entries) == 0 {
		return nil
	}
	minConf := 1.0
	for _, e := range entries {
		if e == nil {
			continue
		}
		if e.Quality == "low_confidence" {
			return &semanticDegrade{
				Reason:  fmt.Sprintf("low semantic confidence (quality=low_confidence on element_key=%q), fall back to browser.snapshot + LLM reasoning", e.ElementKey),
				Quality: e.Quality,
			}
		}
		if e.Confidence > 0 && e.Confidence < minConf {
			minConf = e.Confidence
		}
	}
	if minConf < lowConfidenceThreshold {
		return &semanticDegrade{
			Reason:        fmt.Sprintf("low semantic confidence (min=%.2f < %.2f), fall back to browser.snapshot + LLM reasoning", minConf, lowConfidenceThreshold),
			MinConfidence: minConf,
		}
	}
	return nil
}

// checkPageSemanticConfidence 是 pattern_match / pattern_exec 在触碰 DOM
// 前的降级门禁。它取当前页的 URL + 元素快照,按 understand 缓存 key 查
// SemanticCache,然后交给 evaluateSemanticConfidence 判定。
//
// sess/cache 任一为 nil 视为无法判定,返回 nil(放行)。
var checkPageSemanticConfidence = func(ctx context.Context, holder *browserSessionHolder) *semanticDegrade {
	if holder == nil {
		return nil
	}
	sess, err := holder.get(ctx)
	if err != nil || sess == nil {
		return nil
	}
	cache, err := NewSemanticCache("")
	if err != nil || cache == nil {
		return nil
	}
	defer cache.Close()

	pageURL, _ := readPageMeta(ctx, sess)
	elements, err := collectInteractive(ctx, sess)
	if err != nil || len(elements) == 0 {
		return nil
	}
	urlPat := urlPattern(pageURL)
	dhash := domHash(elements)
	keys := make([]string, 0, len(elements))
	for _, el := range elements {
		keys = append(keys, elementKey(el))
	}
	hits, _ := cache.Lookup(ctx, urlPat, dhash, keys)
	return evaluateSemanticConfidence(hits)
}

// browser.pattern_match / browser.pattern_exec / browser.pattern_list tools.
// See sdk/docs/40-Browser-Brain语义理解架构.md §3.3.
//
// These tools expose the UI pattern library to the Agent:
//   - pattern_match: what patterns are candidates for the current page?
//   - pattern_exec:  execute one pattern end-to-end
//   - pattern_list:  browse the library (used by WebUI and debugging)
//
// The library is lazily initialized at first use and shared across tools
// via a process-wide singleton.

var (
	patternLibOnce sync.Once
	patternLib     *PatternLibrary
	patternLibErr  error
)

func sharedPatternLib() (*PatternLibrary, error) {
	patternLibOnce.Do(func() {
		patternLib, patternLibErr = NewPatternLibrary("")
		if patternLibErr != nil {
			fmt.Fprintf(os.Stderr, "  [pattern] library unavailable: %v\n", patternLibErr)
		}
	})
	return patternLib, patternLibErr
}

// SharedPatternLibrary 暴露进程级单例给 dashboard / cli。首次调用会打开
// 默认 DSN 下的 SQLite 库;读取失败返回 nil。
func SharedPatternLibrary() *PatternLibrary {
	lib, _ := sharedPatternLib()
	return lib
}

// ---------------------------------------------------------------------------
// browser.pattern_match
// ---------------------------------------------------------------------------

type browserPatternMatchTool struct{ holder *browserSessionHolder }

func (t *browserPatternMatchTool) Name() string { return "browser.pattern_match" }
func (t *browserPatternMatchTool) Risk() Risk   { return RiskSafe }

func (t *browserPatternMatchTool) Schema() Schema {
	return Schema{
		Name: t.Name(),
		Description: `Find UI patterns that match the current page.

Use on a new page to discover "this is a login form" / "this is a product
detail page" shortcuts. Each returned pattern has a score combining historical
success rate with current match strength. Top-ranked candidate may be passed
to browser.pattern_exec.`,
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "category": { "type": "string", "description": "Limit to one category: auth|search|commerce|nav|form" },
    "limit":    { "type": "integer", "description": "Max matches to return (default 5)" }
  }
}`),
		OutputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "url":     { "type": "string" },
    "count":   { "type": "integer" },
    "matches": { "type": "array" }
  }
}`),
		Brain: "browser",
		Concurrency: &ToolConcurrencySpec{
			Capability:          "web.read",
			ResourceKeyTemplate: "browser:session",
			AccessMode:          "shared-session",
			Scope:               "turn",
			ApprovalClass:       "safe",
		},
	}
}

func (t *browserPatternMatchTool) Execute(ctx context.Context, args json.RawMessage) (*Result, error) {
	var input struct {
		Category string `json:"category"`
		Limit    int    `json:"limit"`
	}
	if len(args) > 0 && string(args) != "null" {
		if err := json.Unmarshal(args, &input); err != nil {
			return errResult("invalid arguments: %v", err), nil
		}
	}
	if input.Limit <= 0 {
		input.Limit = 5
	}

	sess, err := t.holder.get(ctx)
	if err != nil {
		return errResult("no browser session: %v", err), nil
	}
	lib, err := sharedPatternLib()
	if err != nil {
		return errResult("library unavailable: %v", err), nil
	}

	matches, err := MatchPatterns(ctx, sess, lib, input.Category)
	if err != nil {
		return errResult("match: %v", err), nil
	}
	if len(matches) > input.Limit {
		matches = matches[:input.Limit]
	}

	// P3.5:把 top 候选 score 写进 recorder,让 DecideBrowserStage 可以
	// 依据"模式库匹配度"自动切换到 known_flow / new_page。无候选记 0.0,
	// 有候选取第一个(MatchPatterns 已按 Score 降序)。
	top := 0.0
	if len(matches) > 0 {
		top = matches[0].Score
	}
	RecordPatternMatchScore(ctx, top)

	// M2 降级:SemanticCache 里该页命中的 understand 条目存在低置信或
	// low_confidence 质量标记时,给每条 match 打上 _degrade_reason,让
	// Agent 清楚看到"虽然有模式候选,但当前页语义质量不足以直接执行"。
	degrade := checkPageSemanticConfidence(ctx, t.holder)

	out := make([]map[string]interface{}, 0, len(matches))
	for _, m := range matches {
		item := map[string]interface{}{
			"pattern_id":    m.Pattern.ID,
			"category":      m.Pattern.Category,
			"description":   m.Pattern.Description,
			"score":         m.Score,
			"matched_via":   m.MatchedVia,
			"success_rate":  m.Pattern.Stats.SuccessRate(),
			"match_count":   m.Pattern.Stats.MatchCount,
		}
		if degrade != nil {
			item["_degrade_reason"] = degrade.Reason
		}
		out = append(out, item)
	}
	pageURL, _ := readPageMeta(ctx, sess)
	result := map[string]interface{}{
		"url": pageURL, "count": len(out), "matches": out,
	}
	if degrade != nil {
		result["_degrade_reason"] = degrade.Reason
	}
	return okResult(result), nil
}

// ---------------------------------------------------------------------------
// browser.pattern_exec
// ---------------------------------------------------------------------------

// patternExecRegistry shim — so ExecutePattern can call sibling tools via
// the real registry without this tool needing it at construction time.
type patternExecRegistry struct {
	tools map[string]Tool
}

func (r *patternExecRegistry) Register(_ Tool) error            { return nil }
func (r *patternExecRegistry) Lookup(name string) (Tool, bool)  { t, ok := r.tools[name]; return t, ok }
func (r *patternExecRegistry) List() []Tool                     { return nil }
func (r *patternExecRegistry) ListByBrain(_ string) []Tool      { return nil }

type browserPatternExecTool struct {
	holder *browserSessionHolder
	// siblings is set by NewBrowserTools after all tools are constructed.
	siblingsMu sync.Mutex
	siblings   map[string]Tool
}

func (t *browserPatternExecTool) Name() string { return "browser.pattern_exec" }
func (t *browserPatternExecTool) Risk() Risk   { return RiskMedium }

func (t *browserPatternExecTool) Schema() Schema {
	return Schema{
		Name: t.Name(),
		Description: `Execute a matched UI pattern end-to-end.

Resolves ElementDescriptors (with self-healing fallbacks), runs the
ActionSequence in order, validates PostConditions, and updates stats.
Returns a structured ExecutionResult with per-step outcomes.

Pass 'variables' to fill placeholders like $credentials.email used in
seed patterns (e.g. login_username_password).`,
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "pattern_id": { "type": "string", "description": "ID of pattern to execute (from pattern_match)" },
    "variables":  { "type": "object", "description": "Values for placeholders. Example: {\"credentials\":{\"email\":\"...\",\"password\":\"...\"}}" }
  },
  "required": ["pattern_id"]
}`),
		OutputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "pattern_id":         { "type": "string" },
    "success":            { "type": "boolean" },
    "actions_run":        { "type": "integer" },
    "step_results":       { "type": "array" },
    "post_conditions":    { "type": "array" },
    "duration_ms":        { "type": "integer" },
    "error":              { "type": "string" },
    "aborted_by_anomaly": { "type": "string" }
  }
}`),
		Brain: "browser",
		Concurrency: &ToolConcurrencySpec{
			Capability:          "web.interact",
			ResourceKeyTemplate: "browser:session",
			AccessMode:          "exclusive-session",
			Scope:               "turn",
			ApprovalClass:       "external-network",
		},
	}
}

func (t *browserPatternExecTool) setSiblings(tools []Tool) {
	t.siblingsMu.Lock()
	defer t.siblingsMu.Unlock()
	t.siblings = make(map[string]Tool, len(tools))
	for _, tl := range tools {
		t.siblings[tl.Name()] = tl
	}
}

func (t *browserPatternExecTool) Execute(ctx context.Context, args json.RawMessage) (*Result, error) {
	var input struct {
		PatternID string                 `json:"pattern_id"`
		Variables map[string]interface{} `json:"variables"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return errResult("invalid arguments: %v", err), nil
	}
	if input.PatternID == "" {
		return errResult("pattern_id is required"), nil
	}

	sess, err := t.holder.get(ctx)
	if err != nil {
		return errResult("no browser session: %v", err), nil
	}
	lib, err := sharedPatternLib()
	if err != nil {
		return errResult("library unavailable: %v", err), nil
	}
	pat := lib.Get(input.PatternID)
	if pat == nil {
		return errResult("pattern %q not found", input.PatternID), nil
	}

	// M2 降级:低语义置信时拒绝执行,让 Agent 回落到 browser.snapshot +
	// LLM 推理路径。错误 code 走 brainerrors.CodeToolExecutionFailed,使
	// retry 装饰器按矩阵决策(permanent,不重试)。
	if degrade := checkPageSemanticConfidence(ctx, t.holder); degrade != nil {
		return ErrorResult(brainerrors.CodeToolExecutionFailed, "%s", degrade.Reason), nil
	}

	t.siblingsMu.Lock()
	siblings := t.siblings
	t.siblingsMu.Unlock()
	if siblings == nil {
		return errResult("siblings not wired; internal setup error"), nil
	}
	registry := &patternExecRegistry{tools: siblings}

	exec := ExecutePattern(ctx, sess, registry, pat, input.Variables)
	_ = lib.RecordExecution(ctx, pat.ID, exec.Success, exec.DurationMS)

	// P3.5:把 pattern_exec 的 turn-level 结果写进 recorder,供
	// DecideBrowserStage 判断"连续 N turn 无进展"是否应切 fallback。
	outcome := "ok"
	if !exec.Success {
		outcome = "error"
	}
	RecordTurnOutcome(ctx, outcome)

	// P3.2:失败时记一条 PatternFailureSample。只给 learned 模式记——
	// seed 模式我们不分裂,记了也没用,还会污染 site_anomaly 统计。
	// AbortedByAnomaly 空代表"这次失败没被路由成特定异常"——仍然记录,
	// 但 subtype 空,ScanForSplit 会跳过该样本。
	if !exec.Success && pat.Source == "learned" {
		pageURL, _ := readPageMeta(ctx, sess)
		site := siteFromURL(pageURL)
		_ = RecordPatternFailure(ctx, pat.ID, site, exec.AbortedByAnomaly, exec.ActionsRun, pageURL)
	}

	data, _ := json.Marshal(exec)
	return &Result{Output: data, IsError: !exec.Success}, nil
}

// ---------------------------------------------------------------------------
// browser.pattern_list
// ---------------------------------------------------------------------------

type browserPatternListTool struct{}

func (t *browserPatternListTool) Name() string { return "browser.pattern_list" }
func (t *browserPatternListTool) Risk() Risk   { return RiskSafe }

func (t *browserPatternListTool) Schema() Schema {
	return Schema{
		Name: t.Name(),
		Description: `List UI patterns in the library (for browsing / debugging / WebUI).`,
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "category": { "type": "string" },
    "limit":    { "type": "integer" }
  }
}`),
		OutputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "count":    { "type": "integer" },
    "patterns": { "type": "array" }
  }
}`),
		Brain: "browser",
		Concurrency: &ToolConcurrencySpec{
			Capability:          "web.read",
			ResourceKeyTemplate: "pattern:library",
			AccessMode:          "shared-session",
			Scope:               "turn",
			ApprovalClass:       "safe",
		},
	}
}

func (t *browserPatternListTool) Execute(ctx context.Context, args json.RawMessage) (*Result, error) {
	var input struct {
		Category string `json:"category"`
		Limit    int    `json:"limit"`
	}
	if len(args) > 0 && string(args) != "null" {
		if err := json.Unmarshal(args, &input); err != nil {
			return errResult("invalid arguments: %v", err), nil
		}
	}
	if input.Limit <= 0 {
		input.Limit = 50
	}
	lib, err := sharedPatternLib()
	if err != nil {
		return errResult("library unavailable: %v", err), nil
	}
	all := lib.List(input.Category)
	if len(all) > input.Limit {
		all = all[:input.Limit]
	}
	out := make([]map[string]interface{}, 0, len(all))
	for _, p := range all {
		out = append(out, map[string]interface{}{
			"id":            p.ID,
			"category":      p.Category,
			"description":   p.Description,
			"source":        p.Source,
			"success_rate":  p.Stats.SuccessRate(),
			"match_count":   p.Stats.MatchCount,
			"success_count": p.Stats.SuccessCount,
			"failure_count": p.Stats.FailureCount,
		})
	}
	return okResult(map[string]interface{}{"count": len(out), "patterns": out}), nil
}
