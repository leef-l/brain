package kernel

import (
	"math"
	"strings"
	"time"

	"github.com/leef-l/brain/sdk/agent"
)

// ComplexityEstimate 复杂度预估结果。
type ComplexityEstimate struct {
	EstimatedTurns  int           `json:"estimated_turns"`
	EstimatedTokens int           `json:"estimated_tokens"`
	EstimatedTime   time.Duration `json:"estimated_time"`
	Confidence      float64       `json:"confidence"` // 0-1
	Source          string        `json:"source"`      // "learning"/"heuristic"
}

// ComplexityEstimator 任务复杂度预估器。
// 优先使用 LearningEngine 的历史数据；样本不足时尝试 TransferLearner
// 跨项目迁移先验；最终 fallback 到启发式。
type ComplexityEstimator struct {
	learner  *LearningEngine
	transfer TransferLearner
}

// NewComplexityEstimator 创建复杂度预估器。learner 可为 nil（退化为纯启发式）。
func NewComplexityEstimator(learner *LearningEngine) *ComplexityEstimator {
	return &ComplexityEstimator{learner: learner}
}

// NewComplexityEstimatorWithTransfer 创建带迁移学习能力的复杂度预估器。
// transfer 可为 nil（退化为只用 learner + 启发式）。
func NewComplexityEstimatorWithTransfer(learner *LearningEngine, transfer TransferLearner) *ComplexityEstimator {
	return &ComplexityEstimator{learner: learner, transfer: transfer}
}

// Estimate 预估单个子任务的复杂度。
func (e *ComplexityEstimator) Estimate(task PlanSubTask) ComplexityEstimate {
	// 1. 尝试从 LearningEngine 获取历史数据
	if e.learner != nil {
		if est, ok := e.estimateFromLearning(task); ok {
			return est
		}
	}
	// 2. 历史样本不足 — 尝试跨项目迁移学习先验
	if e.transfer != nil {
		if est, ok := e.estimateFromTransfer(task); ok {
			return est
		}
	}
	// 3. Fallback 到启发式
	return e.estimateHeuristic(task)
}

// learningMinSamples 基于学习数据预估所需的最低样本数。
const learningMinSamples = 3

// estimateFromLearning 基于学习数据预估。
func (e *ComplexityEstimator) estimateFromLearning(task PlanSubTask) (ComplexityEstimate, bool) {
	profiles := e.learner.Profiles()
	profile, ok := profiles[task.Kind]
	if !ok {
		return ComplexityEstimate{}, false
	}

	// 尝试匹配任务类型：先精确匹配 Kind 字符串，再遍历查找包含关系
	score := e.findBestTaskScore(profile, task.Kind)
	if score == nil || score.SampleCount < learningMinSamples {
		return ComplexityEstimate{}, false
	}

	// Speed.Value ∈ [0,1]，值越高表示越快（turns 越少）
	// 将 speed 映射到 turns：speed=1 → 5 turns, speed=0 → 30 turns
	speedVal := clampFloat(score.Speed.Value, 0, 1)
	turns := int(math.Round(5 + 25*(1-speedVal)))

	// 如果子任务自带 EstimatedTurns 且非零，取加权平均（学习数据权重更高）
	if task.EstimatedTurns > 0 {
		turns = int(math.Round(0.7*float64(turns) + 0.3*float64(task.EstimatedTurns)))
	}
	if turns < 1 {
		turns = 1
	}

	// Cost.Value 用于估算 token：cost 高 → token 多
	costVal := clampFloat(score.Cost.Value, 0, 1)
	tokens := int(math.Round(float64(turns) * (2000 + 6000*costVal)))

	// 用 LatencyMs 估算时间，如果有的话
	var estTime time.Duration
	if score.LatencyMs.Value > 0 {
		estTime = time.Duration(score.LatencyMs.Value*float64(turns)) * time.Millisecond
	} else {
		estTime = time.Duration(turns) * 30 * time.Second
	}

	conf := wilsonConfidence(score.SampleCount)

	return ComplexityEstimate{
		EstimatedTurns:  turns,
		EstimatedTokens: tokens,
		EstimatedTime:   estTime,
		Confidence:      conf,
		Source:          "learning",
	}, true
}

// taskFingerprint 从 PlanSubTask 派生 (category, tags) 二元组用于跨项目迁移检索。
// category 来自 Domain（如 web_app/api_service），为空时退回到 Kind。
// tags 包含 Language、Kind 字符串以及（若有）Domain。
func taskFingerprint(task PlanSubTask) (category string, tags []string) {
	category = strings.TrimSpace(task.Domain)
	if category == "" {
		category = strings.TrimSpace(string(task.Kind))
	}
	if lang := strings.TrimSpace(task.Language); lang != "" {
		tags = append(tags, "lang:"+lang)
	}
	if kind := strings.TrimSpace(string(task.Kind)); kind != "" {
		tags = append(tags, "kind:"+kind)
	}
	if dom := strings.TrimSpace(task.Domain); dom != "" {
		tags = append(tags, "domain:"+dom)
	}
	return category, tags
}

// estimateFromTransfer 基于跨项目迁移学习的先验生成估算。
// 仅当本地 LearningEngine 样本不足以决断时调用。
// 用相似度加权平均 AvgTurns 派生 turns，相似度作为置信度上限。
func (e *ComplexityEstimator) estimateFromTransfer(task PlanSubTask) (ComplexityEstimate, bool) {
	category, tags := taskFingerprint(task)
	// 检索 top3 相似项目经验
	candidates := e.transfer.FindSimilar(category, tags, 3)
	if len(candidates) == 0 {
		return ComplexityEstimate{}, false
	}

	// 用相似度作为权重加权平均 AvgTurns 与 SuccessRate
	var weightedTurns, weightedSuccess, weightSum float64
	for _, c := range candidates {
		if c.Experience == nil || c.Similarity <= 0 || c.Experience.AvgTurns <= 0 {
			continue
		}
		w := c.Similarity
		weightedTurns += w * c.Experience.AvgTurns
		weightedSuccess += w * c.Experience.SuccessRate
		weightSum += w
	}
	if weightSum == 0 {
		return ComplexityEstimate{}, false
	}

	turns := int(math.Round(weightedTurns / weightSum))
	if turns < 1 {
		turns = 1
	}

	// 与子任务自带预估融合（迁移先验权重 0.6 / 自带 0.4）
	if task.EstimatedTurns > 0 {
		turns = int(math.Round(0.6*float64(turns) + 0.4*float64(task.EstimatedTurns)))
	}

	// 成功率低 → 适度上调预算（失败需要更多 turns 重试）
	successRate := weightedSuccess / weightSum
	if successRate > 0 && successRate < 1 {
		turns = int(math.Round(float64(turns) * (2.0 - successRate)))
	}
	if turns < 1 {
		turns = 1
	}

	tokens := turns * 4500
	estTime := time.Duration(turns) * 30 * time.Second

	// 置信度：取最高相似度，但封顶 0.5（毕竟是跨项目先验，不如本地样本可靠）
	maxSim := candidates[0].Similarity
	conf := clampFloat(maxSim, 0, 0.5)

	return ComplexityEstimate{
		EstimatedTurns:  turns,
		EstimatedTokens: tokens,
		EstimatedTime:   estTime,
		Confidence:      conf,
		Source:          "transferred",
	}, true
}

// findBestTaskScore 在 profile 中查找与 kind 最匹配的 TaskTypeScore。
func (e *ComplexityEstimator) findBestTaskScore(profile *BrainCapabilityProfile, kind agent.Kind) *TaskTypeScore {
	kindStr := string(kind)

	// 精确匹配
	if ts, ok := profile.TaskScores[kindStr]; ok {
		return ts
	}

	// 模糊匹配：找第一个包含关系的
	lower := strings.ToLower(kindStr)
	for key, ts := range profile.TaskScores {
		if strings.Contains(strings.ToLower(key), lower) || strings.Contains(lower, strings.ToLower(key)) {
			return ts
		}
	}

	return nil
}

// wilsonConfidence 基于样本数计算 Wilson 置信度。
// 样本越多，置信度越高，上限趋近 0.95。
func wilsonConfidence(sampleCount int) float64 {
	if sampleCount <= 0 {
		return 0
	}
	n := float64(sampleCount)
	// Wilson score interval 简化：confidence = 1 - 1/(1 + sqrt(n/10))
	// n=3 → ~0.35, n=10 → ~0.50, n=50 → ~0.69, n=100 → ~0.76, n=500 → ~0.88
	conf := 1.0 - 1.0/(1.0+math.Sqrt(n/10.0))
	return clampFloat(conf, 0, 0.95)
}

// heuristic 关键词及对应的 turns 增量
var heuristicKeywords = []struct {
	keywords []string
	delta    int
}{
	{[]string{"implement", "实现"}, 15},
	{[]string{"design", "设计"}, 12},
	{[]string{"refactor", "重构"}, 10},
	{[]string{"test", "测试"}, 8},
	{[]string{"review", "审核"}, 5},
	{[]string{"fix", "修复"}, 5},
}

// estimateHeuristic 启发式预估。
func (e *ComplexityEstimator) estimateHeuristic(task PlanSubTask) ComplexityEstimate {
	baseTurns := 10
	instruction := strings.ToLower(task.Instruction)

	for _, kw := range heuristicKeywords {
		for _, word := range kw.keywords {
			if strings.Contains(instruction, word) {
				baseTurns += kw.delta
				break // 同组关键词只加一次
			}
		}
	}

	// 验证标准每多一条 +3
	baseTurns += len(task.VerificationCriteria) * 3

	// 如果子任务自带 EstimatedTurns，取平均（启发式权重较低）
	if task.EstimatedTurns > 0 {
		baseTurns = int(math.Round(0.4*float64(baseTurns) + 0.6*float64(task.EstimatedTurns)))
	}
	if baseTurns < 1 {
		baseTurns = 1
	}

	tokens := baseTurns * 4000
	estTime := time.Duration(baseTurns) * 30 * time.Second

	return ComplexityEstimate{
		EstimatedTurns:  baseTurns,
		EstimatedTokens: tokens,
		EstimatedTime:   estTime,
		Confidence:      0.3,
		Source:          "heuristic",
	}
}

// EstimatePlan 预估整个 TaskPlan 的复杂度。
func (e *ComplexityEstimator) EstimatePlan(plan *TaskPlan) ComplexityEstimate {
	if plan == nil || len(plan.SubTasks) == 0 {
		return ComplexityEstimate{
			EstimatedTurns:  0,
			EstimatedTokens: 0,
			EstimatedTime:   0,
			Confidence:      0,
			Source:          "heuristic",
		}
	}

	var (
		totalTurns     int
		totalTokens    int
		totalTime      time.Duration
		minConf        = 1.0
		hasLearning    bool
		hasTransferred bool
	)

	for _, sub := range plan.SubTasks {
		est := e.Estimate(sub)
		totalTurns += est.EstimatedTurns
		totalTokens += est.EstimatedTokens
		totalTime += est.EstimatedTime
		if est.Confidence < minConf {
			minConf = est.Confidence
		}
		switch est.Source {
		case "learning":
			hasLearning = true
		case "transferred":
			hasTransferred = true
		}
	}

	source := "heuristic"
	switch {
	case hasLearning:
		source = "learning"
	case hasTransferred:
		source = "transferred"
	}

	return ComplexityEstimate{
		EstimatedTurns:  totalTurns,
		EstimatedTokens: totalTokens,
		EstimatedTime:   totalTime,
		Confidence:      minConf,
		Source:          source,
	}
}

// SuggestBudget 基于预估建议预算 turns 数（加安全边际）。
func (e *ComplexityEstimator) SuggestBudget(estimate ComplexityEstimate) int {
	var multiplier float64
	switch {
	case estimate.Confidence > 0.7:
		multiplier = 1.3
	case estimate.Confidence >= 0.3:
		multiplier = 1.5
	default:
		multiplier = 2.0
	}

	budget := int(math.Ceil(float64(estimate.EstimatedTurns) * multiplier))
	if budget < 10 {
		budget = 10
	}
	return budget
}

// clampFloat 将 v 限制在 [lo, hi] 范围内。
func clampFloat(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
