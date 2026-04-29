// learner.go — VerifierBrain 领域特化 L0 学习器
//
// 通过 ToolOutcomeRecorder 接收每次验证工具调用的结果，
// 追踪验证通过率、误报、漏报，为 Adapt() 提供闭环领域信号。
package main

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/leef-l/brain/sdk/agent"
	"github.com/leef-l/brain/sdk/kernel"
)

// VerifierBrainLearner 是 Verifier Brain 的领域特化 L0 学习器。
type VerifierBrainLearner struct {
	mu            sync.Mutex
	taskCount     int
	successCount  int
	verifyCount   int
	verifyOk      int
	falsePositive int
	falseNegative int
	latencyEWMA   kernel.EWMAScore
	startTime     time.Time

	adaptSuggestion string
}

// NewVerifierBrainLearner 创建领域特化学习器实例。
func NewVerifierBrainLearner() *VerifierBrainLearner {
	return &VerifierBrainLearner{
		latencyEWMA: kernel.EWMAScore{Alpha: 0.2},
		startTime:   time.Now(),
	}
}

// RecordOutcome 记录通用任务结果。
func (vl *VerifierBrainLearner) RecordOutcome(_ context.Context, outcome kernel.TaskOutcome) error {
	vl.mu.Lock()
	defer vl.mu.Unlock()
	vl.taskCount++
	if outcome.Success {
		vl.successCount++
	}
	vl.latencyEWMA.Update(outcome.Duration.Seconds())
	return nil
}

// RecordToolOutcome 根据 tool name 分类更新领域指标。
func (vl *VerifierBrainLearner) RecordToolOutcome(toolName string, success bool) {
	vl.mu.Lock()
	defer vl.mu.Unlock()

	if strings.Contains(toolName, "run_tests") || strings.Contains(toolName, "check_output") || strings.Contains(toolName, "browser_action") {
		vl.verifyCount++
		if success {
			vl.verifyOk++
		}
	}
}

// RecordFalsePositive 记录一次误报。
func (vl *VerifierBrainLearner) RecordFalsePositive() {
	vl.mu.Lock()
	defer vl.mu.Unlock()
	vl.falsePositive++
}

// RecordFalseNegative 记录一次漏报。
func (vl *VerifierBrainLearner) RecordFalseNegative() {
	vl.mu.Lock()
	defer vl.mu.Unlock()
	vl.falseNegative++
}

// ExportMetrics 导出标准 BrainMetrics，综合领域指标计算趋势。
func (vl *VerifierBrainLearner) ExportMetrics() kernel.BrainMetrics {
	vl.mu.Lock()
	defer vl.mu.Unlock()

	successRate := 0.0
	if vl.taskCount > 0 {
		successRate = float64(vl.successCount) / float64(vl.taskCount)
	}

	trend := successRate - 0.5
	if vl.verifyCount > 0 {
		verifyRate := float64(vl.verifyOk) / float64(vl.verifyCount)
		trend = trend*0.5 + (verifyRate-0.5)*0.5
	}
	fpfn := vl.falsePositive + vl.falseNegative
	if fpfn > 0 && vl.verifyCount > 0 {
		trend -= float64(fpfn) / float64(vl.verifyCount) * 0.2
	}
	if trend > 1 {
		trend = 1
	}
	if trend < -1 {
		trend = -1
	}

	return kernel.BrainMetrics{
		BrainKind:       agent.KindVerifier,
		Period:          time.Since(vl.startTime),
		TaskCount:       vl.taskCount,
		SuccessRate:     successRate,
		AvgLatencyMs:    vl.latencyEWMA.Value * 1000,
		ConfidenceTrend: trend,
	}
}

// Adapt 根据历史指标生成自适应建议。
// 当误报率偏高时建议提高验证深度；漏报率高时建议增加测试覆盖。
func (vl *VerifierBrainLearner) Adapt(_ context.Context) error {
	vl.mu.Lock()
	defer vl.mu.Unlock()

	fpRate := 0.0
	fnRate := 0.0
	if vl.verifyCount > 0 {
		fpRate = float64(vl.falsePositive) / float64(vl.verifyCount)
		fnRate = float64(vl.falseNegative) / float64(vl.verifyCount)
	}

	if vl.verifyCount >= 5 && fpRate > 0.3 {
		vl.adaptSuggestion = "false_positive_high: suggest tightening verification criteria and adding cross-checks"
	} else if vl.verifyCount >= 5 && fnRate > 0.3 {
		vl.adaptSuggestion = "false_negative_high: suggest increasing test coverage and edge-case probes"
	} else {
		vl.adaptSuggestion = "verifier_metrics_healthy: maintain current verification depth"
	}
	return nil
}

// LastAdaptSuggestion 返回最近一次 Adapt() 生成的建议文本。
func (vl *VerifierBrainLearner) LastAdaptSuggestion() string {
	vl.mu.Lock()
	defer vl.mu.Unlock()
	return vl.adaptSuggestion
}

var _ kernel.BrainLearner = (*VerifierBrainLearner)(nil)
var _ kernel.ToolOutcomeRecorder = (*VerifierBrainLearner)(nil)
