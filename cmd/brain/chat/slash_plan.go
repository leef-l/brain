package chat

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/leef-l/brain/sdk/agent"
	"github.com/leef-l/brain/sdk/kernel"
)

// chatPlanRegistry 持有当前 chat 会话中创建过的 PlanOrchestrator 和相关状态。
// 每个 State 对应一个实例，在 handlePlanCreate 时懒初始化。
type chatPlanRegistry struct {
	mu        sync.RWMutex
	planOrch  *kernel.PlanOrchestrator
	progStore kernel.ProgressStore
	parser    kernel.RequirementParser
	designer  kernel.DesignGenerator
	memory    kernel.ProjectMemory

	// project_id → plan 缓存，用于 list / status 展示
	plans map[string]*kernel.TaskPlan
}

// globalPlanReg 以 *State 为 key 存各会话的 chatPlanRegistry。
// 用 sync.Map 避免 import 循环（State 不能持有 chatPlanRegistry 字段是为了减少依赖）。
var globalPlanReg sync.Map // map[*State]*chatPlanRegistry

// RemovePlanRegistry 在 State 关闭时清除对应的 chatPlanRegistry，防止内存泄漏。
// 由 State.Close 调用。
func RemovePlanRegistry(s *State) {
	globalPlanReg.Delete(s)
}

// getPlanRegistry 获取（或懒创建）与 state 绑定的 chatPlanRegistry。
func getPlanRegistry(state *State) *chatPlanRegistry {
	if v, ok := globalPlanReg.Load(state); ok {
		return v.(*chatPlanRegistry)
	}
	reg := &chatPlanRegistry{
		plans: make(map[string]*kernel.TaskPlan),
	}
	globalPlanReg.Store(state, reg)
	return reg
}

// ensurePlanOrch 确保 planOrch 已初始化（依赖 state.Orchestrator）。
// 返回 false 表示 Orchestrator 不可用，已向终端打印错误。
func (r *chatPlanRegistry) ensurePlanOrch(state *State) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.planOrch != nil {
		return true
	}
	if state.Orchestrator == nil {
		fmt.Println("  \033[1;31m! No orchestrator available (solo mode)\033[0m")
		fmt.Println()
		return false
	}

	r.memory = kernel.NewMemProjectMemory()
	r.progStore = kernel.NewMemoryProgressStore()
	r.parser = kernel.NewDefaultRequirementParser()
	r.designer = kernel.NewDefaultDesignGenerator()

	var learner *kernel.LearningEngine
	if state.Orchestrator != nil {
		learner = state.Orchestrator.Learner()
	}

	r.planOrch = kernel.NewPlanOrchestrator(state.Orchestrator, kernel.PlanOrchestratorConfig{
		Memory:        r.memory,
		Learner:       learner,
		TotalBudget:   200,
		ProgressStore: r.progStore,
	})
	return true
}

// buildPlan 把自然语言 prompt 转成 TaskPlan（复用 cmd_serve_plans.go 同名逻辑）。
func (r *chatPlanRegistry) buildPlan(ctx context.Context, projectID, goal string) (*kernel.TaskPlan, error) {
	if strings.TrimSpace(goal) == "" {
		return nil, fmt.Errorf("prompt 不能为空")
	}

	spec, err := r.parser.Parse(ctx, goal)
	if err != nil || spec == nil {
		return r.fallbackPlan(projectID, goal), nil
	}

	proposal, err := r.designer.Generate(ctx, spec)
	if err != nil || proposal == nil {
		return r.fallbackPlan(projectID, goal), nil
	}

	plan := r.designer.ToTaskPlan(proposal)
	if plan == nil || len(plan.SubTasks) == 0 {
		return r.fallbackPlan(projectID, goal), nil
	}

	if projectID != "" {
		plan.ProjectID = projectID
	}
	return plan, nil
}

// fallbackPlan 兜底：单 central 子任务。
func (r *chatPlanRegistry) fallbackPlan(projectID, goal string) *kernel.TaskPlan {
	if projectID == "" {
		projectID = fmt.Sprintf("proj-%d", time.Now().UnixNano())
	}
	plan := kernel.NewTaskPlan(projectID, goal)
	plan.AddSubTask(kernel.PlanSubTask{
		TaskID:         "task-1",
		Name:           "process_goal",
		Kind:           agent.KindCentral,
		Instruction:    goal,
		EstimatedTurns: 5,
		RetryPolicy:    kernel.RetryPolicy{MaxRetries: 1},
	})
	_ = plan.ComputeParallelLayers()
	return plan
}

// rememberPlan 缓存 plan，用于 list / status。
func (r *chatPlanRegistry) rememberPlan(plan *kernel.TaskPlan) {
	if plan == nil {
		return
	}
	r.mu.Lock()
	r.plans[plan.ProjectID] = plan
	r.mu.Unlock()
}

// ---------------------------------------------------------------------------
// 命令处理器（由 slash.go 调用）
// ---------------------------------------------------------------------------

// handlePlanCreate 处理 /plan <prompt>：构建 TaskPlan 并执行。
func handlePlanCreate(state *State, prompt string) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		fmt.Println("  用法: /plan <prompt>")
		fmt.Println("        /plan list")
		fmt.Println("        /plan status <project_id>")
		fmt.Println()
		return
	}

	reg := getPlanRegistry(state)
	if !reg.ensurePlanOrch(state) {
		return
	}

	projectID := fmt.Sprintf("proj-%d", time.Now().UnixNano())
	ctx := context.Background()

	fmt.Printf("  \033[2m解析需求: %s\033[0m\n", planTruncate(prompt, 60))

	plan, err := reg.buildPlan(ctx, projectID, prompt)
	if err != nil {
		fmt.Printf("  \033[1;31m! 计划构建失败: %v\033[0m\n\n", err)
		return
	}
	reg.rememberPlan(plan)

	// 打印计划摘要
	fmt.Printf("  \033[1mPlan ID:\033[0m  %s\n", plan.PlanID)
	fmt.Printf("  \033[1mProject:\033[0m  %s\n", plan.ProjectID)
	fmt.Printf("  \033[1m复杂度:\033[0m   %s  (预算 %d turns)\n", complexityLabelChat(plan), plan.Budget.TotalTurns)
	fmt.Printf("  \033[1m子任务:\033[0m   %d 个\n", len(plan.SubTasks))
	for i, t := range plan.SubTasks {
		fmt.Printf("    %d. [%-8s] %s  \033[2m(~%d turns)\033[0m\n",
			i+1, string(t.Kind), t.Name, t.EstimatedTurns)
	}
	fmt.Println()
	fmt.Println("  \033[2m正在执行计划...\033[0m")

	// 执行计划，使用独立 ctx 避免 REPL stdin 干扰
	execCtx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	result, runErr := reg.planOrch.ExecuteProject(execCtx, plan)

	// 打印执行结果
	fmt.Printf("\n  \033[1m执行结果\033[0m  project=%s\n", plan.ProjectID)
	if result != nil {
		phase := string(result.Progress.Phase)
		pct := result.Progress.OverallPercent
		icon := "\033[32m✓\033[0m"
		if runErr != nil || result.ExecError != nil {
			icon = "\033[1;31m✗\033[0m"
		}
		fmt.Printf("  %s 阶段: %-12s  完成度: %.0f%%  耗时: %s\n",
			icon, phase, pct, result.Duration.Round(time.Millisecond))

		if result.PlanResult != nil {
			pr := result.PlanResult
			fmt.Printf("     任务: 完成 %d / 失败 %d / 总计 %d\n",
				pr.CompletedTasks, pr.FailedTasks, pr.TotalTasks)
		}
		if result.Reflection != nil && len(result.Reflection.Lessons) > 0 {
			fmt.Printf("     反思: %d 条经验教训\n", len(result.Reflection.Lessons))
		}
		if runErr == nil && result.ExecError != nil {
			runErr = result.ExecError
		}
	}
	if runErr != nil {
		fmt.Printf("  \033[1;31m! 执行错误: %v\033[0m\n", runErr)
	}
	fmt.Printf("  \033[2m查看进度: /plan status %s\033[0m\n\n", plan.ProjectID)
}

// handlePlanList 处理 /plan list：列出会话内已创建的计划。
func handlePlanList(state *State) {
	reg := getPlanRegistry(state)
	reg.mu.RLock()
	defer reg.mu.RUnlock()

	if len(reg.plans) == 0 {
		fmt.Println("  当前会话中还没有创建任何计划。")
		fmt.Println("  使用 /plan <prompt> 创建一个新计划。")
		fmt.Println()
		return
	}

	fmt.Printf("  \033[1m当前会话计划列表\033[0m (%d 个)\n", len(reg.plans))
	for _, plan := range reg.plans {
		fmt.Printf("    %-34s  复杂度: %-6s  任务数: %d\n",
			plan.ProjectID, complexityLabelChat(plan), len(plan.SubTasks))
	}
	fmt.Println()
}

// handlePlanStatus 处理 /plan status <project_id>。
func handlePlanStatus(state *State, projectID string) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		fmt.Println("  用法: /plan status <project_id>")
		fmt.Println()
		return
	}

	reg := getPlanRegistry(state)
	if reg.progStore == nil {
		fmt.Println("  \033[33m尚未创建任何计划（ProgressStore 未初始化）\033[0m")
		fmt.Println()
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	progress, err := reg.progStore.LoadProgress(ctx, projectID)
	if err != nil {
		fmt.Printf("  \033[1;31m! 未找到项目 %s: %v\033[0m\n\n", projectID, err)
		return
	}

	fmt.Printf("  \033[1mProject:\033[0m  %s\n", projectID)
	fmt.Printf("  \033[1m阶段:\033[0m     %s\n", string(progress.Phase))
	fmt.Printf("  \033[1m完成度:\033[0m   %.0f%%\n", progress.OverallPercent)
	fmt.Printf("  \033[1m完成任务:\033[0m %d  阻塞任务: %d\n",
		len(progress.CompletedTasks), len(progress.BlockedTasks))

	reg.mu.RLock()
	plan := reg.plans[projectID]
	reg.mu.RUnlock()

	if plan != nil {
		fmt.Printf("  \033[1mPlan ID:\033[0m  %s\n", plan.PlanID)
		fmt.Printf("  \033[1m复杂度:\033[0m   %s  (预算 %d turns)\n",
			complexityLabelChat(plan), plan.Budget.TotalTurns)
		fmt.Println("  \033[1m子任务:\033[0m")
		// 构建已完成/阻塞 taskID 集合，加速查找
		completedSet := make(map[string]bool, len(progress.CompletedTasks))
		for _, ct := range progress.CompletedTasks {
			completedSet[ct.TaskID] = true
		}
		blockedSet := make(map[string]bool, len(progress.BlockedTasks))
		for _, bt := range progress.BlockedTasks {
			blockedSet[bt.TaskID] = true
		}
		for _, t := range plan.SubTasks {
			statusIcon := "○"
			if completedSet[t.TaskID] {
				statusIcon = "\033[32m✓\033[0m"
			} else if blockedSet[t.TaskID] {
				statusIcon = "\033[1;31m✗\033[0m"
			}
			fmt.Printf("    %s [%-8s] %s\n", statusIcon, string(t.Kind), t.Name)
		}
	}
	fmt.Println()
}

// ---------------------------------------------------------------------------
// 辅助函数
// ---------------------------------------------------------------------------

// complexityLabelChat 与 cmd_serve_plans.go 的 complexityLabel 逻辑一致。
func complexityLabelChat(plan *kernel.TaskPlan) string {
	if plan == nil {
		return "unknown"
	}
	turns := plan.Budget.TotalTurns
	tasks := len(plan.SubTasks)
	switch {
	case turns >= 80 || tasks >= 8:
		return "high"
	case turns >= 30 || tasks >= 4:
		return "medium"
	default:
		return "low"
	}
}

// planTruncate 截断字符串到 maxLen，超出时加 "..."。
func planTruncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen-3]) + "..."
}
