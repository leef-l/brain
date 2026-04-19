// Package mind2web 是 Browser Brain 的"跨站泛化"回归基准。
//
// 设计对齐 sdk/test/webarena/:同样的 Task / SuccessCheck / Executor 接口,
// 同样的 tasks/*.json 加载机制,同样的 Summary + Markdown 报告流水线。
// 字段 100% 对齐,避免 schema 分家。
//
// 与 WebArena 的差别在"看什么":
//   - WebArena 关注"单站能不能做对一条任务"
//   - Mind2Web 关注"在 N 个不同站点做同类任务,模式库能复用多少次"
//
// 因此本包额外聚合两个指标(report.go 里实现):
//   - 按 category×site 的成功分布矩阵(跨站命中率)
//   - 模式复用次数 / 任务数(PatternReused / Total)
//
// Executor 由集成方注入(和 webarena 一样),不直接起浏览器。
//
// 数据来源见 README.md:Mind2Web public subset 的 100 条挑选。
package mind2web

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// SuccessCheck 与 webarena.SuccessCheck 完全对齐。支持的 kind:
//   - url_contains / url_matches / dom_has / text_contains
//
// Mind2Web 原始数据集里的 "element" 级 oracle 被我们压缩成上面几种通用
// kind —— 代价是部分 action-level 细节丢失,但好处是 Executor 能复用
// webarena 里验证过的检查器,不必再写一套。
type SuccessCheck struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

// Task 与 webarena.Task 字段一一对应。Mind2Web 特有元数据放在 Metadata
// 里,避免污染公共 schema(webarena 的 JSON 加上 metadata 也解析通过)。
//
// Tags 约定:每条任务必须带:
//   - "mind2web" — 基准来源标记
//   - 场景标签(login / ecommerce / crud / misc) —— 用于跨基准聚合
//   - 可选 "cross_site" / "transfer" — 标记是否是跨站迁移验证
type Task struct {
	ID       string            `json:"id"`
	Category string            `json:"category"` // auth / ecommerce / admin / misc
	Site     string            `json:"site"`
	Goal     string            `json:"goal"`
	MaxTurns int               `json:"max_turns"`
	Success  []SuccessCheck    `json:"success"`
	Tags     []string          `json:"tags,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"` // annotation_id / domain / subdomain 等
}

// TaskResult 与 webarena.TaskResult 对齐。PatternID / PatternReused 是
// Mind2Web 特有的"模式复用"追踪位 —— Executor 可填,Summarize 按它们
// 聚合跨站命中率。
type TaskResult struct {
	Task       *Task         `json:"-"`
	TaskID     string        `json:"task_id"`
	Success    bool          `json:"success"`
	Turns      int           `json:"turns"`
	Duration   time.Duration `json:"duration"`
	FailReason string        `json:"fail_reason,omitempty"`
	// PatternID 是命中并执行成功的 UIPattern id(如 login_username_password
	// / ecommerce_add_to_cart_with_feedback),由 Executor 从 ExecutionResult
	// 摘出来。空串表示未走 pattern 路径。
	PatternID string `json:"pattern_id,omitempty"`
	// PatternReused=true 表示本次任务用到了上面 PatternID,且该 pattern
	// 在本轮 run 中不是第一次命中 —— 这是"跨站复用"的信号。
	PatternReused bool `json:"pattern_reused,omitempty"`
}

// Executor 执行一条任务。与 webarena.Executor 形同签名,这样集成方
// 一套 Browser Brain 适配器能同时喂 WebArena 和 Mind2Web。
type Executor interface {
	Execute(ctx context.Context, task *Task) *TaskResult
}

// LoadTasks 从目录读所有 *.json 任务定义。
// 会校验:id 非空、goal 非空、max_turns>0、success 非空。
func LoadTasks(dir string) ([]*Task, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read dir: %w", err)
	}
	var tasks []*Task
	seen := map[string]bool{}
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
		if err := validateTask(&t, e.Name()); err != nil {
			return nil, err
		}
		if seen[t.ID] {
			return nil, fmt.Errorf("duplicate task id %q in %s", t.ID, e.Name())
		}
		seen[t.ID] = true
		tasks = append(tasks, &t)
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].ID < tasks[j].ID })
	return tasks, nil
}

// validateTask 集中校验任务文件字段,避免批量 100 条里漏一条被 CI 放过。
func validateTask(t *Task, fname string) error {
	if t.ID == "" {
		return fmt.Errorf("%s: missing id", fname)
	}
	if t.Goal == "" {
		return fmt.Errorf("%s (%s): missing goal", fname, t.ID)
	}
	if t.MaxTurns <= 0 {
		return fmt.Errorf("%s (%s): max_turns must be > 0", fname, t.ID)
	}
	if len(t.Success) == 0 {
		return fmt.Errorf("%s (%s): at least one success check required", fname, t.ID)
	}
	switch t.Category {
	case "auth", "ecommerce", "admin", "misc":
	default:
		return fmt.Errorf("%s (%s): category %q not in {auth,ecommerce,admin,misc}", fname, t.ID, t.Category)
	}
	for i, sc := range t.Success {
		if sc.Kind == "" {
			return fmt.Errorf("%s (%s): success[%d].kind empty", fname, t.ID, i)
		}
		if sc.Value == "" {
			return fmt.Errorf("%s (%s): success[%d].value empty", fname, t.ID, i)
		}
	}
	return nil
}

// Run 跑所有任务。ctx 失效时中止。
func Run(ctx context.Context, tasks []*Task, exec Executor) []*TaskResult {
	out := make([]*TaskResult, 0, len(tasks))
	patternFirstSeen := map[string]bool{}
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
		// 由 runner 计算 PatternReused:同一个 pattern_id 第二次出现即算
		// "跨站复用"(Executor 也可以自己填,runner 会尊重它的值)。
		if res.PatternID != "" && !res.PatternReused {
			if patternFirstSeen[res.PatternID] {
				res.PatternReused = true
			} else {
				patternFirstSeen[res.PatternID] = true
			}
		} else if res.PatternID != "" {
			patternFirstSeen[res.PatternID] = true
		}
		out = append(out, res)
	}
	return out
}
