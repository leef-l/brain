package tool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// P1.3 后台管理 CRUD 场景包测试。
//
// 两层覆盖:
//   1. 契约:AdminSeedPatterns 返回的每个模式结构合法(id 唯一 / category=admin
//      / 非 delete 模式默认 deleteGuard / OnAnomaly 规则正确)。
//   2. 跨框架一致性:AdminLTE / Ant Design Pro / React Admin 三份 fixture
//      对"表格分页"最通用模式都应命中,验证 UIPattern 能做到跨站复用。

func loadFixture(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join("ui_patterns_admin_fixtures", name)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return string(b)
}

func getAdminPatternByID(t *testing.T, id string) *UIPattern {
	t.Helper()
	for _, p := range AdminSeedPatterns() {
		if p.ID == id {
			return p
		}
	}
	t.Fatalf("admin pattern %q not found", id)
	return nil
}

// ---------------------------------------------------------------------------
// 结构契约
// ---------------------------------------------------------------------------

func TestAdminSeedPatternsCount(t *testing.T) {
	got := len(AdminSeedPatterns())
	// 任务要求 5-8 个。
	if got < 5 || got > 8 {
		t.Fatalf("admin seed count = %d, want 5-8", got)
	}
}

func TestAdminSeedPatternsHaveUniqueIDs(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range AdminSeedPatterns() {
		if p.ID == "" {
			t.Errorf("empty pattern ID in seeds")
		}
		if !strings.HasPrefix(p.ID, "admin_") {
			t.Errorf("admin pattern %q should have admin_ prefix", p.ID)
		}
		if seen[p.ID] {
			t.Errorf("duplicate pattern ID: %s", p.ID)
		}
		seen[p.ID] = true
		if p.Category != "admin" {
			t.Errorf("pattern %q category = %q, want admin", p.ID, p.Category)
		}
		if p.Source != "seed" {
			t.Errorf("pattern %q source = %q, want seed", p.ID, p.Source)
		}
	}
}

func TestAdminSeedPatternsDeleteGuard(t *testing.T) {
	// 非 delete 类模式遇到 confirm_delete 应该 abort;batch_action 和
	// row_delete_confirm 里的 confirm 是预期行为,OnAnomaly 不用挂 abort。
	nonDelete := []string{
		"admin_table_pagination",
		"admin_table_next_page",
		"admin_infinite_scroll_load_more",
		"admin_filter_apply",
		"admin_row_edit",
		"admin_export_csv",
	}
	for _, id := range nonDelete {
		p := getAdminPatternByID(t, id)
		h, ok := p.OnAnomaly["confirm_delete"]
		if !ok {
			t.Errorf("%s: must register confirm_delete guard", id)
			continue
		}
		if h.Action != "abort" {
			t.Errorf("%s: confirm_delete action = %q, want abort", id, h.Action)
		}
	}

	// admin_row_delete_confirm 是唯一一个把确认 modal 当正常路径的 —— 它
	// 不应该把 confirm_delete 映射为 abort(没登记也行,没登记时 runActionSequence
	// 不会把它当异常处理)。
	del := getAdminPatternByID(t, "admin_row_delete_confirm")
	if h, ok := del.OnAnomaly["confirm_delete"]; ok && h.Action == "abort" {
		t.Errorf("admin_row_delete_confirm should not abort on confirm_delete")
	}

	// admin_batch_action 同理 —— 批量流程本来就会走 confirm。
	batch := getAdminPatternByID(t, "admin_batch_action")
	if h, ok := batch.OnAnomaly["confirm_delete"]; ok && h.Action == "abort" {
		t.Errorf("admin_batch_action should not abort on confirm_delete (part of flow)")
	}
}

func TestAdminPatternsReuseBuiltinToolsOnly(t *testing.T) {
	// 复用铁律:ActionSequence 里只能出现已经存在的 browser.* / wait.*
	// 工具。新写一个工具需要改 tool registry,超出本任务范围。
	allowed := map[string]bool{
		"browser.click":           true,
		"browser.type":            true,
		"browser.press_key":       true,
		"browser.select_option":   true,
		"browser.fill_form":       true,
		"browser.scroll":          true,
		"browser.downloads":       true,
		"browser.snapshot":        true,
		"wait.network_idle":       true,
	}
	for _, p := range AdminSeedPatterns() {
		for _, step := range p.ActionSequence {
			if !allowed[step.Tool] {
				t.Errorf("pattern %s uses un-allowlisted tool %q", p.ID, step.Tool)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// 跨框架 fixture 匹配 — UIPattern 必须在 AdminLTE / AntD Pro / React Admin
// 三种实现上都判定为适用。
// ---------------------------------------------------------------------------

type frameworkFixture struct {
	name  string
	file  string
	url   string
	title string
}

var adminFixtures = []frameworkFixture{
	{name: "AdminLTE", file: "adminlte_users.html", url: "https://demo.example.com/users?page=1", title: "Users | AdminLTE Demo"},
	{name: "AntDesignPro", file: "antd_pro_orders.html", url: "https://demo.example.com/orders?status=paid", title: "订单管理 - Ant Design Pro"},
	{name: "ReactAdmin", file: "react_admin_posts.html", url: "https://demo.example.com/#/posts", title: "Posts - React Admin"},
}

// TestAdminTablePaginationMatchesAllFrameworks 验证"表格分页"作为最通用
// 模式,在三种主流 admin 框架 fixture 下都能命中 AppliesWhen。
func TestAdminTablePaginationMatchesAllFrameworks(t *testing.T) {
	p := getAdminPatternByID(t, "admin_table_pagination")
	for _, fx := range adminFixtures {
		html := loadFixture(t, fx.file)
		ok := MatchStaticHTML(&p.AppliesWhen, fx.url, fx.title, html)
		if !ok {
			t.Errorf("%s: admin_table_pagination should match fixture %s", fx.name, fx.file)
		}
	}
}

func TestAdminBatchActionMatchesAllFrameworks(t *testing.T) {
	p := getAdminPatternByID(t, "admin_batch_action")
	for _, fx := range adminFixtures {
		html := loadFixture(t, fx.file)
		ok := MatchStaticHTML(&p.AppliesWhen, fx.url, fx.title, html)
		if !ok {
			t.Errorf("%s: admin_batch_action should match fixture %s", fx.name, fx.file)
		}
	}
}

func TestAdminRowEditMatchesAllFrameworks(t *testing.T) {
	p := getAdminPatternByID(t, "admin_row_edit")
	for _, fx := range adminFixtures {
		html := loadFixture(t, fx.file)
		ok := MatchStaticHTML(&p.AppliesWhen, fx.url, fx.title, html)
		if !ok {
			t.Errorf("%s: admin_row_edit should match fixture %s", fx.name, fx.file)
		}
	}
}

func TestAdminExportMatchesAllFrameworks(t *testing.T) {
	p := getAdminPatternByID(t, "admin_export_csv")
	for _, fx := range adminFixtures {
		html := loadFixture(t, fx.file)
		ok := MatchStaticHTML(&p.AppliesWhen, fx.url, fx.title, html)
		if !ok {
			t.Errorf("%s: admin_export_csv should match fixture %s", fx.name, fx.file)
		}
	}
}

// TestAdminFilterMatchesAllFrameworks 筛选模式应在三套 fixture 下命中 ——
// 因为都有 <table> + 筛选控件。
func TestAdminFilterMatchesAllFrameworks(t *testing.T) {
	p := getAdminPatternByID(t, "admin_filter_apply")
	for _, fx := range adminFixtures {
		html := loadFixture(t, fx.file)
		ok := MatchStaticHTML(&p.AppliesWhen, fx.url, fx.title, html)
		if !ok {
			t.Errorf("%s: admin_filter_apply should match fixture %s", fx.name, fx.file)
		}
	}
}

// TestInfiniteScrollDoesNotMatchPaginatedTable 负向用例:无限滚动模式
// 的 HasNot 会排除包含 .pagination 的 AdminLTE / .ant-pagination 的 AntD
// fixture —— 避免在有传统分页条的页面上误触发 scroll。
func TestInfiniteScrollDoesNotMatchPaginatedTable(t *testing.T) {
	p := getAdminPatternByID(t, "admin_infinite_scroll_load_more")
	// AdminLTE 和 AntD 都带 .pagination / .ant-pagination —— 应 NOT 命中。
	for _, fx := range []frameworkFixture{adminFixtures[0], adminFixtures[1]} {
		html := loadFixture(t, fx.file)
		if MatchStaticHTML(&p.AppliesWhen, fx.url, fx.title, html) {
			t.Errorf("%s: infinite_scroll should NOT match a paginated table fixture", fx.name)
		}
	}
}

// ---------------------------------------------------------------------------
// SeedAdmin 幂等 Upsert
// ---------------------------------------------------------------------------

func TestSeedAdminIdempotent(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "admin.db")
	lib, err := NewPatternLibrary(dsn)
	if err != nil {
		t.Fatalf("NewPatternLibrary: %v", err)
	}
	defer lib.Close()

	ctx := context.Background()
	// init() 已经把 AdminSeedPatterns 挂到 extraSeedProviders,空库首次
	// Seed 时 admin 种子已经自动入库。再调 SeedAdmin 是幂等 Upsert 覆盖,
	// 总数不应变化。
	want := len(AdminSeedPatterns())
	baseline := len(lib.ListAll(""))

	if err := SeedAdmin(ctx, lib); err != nil {
		t.Fatalf("SeedAdmin first: %v", err)
	}
	afterFirst := len(lib.ListAll(""))
	if afterFirst != baseline {
		t.Errorf("SeedAdmin after auto-register changed count: %d → %d (should be no-op)",
			baseline, afterFirst)
	}

	if err := SeedAdmin(ctx, lib); err != nil {
		t.Fatalf("SeedAdmin second: %v", err)
	}
	afterSecond := len(lib.ListAll(""))
	if afterSecond != afterFirst {
		t.Errorf("SeedAdmin not idempotent: count went from %d to %d", afterFirst, afterSecond)
	}

	// Category 过滤应返回全部 admin 模式。
	adminOnly := lib.ListAll("admin")
	if len(adminOnly) != want {
		t.Errorf("ListAll(admin) = %d, want %d", len(adminOnly), want)
	}
}

func TestSeedAdminNilLibSafe(t *testing.T) {
	if err := SeedAdmin(context.Background(), nil); err != nil {
		t.Errorf("SeedAdmin(nil) should be no-op, got %v", err)
	}
}

// 新建空 PatternLibrary 时,seedPatterns() 会把 extraSeedProviders 里的
// AdminSeedPatterns 自动并入。这里验证挂接生效:空库 Seed 后能 Get 到
// 8 个 admin ID,ops 不用再显式调 SeedAdmin。
func TestAdminSeedsAutoRegisteredViaInit(t *testing.T) {
	lib := newTestLib(t)
	required := []string{
		"admin_table_pagination",
		"admin_table_next_page",
		"admin_infinite_scroll_load_more",
		"admin_filter_apply",
		"admin_batch_action",
		"admin_row_edit",
		"admin_row_delete_confirm",
		"admin_export_csv",
	}
	for _, id := range required {
		if got := lib.Get(id); got == nil {
			t.Errorf("admin seed %q not auto-registered via init() → extraSeedProviders", id)
		}
	}
}

// ---------------------------------------------------------------------------
// staticSelectorMatches 边界
// ---------------------------------------------------------------------------

func TestStaticSelectorBasicForms(t *testing.T) {
	html := strings.ToLower(`<table class="ant-table"><tr><td class="ant-table-cell">x</td></tr></table>
<div role="alertdialog" aria-label="delete">...</div>
<input type="checkbox" data-brain-id="7">
<nav aria-label="pagination"><a rel="next" href="#">Next</a></nav>`)

	cases := []struct {
		sel  string
		want bool
	}{
		{`table`, true},
		{`.ant-table`, true},
		{`.MuiDataGrid-root`, false},
		{`input[type="checkbox"]`, true},
		{`input[type="password"]`, false},
		{`[role="alertdialog"]`, true},
		{`[aria-label*="pag" i]`, true},
		{`a[rel="next"]`, true},
		{`table, [role="table"]`, true}, // 列表中任一命中即真
	}
	for _, c := range cases {
		got := staticSelectorMatches(html, c.sel)
		if got != c.want {
			t.Errorf("selector %q: got %v, want %v", c.sel, got, c.want)
		}
	}
}

func TestMatchStaticHTMLURLPattern(t *testing.T) {
	cond := &MatchCondition{URLPattern: `(?i)/users`}
	if !MatchStaticHTML(cond, "https://x.com/users", "", "") {
		t.Errorf("url pattern should match /users")
	}
	if MatchStaticHTML(cond, "https://x.com/orders", "", "") {
		t.Errorf("url pattern should not match /orders")
	}
}

func TestMatchStaticHTMLTitleContains(t *testing.T) {
	cond := &MatchCondition{TitleContains: []string{"Admin"}}
	if !MatchStaticHTML(cond, "", "React Admin Home", "") {
		t.Errorf("title contains should match")
	}
	if MatchStaticHTML(cond, "", "Public Site", "") {
		t.Errorf("title contains should not match")
	}
}
