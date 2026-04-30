package main

// PlanOrchestrator HTTP 接入层。
//
// 把 sdk/kernel/plan_orchestrator.go 中实现的 PlanOrchestrator.ExecuteProject
// 串联到 brain serve 主线，提供 REST 入口：
//
//	POST /v1/plans              创建并执行一个项目计划
//	GET  /v1/plans/{project_id} 查询指定项目的最新执行进度（从 ProgressStore 读）
//
// 这是把 PlanOrchestrator 从孤岛模块接入主线的最小闭环：
//   - 复用 cmd_serve.go 中已构造的全局 Orchestrator / LearningEngine /
//     ProjectMemory，不再 fork 新对象
//   - ProgressStore 默认走内存存储（MemoryProgressStore），便于查询返回结果
//   - 计划生成走「DefaultRequirementParser → DefaultDesignGenerator → ToTaskPlan」
//     的启发式管线，goal 为空或解析失败时回退到单任务兜底计划，确保 ExecuteProject
//     永远拿到一个非空 *TaskPlan

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/leef-l/brain/sdk/agent"
	"github.com/leef-l/brain/sdk/kernel"
)

// planService 把 PlanOrchestrator + ProgressStore + 解析/生成器
// 打包成一个可被 HTTP handler 直接调用的小服务。
type planService struct {
	orch       *kernel.PlanOrchestrator
	progStore  kernel.ProgressStore
	parser     kernel.RequirementParser
	designer   kernel.DesignGenerator
	memory     kernel.ProjectMemory
	defaultBud int
	serveCtx   context.Context // server 生命周期 ctx，shutdown 时自动取消

	// 记录 project_id → plan_id 映射，便于 GET 时返回最近一次的 plan 信息。
	mu       sync.RWMutex
	lastPlan map[string]*kernel.TaskPlan
}

// newPlanService 用 cmd_serve 构造好的 Orchestrator + LearningEngine 装配
// 一个 PlanOrchestrator 服务实例。
//
// 任意参数为 nil 时返回 nil，调用方需做空指针检查。注入的 baseOrch 即 startupOrch，
// PlanOrchestrator 内部会调用 baseOrch.ExecuteTaskPlan 真实跑任务。
func newPlanService(baseOrch *kernel.Orchestrator, learner *kernel.LearningEngine, serveCtx context.Context) *planService {
	if baseOrch == nil {
		return nil
	}
	memory := kernel.NewMemProjectMemory()
	progStore := kernel.NewMemoryProgressStore()

	po := kernel.NewPlanOrchestrator(baseOrch, kernel.PlanOrchestratorConfig{
		Memory:        memory,
		Learner:       learner,
		TotalBudget:   200,
		ProgressStore: progStore,
	})

	return &planService{
		orch:       po,
		progStore:  progStore,
		parser:     kernel.NewDefaultRequirementParser(),
		designer:   kernel.NewDefaultDesignGenerator(),
		memory:     memory,
		defaultBud: 200,
		serveCtx:   serveCtx,
		lastPlan:   make(map[string]*kernel.TaskPlan),
	}
}

// createPlanRequest POST /v1/plans 的请求体。
type createPlanRequest struct {
	ProjectID string `json:"project_id"`
	Goal      string `json:"goal"`
	Context   string `json:"context"`
}

// createPlanResponse POST /v1/plans 的响应体。
type createPlanResponse struct {
	PlanID              string                 `json:"plan_id"`
	ProjectID           string                 `json:"project_id"`
	Tasks               []planTaskBrief        `json:"tasks"`
	EstimatedComplexity string                 `json:"estimated_complexity"`
	EstimatedTurns      int                    `json:"estimated_turns"`
	Result              map[string]interface{} `json:"result,omitempty"`
	Error               string                 `json:"error,omitempty"`
}

// planTaskBrief 子任务摘要，避免直接暴露 PlanSubTask 的全部字段。
type planTaskBrief struct {
	TaskID         string `json:"task_id"`
	Name           string `json:"name"`
	Kind           string `json:"kind"`
	EstimatedTurns int    `json:"estimated_turns"`
	Status         string `json:"status"`
}

// buildPlan 把自然语言目标转成可执行的 TaskPlan。
//
// 按 Requirement → Design → Plan 三段式生成；若解析或生成失败则回退到
// 单任务兜底计划（central kind），保证 ExecuteProject 一定能拿到非空 plan。
func (s *planService) buildPlan(ctx context.Context, projectID, goal, extra string) (*kernel.TaskPlan, error) {
	combinedGoal := strings.TrimSpace(goal)
	if extra != "" {
		combinedGoal = combinedGoal + "\n\n[context]\n" + strings.TrimSpace(extra)
	}
	if combinedGoal == "" {
		return nil, fmt.Errorf("goal 不能为空")
	}

	// 1. 解析需求
	spec, err := s.parser.Parse(ctx, combinedGoal)
	if err != nil || spec == nil {
		// 兜底：直接构造 1 个 central 子任务的最小 plan
		return s.fallbackPlan(projectID, combinedGoal), nil
	}

	// 2. 生成设计方案
	proposal, err := s.designer.Generate(ctx, spec)
	if err != nil || proposal == nil {
		return s.fallbackPlan(projectID, combinedGoal), nil
	}

	// 3. 转 TaskPlan
	plan := s.designer.ToTaskPlan(proposal)
	if plan == nil || len(plan.SubTasks) == 0 {
		return s.fallbackPlan(projectID, combinedGoal), nil
	}

	// 用调用方传入的 project_id 覆盖（DesignGenerator 默认用 spec.SpecID）
	if projectID != "" {
		plan.ProjectID = projectID
	}
	return plan, nil
}

// fallbackPlan 当解析或方案生成失败时使用的兜底计划：单 central 任务。
func (s *planService) fallbackPlan(projectID, goal string) *kernel.TaskPlan {
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

// rememberPlan 缓存 project → plan，便于 GET 接口拼装响应。
func (s *planService) rememberPlan(plan *kernel.TaskPlan) {
	if plan == nil || plan.ProjectID == "" {
		return
	}
	s.mu.Lock()
	s.lastPlan[plan.ProjectID] = plan
	s.mu.Unlock()
}

func (s *planService) lookupPlan(projectID string) *kernel.TaskPlan {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastPlan[projectID]
}

// ---------------------------------------------------------------------------
// HTTP handlers
// ---------------------------------------------------------------------------

// handleCreatePlan POST /v1/plans 处理器。
//
// 接收 {project_id, goal, context}，生成 TaskPlan，调用
// PlanOrchestrator.ExecuteProject 真正执行；返回执行摘要 + ProjectExecutionResult。
func (s *planService) handleCreatePlan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req createPlanRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"invalid json: %v"}`, err), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Goal) == "" {
		http.Error(w, `{"error":"goal is required"}`, http.StatusBadRequest)
		return
	}
	if req.ProjectID == "" {
		req.ProjectID = fmt.Sprintf("proj-%d", time.Now().UnixNano())
	}

	// 构建 TaskPlan
	plan, err := s.buildPlan(r.Context(), req.ProjectID, req.Goal, req.Context)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusBadRequest)
		return
	}
	s.rememberPlan(plan)

	// 用 serveCtx 派生执行 ctx：server 关停时可立即取消，同时保留 30 分钟上限超时。
	execCtx, cancel := context.WithTimeout(s.serveCtx, 30*time.Minute)
	defer cancel()

	result, runErr := s.orch.ExecuteProject(execCtx, plan)

	resp := createPlanResponse{
		PlanID:              plan.PlanID,
		ProjectID:           plan.ProjectID,
		Tasks:               briefTasks(plan),
		EstimatedComplexity: complexityLabel(plan),
		EstimatedTurns:      plan.Budget.TotalTurns,
	}
	if runErr != nil {
		resp.Error = runErr.Error()
	}
	if result != nil {
		// 写入便于客户端排查的额外信息
		details := map[string]interface{}{
			"duration_ms":     result.Duration.Milliseconds(),
			"completed_tasks": 0,
			"failed_tasks":    0,
			"total_tasks":     len(plan.SubTasks),
		}
		if result.PlanResult != nil {
			details["completed_tasks"] = result.PlanResult.CompletedTasks
			details["failed_tasks"] = result.PlanResult.FailedTasks
			details["total_tasks"] = result.PlanResult.TotalTasks
		}
		if result.Reflection != nil {
			details["reflection_lessons"] = len(result.Reflection.Lessons)
			details["reflection_recommendations"] = result.Reflection.Recommendations
			details["reflection_completion_rate"] = result.Reflection.PlanDeviation.CompletionRate
		}
		details["progress_phase"] = string(result.Progress.Phase)
		details["progress_overall_percent"] = result.Progress.OverallPercent
		resp.Result = details
		if resp.Error == "" && result.ExecError != nil {
			resp.Error = result.ExecError.Error()
		}
	}

	w.Header().Set("Content-Type", "application/json")
	if resp.Error != "" {
		w.WriteHeader(http.StatusInternalServerError)
	} else {
		w.WriteHeader(http.StatusOK)
	}
	_ = json.NewEncoder(w).Encode(resp)
}

// handleGetPlan GET /v1/plans/{project_id} 处理器。
//
// 从 ProgressStore 读取最新 ProjectProgress 快照，再附上缓存的 plan 摘要。
func (s *planService) handleGetPlan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	projectID := strings.TrimPrefix(r.URL.Path, "/v1/plans/")
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		http.Error(w, `{"error":"missing project id"}`, http.StatusBadRequest)
		return
	}

	progress, err := s.progStore.LoadProgress(r.Context(), projectID)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusNotFound)
		return
	}

	resp := map[string]interface{}{
		"project_id":       projectID,
		"phase":            string(progress.Phase),
		"overall_percent":  progress.OverallPercent,
		"completed_tasks":  len(progress.CompletedTasks),
		"blocked_tasks":    len(progress.BlockedTasks),
	}
	if plan := s.lookupPlan(projectID); plan != nil {
		resp["plan_id"] = plan.PlanID
		resp["tasks"] = briefTasks(plan)
		resp["estimated_complexity"] = complexityLabel(plan)
		resp["estimated_turns"] = plan.Budget.TotalTurns
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// briefTasks 把 PlanSubTask 列表压缩成对外暴露的 brief 视图。
func briefTasks(plan *kernel.TaskPlan) []planTaskBrief {
	if plan == nil {
		return nil
	}
	out := make([]planTaskBrief, 0, len(plan.SubTasks))
	for _, t := range plan.SubTasks {
		out = append(out, planTaskBrief{
			TaskID:         t.TaskID,
			Name:           t.Name,
			Kind:           string(t.Kind),
			EstimatedTurns: t.EstimatedTurns,
			Status:         string(t.Status),
		})
	}
	return out
}

// complexityLabel 把 plan 的预算/任务数量映射为人类可读的复杂度等级。
//
// 这里是一个轻启发式：不依赖外部 LLM；阈值经验取自 ComplexityEstimator 的
// 同名分类，便于 dashboard 和 chat 直观展示。
func complexityLabel(plan *kernel.TaskPlan) string {
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
