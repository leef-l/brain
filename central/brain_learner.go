package central

import (
	"context"
	"sync"
	"time"

	"github.com/leef-l/brain/sdk/agent"
	"github.com/leef-l/brain/sdk/kernel"
)

// CentralBrainLearner 实现 kernel.BrainLearner 接口。
// Central brain 作为编排层，追踪委派任务的成功率和延迟。
type CentralBrainLearner struct {
	mu           sync.Mutex
	taskCount    int
	successCount int
	delegateOk   int
	delegateFail int
	latencyEWMA  kernel.EWMAScore
	startTime    time.Time
}

func NewCentralBrainLearner() *CentralBrainLearner {
	return &CentralBrainLearner{
		latencyEWMA: kernel.EWMAScore{Alpha: 0.2},
		startTime:   time.Now(),
	}
}

func (cl *CentralBrainLearner) RecordOutcome(_ context.Context, outcome kernel.TaskOutcome) error {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	cl.taskCount++
	if outcome.Success {
		cl.successCount++
	}
	cl.latencyEWMA.Update(outcome.Duration.Seconds())

	if outcome.TaskType == "delegate" {
		if outcome.Success {
			cl.delegateOk++
		} else {
			cl.delegateFail++
		}
	}
	return nil
}

func (cl *CentralBrainLearner) ExportMetrics() kernel.BrainMetrics {
	cl.mu.Lock()
	defer cl.mu.Unlock()

	successRate := 0.0
	if cl.taskCount > 0 {
		successRate = float64(cl.successCount) / float64(cl.taskCount)
	}

	trend := 0.0
	totalDelegate := cl.delegateOk + cl.delegateFail
	if totalDelegate > 0 {
		delegateSuccessRate := float64(cl.delegateOk) / float64(totalDelegate)
		trend = delegateSuccessRate - 0.5
	}

	return kernel.BrainMetrics{
		BrainKind:       agent.KindCentral,
		Period:          time.Since(cl.startTime),
		TaskCount:       cl.taskCount,
		SuccessRate:     successRate,
		AvgLatencyMs:    cl.latencyEWMA.Value * 1000,
		ConfidenceTrend: trend,
	}
}

var _ kernel.BrainLearner = (*CentralBrainLearner)(nil)
