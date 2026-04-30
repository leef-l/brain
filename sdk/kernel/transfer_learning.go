// transfer_learning.go — 迁移学习引擎：跨项目经验复用
//
// MACCS Wave 5 学习系统进化。将一个项目的成功经验（模式、踩坑）
// 迁移到类别和标签相似的新项目，提升冷启动阶段的成功率。
package kernel

import (
	"context"
	"math"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// 数据模型
// ---------------------------------------------------------------------------

// ProjectExperience 项目经验
type ProjectExperience struct {
	ExperienceID string              `json:"experience_id"`
	ProjectID    string              `json:"project_id"`
	Category     string              `json:"category"`     // web_app/cli_tool/game/api_service 等
	Tags         []string            `json:"tags"`
	TaskCount    int                 `json:"task_count"`
	SuccessRate  float64             `json:"success_rate"` // 0-1
	AvgTurns     float64             `json:"avg_turns"`
	Patterns     []ExperiencePattern `json:"patterns"`    // 成功模式
	Pitfalls     []ExperiencePitfall `json:"pitfalls"`    // 踩过的坑
	BrainUsage   map[string]float64  `json:"brain_usage"` // brainKind -> 使用比例
	Duration     time.Duration       `json:"duration"`
	CreatedAt    time.Time           `json:"created_at"`
}

// ExperiencePattern 成功模式
type ExperiencePattern struct {
	PatternID     string  `json:"pattern_id"`
	Name          string  `json:"name"`
	Description   string  `json:"description"`
	Applicability float64 `json:"applicability"` // 适用性 0-1
	SuccessBoost  float64 `json:"success_boost"` // 采用后成功率提升
}

// ExperiencePitfall 踩坑记录
type ExperiencePitfall struct {
	PitfallID   string `json:"pitfall_id"`
	Description string `json:"description"`
	Severity    string `json:"severity"`  // high/medium/low
	Avoidance   string `json:"avoidance"` // 如何避免
}

// TransferCandidate 迁移候选
type TransferCandidate struct {
	Experience   *ProjectExperience  `json:"experience"`
	Similarity   float64             `json:"similarity"`              // 0-1
	Transferable []ExperiencePattern `json:"transferable"`            // 可迁移的模式
	Warnings     []string            `json:"warnings,omitempty"`      // 不适用的警告
}

// ---------------------------------------------------------------------------
// TransferLearner 接口
// ---------------------------------------------------------------------------

// TransferLearner 迁移学习器：跨项目经验检索与迁移
type TransferLearner interface {
	RecordExperience(exp ProjectExperience)
	FindSimilar(category string, tags []string, topK int) []TransferCandidate
	Transfer(candidate TransferCandidate) []ExperiencePattern
	ComputeSimilarity(a, b *ProjectExperience) float64
	GetExperience(experienceID string) (*ProjectExperience, bool)
	AllExperiences() []ProjectExperience
}

// ---------------------------------------------------------------------------
// DefaultTransferLearner
// ---------------------------------------------------------------------------

// DefaultTransferLearner 基于内存的迁移学习实现
type DefaultTransferLearner struct {
	mu          sync.RWMutex
	experiences map[string]ProjectExperience // experienceID -> exp
}

// NewTransferLearner 创建迁移学习器
func NewTransferLearner() *DefaultTransferLearner {
	return &DefaultTransferLearner{
		experiences: make(map[string]ProjectExperience),
	}
}

// RecordExperience 记录项目经验
func (tl *DefaultTransferLearner) RecordExperience(exp ProjectExperience) {
	tl.mu.Lock()
	defer tl.mu.Unlock()
	tl.experiences[exp.ExperienceID] = exp
}

// FindSimilar 查找相似经验，返回 topK 个候选
// 相似度 = category_match(0.4) + tag_jaccard(0.3) + brain_usage_cosine(0.3)
func (tl *DefaultTransferLearner) FindSimilar(category string, tags []string, topK int) []TransferCandidate {
	tl.mu.RLock()
	defer tl.mu.RUnlock()

	// 构造一个虚拟查询经验用于相似度计算
	query := &ProjectExperience{
		Category: category,
		Tags:     tags,
	}

	var candidates []TransferCandidate
	for _, exp := range tl.experiences {
		expCopy := exp
		sim := tl.computeSimilarityLocked(query, &expCopy)
		if sim <= 0 {
			continue
		}
		transferable := filterTransferable(expCopy.Patterns)
		var warnings []string
		if sim < 0.3 {
			warnings = append(warnings, "相似度较低，迁移效果可能有限")
		}
		candidates = append(candidates, TransferCandidate{
			Experience:   &expCopy,
			Similarity:   sim,
			Transferable: transferable,
			Warnings:     warnings,
		})
	}

	// 按相似度降序排序
	sortTransferCandidates(candidates)

	if topK > 0 && len(candidates) > topK {
		candidates = candidates[:topK]
	}
	return candidates
}

// Transfer 从候选中迁移适用模式（applicability > 0.5）
func (tl *DefaultTransferLearner) Transfer(candidate TransferCandidate) []ExperiencePattern {
	return filterTransferable(candidate.Experience.Patterns)
}

// ComputeSimilarity 计算两个项目经验的相似度
func (tl *DefaultTransferLearner) ComputeSimilarity(a, b *ProjectExperience) float64 {
	tl.mu.RLock()
	defer tl.mu.RUnlock()
	return tl.computeSimilarityLocked(a, b)
}

func (tl *DefaultTransferLearner) computeSimilarityLocked(a, b *ProjectExperience) float64 {
	// category 完全匹配权重 0.4
	var catScore float64
	if a.Category == b.Category && a.Category != "" {
		catScore = 1.0
	}

	// tag jaccard 权重 0.3
	tagScore := jaccardSimilarity(a.Tags, b.Tags)

	// brain_usage 余弦相似度权重 0.3
	usageScore := cosineSimilarity(a.BrainUsage, b.BrainUsage)

	return 0.4*catScore + 0.3*tagScore + 0.3*usageScore
}

// GetExperience 按 ID 查找经验
func (tl *DefaultTransferLearner) GetExperience(experienceID string) (*ProjectExperience, bool) {
	tl.mu.RLock()
	defer tl.mu.RUnlock()
	exp, ok := tl.experiences[experienceID]
	if !ok {
		return nil, false
	}
	return &exp, true
}

// AllExperiences 返回全部经验
func (tl *DefaultTransferLearner) AllExperiences() []ProjectExperience {
	tl.mu.RLock()
	defer tl.mu.RUnlock()
	result := make([]ProjectExperience, 0, len(tl.experiences))
	for _, exp := range tl.experiences {
		result = append(result, exp)
	}
	return result
}

// ---------------------------------------------------------------------------
// ExperienceStore 持久化接口
// ---------------------------------------------------------------------------

// ExperienceStore 经验持久化接口
type ExperienceStore interface {
	Save(ctx context.Context, exp *ProjectExperience) error
	Load(ctx context.Context, experienceID string) (*ProjectExperience, error)
	List(ctx context.Context) ([]*ProjectExperience, error)
}

// MemExperienceStore 内存实现
type MemExperienceStore struct {
	mu   sync.RWMutex
	data map[string]*ProjectExperience
}

// NewMemExperienceStore 创建内存经验存储
func NewMemExperienceStore() *MemExperienceStore {
	return &MemExperienceStore{data: make(map[string]*ProjectExperience)}
}

// Save 保存经验
func (s *MemExperienceStore) Save(_ context.Context, exp *ProjectExperience) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	clone := *exp
	s.data[exp.ExperienceID] = &clone
	return nil
}

// Load 加载经验
func (s *MemExperienceStore) Load(_ context.Context, experienceID string) (*ProjectExperience, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	exp, ok := s.data[experienceID]
	if !ok {
		return nil, nil
	}
	clone := *exp
	return &clone, nil
}

// List 列出全部经验
func (s *MemExperienceStore) List(_ context.Context) ([]*ProjectExperience, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]*ProjectExperience, 0, len(s.data))
	for _, exp := range s.data {
		clone := *exp
		result = append(result, &clone)
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// 工厂函数
// ---------------------------------------------------------------------------

// NewProjectExperience 创建项目经验（自动填充 ID 和时间）
func NewProjectExperience(projectID, category string) *ProjectExperience {
	return &ProjectExperience{
		ExperienceID: projectID + "-" + category + "-" + time.Now().Format("20060102150405"),
		ProjectID:    projectID,
		Category:     category,
		BrainUsage:   make(map[string]float64),
		CreatedAt:    time.Now(),
	}
}

// ---------------------------------------------------------------------------
// 内部辅助
// ---------------------------------------------------------------------------

// jaccardSimilarity 计算两个字符串集合的 Jaccard 相似度
func jaccardSimilarity(a, b []string) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 0
	}
	setA := make(map[string]struct{}, len(a))
	for _, v := range a {
		setA[v] = struct{}{}
	}
	setB := make(map[string]struct{}, len(b))
	for _, v := range b {
		setB[v] = struct{}{}
	}
	var intersection int
	for k := range setA {
		if _, ok := setB[k]; ok {
			intersection++
		}
	}
	union := len(setA) + len(setB) - intersection
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

// cosineSimilarity 计算两个向量（map 表示）的余弦相似度
func cosineSimilarity(a, b map[string]float64) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	var dot, normA, normB float64
	for k, va := range a {
		normA += va * va
		if vb, ok := b[k]; ok {
			dot += va * vb
		}
	}
	for _, vb := range b {
		normB += vb * vb
	}
	denom := math.Sqrt(normA) * math.Sqrt(normB)
	if denom == 0 {
		return 0
	}
	return dot / denom
}

// filterTransferable 筛选 applicability > 0.5 的模式
func filterTransferable(patterns []ExperiencePattern) []ExperiencePattern {
	var result []ExperiencePattern
	for _, p := range patterns {
		if p.Applicability > 0.5 {
			result = append(result, p)
		}
	}
	return result
}

// sortTransferCandidates 按 Similarity 降序排序
func sortTransferCandidates(candidates []TransferCandidate) {
	for i := 1; i < len(candidates); i++ {
		for j := i; j > 0 && candidates[j].Similarity > candidates[j-1].Similarity; j-- {
			candidates[j], candidates[j-1] = candidates[j-1], candidates[j]
		}
	}
}
