package errors

// errors_test.go — BrainKernel v1 错误模型合规测试
//
// 覆盖 21-错误模型.md §14 合规性测试矩阵 C-E-01 ~ C-E-20（按任务要求重映射到实际 API）。
// 只使用 Go 标准库，无第三方依赖。
// 测试函数名格式: TestC_E_<NN>_<简述>

import (
	"context"
	stderrors "errors"
	"encoding/json"
	"io"
	"math/rand"
	"strings"
	"syscall"
	"testing"
	"time"
)

// ──────────────────────────────────────────────────────────────────────────────
// 辅助
// ──────────────────────────────────────────────────────────────────────────────

// fixedRand 返回一个用固定种子初始化的 rand，使退避测试确定性。
func fixedRand() *rand.Rand {
	return rand.New(rand.NewSource(42))
}

// fixedTime 是一个测试用固定时间戳。
var fixedTime = time.Date(2026, 4, 11, 12, 0, 0, 0, time.UTC)

// assertf 失败时终止当前测试并打印消息。
func assertf(t *testing.T, cond bool, format string, args ...interface{}) {
	t.Helper()
	if !cond {
		t.Errorf(format, args...)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// C-E-01: 保留 code 注册表完整性
// 每个 ReservedCodes() 条目都能在注册表中找到，且 Class/Retryable 完全匹配。
// ──────────────────────────────────────────────────────────────────────────────

func TestC_E_01_ReservedCodesRegistryIntegrity(t *testing.T) {
	reserved := ReservedCodes()
	if len(reserved) == 0 {
		t.Fatal("ReservedCodes() returned empty slice")
	}

	for _, want := range reserved {
		got, ok := Lookup(want.Code)
		if !ok {
			t.Errorf("code %q missing from registry", want.Code)
			continue
		}
		if got.Class != want.Class {
			t.Errorf("code %q: Class got=%q want=%q", want.Code, got.Class, want.Class)
		}
		if got.Retryable != want.Retryable {
			t.Errorf("code %q: Retryable got=%v want=%v", want.Code, got.Retryable, want.Retryable)
		}
	}

	// 注册表条目数 ≥ 保留清单条目数
	if DefaultRegistry.Len() < len(reserved) {
		t.Errorf("registry len %d < reserved len %d", DefaultRegistry.Len(), len(reserved))
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// C-E-02: New / Wrap 构造的 BrainError 基本字段合规
// ──────────────────────────────────────────────────────────────────────────────

func TestC_E_02_NewWrapBasicFields(t *testing.T) {
	// WithOccurredAt 在 New 内部被 now() 覆盖，所以正确做法是替换 now 变量
	ts := fixedTime
	origNow := now
	now = func() time.Time { return ts }
	defer func() { now = origNow }()

	// New 测试
	e := New(CodeSidecarHung,
		WithMessage("sidecar missed heartbeat"),
	)
	assertf(t, e != nil, "New returned nil")
	assertf(t, e.Fingerprint != "", "Fingerprint must be non-empty")
	assertf(t, e.OccurredAt.Equal(ts), "OccurredAt mismatch: got %v", e.OccurredAt)
	assertf(t, e.Message == "sidecar missed heartbeat", "Message mismatch: %q", e.Message)
	assertf(t, e.ErrorCode == CodeSidecarHung, "ErrorCode mismatch: %q", e.ErrorCode)
	assertf(t, e.Class == ClassTransient, "Class mismatch: %q", e.Class)
	assertf(t, e.Retryable == true, "Retryable must be true for Transient")

	// 验证 code 存在于注册表
	_, ok := Lookup(e.ErrorCode)
	assertf(t, ok, "ErrorCode %q not in registry", e.ErrorCode)

	// Wrap 测试：BrainError 作为 cause
	outer := Wrap(e, CodeBrainTaskFailed,
		WithMessage("task wrapper"),
		WithOccurredAt(ts),
	)
	assertf(t, outer != nil, "Wrap returned nil")
	assertf(t, outer.Fingerprint != "", "outer Fingerprint must be non-empty")
	assertf(t, outer.Cause != nil, "Wrap must set Cause")
	assertf(t, outer.Cause.ErrorCode == CodeSidecarHung, "Cause.ErrorCode mismatch")
}

// ──────────────────────────────────────────────────────────────────────────────
// C-E-03: Class + Retryable 不变量检查
// Transient + Retryable=false → Decide 返回 Violation=true
// ──────────────────────────────────────────────────────────────────────────────

func TestC_E_03_ClassRetryableInvariant(t *testing.T) {
	// 正常：Transient + Retryable=true → 无 Violation
	good := New(CodeDeadlineExceeded, WithOccurredAt(fixedTime))
	assertf(t, good.Class == ClassTransient, "expected Transient")
	assertf(t, good.Retryable == true, "expected Retryable=true")

	dctx := DecideContext{
		FaultPolicy: FaultPolicyFailFast,
		Health:      HealthHealthy,
		Attempt:     0,
		Rand:        fixedRand(),
	}
	d := Decide(good, dctx)
	assertf(t, !d.Violation, "good Transient should not produce Violation")
	assertf(t, d.Action == ActionRetry, "good Transient attempt=0 should Retry")

	// 违规：Transient + Retryable=false（WithRetryable 覆盖）
	bad := New(CodeDeadlineExceeded,
		WithRetryable(false),
		WithOccurredAt(fixedTime),
	)
	assertf(t, bad.Class == ClassTransient, "still Transient")
	assertf(t, bad.Retryable == false, "Retryable overridden to false")

	dBad := Decide(bad, dctx)
	assertf(t, dBad.Violation, "Transient+Retryable=false must produce Violation")
	assertf(t, dBad.Action == ActionFail, "Violation row must Fail")
}

// ──────────────────────────────────────────────────────────────────────────────
// C-E-04: 17 行决策矩阵（附录 B 逐行验证）
// ──────────────────────────────────────────────────────────────────────────────

func TestC_E_04_DecideMatrix(t *testing.T) {
	type row struct {
		name          string
		code          string
		retryable     bool
		faultPolicy   FaultPolicy
		health        Health
		attempt       int
		wantAction    Action
		wantBackoff   bool // BackoffHint > 0
		wantViolation bool
	}

	// 用同一个固定 rand 生成器——每行都重新建，确保独立
	mkErr := func(code string, retryable bool) *BrainError {
		if retryable {
			return New(code, WithOccurredAt(fixedTime))
		}
		return New(code, WithRetryable(false), WithOccurredAt(fixedTime))
	}

	rows := []row{
		// 1: Transient / retryable / healthy / attempt=0 → Retry, backoff>0
		{"row1_transient_healthy_attempt0", CodeSidecarHung, true, FaultPolicyFailFast, HealthHealthy, 0, ActionRetry, true, false},
		// 2: Transient / retryable / healthy / attempt=1 → Retry, backoff>0
		{"row2_transient_healthy_attempt1", CodeSidecarHung, true, FaultPolicyFailFast, HealthHealthy, 1, ActionRetry, true, false},
		// 3: Transient / retryable / healthy / attempt=2 → Retry, backoff>0
		{"row3_transient_healthy_attempt2", CodeSidecarHung, true, FaultPolicyFailFast, HealthHealthy, 2, ActionRetry, true, false},
		// 4: Transient / retryable / healthy / attempt≥3 → Fail
		{"row4_transient_healthy_attempt3", CodeSidecarHung, true, FaultPolicyFailFast, HealthHealthy, 3, ActionFail, false, false},
		// 5: Transient / retryable / degraded / attempt=0 → Retry, backoff>0
		{"row5_transient_degraded_attempt0", CodeSidecarHung, true, FaultPolicyFailFast, HealthDegraded, 0, ActionRetry, true, false},
		// 6: Transient / retryable / degraded / attempt≥1 → Fail
		{"row6_transient_degraded_attempt1", CodeSidecarHung, true, FaultPolicyFailFast, HealthDegraded, 1, ActionFail, false, false},
		// 7: Transient / retryable / quarantined / any → Fail
		{"row7_transient_quarantined", CodeSidecarHung, true, FaultPolicyFailFast, HealthQuarantined, 0, ActionFail, false, false},
		// 8: Transient + retryable=false → Fail + violation
		{"row8_transient_retryable_false", CodeDeadlineExceeded, false, FaultPolicyFailFast, HealthHealthy, 0, ActionFail, false, true},
		// 9: Permanent / fail_fast → Fail
		{"row9_permanent_failfast", CodeSidecarExitNonzero, false, FaultPolicyFailFast, HealthHealthy, 0, ActionFail, false, false},
		// 10: Permanent / best_effort → Fail
		{"row10_permanent_bestEffort", CodeSidecarExitNonzero, false, FaultPolicyBestEffort, HealthHealthy, 0, ActionFail, false, false},
		// 11: Permanent / retry policy → Fail
		{"row11_permanent_retry", CodeSidecarExitNonzero, false, FaultPolicyRetry, HealthHealthy, 0, ActionFail, false, false},
		// 12: UserFault → Fail
		{"row12_userfault", CodeToolInputInvalid, false, FaultPolicyFailFast, HealthHealthy, 0, ActionFail, false, false},
		// 13: QuotaExceeded → Fail (cooldown)
		{"row13_quota", CodeLLMQuotaExhaustedDaily, false, FaultPolicyFailFast, HealthHealthy, 0, ActionFail, false, false},
		// 14: SafetyRefused → AskHuman
		{"row14_safety", CodeLLMSafetyRefused, false, FaultPolicyFailFast, HealthHealthy, 0, ActionAskHuman, false, false},
		// 15: InternalBug / healthy → DegradeBrain
		{"row15_internalbug_healthy", CodePanicked, false, FaultPolicyFailFast, HealthHealthy, 0, ActionDegradeBrain, false, false},
		// 16: InternalBug / degraded → Quarantine
		{"row16_internalbug_degraded", CodePanicked, false, FaultPolicyFailFast, HealthDegraded, 0, ActionQuarantine, false, false},
		// 17: InternalBug / quarantined → Fail
		{"row17_internalbug_quarantined", CodePanicked, false, FaultPolicyFailFast, HealthQuarantined, 0, ActionFail, false, false},
	}

	for _, r := range rows {
		r := r
		t.Run(r.name, func(t *testing.T) {
			e := mkErr(r.code, r.retryable)
			dctx := DecideContext{
				FaultPolicy: r.faultPolicy,
				Health:      r.health,
				Attempt:     r.attempt,
				Rand:        fixedRand(),
			}
			d := Decide(e, dctx)

			if d.Action != r.wantAction {
				t.Errorf("Action: got=%q want=%q", d.Action, r.wantAction)
			}
			if r.wantBackoff && d.BackoffHint <= 0 {
				t.Errorf("BackoffHint expected >0, got %v", d.BackoffHint)
			}
			if !r.wantBackoff && d.BackoffHint != 0 {
				t.Errorf("BackoffHint expected 0, got %v", d.BackoffHint)
			}
			if d.Violation != r.wantViolation {
				t.Errorf("Violation: got=%v want=%v", d.Violation, r.wantViolation)
			}
		})
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// C-E-05: 半开态 — HealthQuarantined 下所有 class 的 Decide 行为
// ──────────────────────────────────────────────────────────────────────────────

func TestC_E_05_QuarantinedAlwaysFail(t *testing.T) {
	codes := []struct {
		code  string
		class ErrorClass
	}{
		{CodeSidecarHung, ClassTransient},        // row 7
		{CodePanicked, ClassInternalBug},          // row 17
	}

	for _, tc := range codes {
		e := New(tc.code, WithOccurredAt(fixedTime))
		d := Decide(e, DecideContext{
			FaultPolicy: FaultPolicyFailFast,
			Health:      HealthQuarantined,
			Attempt:     0,
			Rand:        fixedRand(),
		})
		if d.Action != ActionFail {
			t.Errorf("code=%q: quarantined should always Fail, got %q", tc.code, d.Action)
		}
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// C-E-06: Fingerprint 确定性
// 相同输入产生相同 fingerprint；仅 OccurredAt/Attempt/TraceID/SpanID/SidecarPID
// 不同的两个错误有相同 fingerprint。
// ──────────────────────────────────────────────────────────────────────────────

func TestC_E_06_FingerprintDeterminism(t *testing.T) {
	msg := "sidecar missed 3 heartbeats"

	e1 := New(CodeSidecarHung,
		WithMessage(msg),
		WithBrainID("central"),
		WithOccurredAt(fixedTime),
	)
	e2 := New(CodeSidecarHung,
		WithMessage(msg),
		WithBrainID("central"),
		WithOccurredAt(fixedTime.Add(time.Hour)), // 不同时间
	)
	if e1.Fingerprint != e2.Fingerprint {
		t.Errorf("same inputs different timestamps must share fingerprint: %q vs %q",
			e1.Fingerprint, e2.Fingerprint)
	}

	// 不同 Attempt / TraceID / SpanID / SidecarPID → 仍然相同 fingerprint
	e3 := New(CodeSidecarHung,
		WithMessage(msg),
		WithBrainID("central"),
		WithOccurredAt(fixedTime),
		WithAttempt(5),
		WithTraceID("trace-abc"),
		WithSpanID("span-123"),
		WithSidecarPID(9999),
	)
	if e1.Fingerprint != e3.Fingerprint {
		t.Errorf("volatile fields must not affect fingerprint: %q vs %q",
			e1.Fingerprint, e3.Fingerprint)
	}

	// 不同 BrainID → fingerprint 必须不同
	e4 := New(CodeSidecarHung,
		WithMessage(msg),
		WithBrainID("code-brain"),
		WithOccurredAt(fixedTime),
	)
	if e1.Fingerprint == e4.Fingerprint {
		t.Errorf("different BrainID must produce different fingerprint")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// C-E-07: NormalizeMessage 规范化 + 两个仅动态部分不同的错误 fingerprint 相同
// ──────────────────────────────────────────────────────────────────────────────

func TestC_E_07_FingerprintMessageNormalizer(t *testing.T) {
	type normCase struct {
		name  string
		input string
		want  string // 期望的规范化结果（子串匹配而非精确匹配）
	}

	cases := []normCase{
		{
			name:  "digits replaced",
			input: "retry attempt 42 of 100",
			want:  "retry attempt <N> of <N>",
		},
		{
			name:  "UUID replaced",
			input: "task 550e8400-e29b-41d4-a716-446655440000 failed",
			want:  "task <ID> failed",
		},
		{
			name:  "ISO timestamp replaced",
			input: "error at 2026-04-11T12:00:00Z",
			want:  "error at <TIME>",
		},
		{
			name:  "path replaced",
			input: "file /var/log/brain.log not found",
			want:  "file <PATH> not found",
		},
		{
			name:  "hex blob replaced",
			input: "hash deadbeefcafe1234 is invalid",
			want:  "hash <HEX> is invalid",
		},
		{
			name:  "quoted string replaced",
			input: `got "unexpected token" in stream`,
			want:  "got <STR> in stream",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := NormalizeMessage(tc.input)
			if got != tc.want {
				t.Errorf("NormalizeMessage(%q):\n  got  %q\n  want %q", tc.input, got, tc.want)
			}
		})
	}

	// 两个仅 UUID 不同的错误应该有相同 fingerprint
	msg1 := "job 550e8400-e29b-41d4-a716-446655440000 failed"
	msg2 := "job 6ba7b810-9dad-11d1-80b4-00c04fd430c8 failed"
	e1 := New(CodeBrainTaskFailed, WithMessage(msg1), WithOccurredAt(fixedTime))
	e2 := New(CodeBrainTaskFailed, WithMessage(msg2), WithOccurredAt(fixedTime))
	if e1.Fingerprint != e2.Fingerprint {
		t.Errorf("messages differing only in UUID must share fingerprint: %q vs %q",
			e1.Fingerprint, e2.Fingerprint)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// C-E-08: FromGoError 映射表
// ──────────────────────────────────────────────────────────────────────────────

func TestC_E_08_FromGoErrorMapping(t *testing.T) {
	type tc struct {
		name      string
		err       error
		wantCode  string
		wantMapped bool
	}

	cases := []tc{
		{"context.Canceled", context.Canceled, "", false},
		{"context.DeadlineExceeded", context.DeadlineExceeded, CodeDeadlineExceeded, true},
		{"io.EOF", io.EOF, CodeSidecarStdoutEOF, true},
		{"syscall.EPIPE", syscall.EPIPE, CodeSidecarStdinBrokenPipe, true},
		{"plain errors.New", stderrors.New("some error"), "", false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			code, mapped := FromGoError(tc.err)
			if mapped != tc.wantMapped {
				t.Errorf("mapped: got=%v want=%v", mapped, tc.wantMapped)
			}
			if code != tc.wantCode {
				t.Errorf("code: got=%q want=%q", code, tc.wantCode)
			}
		})
	}

	// nil 输入
	code, mapped := FromGoError(nil)
	if mapped || code != "" {
		t.Errorf("FromGoError(nil) should return ('', false), got (%q, %v)", code, mapped)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// C-E-09: MarshalJSON 绝不输出 InternalOnly（深层 Cause 链也不行）
// ──────────────────────────────────────────────────────────────────────────────

func TestC_E_09_MarshalJSONNoInternalOnly(t *testing.T) {
	inner := New(CodePanicked,
		WithMessage("kernel crashed"),
		WithStack("goroutine 1 [running]:\nmain.main()"),
		WithRawStderr("STDERR: fatal error output"),
		WithOccurredAt(fixedTime),
	)
	mid := Wrap(inner, CodeAssertionFailed,
		WithMessage("assertion wrapper layer"),
		WithStack("goroutine 2 [running]"),
		WithOccurredAt(fixedTime),
	)
	outer := Wrap(mid, CodeBrainTaskFailed,
		WithMessage("top level task wrap"),
		WithStack("goroutine 3 [running]"),
		WithOccurredAt(fixedTime),
	)

	b, err := json.Marshal(outer)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}
	jsonStr := string(b)

	// 禁止出现 InternalOnly 字段的 JSON key（不是 message 里的普通文字）
	// 使用 JSON key 格式 "key": 来精确匹配，避免与消息文字误匹配
	forbiddenKeys := []string{`"stack"`, `"raw_stderr"`, `"internal_only"`, `"DebugFlags"`, `"InternalOnly"`}
	for _, kw := range forbiddenKeys {
		if strings.Contains(jsonStr, kw) {
			t.Errorf("JSON output must not contain key %s; got: %s", kw, jsonStr)
		}
	}

	// 基本字段必须存在
	required := []string{"error_code", "class", "retryable", "fingerprint"}
	for _, kw := range required {
		if !strings.Contains(jsonStr, kw) {
			t.Errorf("JSON output must contain %q; got: %s", kw, jsonStr)
		}
	}

	// 验证 InternalOnly 在 outer 上确实存在（说明我们真的在测试 InternalOnly 的脱敏）
	if outer.InternalOnly == nil || outer.InternalOnly.Stack == "" {
		t.Error("test setup: outer.InternalOnly.Stack should be populated before marshaling")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// C-E-10: SanitizeForWire 递归清除 TraceID/SpanID/InternalOnly
// ──────────────────────────────────────────────────────────────────────────────

func TestC_E_10_SanitizeForWireRecursive(t *testing.T) {
	inner := New(CodeDeadlineExceeded,
		WithMessage("inner timeout"),
		WithTraceID("trace-inner"),
		WithSpanID("span-inner"),
		WithStack("inner stack"),
		WithOccurredAt(fixedTime),
	)
	outer := Wrap(inner, CodeBrainTaskFailed,
		WithMessage("outer wrap"),
		WithTraceID("trace-outer"),
		WithSpanID("span-outer"),
		WithStack("outer stack"),
		WithOccurredAt(fixedTime),
	)

	clean := SanitizeForWire(outer)

	if clean == nil {
		t.Fatal("SanitizeForWire returned nil")
	}

	// 顶层清除
	if clean.TraceID != "" {
		t.Errorf("top-level TraceID must be stripped, got %q", clean.TraceID)
	}
	if clean.SpanID != "" {
		t.Errorf("top-level SpanID must be stripped, got %q", clean.SpanID)
	}
	if clean.InternalOnly != nil {
		t.Error("top-level InternalOnly must be stripped")
	}

	// Cause 链递归清除
	if clean.Cause == nil {
		t.Fatal("Cause must be preserved after sanitize")
	}
	if clean.Cause.TraceID != "" {
		t.Errorf("cause TraceID must be stripped, got %q", clean.Cause.TraceID)
	}
	if clean.Cause.SpanID != "" {
		t.Errorf("cause SpanID must be stripped, got %q", clean.Cause.SpanID)
	}
	if clean.Cause.InternalOnly != nil {
		t.Error("cause InternalOnly must be stripped")
	}

	// 原始对象不应被修改
	if outer.TraceID == "" {
		t.Error("original TraceID must not be mutated by SanitizeForWire")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// C-E-11: errors.Is + errors.As 跨 Wrap 链工作
// ──────────────────────────────────────────────────────────────────────────────

func TestC_E_11_ErrorsIsAsAcrossWrapChain(t *testing.T) {
	inner := New(CodeSidecarHung, WithMessage("inner"), WithOccurredAt(fixedTime))
	mid := Wrap(inner, CodeBrainTaskFailed, WithMessage("mid"), WithOccurredAt(fixedTime))
	outer := Wrap(mid, CodeAssertionFailed, WithMessage("outer"), WithOccurredAt(fixedTime))

	// errors.Is — 按 ErrorCode 匹配
	target := New(CodeSidecarHung, WithOccurredAt(fixedTime))
	if !stderrors.Is(outer, target) {
		t.Error("errors.Is should find CodeSidecarHung in the chain")
	}

	// errors.Is — 不匹配的 code
	wrongTarget := New(CodePanicked, WithOccurredAt(fixedTime))
	if stderrors.Is(outer, wrongTarget) {
		t.Error("errors.Is should NOT match CodePanicked in this chain")
	}

	// errors.As — 提取 *BrainError
	var be *BrainError
	if !stderrors.As(outer, &be) {
		t.Error("errors.As should succeed on a *BrainError")
	}
	if be == nil {
		t.Fatal("errors.As populated a nil pointer")
	}
	if be.ErrorCode != CodeAssertionFailed {
		t.Errorf("errors.As should return outermost, got %q", be.ErrorCode)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// C-E-12: QuotaExceeded → Decide 总是 ActionFail
// ──────────────────────────────────────────────────────────────────────────────

func TestC_E_12_QuotaCooldownAlwaysFail(t *testing.T) {
	e := New(CodeLLMQuotaExhaustedDaily,
		WithMessage("daily quota hit"),
		WithOccurredAt(fixedTime),
	)
	assertf(t, e.Class == ClassQuotaExceeded, "class must be QuotaExceeded")

	policies := []FaultPolicy{FaultPolicyFailFast, FaultPolicyBestEffort, FaultPolicyRetry}
	healths := []Health{HealthHealthy, HealthDegraded, HealthQuarantined}
	for _, fp := range policies {
		for _, h := range healths {
			d := Decide(e, DecideContext{FaultPolicy: fp, Health: h, Rand: fixedRand()})
			if d.Action != ActionFail {
				t.Errorf("QuotaExceeded policy=%q health=%q: expected Fail got %q", fp, h, d.Action)
			}
		}
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// C-E-13: SafetyRefused → Decide 总是 ActionAskHuman
// ──────────────────────────────────────────────────────────────────────────────

func TestC_E_13_SafetyRefusedAlwaysAskHuman(t *testing.T) {
	safetyCodes := []string{CodeLLMSafetyRefused, CodeToolSandboxDenied, CodePolicyGateDenied}

	for _, code := range safetyCodes {
		e := New(code, WithMessage("refused"), WithOccurredAt(fixedTime))
		assertf(t, e.Class == ClassSafetyRefused, "code=%q must be SafetyRefused, got %q", code, e.Class)

		for _, h := range []Health{HealthHealthy, HealthDegraded, HealthQuarantined} {
			d := Decide(e, DecideContext{FaultPolicy: FaultPolicyFailFast, Health: h, Rand: fixedRand()})
			if d.Action != ActionAskHuman {
				t.Errorf("code=%q health=%q: expected AskHuman got %q", code, h, d.Action)
			}
		}
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// C-E-14: InternalBug 升级梯队（healthy→DegradeBrain, degraded→Quarantine, quarantined→Fail）
// ──────────────────────────────────────────────────────────────────────────────

func TestC_E_14_InternalBugEscalationLadder(t *testing.T) {
	e := New(CodePanicked, WithMessage("panic!"), WithOccurredAt(fixedTime))

	// Healthy → DegradeBrain (row 15)
	d1 := Decide(e, DecideContext{Health: HealthHealthy, Rand: fixedRand()})
	if d1.Action != ActionDegradeBrain {
		t.Errorf("healthy InternalBug: expected DegradeBrain got %q", d1.Action)
	}

	// Degraded → Quarantine (row 16)
	d2 := Decide(e, DecideContext{Health: HealthDegraded, Rand: fixedRand()})
	if d2.Action != ActionQuarantine {
		t.Errorf("degraded InternalBug: expected Quarantine got %q", d2.Action)
	}

	// Quarantined → Fail (row 17)
	d3 := Decide(e, DecideContext{Health: HealthQuarantined, Rand: fixedRand()})
	if d3.Action != ActionFail {
		t.Errorf("quarantined InternalBug: expected Fail got %q", d3.Action)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// C-E-15: 未注册 code → 回退到 CodeUnknown + ClassInternalBug + DebugFlags
// ──────────────────────────────────────────────────────────────────────────────

func TestC_E_15_UnregisteredCodeFallback(t *testing.T) {
	unregistered := "some_totally_fake_code_xyz"

	// 确保该 code 确实未注册
	_, ok := Lookup(unregistered)
	if ok {
		t.Fatalf("test setup error: %q is actually registered", unregistered)
	}

	e := New(unregistered, WithOccurredAt(fixedTime))

	if e.ErrorCode != CodeUnknown {
		t.Errorf("ErrorCode: got=%q want=%q", e.ErrorCode, CodeUnknown)
	}
	if e.Class != ClassInternalBug {
		t.Errorf("Class: got=%q want=%q", e.Class, ClassInternalBug)
	}
	if e.InternalOnly == nil {
		t.Fatal("InternalOnly must be set for registry violation")
	}
	if e.InternalOnly.DebugFlags == nil {
		t.Fatal("DebugFlags must be set")
	}
	if e.InternalOnly.DebugFlags["requested_code"] != unregistered {
		t.Errorf("DebugFlags[requested_code]: got=%q want=%q",
			e.InternalOnly.DebugFlags["requested_code"], unregistered)
	}
	if e.InternalOnly.DebugFlags["registry_violation"] != "true" {
		t.Errorf("DebugFlags[registry_violation] must be 'true', got %q",
			e.InternalOnly.DebugFlags["registry_violation"])
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// C-E-16: ObservabilityHook 扇出 + unregister 闭包有效
// ──────────────────────────────────────────────────────────────────────────────

func TestC_E_16_ObservabilityHookFanout(t *testing.T) {
	ResetObservabilityHooks()
	defer ResetObservabilityHooks()

	var captured []*BrainError

	unregister := RegisterObservabilityHook(HookFunc(func(e *BrainError) {
		captured = append(captured, e)
	}))

	// 构造一个错误，hook 应当被调用
	e1 := New(CodeSidecarHung, WithMessage("hook test 1"), WithOccurredAt(fixedTime))
	if len(captured) != 1 {
		t.Fatalf("expected 1 hook call after first New, got %d", len(captured))
	}
	if captured[0].ErrorCode != e1.ErrorCode {
		t.Errorf("hook received wrong ErrorCode: %q", captured[0].ErrorCode)
	}

	// 注销 hook
	unregister()

	// 再构造一个，hook 不应再收到
	_ = New(CodeSidecarHung, WithMessage("hook test 2"), WithOccurredAt(fixedTime))
	if len(captured) != 1 {
		t.Errorf("after unregister, hook should not fire; fired %d times total", len(captured))
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// C-E-17: panic 钩子被隔离，其他钩子仍然触发
// ──────────────────────────────────────────────────────────────────────────────

func TestC_E_17_PanicHookIsolated(t *testing.T) {
	ResetObservabilityHooks()
	defer ResetObservabilityHooks()

	// 注册一个 panic 钩子
	RegisterObservabilityHook(HookFunc(func(e *BrainError) {
		panic("intentional hook panic")
	}))

	// 注册第二个正常钩子
	var secondFired bool
	RegisterObservabilityHook(HookFunc(func(e *BrainError) {
		secondFired = true
	}))

	// 构造应该正常返回（panic 被 safeDispatch 捕获）
	var result *BrainError
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("panic escaped from New: %v", r)
			}
		}()
		result = New(CodeSidecarHung, WithMessage("panic test"), WithOccurredAt(fixedTime))
	}()

	if result == nil {
		t.Fatal("New must return a valid *BrainError even when a hook panics")
	}
	if !secondFired {
		t.Error("second hook must fire even when the first hook panics")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// C-E-18: ToSpanAttrs 返回 OTel 语义键
// ──────────────────────────────────────────────────────────────────────────────

func TestC_E_18_ToSpanAttrs(t *testing.T) {
	inner := New(CodeDeadlineExceeded,
		WithMessage("inner"),
		WithOccurredAt(fixedTime),
	)
	e := Wrap(inner, CodeBrainTaskFailed,
		WithMessage("outer"),
		WithBrainID("central"),
		WithAttempt(2),
		WithOccurredAt(fixedTime),
	)

	attrs := e.ToSpanAttrs()

	required := []string{
		"error.type", "error.code", "error.class", "error.retryable", "error.fingerprint",
	}
	for _, k := range required {
		if _, ok := attrs[k]; !ok {
			t.Errorf("ToSpanAttrs missing required key %q", k)
		}
	}

	if attrs["error.type"] != "brain" {
		t.Errorf("error.type: got=%q want=%q", attrs["error.type"], "brain")
	}
	if attrs["error.code"] != CodeBrainTaskFailed {
		t.Errorf("error.code: got=%q want=%q", attrs["error.code"], CodeBrainTaskFailed)
	}
	if attrs["brain.id"] != "central" {
		t.Errorf("brain.id: got=%q want=%q", attrs["brain.id"], "central")
	}
	if attrs["brain.attempt"] != "2" {
		t.Errorf("brain.attempt: got=%q want=%q", attrs["brain.attempt"], "2")
	}

	// 有 Cause 时包含 cause.code / cause.class
	if _, ok := attrs["error.cause.code"]; !ok {
		t.Error("ToSpanAttrs must include error.cause.code when Cause is set")
	}
	if attrs["error.cause.code"] != CodeDeadlineExceeded {
		t.Errorf("error.cause.code: got=%q want=%q", attrs["error.cause.code"], CodeDeadlineExceeded)
	}
	if _, ok := attrs["error.cause.class"]; !ok {
		t.Error("ToSpanAttrs must include error.cause.class when Cause is set")
	}

	// 无 BrainID 的错误，brain.id 应缺省
	e2 := New(CodeSidecarHung, WithOccurredAt(fixedTime))
	attrs2 := e2.ToSpanAttrs()
	if _, ok := attrs2["brain.id"]; ok {
		t.Error("brain.id should be omitted when BrainID is empty")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// C-E-19: Wrap(nil, code) ≡ New(code)
// ──────────────────────────────────────────────────────────────────────────────

func TestC_E_19_WrapNilEquivNew(t *testing.T) {
	// 强制 now() 一致
	fixedNow := fixedTime
	origNow := now
	now = func() time.Time { return fixedNow }
	defer func() { now = origNow }()

	wrapNil := Wrap(nil, CodeSidecarHung, WithMessage("hello"))
	fresh := New(CodeSidecarHung, WithMessage("hello"))

	if wrapNil.ErrorCode != fresh.ErrorCode {
		t.Errorf("ErrorCode: Wrap(nil) got=%q New got=%q", wrapNil.ErrorCode, fresh.ErrorCode)
	}
	if wrapNil.Class != fresh.Class {
		t.Errorf("Class: Wrap(nil) got=%q New got=%q", wrapNil.Class, fresh.Class)
	}
	if wrapNil.Retryable != fresh.Retryable {
		t.Errorf("Retryable: Wrap(nil) got=%v New got=%v", wrapNil.Retryable, fresh.Retryable)
	}
	if wrapNil.Cause != nil {
		t.Errorf("Wrap(nil) must have no Cause, got %+v", wrapNil.Cause)
	}
	if wrapNil.Fingerprint != fresh.Fingerprint {
		t.Errorf("Fingerprint: Wrap(nil) got=%q New got=%q", wrapNil.Fingerprint, fresh.Fingerprint)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// C-E-20: Wrap 包裹非 BrainError Go 错误
// cause.Message == original.Error()  且  FromGoError 映射的 code 生效
// ──────────────────────────────────────────────────────────────────────────────

func TestC_E_20_WrapNonBrainGoError(t *testing.T) {
	// context.DeadlineExceeded → FromGoError → CodeDeadlineExceeded
	goErr := context.DeadlineExceeded
	e := Wrap(goErr, CodeBrainTaskFailed,
		WithMessage("task timed out"),
		WithOccurredAt(fixedTime),
	)

	if e.Cause == nil {
		t.Fatal("Cause must be set when wrapping a Go error")
	}

	// Cause.Message 保留原始 .Error() 文本
	wantMsg := goErr.Error()
	if e.Cause.Message != wantMsg {
		t.Errorf("Cause.Message: got=%q want=%q", e.Cause.Message, wantMsg)
	}

	// Cause.ErrorCode 应该是 FromGoError 映射后的 code
	if e.Cause.ErrorCode != CodeDeadlineExceeded {
		t.Errorf("Cause.ErrorCode: got=%q want=%q", e.Cause.ErrorCode, CodeDeadlineExceeded)
	}

	// 无映射的 Go 错误 → Cause.ErrorCode == CodeUnknown
	plainErr := stderrors.New("some unclassified error")
	e2 := Wrap(plainErr, CodeBrainTaskFailed,
		WithMessage("unknown cause"),
		WithOccurredAt(fixedTime),
	)
	if e2.Cause == nil {
		t.Fatal("Cause must be set even for unmapped Go errors")
	}
	if e2.Cause.ErrorCode != CodeUnknown {
		t.Errorf("unmapped Go error: Cause.ErrorCode got=%q want=%q",
			e2.Cause.ErrorCode, CodeUnknown)
	}
	if e2.Cause.Message != plainErr.Error() {
		t.Errorf("unmapped Go error: Cause.Message got=%q want=%q",
			e2.Cause.Message, plainErr.Error())
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// 附加：退避抖动范围验证（对应规范 §8.3 ±20% jitter = [0.8x, 1.2x]）
// ──────────────────────────────────────────────────────────────────────────────

func TestC_E_16_BackoffJitterRange(t *testing.T) {
	e := New(CodeSidecarHung, WithOccurredAt(fixedTime))

	// 多次调用，验证退避落在 [0.8 * base, 1.2 * base] 区间
	// attempt=0 → base=1s
	const base = float64(1 * time.Second)
	const iterations = 1000
	lo := base * 0.8
	hi := base * 1.2

	for i := 0; i < iterations; i++ {
		r := rand.New(rand.NewSource(int64(i)))
		dctx := DecideContext{
			FaultPolicy: FaultPolicyFailFast,
			Health:      HealthHealthy,
			Attempt:     0,
			Rand:        r,
		}
		d := Decide(e, dctx)
		bh := float64(d.BackoffHint)
		if bh < lo || bh > hi {
			t.Errorf("seed=%d BackoffHint=%v out of [%v, %v]", i, d.BackoffHint, time.Duration(lo), time.Duration(hi))
		}
	}
}
