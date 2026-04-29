// learner.go — FaultBrain 领域特化 L0 学习器
//
// 通过 ToolOutcomeRecorder 接收每次故障注入工具调用的结果，
// 追踪故障注入成功率、系统恢复率，为 Adapt() 提供闭环领域信号。
package main

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/leef-l/brain/sdk/agent"
	"github.com/leef-l/brain/sdk/kernel"
)

// FaultBrainLearner 是 Fault Brain 的领域特化 L0 学习器。
type FaultBrainLearner struct {
	mu            sync.Mutex
	taskCount     int
	successCount  int
	injectCount   int
	injectOk      int
	recoveryCount int
	recoveryOk    int
	latencyEWMA   kernel.EWMAScore
	startTime     time.Time

	adaptSuggestion string
}

// NewFaultBrainLearner 创建领域特化学习器实例。
func NewFaultBrainLearner() *FaultBrainLearner {
	return &FaultBrainLearner{
		latencyEWMA: kernel.EWMAScore{Alpha: 0.2},
		startTime:   time.Now(),
	}
}

// RecordOutcome 记录通用任务结果。
func (fl *FaultBrainLearner) RecordOutcome(_ context.Context, outcome kernel.TaskOutcome) error {
	fl.mu.Lock()
	defer fl.mu.Unlock()
	fl.taskCount++
	if outcome.Success {
		fl.successCount++
	}
	fl.latencyEWMA.Update(outcome.Duration.Seconds())
	return nil
}

// RecordToolOutcome 根据 tool name 分类更新领域指标。
func (fl *FaultBrainLearner) RecordToolOutcome(toolName string, success bool) {
	fl.mu.Lock()
	defer fl.mu.Unlock()

	if strings.Contains(toolName, "inject_error") || strings.Contains(toolName, "inject_latency") ||
		strings.Contains(toolName, "kill_process") || strings.Contains(toolName, "corrupt_response") {
		fl.injectCount++
		if success {
			fl.injectOk++
		}
	}
}

// RecordRecoveryResult 记录一次系统恢复验证结果。
func (fl *FaultBrainLearner) RecordRecoveryResult(ok bool) {
	fl.mu.Lock()
	defer fl.mu.Unlock()
	fl.recoveryCount++
	if ok {
		fl.recoveryOk++
	}
}

// ExportMetrics 导出标准 BrainMetrics，综合领域指标计算趋势。
func (fl *FaultBrainLearner) ExportMetrics() kernel.BrainMetrics {
	fl.mu.Lock()
	defer fl.mu.Unlock()

	successRate := 0.0
	if fl.taskCount > 0 {
		successRate = float64(fl.successCount) / float64(fl.taskCount)
	}

	trend := successRate - 0.5
	totalDomain := fl.injectCount + fl.recoveryCount
	if totalDomain > 0 {
		domainOk := fl.injectOk + fl.recoveryOk
		domainRate := float64(domainOk) / float64(totalDomain)
		trend = trend*0.5 + (domainRate-0.5)*0.5
	}

	return kernel.BrainMetrics{
		BrainKind:       agent.KindFault,
		Period:          time.Since(fl.startTime),
		TaskCount:       fl.taskCount,
		SuccessRate:     successRate,
		AvgLatencyMs:    fl.latencyEWMA.Value * 1000,
		ConfidenceTrend: trend,
	}
}

// Adapt 根据历史指标生成自适应建议。
// 当注入成功率偏低时建议降低强度；恢复率低时建议延长观察窗口。
func (fl *FaultBrainLearner) Adapt(_ context.Context) error {
	fl.mu.Lock()
	defer fl.mu.Unlock()

	injectRate := 0.0
	recoveryRate := 0.0
	if fl.injectCount > 0 {
		injectRate = float64(fl.injectOk) / float64(fl.injectCount)
	}
	if fl.recoveryCount > 0 {
		recoveryRate = float64(fl.recoveryOk) / float64(fl.recoveryCount)
	}

	if fl.injectCount >= 5 && injectRate < 0.5 {
		fl.adaptSuggestion = "inject_rate_low: suggest reducing fault intensity and increasing cooldown"
	} else if fl.recoveryCount >= 5 && recoveryRate < 0.5 {
		fl.adaptSuggestion = "recovery_rate_low: suggest extending observation window and validating health checks"
	} else {
		fl.adaptSuggestion = "fault_metrics_healthy: maintain current injection profile"
	}
	return nil
}

// LastAdaptSuggestion 返回最近一次 Adapt() 生成的建议文本。
func (fl *FaultBrainLearner) LastAdaptSuggestion() string {
	fl.mu.Lock()
	defer fl.mu.Unlock()
	return fl.adaptSuggestion
}

var _ kernel.BrainLearner = (*FaultBrainLearner)(nil)
var _ kernel.ToolOutcomeRecorder = (*FaultBrainLearner)(nil)
