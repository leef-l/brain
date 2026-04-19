package data

import (
	"context"
	"sync"
	"time"

	"github.com/leef-l/brain/sdk/agent"
	"github.com/leef-l/brain/sdk/kernel"
)

// DataBrainLearner 实现 kernel.BrainLearner 接口，
// 将 DataBrain 的运行指标桥接到 L0 标准协议。
type DataBrainLearner struct {
	db *DataBrain

	mu           sync.Mutex
	taskCount    int
	successCount int
	latencyEWMA  kernel.EWMAScore
	startTime    time.Time
}

func NewDataBrainLearner(db *DataBrain) *DataBrainLearner {
	return &DataBrainLearner{
		db:          db,
		latencyEWMA: kernel.EWMAScore{Alpha: 0.2},
		startTime:   time.Now(),
	}
}

func (dl *DataBrainLearner) RecordOutcome(_ context.Context, outcome kernel.TaskOutcome) error {
	dl.mu.Lock()
	defer dl.mu.Unlock()
	dl.taskCount++
	if outcome.Success {
		dl.successCount++
	}
	dl.latencyEWMA.Update(outcome.Duration.Seconds())
	return nil
}

func (dl *DataBrainLearner) ExportMetrics() kernel.BrainMetrics {
	dl.mu.Lock()
	defer dl.mu.Unlock()

	successRate := 0.0
	if dl.taskCount > 0 {
		successRate = float64(dl.successCount) / float64(dl.taskCount)
	}

	trend := 0.0
	if dl.db != nil {
		total := dl.db.metrics.WSMessagesTotal.Load()
		rejected := dl.db.metrics.ValidatorRejected.Load()
		if total > 0 {
			acceptRate := float64(total-rejected) / float64(total)
			trend = acceptRate - 0.5
		}
		writeErrors := dl.db.metrics.PGWriteErrors.Load()
		writes := dl.db.metrics.PGWriteTotal.Load()
		if writes > 0 && writeErrors > 0 {
			trend -= float64(writeErrors) / float64(writes) * 0.5
		}
	}
	if trend > 1 {
		trend = 1
	}
	if trend < -1 {
		trend = -1
	}

	return kernel.BrainMetrics{
		BrainKind:       agent.KindData,
		Period:          time.Since(dl.startTime),
		TaskCount:       dl.taskCount,
		SuccessRate:     successRate,
		AvgLatencyMs:    dl.latencyEWMA.Value * 1000,
		ConfidenceTrend: trend,
	}
}

var _ kernel.BrainLearner = (*DataBrainLearner)(nil)
