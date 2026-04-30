// pattern_extraction.go — 项目模式提取引擎
//
// MACCS Wave 5 Batch 2。从多个项目的执行历史中自动提取最佳实践模式，
// 形成模式库，供新项目冷启动或规划阶段参考。
package kernel

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// 数据模型
// ---------------------------------------------------------------------------

// ProjectPattern 项目模式
type ProjectPattern struct {
	PatternID   string             `json:"pattern_id"`
	Name        string             `json:"name"`
	Category    string             `json:"category"`     // architecture/workflow/tool_usage/brain_selection/error_handling
	Description string             `json:"description"`
	Conditions  []PatternCondition `json:"conditions"`   // 触发条件
	Actions     []PatternAction    `json:"actions"`      // 推荐动作
	SuccessRate float64            `json:"success_rate"` // 采用此模式的成功率
	Frequency   int                `json:"frequency"`    // 被观察到的次数
	Confidence  float64            `json:"confidence"`   // 置信度 0-1
	Source      []string           `json:"source"`       // 来源项目 ID
	CreatedAt   time.Time          `json:"created_at"`
	LastSeenAt  time.Time          `json:"last_seen_at"`
}

// PatternCondition 模式触发条件
type PatternCondition struct {
	Field    string `json:"field"`    // project_category/task_count/complexity/brain_kind
	Operator string `json:"operator"` // eq/neq/gt/lt/contains
	Value    string `json:"value"`
}

// PatternAction 模式推荐动作
type PatternAction struct {
	ActionType string `json:"action_type"` // use_brain/set_budget/add_task/skip_phase/set_parallel
	Target     string `json:"target"`
	Value      string `json:"value"`
	Rationale  string `json:"rationale"`
}

// PatternMatchResult 匹配结果
type PatternMatchResult struct {
	Pattern           *ProjectPattern `json:"pattern"`
	MatchScore        float64         `json:"match_score"` // 0-1
	MatchedConditions int             `json:"matched_conditions"`
	TotalConditions   int             `json:"total_conditions"`
}

// ---------------------------------------------------------------------------
// PatternLibrary 模式库
// ---------------------------------------------------------------------------

// PatternLibrary 线程安全的模式库
type PatternLibrary struct {
	mu       sync.RWMutex
	patterns map[string]*ProjectPattern // patternID -> pattern
}

// NewPatternLibrary 创建模式库
func NewPatternLibrary() *PatternLibrary {
	return &PatternLibrary{
		patterns: make(map[string]*ProjectPattern),
	}
}

// ---------------------------------------------------------------------------
// PatternExtractor 接口
// ---------------------------------------------------------------------------

// PatternExtractor 模式提取器接口
type PatternExtractor interface {
	Extract(experiences []ProjectExperience) []ProjectPattern
	Match(context map[string]string) []PatternMatchResult
	AddPattern(pattern ProjectPattern)
	GetPattern(patternID string) (*ProjectPattern, bool)
	AllPatterns() []ProjectPattern
	TopPatterns(category string, topK int) []ProjectPattern
}

// ---------------------------------------------------------------------------
// DefaultPatternExtractor
// ---------------------------------------------------------------------------

// DefaultPatternExtractor 基于内存的模式提取实现
type DefaultPatternExtractor struct {
	library *PatternLibrary
}

// NewPatternExtractor 创建模式提取器
func NewPatternExtractor() *DefaultPatternExtractor {
	return &DefaultPatternExtractor{
		library: NewPatternLibrary(),
	}
}

// Extract 从多个 ProjectExperience 中提取模式
func (pe *DefaultPatternExtractor) Extract(experiences []ProjectExperience) []ProjectPattern {
	if len(experiences) == 0 {
		return nil
	}

	var extracted []ProjectPattern
	now := time.Now()

	// 1. 分析 category 分布 → architecture 模式
	catCount := make(map[string][]ProjectExperience)
	for _, exp := range experiences {
		if exp.Category != "" {
			catCount[exp.Category] = append(catCount[exp.Category], exp)
		}
	}
	for cat, exps := range catCount {
		if len(exps) < 2 {
			continue
		}
		avgSuccess := avgSuccessRate(exps)
		sources := projectIDs(exps)
		p := ProjectPattern{
			PatternID:   fmt.Sprintf("arch-%s-%d", cat, now.UnixMilli()),
			Name:        fmt.Sprintf("%s 项目架构模式", cat),
			Category:    "architecture",
			Description: fmt.Sprintf("在 %d 个 %s 类项目中观察到的通用架构模式", len(exps), cat),
			Conditions:  []PatternCondition{{Field: "project_category", Operator: "eq", Value: cat}},
			Actions:     []PatternAction{{ActionType: "set_budget", Target: "architecture", Value: "standard", Rationale: fmt.Sprintf("基于 %d 个同类项目的经验", len(exps))}},
			SuccessRate: avgSuccess,
			Frequency:   len(exps),
			Confidence:  confidenceFromCount(len(exps)),
			Source:       sources,
			CreatedAt:   now,
			LastSeenAt:  now,
		}
		extracted = append(extracted, p)
	}

	// 2. 分析 brain_usage → brain_selection 模式
	brainCat := make(map[string]map[string]float64) // category -> brainKind -> total usage
	brainCatN := make(map[string]int)
	for _, exp := range experiences {
		if exp.Category == "" || len(exp.BrainUsage) == 0 {
			continue
		}
		if brainCat[exp.Category] == nil {
			brainCat[exp.Category] = make(map[string]float64)
		}
		for bk, v := range exp.BrainUsage {
			brainCat[exp.Category][bk] += v
		}
		brainCatN[exp.Category]++
	}
	for cat, usage := range brainCat {
		n := brainCatN[cat]
		if n < 2 {
			continue
		}
		dominant := dominantBrain(usage)
		if dominant == "" {
			continue
		}
		exps := catCount[cat]
		p := ProjectPattern{
			PatternID:   fmt.Sprintf("brain-%s-%s-%d", cat, dominant, now.UnixMilli()),
			Name:        fmt.Sprintf("%s 项目推荐 %s 大脑", cat, dominant),
			Category:    "brain_selection",
			Description: fmt.Sprintf("在 %s 类项目中，%s 大脑使用率最高", cat, dominant),
			Conditions:  []PatternCondition{{Field: "project_category", Operator: "eq", Value: cat}},
			Actions:     []PatternAction{{ActionType: "use_brain", Target: dominant, Value: "primary", Rationale: fmt.Sprintf("基于 %d 个项目的使用统计", n)}},
			SuccessRate: avgSuccessRate(exps),
			Frequency:   n,
			Confidence:  confidenceFromCount(n),
			Source:       projectIDs(exps),
			CreatedAt:   now,
			LastSeenAt:  now,
		}
		extracted = append(extracted, p)
	}

	// 3. 分析 success_rate 高的经验共性 → workflow 模式
	var highSuccess []ProjectExperience
	for _, exp := range experiences {
		if exp.SuccessRate >= 0.8 {
			highSuccess = append(highSuccess, exp)
		}
	}
	if len(highSuccess) >= 2 {
		commonTags := commonElements(tagSets(highSuccess))
		if len(commonTags) > 0 {
			p := ProjectPattern{
				PatternID:   fmt.Sprintf("wf-high-success-%d", now.UnixMilli()),
				Name:        "高成功率项目工作流模式",
				Category:    "workflow",
				Description: fmt.Sprintf("在 %d 个高成功率项目中发现共性标签: %s", len(highSuccess), strings.Join(commonTags, ", ")),
				Conditions:  tagsToConditions(commonTags),
				Actions:     []PatternAction{{ActionType: "set_parallel", Target: "workflow", Value: "true", Rationale: "高成功率项目的共性工作流"}},
				SuccessRate: avgSuccessRate(highSuccess),
				Frequency:   len(highSuccess),
				Confidence:  confidenceFromCount(len(highSuccess)),
				Source:       projectIDs(highSuccess),
				CreatedAt:   now,
				LastSeenAt:  now,
			}
			extracted = append(extracted, p)
		}
	}

	// 4. 从 pitfalls 提取 error_handling 模式
	pitfallCount := make(map[string]int) // description -> count
	pitfallSev := make(map[string]string)
	pitfallAvoid := make(map[string]string)
	for _, exp := range experiences {
		for _, pit := range exp.Pitfalls {
			pitfallCount[pit.Description]++
			pitfallSev[pit.Description] = pit.Severity
			pitfallAvoid[pit.Description] = pit.Avoidance
		}
	}
	for desc, cnt := range pitfallCount {
		if cnt < 2 {
			continue
		}
		p := ProjectPattern{
			PatternID:   fmt.Sprintf("err-%d-%d", hash(desc), now.UnixMilli()),
			Name:        "重复踩坑预防",
			Category:    "error_handling",
			Description: desc,
			Conditions:  []PatternCondition{{Field: "complexity", Operator: "gt", Value: "0"}},
			Actions:     []PatternAction{{ActionType: "skip_phase", Target: pitfallSev[desc], Value: pitfallAvoid[desc], Rationale: fmt.Sprintf("在 %d 个项目中重复出现", cnt)}},
			SuccessRate: 0,
			Frequency:   cnt,
			Confidence:  confidenceFromCount(cnt),
			Source:       nil,
			CreatedAt:   now,
			LastSeenAt:  now,
		}
		extracted = append(extracted, p)
	}

	// 自动入库
	for i := range extracted {
		pe.AddPattern(extracted[i])
	}
	return extracted
}

// Match 将当前 context 与模式库匹配，返回按 matchScore * confidence 降序
func (pe *DefaultPatternExtractor) Match(ctx map[string]string) []PatternMatchResult {
	pe.library.mu.RLock()
	defer pe.library.mu.RUnlock()

	var results []PatternMatchResult
	for _, p := range pe.library.patterns {
		if len(p.Conditions) == 0 {
			continue
		}
		matched := 0
		for _, cond := range p.Conditions {
			if evaluateCondition(cond, ctx) {
				matched++
			}
		}
		if matched == 0 {
			continue
		}
		score := float64(matched) / float64(len(p.Conditions))
		results = append(results, PatternMatchResult{
			Pattern:           p,
			MatchScore:        score,
			MatchedConditions: matched,
			TotalConditions:   len(p.Conditions),
		})
	}

	sort.Slice(results, func(i, j int) bool {
		si := results[i].MatchScore * results[i].Pattern.Confidence
		sj := results[j].MatchScore * results[j].Pattern.Confidence
		return si > sj
	})
	return results
}

// AddPattern 添加模式到库
func (pe *DefaultPatternExtractor) AddPattern(pattern ProjectPattern) {
	pe.library.mu.Lock()
	defer pe.library.mu.Unlock()
	p := pattern
	pe.library.patterns[pattern.PatternID] = &p
}

// GetPattern 按 ID 查找模式
func (pe *DefaultPatternExtractor) GetPattern(patternID string) (*ProjectPattern, bool) {
	pe.library.mu.RLock()
	defer pe.library.mu.RUnlock()
	p, ok := pe.library.patterns[patternID]
	if !ok {
		return nil, false
	}
	clone := *p
	return &clone, true
}

// AllPatterns 返回全部模式
func (pe *DefaultPatternExtractor) AllPatterns() []ProjectPattern {
	pe.library.mu.RLock()
	defer pe.library.mu.RUnlock()
	result := make([]ProjectPattern, 0, len(pe.library.patterns))
	for _, p := range pe.library.patterns {
		result = append(result, *p)
	}
	return result
}

// TopPatterns 按 frequency * successRate 降序返回 topK 个模式
func (pe *DefaultPatternExtractor) TopPatterns(category string, topK int) []ProjectPattern {
	pe.library.mu.RLock()
	defer pe.library.mu.RUnlock()

	var filtered []*ProjectPattern
	for _, p := range pe.library.patterns {
		if category == "" || p.Category == category {
			filtered = append(filtered, p)
		}
	}

	sort.Slice(filtered, func(i, j int) bool {
		si := float64(filtered[i].Frequency) * filtered[i].SuccessRate
		sj := float64(filtered[j].Frequency) * filtered[j].SuccessRate
		return si > sj
	})

	if topK > 0 && len(filtered) > topK {
		filtered = filtered[:topK]
	}
	result := make([]ProjectPattern, len(filtered))
	for i, p := range filtered {
		result[i] = *p
	}
	return result
}

// ---------------------------------------------------------------------------
// 条件评估
// ---------------------------------------------------------------------------

// evaluateCondition 评估单个条件是否满足
func evaluateCondition(cond PatternCondition, ctx map[string]string) bool {
	actual, ok := ctx[cond.Field]
	if !ok {
		return false
	}
	switch cond.Operator {
	case "eq":
		return actual == cond.Value
	case "neq":
		return actual != cond.Value
	case "gt":
		return compareNumeric(actual, cond.Value) > 0
	case "lt":
		return compareNumeric(actual, cond.Value) < 0
	case "contains":
		return strings.Contains(actual, cond.Value)
	default:
		return false
	}
}

// ---------------------------------------------------------------------------
// 内部辅助
// ---------------------------------------------------------------------------

func compareNumeric(a, b string) int {
	fa, errA := strconv.ParseFloat(a, 64)
	fb, errB := strconv.ParseFloat(b, 64)
	if errA != nil || errB != nil {
		return strings.Compare(a, b)
	}
	if fa > fb {
		return 1
	}
	if fa < fb {
		return -1
	}
	return 0
}

func avgSuccessRate(exps []ProjectExperience) float64 {
	if len(exps) == 0 {
		return 0
	}
	var sum float64
	for _, e := range exps {
		sum += e.SuccessRate
	}
	return sum / float64(len(exps))
}

func projectIDs(exps []ProjectExperience) []string {
	seen := make(map[string]struct{})
	var ids []string
	for _, e := range exps {
		if _, ok := seen[e.ProjectID]; !ok {
			seen[e.ProjectID] = struct{}{}
			ids = append(ids, e.ProjectID)
		}
	}
	return ids
}

func confidenceFromCount(n int) float64 {
	if n <= 0 {
		return 0
	}
	// 简单对数置信度: 随样本增加趋近 1
	c := 1.0 - 1.0/float64(n+1)
	if c > 0.99 {
		c = 0.99
	}
	return c
}

func dominantBrain(usage map[string]float64) string {
	var best string
	var bestVal float64
	for k, v := range usage {
		if v > bestVal {
			bestVal = v
			best = k
		}
	}
	return best
}

func tagSets(exps []ProjectExperience) [][]string {
	sets := make([][]string, len(exps))
	for i, e := range exps {
		sets[i] = e.Tags
	}
	return sets
}

func commonElements(sets [][]string) []string {
	if len(sets) == 0 {
		return nil
	}
	count := make(map[string]int)
	for _, s := range sets {
		seen := make(map[string]struct{})
		for _, v := range s {
			if _, ok := seen[v]; !ok {
				seen[v] = struct{}{}
				count[v]++
			}
		}
	}
	var common []string
	for v, c := range count {
		if c == len(sets) {
			common = append(common, v)
		}
	}
	sort.Strings(common)
	return common
}

func tagsToConditions(tags []string) []PatternCondition {
	conds := make([]PatternCondition, len(tags))
	for i, t := range tags {
		conds[i] = PatternCondition{Field: "tags", Operator: "contains", Value: t}
	}
	return conds
}

func hash(s string) uint32 {
	var h uint32
	for _, c := range s {
		h = h*31 + uint32(c)
	}
	return h
}
