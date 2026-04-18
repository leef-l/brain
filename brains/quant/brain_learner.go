// brain_learner.go — QuantBrainLearner 适配器
//
// 将 quant 现有的 WeightAdapter / SymbolScorer / SLTPOptimizer 学习系统
// 适配到 kernel.BrainLearner 标准接口 (RecordOutcome / ExportMetrics)。
// 这是一个纯适配层，不修改任何现有学习逻辑。
package quant

import (
	"context"
	"sync"
	"time"

	"github.com/leef-l/brain/brains/quant/tradestore"
	"github.com/leef-l/brain/sdk/agent"
	"github.com/leef-l/brain/sdk/kernel"
)

// QuantBrainLearner 实现 kernel.BrainLearner 接口，将标准 L0 协议
// 桥接到 quant 领域特有的学习系统。
//
// RecordOutcome 将通用 TaskOutcome 转换为 tradestore.TradeRecord 格式，
// 供 WeightAdapter / SymbolScorer / SLTPOptimizer 消费。
// ExportMetrics 从 QuantBrain 运行时指标导出标准 BrainMetrics。
type QuantBrainLearner struct {
	qb *QuantBrain

	// EWMA 跟踪成功率和延迟，用于 ExportMetrics
	mu           sync.Mutex
	taskCount    int
	successCount int
	latencyEWMA  kernel.EWMAScore
	startTime    time.Time
}

// NewQuantBrainLearner 创建 QuantBrainLearner 实例。
// qb 是 QuantBrain 实例，用于读取运行时指标。
func NewQuantBrainLearner(qb *QuantBrain) *QuantBrainLearner {
	return &QuantBrainLearner{
		qb:          qb,
		latencyEWMA: kernel.EWMAScore{Alpha: 0.2},
		startTime:   time.Now(),
	}
}

// RecordOutcome 记录一次任务执行结果。
// 将 kernel.TaskOutcome 转换为 quant 学习系统能消费的信息：
//   - 更新内部 EWMA 成功率/延迟跟踪
//   - 如果 quant brain 配置了 TradeStore，可以利用已有学习循环自动消费
//
// 注意：quant 的核心学习（WeightAdapter 等）通过 learningLoop 自动从
// TradeStore 读取历史数据来学习。RecordOutcome 主要用于补充通用任务级
// 的指标跟踪，不取代现有 learningLoop 机制。
func (ql *QuantBrainLearner) RecordOutcome(_ context.Context, outcome kernel.TaskOutcome) error {
	ql.mu.Lock()
	defer ql.mu.Unlock()

	ql.taskCount++
	if outcome.Success {
		ql.successCount++
	}
	ql.latencyEWMA.Update(outcome.Duration.Seconds())

	return nil
}

// ExportMetrics 导出 quant brain 的聚合指标快照。
// 综合以下数据源：
//   - QuantBrain.Health() 运行时计数器（cycles, signals, trades）
//   - 内部 EWMA 跟踪的成功率和延迟
//   - TradeStore 的交易统计（如果可用）
func (ql *QuantBrainLearner) ExportMetrics() kernel.BrainMetrics {
	ql.mu.Lock()
	defer ql.mu.Unlock()

	// 基础指标：从内部 EWMA 跟踪
	successRate := 0.0
	if ql.taskCount > 0 {
		successRate = float64(ql.successCount) / float64(ql.taskCount)
	}

	// 置信度趋势：结合运行时交易指标估算
	trend := 0.0
	health := ql.qb.Health()
	tradesExecuted, _ := health["trades_executed"].(int64)
	tradesRejected, _ := health["trades_rejected"].(int64)

	if tradesExecuted+tradesRejected > 0 {
		// 执行率作为置信度趋势的代理指标
		execRate := float64(tradesExecuted) / float64(tradesExecuted+tradesRejected)
		trend = execRate - 0.5 // [-0.5, 0.5]
	}

	// 如果有 TradeStore 数据，用交易级统计增强成功率
	enhancedSuccessRate := successRate
	units := ql.qb.Units()
	var totalStats tradestore.Stats
	storeFound := false
	for _, u := range units {
		if u.TradeStore != nil {
			stats := u.TradeStore.Stats(tradestore.Filter{UnitID: u.ID})
			totalStats.TotalTrades += stats.TotalTrades
			totalStats.Wins += stats.Wins
			totalStats.Losses += stats.Losses
			totalStats.TotalPnL += stats.TotalPnL
			storeFound = true
		}
	}
	if storeFound && totalStats.TotalTrades > 0 {
		tradeWinRate := float64(totalStats.Wins) / float64(totalStats.TotalTrades)
		// 混合：50% 任务级成功率 + 50% 交易级胜率
		if ql.taskCount > 0 {
			enhancedSuccessRate = successRate*0.5 + tradeWinRate*0.5
		} else {
			enhancedSuccessRate = tradeWinRate
		}
		// 用 PnL 正负作为趋势修正
		if totalStats.TotalPnL > 0 {
			trend += 0.1
		} else if totalStats.TotalPnL < 0 {
			trend -= 0.1
		}
	}

	// 限制趋势范围
	if trend > 1 {
		trend = 1
	}
	if trend < -1 {
		trend = -1
	}

	return kernel.BrainMetrics{
		BrainKind:       agent.KindQuant,
		Period:          time.Since(ql.startTime),
		TaskCount:       ql.taskCount + totalStats.TotalTrades,
		SuccessRate:     enhancedSuccessRate,
		AvgLatencyMs:    ql.latencyEWMA.Value * 1000,
		ConfidenceTrend: trend,
	}
}

// 编译时断言：确保 QuantBrainLearner 实现 kernel.BrainLearner 接口。
var _ kernel.BrainLearner = (*QuantBrainLearner)(nil)
