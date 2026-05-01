// failure_log_wrapper.go — 在工具最外层装饰一层失败日志记录。
//
// 用法：构建工具时用 WrapWithFailureLog(t) 包一层。任何 Execute 返回
// IsError=true 的调用都会被结构化追加到 ~/.brain/logs/tool-failures.log。
//
// 设计：作为最外层装饰器（在 sandbox / approval / brain-aware 之后），
// 这样能捕获到所有失败 —— 包括 sandbox 拒绝、approval 拒绝、工具内部错误等。

package tool

import (
	"context"
	"encoding/json"
)

type failureLoggingTool struct {
	inner Tool
}

// WrapWithFailureLog 把工具包一层失败日志装饰器。
// inner 为 nil 时返回 nil。已经被包装过的工具不会重复包装（避免双写）。
func WrapWithFailureLog(inner Tool) Tool {
	if inner == nil {
		return nil
	}
	if _, ok := inner.(*failureLoggingTool); ok {
		return inner
	}
	return &failureLoggingTool{inner: inner}
}

func (t *failureLoggingTool) Name() string   { return t.inner.Name() }
func (t *failureLoggingTool) Schema() Schema { return t.inner.Schema() }
func (t *failureLoggingTool) Risk() Risk     { return t.inner.Risk() }

func (t *failureLoggingTool) Execute(ctx context.Context, args json.RawMessage) (*Result, error) {
	result, err := t.inner.Execute(ctx, args)

	// 即使 err != nil 也记录（框架层错误，没产生 result）
	if err != nil {
		LogToolFailure(t.inner.Name(), t.inner.Schema().Brain, formatArgsForLog(args), err.Error())
		return result, err
	}
	if result != nil && result.IsError {
		detail := ""
		if len(result.Output) > 0 {
			detail = string(result.Output)
		}
		LogToolFailure(t.inner.Name(), t.inner.Schema().Brain, formatArgsForLog(args), detail)
	}
	return result, err
}
