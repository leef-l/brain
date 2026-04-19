package visualwebarena

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leef-l/brain/sdk/toolpolicy"
)

// CI smoke:tasks/ 目录所有 JSON 都能解析,字段合法。不联网、不起浏览器。
func TestLoadTasks(t *testing.T) {
	tasks, err := LoadTasks("tasks")
	if err != nil {
		t.Fatalf("LoadTasks: %v", err)
	}
	if len(tasks) != 30 {
		t.Fatalf("expected 30 tasks, got %d", len(tasks))
	}
	seen := map[string]bool{}
	hard, soft, aux := 0, 0, 0
	for _, tk := range tasks {
		if seen[tk.ID] {
			t.Fatalf("duplicate id %q", tk.ID)
		}
		seen[tk.ID] = true
		if tk.Goal == "" || tk.Site == "" {
			t.Fatalf("task %s: missing goal/site", tk.ID)
		}
		if tk.MaxTurns <= 0 {
			t.Fatalf("task %s: max_turns must be > 0", tk.ID)
		}
		if len(tk.Success) == 0 {
			t.Fatalf("task %s: at least one success check required", tk.ID)
		}
		if len(tk.Modality) == 0 {
			t.Fatalf("task %s: modality tags required", tk.ID)
		}
		switch tk.VisualRequired {
		case "hard":
			hard++
		case "soft":
			soft++
		case "aux":
			aux++
		default:
			t.Fatalf("task %s: invalid visual_required %q", tk.ID, tk.VisualRequired)
		}
	}
	// 覆盖要求:至少 10 条 hard(视觉硬依赖),用于测 visual_inspect 真实 ROI
	if hard < 10 {
		t.Errorf("hard task count = %d, want ≥ 10 for meaningful ROI signal", hard)
	}
	if soft < 5 || aux < 5 {
		t.Errorf("coverage too thin: soft=%d aux=%d (want each ≥ 5)", soft, aux)
	}
}

// TestBrowserStageMapping 验证 A/B 模式确实走已有 BrowserStage 机制,
// 而不是自造新 flag。这是"复用铁律"的固化测试。
func TestBrowserStageMapping(t *testing.T) {
	if got := BrowserStageFor(ModeDOMOnly); got != toolpolicy.BrowserStageKnownFlow {
		t.Errorf("ModeDOMOnly → %q, want %q", got, toolpolicy.BrowserStageKnownFlow)
	}
	if got := BrowserStageFor(ModeWithVisual); got != toolpolicy.BrowserStageFallback {
		t.Errorf("ModeWithVisual → %q, want %q", got, toolpolicy.BrowserStageFallback)
	}
}

// mockExecutor 模拟 A/B 行为:
//   - hard 任务在 DOM-only 下 20% 成功;with_visual 下 90% 成功 → Δ≈+70%
//   - soft 任务在 DOM-only 下 50%;with_visual 下 80% → Δ≈+30%
//   - aux  任务两轮都 95%,visual 基本不触发
//
// 用 task.ID 做伪随机保证 deterministic,不引入 math/rand。
type mockExecutor struct{}

func (mockExecutor) Execute(_ context.Context, tk *VisualTask, mode RunMode) *TaskResult {
	r := &TaskResult{TaskID: tk.ID, Mode: mode, Turns: tk.MaxTurns / 2}
	// deterministic pseudo-hash:按 ID 字节和取模
	hash := 0
	for i := 0; i < len(tk.ID); i++ {
		hash += int(tk.ID[i])
	}
	roll := hash % 100
	switch tk.VisualRequired {
	case "hard":
		if mode == ModeWithVisual {
			r.Success = roll < 90
			r.VisualCalls = 2
			r.TokenCost = 2400
		} else {
			r.Success = roll < 20
			r.TokenCost = 800
		}
	case "soft":
		if mode == ModeWithVisual {
			r.Success = roll < 80
			if roll < 50 {
				r.VisualCalls = 1
			}
			r.TokenCost = 1400
		} else {
			r.Success = roll < 50
			r.TokenCost = 700
		}
	case "aux":
		r.Success = roll < 95
		r.TokenCost = 600
		if mode == ModeWithVisual {
			r.TokenCost = 650
			// 极少触发 visual
		}
	}
	if !r.Success {
		r.FailReason = "mock: roll " + tk.VisualRequired
	}
	return r
}

// TestRunABAndReport 跑 mock A/B,验证 report 流水线 + ROI 决策走到 loosen
// (因为 hard 任务带的 Δ 足够大)。
func TestRunABAndReport(t *testing.T) {
	tasks, err := LoadTasks("tasks")
	if err != nil {
		t.Fatalf("LoadTasks: %v", err)
	}
	resA, resB := RunAB(context.Background(), tasks, mockExecutor{})
	if len(resA) != len(tasks) || len(resB) != len(tasks) {
		t.Fatalf("AB length mismatch: A=%d B=%d tasks=%d", len(resA), len(resB), len(tasks))
	}

	sA := Summarize(ModeDOMOnly, resA)
	sB := Summarize(ModeWithVisual, resB)
	if sA.VisualTriggerRate != 0 {
		t.Errorf("A-side must have 0 visual triggers, got %.2f", sA.VisualTriggerRate)
	}
	if sB.VisualTriggerRate <= 0 {
		t.Errorf("B-side should have >0 visual triggers, got %.2f", sB.VisualTriggerRate)
	}

	cmp := CompareAB(sA, sB)
	if cmp.SuccessDelta <= 0 {
		t.Errorf("expected positive success delta, got %.3f", cmp.SuccessDelta)
	}
	if cmp.TokenInflation <= 1.0 {
		t.Errorf("expected token inflation > 1.0, got %.2f", cmp.TokenInflation)
	}

	dir := t.TempDir()
	report := filepath.Join(dir, "report.md")
	if err := WriteMarkdown(report, cmp, resA, resB); err != nil {
		t.Fatalf("WriteMarkdown: %v", err)
	}
	raw, err := os.ReadFile(report)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	body := string(raw)
	for _, needle := range []string{
		"Visual WebArena A/B 对比报告",
		"Run A",
		"Run B",
		"ROI 结论",
		"按 visual_required",
	} {
		if !strings.Contains(body, needle) {
			t.Errorf("report missing section %q", needle)
		}
	}
}

// TestDecideVisualROITightenBand 覆盖 tighten 决策分支:
//   Δ 小 + token 高 → tighten。
func TestDecideVisualROITightenBand(t *testing.T) {
	cmp := &ABComparison{
		A:              &Summary{Total: 10, Succeeded: 8, AvgTokens: 500},
		B:              &Summary{Total: 10, Succeeded: 8, AvgTokens: 1500},
		SuccessDelta:   0.0,
		TokenInflation: 3.0,
	}
	rec, rat := DecideVisualROI(cmp)
	if rec != "tighten" {
		t.Errorf("expected tighten, got %q (rationale=%q)", rec, rat)
	}
}

// TestDecideVisualROILoosenBand 覆盖 loosen:Δ ≥ 30%。
func TestDecideVisualROILoosenBand(t *testing.T) {
	cmp := &ABComparison{
		A:              &Summary{Total: 10, Succeeded: 3, AvgTokens: 500},
		B:              &Summary{Total: 10, Succeeded: 9, AvgTokens: 2000},
		SuccessDelta:   0.60,
		TokenInflation: 4.0,
	}
	rec, _ := DecideVisualROI(cmp)
	if rec != "loosen" {
		t.Errorf("expected loosen at Δ=60%%, got %q", rec)
	}
}

// TestDecideVisualROIKeepBand 覆盖 keep:Δ 在 [10%, 30%) 区间。
func TestDecideVisualROIKeepBand(t *testing.T) {
	cmp := &ABComparison{
		A:              &Summary{Total: 10, Succeeded: 5, AvgTokens: 500},
		B:              &Summary{Total: 10, Succeeded: 7, AvgTokens: 1500},
		SuccessDelta:   0.20,
		TokenInflation: 3.0,
	}
	rec, _ := DecideVisualROI(cmp)
	if rec != "keep" {
		t.Errorf("expected keep at Δ=20%%, got %q", rec)
	}
}

// TestDecideVisualROIKeepWhenNoTokenData:Δ 小但无 token 数据时不应
// 误判 tighten,应退回 keep + 理由解释。
func TestDecideVisualROIKeepWhenNoTokenData(t *testing.T) {
	cmp := &ABComparison{
		A:              &Summary{Total: 10, Succeeded: 5},
		B:              &Summary{Total: 10, Succeeded: 5},
		SuccessDelta:   0.0,
		TokenInflation: -1, // unknown
	}
	rec, rationale := DecideVisualROI(cmp)
	if rec != "keep" {
		t.Errorf("expected keep when token data missing, got %q", rec)
	}
	if !strings.Contains(rationale, "token cost unavailable") {
		t.Errorf("rationale should flag missing token data, got %q", rationale)
	}
}

// TestCompareABEdgeCases:空 / 任务数不等。
func TestCompareABEdgeCases(t *testing.T) {
	cmp := CompareAB(nil, nil)
	if cmp.Recommendation != "insufficient_data" {
		t.Errorf("nil summaries should give insufficient_data, got %q", cmp.Recommendation)
	}

	a := &Summary{Total: 10, Succeeded: 5, AvgTurns: 4, AvgTokens: 500}
	b := &Summary{Total: 8, Succeeded: 6, AvgTurns: 5, AvgTokens: 1200}
	cmp2 := CompareAB(a, b)
	if !strings.Contains(cmp2.Rationale, "mismatch") {
		t.Errorf("rationale should flag task count mismatch, got %q", cmp2.Rationale)
	}
}

// TestSummarizeStatsByDimension:visual_required / category 两个维度
// 分别都被正确聚合。
func TestSummarizeStatsByDimension(t *testing.T) {
	results := []*TaskResult{
		{TaskID: "a", Success: true, Turns: 3, Task: &VisualTask{Category: "canvas", VisualRequired: "hard"}},
		{TaskID: "b", Success: false, Turns: 5, Task: &VisualTask{Category: "canvas", VisualRequired: "hard"}},
		{TaskID: "c", Success: true, Turns: 2, Task: &VisualTask{Category: "map", VisualRequired: "soft"}},
	}
	s := Summarize(ModeDOMOnly, results)
	if s.Total != 3 || s.Succeeded != 2 {
		t.Errorf("totals: %d/%d", s.Succeeded, s.Total)
	}
	if s.ByCategory["canvas"].Total != 2 {
		t.Errorf("canvas total = %d, want 2", s.ByCategory["canvas"].Total)
	}
	if s.ByVisualRequired["hard"].Succeeded != 1 {
		t.Errorf("hard succeeded = %d, want 1", s.ByVisualRequired["hard"].Succeeded)
	}
	if math.Abs(s.AvgTurns-10.0/3) > 0.01 {
		t.Errorf("AvgTurns = %f, want ~3.33", s.AvgTurns)
	}
}

// TestRunABContextCancel:ctx 失效时提前返回已完成结果。
func TestRunABContextCancel(t *testing.T) {
	tasks, err := LoadTasks("tasks")
	if err != nil {
		t.Fatalf("LoadTasks: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立刻取消
	a, b := RunAB(ctx, tasks, mockExecutor{})
	if len(a) != 0 || len(b) != 0 {
		t.Errorf("cancelled context should produce empty results, got a=%d b=%d", len(a), len(b))
	}
}
