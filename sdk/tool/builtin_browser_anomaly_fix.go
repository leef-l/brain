package tool

// builtin_browser_anomaly_fix.go — P3.1-C browser.request_anomaly_fix 工具。
//
// 语义:"我被这个 anomaly 卡住了,请 LLM 根据最近 3 步上下文 + 同类站点画像,
// 给我一个 recovery 方案"。调用方(Agent)显式触发,不自动注入。
//
// 流程:
//   1. 参数解析 → 组装 prompt(anomaly 结构 + 最近 3 步 SequenceRecorder 动作 + 同类 site profile)
//   2. 调 LLMBackend 拿 action JSON(schema 严格 == []AnomalyTemplateRecoveryAction)
//   3. 返回给 Agent,同时把候选方案放进 AnomalyFixCandidate 暂存,由调用方在执行完后
//      通过 RecordAnomalyFixOutcome 反馈成功/失败;累积满阈值 + SuccessRate ≥ 0.6
//      自动 PromoteCandidate 固化为 AnomalyTemplate 入库
//
// 这里只做"生成 + 记录 + 固化"三步,不负责真的去执行 recovery —— Agent 决定是否按
// 建议去调对应工具,执行还是走 pattern_exec / 直接工具调用的老路。
//
// 和 LearningEngine 的耦合:candidates 的持久化通过 kernel 层完成(持久化接口
// 由 learning.go 注入),本包只持有内存 map 方便测试。

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// AnomalyFixCandidate 暂存一个 LLM 提议的修复方案 + 执行统计,用于后续
// PromoteCandidate 判定。通过 Signature 聚合 —— 同一签名命中的多次 LLM
// 建议会合并到一个 candidate(建议本身可能每次不同,我们保留最新那次
// 的 Recovery,只累计 Stats)。
type AnomalyFixCandidate struct {
	Signature AnomalyTemplateSignature
	Recovery  []AnomalyTemplateRecoveryAction
	Stats     AnomalyTemplateStats
}

// anomalyFixStore 管理 candidates 的线程安全内存态。key: signature 规范化串。
type anomalyFixStore struct {
	mu         sync.Mutex
	candidates map[string]*AnomalyFixCandidate
	// 固化阈值,对齐 A 库的自动停用阈值(成功率 >= 0.6 + 样本 >= 3 → 固化)。
	minSamples int
	minRate    float64
}

func newAnomalyFixStore() *anomalyFixStore {
	return &anomalyFixStore{
		candidates: map[string]*AnomalyFixCandidate{},
		minSamples: 3,
		minRate:    0.6,
	}
}

// sigKey 把 signature 压扁成稳定 key。
func sigKey(sig AnomalyTemplateSignature) string {
	return strings.Join([]string{
		strings.ToLower(sig.Type), strings.ToLower(sig.Subtype),
		sig.SitePattern, strings.ToLower(sig.Severity),
	}, "\x1f")
}

// upsertCandidate 新建或覆盖一个 candidate(覆盖 Recovery 但保留 Stats)。
func (s *anomalyFixStore) upsertCandidate(sig AnomalyTemplateSignature, recovery []AnomalyTemplateRecoveryAction) *AnomalyFixCandidate {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := sigKey(sig)
	c := s.candidates[key]
	if c == nil {
		c = &AnomalyFixCandidate{Signature: sig}
		s.candidates[key] = c
	}
	c.Recovery = recovery
	c.Stats.UpdatedAt = time.Now()
	return c
}

// recordOutcome 累计一次结果。返回更新后的 candidate(nil=找不到)。
func (s *anomalyFixStore) recordOutcome(sig AnomalyTemplateSignature, success bool) *AnomalyFixCandidate {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.candidates[sigKey(sig)]
	if c == nil {
		return nil
	}
	if success {
		c.Stats.SuccessCount++
	} else {
		c.Stats.FailureCount++
	}
	c.Stats.UpdatedAt = time.Now()
	return c
}

// promoteIfReady 条件满足时把 candidate 晋升为 AnomalyTemplate 入库,返回新
// 模板(nil = 还不够资格);晋升后从 store 里移除该 candidate。
func (s *anomalyFixStore) promoteIfReady(lib *AnomalyTemplateLibrary, sig AnomalyTemplateSignature) *AnomalyTemplate {
	if lib == nil {
		return nil
	}
	s.mu.Lock()
	key := sigKey(sig)
	c := s.candidates[key]
	s.mu.Unlock()
	if c == nil {
		return nil
	}
	tpl := lib.PromoteCandidate(c.Signature, c.Recovery, c.Stats, s.minSamples, s.minRate)
	if tpl != nil {
		s.mu.Lock()
		delete(s.candidates, key)
		s.mu.Unlock()
	}
	return tpl
}

// listCandidates 调试/测试用。
func (s *anomalyFixStore) listCandidates() []*AnomalyFixCandidate {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*AnomalyFixCandidate, 0, len(s.candidates))
	for _, c := range s.candidates {
		copy := *c
		out = append(out, &copy)
	}
	return out
}

// ---------------------------------------------------------------------------
// browser.request_anomaly_fix tool
// ---------------------------------------------------------------------------

// AnomalyFixContext 是传给 LLM 的上下文载荷 —— 合并 anomaly 字段 + 最近 N 步
// + 站点画像 —— 都是已存在的结构,这里只是 JSON 载体。
type AnomalyFixContext struct {
	Anomaly       json.RawMessage    `json:"anomaly"`                // 当前 anomaly 完整 JSON
	RecentActions []RecordedAction   `json:"recent_actions,omitempty"` // 最近 3 步(倒序)
	SiteProfile   []HostAnomalyEntry `json:"site_profile,omitempty"` // 同 host 的异常画像
}

type browserRequestAnomalyFixTool struct {
	holder *browserSessionHolder
	llm    LLMBackend
	lib    *AnomalyTemplateLibrary // 用于晋升 candidate
	store  *anomalyFixStore
}

// NewBrowserRequestAnomalyFixTool 生产工厂。lib/llm 为空时工具降级为"只
// 生成请求但不调 LLM",方便测试和离线开发。
func NewBrowserRequestAnomalyFixTool(holder *browserSessionHolder, llm LLMBackend, lib *AnomalyTemplateLibrary) *browserRequestAnomalyFixTool {
	return &browserRequestAnomalyFixTool{
		holder: holder,
		llm:    llm,
		lib:    lib,
		store:  newAnomalyFixStore(),
	}
}

func (t *browserRequestAnomalyFixTool) Name() string { return "browser.request_anomaly_fix" }
func (t *browserRequestAnomalyFixTool) Risk() Risk   { return RiskSafe }

func (t *browserRequestAnomalyFixTool) Schema() Schema {
	return Schema{
		Name: t.Name(),
		Description: `Ask the LLM to propose a recovery action sequence for the given
anomaly, using the current anomaly structure + recent 3 steps + cross-site
profile as context.

Returns a JSON object with a "recovery" array of actions (kind in
{retry, fallback_pattern, human_intervention, custom_steps}). The Agent
chooses whether to apply the proposed recovery — this tool neither executes
the recovery nor persists it as a pattern by default.

Use this when:
  - pattern_exec / on_anomaly routing gave up (no template matched)
  - a novel anomaly subtype keeps occurring
  - you need a site-specific recovery suggestion

Outcome feedback (success / failure) should be reported back via
browser.record_anomaly_fix_outcome so the system can eventually promote a
proven recovery into an AnomalyTemplate.`,
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "anomaly":      { "type": "object", "description": "Anomaly struct from browser.check_anomaly / v2 output" },
    "site_origin":  { "type": "string", "description": "Current page origin (scheme://host). Optional; falls back to sess.URL." },
    "max_context_actions": { "type": "integer", "description": "How many recent recorder actions to include (default 3)" }
  },
  "required": ["anomaly"]
}`),
		OutputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "signature":  { "type": "object" },
    "recovery":   { "type": "array" },
    "rationale":  { "type": "string" },
    "source":     { "type": "string", "enum": ["llm","fallback"] }
  }
}`),
		Brain: "browser",
		Concurrency: &ToolConcurrencySpec{
			Capability:          "llm.anomaly_fix",
			ResourceKeyTemplate: "llm:anomaly_fix",
			AccessMode:          "shared",
			Scope:               "turn",
			ApprovalClass:       "safe",
		},
	}
}

func (t *browserRequestAnomalyFixTool) Execute(ctx context.Context, args json.RawMessage) (*Result, error) {
	var input struct {
		Anomaly           json.RawMessage `json:"anomaly"`
		SiteOrigin        string          `json:"site_origin"`
		MaxContextActions int             `json:"max_context_actions"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return errResult("invalid arguments: %v", err), nil
	}
	if len(input.Anomaly) == 0 {
		return errResult("anomaly is required"), nil
	}
	if input.MaxContextActions <= 0 {
		input.MaxContextActions = 3
	}

	// 解出 Anomaly 基本字段以形成 Signature
	var a struct {
		Type     string `json:"type"`
		Subtype  string `json:"subtype"`
		Severity string `json:"severity"`
	}
	_ = json.Unmarshal(input.Anomaly, &a)
	if a.Type == "" {
		return errResult("anomaly.type is required"), nil
	}

	// 站点来源:显式 > session URL
	siteOrigin := normalizeOrigin(input.SiteOrigin)
	if siteOrigin == "" && t.holder != nil {
		if sess, err := t.holder.get(ctx); err == nil && sess != nil {
			if pageURL, _ := readPageMeta(ctx, sess); pageURL != "" {
				siteOrigin = normalizeOrigin(pageURL)
			}
		}
	}

	// 最近 N 步 + 同 host 画像
	recent := tailRecorderActions(ctx, input.MaxContextActions)
	var siteProfile []HostAnomalyEntry
	if t.holder != nil {
		if hist := t.holder.anomalyHistory(); hist != nil && hist.siteHist != nil && siteOrigin != "" {
			siteProfile = hist.siteHist.listForSite(siteOrigin)
		}
	}

	fixCtx := AnomalyFixContext{
		Anomaly:       input.Anomaly,
		RecentActions: recent,
		SiteProfile:   siteProfile,
	}

	sig := AnomalyTemplateSignature{
		Type: a.Type, Subtype: a.Subtype, Severity: a.Severity, SitePattern: siteOrigin,
	}

	// 调 LLM。没配 backend → 降级为 fallback recovery(单步 retry + human 兜底)。
	recovery, rationale, source := t.propose(ctx, fixCtx, sig)
	if len(recovery) == 0 {
		return errResult("empty recovery from propose"), nil
	}

	// 暂存候选
	t.store.upsertCandidate(sig, recovery)

	out := map[string]interface{}{
		"signature": sig,
		"recovery":  recovery,
		"rationale": rationale,
		"source":    source,
	}
	data, _ := json.Marshal(out)
	return &Result{Output: data}, nil
}

// propose 调 LLM 或 fallback 生成 recovery。返回 (recovery, rationale, source)。
func (t *browserRequestAnomalyFixTool) propose(ctx context.Context, fixCtx AnomalyFixContext, sig AnomalyTemplateSignature) ([]AnomalyTemplateRecoveryAction, string, string) {
	if t.llm == nil {
		return fallbackRecovery(sig), "no-llm-fallback: generic retry + human escalation", "fallback"
	}
	raw, err := t.llm.Complete(ctx, anomalyFixSystemPrompt, buildAnomalyFixPrompt(fixCtx))
	if err != nil || strings.TrimSpace(raw) == "" {
		return fallbackRecovery(sig), fmt.Sprintf("llm error, fallback: %v", err), "fallback"
	}
	rec, rationale, err := parseLLMRecovery(raw)
	if err != nil || len(rec) == 0 {
		return fallbackRecovery(sig), fmt.Sprintf("llm parse error, fallback: %v", err), "fallback"
	}
	return rec, rationale, "llm"
}

// RecordFixOutcome 供调用方反馈 recovery 成功/失败。条件满足时自动晋升。
// 返回 (candidate, promoted) — promoted != nil 表示本次触发了入库。
func (t *browserRequestAnomalyFixTool) RecordFixOutcome(sig AnomalyTemplateSignature, success bool) (*AnomalyFixCandidate, *AnomalyTemplate) {
	c := t.store.recordOutcome(sig, success)
	if c == nil {
		return nil, nil
	}
	tpl := t.store.promoteIfReady(t.lib, sig)
	return c, tpl
}

// ListFixCandidates 便于运维 / 测试。
func (t *browserRequestAnomalyFixTool) ListFixCandidates() []*AnomalyFixCandidate {
	return t.store.listCandidates()
}

// fallbackRecovery 生成一个保守修复:短 retry + 超限升 human。适用于 LLM
// 不可用或返回无效 JSON 时,不让调用方拿到空 recovery。
func fallbackRecovery(sig AnomalyTemplateSignature) []AnomalyTemplateRecoveryAction {
	base := []AnomalyTemplateRecoveryAction{
		{Kind: "retry", MaxRetries: 2, BackoffMS: 1500, Reason: "generic transient retry"},
	}
	// captcha / session_expired 类优先人工
	switch strings.ToLower(sig.Type) {
	case "captcha", "session_expired":
		return []AnomalyTemplateRecoveryAction{
			{Kind: "human_intervention", Reason: "auth/captcha class default escalation"},
		}
	}
	return append(base, AnomalyTemplateRecoveryAction{
		Kind: "human_intervention", Reason: "retry exhausted",
	})
}

// anomalyFixSystemPrompt 规定输出 JSON schema 和执行约束。故意短 —— 真正的
// 信息在 user prompt 的 fixCtx 里。
const anomalyFixSystemPrompt = `You are an anomaly recovery planner for a browser automation agent.
Given the anomaly + recent steps + site profile, output a JSON with fields:
  recovery: [] of { kind, max_retries?, backoff_ms?, fallback_id?, steps?, reason }
  rationale: short free text explanation

"kind" MUST be one of: retry, fallback_pattern, human_intervention, custom_steps.
Be conservative; prefer retry for transient network, human_intervention for auth
walls, custom_steps for deterministic UI remediations (close dialog, reopen menu).
Reply with ONLY JSON, no prose outside.`

func buildAnomalyFixPrompt(ctx AnomalyFixContext) string {
	raw, _ := json.MarshalIndent(ctx, "", "  ")
	return "Anomaly context:\n" + string(raw)
}

// parseLLMRecovery 解析 LLM 返回。它的 top-level schema 为
//   { "recovery":[...], "rationale":"..." }
// 返回非空 recovery / rationale。
func parseLLMRecovery(raw string) ([]AnomalyTemplateRecoveryAction, string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil, "", fmt.Errorf("empty response")
	}
	// LLM 偶尔会用 ``` 包围,剥掉
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)

	var out struct {
		Recovery  []AnomalyTemplateRecoveryAction `json:"recovery"`
		Rationale string                          `json:"rationale"`
	}
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil, "", fmt.Errorf("json unmarshal: %w", err)
	}
	// 过滤非法 kind
	validKind := map[string]bool{
		"retry": true, "fallback_pattern": true,
		"human_intervention": true, "custom_steps": true,
	}
	clean := make([]AnomalyTemplateRecoveryAction, 0, len(out.Recovery))
	for _, a := range out.Recovery {
		if validKind[a.Kind] {
			clean = append(clean, a)
		}
	}
	if len(clean) == 0 {
		return nil, "", fmt.Errorf("no valid recovery actions")
	}
	return clean, out.Rationale, nil
}

// tailRecorderActions 从当前 ctx 绑定的 SequenceRecorder 取最后 N 条动作,
// Recorder 未绑或动作不足时返回空切片。倒序 —— 最新在前。
// 直接访问包内 ctxRecorders(没有 exported 读取 API);加同一把 recorderMu 保证
// 安全并发。
func tailRecorderActions(ctx context.Context, n int) []RecordedAction {
	if ctx == nil || n <= 0 {
		return nil
	}
	recorderMu.Lock()
	rec := ctxRecorders[ctx]
	recorderMu.Unlock()
	if rec == nil {
		return nil
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.actions) == 0 {
		return nil
	}
	if n > len(rec.actions) {
		n = len(rec.actions)
	}
	tail := rec.actions[len(rec.actions)-n:]
	rev := make([]RecordedAction, n)
	for i := 0; i < n; i++ {
		rev[i] = tail[n-1-i]
	}
	return rev
}
