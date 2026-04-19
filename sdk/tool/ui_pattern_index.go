package tool

import (
	"net/url"
	"strings"
	"sync"
)

// P3.4 — 模式库匹配索引。
//
// 现状:MatchPatterns 对每次调用都线性扫描整个 PatternLibrary(80+ 模式,
// 每个都要跑 evaluateMatch → regexp.Compile + 3~4 次 DOM querySelector)。
// 大库下单次 match 要 ~50ms,主要成本在给那些显然不会命中的模式也算了一遍。
//
// 做法:按两维度做前置筛:
//   - URL 桶:把 AppliesWhen.URLPattern 里**能静态推出的**域名 / 首段 path
//     作为桶 key(推不出来的落进 __any 桶,始终参与匹配)。
//   - category 桶:Category 字段字符串等价映射。
//
// 匹配时根据当前页 URL + 请求 category 取两个桶交集,再对桶内模式跑完整
// MatchCondition 评估。这样只有"路由相关"的模式进入详细检查,80 个模式里
// 典型只有 3~10 个进评估阶段。
//
// 索引是懒构建的:第一次 MatchPatterns 调用时用当前 cache 重建;Upsert /
// RecordExecution / SetEnabled / Delete 调 bump(),标记失效,下次用时重建。
// 写路径不原地重建,避免持锁时间长;读路径 double-check 自己是否过期。

// patternIndex 是不可变快照;bump 触发 index 指针替换(copy-on-write),
// 读路径不需要锁,只读取指针快照即可。
type patternIndex struct {
	// byURLBucket: host+firstPath key → 命中的 patternID 集合。
	// key 为空字符串("__any")的桶里放 "推不出桶" 的模式,始终参与匹配。
	byURLBucket map[string][]string
	// byCategory: category → patternID 集合。category="" 桶同样放"推不出" 或
	// 明确声明 category="" 的模式;与 byURLBucket 的 __any 一起,确保遗漏最少。
	byCategory map[string][]string
	// enabled: cache 里 Enabled=true 的 patternID 集合(List 只返回 enabled)。
	enabled map[string]struct{}
}

var (
	patternIndexMu   sync.RWMutex
	patternIndexSnap *patternIndex
	// patternIndexDirty 置 true 表示 cache 改过,下一次读路径重建。
	patternIndexDirty bool = true
)

// bumpPatternIndex 标记索引失效。调用处:Upsert / SetEnabled / Delete /
// RecordExecution(后者因 Enabled 可能翻转)。
func bumpPatternIndex() {
	patternIndexMu.Lock()
	patternIndexDirty = true
	patternIndexMu.Unlock()
}

// loadPatternIndex 返回当前索引快照。若 dirty 或未初始化,基于传入 lib 重建。
// 调用方持有 lib.mu.RLock 时传 holdingRLock=true,避免重入死锁。
func loadPatternIndex(lib *PatternLibrary, holdingRLock bool) *patternIndex {
	patternIndexMu.RLock()
	snap := patternIndexSnap
	dirty := patternIndexDirty
	patternIndexMu.RUnlock()

	if snap != nil && !dirty {
		return snap
	}

	var patterns []*UIPattern
	if holdingRLock {
		patterns = collectCachedPatternsLocked(lib)
	} else {
		lib.mu.RLock()
		patterns = collectCachedPatternsLocked(lib)
		lib.mu.RUnlock()
	}
	next := buildPatternIndex(patterns)

	patternIndexMu.Lock()
	patternIndexSnap = next
	patternIndexDirty = false
	patternIndexMu.Unlock()
	return next
}

// collectCachedPatternsLocked 复制当前 lib.cache 指针列表(值拷贝留给 List
// 调用方做,索引阶段只需要只读访问 AppliesWhen / Category / Enabled / ID)。
func collectCachedPatternsLocked(lib *PatternLibrary) []*UIPattern {
	out := make([]*UIPattern, 0, len(lib.cache))
	for _, p := range lib.cache {
		out = append(out, p)
	}
	return out
}

// buildPatternIndex 根据 AppliesWhen.URLPattern 静态前缀(host + 首段 path)
// 和 Category 字段构造两个倒排表。
func buildPatternIndex(patterns []*UIPattern) *patternIndex {
	idx := &patternIndex{
		byURLBucket: make(map[string][]string),
		byCategory:  make(map[string][]string),
		enabled:     make(map[string]struct{}, len(patterns)),
	}
	for _, p := range patterns {
		if p == nil || p.ID == "" {
			continue
		}
		if p.Enabled {
			idx.enabled[p.ID] = struct{}{}
		}
		bucket := deriveURLBucket(p.AppliesWhen.URLPattern)
		idx.byURLBucket[bucket] = append(idx.byURLBucket[bucket], p.ID)

		cat := p.Category
		idx.byCategory[cat] = append(idx.byCategory[cat], p.ID)
	}
	return idx
}

// deriveURLBucket 从 URLPattern 里抽出"足够稳定"的 host / 首段 path 做桶 key。
// 规则:
//   - 空串或无法识别 → "__any"(始终参与匹配)
//   - "https://shop.example.com/cart" 这种绝对 URL → "shop.example.com/cart"
//   - "/(login|signin|sign-in)" 这类仅 path 的 regex → "/login"(取第一个字面量 segment)
//   - 复杂 regex(含 `(?=`、`|`跨 host 等)退化为 "__any"
func deriveURLBucket(pattern string) string {
	if pattern == "" {
		return "__any"
	}
	// 绝对 URL 前缀先剥离 scheme。
	s := pattern
	if idx := strings.Index(s, "://"); idx >= 0 {
		s = s[idx+3:]
	}
	// 吃掉 regex 里的非捕获组头等噪音。
	s = strings.TrimPrefix(s, "(?i)")
	// 取首段路径 —— 切到第一个 / 后的下一个 /(或结束)。
	// 支持两种形态:"host.com/path..." 或 "/path..."。
	if strings.HasPrefix(s, "/") {
		// path-only regex:提取第一个字面量 segment。
		rest := s[1:]
		lit := extractLiteralSegment(rest)
		if lit == "" {
			return "__any"
		}
		return "/" + lit
	}
	// host 形式:host 本身必须是纯字面量(字母数字 + "." + "-"),否则降级。
	slash := strings.IndexByte(s, '/')
	var host, rest string
	if slash < 0 {
		host = s
		rest = ""
	} else {
		host = s[:slash]
		rest = s[slash+1:]
	}
	if !isLiteralHost(host) {
		return "__any"
	}
	if rest == "" {
		return host
	}
	lit := extractLiteralSegment(rest)
	if lit == "" {
		return host
	}
	return host + "/" + lit
}

// extractLiteralSegment 从 regex path 片段头部提取一个纯字面量首段。
// 遇到 ( | . * ? [ { \ ^ $ ] 等特殊字符或 / 则停止。返回空串表示"首字符
// 就是特殊字符,无法推出桶"。
func extractLiteralSegment(s string) string {
	// 允许一个可选的 "\b" 前导(patterns 里常写 /login\b),剥掉再扫。
	s = strings.TrimPrefix(s, "\\b")
	end := len(s)
	for i, r := range s {
		if r == '/' {
			end = i
			break
		}
		if !isLiteralRune(r) {
			// 如果前面已经有至少一个字符,仍可以当桶(接受前缀匹配);
			// 若首字符就非字面量,返回空串表达"没法推"。
			end = i
			break
		}
	}
	out := s[:end]
	if out == "" {
		return ""
	}
	// 避免桶太零碎:单字符前缀(如 "l" for "l.*")认为不稳定。
	if len(out) < 2 {
		return ""
	}
	return out
}

func isLiteralRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z':
		return true
	case r >= 'A' && r <= 'Z':
		return true
	case r >= '0' && r <= '9':
		return true
	case r == '-' || r == '_':
		return true
	}
	return false
}

func isLiteralHost(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !(isLiteralRune(r) || r == '.') {
			return false
		}
	}
	return true
}

// matchBucketsForURL 给一个具体 URL 生成它命中的桶 key 列表(host、host/path1、
// path1,以及 __any)。用于倒排查询。
func matchBucketsForURL(pageURL string) []string {
	keys := []string{"__any"}
	if pageURL == "" {
		return keys
	}
	u, err := url.Parse(pageURL)
	if err != nil {
		return keys
	}
	host := strings.ToLower(u.Host)
	if host != "" {
		keys = append(keys, host)
	}
	firstSeg := firstPathSegment(u.Path)
	if firstSeg != "" {
		keys = append(keys, "/"+firstSeg)
		if host != "" {
			keys = append(keys, host+"/"+firstSeg)
		}
	}
	return keys
}

func firstPathSegment(p string) string {
	p = strings.TrimPrefix(p, "/")
	if p == "" {
		return ""
	}
	if i := strings.IndexByte(p, '/'); i >= 0 {
		p = p[:i]
	}
	return strings.ToLower(p)
}

// candidatePatterns 返回应该参与详细 MatchCondition 评估的 patternID 列表,
// 已按 enabled + category 过滤。去重 + 稳定顺序(id 字典序)。
func candidatePatterns(lib *PatternLibrary, pageURL, category string) []string {
	idx := loadPatternIndex(lib, false)

	// 1) URL 桶集合
	keys := matchBucketsForURL(pageURL)
	urlHits := make(map[string]struct{}, 16)
	for _, k := range keys {
		for _, id := range idx.byURLBucket[k] {
			urlHits[id] = struct{}{}
		}
	}
	// 2) category 过滤(空 = 不过滤)
	if category != "" {
		catHits := make(map[string]struct{}, len(idx.byCategory[category])+len(idx.byCategory[""]))
		for _, id := range idx.byCategory[category] {
			catHits[id] = struct{}{}
		}
		// category="" 的模式也可能适用(未声明 category),与 category 桶并集
		for _, id := range idx.byCategory[""] {
			catHits[id] = struct{}{}
		}
		intersection := make(map[string]struct{}, len(urlHits))
		for id := range urlHits {
			if _, ok := catHits[id]; ok {
				intersection[id] = struct{}{}
			}
		}
		urlHits = intersection
	}

	// 3) enabled 过滤
	out := make([]string, 0, len(urlHits))
	for id := range urlHits {
		if _, ok := idx.enabled[id]; !ok {
			continue
		}
		out = append(out, id)
	}
	// 稳定顺序便于测试;id 字典序即可。
	stableSortStrings(out)
	return out
}

// stableSortStrings 是 sort.Strings 的最小替代,避免 import sort。仅用于
// 候选列表(N 通常 < 20)的确定性排序。
func stableSortStrings(xs []string) {
	for i := 1; i < len(xs); i++ {
		for j := i; j > 0 && xs[j-1] > xs[j]; j-- {
			xs[j-1], xs[j] = xs[j], xs[j-1]
		}
	}
}
