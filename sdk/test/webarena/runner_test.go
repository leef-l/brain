package webarena

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadTasks 保证 tasks 目录下的 JSON 都能解析,CI 默认就能跑。
func TestLoadTasks(t *testing.T) {
	tasks, err := LoadTasks("tasks")
	if err != nil {
		t.Fatalf("LoadTasks: %v", err)
	}
	if len(tasks) == 0 {
		t.Fatal("expected at least one task")
	}
	for _, task := range tasks {
		if task.ID == "" || task.Goal == "" {
			t.Fatalf("task missing fields: %+v", task)
		}
		if task.MaxTurns <= 0 {
			t.Fatalf("%s: max_turns must be > 0", task.ID)
		}
		if len(task.Success) == 0 {
			t.Fatalf("%s: at least one success check required", task.ID)
		}
	}
}

// mockExecutor 用于 CI:不起浏览器,按 tag 直接标成功/失败。
type mockExecutor struct{}

func (mockExecutor) Execute(_ context.Context, task *Task) *TaskResult {
	r := &TaskResult{TaskID: task.ID, Turns: task.MaxTurns / 2}
	for _, tag := range task.Tags {
		if tag == "happy_path" {
			r.Success = true
			return r
		}
	}
	r.FailReason = "mock executor: not a happy_path tag"
	return r
}

// TestRunAndReport 跑 mock 执行,验证 summary/report 流水线。
func TestRunAndReport(t *testing.T) {
	tasks, err := LoadTasks("tasks")
	if err != nil {
		t.Fatalf("LoadTasks: %v", err)
	}

	results := Run(context.Background(), tasks, mockExecutor{})
	if len(results) != len(tasks) {
		t.Fatalf("result count mismatch: got %d, want %d", len(results), len(tasks))
	}
	s := Summarize(results)
	if s.Total != len(tasks) {
		t.Fatalf("summary total mismatch")
	}

	// 写报告到临时目录
	dir := t.TempDir()
	report := filepath.Join(dir, "report.md")
	if err := WriteMarkdown(report, results, s); err != nil {
		t.Fatalf("WriteMarkdown: %v", err)
	}
	raw, err := os.ReadFile(report)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	if !strings.Contains(string(raw), "WebArena 回归报告") {
		t.Fatal("report missing header")
	}
}
