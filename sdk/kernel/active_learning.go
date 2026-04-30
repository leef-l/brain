// active_learning.go — 主动学习引擎
//
// MACCS Wave 5 — 学习系统进化。
// 在不确定时主动请求用户反馈，实现高效的半监督学习。
package kernel

import (
	"fmt"
	"math"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// 数据结构
// ---------------------------------------------------------------------------

// FeedbackRequest 反馈请求
type FeedbackRequest struct {
	RequestID string    `json:"request_id"`
	Type      string    `json:"type"`    // confirmation/choice/rating/freeform
	Question  string    `json:"question"`
	Context   string    `json:"context"` // 为什么问这个
	Options   []string  `json:"options,omitempty"`
	Priority  string    `json:"priority"` // high/medium/low
	TaskID    string    `json:"task_id,omitempty"`
	BrainKind string    `json:"brain_kind,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	Answered  bool      `json:"answered"`
}

// FeedbackResponse 反馈响应
type FeedbackResponse struct {
	RequestID   string    `json:"request_id"`
	Answer      string    `json:"answer"`
	Rating      float64   `json:"rating,omitempty"` // rating 类型时 1-5
	RespondedAt time.Time `json:"responded_at"`
	RespondedBy string    `json:"responded_by"` // user/system/auto
}

// UncertaintyScore 不确定性评分
type UncertaintyScore struct {
	TaskID      string  `json:"task_id"`
	BrainKind   string  `json:"brain_kind"`
	Uncertainty float64 `json:"uncertainty"` // 0-1, 1=完全不确定
	Reason      string  `json:"reason"`
	DataPoints  int     `json:"data_points"`
}

// ---------------------------------------------------------------------------
// 探索策略
// ---------------------------------------------------------------------------

// ExplorationStrategy 探索策略枚举
type ExplorationStrategy string

const (
	StrategyUncertaintySampling ExplorationStrategy = "uncertainty_sampling" // 优先问不确定的
	StrategyDiversitySampling   ExplorationStrategy = "diversity_sampling"   // 优先问多样性的
	StrategyExpectedImprovement ExplorationStrategy = "expected_improvement" // 优先问预期提升大的
	StrategyRandom              ExplorationStrategy = "random"              // 随机
)

// ---------------------------------------------------------------------------
// ActiveLearner 接口
// ---------------------------------------------------------------------------

// FeedbackStats 反馈统计信息
type FeedbackStats struct {
	TotalRequests   int           `json:"total_requests"`
	AnsweredCount   int           `json:"answered_count"`
	PendingCount    int           `json:"pending_count"`
	ExpiredCount    int           `json:"expired_count"`
	AvgResponseTime time.Duration `json:"avg_response_time"`
	ResponseRate    float64       `json:"response_rate"` // 0-1
}

// ActiveLearner 主动学习接口
type ActiveLearner interface {
	AssessUncertainty(taskID, brainKind string, historicalSuccess float64, dataPoints int) UncertaintyScore
	ShouldAskFeedback(score UncertaintyScore) bool
	GenerateQuestion(score UncertaintyScore) *FeedbackRequest
	RecordFeedback(response FeedbackResponse)
	GetPendingRequests() []FeedbackRequest
	GetFeedbackStats() FeedbackStats
	SetStrategy(strategy ExplorationStrategy)
}

// ---------------------------------------------------------------------------
// DefaultActiveLearner 实现
// ---------------------------------------------------------------------------

// DefaultActiveLearner 默认主动学习实现
type DefaultActiveLearner struct {
	mu         sync.RWMutex
	strategy   ExplorationStrategy
	requests   map[string]*FeedbackRequest  // requestID -> request
	responses  map[string]*FeedbackResponse // requestID -> response
	threshold  float64                      // 不确定性阈值，默认 0.6
	maxPending int                          // 最大待处理请求数，默认 5
}

// NewActiveLearner 使用默认配置创建主动学习器
func NewActiveLearner() *DefaultActiveLearner {
	return NewActiveLearnerWithConfig(0.6, 5, StrategyUncertaintySampling)
}

// NewActiveLearnerWithConfig 使用自定义配置创建主动学习器
func NewActiveLearnerWithConfig(threshold float64, maxPending int, strategy ExplorationStrategy) *DefaultActiveLearner {
	return &DefaultActiveLearner{
		strategy:   strategy,
		requests:   make(map[string]*FeedbackRequest),
		responses:  make(map[string]*FeedbackResponse),
		threshold:  threshold,
		maxPending: maxPending,
	}
}

// AssessUncertainty 评估不确定性
//
// 公式: uncertainty = 1.0 - 2*|successRate - 0.5| - min(dataPoints/20, 0.3)
//   - 数据点不足 (< 5) → 高不确定性 (0.8+)
//   - 成功率接近 0.5 → 高不确定性
//   - 成功率极端 (>0.9 或 <0.1) 且数据充足 → 低不确定性
func (d *DefaultActiveLearner) AssessUncertainty(taskID, brainKind string, historicalSuccess float64, dataPoints int) UncertaintyScore {
	successRate := math.Max(0, math.Min(1, historicalSuccess))

	// 基础不确定性：成功率越接近 0.5 越不确定
	uncertainty := 1.0 - 2*math.Abs(successRate-0.5)

	// 数据量惩罚：数据越多越确定
	dataPenalty := math.Min(float64(dataPoints)/20.0, 0.3)
	uncertainty -= dataPenalty

	// 数据点不足时强制拉高
	if dataPoints < 5 {
		uncertainty = math.Max(uncertainty, 0.8)
	}

	// 钳位到 [0, 1]
	uncertainty = math.Max(0, math.Min(1, uncertainty))

	reason := d.assessReason(successRate, dataPoints)

	return UncertaintyScore{
		TaskID:      taskID,
		BrainKind:   brainKind,
		Uncertainty: uncertainty,
		Reason:      reason,
		DataPoints:  dataPoints,
	}
}

// assessReason 根据数据情况判断不确定原因
func (d *DefaultActiveLearner) assessReason(successRate float64, dataPoints int) string {
	if dataPoints < 5 {
		return "insufficient_data"
	}
	if successRate >= 0.35 && successRate <= 0.65 {
		return "unstable_performance"
	}
	if successRate > 0.9 || successRate < 0.1 {
		return "stable"
	}
	return "moderate_uncertainty"
}

// ShouldAskFeedback 判断是否应该请求反馈
func (d *DefaultActiveLearner) ShouldAskFeedback(score UncertaintyScore) bool {
	d.mu.RLock()
	defer d.mu.RUnlock()

	if score.Uncertainty <= d.threshold {
		return false
	}
	pending := d.countPendingLocked()
	return pending < d.maxPending
}

// GenerateQuestion 根据不确定性评分生成反馈问题
func (d *DefaultActiveLearner) GenerateQuestion(score UncertaintyScore) *FeedbackRequest {
	d.mu.Lock()
	defer d.mu.Unlock()

	reqID := fmt.Sprintf("fb-%s-%s-%d", score.TaskID, score.BrainKind, time.Now().UnixNano())
	now := time.Now()
	expires := now.Add(24 * time.Hour)

	req := &FeedbackRequest{
		RequestID: reqID,
		TaskID:    score.TaskID,
		BrainKind: score.BrainKind,
		CreatedAt: now,
		ExpiresAt: &expires,
		Answered:  false,
	}

	switch score.Reason {
	case "insufficient_data":
		req.Type = "confirmation"
		req.Question = fmt.Sprintf("Brain %s 在任务 %s 上数据不足（%d 条），是否继续使用？",
			score.BrainKind, score.TaskID, score.DataPoints)
		req.Context = "数据点过少，无法可靠评估能力"
		req.Priority = "high"
	case "unstable_performance":
		req.Type = "rating"
		req.Question = fmt.Sprintf("Brain %s 在任务 %s 上表现不稳定，请评价最近的执行质量（1-5）",
			score.BrainKind, score.TaskID)
		req.Context = "成功率在 35%-65% 之间波动"
		req.Priority = "medium"
	case "moderate_uncertainty":
		req.Type = "choice"
		req.Question = fmt.Sprintf("Brain %s 处理任务 %s 时存在不确定性，您希望如何处理？",
			score.BrainKind, score.TaskID)
		req.Context = "系统对此 Brain 的能力判断不够明确"
		req.Options = []string{"继续观察", "增加样本", "切换 Brain", "手动确认"}
		req.Priority = "medium"
	default:
		req.Type = "freeform"
		req.Question = fmt.Sprintf("关于 Brain %s 执行任务 %s，您有什么反馈？",
			score.BrainKind, score.TaskID)
		req.Context = "常规反馈收集"
		req.Priority = "low"
	}

	d.requests[reqID] = req
	return req
}

// RecordFeedback 记录用户反馈
func (d *DefaultActiveLearner) RecordFeedback(response FeedbackResponse) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if response.RespondedAt.IsZero() {
		response.RespondedAt = time.Now()
	}
	d.responses[response.RequestID] = &response

	if req, ok := d.requests[response.RequestID]; ok {
		req.Answered = true
	}
}

// GetPendingRequests 获取未回答且未过期的请求
func (d *DefaultActiveLearner) GetPendingRequests() []FeedbackRequest {
	d.mu.RLock()
	defer d.mu.RUnlock()

	now := time.Now()
	var pending []FeedbackRequest
	for _, req := range d.requests {
		if req.Answered {
			continue
		}
		if req.ExpiresAt != nil && now.After(*req.ExpiresAt) {
			continue
		}
		pending = append(pending, *req)
	}
	return pending
}

// GetFeedbackStats 返回反馈统计信息
func (d *DefaultActiveLearner) GetFeedbackStats() FeedbackStats {
	d.mu.RLock()
	defer d.mu.RUnlock()

	now := time.Now()
	total := len(d.requests)
	answered := 0
	expired := 0
	var totalResponseTime time.Duration

	for reqID, req := range d.requests {
		if req.Answered {
			answered++
			if resp, ok := d.responses[reqID]; ok {
				totalResponseTime += resp.RespondedAt.Sub(req.CreatedAt)
			}
		} else if req.ExpiresAt != nil && now.After(*req.ExpiresAt) {
			expired++
		}
	}

	pending := total - answered - expired

	var avgResponseTime time.Duration
	if answered > 0 {
		avgResponseTime = totalResponseTime / time.Duration(answered)
	}

	var responseRate float64
	if total > 0 {
		responseRate = float64(answered) / float64(total)
	}

	return FeedbackStats{
		TotalRequests:   total,
		AnsweredCount:   answered,
		PendingCount:    pending,
		ExpiredCount:    expired,
		AvgResponseTime: avgResponseTime,
		ResponseRate:    responseRate,
	}
}

// SetStrategy 设置探索策略
func (d *DefaultActiveLearner) SetStrategy(strategy ExplorationStrategy) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.strategy = strategy
}

// countPendingLocked 统计待处理请求数（调用者须持有读锁）
func (d *DefaultActiveLearner) countPendingLocked() int {
	now := time.Now()
	count := 0
	for _, req := range d.requests {
		if !req.Answered && (req.ExpiresAt == nil || !now.After(*req.ExpiresAt)) {
			count++
		}
	}
	return count
}
