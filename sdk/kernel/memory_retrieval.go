package kernel

import (
	"math"
	"sort"
	"strings"
	"time"
)

// RetrievalResult 检索结果。
type RetrievalResult struct {
	Entry     MemoryEntry `json:"entry"`
	Score     float64     `json:"score"`      // 综合相关度 0-1
	MatchType string      `json:"match_type"` // keyword/tag/recency
}

// MemoryRetriever 记忆检索器。
// 支持关键词匹配 + 标签匹配 + 时间衰减 + 重要度加权的多因子排序。
type MemoryRetriever struct {
	// KeywordWeight 关键词匹配权重（默认 0.4）
	KeywordWeight float64
	// TagWeight 标签匹配权重（默认 0.2）
	TagWeight float64
	// RecencyWeight 时间衰减权重（默认 0.2）
	RecencyWeight float64
	// ImportanceWeight 重要度权重（默认 0.2）
	ImportanceWeight float64
	// DecayHalfLife 时间衰减半衰期（默认 7 天）
	DecayHalfLife time.Duration
}

// NewMemoryRetriever 创建一个使用默认权重的记忆检索器。
func NewMemoryRetriever() *MemoryRetriever {
	return &MemoryRetriever{
		KeywordWeight:    0.4,
		TagWeight:        0.2,
		RecencyWeight:    0.2,
		ImportanceWeight: 0.2,
		DecayHalfLife:    7 * 24 * time.Hour,
	}
}

// Retrieve 从 entries 中检索与 query 最相关的结果。
//
// 算法：
//  1. 将 query 按空格分词得到关键词列表
//  2. 对每个 entry 计算 4 个维度的分值：
//     a. keywordScore: query 词在 Content+Summary 中出现的比例（命中词数/总词数）
//     b. tagScore: query tags 与 entry tags 的 Jaccard 相似度
//     c. recencyScore: 指数时间衰减 exp(-ln2 * elapsed / halfLife)
//     d. importanceScore: entry.Importance（已归一化到 0-1）
//  3. 综合分 = keyword*w1 + tag*w2 + recency*w3 + importance*w4
//  4. 按综合分降序排序，截取前 limit 条
func (r *MemoryRetriever) Retrieve(entries []MemoryEntry, query string, tags []string, limit int) []RetrievalResult {
	if len(entries) == 0 {
		return nil
	}

	// 分词：按空格拆分，过滤空串
	keywords := splitKeywords(query)

	var results []RetrievalResult
	for _, entry := range entries {
		kw := keywordScore(entry.Content+" "+entry.Summary, keywords)
		tg := tagJaccardScore(entry.Tags, tags)
		rc := recencyScore(entry.CreatedAt, r.DecayHalfLife)
		imp := entry.Importance // 已经是 0-1

		// 综合得分
		score := kw*r.KeywordWeight +
			tg*r.TagWeight +
			rc*r.RecencyWeight +
			imp*r.ImportanceWeight

		// 确定主要匹配类型（取贡献最高的维度）
		matchType := dominantMatchType(
			kw*r.KeywordWeight,
			tg*r.TagWeight,
			rc*r.RecencyWeight,
		)

		results = append(results, RetrievalResult{
			Entry:     entry,
			Score:     score,
			MatchType: matchType,
		})
	}

	// 按综合分降序排序
	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	// 截取 limit
	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}

	return results
}

// splitKeywords 将查询字符串按空格分词，过滤空串并转小写。
func splitKeywords(query string) []string {
	raw := strings.Fields(query)
	keywords := make([]string, 0, len(raw))
	for _, w := range raw {
		w = strings.TrimSpace(w)
		if w != "" {
			keywords = append(keywords, strings.ToLower(w))
		}
	}
	return keywords
}

// keywordScore 计算关键词匹配分数。
// content 和 keywords 都转小写比较。
// 命中率 = 命中词数 / 总关键词数。
// 如果 keywords 为空，返回 0。
func keywordScore(content string, keywords []string) float64 {
	if len(keywords) == 0 {
		return 0
	}
	contentLower := strings.ToLower(content)
	hits := 0
	for _, kw := range keywords {
		if strings.Contains(contentLower, kw) {
			hits++
		}
	}
	return float64(hits) / float64(len(keywords))
}

// tagJaccardScore 计算标签 Jaccard 相似度。
// Jaccard = |A ∩ B| / |A ∪ B|
// 如果两个集合都为空，返回 0。
func tagJaccardScore(entryTags, queryTags []string) float64 {
	if len(entryTags) == 0 && len(queryTags) == 0 {
		return 0
	}
	if len(entryTags) == 0 || len(queryTags) == 0 {
		return 0
	}

	setA := make(map[string]struct{}, len(entryTags))
	for _, t := range entryTags {
		setA[strings.ToLower(t)] = struct{}{}
	}
	setB := make(map[string]struct{}, len(queryTags))
	for _, t := range queryTags {
		setB[strings.ToLower(t)] = struct{}{}
	}

	// 计算交集
	intersection := 0
	for k := range setA {
		if _, ok := setB[k]; ok {
			intersection++
		}
	}

	// 计算并集 = |A| + |B| - |A ∩ B|
	union := len(setA) + len(setB) - intersection
	if union == 0 {
		return 0
	}

	return float64(intersection) / float64(union)
}

// recencyScore 计算时间衰减分数。
// 使用指数衰减模型：score = exp(-ln2 * elapsed / halfLife)
// elapsed < 0（未来时间）返回 1.0。
// halfLife <= 0 返回 1.0（不衰减）。
func recencyScore(createdAt time.Time, halfLife time.Duration) float64 {
	if halfLife <= 0 {
		return 1.0
	}
	elapsed := time.Since(createdAt)
	if elapsed < 0 {
		// 未来时间，最新鲜
		return 1.0
	}
	return math.Exp(-math.Ln2 * float64(elapsed) / float64(halfLife))
}

// dominantMatchType 根据各维度加权后的贡献值确定主要匹配类型。
func dominantMatchType(keywordContrib, tagContrib, recencyContrib float64) string {
	if keywordContrib >= tagContrib && keywordContrib >= recencyContrib {
		return "keyword"
	}
	if tagContrib >= keywordContrib && tagContrib >= recencyContrib {
		return "tag"
	}
	return "recency"
}
