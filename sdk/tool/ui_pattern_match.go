package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/leef-l/brain/sdk/tool/cdp"
)

// Pattern matching engine — decide which UIPattern applies to the current
// page and execute its ActionSequence with ElementDescriptor self-healing.
//
// See sdk/docs/40-Browser-Brain语义理解架构.md §3.3.
//
// Selection flow:
//   1. For each pattern in library, run AppliesWhen (cheap DOM checks)
//   2. Score candidates by historical success rate × match strength
//   3. Return top-k (default 3), or execute top-1 if requested

// PatternMatch is a candidate pattern + confidence score.
type PatternMatch struct {
	Pattern    *UIPattern `json:"pattern"`
	Score      float64    `json:"score"`
	MatchedVia string     `json:"matched_via"` // summary of which conditions matched
}

// MatchPatterns scores and returns candidate patterns for the current page.
//
// P3.4:使用 patternIndex 做倒排预筛,只对 host/首段 path + category 匹配的
// 模式做完整 MatchCondition 评估,大库下单次耗时从 ~50ms 降到 ~5ms。
func MatchPatterns(ctx context.Context, sess *cdp.BrowserSession, lib *PatternLibrary, category string) ([]*PatternMatch, error) {
	pageURL, pageTitle := readPageMeta(ctx, sess)
	if pageURL == "" || pageURL == "about:blank" {
		return nil, nil
	}
	pageText := readBodyText(ctx, sess, 20_000)

	ids := candidatePatterns(lib, pageURL, category)

	candidates := []*PatternMatch{}
	for _, id := range ids {
		p := lib.Get(id)
		if p == nil {
			continue
		}
		ok, reason := evaluateMatch(ctx, sess, &p.AppliesWhen, pageURL, pageTitle, pageText)
		if !ok {
			continue
		}
		// Score: 0.5 × success rate + 0.3 × match strength + 0.2 × recency
		score := 0.5*p.Stats.SuccessRate() + 0.3*matchStrength(&p.AppliesWhen, reason) + 0.2*recencyBonus(p.Stats.LastSuccessAt)
		candidates = append(candidates, &PatternMatch{Pattern: p, Score: score, MatchedVia: reason})
	}

	// Sort by score desc
	for i := 0; i < len(candidates); i++ {
		for j := i + 1; j < len(candidates); j++ {
			if candidates[j].Score > candidates[i].Score {
				candidates[i], candidates[j] = candidates[j], candidates[i]
			}
		}
	}
	return candidates, nil
}

// evaluateMatch runs AppliesWhen against the current page.
func evaluateMatch(ctx context.Context, sess *cdp.BrowserSession, cond *MatchCondition, url, title, bodyText string) (bool, string) {
	reasons := []string{}

	if cond.URLPattern != "" {
		re, err := regexp.Compile(cond.URLPattern)
		if err != nil || !re.MatchString(url) {
			return false, ""
		}
		reasons = append(reasons, "url")
	}
	if cond.SiteHost != "" {
		if !siteHostMatchesURL(cond.SiteHost, url) {
			return false, ""
		}
		reasons = append(reasons, "site")
	}

	titleLower := strings.ToLower(title)
	for _, needle := range cond.TitleContains {
		if !strings.Contains(titleLower, strings.ToLower(needle)) {
			return false, ""
		}
	}
	if len(cond.TitleContains) > 0 {
		reasons = append(reasons, "title")
	}

	bodyLower := strings.ToLower(bodyText)
	for _, needle := range cond.TextContains {
		if !strings.Contains(bodyLower, strings.ToLower(needle)) {
			return false, ""
		}
	}
	if len(cond.TextContains) > 0 {
		reasons = append(reasons, "text")
	}

	if len(cond.Has) > 0 {
		ok := checkSelectors(ctx, sess, cond.Has, true)
		if !ok {
			return false, ""
		}
		reasons = append(reasons, "has")
	}
	if len(cond.HasNot) > 0 {
		ok := checkSelectors(ctx, sess, cond.HasNot, false)
		if !ok {
			return false, ""
		}
		reasons = append(reasons, "hasnot")
	}

	if len(reasons) == 0 {
		return false, ""
	}
	return true, strings.Join(reasons, "+")
}

// checkSelectors evaluates a list of CSS selectors in the page.
// If mustExist=true, all must match; if false, none must match.
func checkSelectors(ctx context.Context, sess *cdp.BrowserSession, selectors []string, mustExist bool) bool {
	for _, sel := range selectors {
		js := fmt.Sprintf(`!!document.querySelector(%q)`, sel)
		exists, err := evalBool(ctx, sess, js)
		if err != nil {
			return false
		}
		if mustExist && !exists {
			return false
		}
		if !mustExist && exists {
			return false
		}
	}
	return true
}

// readBodyText returns up to maxLen chars of body innerText.
func readBodyText(ctx context.Context, sess *cdp.BrowserSession, maxLen int) string {
	js := fmt.Sprintf(`(document.body ? (document.body.innerText || '') : '').slice(0, %d)`, maxLen)
	var out struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
	}
	if err := sess.Exec(ctx, "Runtime.evaluate", map[string]interface{}{
		"expression": js, "returnByValue": true,
	}, &out); err != nil {
		return ""
	}
	var s string
	_ = json.Unmarshal(out.Result.Value, &s)
	return s
}

func matchStrength(cond *MatchCondition, reason string) float64 {
	signals := 0
	if cond.URLPattern != "" {
		signals++
	}
	if cond.SiteHost != "" {
		signals++
	}
	signals += len(cond.Has)
	signals += len(cond.TitleContains)
	signals += len(cond.TextContains)
	return math.Min(float64(signals)/10.0, 1.0)
}

func siteHostMatchesURL(siteHost, pageURL string) bool {
	if siteHost == "" || pageURL == "" {
		return false
	}
	u, err := url.Parse(pageURL)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Host, siteHost)
}

func recencyBonus(last time.Time) float64 {
	if last.IsZero() {
		return 0.0
	}
	age := time.Since(last)
	if age < 24*time.Hour {
		return 1.0
	}
	if age < 7*24*time.Hour {
		return 0.5
	}
	return 0.1
}

// ---------------------------------------------------------------------------
// ElementDescriptor resolution — self-healing locator
// ---------------------------------------------------------------------------

// ResolveElement finds a DOM element matching the descriptor and returns its
// data-brain-id (attached by browser.snapshot). The descriptor is tried with
// multiple fallbacks, UiPath Object Repository-style:
//  1. role+name exact match from current snapshot
//  2. role+name fuzzy match
//  3. CSS selector
//  4. XPath
//  5. declared fallbacks (recursive)
//
// Returns brain_id > 0 on success, 0 on failure.
func ResolveElement(ctx context.Context, sess *cdp.BrowserSession, desc ElementDescriptor) (int, error) {
	elements, err := collectInteractive(ctx, sess)
	if err != nil {
		return 0, fmt.Errorf("snapshot failed: %w", err)
	}

	if id := matchByRoleName(elements, desc, false); id > 0 {
		return id, nil
	}
	if id := matchByRoleName(elements, desc, true); id > 0 {
		return id, nil
	}
	if desc.CSS != "" {
		if id := matchByCSS(ctx, sess, desc.CSS); id > 0 {
			return id, nil
		}
	}
	if desc.XPath != "" {
		if id := matchByXPath(ctx, sess, desc.XPath); id > 0 {
			return id, nil
		}
	}
	for _, fb := range desc.Fallback {
		if id, err := ResolveElement(ctx, sess, fb); err == nil && id > 0 {
			return id, nil
		}
	}
	return 0, fmt.Errorf("element not resolvable (role=%q name=%q css=%q)", desc.Role, desc.Name, desc.CSS)
}

// matchByRoleName scans a snapshot for a role/name match.
// fuzzy=true enables substring and regex matching on Name.
func matchByRoleName(elements []brainElement, desc ElementDescriptor, fuzzy bool) int {
	// Name may start with "~" to mean regex.
	var nameRE *regexp.Regexp
	literalName := ""
	if strings.HasPrefix(desc.Name, "~") {
		var err error
		nameRE, err = regexp.Compile(desc.Name[1:])
		if err != nil {
			nameRE = nil
		}
	} else {
		literalName = desc.Name
	}

	for _, el := range elements {
		if desc.Role != "" && !strings.EqualFold(el.Role, desc.Role) && !strings.EqualFold(el.Tag, desc.Role) {
			continue
		}
		if desc.Tag != "" && !strings.EqualFold(el.Tag, desc.Tag) {
			continue
		}
		if desc.Type != "" && !strings.EqualFold(el.Type, desc.Type) {
			continue
		}
		if nameRE != nil {
			if !nameRE.MatchString(el.Name) {
				continue
			}
		} else if literalName != "" {
			if fuzzy {
				if !strings.Contains(strings.ToLower(el.Name), strings.ToLower(literalName)) {
					continue
				}
			} else if !strings.EqualFold(el.Name, literalName) {
				continue
			}
		}
		return el.ID
	}
	return 0
}

// matchByCSS evaluates a CSS selector and, if a match exists, finds (or
// assigns) its data-brain-id in the freshest snapshot.
func matchByCSS(ctx context.Context, sess *cdp.BrowserSession, css string) int {
	js := fmt.Sprintf(`(function(){
		var el = document.querySelector(%q);
		if(!el) return -1;
		var id = el.getAttribute('data-brain-id');
		if(!id){
			// Re-tag: snapshot may not have covered this element yet.
			// We just return 0 and let the caller re-snapshot if needed.
			return 0;
		}
		return parseInt(id);
	})()`, css)
	var out struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
	}
	if err := sess.Exec(ctx, "Runtime.evaluate", map[string]interface{}{
		"expression": js, "returnByValue": true,
	}, &out); err != nil {
		return 0
	}
	var id float64
	if err := json.Unmarshal(out.Result.Value, &id); err != nil {
		return 0
	}
	if int(id) <= 0 {
		// Trigger re-snapshot to assign ID, then retry once.
		_, _ = collectInteractive(ctx, sess)
		if err := sess.Exec(ctx, "Runtime.evaluate", map[string]interface{}{
			"expression": js, "returnByValue": true,
		}, &out); err == nil {
			_ = json.Unmarshal(out.Result.Value, &id)
		}
	}
	if int(id) > 0 {
		return int(id)
	}
	return 0
}

func matchByXPath(ctx context.Context, sess *cdp.BrowserSession, xpath string) int {
	js := fmt.Sprintf(`(function(){
		var r = document.evaluate(%q, document, null, XPathResult.FIRST_ORDERED_NODE_TYPE, null);
		var el = r.singleNodeValue;
		if(!el) return -1;
		var id = el.getAttribute ? el.getAttribute('data-brain-id') : null;
		return id ? parseInt(id) : 0;
	})()`, xpath)
	var out struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
	}
	if err := sess.Exec(ctx, "Runtime.evaluate", map[string]interface{}{
		"expression": js, "returnByValue": true,
	}, &out); err != nil {
		return 0
	}
	var id float64
	_ = json.Unmarshal(out.Result.Value, &id)
	if int(id) > 0 {
		return int(id)
	}
	return 0
}

// ---------------------------------------------------------------------------
// PostCondition validation
// ---------------------------------------------------------------------------

// CheckPostCondition returns (ok, reason).
func CheckPostCondition(ctx context.Context, sess *cdp.BrowserSession, cond *PostCondition, urlBefore string) (bool, string) {
	switch cond.Type {
	case "url_changed":
		u, _ := readPageMeta(ctx, sess)
		if u != urlBefore && u != "" {
			return true, "url is " + u
		}
		return false, "url unchanged"

	case "url_matches":
		u, _ := readPageMeta(ctx, sess)
		re, err := regexp.Compile(cond.URLPattern)
		if err != nil {
			return false, "bad regex"
		}
		if re.MatchString(u) {
			return true, "url matches"
		}
		return false, "url " + u + " doesn't match"

	case "dom_contains":
		ok, _ := evalBool(ctx, sess, fmt.Sprintf(`!!document.querySelector(%q)`, cond.Selector))
		if ok {
			return true, cond.Selector + " present"
		}
		return false, cond.Selector + " absent"

	case "cookie_set":
		js := fmt.Sprintf(`document.cookie.split(';').some(function(c){return c.trim().indexOf(%q+'=')===0;})`, cond.CookieName)
		ok, _ := evalBool(ctx, sess, js)
		if ok {
			return true, "cookie set"
		}
		return false, "cookie absent"

	case "title_contains":
		_, title := readPageMeta(ctx, sess)
		if strings.Contains(strings.ToLower(title), strings.ToLower(cond.TitleContains)) {
			return true, "title match"
		}
		return false, "title mismatch"

	case "any_of":
		for _, sub := range cond.Any {
			if ok, _ := CheckPostCondition(ctx, sess, &sub, urlBefore); ok {
				return true, "any_of matched"
			}
		}
		return false, "any_of all failed"

	case "response_ok":
		// Best-effort: we can't access response status without netBuf here.
		// This condition type is validated at runner layer when netBuf is provided.
		return true, "response_ok unchecked at this layer"
	}
	return false, "unknown post condition"
}

// ---------------------------------------------------------------------------
// Pattern execution
// ---------------------------------------------------------------------------

// ExecutionResult is the outcome of running a pattern.
type ExecutionResult struct {
	PatternID        string        `json:"pattern_id"`
	Success          bool          `json:"success"`
	ActionsRun       int           `json:"actions_run"`
	StepResults      []StepOutcome `json:"step_results"`
	PostConditions   []PostOutcome `json:"post_conditions"`
	DurationMS       int64         `json:"duration_ms"`
	Error            string        `json:"error,omitempty"`
	AbortedByAnomaly string        `json:"aborted_by_anomaly,omitempty"`
}

// StepOutcome captures one ActionStep's result.
type StepOutcome struct {
	Tool     string `json:"tool"`
	Skipped  bool   `json:"skipped,omitempty"`
	Error    string `json:"error,omitempty"`
	TargetID int    `json:"target_id,omitempty"`
}

// PostOutcome captures a PostCondition check result.
type PostOutcome struct {
	Type   string `json:"type"`
	OK     bool   `json:"ok"`
	Reason string `json:"reason"`
}

// maxPatternChainDepth 限定 on_anomaly=fallback_pattern 的跳转链深度,
// 防止模式库里两个 pattern 互相指回形成死循环。3 足以覆盖"登录→验证码→
// 再登录"等两段式降级。
const maxPatternChainDepth = 3

// patternLibGetter 使 ExecutePattern 可注入一个"按 ID 取模式"的查找器,
// 生产用 sharedPatternLib().GetAny,测试用内存 map,避免单测打开 SQLite。
type patternLibGetter func(id string) *UIPattern

func (g patternLibGetter) Get(id string) *UIPattern {
	if g == nil {
		return nil
	}
	return g(id)
}

func (patternLibGetter) AnomalyTemplates() AnomalyTemplateSource { return nil }

type patternExecGetter interface {
	Get(id string) *UIPattern
	AnomalyTemplates() AnomalyTemplateSource
}

type patternExecDeps struct {
	patterns  patternLibGetter
	templates AnomalyTemplateSource
}

func (d patternExecDeps) Get(id string) *UIPattern {
	return d.patterns.Get(id)
}

func (d patternExecDeps) AnomalyTemplates() AnomalyTemplateSource {
	if d.templates == nil {
		return SharedAnomalyTemplateLibrary()
	}
	return d.templates
}

func sharedPatternExecGetter() patternExecGetter {
	return patternExecDeps{
		patterns: patternLibGetter(func(id string) *UIPattern {
			lib, _ := sharedPatternLib()
			if lib == nil {
				return nil
			}
			return lib.GetAny(id)
		}),
		templates: nil,
	}
}

// ExecutePattern runs a pattern against the current page. `variables` provides
// placeholder values (e.g. {"credentials.email": "a@b.com"}).
func ExecutePattern(ctx context.Context, sess *cdp.BrowserSession, registry Registry, p *UIPattern, variables map[string]interface{}) *ExecutionResult {
	return executePatternWithLib(ctx, sess, registry, p, variables, sharedPatternExecGetter())
}

// executePatternWithLib 是带 lib 注入点的内部实现,测试用。
func executePatternWithLib(ctx context.Context, sess *cdp.BrowserSession, registry Registry, p *UIPattern, variables map[string]interface{}, getter patternExecGetter) *ExecutionResult {
	start := time.Now()
	res := &ExecutionResult{PatternID: p.ID}
	urlBefore, _ := readPageMeta(ctx, sess)

	// pattern 链最外层:fallback_pattern 会切换 current 并从头重跑。
	current := p
	visited := map[string]bool{p.ID: true}
	depth := 0
	for {
		terminal, switchTo := runActionSequence(ctx, sess, registry, current, variables, res, start, getter)
		if terminal {
			break
		}
		if switchTo == "" {
			break
		}
		// 尝试切 fallback pattern。空 getter / 未找到 / 已访问 → 视为 abort,
		// 避免在模式库缺失时悄悄成功返回。
		if getter == nil {
			res.Error = "fallback_pattern requested but no library getter"
			finalize(res, current, false, start)
			return res
		}
		next := getter.Get(switchTo)
		if next == nil {
			res.Error = "fallback pattern not found: " + switchTo
			finalize(res, current, false, start)
			return res
		}
		if visited[next.ID] || depth+1 >= maxPatternChainDepth {
			res.Error = "fallback chain aborted (cycle or depth exceeded): " + next.ID
			finalize(res, current, false, start)
			return res
		}
		visited[next.ID] = true
		depth++
		current = next
		res.PatternID = current.ID
		// 切 pattern 后重新采集 urlBefore,使 url_changed PostCondition 以
		// 实际起点为基准。
		urlBefore, _ = readPageMeta(ctx, sess)
	}

	// Evaluate PostConditions against the last pattern run. res.Error /
	// res.AbortedByAnomaly 不为空时,terminal 已经 finalize,直接返回。
	if res.Error != "" || res.AbortedByAnomaly != "" {
		finalize(res, current, false, start)
		return res
	}
	allOK := true
	for _, pc := range current.PostConditions {
		ok, reason := CheckPostCondition(ctx, sess, &pc, urlBefore)
		res.PostConditions = append(res.PostConditions, PostOutcome{Type: pc.Type, OK: ok, Reason: reason})
		if !ok {
			allOK = false
		}
	}
	if len(current.PostConditions) > 0 && !allOK {
		res.Error = "post conditions not met"
	}
	finalize(res, current, allOK || len(current.PostConditions) == 0, start)
	return res
}

// runActionSequence 执行一个 pattern 的 ActionSequence。返回 (terminal, switchTo):
//   - terminal=true:res 已 finalize,主循环立刻退出(abort / human abort / 成功跑完)
//   - terminal=false, switchTo=="":本 pattern 正常跑完,交给主循环做 PostCondition
//   - terminal=false, switchTo="<id>":触发 fallback_pattern,主循环切换并重跑
//
// retry 在本函数里原地循环,不回给主循环。
func runActionSequence(ctx context.Context, sess *cdp.BrowserSession, registry Registry, p *UIPattern, variables map[string]interface{}, res *ExecutionResult, start time.Time, getter patternExecGetter) (terminal bool, switchTo string) {
	for i := 0; i < len(p.ActionSequence); i++ {
		step := p.ActionSequence[i]
		retries := 0
	stepLoop:
		for {
			outcome, toolResult, runErr := executeStep(ctx, sess, registry, p, step, variables)

			// 步骤本身错误 + 非 optional 且没有 anomaly 可路由 → 记录后 abort
			if runErr != "" && !step.Optional && (toolResult == nil || len(toolResult.Output) == 0) {
				outcome.Error = runErr
				res.StepResults = append(res.StepResults, outcome)
				res.Error = runErr
				finalize(res, p, false, start)
				return true, ""
			}

			// 检查 tool 返回里的 _anomalies 并按 OnAnomaly 决策
			if toolResult != nil && len(toolResult.Output) > 0 {
				aType, aSubtype, severity := detectAnomalyInOutput(toolResult.Output)
				if aType != "" || aSubtype != "" {
					handler, match, _ := ResolveAnomalyHandler(
						anomalyTemplateSource(getter),
						p,
						aType,
						aSubtype,
						currentSiteOrigin(ctx, sess),
						severity,
					)
					if match {
						switch handler.Action {
						case "abort":
							res.AbortedByAnomaly = nonEmpty(aSubtype, aType)
							res.Error = "aborted_by_anomaly: " + handler.Reason
							res.StepResults = append(res.StepResults, outcome)
							finalize(res, p, false, start)
							return true, ""

						case "retry":
							maxR := handler.MaxRetries
							if maxR <= 0 {
								maxR = 1
							}
							if retries < maxR {
								retries++
								if handler.BackoffMS > 0 {
									// 尊重 ctx 取消,避免 sleep 阻住 shutdown。
									select {
									case <-ctx.Done():
										outcome.Error = "ctx cancelled during retry backoff"
										res.StepResults = append(res.StepResults, outcome)
										res.Error = outcome.Error
										finalize(res, p, false, start)
										return true, ""
									case <-time.After(time.Duration(handler.BackoffMS) * time.Millisecond):
									}
								}
								// 不推进 index,重跑本步
								continue stepLoop
							}
							// 超上限 → 升级为 abort,让 Agent 看得见
							res.AbortedByAnomaly = nonEmpty(aSubtype, aType)
							res.Error = fmt.Sprintf("retry exhausted (max=%d) on anomaly %s", maxR, res.AbortedByAnomaly)
							res.StepResults = append(res.StepResults, outcome)
							finalize(res, p, false, start)
							return true, ""

						case "fallback_pattern":
							if handler.FallbackID == "" {
								res.AbortedByAnomaly = nonEmpty(aSubtype, aType)
								res.Error = "fallback_pattern action without fallback_id"
								res.StepResults = append(res.StepResults, outcome)
								finalize(res, p, false, start)
								return true, ""
							}
							res.StepResults = append(res.StepResults, outcome)
							// 不 finalize,让主循环去切 pattern 重跑。
							return false, handler.FallbackID

						case "human_intervention":
							// 调 siblings 里的 human.request_takeover。没注册视为 abort。
							hRes := invokeHumanTakeover(ctx, registry, p.ID, nonEmpty(aSubtype, aType), handler.Reason)
							outcome.Error = "human_intervention:" + hRes
							res.StepResults = append(res.StepResults, outcome)
							if hRes == "resumed" {
								// 人类处理完,本步重跑一次
								retries++
								if retries > 3 {
									res.AbortedByAnomaly = nonEmpty(aSubtype, aType)
									res.Error = "human_intervention loop exceeded"
									finalize(res, p, false, start)
									return true, ""
								}
								continue stepLoop
							}
							// aborted / no_coordinator → 终止
							res.AbortedByAnomaly = nonEmpty(aSubtype, aType)
							res.Error = "human_intervention:" + hRes
							finalize(res, p, false, start)
							return true, ""
						}
					}
				}
			}

			// 常规路径:工具无 anomaly 可路由。如果 step error 且 !optional,abort;否则推进。
			if runErr != "" && !step.Optional {
				outcome.Error = runErr
				res.StepResults = append(res.StepResults, outcome)
				res.Error = runErr
				finalize(res, p, false, start)
				return true, ""
			}
			res.StepResults = append(res.StepResults, outcome)
			res.ActionsRun = i + 1
			break stepLoop
		}
	}
	return false, ""
}

// executeStep 跑单个 ActionStep,包括 ElementDescriptor 解析、占位符替换、
// 工具调用。返回 outcome(TargetID/Tool 已填)、工具结果、运行错误字符串。
// 错误字符串空串表示成功,非空表示该步未能正常结束(caller 据 optional 决策)。
func executeStep(ctx context.Context, sess *cdp.BrowserSession, registry Registry, p *UIPattern, step ActionStep, variables map[string]interface{}) (StepOutcome, *Result, string) {
	outcome := StepOutcome{Tool: step.Tool}

	if step.TargetRole != "" {
		desc, ok := p.ElementRoles[step.TargetRole]
		if !ok {
			return outcome, nil, "unknown target_role: " + step.TargetRole
		}
		id, err := ResolveElement(ctx, sess, desc)
		if err != nil || id <= 0 {
			return outcome, nil, fmt.Sprintf("resolve %s: %v", step.TargetRole, err)
		}
		outcome.TargetID = id
		if step.Params == nil {
			step.Params = map[string]interface{}{}
		}
		if step.Tool == "browser.drag" {
			step.Params["from_id"] = id
		} else {
			step.Params["id"] = id
		}
	}

	params := resolvePlaceholders(step.Params, variables)
	toolImpl, exists := registry.Lookup(step.Tool)
	if !exists {
		return outcome, nil, "tool not registered: " + step.Tool
	}
	paramsJSON, _ := json.Marshal(params)
	toolResult, err := toolImpl.Execute(ctx, paramsJSON)
	if err != nil {
		return outcome, toolResult, err.Error()
	}
	if toolResult != nil && toolResult.IsError {
		// 把错误体透传给 caller,同时通过 toolResult 让 anomaly 路由有机会介入。
		return outcome, toolResult, string(toolResult.Output)
	}
	return outcome, toolResult, ""
}

// matchAnomalyHandler 按任务描述先 subtype 查 OnAnomaly,后 type 查。
// 两者都空返回 (nil, false)。空指针 p 也视为未匹配。
func matchAnomalyHandler(p *UIPattern, aType, aSubtype string) (*AnomalyHandler, bool) {
	if p == nil || len(p.OnAnomaly) == 0 {
		return nil, false
	}
	if aSubtype != "" {
		if h, ok := p.OnAnomaly[aSubtype]; ok {
			return &h, true
		}
	}
	if aType != "" {
		if h, ok := p.OnAnomaly[aType]; ok {
			return &h, true
		}
	}
	if h, ok := p.OnAnomaly["any"]; ok {
		return &h, true
	}
	return nil, false
}

// invokeHumanTakeover 通过 siblings 里的 human.request_takeover 工具触发接管。
// 返回 outcome 字符串:"resumed" / "aborted" / "no_coordinator" /
// "tool_missing" / "invoke_error:<msg>"。
func invokeHumanTakeover(ctx context.Context, registry Registry, patternID, anomalySig, handlerReason string) string {
	if registry == nil {
		return "tool_missing"
	}
	impl, ok := registry.Lookup("human.request_takeover")
	if !ok {
		return "tool_missing"
	}
	reason := anomalySig
	if reason == "" {
		reason = "pattern_anomaly"
	}
	args := map[string]interface{}{
		"reason":   reason,
		"guidance": fmt.Sprintf("pattern %s blocked by anomaly %q: %s", patternID, anomalySig, handlerReason),
	}
	raw, _ := json.Marshal(args)
	r, err := impl.Execute(ctx, raw)
	if err != nil {
		return "invoke_error:" + err.Error()
	}
	if r == nil || len(r.Output) == 0 {
		return "no_coordinator"
	}
	var body struct {
		Outcome string `json:"outcome"`
	}
	if err := json.Unmarshal(r.Output, &body); err != nil {
		return "invoke_error:unparseable"
	}
	if body.Outcome == "" {
		return "no_coordinator"
	}
	return body.Outcome
}

func nonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func finalize(res *ExecutionResult, p *UIPattern, success bool, start time.Time) {
	res.Success = success
	res.DurationMS = time.Since(start).Milliseconds()
}

// resolvePlaceholders replaces "$path.to.var" tokens with concrete values.
func resolvePlaceholders(params map[string]interface{}, vars map[string]interface{}) map[string]interface{} {
	if len(params) == 0 {
		return params
	}
	out := make(map[string]interface{}, len(params))
	for k, v := range params {
		if s, ok := v.(string); ok && strings.HasPrefix(s, "$") {
			if resolved, found := lookupVar(vars, s[1:]); found {
				out[k] = resolved
				continue
			}
		}
		out[k] = v
	}
	return out
}

func lookupVar(vars map[string]interface{}, path string) (interface{}, bool) {
	parts := strings.Split(path, ".")
	var cur interface{} = vars
	for _, p := range parts {
		m, ok := cur.(map[string]interface{})
		if !ok {
			return nil, false
		}
		cur, ok = m[p]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

// detectAnomalyInOutput peeks at tool output for the first entry of
// `_anomalies.anomalies[0]` and returns (type, subtype, severity). Each field
// may be empty.
// 选第一条异常是 anomaly 列表里优先级最高的(anomalyInjectingTool 按
// severity 排序过),M5 on_anomaly 只对"最严重那条"做路由决策,后续条
// 通过 _anomalies 字段仍可被 Agent 看到。
func detectAnomalyInOutput(output json.RawMessage) (atype, subtype, severity string) {
	var wrapper struct {
		Anomalies struct {
			Anomalies []struct {
				Type     string `json:"type"`
				Subtype  string `json:"subtype"`
				Severity string `json:"severity"`
			} `json:"anomalies"`
		} `json:"_anomalies"`
	}
	if err := json.Unmarshal(output, &wrapper); err == nil && len(wrapper.Anomalies.Anomalies) > 0 {
		first := wrapper.Anomalies.Anomalies[0]
		return first.Type, first.Subtype, first.Severity
	}
	return "", "", ""
}

func anomalyTemplateSource(getter patternExecGetter) AnomalyTemplateSource {
	if getter == nil {
		return nil
	}
	return getter.AnomalyTemplates()
}

func currentSiteOrigin(ctx context.Context, sess *cdp.BrowserSession) string {
	if sess == nil {
		return ""
	}
	url, _ := readPageMeta(ctx, sess)
	return normalizeOrigin(url)
}
