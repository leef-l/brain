// Package visualwebarena 是 Browser Brain 的视觉依赖场景基准。
//
// 见 sdk/test/visualwebarena/README.md 与 sdk/docs/40-Browser-Brain语义理解架构.md §5.1。
//
// 本包专注回答一个问题:**什么时候值得调 browser.visual_inspect?**
// 方法是 A/B 对比:同一批 30 条视觉依赖任务,分别在
//   - Run A(BrowserStage=known_flow,无 visual_inspect)
//   - Run B(BrowserStage=fallback,  visual_inspect 在工具集)
// 下跑一次,聚合成功率/turn 数/token 差值,给出收紧/保持/放宽建议。
//
// 基础类型复用 webarena:Task/TaskResult/Summarize/WriteMarkdown 不重造,
// 这里只扩展 A/B 双轮所需的 Task 超集字段 和 AB 对比聚合器。
package visualwebarena

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/leef-l/brain/sdk/toolpolicy"
)

// VisualTask 是 visualwebarena 的任务定义。在 webarena.Task 的字段之上
// 加了三个视觉专用字段:
//   - VisualRequired:  hard / soft / aux(见 README)
//   - Modality:        canvas / image_search / map / richtext / visual_state
//   - ExpectedVisualHit: 预期 B 轮是否真的用到 visual_inspect(写在任务里
//                        便于验证触发率是否合理 — hard 任务里应该 100% 命中)
//
// SuccessCheck / Success / MaxTurns / Tags 直接内嵌,保持 JSON 兼容
// webarena 的既有任务(未知字段忽略)。
type VisualTask struct {
	ID               string          `json:"id"`
	Category         string          `json:"category"`
	Site             string          `json:"site"`
	Goal             string          `json:"goal"`
	MaxTurns         int             `json:"max_turns"`
	Success          []SuccessCheck  `json:"success"`
	Tags             []string        `json:"tags,omitempty"`
	VisualRequired   string          `json:"visual_required"`            // hard / soft / aux
	Modality         []string        `json:"modality,omitempty"`
	ExpectedVisualHit bool           `json:"expected_visual_hit,omitempty"`
}

// SuccessCheck 语义和 webarena.SuccessCheck 一致 —— 这里重新声明一份
// 而不 import,避免把 sdk/test/webarena 拉成强依赖(两个基准要可独立
// 演进)。runner_test 的兼容性通过"字段布局一致 + json tag 一致"保证。
type SuccessCheck struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

// RunMode 声明 A/B 两轮的身份,Executor 据此决定是否允许 visual_inspect。
type RunMode string

const (
	// ModeDOMOnly — Run A:不开放 visual_inspect,模拟"纯 snapshot"工作流。
	// 对应 BrowserStage=known_flow。
	ModeDOMOnly RunMode = "dom_only"

	// ModeWithVisual — Run B:visual_inspect 在工具集,允许按需触发。
	// 对应 BrowserStage=fallback。
	ModeWithVisual RunMode = "with_visual"
)

// BrowserStageFor 返回每个 RunMode 应该交给 AdaptiveToolPolicy 的
// BrowserStage 字段值。这是"走已有 executionpolicy/toolpolicy 组合"
// 的接入点 —— 不引入新的 feature flag。
func BrowserStageFor(mode RunMode) string {
	switch mode {
	case ModeDOMOnly:
		return toolpolicy.BrowserStageKnownFlow
	case ModeWithVisual:
		return toolpolicy.BrowserStageFallback
	default:
		return ""
	}
}

// TaskResult 是单轮单任务的执行结果,同样自包一份(兼容 webarena 格式)。
// 额外字段:
//   - VisualCalls: B 轮里 visual_inspect 被调用的次数(Executor 填)
//   - TokenCost:   本轮消耗 token(Executor 填,mock 允许 0)
type TaskResult struct {
	Task        *VisualTask   `json:"-"`
	TaskID      string        `json:"task_id"`
	Mode        RunMode       `json:"mode"`
	Success     bool          `json:"success"`
	Turns       int           `json:"turns"`
	Duration    time.Duration `json:"duration"`
	FailReason  string        `json:"fail_reason,omitempty"`
	VisualCalls int           `json:"visual_calls,omitempty"`
	TokenCost   int           `json:"token_cost,omitempty"`
}

// Executor 负责执行一条任务。A/B 通过 mode 区分:
//   - mode = ModeDOMOnly     → 禁用 visual_inspect(toolpolicy 已约束,
//                              Executor 再做一次兜底检查防误用)
//   - mode = ModeWithVisual  → visual_inspect 在工具集,由 LLM 自行
//                              决定是否触发
type Executor interface {
	Execute(ctx context.Context, task *VisualTask, mode RunMode) *TaskResult
}

// LoadTasks 从目录读 *.json 任务。文件顺序按 ID 排序,保证报告稳定。
func LoadTasks(dir string) ([]*VisualTask, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read dir: %w", err)
	}
	var tasks []*VisualTask
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", e.Name(), err)
		}
		var t VisualTask
		if err := json.Unmarshal(raw, &t); err != nil {
			return nil, fmt.Errorf("parse %s: %w", e.Name(), err)
		}
		if t.ID == "" {
			return nil, fmt.Errorf("%s: missing id", e.Name())
		}
		if t.MaxTurns <= 0 {
			return nil, fmt.Errorf("%s: max_turns must be > 0", t.ID)
		}
		if len(t.Success) == 0 {
			return nil, fmt.Errorf("%s: at least one success check required", t.ID)
		}
		if !validVisualRequired(t.VisualRequired) {
			return nil, fmt.Errorf("%s: visual_required must be hard|soft|aux, got %q",
				t.ID, t.VisualRequired)
		}
		tasks = append(tasks, &t)
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].ID < tasks[j].ID })
	return tasks, nil
}

func validVisualRequired(v string) bool {
	switch v {
	case "hard", "soft", "aux":
		return true
	}
	return false
}

// RunAB 执行 A/B 双轮。对每条任务先跑 DOM-only 再跑 with_visual,
// 以减小"缓存预热"带来的偏差(A 的结果不该因为 B 先跑过而被污染)。
// ctx 失效时提前返回已跑完的部分。
func RunAB(ctx context.Context, tasks []*VisualTask, exec Executor) (a, b []*TaskResult) {
	a = make([]*TaskResult, 0, len(tasks))
	b = make([]*TaskResult, 0, len(tasks))
	for _, t := range tasks {
		if err := ctx.Err(); err != nil {
			return a, b
		}
		a = append(a, runOne(ctx, t, ModeDOMOnly, exec))
		if err := ctx.Err(); err != nil {
			return a, b
		}
		b = append(b, runOne(ctx, t, ModeWithVisual, exec))
	}
	return a, b
}

func runOne(ctx context.Context, t *VisualTask, mode RunMode, exec Executor) *TaskResult {
	start := time.Now()
	res := exec.Execute(ctx, t, mode)
	if res == nil {
		res = &TaskResult{TaskID: t.ID, Mode: mode, FailReason: "executor returned nil"}
	}
	res.Task = t
	res.TaskID = t.ID
	res.Mode = mode
	if res.Duration == 0 {
		res.Duration = time.Since(start)
	}
	return res
}
