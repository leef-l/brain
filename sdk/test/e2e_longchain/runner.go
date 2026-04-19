// Package e2e_longchain is Browser Brain 的 **跨页长链路** 回归评测集。
//
// 见 sdk/docs/40-Browser-Brain语义理解架构.md §6.1(阶段 2 指标)。
//
// 与 sdk/test/webarena 的区别:webarena 每条任务是一段**单目标**(登录 /
// 搜索 / 单表单),这里每条任务是一条 **5-10 步跨页链路**:
//
//   登录 → 搜索 → 加购 → 结算 → 支付 → ...
//
// 我们想压测的是:
//
//   1. context 传递:turn 之间共享 credentials / cart_id 等状态。
//   2. 模式库跨 URL 迁移:P1.1 auth 登录命中后,P1.2 ecommerce 能否
//      在下一个 URL 继续接力(pattern_id 链路)。
//   3. 异常恢复:链路中途 session_expired → on_anomaly 路由到
//      fallback_pattern(切回登录再继续)。
//   4. token / turn 预算爆炸的临界点(阈值估计 100k / 10 turn)。
//   5. 所有失败 **必须分类到 sdk/errors.ErrorClass**,不得自造。
//
// Executor 由集成方注入(真 Browser Brain / mock),本包不依赖 sidecar 栈。
package e2e_longchain

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/leef-l/brain/sdk/errors"
)

// ChainStep 是长链路中的一步。每一步都绑定一个期望命中的 pattern:
//
//   - Category:P1 场景包分类(auth / commerce / admin / form / search / nav)
//   - PatternID:最希望命中的 seed/user pattern ID(用于"跨 URL 迁移"度量)
//   - ExpectAnomaly:这一步是否注入特定异常(session_expired / captcha 等),
//     配合 Executor 验证 on_anomaly 路由是否按预期 fallback。
type ChainStep struct {
	Name           string `json:"name"`
	URLHint        string `json:"url_hint,omitempty"`
	Category       string `json:"category"`
	PatternID      string `json:"pattern_id"`
	Goal           string `json:"goal"`
	ExpectAnomaly  string `json:"expect_anomaly,omitempty"`   // e.g. session_expired / captcha / error_message
	ExpectRecovery string `json:"expect_recovery,omitempty"`  // fallback_pattern / retry / human_intervention / abort
	MaxTurns       int    `json:"max_turns,omitempty"`        // 覆盖任务级默认,单步预算
}

// SuccessCheck 是一条任务的**全链路**成功检查(与 webarena 同构,避免重复造轮)。
type SuccessCheck struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

// TaskChain 是一条长链路任务。
type TaskChain struct {
	ID          string         `json:"id"`
	Description string         `json:"description"`
	Site        string         `json:"site"`
	MaxTurns    int             `json:"max_turns"`
	TokenBudget int             `json:"token_budget,omitempty"` // 推测临界(0 = 不限)
	Steps       []ChainStep    `json:"steps"`
	Success     []SuccessCheck `json:"success"`
	Tags        []string       `json:"tags,omitempty"`
}

// StepResult 是单步执行结果。Pass=true 表示该步里命中了期望 pattern 并完成。
type StepResult struct {
	Name        string            `json:"name"`
	Pass        bool              `json:"pass"`
	MatchedID   string            `json:"matched_pattern_id,omitempty"` // 真实命中的 pattern
	Turns       int               `json:"turns"`
	Anomaly     string            `json:"anomaly,omitempty"`
	RecoveryOK  bool              `json:"recovery_ok,omitempty"` // ExpectRecovery 对 fallback 路径的验证
	FailClass   errors.ErrorClass `json:"fail_class,omitempty"`  // sdk/errors.ErrorClass,不得自造
	FailReason  string            `json:"fail_reason,omitempty"`
}

// ChainResult 是一条长链路任务的聚合结果。
type ChainResult struct {
	Task            *TaskChain        `json:"-"`
	TaskID          string            `json:"task_id"`
	Success         bool              `json:"success"`           // 所有 Success 检查全过
	StepsPassed     int               `json:"steps_passed"`
	TotalSteps      int               `json:"total_steps"`
	Turns           int               `json:"turns"`
	TokensUsed      int               `json:"tokens_used,omitempty"`
	Duration        time.Duration     `json:"duration"`
	PatternSwitches int               `json:"pattern_switches"` // 跨 category 切换次数(度量迁移能力)
	StepResults     []StepResult      `json:"steps"`
	FailClass       errors.ErrorClass `json:"fail_class,omitempty"`
	FailReason      string            `json:"fail_reason,omitempty"`
}

// Executor 执行一条长链路任务。实现方负责:
//
//   1. 起会话到 task.Site。
//   2. 把 task.Steps 按顺序喂给主循环,每一步期望命中 PatternID。
//   3. 遇到 ExpectAnomaly:通过 Browser Brain 的 on_anomaly 路由验证恢复。
//   4. 最后对 Success 做验证,填 ChainResult。
//   5. 任何失败分类到 sdk/errors.ErrorClass(不是自由字符串)。
type Executor interface {
	Execute(ctx context.Context, task *TaskChain) *ChainResult
}

// LoadTasks 从目录读所有 *.json 任务定义并做基本合法性校验。
func LoadTasks(dir string) ([]*TaskChain, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read dir: %w", err)
	}
	var tasks []*TaskChain
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", e.Name(), err)
		}
		var t TaskChain
		if err := json.Unmarshal(raw, &t); err != nil {
			return nil, fmt.Errorf("parse %s: %w", e.Name(), err)
		}
		if err := validateChain(&t); err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		tasks = append(tasks, &t)
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].ID < tasks[j].ID })
	return tasks, nil
}

func validateChain(t *TaskChain) error {
	if t.ID == "" {
		return fmt.Errorf("missing id")
	}
	if t.MaxTurns <= 0 {
		return fmt.Errorf("max_turns must be > 0")
	}
	if len(t.Steps) < 2 {
		return fmt.Errorf("a longchain task needs at least 2 steps, got %d", len(t.Steps))
	}
	if len(t.Success) == 0 {
		return fmt.Errorf("at least one success check required")
	}
	for i, s := range t.Steps {
		if s.Category == "" || s.PatternID == "" {
			return fmt.Errorf("step %d (%s): category and pattern_id required", i, s.Name)
		}
	}
	return nil
}

// Run 跑一批长链路任务,ctx 失效时中止。结果按输入顺序返回。
func Run(ctx context.Context, tasks []*TaskChain, exec Executor) []*ChainResult {
	out := make([]*ChainResult, 0, len(tasks))
	for _, t := range tasks {
		select {
		case <-ctx.Done():
			return out
		default:
		}
		start := time.Now()
		res := exec.Execute(ctx, t)
		if res == nil {
			res = &ChainResult{
				TaskID:     t.ID,
				Success:    false,
				FailClass:  errors.ClassInternalBug,
				FailReason: "executor returned nil",
			}
		}
		res.Task = t
		res.TaskID = t.ID
		res.TotalSteps = len(t.Steps)
		if res.Duration == 0 {
			res.Duration = time.Since(start)
		}
		// 补算 PatternSwitches:按 step.Category 相邻变化计一次切换。
		if res.PatternSwitches == 0 {
			prev := ""
			for _, s := range t.Steps {
				if prev != "" && s.Category != prev {
					res.PatternSwitches++
				}
				prev = s.Category
			}
		}
		out = append(out, res)
	}
	return out
}

// classifyOrDefault 在 Executor 没给 FailClass 时,给个兜底分类。避免
// 报告里出现空串 —— 空串会让后续按 class 聚合看起来"不知道"而不是
// "内部 bug"。
func classifyOrDefault(c errors.ErrorClass) errors.ErrorClass {
	if c == "" {
		return errors.ClassInternalBug
	}
	return c
}
