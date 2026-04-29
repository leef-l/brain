package data

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/leef-l/brain/sdk/agent"
	"github.com/leef-l/brain/sdk/kernel"
)

// DataBrainLearner 实现 kernel.BrainLearner 接口，
// 将 DataBrain 的运行指标桥接到 L0 标准协议。
type DataBrainLearner struct {
	db *DataBrain

	mu              sync.Mutex
	taskCount       int
	successCount    int
	latencyEWMA     kernel.EWMAScore
	startTime       time.Time
	adaptSuggestion string
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

// Adapt 基于运行统计生成自适应调整建议。
// 根据任务成功率、延迟趋势、WS 验证通过率和 PG 写入错误率动态生成建议。
func (dl *DataBrainLearner) Adapt(_ context.Context) error {
	dl.mu.Lock()
	defer dl.mu.Unlock()

	successRate := 0.0
	if dl.taskCount > 0 {
		successRate = float64(dl.successCount) / float64(dl.taskCount)
	}
	avgLatencyMs := dl.latencyEWMA.Value * 1000

	wsTotal := int64(0)
	wsRejected := int64(0)
	pgTotal := int64(0)
	pgErrors := int64(0)
	if dl.db != nil {
		wsTotal = dl.db.metrics.WSMessagesTotal.Load()
		wsRejected = dl.db.metrics.ValidatorRejected.Load()
		pgTotal = dl.db.metrics.PGWriteTotal.Load()
		pgErrors = dl.db.metrics.PGWriteErrors.Load()
	}

	var suggestions []string

	// 1. 任务成功率低 → 检查数据源质量或增加验证严格度
	if dl.taskCount >= 5 && successRate < 0.7 {
		suggestions = append(suggestions, "task_success_low: suggest checking data source quality and validation thresholds")
	}

	// 2. 延迟高 → 建议减少活跃品种数或优化批处理
	if avgLatencyMs > 500 {
		suggestions = append(suggestions, "latency_high: suggest reducing active instruments or increasing batch size")
	}

	// 3. WS 验证拒绝率高 → 建议放宽验证阈值或检查数据源异常
	if wsTotal > 100 {
		rejectRate := float64(wsRejected) / float64(wsTotal)
		if rejectRate > 0.15 {
			suggestions = append(suggestions, "ws_reject_rate_high: suggest relaxing validation thresholds or inspecting data source anomalies")
		} else if rejectRate < 0.01 {
			suggestions = append(suggestions, "ws_reject_rate_low: validation is very strict, consider tightening to catch more anomalies")
		}
	}

	// 4. PG 写入错误率高 → 建议检查数据库连接或增加重试
	if pgTotal > 50 {
		errRate := float64(pgErrors) / float64(pgTotal)
		if errRate > 0.05 {
			suggestions = append(suggestions, "pg_write_error_high: suggest checking database connectivity and retry policies")
		}
	}

	if len(suggestions) == 0 {
		dl.adaptSuggestion = "data_pipeline_healthy: no adjustments needed"
	} else {
		dl.adaptSuggestion = strings.Join(suggestions, "; ")
	}
	return nil
}

// LastAdaptSuggestion 返回最近一次 Adapt 生成的建议。
func (dl *DataBrainLearner) LastAdaptSuggestion() string {
	dl.mu.Lock()
	defer dl.mu.Unlock()
	return dl.adaptSuggestion
}

var _ kernel.BrainLearner = (*DataBrainLearner)(nil)
