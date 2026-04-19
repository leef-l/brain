package e2e_longchain

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leef-l/brain/sdk/errors"
)

// TestLoadTasks 保证 tasks/*.json 结构合法。CI 默认就能跑。
func TestLoadTasks(t *testing.T) {
	tasks, err := LoadTasks("tasks")
	if err != nil {
		t.Fatalf("LoadTasks: %v", err)
	}
	if len(tasks) < 10 {
		t.Fatalf("expected >= 10 longchain tasks, got %d", len(tasks))
	}
	ids := map[string]bool{}
	for _, task := range tasks {
		if ids[task.ID] {
			t.Fatalf("duplicate task id: %s", task.ID)
		}
		ids[task.ID] = true
		if len(task.Steps) < 2 {
			t.Fatalf("%s: long-chain must have >= 2 steps, got %d", task.ID, len(task.Steps))
		}
		for i, s := range task.Steps {
			if s.Category == "" || s.PatternID == "" {
				t.Fatalf("%s: step %d missing category/pattern_id", task.ID, i)
			}
		}
	}
}

// mockExecutor 给 CI 用。不起浏览器;按 tag 决定成功/失败,并把失败分类到
// 真正的 sdk/errors.ErrorClass。
type mockExecutor struct{}

func (mockExecutor) Execute(_ context.Context, task *TaskChain) *ChainResult {
	res := &ChainResult{
		TaskID:      task.ID,
		TotalSteps:  len(task.Steps),
		Turns:       task.MaxTurns / 2,
		TokensUsed:  task.TokenBudget / 3,
		StepResults: make([]StepResult, 0, len(task.Steps)),
	}
	happy := hasTag(task.Tags, "phase2")
	for _, s := range task.Steps {
		sr := StepResult{Name: s.Name, Turns: 1}
		if s.ExpectAnomaly != "" {
			sr.Anomaly = s.ExpectAnomaly
			sr.RecoveryOK = happy
		}
		if happy {
			sr.Pass = true
			sr.MatchedID = s.PatternID
			res.StepsPassed++
		} else {
			sr.FailClass = errors.ClassPermanent
			sr.FailReason = "mock: not a phase2 task"
		}
		res.StepResults = append(res.StepResults, sr)
	}
	// 算模式切换
	prev := ""
	for _, s := range task.Steps {
		if prev != "" && s.Category != prev {
			res.PatternSwitches++
		}
		prev = s.Category
	}
	if happy {
		res.Success = true
	} else {
		res.FailClass = errors.ClassPermanent
		res.FailReason = "mock executor rejected non-phase2 task"
	}
	return res
}

func hasTag(tags []string, want string) bool {
	for _, t := range tags {
		if t == want {
			return true
		}
	}
	return false
}

func TestRunAndReport(t *testing.T) {
	tasks, err := LoadTasks("tasks")
	if err != nil {
		t.Fatalf("LoadTasks: %v", err)
	}
	results := Run(context.Background(), tasks, mockExecutor{})
	if len(results) != len(tasks) {
		t.Fatalf("result count mismatch: got %d want %d", len(results), len(tasks))
	}
	s := Summarize(results)
	if s.Total != len(tasks) {
		t.Fatalf("summary total mismatch")
	}
	// 所有任务都带 phase2 tag,mock 应给 100% 成功。这同时验证 Summarize
	// 在 AnomalyRecoveryTotal > 0 时不会零除(任务 06/08/10 都带 expect_anomaly)。
	if s.Succeeded != s.Total {
		t.Errorf("all phase2 tasks should succeed in mock, got %d/%d", s.Succeeded, s.Total)
	}

	dir := t.TempDir()
	report := filepath.Join(dir, "report.md")
	if err := WriteMarkdown(report, results, s); err != nil {
		t.Fatalf("WriteMarkdown: %v", err)
	}
	raw, err := os.ReadFile(report)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	content := string(raw)
	if !strings.Contains(content, "E2E 长链路评测报告") {
		t.Fatal("report missing header")
	}
	if !strings.Contains(content, "单步命中") {
		t.Fatal("report missing per-step detail section")
	}
}

// 保证 runner 拒绝明显非法的链路定义。validateChain 是契约。
func TestValidateChainRejectsSingleStep(t *testing.T) {
	if err := validateChain(&TaskChain{
		ID: "x", MaxTurns: 5,
		Steps:   []ChainStep{{Name: "only", Category: "auth", PatternID: "x"}},
		Success: []SuccessCheck{{Kind: "url_contains", Value: "/"}},
	}); err == nil {
		t.Fatal("expected error for single-step chain")
	}
}
