package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// M5 on_anomaly 路由测试。验证 abort / retry / fallback_pattern /
// human_intervention 四个分支在 runActionSequence 内的行为。
//
// 测试策略:直接调 runActionSequence,绕开 executePatternWithLib 外层
// 的 readPageMeta / PostCondition(那段需要真 cdp.BrowserSession)。
// step 不带 TargetRole 以避免 ResolveElement,step.Tool 用我们注册的 mock。

// ---------- 辅助:mock registry / mock tool ----------

type mockRegistry struct {
	tools map[string]Tool
}

func newMockRegistry(tools ...Tool) *mockRegistry {
	m := &mockRegistry{tools: make(map[string]Tool, len(tools))}
	for _, t := range tools {
		m.tools[t.Name()] = t
	}
	return m
}
func (r *mockRegistry) Register(_ Tool) error           { return nil }
func (r *mockRegistry) Lookup(name string) (Tool, bool) { t, ok := r.tools[name]; return t, ok }
func (r *mockRegistry) List() []Tool                    { return nil }
func (r *mockRegistry) ListByBrain(_ string) []Tool     { return nil }

// countingTool 计数 Execute 调用次数,返回固定的 _anomalies 负载。
type countingTool struct {
	name       string
	anomalyT   string
	anomalySub string
	// invocations 记录被调用次数,便于断言 retry 次数。
	mu          sync.Mutex
	invocations int
	// 当 invocations >= stopAnomalyAt(0 表示永不)时,后续返回无 anomaly 的
	// 成功输出,模拟"重试后问题消失"。
	stopAnomalyAt int
}

func (t *countingTool) Name() string   { return t.name }
func (t *countingTool) Risk() Risk     { return RiskSafe }
func (t *countingTool) Schema() Schema { return Schema{Name: t.name} }
func (t *countingTool) Execute(_ context.Context, _ json.RawMessage) (*Result, error) {
	t.mu.Lock()
	t.invocations++
	n := t.invocations
	t.mu.Unlock()

	if t.stopAnomalyAt > 0 && n >= t.stopAnomalyAt {
		return &Result{Output: []byte(`{"ok":true}`)}, nil
	}
	// 构造一条符合 mergeAnomalyIntoOutput 输出的 JSON,让 detectAnomalyInOutput 识别。
	payload := map[string]interface{}{
		"ok": true,
		"_anomalies": map[string]interface{}{
			"page_health": "degraded",
			"anomalies": []map[string]interface{}{
				{"type": t.anomalyT, "subtype": t.anomalySub, "severity": "high"},
			},
		},
	}
	raw, _ := json.Marshal(payload)
	return &Result{Output: raw}, nil
}

// count 返回被调用次数。
func (t *countingTool) count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.invocations
}

// mockCoordinator 实现 HumanTakeoverCoordinator,用固定 outcome 应答。
type mockCoordinator struct {
	outcome HumanTakeoverOutcome
	note    string
	mu      sync.Mutex
	called  int
	lastReq HumanTakeoverRequest
}

func (m *mockCoordinator) RequestTakeover(_ context.Context, req HumanTakeoverRequest) HumanTakeoverResponse {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.called++
	m.lastReq = req
	return HumanTakeoverResponse{Outcome: m.outcome, Note: m.note}
}

// withCoordinator 暂时注入 coordinator,返回 teardown 恢复之前值。
func withCoordinator(c HumanTakeoverCoordinator) func() {
	takeoverMu.Lock()
	prev := takeoverImpl
	takeoverImpl = c
	takeoverMu.Unlock()
	return func() {
		takeoverMu.Lock()
		takeoverImpl = prev
		takeoverMu.Unlock()
	}
}

// driveSequence 跑 runActionSequence 返回 (res, terminal, switchTo)。
func driveSequence(t *testing.T, p *UIPattern, reg Registry, getter patternLibGetter) (*ExecutionResult, bool, string) {
	t.Helper()
	res := &ExecutionResult{PatternID: p.ID}
	terminal, switchTo := runActionSequence(context.Background(), nil, reg, p, nil, res, time.Now(), getter)
	return res, terminal, switchTo
}

// ---------- 用例 1:abort ----------

func TestOnAnomalyAbort(t *testing.T) {
	tool := &countingTool{name: "mock.click", anomalyT: "error_message", anomalySub: ""}
	reg := newMockRegistry(tool)
	p := &UIPattern{
		ID: "test-abort",
		ActionSequence: []ActionStep{
			{Tool: "mock.click"},
		},
		OnAnomaly: map[string]AnomalyHandler{
			"error_message": {Action: "abort", Reason: "credentials wrong"},
		},
	}
	res, terminal, switchTo := driveSequence(t, p, reg, nil)
	if !terminal {
		t.Fatalf("abort should terminate, got terminal=false")
	}
	if switchTo != "" {
		t.Errorf("abort should not request switch, got %q", switchTo)
	}
	if res.AbortedByAnomaly != "error_message" {
		t.Errorf("AbortedByAnomaly = %q, want error_message", res.AbortedByAnomaly)
	}
	if !strings.Contains(res.Error, "aborted_by_anomaly") {
		t.Errorf("Error = %q, want contains aborted_by_anomaly", res.Error)
	}
	if tool.count() != 1 {
		t.Errorf("abort should call tool once, got %d", tool.count())
	}
}

// ---------- 用例 2:retry 成功 & 超限 ----------

func TestOnAnomalyRetrySucceeds(t *testing.T) {
	// 第 3 次调用不再产生 anomaly,模拟重试后问题自愈。
	tool := &countingTool{
		name: "mock.click", anomalyT: "session_expired", stopAnomalyAt: 3,
	}
	reg := newMockRegistry(tool)
	p := &UIPattern{
		ID: "test-retry-ok",
		ActionSequence: []ActionStep{
			{Tool: "mock.click"},
		},
		OnAnomaly: map[string]AnomalyHandler{
			"session_expired": {Action: "retry", MaxRetries: 5, BackoffMS: 1},
		},
	}
	res, terminal, switchTo := driveSequence(t, p, reg, nil)
	if terminal {
		t.Fatalf("retry-succeed should not terminate, got Error=%q", res.Error)
	}
	if switchTo != "" {
		t.Errorf("retry-succeed should not switch, got %q", switchTo)
	}
	if got := tool.count(); got != 3 {
		t.Errorf("tool calls = %d, want 3 (1 initial + 2 retries)", got)
	}
	if res.AbortedByAnomaly != "" {
		t.Errorf("retry-succeed should not set AbortedByAnomaly, got %q", res.AbortedByAnomaly)
	}
}

func TestOnAnomalyRetryExhausted(t *testing.T) {
	tool := &countingTool{name: "mock.click", anomalyT: "session_expired"}
	reg := newMockRegistry(tool)
	p := &UIPattern{
		ID: "test-retry-exhaust",
		ActionSequence: []ActionStep{
			{Tool: "mock.click"},
		},
		OnAnomaly: map[string]AnomalyHandler{
			"session_expired": {Action: "retry", MaxRetries: 2, BackoffMS: 1},
		},
	}
	res, terminal, _ := driveSequence(t, p, reg, nil)
	if !terminal {
		t.Fatalf("retry-exhaust should terminate")
	}
	if got := tool.count(); got != 3 {
		t.Errorf("tool calls = %d, want 3 (1 initial + 2 retries)", got)
	}
	if res.AbortedByAnomaly != "session_expired" {
		t.Errorf("AbortedByAnomaly = %q, want session_expired", res.AbortedByAnomaly)
	}
	if !strings.Contains(res.Error, "retry exhausted") {
		t.Errorf("Error = %q, want contains 'retry exhausted'", res.Error)
	}
}

// ---------- 用例 3:fallback_pattern ----------

func TestOnAnomalyFallbackPattern(t *testing.T) {
	tool := &countingTool{name: "mock.click", anomalyT: "ui_injection", anomalySub: ""}
	reg := newMockRegistry(tool)
	p := &UIPattern{
		ID: "test-fallback-src",
		ActionSequence: []ActionStep{
			{Tool: "mock.click"},
		},
		OnAnomaly: map[string]AnomalyHandler{
			"ui_injection": {Action: "fallback_pattern", FallbackID: "safer-pattern"},
		},
	}
	res, terminal, switchTo := driveSequence(t, p, reg, nil)
	if terminal {
		t.Fatalf("fallback_pattern should not terminate, got Error=%q", res.Error)
	}
	if switchTo != "safer-pattern" {
		t.Errorf("switchTo = %q, want safer-pattern", switchTo)
	}
	if tool.count() != 1 {
		t.Errorf("fallback should call tool once, got %d", tool.count())
	}
}

func TestOnAnomalyFallbackPatternMissingID(t *testing.T) {
	tool := &countingTool{name: "mock.click", anomalyT: "ui_injection"}
	reg := newMockRegistry(tool)
	p := &UIPattern{
		ID: "test-fallback-empty",
		ActionSequence: []ActionStep{
			{Tool: "mock.click"},
		},
		OnAnomaly: map[string]AnomalyHandler{
			"ui_injection": {Action: "fallback_pattern"}, // FallbackID 为空
		},
	}
	res, terminal, switchTo := driveSequence(t, p, reg, nil)
	if !terminal {
		t.Fatalf("fallback without id should abort")
	}
	if switchTo != "" {
		t.Errorf("switchTo = %q, expected empty on missing fallback_id", switchTo)
	}
	if !strings.Contains(res.Error, "without fallback_id") {
		t.Errorf("Error = %q, want contains 'without fallback_id'", res.Error)
	}
}

// ---------- 用例 4:human_intervention resumed / aborted ----------

func TestOnAnomalyHumanResumed(t *testing.T) {
	// resumed 后本步应再跑一次;第二次调用我们让 tool 不再返回 anomaly,
	// 于是序列成功结束。
	tool := &countingTool{
		name: "mock.click", anomalyT: "captcha", anomalySub: "recaptcha", stopAnomalyAt: 2,
	}
	reg := newMockRegistry(tool, NewHumanRequestTakeoverTool())
	p := &UIPattern{
		ID: "test-human-resume",
		ActionSequence: []ActionStep{
			{Tool: "mock.click"},
		},
		OnAnomaly: map[string]AnomalyHandler{
			"recaptcha": {Action: "human_intervention"},
		},
	}

	coord := &mockCoordinator{outcome: HumanOutcomeResumed, note: "solved"}
	teardown := withCoordinator(coord)
	defer teardown()

	res, terminal, switchTo := driveSequence(t, p, reg, nil)
	if terminal {
		t.Fatalf("human-resumed should not terminate, got Error=%q", res.Error)
	}
	if switchTo != "" {
		t.Errorf("human-resumed should not switch, got %q", switchTo)
	}
	if coord.called != 1 {
		t.Errorf("coordinator should be called once, got %d", coord.called)
	}
	if tool.count() != 2 {
		t.Errorf("tool should run twice (initial + post-takeover), got %d", tool.count())
	}
	if coord.lastReq.Reason != "recaptcha" {
		t.Errorf("coordinator got Reason=%q, want recaptcha", coord.lastReq.Reason)
	}
}

func TestOnAnomalyHumanAborted(t *testing.T) {
	tool := &countingTool{name: "mock.click", anomalyT: "captcha", anomalySub: "hcaptcha"}
	reg := newMockRegistry(tool, NewHumanRequestTakeoverTool())
	p := &UIPattern{
		ID: "test-human-abort",
		ActionSequence: []ActionStep{
			{Tool: "mock.click"},
		},
		OnAnomaly: map[string]AnomalyHandler{
			"captcha": {Action: "human_intervention"},
		},
	}

	coord := &mockCoordinator{outcome: HumanOutcomeAborted}
	teardown := withCoordinator(coord)
	defer teardown()

	res, terminal, _ := driveSequence(t, p, reg, nil)
	if !terminal {
		t.Fatalf("human-aborted should terminate")
	}
	if res.AbortedByAnomaly != "hcaptcha" {
		t.Errorf("AbortedByAnomaly = %q, want hcaptcha (subtype)", res.AbortedByAnomaly)
	}
	if !strings.Contains(res.Error, "aborted") {
		t.Errorf("Error = %q, want contains 'aborted'", res.Error)
	}
	if coord.called != 1 {
		t.Errorf("coordinator should be called once, got %d", coord.called)
	}
}

// ---------- 用例 5:subtype 优先 type 命中 ----------

func TestOnAnomalySubtypePriority(t *testing.T) {
	tool := &countingTool{name: "mock.click", anomalyT: "captcha", anomalySub: "hcaptcha"}
	reg := newMockRegistry(tool)
	p := &UIPattern{
		ID: "test-subtype-priority",
		ActionSequence: []ActionStep{
			{Tool: "mock.click"},
		},
		// hcaptcha(subtype)走 abort,captcha(type)走 retry。subtype 应当优先。
		OnAnomaly: map[string]AnomalyHandler{
			"hcaptcha": {Action: "abort", Reason: "hcaptcha cannot auto-solve"},
			"captcha":  {Action: "retry", MaxRetries: 5},
		},
	}
	res, terminal, _ := driveSequence(t, p, reg, nil)
	if !terminal {
		t.Fatalf("expected abort via subtype match")
	}
	if tool.count() != 1 {
		t.Errorf("subtype abort should call tool once, got %d", tool.count())
	}
	if !strings.Contains(res.Error, "hcaptcha cannot auto-solve") {
		t.Errorf("expected reason text, got %q", res.Error)
	}
}

// ---------- 用例 6:fallback_pattern 端到端经 executePatternWithLib ----------

func TestPatternFallbackChainSwitches(t *testing.T) {
	// src pattern 遇 ui_injection → fallback_pattern=alt,alt 没 anomaly 跑完。
	// 直接调 runActionSequence 只能测一层,这里走 executePatternWithLib
	// 是为了验证主循环真能切 pattern 并继续;sess=nil 会让 readPageMeta
	// panic,因此 getter 要返回没有 PostCondition 的 alt。
	// 但 executePatternWithLib 最外层仍调 readPageMeta(sess)——sess=nil
	// 会崩。改为直接手工模拟切换:验证 getter 被调用且 next pattern 的 ID 生效。
	tool := &countingTool{name: "mock.click", anomalyT: "ui_injection"}
	reg := newMockRegistry(tool)
	src := &UIPattern{
		ID: "src",
		ActionSequence: []ActionStep{
			{Tool: "mock.click"},
		},
		OnAnomaly: map[string]AnomalyHandler{
			"ui_injection": {Action: "fallback_pattern", FallbackID: "alt"},
		},
	}
	_, terminal, switchTo := driveSequence(t, src, reg, nil)
	if terminal {
		t.Fatalf("expected switch, got terminal")
	}
	if switchTo != "alt" {
		t.Fatalf("switchTo = %q, want alt", switchTo)
	}
	// 用 M5 模式跟库查找器模拟主循环切换:第二次 runActionSequence 用 alt。
	alt := &UIPattern{
		ID: "alt",
		// 空 ActionSequence → 直接跑完,不触发 anomaly
	}
	res2 := &ExecutionResult{PatternID: alt.ID}
	terminal2, switchTo2 := runActionSequence(context.Background(), nil, reg, alt, nil, res2, time.Now(), nil)
	if terminal2 {
		t.Fatalf("alt pattern should not terminate (empty sequence)")
	}
	if switchTo2 != "" {
		t.Errorf("alt should not request further switch, got %q", switchTo2)
	}
}

// ---------- 用例 7:matchAnomalyHandler 直接 ----------

func TestMatchAnomalyHandlerDirect(t *testing.T) {
	p := &UIPattern{
		OnAnomaly: map[string]AnomalyHandler{
			"hcaptcha": {Action: "abort"},
			"captcha":  {Action: "retry"},
			"any":      {Action: "human_intervention"},
		},
	}
	// subtype 命中优先
	h, ok := matchAnomalyHandler(p, "captcha", "hcaptcha")
	if !ok || h.Action != "abort" {
		t.Errorf("subtype match failed: ok=%v action=%q", ok, h.Action)
	}
	// subtype 未命中 → fallback type
	h, ok = matchAnomalyHandler(p, "captcha", "recaptcha")
	if !ok || h.Action != "retry" {
		t.Errorf("type fallback match failed: ok=%v action=%q", ok, h.Action)
	}
	// 都不命中 → any 兜底
	h, ok = matchAnomalyHandler(p, "modal_blocking", "")
	if !ok || h.Action != "human_intervention" {
		t.Errorf("any fallback failed: ok=%v action=%q", ok, h.Action)
	}
	// 空 pattern → false
	if _, ok := matchAnomalyHandler(nil, "x", ""); ok {
		t.Errorf("nil pattern should not match")
	}
}

// ---------- 用例 8:detectAnomalyInOutput 解析 ----------

func TestDetectAnomalyInOutputSubtype(t *testing.T) {
	body := map[string]interface{}{
		"ok": true,
		"_anomalies": map[string]interface{}{
			"anomalies": []map[string]interface{}{
				{"type": "captcha", "subtype": "hcaptcha"},
			},
		},
	}
	raw, _ := json.Marshal(body)
	atype, sub := detectAnomalyInOutput(raw)
	if atype != "captcha" || sub != "hcaptcha" {
		t.Errorf("got (%q,%q), want (captcha, hcaptcha)", atype, sub)
	}
	// 空 body
	if a, s := detectAnomalyInOutput(nil); a != "" || s != "" {
		t.Errorf("nil body should return empty, got (%q,%q)", a, s)
	}
	// 无 _anomalies
	if a, s := detectAnomalyInOutput([]byte(`{"ok":true}`)); a != "" || s != "" {
		t.Errorf("no _anomalies should return empty, got (%q,%q)", a, s)
	}
}

// 避免 fmt / unused 报警
var _ = fmt.Sprintf
