// adaptive_prompt.go — 自适应 Prompt 管理器
//
// MACCS Wave 5 Batch 2: 基于学习数据动态优化 Brain system prompt，
// 通过 A/B 测试评估并选择最优 prompt 变体。
package kernel

import (
	"math"
	"math/rand"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// 数据结构
// ---------------------------------------------------------------------------

// PromptVariant 表示一个 prompt 变体。
type PromptVariant struct {
	VariantID string            `json:"variant_id"`
	BrainKind string            `json:"brain_kind"`
	Template  string            `json:"template"`           // prompt 模板
	Variables map[string]string `json:"variables,omitempty"` // 模板变量
	Version   int               `json:"version"`
	CreatedAt time.Time         `json:"created_at"`
}

// PromptPerformance 汇总某个 prompt 变体的表现指标。
type PromptPerformance struct {
	VariantID    string  `json:"variant_id"`
	TrialCount   int     `json:"trial_count"`
	SuccessCount int     `json:"success_count"`
	SuccessRate  float64 `json:"success_rate"` // 0-1
	AvgTurns     float64 `json:"avg_turns"`
	AvgQuality   float64 `json:"avg_quality"` // 0-1
	Score        float64 `json:"score"`        // 综合评分
}

// PromptTrial 记录单次 prompt 试验结果。
type PromptTrial struct {
	TrialID   string    `json:"trial_id"`
	VariantID string    `json:"variant_id"`
	BrainKind string    `json:"brain_kind"`
	TaskID    string    `json:"task_id"`
	Success   bool      `json:"success"`
	TurnsUsed int       `json:"turns_used"`
	Quality   float64   `json:"quality"` // 0-1
	Timestamp time.Time `json:"timestamp"`
}

// ABTestConfig 定义 A/B 测试参数。
type ABTestConfig struct {
	TestID        string    `json:"test_id"`
	BrainKind     string    `json:"brain_kind"`
	VariantIDs    []string  `json:"variant_ids"`    // 参与测试的变体
	TrafficSplit  []float64 `json:"traffic_split"`  // 流量分配比例
	MinTrials     int       `json:"min_trials"`     // 最少试验次数，默认 10
	ConfidenceReq float64  `json:"confidence_req"` // 置信度要求，默认 0.95
	Active        bool      `json:"active"`
}

// ABTestResult 是 A/B 测试评估结果。
type ABTestResult struct {
	TestID       string              `json:"test_id"`
	Winner       string              `json:"winner"` // variantID
	Performances []PromptPerformance `json:"performances"`
	Significant  bool                `json:"significant"` // 结果是否统计显著
	Confidence   float64             `json:"confidence"`
}

// ---------------------------------------------------------------------------
// 接口
// ---------------------------------------------------------------------------

// AdaptivePromptManager 定义自适应 prompt 管理能力。
type AdaptivePromptManager interface {
	RegisterVariant(variant PromptVariant)
	SelectVariant(brainKind string) *PromptVariant
	RecordTrial(trial PromptTrial)
	GetPerformance(variantID string) *PromptPerformance
	StartABTest(config ABTestConfig)
	EvaluateABTest(testID string) *ABTestResult
	BestVariant(brainKind string) *PromptVariant
	AllVariants(brainKind string) []PromptVariant
}

// ---------------------------------------------------------------------------
// 默认实现
// ---------------------------------------------------------------------------

// DefaultAdaptivePromptManager 是 AdaptivePromptManager 的内存实现。
type DefaultAdaptivePromptManager struct {
	mu       sync.RWMutex
	variants map[string]*PromptVariant // variantID -> variant
	trials   map[string][]PromptTrial  // variantID -> trials
	abTests  map[string]*ABTestConfig  // testID -> config
}

// NewAdaptivePromptManager 创建新的自适应 prompt 管理器。
func NewAdaptivePromptManager() *DefaultAdaptivePromptManager {
	return &DefaultAdaptivePromptManager{
		variants: make(map[string]*PromptVariant),
		trials:   make(map[string][]PromptTrial),
		abTests:  make(map[string]*ABTestConfig),
	}
}

// RegisterVariant 注册一个 prompt 变体。
func (m *DefaultAdaptivePromptManager) RegisterVariant(variant PromptVariant) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v := variant // copy
	m.variants[variant.VariantID] = &v
}

// SelectVariant 选择一个 prompt 变体。
// 如果有活跃的 A/B 测试，按 traffic_split 随机选择；否则选 score 最高的。
func (m *DefaultAdaptivePromptManager) SelectVariant(brainKind string) *PromptVariant {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// 检查是否有活跃的 A/B 测试
	for _, cfg := range m.abTests {
		if cfg.Active && cfg.BrainKind == brainKind && len(cfg.VariantIDs) > 0 {
			return m.selectByTrafficSplit(cfg)
		}
	}

	// 无活跃测试 → 选 score 最高的
	return m.bestVariantLocked(brainKind)
}

// selectByTrafficSplit 按流量分配比例随机选择变体（需持有 RLock）。
func (m *DefaultAdaptivePromptManager) selectByTrafficSplit(cfg *ABTestConfig) *PromptVariant {
	r := rand.Float64() //nolint:gosec
	cumulative := 0.0
	for i, split := range cfg.TrafficSplit {
		cumulative += split
		if r < cumulative && i < len(cfg.VariantIDs) {
			if v, ok := m.variants[cfg.VariantIDs[i]]; ok {
				return v
			}
		}
	}
	// fallback: 返回最后一个有效变体
	for i := len(cfg.VariantIDs) - 1; i >= 0; i-- {
		if v, ok := m.variants[cfg.VariantIDs[i]]; ok {
			return v
		}
	}
	return nil
}

// RecordTrial 记录一次试验结果。
func (m *DefaultAdaptivePromptManager) RecordTrial(trial PromptTrial) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.trials[trial.VariantID] = append(m.trials[trial.VariantID], trial)
}

// GetPerformance 计算指定变体的性能指标。
// score = successRate * 0.5 + (1 - avgTurns/50) * 0.3 + avgQuality * 0.2
func (m *DefaultAdaptivePromptManager) GetPerformance(variantID string) *PromptPerformance {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.computePerformance(variantID)
}

// computePerformance 从 trials 计算 PromptPerformance（需持有 RLock）。
func (m *DefaultAdaptivePromptManager) computePerformance(variantID string) *PromptPerformance {
	trials := m.trials[variantID]
	n := len(trials)
	if n == 0 {
		return &PromptPerformance{VariantID: variantID}
	}

	successCount := 0
	totalTurns := 0
	totalQuality := 0.0
	for _, t := range trials {
		if t.Success {
			successCount++
		}
		totalTurns += t.TurnsUsed
		totalQuality += t.Quality
	}

	successRate := float64(successCount) / float64(n)
	avgTurns := float64(totalTurns) / float64(n)
	avgQuality := totalQuality / float64(n)

	// 综合评分
	turnsComponent := 1.0 - avgTurns/50.0
	if turnsComponent < 0 {
		turnsComponent = 0
	}
	score := successRate*0.5 + turnsComponent*0.3 + avgQuality*0.2

	return &PromptPerformance{
		VariantID:    variantID,
		TrialCount:   n,
		SuccessCount: successCount,
		SuccessRate:  successRate,
		AvgTurns:     avgTurns,
		AvgQuality:   avgQuality,
		Score:        score,
	}
}

// StartABTest 启动一个 A/B 测试。
func (m *DefaultAdaptivePromptManager) StartABTest(config ABTestConfig) {
	if config.MinTrials <= 0 {
		config.MinTrials = 10
	}
	if config.ConfidenceReq <= 0 {
		config.ConfidenceReq = 0.95
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	cfg := config // copy
	m.abTests[config.TestID] = &cfg
}

// EvaluateABTest 评估 A/B 测试结果。
// 每个变体至少 MinTrials 次试验后，按 score 排序选 winner。
// 简化显著性检验：两组 successRate 差距 > 0.1 且样本足够 → significant。
func (m *DefaultAdaptivePromptManager) EvaluateABTest(testID string) *ABTestResult {
	m.mu.RLock()
	defer m.mu.RUnlock()

	cfg, ok := m.abTests[testID]
	if !ok {
		return nil
	}

	result := &ABTestResult{TestID: testID}
	allReady := true

	for _, vid := range cfg.VariantIDs {
		perf := m.computePerformance(vid)
		result.Performances = append(result.Performances, *perf)
		if perf.TrialCount < cfg.MinTrials {
			allReady = false
		}
	}

	if len(result.Performances) == 0 {
		return result
	}

	// 按 score 降序找 winner
	bestIdx := 0
	for i := 1; i < len(result.Performances); i++ {
		if result.Performances[i].Score > result.Performances[bestIdx].Score {
			bestIdx = i
		}
	}
	result.Winner = result.Performances[bestIdx].VariantID

	// 显著性检验（简化版）
	if allReady && len(result.Performances) >= 2 {
		best := result.Performances[bestIdx]
		// 找次优
		secondIdx := -1
		for i := range result.Performances {
			if i == bestIdx {
				continue
			}
			if secondIdx == -1 || result.Performances[i].Score > result.Performances[secondIdx].Score {
				secondIdx = i
			}
		}
		if secondIdx >= 0 {
			second := result.Performances[secondIdx]
			diff := math.Abs(best.SuccessRate - second.SuccessRate)
			if diff > 0.1 {
				result.Significant = true
				result.Confidence = math.Min(cfg.ConfidenceReq, 0.95+diff)
			}
		}
	}

	return result
}

// BestVariant 返回指定 brainKind 下 score 最高的变体。
func (m *DefaultAdaptivePromptManager) BestVariant(brainKind string) *PromptVariant {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.bestVariantLocked(brainKind)
}

// bestVariantLocked 在持有 RLock 的情况下返回 brainKind 下 score 最高的变体。
func (m *DefaultAdaptivePromptManager) bestVariantLocked(brainKind string) *PromptVariant {
	var best *PromptVariant
	bestScore := -1.0
	for _, v := range m.variants {
		if v.BrainKind != brainKind {
			continue
		}
		perf := m.computePerformance(v.VariantID)
		if perf.Score > bestScore {
			bestScore = perf.Score
			best = v
		}
	}
	return best
}

// AllVariants 返回指定 brainKind 的所有变体。
func (m *DefaultAdaptivePromptManager) AllVariants(brainKind string) []PromptVariant {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []PromptVariant
	for _, v := range m.variants {
		if v.BrainKind == brainKind {
			result = append(result, *v)
		}
	}
	return result
}

// ---------------------------------------------------------------------------
// 辅助函数
// ---------------------------------------------------------------------------

// RenderPrompt 用 variables 替换 template 中的 {{key}} 占位符。
func RenderPrompt(variant *PromptVariant) string {
	if variant == nil {
		return ""
	}
	result := variant.Template
	for k, v := range variant.Variables {
		result = strings.ReplaceAll(result, "{{"+k+"}}", v)
	}
	return result
}
