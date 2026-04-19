package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/leef-l/brain/sdk/persistence"
)

// P3.4 — sitemap 缓存测试 + benchmark。
//
// 不开浏览器、不触网:覆盖 sitemapResultFromCache 的装配 + cache 读取命中
// 判定 + TTL 过期语义。真实 crawler.run() 在另一个集成测试里跑。

// memSitemapCache 是纯内存实现,用于单测/benchmark,不依赖 SQLite。
type memSitemapCache struct {
	mu    sync.Mutex
	items map[string]*persistence.SitemapSnapshot // key: origin|depth
}

func newMemSitemapCache() *memSitemapCache {
	return &memSitemapCache{items: make(map[string]*persistence.SitemapSnapshot)}
}

func (c *memSitemapCache) Save(_ context.Context, snap *persistence.SitemapSnapshot) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := fmt.Sprintf("%s|%d", snap.SiteOrigin, snap.Depth)
	cp := *snap
	if cp.CollectedAt.IsZero() {
		cp.CollectedAt = time.Now().UTC()
	}
	c.items[key] = &cp
	return nil
}

func (c *memSitemapCache) Get(_ context.Context, origin string, depth int) (*persistence.SitemapSnapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := fmt.Sprintf("%s|%d", origin, depth)
	if v, ok := c.items[key]; ok {
		cp := *v
		return &cp, nil
	}
	return nil, nil
}

func (c *memSitemapCache) Purge(_ context.Context, olderThan time.Time) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var n int64
	for k, v := range c.items {
		if v.CollectedAt.Before(olderThan) {
			delete(c.items, k)
			n++
		}
	}
	return n, nil
}

// -----------------------------------------------------------------------------
// sitemapResultFromCache 装配
// -----------------------------------------------------------------------------

func TestSitemapResultFromCacheSummary(t *testing.T) {
	urls := []string{
		"https://shop.example.com/",
		"https://shop.example.com/products",
		"https://shop.example.com/products/1",
		"https://shop.example.com/products/42",
	}
	body, _ := json.Marshal(urls)
	snap := &persistence.SitemapSnapshot{
		SiteOrigin:  "https://shop.example.com",
		Depth:       3,
		URLs:        body,
		CollectedAt: time.Now().UTC(),
	}
	res := sitemapResultFromCache(
		"https://shop.example.com",
		sitemapInput{StartURL: "https://shop.example.com", MaxDepth: 3, SummaryOnly: true},
		snap,
	)
	if res == nil {
		t.Fatal("expected non-nil result")
	}
	if !res.CacheHit {
		t.Error("CacheHit must be true")
	}
	if res.PagesVisited != 4 {
		t.Errorf("PagesVisited = %d, want 4", res.PagesVisited)
	}
	if len(res.Pages) != 0 {
		t.Errorf("SummaryOnly → Pages must be empty, got %d", len(res.Pages))
	}
	if len(res.RoutePatterns) == 0 {
		t.Error("expected mined route patterns from cached URLs")
	}
}

func TestSitemapResultFromCache_Pages(t *testing.T) {
	urls := []string{"https://shop.example.com/a", "https://shop.example.com/b"}
	body, _ := json.Marshal(urls)
	snap := &persistence.SitemapSnapshot{
		SiteOrigin: "https://shop.example.com",
		Depth:      2,
		URLs:       body,
	}
	res := sitemapResultFromCache(
		"https://shop.example.com",
		sitemapInput{StartURL: "https://shop.example.com", MaxDepth: 2, SummaryOnly: false},
		snap,
	)
	if res == nil || len(res.Pages) != 2 {
		t.Fatalf("Pages = %+v, want 2", res)
	}
}

func TestSitemapResultFromCache_BrokenJSON(t *testing.T) {
	snap := &persistence.SitemapSnapshot{
		SiteOrigin: "https://s.com", Depth: 1,
		URLs: []byte("not-json"),
	}
	if res := sitemapResultFromCache("https://s.com", sitemapInput{}, snap); res != nil {
		t.Errorf("broken JSON must return nil, got %+v", res)
	}
}

// -----------------------------------------------------------------------------
// Save / Get 往返
// -----------------------------------------------------------------------------

func TestSitemapCacheSaveGet(t *testing.T) {
	cache := newMemSitemapCache()
	SetSitemapCache(cache)
	defer SetSitemapCache(nil)

	urls := []string{"https://site.com/a", "https://site.com/b"}
	body, _ := json.Marshal(urls)
	_ = cache.Save(context.Background(), &persistence.SitemapSnapshot{
		SiteOrigin: "https://site.com", Depth: 2, URLs: body,
	})
	got, _ := cache.Get(context.Background(), "https://site.com", 2)
	if got == nil {
		t.Fatal("expected cache hit")
	}
	// 命中后装配
	res := sitemapResultFromCache("https://site.com",
		sitemapInput{StartURL: "https://site.com", MaxDepth: 2},
		got)
	if res == nil || !res.CacheHit {
		t.Fatalf("assembled result = %+v", res)
	}
}

// -----------------------------------------------------------------------------
// TTL 过期语义
// -----------------------------------------------------------------------------

func TestSitemapCacheTTL(t *testing.T) {
	cache := newMemSitemapCache()
	urls := []string{"https://site.com/a"}
	body, _ := json.Marshal(urls)
	_ = cache.Save(context.Background(), &persistence.SitemapSnapshot{
		SiteOrigin:  "https://site.com",
		Depth:       1,
		URLs:        body,
		CollectedAt: time.Now().Add(-48 * time.Hour), // 显式设置为 48h 前
	})
	got, _ := cache.Get(context.Background(), "https://site.com", 1)
	if got == nil {
		t.Fatal("Get must still return row, TTL 是调用方判定")
	}
	// 24h 默认 TTL 下,这一条应被视为过期(模拟工具主循环里的判断)
	if time.Since(got.CollectedAt) <= defaultSitemapTTL {
		t.Errorf("expected expired (>24h), got age %v", time.Since(got.CollectedAt))
	}
}

// -----------------------------------------------------------------------------
// Benchmarks
// -----------------------------------------------------------------------------

// BenchmarkSitemapCached 衡量"缓存命中直接装配"路径的耗时。对比 Bench
// NoCache(模拟全量爬取路径的 BFS + HTTP 抓取成本)可以拿到相对提升。
// 这里 NoCache 只模拟一次 route mining,实际爬取耗时远超本 bench。
func BenchmarkSitemapCached(b *testing.B) {
	cache := newMemSitemapCache()
	SetSitemapCache(cache)
	defer SetSitemapCache(nil)

	urls := benchURLs(500)
	body, _ := json.Marshal(urls)
	_ = cache.Save(context.Background(), &persistence.SitemapSnapshot{
		SiteOrigin: "https://bench.com", Depth: 3, URLs: body,
	})
	input := sitemapInput{StartURL: "https://bench.com", MaxDepth: 3, SummaryOnly: true}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		snap, _ := cache.Get(context.Background(), "https://bench.com", 3)
		if snap == nil {
			b.Fatal("expected hit")
		}
		_ = sitemapResultFromCache("https://bench.com", input, snap)
	}
}

// BenchmarkSitemapNoCacheMining 只做 route mining(纯 CPU),作为"不算
// HTTP 抓取"的下限基线。缓存路径需要做的 JSON 解码 + mining 与本 bench
// 在同一数据规模上对比,看缓存总开销是否显著低于"就算网络免费也得做的
// mining"—— 实际上缓存命中时 mining 开销二者完全相同,差异在缓存只省了
// 真实 HTTP round trips(不在 bench 里)。
func BenchmarkSitemapNoCacheMining(b *testing.B) {
	urls := benchURLs(500)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = mineRoutePatterns(urls)
	}
}

func benchURLs(n int) []string {
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, fmt.Sprintf("https://bench.com/products/%d", i))
		if i%3 == 0 {
			out = append(out, fmt.Sprintf("https://bench.com/users/%d", i))
		}
	}
	return out
}
