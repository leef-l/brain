// learner.go — BrowserBrain 领域特化 L0 学习器
//
// 通过 ToolOutcomeRecorder 接收每次浏览器工具调用的结果，
// 追踪页面加载、元素定位、浏览器操作三类核心交互的成功率。
package main

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/leef-l/brain/sdk/agent"
	"github.com/leef-l/brain/sdk/kernel"
)

// BrowserBrainLearner 是 Browser Brain 的领域特化 L0 学习器。
type BrowserBrainLearner struct {
	mu           sync.Mutex
	taskCount    int
	successCount int
	loadCount    int
	loadOk       int
	locateCount  int
	locateOk     int
	actionCount  int
	actionOk     int
	latencyEWMA  kernel.EWMAScore
	startTime    time.Time

	adaptSuggestion string
}

// NewBrowserBrainLearner 创建领域特化学习器实例。
func NewBrowserBrainLearner() *BrowserBrainLearner {
	return &BrowserBrainLearner{
		latencyEWMA: kernel.EWMAScore{Alpha: 0.2},
		startTime:   time.Now(),
	}
}

// RecordOutcome 记录通用任务结果。
func (bl *BrowserBrainLearner) RecordOutcome(_ context.Context, outcome kernel.TaskOutcome) error {
	bl.mu.Lock()
	defer bl.mu.Unlock()
	bl.taskCount++
	if outcome.Success {
		bl.successCount++
	}
	bl.latencyEWMA.Update(outcome.Duration.Seconds())
	return nil
}

// RecordToolOutcome 根据 tool name 分类更新领域指标。
func (bl *BrowserBrainLearner) RecordToolOutcome(toolName string, success bool) {
	bl.mu.Lock()
	defer bl.mu.Unlock()

	switch {
	case strings.Contains(toolName, "navigate"), strings.Contains(toolName, "goto"):
		bl.loadCount++
		if success {
			bl.loadOk++
		}
	case strings.Contains(toolName, "find"), strings.Contains(toolName, "query_selector"), strings.Contains(toolName, "locate"):
		bl.locateCount++
		if success {
			bl.locateOk++
		}
	case strings.Contains(toolName, "click"), strings.Contains(toolName, "type"), strings.Contains(toolName, "scroll"),
		strings.Contains(toolName, "upload"), strings.Contains(toolName, "drag"), strings.Contains(toolName, "screenshot"):
		bl.actionCount++
		if success {
			bl.actionOk++
		}
	}
}

// ExportMetrics 导出标准 BrainMetrics，综合领域指标计算趋势。
func (bl *BrowserBrainLearner) ExportMetrics() kernel.BrainMetrics {
	bl.mu.Lock()
	defer bl.mu.Unlock()

	successRate := 0.0
	if bl.taskCount > 0 {
		successRate = float64(bl.successCount) / float64(bl.taskCount)
	}

	trend := successRate - 0.5
	totalDomain := bl.loadCount + bl.locateCount + bl.actionCount
	if totalDomain > 0 {
		domainOk := bl.loadOk + bl.locateOk + bl.actionOk
		domainRate := float64(domainOk) / float64(totalDomain)
		trend = trend*0.5 + (domainRate-0.5)*0.5
	}

	return kernel.BrainMetrics{
		BrainKind:       agent.KindBrowser,
		Period:          time.Since(bl.startTime),
		TaskCount:       bl.taskCount,
		SuccessRate:     successRate,
		AvgLatencyMs:    bl.latencyEWMA.Value * 1000,
		ConfidenceTrend: trend,
	}
}

// Adapt 根据历史指标生成自适应建议。
// 当元素定位成功率偏低时，建议增加等待时间；页面加载失败率高时建议降速。
func (bl *BrowserBrainLearner) Adapt(_ context.Context) error {
	bl.mu.Lock()
	defer bl.mu.Unlock()

	locateRate := 0.0
	if bl.locateCount > 0 {
		locateRate = float64(bl.locateOk) / float64(bl.locateCount)
	}
	loadRate := 0.0
	if bl.loadCount > 0 {
		loadRate = float64(bl.loadOk) / float64(bl.loadCount)
	}

	if bl.locateCount >= 5 && locateRate < 0.5 {
		bl.adaptSuggestion = "locate_rate_low: suggest increasing action_timeout and wait_for readiness"
	} else if bl.loadCount >= 5 && loadRate < 0.5 {
		bl.adaptSuggestion = "load_rate_low: suggest reducing navigation speed and retry on timeout"
	} else {
		bl.adaptSuggestion = "browser_metrics_healthy: maintain current timeout settings"
	}
	return nil
}

// LastAdaptSuggestion 返回最近一次 Adapt() 生成的建议文本。
func (bl *BrowserBrainLearner) LastAdaptSuggestion() string {
	bl.mu.Lock()
	defer bl.mu.Unlock()
	return bl.adaptSuggestion
}

var _ kernel.BrainLearner = (*BrowserBrainLearner)(nil)
var _ kernel.ToolOutcomeRecorder = (*BrowserBrainLearner)(nil)
