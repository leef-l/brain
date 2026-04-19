package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	brainerrors "github.com/leef-l/brain/sdk/errors"
)

// Task #14 — 工具层 retry-scope + 异常分类。
//
// 复用 brain-v3 的错误体系(sdk/errors):每次工具失败都附带 error_code,调用方
// (Agent 或装饰器)可以通过 brainerrors.Decide 判断要不要重试。
//
// 本文件提供:
//   1. ErrorResult:生成带 error_code / error_class / retryable 的 Result,
//      供 LLM 在 tool_result 看到分类,避免它对 transient 错误也放弃。
//   2. RetryTool 装饰器:按 Decide 矩阵自动重试 transient 错误(工具级),
//      不把重试压力传递给 LLM。
//
// 设计原则:**不自造 retry policy**,所有 BackoffHint/MaxRetries 都来自
// brainerrors.Decide,和 Kernel 其他重试路径共享同一个矩阵。

// ErrorResult builds a Result whose Output JSON carries the BrainError code
// and classification, so LLM reflection +装饰器 retry 都能看到分类。
//
// 用法:
//	return tool.ErrorResult(brainerrors.CodeToolTimeout, "click: element %q did not respond", selector), nil
func ErrorResult(code string, format string, args ...interface{}) *Result {
	msg := fmt.Sprintf(format, args...)
	meta, ok := brainerrors.Lookup(code)
	payload := map[string]interface{}{
		"error":      msg,
		"error_code": code,
	}
	if ok {
		payload["error_class"] = string(meta.Class)
		payload["retryable"] = meta.Retryable
	}
	raw, _ := json.Marshal(payload)
	return &Result{Output: raw, IsError: true}
}

// parseErrorResult 从 Result.Output 里抽 error_code,供 retryWrapper 判断分类。
// 失败返回 ("", false) — 装饰器保守认为不可重试。
func parseErrorResult(r *Result) (code string, ok bool) {
	if r == nil || !r.IsError || len(r.Output) == 0 {
		return "", false
	}
	var payload struct {
		Code string `json:"error_code"`
	}
	if err := json.Unmarshal(r.Output, &payload); err != nil || payload.Code == "" {
		return "", false
	}
	return payload.Code, true
}

// retryWrapper 是针对工具层 transient 错误的自动重试装饰器。遇到
// ClassTransient + Retryable 的错误时按 brainerrors.Decide 的 BackoffHint
// sleep 后再跑,最多 Decide 允许的 MaxRetries 次。其他错误分类直接透传。
type retryWrapper struct {
	inner       Tool
	faultPolicy brainerrors.FaultPolicy
	health      brainerrors.Health
	now         func() time.Time
	sleep       func(context.Context, time.Duration)
}

// WithRetry 包装一个工具,启用工具级自动重试。policy 空时默认 FailFast。
// 不修改 Schema,LLM 侧感知不到 wrapper 存在。
func WithRetry(t Tool) Tool {
	if t == nil {
		return nil
	}
	return &retryWrapper{
		inner:       t,
		faultPolicy: brainerrors.FaultPolicyFailFast,
		health:      brainerrors.HealthHealthy,
		now:         time.Now,
		sleep: func(ctx context.Context, d time.Duration) {
			if d <= 0 {
				return
			}
			select {
			case <-ctx.Done():
			case <-time.After(d):
			}
		},
	}
}

func (w *retryWrapper) Name() string   { return w.inner.Name() }
func (w *retryWrapper) Risk() Risk     { return w.inner.Risk() }
func (w *retryWrapper) Schema() Schema { return w.inner.Schema() }

func (w *retryWrapper) Execute(ctx context.Context, args json.RawMessage) (*Result, error) {
	attempt := 0
	var last *Result
	for {
		select {
		case <-ctx.Done():
			if last != nil {
				return last, nil
			}
			return ErrorResult(brainerrors.CodeDeadlineExceeded, "context canceled: %v", ctx.Err()), nil
		default:
		}

		res, err := w.inner.Execute(ctx, args)
		if err != nil {
			// Hard error (not a tool_result error) — let caller handle.
			return res, err
		}
		last = res
		if res == nil || !res.IsError {
			return res, nil
		}

		code, ok := parseErrorResult(res)
		if !ok {
			return res, nil // 旧工具没 code,走不了重试分类
		}
		meta, found := brainerrors.Lookup(code)
		if !found {
			return res, nil
		}
		be := brainerrors.New(code,
			brainerrors.WithMessage(res.errorMessage()),
			brainerrors.WithRetryable(meta.Retryable),
		)
		dc := brainerrors.DecideContext{
			FaultPolicy: w.faultPolicy,
			Attempt:     attempt,
			Health:      w.health,
			Now:         w.now(),
		}
		decision := brainerrors.Decide(be, dc)
		if decision.Action != brainerrors.ActionRetry {
			return res, nil
		}
		w.sleep(ctx, decision.BackoffHint)
		attempt++
	}
}

// errorMessage 从 Result.Output 抽原始 error 字段。
func (r *Result) errorMessage() string {
	if r == nil || len(r.Output) == 0 {
		return ""
	}
	var p struct {
		Err string `json:"error"`
	}
	if err := json.Unmarshal(r.Output, &p); err == nil {
		return p.Err
	}
	return string(r.Output)
}
