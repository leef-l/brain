package tool

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"testing"
)

// P3.4 — 模式索引单元测试 + 基准。
//
// 不开浏览器、不触网:MatchPatterns 的 regex/selector 阶段这里也绕不过,
// 所以 benchmark 只跑 candidatePatterns(倒排预筛) ,对比"线性扫全库"
// 的耗时相对差。真实 MatchPatterns 的"预筛 + 完整评估"组合收益在集成
// 测试里才能观测到,Benchmark 在这里只量化预筛层本身。

// -----------------------------------------------------------------------------
// deriveURLBucket / matchBucketsForURL 静态行为
// -----------------------------------------------------------------------------

func TestDeriveURLBucket(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", "__any"},
		{`(?i)/(login|signin|sign-in|auth|account/login)\b`, "__any"}, // 首字符是 ( → 降级
		{`/login`, "/login"},
		{`/checkout\b`, "/checkout"},
		{`https://shop.example.com/cart`, "shop.example.com/cart"},
		{`https://admin.example.com/`, "admin.example.com"},
		{`shop.example.com/products`, "shop.example.com/products"},
		// 非字面量 host 退化
		{`[a-z]+\.example\.com/foo`, "__any"},
		// 首字符就是特殊字符
		{`.*/admin`, "__any"},
		// 单字符前缀不稳定
		{`/l`, "__any"},
	}
	for _, c := range cases {
		got := deriveURLBucket(c.in)
		if got != c.want {
			t.Errorf("deriveURLBucket(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestMatchBucketsForURL(t *testing.T) {
	keys := matchBucketsForURL("https://shop.example.com/cart/items/42")
	mustContain(t, keys, []string{"__any", "shop.example.com", "/cart", "shop.example.com/cart"})

	keys2 := matchBucketsForURL("")
	mustContain(t, keys2, []string{"__any"})
}

func mustContain(t *testing.T, got, want []string) {
	t.Helper()
	set := make(map[string]struct{}, len(got))
	for _, k := range got {
		set[k] = struct{}{}
	}
	for _, k := range want {
		if _, ok := set[k]; !ok {
			t.Errorf("keys %v missing %q", got, k)
		}
	}
}

// -----------------------------------------------------------------------------
// candidatePatterns 端到端:Upsert 后索引生效,Enabled=false 被剔除
// -----------------------------------------------------------------------------

func TestCandidatePatternsFilterByBucket(t *testing.T) {
	lib := newIndexTestLib(t)

	// 装三个模式:一个落 /login 桶,一个落 shop.example.com/cart 桶,一个 __any。
	mustUpsert(t, lib, &UIPattern{
		ID: "login-x", Category: "auth", Source: "user",
		AppliesWhen: MatchCondition{URLPattern: `/login`},
	})
	mustUpsert(t, lib, &UIPattern{
		ID: "cart-x", Category: "commerce", Source: "user",
		AppliesWhen: MatchCondition{URLPattern: `https://shop.example.com/cart`},
	})
	mustUpsert(t, lib, &UIPattern{
		ID: "universal", Category: "nav", Source: "user",
		AppliesWhen: MatchCondition{URLPattern: ``}, // any
	})
	// 一个显式禁用的模式(落 /login 桶),验证过滤。
	mustUpsert(t, lib, &UIPattern{
		ID: "login-disabled", Category: "auth", Source: "user",
		AppliesWhen: MatchCondition{URLPattern: `/login`},
	})
	_ = lib.SetEnabled(context.Background(), "login-disabled", false)

	// 登录页
	got := candidatePatterns(lib, "https://any.example.com/login?next=/", "")
	expect := []string{"login-x", "universal"}
	if !equalStringSet(got, expect) {
		t.Errorf("login-page candidates = %v, want %v", got, expect)
	}

	// 购物车页 + category=commerce:universal 没声明 category(空字符串),
	// 按规则与 category=commerce 并集,应当出现。
	got2 := candidatePatterns(lib, "https://shop.example.com/cart/items/42", "commerce")
	// 只要候选里包含 cart-x 并不含 login-x 即可;universal 因为没有 category
	// 限制,也可以参与。
	if !containsAll(got2, []string{"cart-x"}) {
		t.Errorf("cart-page candidates = %v, want contains cart-x", got2)
	}
	if contains(got2, "login-x") {
		t.Errorf("cart-page candidates = %v, must not contain login-x", got2)
	}

	// URL 桶全不命中时仍有 __any 兜底
	got3 := candidatePatterns(lib, "https://unrelated.example.com/foo/bar", "")
	if !contains(got3, "universal") {
		t.Errorf("unrelated page missing __any bucket: %v", got3)
	}
}

func TestBumpInvalidatesIndex(t *testing.T) {
	lib := newIndexTestLib(t)
	mustUpsert(t, lib, &UIPattern{
		ID: "a", Category: "nav", Source: "user",
		AppliesWhen: MatchCondition{URLPattern: `/apage`},
	})
	got := candidatePatterns(lib, "https://site.com/apage", "")
	if !contains(got, "a") {
		t.Fatalf("first query missing 'a': %v", got)
	}
	// 新增 pattern 后,索引必须重建。
	mustUpsert(t, lib, &UIPattern{
		ID: "b", Category: "nav", Source: "user",
		AppliesWhen: MatchCondition{URLPattern: `/apage`},
	})
	got2 := candidatePatterns(lib, "https://site.com/apage", "")
	if !containsAll(got2, []string{"a", "b"}) {
		t.Fatalf("after new Upsert, candidates = %v, want both a+b", got2)
	}
}

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

func newIndexTestLib(t *testing.T) *PatternLibrary {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "idx.db")
	lib, err := NewPatternLibrary(dsn)
	if err != nil {
		t.Fatalf("NewPatternLibrary: %v", err)
	}
	t.Cleanup(func() { lib.Close() })
	// Seed 进来的种子会污染候选集。为测试语义清晰,把默认 seed 全部 Delete。
	for _, p := range lib.ListAll("") {
		if p.Source == "seed" {
			_ = lib.Delete(context.Background(), p.ID)
		}
	}
	return lib
}

func mustUpsert(t *testing.T, lib *PatternLibrary, p *UIPattern) {
	t.Helper()
	if err := lib.Upsert(context.Background(), p); err != nil {
		t.Fatalf("Upsert %s: %v", p.ID, err)
	}
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
func containsAll(xs, want []string) bool {
	for _, w := range want {
		if !contains(xs, w) {
			return false
		}
	}
	return true
}
func equalStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	sa := make(map[string]struct{}, len(a))
	for _, v := range a {
		sa[v] = struct{}{}
	}
	for _, v := range b {
		if _, ok := sa[v]; !ok {
			return false
		}
	}
	return true
}

// -----------------------------------------------------------------------------
// Benchmarks
// -----------------------------------------------------------------------------

// BenchmarkMatchPatternsIndexed 衡量 P3.4 索引路径下 candidatePatterns 的
// 单次耗时。基线对比见 BenchmarkMatchPatternsLinear。
func BenchmarkMatchPatternsIndexed(b *testing.B) {
	lib := setupBenchLib(b, 80)
	urls := []string{
		"https://shop.example.com/cart/42",
		"https://any.example.com/login",
		"https://blog.example.com/post/5",
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = candidatePatterns(lib, urls[i%len(urls)], "")
	}
}

// BenchmarkMatchPatternsLinear 模拟未上索引时的"线性扫全库"路径:对每个
// pattern 都跑 regexp.Compile + MatchString,正是 evaluateMatch 现实成本
// 的核心部分。和 BenchmarkMatchPatternsIndexed 对比,衡量"把 N 次 regex
// 压到 candidate_k << N"带来的相对提升。
func BenchmarkMatchPatternsLinear(b *testing.B) {
	lib := setupBenchLib(b, 80)
	urls := []string{
		"https://shop.example.com/cart/42",
		"https://any.example.com/login",
		"https://blog.example.com/post/5",
	}
	all := lib.List("")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		pageURL := urls[i%len(urls)]
		out := make([]string, 0, 8)
		for _, p := range all {
			if p.AppliesWhen.URLPattern == "" {
				out = append(out, p.ID)
				continue
			}
			re, err := regexp.Compile(p.AppliesWhen.URLPattern)
			if err == nil && re.MatchString(pageURL) {
				out = append(out, p.ID)
			}
		}
		_ = out
	}
}

func setupBenchLib(b *testing.B, count int) *PatternLibrary {
	b.Helper()
	dsn := filepath.Join(b.TempDir(), "bench.db")
	lib, err := NewPatternLibrary(dsn)
	if err != nil {
		b.Fatalf("NewPatternLibrary: %v", err)
	}
	b.Cleanup(func() { lib.Close() })
	// 清掉种子保持计数准确。
	for _, p := range lib.ListAll("") {
		if p.Source == "seed" {
			_ = lib.Delete(context.Background(), p.ID)
		}
	}
	ctx := context.Background()
	// 80 个 pattern 分布在三个主机 + 几个 path 桶上。
	hosts := []string{"shop.example.com", "blog.example.com", "any.example.com"}
	paths := []string{"/login", "/cart", "/post", "/checkout", "/search"}
	for i := 0; i < count; i++ {
		var urlPat string
		switch i % 4 {
		case 0:
			urlPat = fmt.Sprintf("https://%s%s", hosts[i%len(hosts)], paths[i%len(paths)])
		case 1:
			urlPat = paths[i%len(paths)]
		case 2:
			urlPat = "" // any
		case 3:
			urlPat = fmt.Sprintf("https://%s/page-%d", hosts[i%len(hosts)], i)
		}
		_ = lib.Upsert(ctx, &UIPattern{
			ID:          fmt.Sprintf("p-%03d", i),
			Category:    []string{"auth", "commerce", "nav", ""}[i%4],
			Source:      "user",
			AppliesWhen: MatchCondition{URLPattern: urlPat},
		})
	}
	return lib
}
