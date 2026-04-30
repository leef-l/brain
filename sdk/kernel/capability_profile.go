// capability_profile.go — 能力画像评估器
//
// 为每个 Brain 构建能力雷达图，提供多维度评分、成长追踪和改进建议。
// EWMA 平滑更新 + 自动趋势检测 + 快照对比。

package kernel

import (
	"fmt"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// 默认维度与权重
// ---------------------------------------------------------------------------

var defaultDimensions = []string{
	"coding", "testing", "architecture", "debugging", "documentation", "performance",
}

var defaultWeights = map[string]float64{
	"coding":        0.25,
	"testing":       0.15,
	"architecture":  0.20,
	"debugging":     0.15,
	"documentation": 0.10,
	"performance":   0.15,
}

// ---------------------------------------------------------------------------
// CapabilityDimension — 单个能力维度
// ---------------------------------------------------------------------------

// CapabilityDimension 描述 Brain 在某个维度上的能力评分。
type CapabilityDimension struct {
	Name        string    `json:"name"`
	Score       float64   `json:"score"`        // 0-100
	SampleCount int       `json:"sample_count"`
	Trend       string    `json:"trend"`         // improving / stable / declining
	LastUpdated time.Time `json:"last_updated"`
}

// ---------------------------------------------------------------------------
// BrainCapabilityRadar — 能力雷达图
// ---------------------------------------------------------------------------

// BrainCapabilityRadar 汇总某个 Brain 的多维度能力评分。
type BrainCapabilityRadar struct {
	BrainKind    string                `json:"brain_kind"`
	Dimensions   []CapabilityDimension `json:"dimensions"`
	OverallScore float64               `json:"overall_score"`
	Level        string                `json:"level"` // novice / intermediate / advanced / expert
	UpdatedAt    time.Time             `json:"updated_at"`
}

// ---------------------------------------------------------------------------
// GrowthRecord — 成长记录
// ---------------------------------------------------------------------------

// GrowthRecord 记录某个维度上的显著分数变化。
type GrowthRecord struct {
	RecordID  string    `json:"record_id"`
	BrainKind string    `json:"brain_kind"`
	Dimension string    `json:"dimension"`
	OldScore  float64   `json:"old_score"`
	NewScore  float64   `json:"new_score"`
	Delta     float64   `json:"delta"`
	Reason    string    `json:"reason"`
	Timestamp time.Time `json:"timestamp"`
}

// ---------------------------------------------------------------------------
// CapabilitySnapshot — 历史快照
// ---------------------------------------------------------------------------

// CapabilitySnapshot 用于快照某一时刻的能力评分，便于历史对比。
type CapabilitySnapshot struct {
	SnapshotID   string             `json:"snapshot_id"`
	BrainKind    string             `json:"brain_kind"`
	Scores       map[string]float64 `json:"scores"` // dimension -> score
	OverallScore float64            `json:"overall_score"`
	TakenAt      time.Time          `json:"taken_at"`
}

// ---------------------------------------------------------------------------
// CapabilityAssessor 接口
// ---------------------------------------------------------------------------

// CapabilityAssessor 定义能力画像评估的核心操作。
type CapabilityAssessor interface {
	RecordOutcome(brainKind, dimension string, success bool, quality float64)
	GetRadar(brainKind string) *BrainCapabilityRadar
	GetGrowthHistory(brainKind string) []GrowthRecord
	TakeSnapshot(brainKind string) *CapabilitySnapshot
	CompareSnapshots(a, b *CapabilitySnapshot) map[string]float64
	SuggestImprovement(brainKind string) []string
	AllRadars() map[string]*BrainCapabilityRadar
}

// ---------------------------------------------------------------------------
// DefaultCapabilityAssessor 实现
// ---------------------------------------------------------------------------

// DefaultCapabilityAssessor 基于 EWMA 的能力画像评估器。
type DefaultCapabilityAssessor struct {
	mu      sync.RWMutex
	radars  map[string]*BrainCapabilityRadar // brainKind -> radar
	history map[string][]GrowthRecord        // brainKind -> growth records
	weights map[string]float64               // dimension -> weight
	seqID   int                              // 用于生成唯一 ID
}

// NewCapabilityAssessor 创建默认评估器。
func NewCapabilityAssessor() *DefaultCapabilityAssessor {
	w := make(map[string]float64, len(defaultWeights))
	for k, v := range defaultWeights {
		w[k] = v
	}
	return &DefaultCapabilityAssessor{
		radars:  make(map[string]*BrainCapabilityRadar),
		history: make(map[string][]GrowthRecord),
		weights: w,
	}
}

// RecordOutcome 记录一次任务结果，更新对应维度评分。
func (a *DefaultCapabilityAssessor) RecordOutcome(brainKind, dimension string, success bool, quality float64) {
	a.mu.Lock()
	defer a.mu.Unlock()

	now := time.Now()
	radar := a.ensureRadar(brainKind, now)

	// 找到或创建维度
	idx := -1
	for i := range radar.Dimensions {
		if radar.Dimensions[i].Name == dimension {
			idx = i
			break
		}
	}
	if idx < 0 {
		radar.Dimensions = append(radar.Dimensions, CapabilityDimension{
			Name:        dimension,
			Score:       50, // 初始中位分
			Trend:       "stable",
			LastUpdated: now,
		})
		idx = len(radar.Dimensions) - 1
	}

	dim := &radar.Dimensions[idx]
	oldScore := dim.Score

	// EWMA 更新: newScore = 0.8 * old + 0.2 * quality * 100
	inputScore := quality * 100
	if inputScore > 100 {
		inputScore = 100
	}
	if inputScore < 0 {
		inputScore = 0
	}
	dim.Score = 0.8*oldScore + 0.2*inputScore
	dim.SampleCount++
	dim.LastUpdated = now

	delta := dim.Score - oldScore

	// 显著变化 (|delta| > 5) 时记录成长
	if delta > 5 || delta < -5 {
		reason := "performance improved"
		if delta < 0 {
			reason = "performance declined"
		}
		a.seqID++
		rec := GrowthRecord{
			RecordID:  fmt.Sprintf("gr-%s-%s-%d", brainKind, dimension, a.seqID),
			BrainKind: brainKind,
			Dimension: dimension,
			OldScore:  oldScore,
			NewScore:  dim.Score,
			Delta:     delta,
			Reason:    reason,
			Timestamp: now,
		}
		a.history[brainKind] = append(a.history[brainKind], rec)
	}

	// 更新趋势：基于最近 growth records
	dim.Trend = a.computeTrend(brainKind, dimension)

	// 重新计算综合分与等级
	radar.OverallScore = a.calcOverall(radar)
	radar.Level = computeLevel(radar.OverallScore)
	radar.UpdatedAt = now
}

// GetRadar 返回指定 brain 的雷达图深拷贝。
func (a *DefaultCapabilityAssessor) GetRadar(brainKind string) *BrainCapabilityRadar {
	a.mu.RLock()
	defer a.mu.RUnlock()

	r, ok := a.radars[brainKind]
	if !ok {
		return nil
	}
	return a.copyRadar(r)
}

// GetGrowthHistory 返回成长记录切片的拷贝。
func (a *DefaultCapabilityAssessor) GetGrowthHistory(brainKind string) []GrowthRecord {
	a.mu.RLock()
	defer a.mu.RUnlock()

	src := a.history[brainKind]
	if len(src) == 0 {
		return nil
	}
	out := make([]GrowthRecord, len(src))
	copy(out, src)
	return out
}

// TakeSnapshot 从当前雷达图创建快照。
func (a *DefaultCapabilityAssessor) TakeSnapshot(brainKind string) *CapabilitySnapshot {
	a.mu.Lock()
	defer a.mu.Unlock()

	r, ok := a.radars[brainKind]
	if !ok {
		return nil
	}
	a.seqID++
	snap := &CapabilitySnapshot{
		SnapshotID:   fmt.Sprintf("snap-%s-%d", brainKind, a.seqID),
		BrainKind:    brainKind,
		Scores:       make(map[string]float64, len(r.Dimensions)),
		OverallScore: r.OverallScore,
		TakenAt:      time.Now(),
	}
	for _, d := range r.Dimensions {
		snap.Scores[d.Name] = d.Score
	}
	return snap
}

// CompareSnapshots 对比两个快照，返回每个维度的分差。
func (a *DefaultCapabilityAssessor) CompareSnapshots(x, y *CapabilitySnapshot) map[string]float64 {
	if x == nil || y == nil {
		return nil
	}
	dims := make(map[string]struct{})
	for k := range x.Scores {
		dims[k] = struct{}{}
	}
	for k := range y.Scores {
		dims[k] = struct{}{}
	}
	result := make(map[string]float64, len(dims))
	for d := range dims {
		result[d] = y.Scores[d] - x.Scores[d]
	}
	return result
}

// SuggestImprovement 找到得分最低的 2-3 个维度，返回改进建议。
func (a *DefaultCapabilityAssessor) SuggestImprovement(brainKind string) []string {
	a.mu.RLock()
	defer a.mu.RUnlock()

	r, ok := a.radars[brainKind]
	if !ok || len(r.Dimensions) == 0 {
		return nil
	}

	// 按分数升序收集
	type ds struct {
		name  string
		score float64
	}
	sorted := make([]ds, len(r.Dimensions))
	for i, d := range r.Dimensions {
		sorted[i] = ds{name: d.Name, score: d.Score}
	}
	for i := 0; i < len(sorted); i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[j].score < sorted[i].score {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}

	count := 3
	if len(sorted) < count {
		count = len(sorted)
	}

	suggestions := make([]string, 0, count)
	for i := 0; i < count; i++ {
		s := sorted[i]
		suggestions = append(suggestions, fmt.Sprintf(
			"[%s] score %.1f — recommend focused practice to improve %s capabilities",
			s.name, s.score, s.name,
		))
	}
	return suggestions
}

// AllRadars 返回所有 brain 的雷达图深拷贝。
func (a *DefaultCapabilityAssessor) AllRadars() map[string]*BrainCapabilityRadar {
	a.mu.RLock()
	defer a.mu.RUnlock()

	out := make(map[string]*BrainCapabilityRadar, len(a.radars))
	for k, r := range a.radars {
		out[k] = a.copyRadar(r)
	}
	return out
}

// ---------------------------------------------------------------------------
// 内部辅助
// ---------------------------------------------------------------------------

func (a *DefaultCapabilityAssessor) ensureRadar(brainKind string, now time.Time) *BrainCapabilityRadar {
	r, ok := a.radars[brainKind]
	if ok {
		return r
	}
	r = &BrainCapabilityRadar{
		BrainKind:  brainKind,
		Dimensions: make([]CapabilityDimension, 0, len(defaultDimensions)),
		Level:      "novice",
		UpdatedAt:  now,
	}
	for _, name := range defaultDimensions {
		r.Dimensions = append(r.Dimensions, CapabilityDimension{
			Name:        name,
			Score:       50,
			Trend:       "stable",
			LastUpdated: now,
		})
	}
	r.OverallScore = 50
	r.Level = computeLevel(r.OverallScore)
	a.radars[brainKind] = r
	return r
}

func (a *DefaultCapabilityAssessor) calcOverall(r *BrainCapabilityRadar) float64 {
	totalWeight := 0.0
	weightedSum := 0.0
	for _, d := range r.Dimensions {
		w, ok := a.weights[d.Name]
		if !ok {
			w = 0.1 // 未知维度给默认权重
		}
		weightedSum += d.Score * w
		totalWeight += w
	}
	if totalWeight == 0 {
		return 0
	}
	return weightedSum / totalWeight
}

func (a *DefaultCapabilityAssessor) computeTrend(brainKind, dimension string) string {
	recs := a.history[brainKind]
	// 只看最近 5 条该维度的记录
	var recent []GrowthRecord
	for i := len(recs) - 1; i >= 0 && len(recent) < 5; i-- {
		if recs[i].Dimension == dimension {
			recent = append(recent, recs[i])
		}
	}
	if len(recent) < 2 {
		return "stable"
	}
	posCount, negCount := 0, 0
	for _, r := range recent {
		if r.Delta > 0 {
			posCount++
		} else if r.Delta < 0 {
			negCount++
		}
	}
	if posCount > negCount {
		return "improving"
	}
	if negCount > posCount {
		return "declining"
	}
	return "stable"
}

func (a *DefaultCapabilityAssessor) copyRadar(r *BrainCapabilityRadar) *BrainCapabilityRadar {
	cp := &BrainCapabilityRadar{
		BrainKind:    r.BrainKind,
		OverallScore: r.OverallScore,
		Level:        r.Level,
		UpdatedAt:    r.UpdatedAt,
		Dimensions:   make([]CapabilityDimension, len(r.Dimensions)),
	}
	copy(cp.Dimensions, r.Dimensions)
	return cp
}

// computeLevel 根据综合分计算等级。
func computeLevel(overallScore float64) string {
	switch {
	case overallScore < 30:
		return "novice"
	case overallScore < 55:
		return "intermediate"
	case overallScore < 80:
		return "advanced"
	default:
		return "expert"
	}
}
