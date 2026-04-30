package main

// ClosedLoopController HTTP 接入层（MACCS Wave 3 Batch 3）。
//
// 把 sdk/kernel/closed_loop_controller.go 中实现的七阶段闭环工作流
// （Requirement → Design → Review → Execution → Acceptance → Delivery → Retrospective）
// 串联到 brain serve 主线，提供 REST 入口：
//
//	POST /v1/projects               创建并执行一个完整的七阶段闭环项目
//	GET  /v1/projects/{session_id}  查询指定 session 的所有 PhaseRecord 历史
//
// 与 /v1/plans（PlanOrchestrator 轻量路径）并列存在，互不替代：
//   - /v1/plans     — PlanOrchestrator 轻量执行（Plan → Execute → Reflect）
//   - /v1/projects  — ClosedLoopController 完整七阶段闭环（带 ProjectSession +
//                     ProjectStateMachine + DesignReviewLoop + DeliveryGenerator + Retrospective）
//
// 该文件一次性把 Wave 3 五项孤岛任务接入主线：
//   3.1 ProjectSession（NewProjectSession）
//   3.4 ProjectStateMachine（NewProjectStateMachineWithConfig，控制器内部使用）
//   3.5 DesignReviewLoop（NewDesignReviewLoop + DefaultDesignReviewer）
//   3.8 DeliveryGenerator（NewDefaultDeliveryGenerator）
//   3.9 Retrospective（NewRetrospectiveEngine + RetroAdapter slim 桥接）

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/leef-l/brain/sdk/kernel"
)

// projectService 持有 ClosedLoopController + 5 个组件的内存装配。
//
// sessions 缓存所有跑过的 ProjectSession 以便 GET 接口返回详细的
// PhaseRecord 历史；它与 controller 内部的 SessionStore 同源，但
// 暴露 sessionID -> session 的直接索引以避免重复读取 store。
type projectService struct {
	controller   *kernel.ClosedLoopController
	sessionStore kernel.ProjectSessionStore
	serveCtx     context.Context

	mu       sync.RWMutex
	sessions map[string]*kernel.ProjectSession
}

// retroAdapter 把 *DefaultRetrospectiveEngine.Analyze(ctx, *RetroInput)
// 适配成 ClosedLoopRetrospector.RunRetrospective(ctx, *ProjectSession)。
//
// 控制器在 Phase 7 仅传入当前 session，所以这里组装一个最小 RetroInput：
//   - Session 直接透传
//   - TaskResults 暂留空（具体执行结果已通过 session.SetContext("task_plan_result", ...)
//     存入 session，复盘引擎需要时可自行从 session.GetContext 读）
type retroAdapter struct{ engine *kernel.DefaultRetrospectiveEngine }

func (a *retroAdapter) RunRetrospective(ctx context.Context, session *kernel.ProjectSession) (interface{}, error) {
	if a.engine == nil || session == nil {
		return nil, fmt.Errorf("retroAdapter: engine 或 session 为空")
	}
	input := &kernel.RetroInput{Session: session}
	return a.engine.Analyze(ctx, input)
}

// newProjectService 装配一个 ClosedLoopController 服务实例。
//
// 任意必备依赖为 nil 时返回 nil，调用方需做空指针检查。注入的 baseOrch
// 即 startupOrch，控制器在 Phase 4 会通过 baseOrch.ExecuteTaskPlan 真实
// 派发子任务到 brain pool；learner 当前未直接注入控制器（控制器 7 阶段
// 不直接消费 LearningEngine），但保留参数以便未来在复盘阶段写回 L2/L3。
func newProjectService(baseOrch *kernel.Orchestrator, learner *kernel.LearningEngine, serveCtx context.Context) *projectService {
	if baseOrch == nil {
		return nil
	}
	_ = learner // 保留参数以便未来复盘阶段把 RetroLessons 写回学习引擎

	sessionStore := kernel.NewMemProjectSessionStore()
	reviewer := kernel.NewDefaultDesignReviewer()
	reviewLoop := kernel.NewDesignReviewLoop(reviewer, kernel.NewDesignReviewCriteria())
	deliveryGen := kernel.NewDefaultDeliveryGenerator()
	retroEngine := kernel.NewRetrospectiveEngine()

	deps := kernel.ClosedLoopDeps{
		Parser:       kernel.NewDefaultRequirementParser(),
		Designer:     kernel.NewDefaultDesignGenerator(),
		Reviewer:     reviewLoop,
		Scheduler:    kernel.NewExecutionScheduler(kernel.ExecutionSchedulerConfig{}),
		Tester:       kernel.NewDefaultAcceptanceTester(),
		DeliveryGen:  deliveryGen,
		Retrospect:   &retroAdapter{engine: retroEngine},
		SessionStore: sessionStore,
		Orchestrator: baseOrch,
	}
	controller := kernel.NewClosedLoopController(kernel.NewDefaultClosedLoopConfig(), deps)

	return &projectService{
		controller:   controller,
		sessionStore: sessionStore,
		serveCtx:     serveCtx,
		sessions:     make(map[string]*kernel.ProjectSession),
	}
}

// createProjectRequest POST /v1/projects 的请求体。
//
// project_name 为对外的项目名称（出现在 PhaseRecord / DeliveryManifest 等产出中）；
// goal 是自然语言目标，控制器会用 RequirementParser 解析为 RequirementSpec。
type createProjectRequest struct {
	ProjectName string `json:"project_name"`
	Goal        string `json:"goal"`
}

// createProjectResponse POST /v1/projects 的响应体。
//
// 返回完整的 ClosedLoopResult，并附上 phase_records 简表方便客户端
// 直接展示七阶段进度。
type createProjectResponse struct {
	SessionID     string                 `json:"session_id"`
	ProjectID     string                 `json:"project_id"`
	Success       bool                   `json:"success"`
	FinalPhase    string                 `json:"final_phase"`
	TotalDurMS    int64                  `json:"total_duration_ms"`
	PhaseResults  map[string]interface{} `json:"phase_results,omitempty"`
	PhaseRecords  []phaseRecordBrief     `json:"phase_records,omitempty"`
	Error         string                 `json:"error,omitempty"`
}

// phaseRecordBrief 把 PhaseRecord 压缩成对外暴露的 brief 视图。
type phaseRecordBrief struct {
	Phase     string `json:"phase"`
	Status    string `json:"status"`
	StartedAt string `json:"started_at,omitempty"`
	EndedAt   string `json:"ended_at,omitempty"`
	Error     string `json:"error,omitempty"`
	Artifacts int    `json:"artifacts"`
}

// ---------------------------------------------------------------------------
// HTTP handlers
// ---------------------------------------------------------------------------

// handleCreateProject POST /v1/projects 处理器。
//
// 接收 {project_name, goal}，调用 ClosedLoopController.Execute 跑完
// 七阶段闭环；返回 ClosedLoopResult + PhaseRecord 摘要。
func (s *projectService) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req createProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"invalid json: %v"}`, err), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Goal) == "" {
		http.Error(w, `{"error":"goal is required"}`, http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.ProjectName) == "" {
		req.ProjectName = fmt.Sprintf("project-%d", time.Now().UnixNano())
	}

	// 用 serveCtx 派生执行 ctx：server 关停时立刻取消，同时设置 30 分钟上限超时
	// （七阶段最坏情况是 7 * PhaseTimeout(15min) = 105min，所以 30 分钟仍可能不够；
	// 这里参考 /v1/plans 路径取一个保守上限，超过即 context.DeadlineExceeded 失败返回）
	execCtx, cancel := context.WithTimeout(s.serveCtx, 30*time.Minute)
	defer cancel()

	result, runErr := s.controller.Execute(execCtx, req.ProjectName, req.Goal)

	resp := createProjectResponse{}
	if result != nil {
		resp.SessionID = result.SessionID
		resp.ProjectID = result.ProjectID
		resp.Success = result.Success
		resp.FinalPhase = result.FinalPhase
		resp.TotalDurMS = result.TotalDuration.Milliseconds()
		resp.PhaseResults = result.PhaseResults
		resp.Error = result.Error

		// 加载完整 session 以便缓存 + 返回 PhaseRecord 摘要
		if session, loadErr := s.sessionStore.LoadSession(execCtx, result.SessionID); loadErr == nil && session != nil {
			s.rememberSession(session)
			resp.PhaseRecords = briefPhaseRecords(session)
		}
	}
	if runErr != nil && resp.Error == "" {
		resp.Error = runErr.Error()
	}

	w.Header().Set("Content-Type", "application/json")
	if resp.Error != "" {
		w.WriteHeader(http.StatusInternalServerError)
	} else {
		w.WriteHeader(http.StatusOK)
	}
	_ = json.NewEncoder(w).Encode(resp)
}

// handleGetProject GET /v1/projects/{session_id} 处理器。
//
// 优先从内存 sessions 索引读取；未命中再回退 sessionStore。
func (s *projectService) handleGetProject(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	sessionID := strings.TrimPrefix(r.URL.Path, "/v1/projects/")
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		http.Error(w, `{"error":"missing session id"}`, http.StatusBadRequest)
		return
	}

	session := s.lookupSession(sessionID)
	if session == nil {
		loaded, err := s.sessionStore.LoadSession(r.Context(), sessionID)
		if err != nil || loaded == nil {
			http.Error(w, fmt.Sprintf(`{"error":"session not found: %s"}`, sessionID), http.StatusNotFound)
			return
		}
		session = loaded
		s.rememberSession(session)
	}

	resp := map[string]interface{}{
		"session_id":    session.SessionID,
		"project_id":    session.ProjectID,
		"project_name":  session.ProjectName,
		"goal":          session.Goal,
		"status":        session.Status,
		"created_at":    session.CreatedAt.Format(time.RFC3339),
		"updated_at":    session.UpdatedAt.Format(time.RFC3339),
		"duration_ms":   session.Duration().Milliseconds(),
		"is_completed":  session.IsCompleted(),
		"phase_records": briefPhaseRecords(session),
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// rememberSession 缓存 sessionID → session，便于 GET 接口直接返回完整快照。
func (s *projectService) rememberSession(session *kernel.ProjectSession) {
	if session == nil || session.SessionID == "" {
		return
	}
	s.mu.Lock()
	s.sessions[session.SessionID] = session
	s.mu.Unlock()
}

func (s *projectService) lookupSession(sessionID string) *kernel.ProjectSession {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sessions[sessionID]
}

// briefPhaseRecords 把 ProjectSession.Phases 压缩成对外暴露的 brief 列表。
//
// 严格按照 ClosedLoopController 的七阶段顺序输出，便于前端按时间轴渲染。
func briefPhaseRecords(session *kernel.ProjectSession) []phaseRecordBrief {
	if session == nil {
		return nil
	}
	order := []kernel.ProjectPhaseType{
		kernel.PhaseRequirement,
		kernel.PhaseDesign,
		kernel.PhaseReview,
		kernel.PhaseExecution,
		kernel.PhaseAcceptance,
		kernel.PhaseDelivery,
		kernel.PhaseRetrospect,
	}
	out := make([]phaseRecordBrief, 0, len(order))
	for _, p := range order {
		rec := session.GetPhaseRecord(p)
		if rec == nil {
			continue
		}
		brief := phaseRecordBrief{
			Phase:     string(rec.Phase),
			Status:    rec.Status,
			Error:     rec.Error,
			Artifacts: len(rec.Artifacts),
		}
		if rec.StartedAt != nil {
			brief.StartedAt = rec.StartedAt.Format(time.RFC3339)
		}
		if rec.EndedAt != nil {
			brief.EndedAt = rec.EndedAt.Format(time.RFC3339)
		}
		out = append(out, brief)
	}
	return out
}
