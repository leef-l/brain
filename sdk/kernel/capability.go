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
	// Version 是可选的能力版本号（如 "v2"、"v1.3"）。
	// 支持语义化版本匹配，使 CapabilityMatcher 可以区分同一能力的不同版本。
	Version string
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
//   - 如果最后一段以 "v" 开头且后面是数字，则视为版本号，从 Sub 中分离出来。
func ParseCapabilityTag(raw string) CapabilityTag {
	tag := CapabilityTag{Raw: raw}

	parts := strings.Split(raw, ".")
	if len(parts) == 0 {
		return tag
	}

	// Check if the last part is a version string (e.g. "v2", "v1.3").
	last := parts[len(parts)-1]
	if isVersionString(last) {
		tag.Version = last
		parts = parts[:len(parts)-1]
	}

	first := parts[0]
	if knownCategories[first] {
		tag.Category = first
		if len(parts) > 1 {
			tag.Primary = parts[1]
		}
		if len(parts) > 2 {
			tag.Sub = strings.Join(parts[2:], ".")
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

// isVersionString returns true if s looks like a version tag (e.g. "v2", "v1.3").
func isVersionString(s string) bool {
	if len(s) < 2 {
		return false
	}
	if s[0] != 'v' && s[0] != 'V' {
		return false
	}
	// Remaining chars must be digits or dots.
	for i := 1; i < len(s); i++ {
		c := s[i]
		if c != '.' && (c < '0' || c > '9') {
			return false
		}
	}
	return true
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

// BrainCapabilities returns the raw capability strings for a given brain kind.
func (idx *CapabilityIndex) BrainCapabilities(kind agent.Kind) []string {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	bs, ok := idx.brains[kind]
	if !ok {
		return nil
	}
	result := make([]string, 0, len(bs.Tags))
	for _, t := range bs.Tags {
		result = append(result, t.Raw)
	}
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

// BestMatch 执行匹配并返回最佳候选。如果没有匹配结果，返回零值和 false。
func (m *CapabilityMatcher) BestMatch(req MatchRequest) (MatchResult, bool) {
	results := m.Match(req)
	if len(results) == 0 {
		return MatchResult{}, false
	}
	return results[0], true
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

		// 阶段 1：硬匹配（支持版本语义）
		hardPass := true
		for _, reqTag := range req.Required {
			if !hasCapability(tagSet, reqTag) {
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

// hasCapability checks whether a brain's tagSet satisfies a requested capability.
// It supports version semantics:
//   - Exact match: "function.write_file.v2" matches "function.write_file.v2"
//   - No-version request: "function.write_file" matches any version ("v1", "v2", ...)
//   - Versioned request: "function.write_file.v2" matches v2 or higher.
func hasCapability(tagSet map[string]struct{}, req string) bool {
	if _, ok := tagSet[req]; ok {
		return true
	}

	reqTag := ParseCapabilityTag(req)
	if reqTag.Version == "" {
		// Request has no version — match any version of the same capability.
		for raw := range tagSet {
			t := ParseCapabilityTag(raw)
			if t.Category == reqTag.Category && t.Primary == reqTag.Primary && t.Sub == reqTag.Sub {
				return true
			}
		}
		return false
	}

	// Request has a version — match same or higher version.
	reqVer := parseVersion(reqTag.Version)
	for raw := range tagSet {
		t := ParseCapabilityTag(raw)
		if t.Category == reqTag.Category && t.Primary == reqTag.Primary && t.Sub == reqTag.Sub {
			if t.Version == "" {
				// Brain declares no version — assume latest, always match.
				return true
			}
			brainVer := parseVersion(t.Version)
			if versionGTE(brainVer, reqVer) {
				return true
			}
		}
	}
	return false
}

// versionTuple holds numeric version components (major, minor, patch).
type versionTuple struct {
	major int
	minor int
	patch int
}

// parseVersion parses "v2", "v1.3", "v2.1.0" into a versionTuple.
func parseVersion(s string) versionTuple {
	s = strings.TrimPrefix(strings.TrimPrefix(s, "v"), "V")
	parts := strings.Split(s, ".")
	var v versionTuple
	if len(parts) > 0 {
		v.major = atoi(parts[0])
	}
	if len(parts) > 1 {
		v.minor = atoi(parts[1])
	}
	if len(parts) > 2 {
		v.patch = atoi(parts[2])
	}
	return v
}

func versionGTE(a, b versionTuple) bool {
	if a.major != b.major {
		return a.major > b.major
	}
	if a.minor != b.minor {
		return a.minor > b.minor
	}
	return a.patch >= b.patch
}

func atoi(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}
