// Package webarena 是 Browser Brain 的轻量回归基准。
//
// 见 sdk/test/webarena/README.md 与 sdk/docs/40-Browser-Brain语义理解架构.md §6.2。
//
// runner 只负责:
//  1. 加载 tasks/*.json
//  2. 准备每条任务的执行上下文(goal + success checks)
//  3. 交给调用方提供的 Executor 去跑(Executor 接入 Browser Brain 主循环)
//  4. 汇总成功率/turn 数/类别分布到 report.md
//
// Executor 由集成方注入,本包不直接起浏览器会话,避免测试目录依赖整个
// sidecar 栈——这样 webarena 既能在 CI 里用 mock Executor 做 smoke,也能在
// 开发机里换成真 Browser Brain 跑。
package webarena

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// SuccessCheck 是单条任务的成功条件。kind 取值见 README。
type SuccessCheck struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

// Task 是一条 WebArena 任务。
type Task struct {
	ID       string         `json:"id"`
	Category string         `json:"category"`
	Site     string         `json:"site"`
	Goal     string         `json:"goal"`
	MaxTurns int            `json:"max_turns"`
	Success  []SuccessCheck `json:"success"`
	Tags     []string       `json:"tags,omitempty"`
}

// TaskResult 是一条任务的执行结果。
type TaskResult struct {
	Task       *Task         `json:"-"`
	TaskID     string        `json:"task_id"`
	Success    bool          `json:"success"`
	Turns      int           `json:"turns"`
	Duration   time.Duration `json:"duration"`
	FailReason string        `json:"fail_reason,omitempty"`
}

// Executor 执行一条任务。实现方负责:
//   - 起浏览器会话到 task.Site
//   - 把 task.Goal 作为 instruction 喂给 Browser Brain
//   - 跑完 loop 后对每条 SuccessCheck 做验证
//   - 返回 TaskResult
type Executor interface {
	Execute(ctx context.Context, task *Task) *TaskResult
}

// LoadTasks 从目录读所有 *.json 任务定义。
func LoadTasks(dir string) ([]*Task, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read dir: %w", err)
	}
	var tasks []*Task
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", e.Name(), err)
		}
		var t Task
		if err := json.Unmarshal(raw, &t); err != nil {
			return nil, fmt.Errorf("parse %s: %w", e.Name(), err)
		}
		if t.ID == "" {
			return nil, fmt.Errorf("%s: missing id", e.Name())
		}
		tasks = append(tasks, &t)
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].ID < tasks[j].ID })
	return tasks, nil
}

// Run 跑所有任务,返回结果切片。ctx 失效时中止。
func Run(ctx context.Context, tasks []*Task, exec Executor) []*TaskResult {
	out := make([]*TaskResult, 0, len(tasks))
	for _, t := range tasks {
		select {
		case <-ctx.Done():
			return out
		default:
		}
		start := time.Now()
		res := exec.Execute(ctx, t)
		if res == nil {
			res = &TaskResult{TaskID: t.ID, Success: false, FailReason: "executor returned nil"}
		}
		res.Task = t
		res.TaskID = t.ID
		if res.Duration == 0 {
			res.Duration = time.Since(start)
		}
		out = append(out, res)
	}
	return out
}
