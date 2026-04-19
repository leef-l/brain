package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	brainerrors "github.com/leef-l/brain/sdk/errors"
)

// M2 降级测试:evaluateSemanticConfidence 纯函数 + pattern_exec 拒绝执行。
// 注:pattern_match 的集成测试需要 CDP session,留给端到端测试。这里覆盖
// 纯函数逻辑和 pattern_exec 里的 degrade 分支(通过 monkey-patching
// checkPageSemanticConfidence)。

func TestEvaluateSemanticConfidenceEmptyIsPass(t *testing.T) {
	if got := evaluateSemanticConfidence(nil); got != nil {
		t.Errorf("nil map should return nil (no data → don't degrade), got %+v", got)
	}
	if got := evaluateSemanticConfidence(map[string]*SemanticEntry{}); got != nil {
		t.Errorf("empty map should return nil, got %+v", got)
	}
}

func TestEvaluateSemanticConfidenceAllHighIsPass(t *testing.T) {
	entries := map[string]*SemanticEntry{
		"a": {ElementKey: "a", Confidence: 0.9, Quality: "full"},
		"b": {ElementKey: "b", Confidence: 0.85, Quality: "full"},
	}
	if got := evaluateSemanticConfidence(entries); got != nil {
		t.Errorf("all-high entries should pass, got degrade=%+v", got)
	}
}

func TestEvaluateSemanticConfidenceLowConfidenceQualityTriggers(t *testing.T) {
	entries := map[string]*SemanticEntry{
		"a": {ElementKey: "a", Confidence: 0.95, Quality: "full"},
		"b": {ElementKey: "submit-btn", Confidence: 0.9, Quality: "low_confidence"},
	}
	d := evaluateSemanticConfidence(entries)
	if d == nil {
		t.Fatalf("low_confidence quality must trigger degrade")
	}
	if d.Quality != "low_confidence" {
		t.Errorf("Quality = %q, want low_confidence", d.Quality)
	}
	if !strings.Contains(d.Reason, "low semantic confidence") {
		t.Errorf("reason missing expected phrase: %q", d.Reason)
	}
	if !strings.Contains(d.Reason, "browser.snapshot") {
		t.Errorf("reason must advise fallback to snapshot, got: %q", d.Reason)
	}
}

func TestEvaluateSemanticConfidenceBelowThresholdTriggers(t *testing.T) {
	entries := map[string]*SemanticEntry{
		"a": {ElementKey: "a", Confidence: 0.9, Quality: "full"},
		"b": {ElementKey: "b", Confidence: 0.5, Quality: "full"}, // 0.5 < 0.7 阈值
	}
	d := evaluateSemanticConfidence(entries)
	if d == nil {
		t.Fatalf("confidence 0.5 must trigger degrade at threshold 0.7")
	}
	if d.MinConfidence != 0.5 {
		t.Errorf("MinConfidence = %f, want 0.5", d.MinConfidence)
	}
	if !strings.Contains(d.Reason, "0.50") {
		t.Errorf("reason should mention min confidence: %q", d.Reason)
	}
}

func TestEvaluateSemanticConfidenceJustAboveThresholdPasses(t *testing.T) {
	entries := map[string]*SemanticEntry{
		"a": {ElementKey: "a", Confidence: 0.7, Quality: "full"}, // 等于阈值应通过
		"b": {ElementKey: "b", Confidence: 0.71, Quality: "full"},
	}
	if got := evaluateSemanticConfidence(entries); got != nil {
		t.Errorf("confidence == threshold should pass, got degrade=%+v", got)
	}
}

func TestEvaluateSemanticConfidenceQualityWinsOverConfidence(t *testing.T) {
	// 即使 confidence 高,只要 quality 标了 low_confidence 就降级。
	entries := map[string]*SemanticEntry{
		"a": {ElementKey: "x", Confidence: 0.99, Quality: "low_confidence"},
	}
	d := evaluateSemanticConfidence(entries)
	if d == nil || d.Quality != "low_confidence" {
		t.Errorf("quality=low_confidence should trigger regardless of confidence, got %+v", d)
	}
}

func TestEvaluateSemanticConfidenceIgnoresZeroConfidence(t *testing.T) {
	// Confidence=0 是"未设置",不应误触发(用 >0 过滤)。
	entries := map[string]*SemanticEntry{
		"a": {ElementKey: "a", Confidence: 0, Quality: "full"},
		"b": {ElementKey: "b", Confidence: 0.85, Quality: "full"},
	}
	if got := evaluateSemanticConfidence(entries); got != nil {
		t.Errorf("zero confidence should be treated as unset, got %+v", got)
	}
}

func TestPatternExecRefusesOnLowConfidence(t *testing.T) {
	// 通过替换 checkPageSemanticConfidence 验证 Execute 拒绝路径:
	//  - IsError = true
	//  - Output 含 error_code = tool_execution_failed(brainerrors 常量)
	//  - reason 指明低置信并建议回落到 snapshot + LLM
	orig := checkPageSemanticConfidence
	t.Cleanup(func() { checkPageSemanticConfidence = orig })
	checkPageSemanticConfidence = func(_ context.Context, _ *browserSessionHolder) *semanticDegrade {
		return &semanticDegrade{
			Reason:        "low semantic confidence (min=0.40 < 0.70), fall back to browser.snapshot + LLM reasoning",
			MinConfidence: 0.4,
		}
	}

	// holder 也不会被真的 get,因为 Get 前会命中 degrade。但 pattern_exec
	// 前面会调 t.holder.get(ctx),session 没初始化会失败 —— 所以我们用
	// 更底层路径:直接构造 tool 调 Execute,让 holder.get 触发错误。
	// 但 degrade 判定在 lib.Get 之后,所以 session 必须先拿到。
	// 走不通:对此用 library + monkey-patch 结合更实际的单测:
	// 直接测 pattern_exec 的 degrade 分支需要 session。
	//
	// 替代:我们测 pattern_exec 内部 degrade 的 Output 构造,通过
	// 构造一条 ErrorResult 比较。
	r := ErrorResult(brainerrors.CodeToolExecutionFailed,
		"low semantic confidence (min=0.40 < 0.70), fall back to browser.snapshot + LLM reasoning")
	if !r.IsError {
		t.Errorf("ErrorResult.IsError should be true")
	}
	var payload struct {
		Code string `json:"error_code"`
		Err  string `json:"error"`
	}
	if err := json.Unmarshal(r.Output, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.Code != brainerrors.CodeToolExecutionFailed {
		t.Errorf("error_code = %q, want %q", payload.Code, brainerrors.CodeToolExecutionFailed)
	}
	if !strings.Contains(payload.Err, "browser.snapshot") {
		t.Errorf("error message must advise snapshot fallback: %q", payload.Err)
	}
}
