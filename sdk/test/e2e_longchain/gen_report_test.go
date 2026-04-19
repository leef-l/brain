//go:build e2e_longchain_report

package e2e_longchain

import (
	"context"
	"path/filepath"
	"testing"
)

// TestGenerateBaselineReport 用 build tag 隔离,不会在默认 go test ./... 时跑。
// 想刷新 baseline report.md 执行:
//
//	go test -tags e2e_longchain_report ./sdk/test/e2e_longchain/ -run TestGenerateBaselineReport
func TestGenerateBaselineReport(t *testing.T) {
	tasks, err := LoadTasks("tasks")
	if err != nil {
		t.Fatalf("LoadTasks: %v", err)
	}
	results := Run(context.Background(), tasks, mockExecutor{})
	s := Summarize(results)
	out := filepath.Join(".", "report.md")
	if err := WriteMarkdown(out, results, s); err != nil {
		t.Fatalf("WriteMarkdown: %v", err)
	}
}
