// capability.go — Brain Capability 标签体系与匹配算法
//
// 四类标签（前缀区分）：
//   - function: {domain}.{verb}  — 脑能做什么操作
//   - domain:   domain.{area}    — 覆盖什么领域
//   - resource: resource.{type}  — 能访问什么资源
//   - mode:     mode.{pattern}   — 支持什么运行模式
//
// 匹配三阶段：硬匹配过滤 → 软匹配打分 → 综合排序

package kernel

import (
	"sort"
	"strings"
	"sync"

	"github.com/leef-l/brain/sdk/agent"
)

// ---------------------------------------------------------------------------
// CapabilityTag
// ---------------------------------------------------------------------------

// CapabilityTag 是单个能力标签。
type CapabilityTag struct {
	Raw      string // 原始标签字符串，如 "trading.execute"
	Category string // "function" | "domain" | "resource" | "mode"
	Primary  string // 第一段，如 "trading"
	Sub      string // 第二段，如 "execute"
}

// knownCategories 用于判断标签前缀是否为已知类别。
var knownCategories = map[string]bool{
	"domain":   true,
	"resource": true,
	"mode":     true,
}

// ParseCapabilityTag 解析标签字符串。
//
// 规则：
//   - 若第一段是 domain / resource / mode，则 Category 取该前缀，Primary 取第二段，Sub 取第三段（如有）。
//   - 否则 Category = "function"，Primary 取第一段，Sub 取第二段。
func ParseCapabilityTag(raw string) CapabilityTag {
	tag := CapabilityTag{Raw: raw}

	parts := strings.SplitN(raw, ".", 3)
	if len(parts) == 0 {
		return tag
	}

	first := parts[0]
	if knownCategories[first] {
		tag.Category = first
		if len(parts) > 1 {
			tag.Primary = parts[1]
		}
		if len(parts) > 2 {
			tag.Sub = parts[2]
		}
	} else {
		tag.Category = "function"
		tag.Primary = first
		if len(parts) > 1 {
			tag.Sub = parts[1]
		}
	}
	return tag
}

// ---------------------------------------------------------------------------
// BrainCapabilitySet
// ---------------------------------------------------------------------------

// BrainCapabilitySet 是某个 brain 声明的全部能力标签集合。
type BrainCapabilitySet struct {
	BrainKind agent.Kind
	Tags      []CapabilityTag
}

// ---------------------------------------------------------------------------
// CapabilityIndex
// ---------------------------------------------------------------------------

// CapabilityIndex 索引结构，支持按标签快速查找 brain。
type CapabilityIndex struct {
	mu sync.RWMutex

	// brains: brainKind → 该 brain 拥有的所有标签
	brains map[agent.Kind]*BrainCapabilitySet

	// tagIndex: 完整标签字符串 → brainKind 集合
	tagIndex map[string]map[agent.Kind]struct{}
}

// NewCapabilityIndex 创建空索引。
func NewCapabilityIndex() *CapabilityIndex {
	return &CapabilityIndex{
		brains:   make(map[agent.Kind]*BrainCapabilitySet),
		tagIndex: make(map[string]map[agent.Kind]struct{}),
	}
}

// AddBrain 注册一个 brain 及其能力标签列表。
func (idx *CapabilityIndex) AddBrain(brainKind agent.Kind, capabilities []string) {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	// 如果已存在，先清理旧索引
	idx.removeBrainLocked(brainKind)

	tags := make([]CapabilityTag, 0, len(capabilities))
	for _, raw := range capabilities {
		tag := ParseCapabilityTag(raw)
		tags = append(tags, tag)

		if idx.tagIndex[raw] == nil {
			idx.tagIndex[raw] = make(map[agent.Kind]struct{})
		}
		idx.tagIndex[raw][brainKind] = struct{}{}
	}

	idx.brains[brainKind] = &BrainCapabilitySet{
		BrainKind: brainKind,
		Tags:      tags,
	}
}

// RemoveBrain 移除一个 brain 的全部索引。
func (idx *CapabilityIndex) RemoveBrain(brainKind agent.Kind) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.removeBrainLocked(brainKind)
}

func (idx *CapabilityIndex) removeBrainLocked(brainKind agent.Kind) {
	bs, ok := idx.brains[brainKind]
	if !ok {
		return
	}
	for _, tag := range bs.Tags {
		if m, exists := idx.tagIndex[tag.Raw]; exists {
			delete(m, brainKind)
			if len(m) == 0 {
				delete(idx.tagIndex, tag.Raw)
			}
		}
	}
	delete(idx.brains, brainKind)
}

// FindByTag 精确匹配标签，返回拥有该标签的 brain 列表。
func (idx *CapabilityIndex) FindByTag(tag string) []agent.Kind {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	m := idx.tagIndex[tag]
	result := make([]agent.Kind, 0, len(m))
	for k := range m {
		result = append(result, k)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

// FindByPrefix 前缀匹配标签，返回拥有任一匹配标签的 brain 列表。
func (idx *CapabilityIndex) FindByPrefix(prefix string) []agent.Kind {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	seen := make(map[agent.Kind]struct{})
	for raw, m := range idx.tagIndex {
		if strings.HasPrefix(raw, prefix) {
			for k := range m {
				seen[k] = struct{}{}
			}
		}
	}
	result := make([]agent.Kind, 0, len(seen))
	for k := range seen {
		result = append(result, k)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

// FindByCategory 按类别（function/domain/resource/mode）查找所有 brain。
func (idx *CapabilityIndex) FindByCategory(category string) []agent.Kind {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	seen := make(map[agent.Kind]struct{})
	for _, bs := range idx.brains {
		for _, tag := range bs.Tags {
			if tag.Category == category {
				seen[bs.BrainKind] = struct{}{}
				break
			}
		}
	}
	result := make([]agent.Kind, 0, len(seen))
	for k := range seen {
		result = append(result, k)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

// AllBrains 返回已注册的全部 brain Kind。
func (idx *CapabilityIndex) AllBrains() []agent.Kind {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	result := make([]agent.Kind, 0, len(idx.brains))
	for k := range idx.brains {
		result = append(result, k)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

// ---------------------------------------------------------------------------
// MatchRequest / MatchResult
// ---------------------------------------------------------------------------

// MatchRequest 描述一次能力匹配请求。
type MatchRequest struct {
	Required  []string // 硬匹配：候选 brain 必须具备全部
	Preferred []string // 软匹配：有更好，没有也行
}

// MatchResult 描述匹配结果。
type MatchResult struct {
	BrainKind     agent.Kind
	HardScore     float64 // 硬匹配分（0 或 1）
	SoftScore     float64 // 软匹配分 [0, 1]
	CombinedScore float64 // 综合分 = HardScore*0.6 + SoftScore*0.4
}

// ---------------------------------------------------------------------------
// CapabilityMatcher
// ---------------------------------------------------------------------------

// CapabilityMatcher 执行三阶段匹配算法。
type CapabilityMatcher struct {
	index *CapabilityIndex
}

// NewCapabilityMatcher 创建匹配器。
func NewCapabilityMatcher(index *CapabilityIndex) *CapabilityMatcher {
	return &CapabilityMatcher{index: index}
}

// Match 执行匹配，返回按 CombinedScore 降序排列的候选列表。
//
// 三阶段：
//  1. 硬匹配过滤：没有全部 Required 的直接排除（HardScore = 0）
//  2. 软匹配打分：Preferred 命中比例作为 SoftScore
//  3. 综合排序：CombinedScore = HardScore*0.6 + SoftScore*0.4
func (m *CapabilityMatcher) Match(req MatchRequest) []MatchResult {
	m.index.mu.RLock()
	defer m.index.mu.RUnlock()

	var results []MatchResult

	for kind, bs := range m.index.brains {
		// 构建该 brain 的标签快查集合
		tagSet := make(map[string]struct{}, len(bs.Tags))
		for _, t := range bs.Tags {
			tagSet[t.Raw] = struct{}{}
		}

		// 阶段 1：硬匹配
		hardPass := true
		for _, req := range req.Required {
			if _, ok := tagSet[req]; !ok {
				hardPass = false
				break
			}
		}
		if !hardPass {
			continue
		}

		hardScore := 1.0

		// 阶段 2：软匹配
		softScore := 0.0
		if len(req.Preferred) > 0 {
			hits := 0
			for _, p := range req.Preferred {
				if _, ok := tagSet[p]; ok {
					hits++
				}
			}
			softScore = float64(hits) / float64(len(req.Preferred))
		}

		// 阶段 3：综合
		combined := hardScore*0.6 + softScore*0.4

		results = append(results, MatchResult{
			BrainKind:     kind,
			HardScore:     hardScore,
			SoftScore:     softScore,
			CombinedScore: combined,
		})
	}

	// 按 CombinedScore 降序排列，同分按 Kind 字母序稳定排序
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].CombinedScore != results[j].CombinedScore {
			return results[i].CombinedScore > results[j].CombinedScore
		}
		return results[i].BrainKind < results[j].BrainKind
	})

	return results
}
