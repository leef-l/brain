// learner.go — CodeBrain 领域特化 L0 学习器
//
// 在通用 DefaultBrainLearner 基础上，通过 ToolOutcomeRecorder 接口接收
// 每次单工具调用的成功/失败信号，从而追踪编译、测试、文件编辑三类
// 核心操作的成功率，为 Adapt() 提供闭环领域信号。
package main

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/leef-l/brain/sdk/agent"
	"github.com/leef-l/brain/sdk/kernel"
)

// CodeBrainLearner 是 Code Brain 的领域特化 L0 学习器。
type CodeBrainLearner struct {
	mu           sync.Mutex
	taskCount    int
	successCount int
	compileCount int
	compileOk    int
	testCount    int
	testOk       int
	editCount    int
	editOk       int
	latencyEWMA  kernel.EWMAScore
	startTime    time.Time

	// adaptSuggestion 由 Adapt() 生成，描述最近一次自适应决策。
	adaptSuggestion string
}

// NewCodeBrainLearner 创建领域特化学习器实例。
func NewCodeBrainLearner() *CodeBrainLearner {
	return &CodeBrainLearner{
		latencyEWMA: kernel.EWMAScore{Alpha: 0.2},
		startTime:   time.Now(),
	}
}

// RecordOutcome 记录通用任务结果。
func (cl *CodeBrainLearner) RecordOutcome(_ context.Context, outcome kernel.TaskOutcome) error {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	cl.taskCount++
	if outcome.Success {
		cl.successCount++
	}
	cl.latencyEWMA.Update(outcome.Duration.Seconds())
	return nil
}

// RecordToolOutcome 根据 tool name 分类更新领域指标。
// 实现 kernel.ToolOutcomeRecorder 接口。
func (cl *CodeBrainLearner) RecordToolOutcome(toolName string, success bool) {
	cl.mu.Lock()
	defer cl.mu.Unlock()

	switch {
	case strings.Contains(toolName, "shell_exec"):
		// shell_exec 同时承担编译与测试职责，按各 50% 统计
		cl.compileCount++
		cl.testCount++
		if success {
			cl.compileOk++
			cl.testOk++
		}
	case strings.Contains(toolName, "write_file"), strings.Contains(toolName, "edit_file"), strings.Contains(toolName, "delete_file"):
		cl.editCount++
		if success {
			cl.editOk++
		}
	}
}

// ExportMetrics 导出标准 BrainMetrics，综合领域指标计算趋势。
func (cl *CodeBrainLearner) ExportMetrics() kernel.BrainMetrics {
	cl.mu.Lock()
	defer cl.mu.Unlock()

	successRate := 0.0
	if cl.taskCount > 0 {
		successRate = float64(cl.successCount) / float64(cl.taskCount)
	}

	trend := successRate - 0.5
	totalDomain := cl.compileCount + cl.testCount + cl.editCount
	if totalDomain > 0 {
		domainOk := cl.compileOk + cl.testOk + cl.editOk
		domainRate := float64(domainOk) / float64(totalDomain)
		trend = trend*0.5 + (domainRate-0.5)*0.5
	}

	return kernel.BrainMetrics{
		BrainKind:       agent.KindCode,
		Period:          time.Since(cl.startTime),
		TaskCount:       cl.taskCount,
		SuccessRate:     successRate,
		AvgLatencyMs:    cl.latencyEWMA.Value * 1000,
		ConfidenceTrend: trend,
	}
}

// Adapt 根据历史指标生成自适应建议。
// 当编译成功率持续低于阈值时，建议降低任务复杂度（减少 max turns）。
func (cl *CodeBrainLearner) Adapt(_ context.Context) error {
	cl.mu.Lock()
	defer cl.mu.Unlock()

	compileRate := 0.0
	if cl.compileCount > 0 {
		compileRate = float64(cl.compileOk) / float64(cl.compileCount)
	}
	testRate := 0.0
	if cl.testCount > 0 {
		testRate = float64(cl.testOk) / float64(cl.testCount)
	}

	if cl.compileCount >= 5 && compileRate < 0.5 {
		cl.adaptSuggestion = "compile_rate_low: suggest reducing task complexity (max_turns=15) and enabling pre-build checks"
	} else if cl.testCount >= 5 && testRate < 0.5 {
		cl.adaptSuggestion = "test_rate_low: suggest increasing test isolation and retry flaky suites"
	} else {
		cl.adaptSuggestion = "code_metrics_healthy: maintain current strategy"
	}
	return nil
}

// LastAdaptSuggestion 返回最近一次 Adapt() 生成的建议文本。
func (cl *CodeBrainLearner) LastAdaptSuggestion() string {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	return cl.adaptSuggestion
}

var _ kernel.BrainLearner = (*CodeBrainLearner)(nil)
var _ kernel.ToolOutcomeRecorder = (*CodeBrainLearner)(nil)
