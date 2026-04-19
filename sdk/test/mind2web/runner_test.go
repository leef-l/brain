package mind2web

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadTasks 保证 tasks/ 目录的 100 条 JSON 全部解析通过、字段合规。
func TestLoadTasks(t *testing.T) {
	tasks, err := LoadTasks("tasks")
	if err != nil {
		t.Fatalf("LoadTasks: %v", err)
	}
	if len(tasks) != 100 {
		t.Fatalf("expected 100 tasks, got %d", len(tasks))
	}
	seen := map[string]bool{}
	for _, tk := range tasks {
		if seen[tk.ID] {
			t.Fatalf("duplicate task id: %s", tk.ID)
		}
		seen[tk.ID] = true
	}
}

// 类别分布:任务说明要求 30 auth / 30 ecommerce / 20 admin / 20 misc。
func TestLoadTasksCategoryDistribution(t *testing.T) {
	tasks, err := LoadTasks("tasks")
	if err != nil {
		t.Fatalf("LoadTasks: %v", err)
	}
	want := map[string]int{
		"auth":      30,
		"ecommerce": 30,
		"admin":     20,
		"misc":      20,
	}
	got := map[string]int{}
	for _, tk := range tasks {
		got[tk.Category]++
	}
	for cat, n := range want {
		if got[cat] != n {
			t.Errorf("category %s: got %d, want %d", cat, got[cat], n)
		}
	}
}

// 跨站覆盖度:每个类别应当分布在 ≥4 个不同站点,否则不构成"跨站"基准。
func TestLoadTasksCrossSiteCoverage(t *testing.T) {
	tasks, err := LoadTasks("tasks")
	if err != nil {
		t.Fatalf("LoadTasks: %v", err)
	}
	sitesByCat := map[string]map[string]bool{}
	for _, tk := range tasks {
		m := sitesByCat[tk.Category]
		if m == nil {
			m = map[string]bool{}
			sitesByCat[tk.Category] = m
		}
		m[tk.Site] = true
	}
	minSites := map[string]int{"auth": 4, "ecommerce": 4, "admin": 4, "misc": 4}
	for cat, need := range minSites {
		if len(sitesByCat[cat]) < need {
			t.Errorf("category %s covers %d sites, want >= %d", cat, len(sitesByCat[cat]), need)
		}
	}
}

// 必备 metadata 字段:annotation_id / domain / subdomain 用于回溯 Mind2Web
// 原数据集条目,至少保留 annotation_id。
func TestLoadTasksHaveAnnotationID(t *testing.T) {
	tasks, err := LoadTasks("tasks")
	if err != nil {
		t.Fatalf("LoadTasks: %v", err)
	}
	for _, tk := range tasks {
		if tk.Metadata["annotation_id"] == "" {
			t.Errorf("%s: missing metadata.annotation_id", tk.ID)
		}
	}
}

// mockExecutor 按 tag 决定结果,给定 pattern_id 让 PatternReused 聚合有得测。
type mockExecutor struct {
	// successTag 命中即算成功
	successTag string
	// patternForCategory: 不同 category 返回不同 pattern_id,让同 category
	// 的第 2 条以后会被标记 PatternReused=true。
	patternForCategory map[string]string
}

func (m *mockExecutor) Execute(_ context.Context, task *Task) *TaskResult {
	r := &TaskResult{TaskID: task.ID, Turns: task.MaxTurns / 2}
	for _, tag := range task.Tags {
		if tag == m.successTag {
			r.Success = true
			break
		}
	}
	if !r.Success {
		r.FailReason = "mock executor: tag miss"
	}
	if pid := m.patternForCategory[task.Category]; pid != "" {
		r.PatternID = pid
	}
	return r
}

// TestRunAndReport 端到端:100 条任务跑完 → Summarize → 写 Markdown。
func TestRunAndReport(t *testing.T) {
	tasks, err := LoadTasks("tasks")
	if err != nil {
		t.Fatalf("LoadTasks: %v", err)
	}

	exec := &mockExecutor{
		successTag: "cross_site", // 绝大多数任务带此 tag,成功率接近 100%
		patternForCategory: map[string]string{
			"auth":      "login_username_password",
			"ecommerce": "ecommerce_add_to_cart_with_feedback",
			"admin":     "admin_table_pagination",
			"misc":      "search_query",
		},
	}
	results := Run(context.Background(), tasks, exec)
	if len(results) != len(tasks) {
		t.Fatalf("result count mismatch: got %d, want %d", len(results), len(tasks))
	}

	s := Summarize(results)
	if s.Total != 100 {
		t.Fatalf("summary total = %d, want 100", s.Total)
	}
	// 每个 category 的第二条任务起应被标记 PatternReused;大多数任务走了
	// 常见 pattern,复用率应 > 80%。
	if s.PatternReuseRate < 0.8 {
		t.Errorf("expected pattern reuse rate > 0.8, got %.2f", s.PatternReuseRate)
	}
	if s.PatternCount != 4 {
		t.Errorf("expected 4 distinct patterns, got %d", s.PatternCount)
	}
	if len(s.BySite) < 10 {
		t.Errorf("expected ≥10 distinct sites, got %d", len(s.BySite))
	}
	if _, ok := s.ByCategorySite["ecommerce"]; !ok {
		t.Error("missing ecommerce row in category×site matrix")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "report.md")
	if err := WriteMarkdown(path, results, s); err != nil {
		t.Fatalf("WriteMarkdown: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	body := string(raw)
	for _, needle := range []string{
		"Mind2Web 跨站泛化回归报告",
		"## 按类别",
		"## 按站点",
		"## 跨站迁移矩阵",
		"模式复用率",
	} {
		if !strings.Contains(body, needle) {
			t.Errorf("report missing section %q", needle)
		}
	}
}

// validateTask 的错误分支应当全部触发(空 id / 未知 category / success 缺失)。
func TestValidateTaskFailures(t *testing.T) {
	cases := []struct {
		name   string
		t      Task
		wantIn string
	}{
		{"empty id", Task{Category: "auth", Goal: "x", MaxTurns: 1, Success: []SuccessCheck{{Kind: "k", Value: "v"}}}, "missing id"},
		{"empty goal", Task{ID: "x", Category: "auth", MaxTurns: 1, Success: []SuccessCheck{{Kind: "k", Value: "v"}}}, "missing goal"},
		{"bad category", Task{ID: "x", Category: "weird", Goal: "g", MaxTurns: 1, Success: []SuccessCheck{{Kind: "k", Value: "v"}}}, "category"},
		{"zero max", Task{ID: "x", Category: "auth", Goal: "g", MaxTurns: 0, Success: []SuccessCheck{{Kind: "k", Value: "v"}}}, "max_turns"},
		{"empty success", Task{ID: "x", Category: "auth", Goal: "g", MaxTurns: 1}, "success check"},
		{"empty kind", Task{ID: "x", Category: "auth", Goal: "g", MaxTurns: 1, Success: []SuccessCheck{{}}}, "kind empty"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateTask(&c.t, "inline")
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", c.wantIn)
			}
			if !strings.Contains(err.Error(), c.wantIn) {
				t.Errorf("error = %q, want substring %q", err.Error(), c.wantIn)
			}
		})
	}
}
