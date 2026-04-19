package tool

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/leef-l/brain/sdk/persistence"
	"github.com/leef-l/brain/sdk/tool/cdp"
)

// browser.sitemap — Stage 1 — whole-site crawl + route pattern mining.
//
// See sdk/docs/39-Browser-Brain感知与嗅探增强设计.md §3.4.
//
// Stage 1 scope: BFS crawl, same-origin filter, robots.txt honor, sitemap.xml
// merge, route parameterization. API route discovery + router-type detection
// are Stage 2 and intentionally out of scope here.
//
// P3.4 — 引入持久化缓存:同 (site_origin, max_depth) 的爬取结果进 LearningStore
// 的 sitemap_snapshots 表,默认 TTL 24h。force=true 绕开缓存重爬;incremental
// 缺省 true,命中缓存时跳过整个 BFS,直接把上次的 URL 列表返回给 Agent。
//
// 缓存只负责 URL 列表本身,不回放每页的 title / has_forms 等富字段 —— 那些
// 要靠当场爬才能拿到最新的。调用方若需要富字段必须传 force=true。

// SitemapCache 是 P3.4 持久化缓存接口,由主机侧注入。
// 生产实现直接走 LearningStore.{Save,Get,Purge}SitemapSnapshot。
// 测试可以注入内存 mock,不必打开 SQLite。nil 表示"未配置缓存",
// 工具退化为每次重爬。
type SitemapCache interface {
	Save(ctx context.Context, snap *persistence.SitemapSnapshot) error
	Get(ctx context.Context, siteOrigin string, depth int) (*persistence.SitemapSnapshot, error)
	Purge(ctx context.Context, olderThan time.Time) (int64, error)
}

var (
	sitemapCacheMu sync.RWMutex
	sitemapCache   SitemapCache
)

// SetSitemapCache 在进程启动时被 kernel/cmd 注入,传 nil 即关闭缓存。
// 和 SetInteractionSink / SetHumanDemoSink 同款风格。
func SetSitemapCache(c SitemapCache) {
	sitemapCacheMu.Lock()
	defer sitemapCacheMu.Unlock()
	sitemapCache = c
}

func currentSitemapCache() SitemapCache {
	sitemapCacheMu.RLock()
	defer sitemapCacheMu.RUnlock()
	return sitemapCache
}

// defaultSitemapTTL 决定缓存过期时间。同站重访窗口 24h 足够覆盖一天内多次
// 任务,又不至于把长期变化的电商 / 管理台新增 URL 遮蔽。调用方可显式传
// max_age_sec 覆盖。
const defaultSitemapTTL = 24 * time.Hour

// sitemapInput matches the tool's JSON schema.
type sitemapInput struct {
	StartURL         string   `json:"start_url"`
	MaxPages         int      `json:"max_pages"`
	MaxDepth         int      `json:"max_depth"`
	SameOriginOnly   bool     `json:"same_origin_only"`
	IncludeExternal  bool     `json:"include_external"`
	Concurrency      int      `json:"concurrency"`
	DelayMS          int      `json:"delay_ms"`
	ObeyRobotsTxt    bool     `json:"obey_robots_txt"`
	IncludeSitemapXML bool    `json:"include_sitemap_xml"`
	IgnorePatterns   []string `json:"ignore_patterns"`
	SummaryOnly      bool     `json:"summary_only"`
	// P3.4 — 持久化缓存控制。
	// Force=true 绕过缓存直接重爬;MaxAgeSec 覆盖默认 TTL(秒)。
	Force     bool `json:"force"`
	MaxAgeSec int  `json:"max_age_sec"`
}

// sitemapPage is one entry in the output.
type sitemapPage struct {
	URL             string   `json:"url"`
	Canonical       string   `json:"canonical"`
	Title           string   `json:"title"`
	Depth           int      `json:"depth"`
	DiscoveredFrom  string   `json:"discovered_from,omitempty"`
	Status          int      `json:"status"`
	ContentType     string   `json:"content_type,omitempty"`
	InternalLinks   int      `json:"internal_links"`
	ExternalLinks   int      `json:"external_links"`
	HasForms        bool     `json:"has_forms"`
	FormActions     []string `json:"form_actions,omitempty"`
}

// sitemapPattern is a mined route template.
type sitemapPattern struct {
	Pattern    string  `json:"pattern"`
	Matches    int     `json:"matches"`
	Example    string  `json:"example"`
	Type       string  `json:"type,omitempty"` // "page" | "api"
	Confidence float64 `json:"confidence"`
}

// sitemapResult is the full tool output.
type sitemapResult struct {
	StartURL          string           `json:"start_url"`
	PagesVisited      int              `json:"pages_visited"`
	Pages             []sitemapPage    `json:"pages,omitempty"`
	RoutePatterns     []sitemapPattern `json:"route_patterns"`
	SitemapXMLUrls    []string         `json:"sitemap_xml_urls,omitempty"`
	RobotsDisallow    []string         `json:"robots_txt_disallow,omitempty"`
	Unreachable       []sitemapPage    `json:"unreachable,omitempty"`
	SummaryOnly       bool             `json:"summary_only,omitempty"`
	Truncated         bool             `json:"truncated,omitempty"`
	ElapsedMS         int64            `json:"elapsed_ms"`
	// P3.4 — 是否命中缓存(true 时 Pages 为空、RoutePatterns 从缓存 URL 现场
	// 重新 mine,走全本地路径,无 HTTP 请求)。
	CacheHit bool `json:"cache_hit,omitempty"`
}

type browserSitemapTool struct{ holder *browserSessionHolder }

func (t *browserSitemapTool) Name() string { return "browser.sitemap" }
func (t *browserSitemapTool) Risk() Risk   { return RiskMedium }

func (t *browserSitemapTool) Schema() Schema {
	return Schema{
		Name: t.Name(),
		Description: `Crawl a site starting from start_url and produce:
  - a list of reachable pages (BFS, same-origin by default)
  - mined route patterns (/users/:id, /posts/:slug) via path parameterization
  - robots.txt Disallow rules honored by default
  - sitemap.xml URLs merged in

Stage 1: no API route discovery, no router-type detection. Use for site
reconnaissance before automation, security scans, or feeding the semantic
understanding layer with bulk URL templates.

Safety defaults: obey_robots_txt=true, concurrency=3, delay_ms=200.`,
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "start_url":          { "type": "string",  "description": "Entry URL (required)" },
    "max_pages":          { "type": "integer", "description": "Max pages to visit (default 50, hard cap 500)" },
    "max_depth":          { "type": "integer", "description": "Max BFS depth (default 3)" },
    "same_origin_only":   { "type": "boolean", "description": "Default true" },
    "include_external":   { "type": "boolean", "description": "Include external link counts in output" },
    "concurrency":        { "type": "integer", "description": "Parallel page fetches (default 3, max 8)" },
    "delay_ms":           { "type": "integer", "description": "Delay between fetches (default 200)" },
    "obey_robots_txt":    { "type": "boolean", "description": "Default true" },
    "include_sitemap_xml":{ "type": "boolean", "description": "Fetch and merge /sitemap.xml (default true)" },
    "ignore_patterns":    { "type": "array",   "items": {"type":"string"}, "description": "Glob patterns to skip" },
    "summary_only":       { "type": "boolean", "description": "Omit pages[] in output, return only route_patterns + counts" },
    "force":              { "type": "boolean", "description": "Bypass the persistent URL-list cache and re-crawl" },
    "max_age_sec":        { "type": "integer", "description": "Cache TTL override in seconds (default 86400 = 24h)" }
  },
  "required": ["start_url"]
}`),
		OutputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "start_url":      { "type": "string" },
    "pages_visited":  { "type": "integer" },
    "pages":          { "type": "array" },
    "route_patterns": { "type": "array" },
    "sitemap_xml_urls": { "type": "array" },
    "robots_txt_disallow": { "type": "array" },
    "unreachable":    { "type": "array" },
    "truncated":      { "type": "boolean" },
    "elapsed_ms":     { "type": "integer" }
  }
}`),
		Brain: "browser",
		Concurrency: &ToolConcurrencySpec{
			Capability:          "web.read",
			ResourceKeyTemplate: "browser:session",
			AccessMode:          "exclusive-session",
			Scope:               "turn",
			ApprovalClass:       "external-network",
		},
	}
}

func (t *browserSitemapTool) Execute(ctx context.Context, args json.RawMessage) (*Result, error) {
	input := sitemapInput{
		SameOriginOnly:    true,
		ObeyRobotsTxt:     true,
		IncludeSitemapXML: true,
		Concurrency:       3,
		DelayMS:           200,
		MaxPages:          50,
		MaxDepth:          3,
	}
	if len(args) > 0 && string(args) != "null" {
		if err := json.Unmarshal(args, &input); err != nil {
			return errResult("invalid arguments: %v", err), nil
		}
	}
	if input.StartURL == "" {
		return errResult("start_url is required"), nil
	}
	// Hard caps
	if input.MaxPages <= 0 {
		input.MaxPages = 50
	}
	if input.MaxPages > 500 {
		input.MaxPages = 500
	}
	if input.MaxDepth <= 0 {
		input.MaxDepth = 3
	}
	if input.MaxDepth > 10 {
		input.MaxDepth = 10
	}
	if input.Concurrency <= 0 {
		input.Concurrency = 3
	}
	if input.Concurrency > 8 {
		input.Concurrency = 8
	}
	if input.DelayMS < 0 {
		input.DelayMS = 0
	}

	start, err := url.Parse(input.StartURL)
	if err != nil {
		return errResult("invalid start_url: %v", err), nil
	}
	if start.Scheme == "" || start.Host == "" {
		return errResult("start_url must be absolute (include http/https and host)"), nil
	}

	sess, err := t.holder.get(ctx)
	if err != nil {
		return errResult("no browser session: %v", err), nil
	}

	origin := start.Scheme + "://" + start.Host

	// P3.4 — 持久化缓存读。未配置缓存 / force=true / 查不到 / 过期都走全爬。
	startedAt := time.Now()
	if !input.Force {
		if cache := currentSitemapCache(); cache != nil {
			if snap, cerr := cache.Get(ctx, origin, input.MaxDepth); cerr == nil && snap != nil {
				ttl := defaultSitemapTTL
				if input.MaxAgeSec > 0 {
					ttl = time.Duration(input.MaxAgeSec) * time.Second
				}
				if time.Since(snap.CollectedAt) < ttl {
					if hit := sitemapResultFromCache(origin, input, snap); hit != nil {
						hit.ElapsedMS = time.Since(startedAt).Milliseconds()
						return okResult(hit), nil
					}
				}
			}
		}
	}

	crawler := &sitemapCrawler{
		ctx:     ctx,
		sess:    sess,
		input:   input,
		start:   start,
		origin:  origin,
		visited: make(map[string]*sitemapPage),
		seen:    make(map[string]bool),
	}

	result, crawlErr := crawler.run()
	if crawlErr != nil {
		return errResult("crawl: %v", crawlErr), nil
	}

	// P3.4 — 持久化写回。只存 URL 列表(visited key 即已访问 URL 集合);
	// 错误不致命,缓存写失败不影响返回。
	if cache := currentSitemapCache(); cache != nil && len(result.Pages) > 0 {
		urls := make([]string, 0, len(result.Pages))
		for _, pg := range result.Pages {
			if pg.URL != "" {
				urls = append(urls, pg.URL)
			}
		}
		if raw, mErr := json.Marshal(urls); mErr == nil {
			_ = cache.Save(ctx, &persistence.SitemapSnapshot{
				SiteOrigin: origin,
				Depth:      input.MaxDepth,
				URLs:       raw,
			})
		}
	}

	return okResult(result), nil
}

// sitemapResultFromCache 用缓存里的 URL 列表装配一个"足够用"的 sitemapResult。
// pages 不回放,只在 summary_only=false 时给出占位(仅 URL、depth=0、status=0)。
// 真正的富字段需要调用方传 force=true 重爬。返回 nil 表示缓存损坏,调用方
// 应回退走 crawler。
func sitemapResultFromCache(origin string, input sitemapInput, snap *persistence.SitemapSnapshot) *sitemapResult {
	var urls []string
	if err := json.Unmarshal([]byte(snap.URLs), &urls); err != nil {
		return nil
	}
	if len(urls) == 0 {
		return nil
	}
	res := &sitemapResult{
		StartURL:     input.StartURL,
		PagesVisited: len(urls),
		SummaryOnly:  input.SummaryOnly,
		CacheHit:     true,
	}
	res.RoutePatterns = mineRoutePatterns(urls)
	if !input.SummaryOnly {
		pages := make([]sitemapPage, 0, len(urls))
		for _, u := range urls {
			pages = append(pages, sitemapPage{URL: u})
		}
		res.Pages = pages
	}
	return res
}

// ---------------------------------------------------------------------------
// Crawler implementation
// ---------------------------------------------------------------------------

type crawlTarget struct {
	url            string
	depth          int
	discoveredFrom string
}

type sitemapCrawler struct {
	ctx      context.Context
	sess     *cdp.BrowserSession
	input    sitemapInput
	start    *url.URL
	origin   string
	visited  map[string]*sitemapPage
	unreached []sitemapPage
	seen     map[string]bool // url -> in queue or visited

	robots    *robotsRules
	sitemapURLs []string

	mu sync.Mutex
}

func (c *sitemapCrawler) run() (*sitemapResult, error) {
	startedAt := time.Now()

	// 1. robots.txt & sitemap.xml
	if c.input.ObeyRobotsTxt {
		c.robots = fetchRobots(c.ctx, c.origin)
	}
	if c.input.IncludeSitemapXML {
		c.sitemapURLs = fetchSitemapXML(c.ctx, c.origin, c.robots)
	}

	// 2. BFS
	queue := []crawlTarget{{url: c.start.String(), depth: 0}}
	c.seen[normalizeURL(c.start.String())] = true

	// Seed from sitemap.xml
	for _, su := range c.sitemapURLs {
		n := normalizeURL(su)
		if !c.seen[n] {
			c.seen[n] = true
			queue = append(queue, crawlTarget{url: su, depth: 0, discoveredFrom: "sitemap.xml"})
		}
	}

	ignoreRes := compileIgnore(c.input.IgnorePatterns)

	for len(queue) > 0 && len(c.visited) < c.input.MaxPages {
		// Process a batch up to Concurrency wide (but sequentially for Stage 1 —
		// a single session only has one active target at a time; true parallelism
		// needs multi-Context which is Stage 2).
		target := queue[0]
		queue = queue[1:]

		if target.depth > c.input.MaxDepth {
			continue
		}
		if c.input.ObeyRobotsTxt && c.robots != nil && c.robots.disallowed(target.url) {
			continue
		}
		if shouldIgnore(target.url, ignoreRes) {
			continue
		}

		page, links, err := c.fetchPage(target)
		if err != nil {
			page = &sitemapPage{
				URL: target.url, Depth: target.depth, DiscoveredFrom: target.discoveredFrom,
				Status: 0,
			}
			c.unreached = append(c.unreached, *page)
		} else {
			c.visited[normalizeURL(target.url)] = page
			for _, l := range links {
				n := normalizeURL(l)
				if c.seen[n] {
					continue
				}
				// Same-origin filter
				if c.input.SameOriginOnly && !sameOrigin(c.origin, l) {
					page.ExternalLinks++
					continue
				}
				c.seen[n] = true
				queue = append(queue, crawlTarget{url: l, depth: target.depth + 1, discoveredFrom: target.url})
			}
		}

		if c.input.DelayMS > 0 {
			select {
			case <-c.ctx.Done():
				return nil, c.ctx.Err()
			case <-time.After(time.Duration(c.input.DelayMS) * time.Millisecond):
			}
		}
	}

	// 3. Mine patterns from visited URLs + unreached
	allURLs := make([]string, 0, len(c.visited))
	for _, p := range c.visited {
		allURLs = append(allURLs, p.URL)
	}
	patterns := mineRoutePatterns(allURLs)

	// 4. Assign canonical to each page
	urlToPattern := make(map[string]string, len(allURLs))
	for _, pat := range patterns {
		// Re-match to find which URLs belong to which pattern
		re := patternToRegex(pat.Pattern)
		for _, u := range allURLs {
			if re.MatchString(u) {
				urlToPattern[u] = pat.Pattern
			}
		}
	}

	pages := make([]sitemapPage, 0, len(c.visited))
	for _, p := range c.visited {
		cp := *p
		if can, ok := urlToPattern[p.URL]; ok {
			cp.Canonical = can
		} else {
			cp.Canonical = p.URL
		}
		pages = append(pages, cp)
	}
	sort.Slice(pages, func(i, j int) bool { return pages[i].URL < pages[j].URL })

	truncated := len(c.visited) >= c.input.MaxPages

	out := &sitemapResult{
		StartURL:       c.start.String(),
		PagesVisited:   len(c.visited),
		RoutePatterns:  patterns,
		SitemapXMLUrls: c.sitemapURLs,
		Unreachable:    c.unreached,
		SummaryOnly:    c.input.SummaryOnly,
		Truncated:      truncated,
		ElapsedMS:      time.Since(startedAt).Milliseconds(),
	}
	if c.robots != nil {
		out.RobotsDisallow = c.robots.disallowPaths
	}
	if !c.input.SummaryOnly {
		out.Pages = pages
	}
	return out, nil
}

// fetchPage navigates to a URL (via CDP), extracts title, links, forms.
func (c *sitemapCrawler) fetchPage(target crawlTarget) (*sitemapPage, []string, error) {
	// Use browser.open-style navigation on the shared session.
	if err := c.sess.Navigate(c.ctx, target.url); err != nil {
		return nil, nil, err
	}

	// Wait briefly for DOM ready
	waitCtx, cancel := context.WithTimeout(c.ctx, 15*time.Second)
	defer cancel()
	_ = pollUntil(waitCtx, 200*time.Millisecond, func() (bool, error) {
		return evalBool(waitCtx, c.sess, `document.readyState === "complete" || document.readyState === "interactive"`)
	})

	// Extract via injected JS
	js := `JSON.stringify({
		title: document.title,
		links: Array.from(document.querySelectorAll('a[href]')).map(function(a){return a.href}).slice(0, 500),
		forms: Array.from(document.querySelectorAll('form')).map(function(f){return f.action||''}).slice(0, 50)
	})`
	var raw struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
	}
	if err := c.sess.Exec(c.ctx, "Runtime.evaluate", map[string]interface{}{
		"expression":    js,
		"returnByValue": true,
	}, &raw); err != nil {
		return nil, nil, err
	}
	var asStr string
	if err := json.Unmarshal(raw.Result.Value, &asStr); err != nil {
		return nil, nil, fmt.Errorf("result not string")
	}
	var data struct {
		Title string   `json:"title"`
		Links []string `json:"links"`
		Forms []string `json:"forms"`
	}
	if err := json.Unmarshal([]byte(asStr), &data); err != nil {
		return nil, nil, fmt.Errorf("parse: %w", err)
	}

	p := &sitemapPage{
		URL:            target.url,
		Title:          data.Title,
		Depth:          target.depth,
		DiscoveredFrom: target.discoveredFrom,
		Status:         200, // CDP navigate doesn't expose HTTP status easily; assume 200 on success
		HasForms:       len(data.Forms) > 0,
	}
	if p.HasForms {
		for _, f := range data.Forms {
			if f != "" {
				p.FormActions = append(p.FormActions, f)
			}
		}
	}

	// Count internal vs external links
	links := make([]string, 0, len(data.Links))
	for _, l := range data.Links {
		if !strings.HasPrefix(l, "http") {
			continue
		}
		if sameOrigin(c.origin, l) {
			p.InternalLinks++
		} else {
			p.ExternalLinks++
		}
		// Strip fragment for dedup
		if i := strings.IndexByte(l, '#'); i >= 0 {
			l = l[:i]
		}
		links = append(links, l)
	}
	return p, links, nil
}

// ---------------------------------------------------------------------------
// Helpers: URL normalization, origin check, ignore globs
// ---------------------------------------------------------------------------

// normalizeURL trims fragment and trailing slash for dedup.
func normalizeURL(u string) string {
	if i := strings.IndexByte(u, '#'); i >= 0 {
		u = u[:i]
	}
	u = strings.TrimRight(u, "/")
	return u
}

func sameOrigin(origin, u string) bool {
	parsed, err := url.Parse(u)
	if err != nil {
		return false
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return false
	}
	return origin == parsed.Scheme+"://"+parsed.Host
}

func compileIgnore(patterns []string) []*regexp.Regexp {
	out := make([]*regexp.Regexp, 0, len(patterns))
	for _, p := range patterns {
		// Translate glob '*' to '.*', '?' to '.'
		escaped := regexp.QuoteMeta(p)
		escaped = strings.ReplaceAll(escaped, `\*`, `.*`)
		escaped = strings.ReplaceAll(escaped, `\?`, `.`)
		re, err := regexp.Compile("^" + escaped + "$")
		if err == nil {
			out = append(out, re)
		}
	}
	return out
}

func shouldIgnore(u string, res []*regexp.Regexp) bool {
	for _, re := range res {
		if re.MatchString(u) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Route pattern mining
// ---------------------------------------------------------------------------

// mineRoutePatterns groups URLs by their path structure and parameterizes
// segments whose values differ between visits.
//
// Bucket key is (origin, per-segment shape fingerprint), where each segment's
// shape is either its literal (word-looking, short) OR its inferred
// parameter type (:id / :uuid / :slug / :hash / :date). This means:
//   /users/42 and /users/137 → same bucket (literal 'users' + :id)
//   /posts/hello-world       → different bucket (literal 'posts' + :slug)
// Without this, a naïve (origin, segCount) key collapses unrelated routes.
func mineRoutePatterns(urls []string) []sitemapPattern {
	type bucketKey struct {
		originPrefix string
		shape        string
	}
	buckets := make(map[bucketKey][]string)

	for _, u := range urls {
		parsed, err := url.Parse(u)
		if err != nil {
			continue
		}
		origin := parsed.Scheme + "://" + parsed.Host
		segments := splitPath(parsed.Path)
		shapeParts := make([]string, len(segments))
		for i, s := range segments {
			shapeParts[i] = paramize(s)
		}
		key := bucketKey{originPrefix: origin, shape: strings.Join(shapeParts, "/")}
		buckets[key] = append(buckets[key], u)
	}

	patterns := make([]sitemapPattern, 0)
	for key, bucketURLs := range buckets {
		if len(bucketURLs) == 0 {
			continue
		}

		// Same shape → segments agree on which positions are literal vs param.
		// Use the first URL as the canonical structure; overlay param-type
		// detection from all distinct values at each param position.
		firstParsed, _ := url.Parse(bucketURLs[0])
		segs := splitPath(firstParsed.Path)
		segCount := len(segs)

		// Collect distinct values per segment across all URLs in bucket.
		perSeg := make([][]string, segCount)
		for i := range perSeg {
			perSeg[i] = make([]string, 0, len(bucketURLs))
		}
		for _, u := range bucketURLs {
			parsed, _ := url.Parse(u)
			ps := splitPath(parsed.Path)
			for i := 0; i < segCount && i < len(ps); i++ {
				perSeg[i] = append(perSeg[i], ps[i])
			}
		}

		var pb strings.Builder
		pb.WriteString(key.originPrefix)
		for i := 0; i < segCount; i++ {
			pb.WriteByte('/')
			// Shape decided by paramize() of first URL's segment — identical
			// for all URLs in this bucket because they share the same shape.
			shaped := paramize(segs[i])
			if strings.HasPrefix(shaped, ":") {
				// Param position — refine using distinct actual values
				distinct := uniqueStrings(perSeg[i])
				pb.WriteString(inferParamType(distinct))
			} else {
				// Literal — write as-is
				pb.WriteString(shaped)
			}
		}
		patternStr := pb.String()
		example := bucketURLs[0]
		patterns = append(patterns, sitemapPattern{
			Pattern:    patternStr,
			Matches:    len(bucketURLs),
			Example:    example,
			Type:       "page",
			Confidence: confidenceFromVariance(perSeg),
		})
	}

	// Merge identical patterns (can happen when single-URL buckets collapse)
	patterns = mergePatterns(patterns)
	sort.Slice(patterns, func(i, j int) bool { return patterns[i].Matches > patterns[j].Matches })
	return patterns
}

func splitPath(p string) []string {
	p = strings.Trim(p, "/")
	if p == "" {
		return []string{}
	}
	return strings.Split(p, "/")
}

// paramizePathRoute parameterizes a single path string.
func paramizePathRoute(p string) string {
	segs := splitPath(p)
	for i, s := range segs {
		segs[i] = paramize(s)
	}
	return "/" + strings.Join(segs, "/")
}

// inferParamType picks the most specific parameter type for a set of
// distinct segment values.
func inferParamType(values []string) string {
	allDigits := true
	allUUID := true
	allHex40 := true
	allDate := true
	allSlug := true
	for _, v := range values {
		if !isAllDigits(v) {
			allDigits = false
		}
		if !(len(v) == 36 && v[8] == '-' && v[13] == '-' && v[18] == '-' && v[23] == '-') {
			allUUID = false
		}
		if !(len(v) == 40 && isHex(v)) {
			allHex40 = false
		}
		if !(len(v) == 10 && v[4] == '-' && v[7] == '-') {
			allDate = false
		}
		if !(len(v) > 2 && containsByte(v, '-') && hasLetter(v)) {
			allSlug = false
		}
	}
	switch {
	case allDigits:
		return ":id"
	case allUUID:
		return ":uuid"
	case allHex40:
		return ":hash"
	case allDate:
		return ":date"
	case allSlug:
		return ":slug"
	}
	return ":arg"
}

func uniqueStrings(xs []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	if len(out) == 0 {
		out = append(out, "")
	}
	return out
}

func confidenceFromVariance(segs [][]string) float64 {
	// Higher variance in parameterized positions → higher confidence.
	totalVariable := 0
	for _, s := range segs {
		if len(uniqueStrings(s)) > 1 {
			totalVariable++
		}
	}
	if len(segs) == 0 {
		return 0.8
	}
	base := 0.7 + 0.3*float64(totalVariable)/float64(len(segs))
	if base > 1.0 {
		base = 1.0
	}
	return base
}

func mergePatterns(ps []sitemapPattern) []sitemapPattern {
	m := make(map[string]*sitemapPattern, len(ps))
	for i := range ps {
		k := ps[i].Pattern
		if existing, ok := m[k]; ok {
			existing.Matches += ps[i].Matches
			if ps[i].Confidence > existing.Confidence {
				existing.Confidence = ps[i].Confidence
			}
		} else {
			p := ps[i]
			m[k] = &p
		}
	}
	out := make([]sitemapPattern, 0, len(m))
	for _, p := range m {
		out = append(out, *p)
	}
	return out
}

// patternToRegex converts "/users/:id" into a regex for re-matching URLs.
func patternToRegex(pattern string) *regexp.Regexp {
	// Escape literal parts, replace :name with [^/]+
	re := regexp.MustCompile(`:\w+`)
	escaped := re.ReplaceAllStringFunc(regexp.QuoteMeta(pattern), func(_ string) string { return "[^/]+" })
	// Re-convert \: to : (QuoteMeta escapes colons)
	escaped = strings.ReplaceAll(escaped, `:\w\+`, `:\w+`)
	// Simpler: hand-built
	parts := strings.Split(pattern, "/")
	for i, p := range parts {
		if strings.HasPrefix(p, ":") {
			parts[i] = "[^/]+"
		} else {
			parts[i] = regexp.QuoteMeta(p)
		}
	}
	finalExpr := "^" + strings.Join(parts, "/") + "$"
	compiled, err := regexp.Compile(finalExpr)
	if err != nil {
		// Fall back to never-match
		return regexp.MustCompile(`\A\z`)
	}
	_ = escaped
	return compiled
}

// ---------------------------------------------------------------------------
// robots.txt
// ---------------------------------------------------------------------------

type robotsRules struct {
	disallowPaths []string
	sitemaps      []string
}

func (r *robotsRules) disallowed(u string) bool {
	if r == nil {
		return false
	}
	parsed, err := url.Parse(u)
	if err != nil {
		return false
	}
	p := parsed.Path
	for _, dp := range r.disallowPaths {
		if dp == "" {
			continue
		}
		if strings.HasSuffix(dp, "*") {
			prefix := strings.TrimSuffix(dp, "*")
			if strings.HasPrefix(p, prefix) {
				return true
			}
			continue
		}
		if strings.HasPrefix(p, dp) {
			return true
		}
	}
	return false
}

func fetchRobots(ctx context.Context, origin string) *robotsRules {
	req, err := http.NewRequestWithContext(ctx, "GET", origin+"/robots.txt", nil)
	if err != nil {
		return nil
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != 200 {
		if resp != nil {
			resp.Body.Close()
		}
		return nil
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if err != nil {
		return nil
	}
	return parseRobots(string(body))
}

func parseRobots(body string) *robotsRules {
	rules := &robotsRules{}
	agentApplies := false
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		lc := strings.ToLower(line)
		if strings.HasPrefix(lc, "user-agent:") {
			ua := strings.TrimSpace(line[len("user-agent:"):])
			agentApplies = ua == "*"
			continue
		}
		if strings.HasPrefix(lc, "sitemap:") {
			rules.sitemaps = append(rules.sitemaps, strings.TrimSpace(line[len("sitemap:"):]))
			continue
		}
		if !agentApplies {
			continue
		}
		if strings.HasPrefix(lc, "disallow:") {
			p := strings.TrimSpace(line[len("disallow:"):])
			if p != "" {
				rules.disallowPaths = append(rules.disallowPaths, p)
			}
		}
	}
	return rules
}

// ---------------------------------------------------------------------------
// sitemap.xml
// ---------------------------------------------------------------------------

type sitemapXMLIndex struct {
	XMLName xml.Name `xml:"sitemapindex"`
	Entries []struct {
		Loc string `xml:"loc"`
	} `xml:"sitemap"`
}

type urlset struct {
	XMLName xml.Name `xml:"urlset"`
	URLs    []struct {
		Loc string `xml:"loc"`
	} `xml:"url"`
}

func fetchSitemapXML(ctx context.Context, origin string, robots *robotsRules) []string {
	urls := []string{origin + "/sitemap.xml"}
	if robots != nil {
		urls = append(urls, robots.sitemaps...)
	}

	out := []string{}
	visited := map[string]bool{}
	for _, u := range urls {
		if visited[u] {
			continue
		}
		visited[u] = true
		entries := fetchSingleSitemap(ctx, u, visited, 0)
		out = append(out, entries...)
	}
	// Dedup
	seen := map[string]bool{}
	uniq := make([]string, 0, len(out))
	for _, u := range out {
		if !seen[u] {
			seen[u] = true
			uniq = append(uniq, u)
		}
	}
	if len(uniq) > 200 {
		uniq = uniq[:200]
	}
	return uniq
}

func fetchSingleSitemap(ctx context.Context, u string, visited map[string]bool, depth int) []string {
	if depth > 2 {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != 200 {
		if resp != nil {
			resp.Body.Close()
		}
		return nil
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if err != nil {
		return nil
	}

	// Try urlset first
	var set urlset
	if xml.Unmarshal(body, &set) == nil && len(set.URLs) > 0 {
		out := make([]string, 0, len(set.URLs))
		for _, u := range set.URLs {
			if u.Loc != "" {
				out = append(out, u.Loc)
			}
		}
		return out
	}
	// Try sitemap index
	var idx sitemapXMLIndex
	if xml.Unmarshal(body, &idx) == nil && len(idx.Entries) > 0 {
		out := []string{}
		for _, e := range idx.Entries {
			if e.Loc == "" || visited[e.Loc] {
				continue
			}
			visited[e.Loc] = true
			out = append(out, fetchSingleSitemap(ctx, e.Loc, visited, depth+1)...)
		}
		return out
	}
	return nil
}
