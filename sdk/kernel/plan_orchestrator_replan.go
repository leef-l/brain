// plan_orchestrator_replan.go — PlanOrchestrator 的动态重规划能力
//
// 设计动机:
//   原 ExecuteProject 是阻塞顺序流程,Plan 一旦启动只能等执行完成才能修改。
//   Replan 改造让 ExecuteProject 进入 for 循环:跑 ExecuteTaskPlan 的同时
//   旁路 goroutine 监听 EventReplanRequested 事件,收到则 cancel runCtx 触发
//   ExecuteTaskPlan 干净退出,基于当前快照(完成/中断/待执行 + 项目记忆)调用
//   ReplanCapableDesigner 生成新 plan,再回到 for 循环用新 plan 继续执行。
//
//   触发源:
//     - chat REPL 发布 user.modification → relevance 命中 Modification → 转 EventReplanRequested
//     - sub agent 通过 brain.feedback.requested 上报 → PlanOrchestrator 升级
//     - PlanOrchestrator 内部判断 sub_failure 重试耗尽 → 自发布 EventReplanRequested
//
// 安全边界:
//   - 单次 ExecuteProject 最多 5 次 replan(防止 user 连续改触发风暴)
//   - 连续 2 次 replan 失败(LLM 错 / Designer 错)→ 整个 project fail
//   - replan 进行时,本轮 ExecuteTaskPlan 必须等 ctx 真的 cancel + DelegateBatch 退出
//
// 设计文档:Replan-基于现有持久化与EventBus的渐进路线.md §2.3-2.4

package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/leef-l/brain/sdk/diaglog"
	"github.com/leef-l/brain/sdk/events"
)

const (
	// maxReplansPerProject 单次 ExecuteProject 内的硬上限。
	// 超过此值 → 标记 PlanFailed 不再 replan。
	maxReplansPerProject = 5

	// maxConsecutiveReplanFailures replan 连续失败上限。
	// 超过此值 → 标记 PlanFailed 不再尝试。
	maxConsecutiveReplanFailures = 2

	// replanCooldownMs replan 完成后的冷静期。
	// 期间到达的新 modification 事件先入缓冲区,统一在 cooldown 末尾触发一次 replan。
	// 实现位于 chat REPL(本文件不实现 cooldown,Phase 3 在 dispatchUserInput 处加)。
	replanCooldownMs = 3000
)

// replanCause 是 ctx.WithCancelCause 用的 error 类型,
// 让 ExecuteTaskPlan 通过 context.Cause(ctx) 拿到结构化的 abort 原因。
type replanCause struct {
	Trigger ReplanTrigger
	Reason  string
}

func (e *replanCause) Error() string {
	if e.Reason != "" {
		return fmt.Sprintf("replan:%s:%s", e.Trigger, e.Reason)
	}
	return fmt.Sprintf("replan:%s", e.Trigger)
}

// ExecuteProjectWithReplan 是 ExecuteProject 的 Replan-aware 版本。
//
// 与 ExecuteProject 的差异:
//   - 进入 for 循环,允许多次 replan
//   - 派生可中断 runCtx + 旁路 goroutine 订阅 EventReplanRequested
//   - 收到事件 → cancel runCtx → ExecuteTaskPlan 干净退出 → snapshot → replan → newPlan
//   - 已完成 SubTask 在新 plan 中保留 Status=Completed 不重做
//
// 调用方:agentpipe.PlanRunner / chat 的 runChatPlanFlow 应改用此方法替代 ExecuteProject。
// 旧 ExecuteProject 保留兼容,但不订阅 EventBus 也不响应 replan,只走单次执行。
func (po *PlanOrchestrator) ExecuteProjectWithReplan(ctx context.Context, plan *TaskPlan) (*ProjectExecutionResult, error) {
	start := time.Now()
	progress := NewProjectProgress(plan.ProjectID, plan.PlanID)
	progress.SetPhase(PhaseExecuting)

	// 估算复杂度 + 预算分配(同 ExecuteProject)
	if po.Estimator != nil {
		for i := range plan.SubTasks {
			est := po.Estimator.Estimate(plan.SubTasks[i])
			plan.SubTasks[i].EstimatedTurns = est.EstimatedTurns
		}
	}
	if po.BudgetPool != nil {
		po.BudgetPool.AllocateForPlan(plan)
	}

	var (
		planResult              *TaskPlanResult
		execErr                 error
		replanCount             int
		consecutiveReplanErrors int
	)

	// for 循环跑 ExecuteTaskPlan,允许 replan 多次
	for {
		// 派生可被 replan 取消的 runCtx
		runCtx, cancel := context.WithCancelCause(ctx)

		// 启动旁路 goroutine 订阅 EventReplanRequested
		replanCh := make(chan *ReplanRequest, 1)
		go po.subscribeReplanRequests(runCtx, plan.ProjectID, replanCh)

		// 启动 ExecuteTaskPlan
		done := make(chan execTaskPlanDone, 1)
		go func() {
			result, err := po.Orchestrator.ExecuteTaskPlan(runCtx, plan, progress, po.makeReporter(progress))
			done <- execTaskPlanDone{result, err}
		}()

		// 等执行完成 OR 收到 replan
		select {
		case d := <-done:
			// 正常完成(或 ctx.Err 命中,但没有 replan 请求)
			cancel(nil) // 清理 runCtx
			planResult = d.result
			execErr = d.err
			// 跳出 for,进入反思阶段
			goto reflection

		case req := <-replanCh:
			// 收到 replan 请求
			diaglog.Info("plan_orchestrator", "replan triggered",
				"plan_id", plan.PlanID,
				"trigger", req.Trigger,
				"replan_count", replanCount,
			)
			po.publishReplanStarted(plan, req)

			// 触发 abort:cancel runCtx with cause
			cancel(&replanCause{Trigger: req.Trigger, Reason: replanReasonText(req)})

			// 等 ExecuteTaskPlan 干净退出(DelegateBatch 已改 ctx-aware,100ms 内)
			d := <-done
			planResult = d.result // 部分结果,可能含已完成的 SubTask

			// 检查 replan 次数硬上限
			replanCount++
			if replanCount > maxReplansPerProject {
				diaglog.Warn("plan_orchestrator", "replan limit exceeded, failing project",
					"plan_id", plan.PlanID,
					"replan_count", replanCount,
				)
				plan.Status = PlanFailed
				po.publishReplanAborted(plan, fmt.Errorf("replan limit %d exceeded", maxReplansPerProject))
				execErr = fmt.Errorf("replan limit exceeded")
				goto reflection
			}

			// 标记 plan 暂停
			plan.Status = PlanPaused

			// 收集快照
			snapshot := po.snapshotState(ctx, plan, progress, req)

			// 调 ReplanLLM(复用 RequirementParser + DesignGenerator)
			newPlan, err := po.replan(ctx, plan, snapshot, req)
			if err != nil {
				consecutiveReplanErrors++
				diaglog.Warn("plan_orchestrator", "replan failed",
					"plan_id", plan.PlanID,
					"err", err,
					"consecutive_errors", consecutiveReplanErrors,
				)
				if consecutiveReplanErrors >= maxConsecutiveReplanFailures {
					plan.Status = PlanFailed
					po.publishReplanAborted(plan, err)
					execErr = fmt.Errorf("replan failed %d times consecutively: %w",
						consecutiveReplanErrors, err)
					goto reflection
				}
				// 单次失败,继续用旧 plan 跑(已被中断的 SubTask 标记 Interrupted,
				// 重新进入 for 会重试)
				plan.Status = PlanActive
				po.unmarkInterruptedAsRunning(plan)
				continue
			}

			// 成功:替换为 newPlan
			consecutiveReplanErrors = 0
			plan = newPlan
			po.publishReplanCompleted(plan, snapshot)
			// 进入下一轮 for(用 newPlan 跑)
		}
	}

reflection:
	// 5. 元认知反思(同 ExecuteProject,但 Plan 可能 PlanFailed)
	var reflection *ReflectionReport
	if po.MetaCognitive != nil {
		reflection = po.MetaCognitive.Reflect(plan, progress)
		po.MetaCognitive.FeedbackToLearner(reflection)
		po.storeReflectionToMemory(ctx, plan.ProjectID, reflection)
		po.appendMemoryRetrievedHints(ctx, plan, reflection)
	}

	// 6. 最终持久化
	if plan.Status == PlanCompleted {
		progress.SetPhase(PhaseDelivered)
	}
	if po.ProgressStore != nil {
		_ = po.ProgressStore.SaveProgress(ctx, progress)
	}

	// 7. MACCS 5.4:异步抽取项目模式
	if po.PatternExtractor != nil && po.ExperienceStore != nil {
		exp := buildProjectExperience(plan, planResult, reflection, time.Since(start))
		go po.runPatternExtraction(exp)
	}

	return &ProjectExecutionResult{
		PlanResult: planResult,
		Progress:   progress.Snapshot(),
		Reflection: reflection,
		Duration:   time.Since(start),
		ExecError:  execErr,
	}, nil
}

// execTaskPlanDone 是 ExecuteTaskPlan 的返回结果包装。
type execTaskPlanDone struct {
	result *TaskPlanResult
	err    error
}

// makeReporter 构造 TaskPlanReporter,转发事件并持久化进度。
// 与原 ExecuteProject 内的匿名 reporter 等价。
func (po *PlanOrchestrator) makeReporter(progress *ProjectProgress) TaskPlanReporter {
	return func(eventType, taskID, status, detail string) {
		diaglog.Info("plan_orchestrator", eventType,
			"task_id", taskID,
			"status", status,
		)
		if po.ProgressStore != nil && progress != nil {
			_ = po.ProgressStore.SaveProgress(context.Background(), progress)
		}
	}
}

// subscribeReplanRequests 订阅 EventReplanRequested 事件,过滤本项目相关后写入 ch。
//
// 发布方包括:
//   - chat REPL.dispatchUserInput(Phase 3 实现)
//   - sub agent 通过 brain.feedback.requested 触发(Phase 3 加路由)
//   - PlanOrchestrator 自身在 sub_failure 时也可发布
//
// ch 缓冲为 1:本设计假设同时只处理一个 replan,后续到达的 modification 应该在
// chat 层 cooldown 缓冲合并(Phase 3 实现)。
func (po *PlanOrchestrator) subscribeReplanRequests(ctx context.Context, projectID string, ch chan<- *ReplanRequest) {
	if po.Orchestrator == nil || po.Orchestrator.EventBus == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			diaglog.Error("plan_orchestrator", "replan subscriber panic", "recover", fmt.Sprint(r))
		}
	}()

	evCh, unsub := po.Orchestrator.EventBus.Subscribe(ctx, projectID)
	defer unsub()

	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-evCh:
			if !ok {
				return
			}
			if ev.Type != EventReplanRequested {
				continue
			}
			req, err := UnmarshalReplanRequest(ev.Data)
			if err != nil {
				diaglog.Warn("plan_orchestrator", "malformed replan request",
					"err", err)
				continue
			}
			// 只处理本项目的 request(Subscribe 已按 executionID 过滤,这里再 double check)
			if req.ProjectID != "" && req.ProjectID != projectID {
				continue
			}
			select {
			case ch <- req:
				return // 一次 ExecuteProject 内只处理一个 replan,处理完该轮 for 重新订阅
			case <-ctx.Done():
				return
			}
		}
	}
}

// snapshotState 从 plan + progress + Memory 收集 Replan 上下文。
//
// 包括:
//   - 已完成 SubTask 列表(从 plan.SubTasks Status=Completed,带 Result.Output 摘要)
//   - 中断 SubTask 列表(Status=Interrupted,带 PartialFiles + AbortReason)
//   - 待执行 SubTask 列表(Status=Pending)
//   - 项目记忆 top-N(MemoryRetriever 按 plan.Goal 检索)
//
// 副作用:
//   - 把所有 InterruptedTasks 的 PartialFiles 备份到 .brain/partial/<runID>/
//     然后从工作目录删除,确保 newPlan 启动时 sub agent 看到干净环境
//     (用户可用 chat /restore <runID> 恢复)
func (po *PlanOrchestrator) snapshotState(
	ctx context.Context,
	plan *TaskPlan,
	progress *ProjectProgress,
	req *ReplanRequest,
) *ReplanSnapshot {
	snap := &ReplanSnapshot{
		CapturedAt: time.Now(),
	}

	// 按 SubTask 状态分桶
	for _, st := range plan.SubTasks {
		s := SubTaskSnapshot{
			TaskID:      st.TaskID,
			Name:        st.Name,
			Kind:        string(st.Kind),
			Instruction: st.Instruction,
			Status:      st.Status,
		}
		if st.Result != nil {
			s.OutputSummary = summarizeOutput(st.Result.Output)
			s.Confidence = st.Result.Confidence
		}
		s.PartialFiles = append(s.PartialFiles, st.PartialFiles...)
		s.AbortReason = st.AbortReason

		switch st.Status {
		case PlanTaskCompleted:
			snap.CompletedTasks = append(snap.CompletedTasks, s)
		case PlanTaskInterrupted, PlanTaskRunning:
			// Running 状态正常应该已被 markRunningTasksInterrupted 改成 Interrupted,
			// 但防御一下
			snap.InterruptedTasks = append(snap.InterruptedTasks, s)
		case PlanTaskPending, PlanTaskBlocked:
			snap.PendingTasks = append(snap.PendingTasks, s)
		}
	}

	// 从 progress 补充 turns_used
	if progress != nil {
		progressSnap := progress.Snapshot()
		taskUsage := make(map[string]int, len(progressSnap.CompletedTasks))
		for _, t := range progressSnap.CompletedTasks {
			taskUsage[t.TaskID] = t.TurnsUsed
		}
		fillTurnsUsed(snap.CompletedTasks, taskUsage)
		fillTurnsUsed(snap.InterruptedTasks, taskUsage)
	}

	// 备份所有中断 SubTask 的 PartialFiles 到 .brain/partial/<task_id>/
	// 失败 silent(不阻塞 replan,partial 文件仍在原位次优而非阻塞)
	if plan.Workdir != "" {
		for i := range snap.InterruptedTasks {
			if len(snap.InterruptedTasks[i].PartialFiles) == 0 {
				continue
			}
			_, _ = BackupPartialFiles(plan.Workdir,
				snap.InterruptedTasks[i].TaskID,
				snap.InterruptedTasks[i].PartialFiles)
		}
	}

	// 从 ProjectMemory 检索相关历史(MemoryRetriever 在 PlanOrchestrator 上)
	if po.Memory != nil && po.MemoryRetriever != nil {
		entries, err := po.Memory.Query(ctx, MemoryQuery{
			ProjectID: plan.ProjectID,
			Limit:     50,
		})
		if err == nil && len(entries) > 0 {
			results := po.MemoryRetriever.Retrieve(entries, plan.Goal, nil, po.memoryRetrieveN)
			for _, r := range results {
				if r.Score <= 0 {
					continue
				}
				summary := r.Entry.Summary
				if summary == "" {
					summary = r.Entry.Content
				}
				snap.MemoryHints = append(snap.MemoryHints,
					fmt.Sprintf("[%s/%s] %s", r.Entry.Type, r.MatchType, summary))
			}
		}
	}

	return snap
}

// replan 调 DesignGenerator 重新生成 plan。
//
// 优先调 ReplanCapableDesigner.GenerateWithModification(LLM 或启发式)。
// 如果 designer 不实现该接口(测试 / mock 场景),返回 ErrNoReplanSupport,
// 调用方降级为单次失败(consecutiveReplanErrors++)。
func (po *PlanOrchestrator) replan(
	ctx context.Context,
	plan *TaskPlan,
	snapshot *ReplanSnapshot,
	req *ReplanRequest,
) (*TaskPlan, error) {
	if po.designer() == nil {
		return nil, ErrNoReplanSupport
	}

	rd, ok := po.designer().(ReplanCapableDesigner)
	if !ok {
		return nil, ErrNoReplanSupport
	}

	// 用 RequirementParser 解出 OriginalSpec(可选,失败也不阻塞)
	var originalSpec *RequirementSpec
	if po.parser() != nil {
		if spec, err := po.parser().Parse(ctx, plan.Goal); err == nil {
			originalSpec = spec
		}
	}

	in := ReplanInput{
		OriginalSpec:     originalSpec,
		OriginalPlan:     plan,
		Snapshot:         snapshot,
		Trigger:          req.Trigger,
		UserModification: req.UserInput,
		SubError:         req.SubError,
		SubHint:          req.Hint,
	}

	proposal, err := rd.GenerateWithModification(ctx, in)
	if err != nil {
		return nil, fmt.Errorf("replan: GenerateWithModification: %w", err)
	}

	// 用 ToReplanTaskPlan 把 proposal 转 TaskPlan(已完成任务复用 Result + Status=Completed)
	if dg, ok := rd.(*DefaultDesignGenerator); ok {
		newPlan := dg.ToReplanTaskPlan(proposal, plan)
		if newPlan == nil {
			return nil, fmt.Errorf("replan: ToReplanTaskPlan returned nil")
		}
		newPlan.ProjectID = plan.ProjectID
		newPlan.Status = PlanActive
		return newPlan, nil
	}

	// 兜底:用普通 ToTaskPlan(已完成任务可能被重做,这是次优但安全)
	if dg, ok := po.designer().(DesignGenerator); ok {
		newPlan := dg.ToTaskPlan(proposal)
		if newPlan == nil {
			return nil, fmt.Errorf("replan: ToTaskPlan returned nil")
		}
		newPlan.ProjectID = plan.ProjectID
		newPlan.Version = plan.Version + 1
		newPlan.Status = PlanActive
		return newPlan, nil
	}

	return nil, ErrNoReplanSupport
}

// ErrNoReplanSupport 当 designer 不支持 replan 时返回。
var ErrNoReplanSupport = errors.New("designer does not implement ReplanCapableDesigner")

// designer 返回 PlanOrchestrator 的 DesignGenerator 引用。
//
// 设计动机:PlanOrchestrator 当前没有 Designer 字段(由 PlanRunner 持有),
// 这里临时通过弱引用机制 — 使用 Memory.Get / Estimator 等同源构造路径不优雅。
// 简化:让 PlanOrchestrator 自身持有 Designer 字段(下方 SetDesigner 注入)。
func (po *PlanOrchestrator) designer() interface{} {
	return po.cachedDesigner
}

// parser 返回 RequirementParser 引用。
func (po *PlanOrchestrator) parser() RequirementParser {
	return po.cachedParser
}

// SetReplanComponents 注入 Designer + Parser(供 PlanRunner 在构造后调用)。
//
// 不进 PlanOrchestratorConfig 是为了不破坏现有 NewPlanOrchestrator 签名。
// PlanRunner.ensurePlanOrch 在创建 PlanOrchestrator 后调一次此方法。
func (po *PlanOrchestrator) SetReplanComponents(designer DesignGenerator, parser RequirementParser) {
	po.cachedDesigner = designer
	po.cachedParser = parser
}

// publishReplanStarted 发布 replan.started 事件供 chat UI 渲染。
func (po *PlanOrchestrator) publishReplanStarted(plan *TaskPlan, req *ReplanRequest) {
	po.publishReplanEvent(EventReplanStarted, plan.ProjectID, map[string]interface{}{
		"plan_id":        plan.PlanID,
		"version_before": plan.Version,
		"trigger":        req.Trigger,
	})
}

// publishReplanCompleted 发布 replan.completed 事件。
func (po *PlanOrchestrator) publishReplanCompleted(newPlan *TaskPlan, snap *ReplanSnapshot) {
	added := 0
	modified := 0
	kept := 0
	for _, st := range newPlan.SubTasks {
		switch st.Status {
		case PlanTaskCompleted:
			kept++
		default:
			// 简化:与 snapshot 中已存在的 task_id 比对决定 modified vs added
			if isExistingTask(st.TaskID, snap) {
				modified++
			} else {
				added++
			}
		}
	}
	po.publishReplanEvent(EventReplanCompleted, newPlan.ProjectID, map[string]interface{}{
		"plan_id":            newPlan.PlanID,
		"version_after":      newPlan.Version,
		"sub_tasks_added":    added,
		"sub_tasks_modified": modified,
		"sub_tasks_kept":     kept,
	})
}

// publishReplanAborted 发布 replan.aborted 事件。
func (po *PlanOrchestrator) publishReplanAborted(plan *TaskPlan, err error) {
	po.publishReplanEvent(EventReplanAborted, plan.ProjectID, map[string]interface{}{
		"plan_id": plan.PlanID,
		"error":   err.Error(),
	})
}

// publishReplanEvent 是 EventBus.Publish 的 helper,自动序列化 payload。
func (po *PlanOrchestrator) publishReplanEvent(eventType, projectID string, payload map[string]interface{}) {
	if po.Orchestrator == nil || po.Orchestrator.EventBus == nil {
		return
	}
	data, _ := json.Marshal(payload)
	po.Orchestrator.EventBus.Publish(context.Background(), events.Event{
		ExecutionID: projectID,
		Type:        eventType,
		Data:        data,
	})
}

// unmarkInterruptedAsRunning 把 Interrupted 状态恢复成 Pending,让下一轮 for
// 重新调度执行(replan 失败,用旧 plan 重试)。
func (po *PlanOrchestrator) unmarkInterruptedAsRunning(plan *TaskPlan) {
	for i := range plan.SubTasks {
		if plan.SubTasks[i].Status == PlanTaskInterrupted {
			plan.SubTasks[i].Status = PlanTaskPending
			plan.SubTasks[i].AbortReason = ""
		}
	}
}

// storeReflectionToMemory 把 reflection 的 lessons + recommendations 写入 ProjectMemory。
// 抽自原 ExecuteProject 第 4 步(plan_orchestrator.go:279-310)以便复用。
func (po *PlanOrchestrator) storeReflectionToMemory(ctx context.Context, projectID string, reflection *ReflectionReport) {
	if po.Memory == nil || reflection == nil {
		return
	}
	for _, lesson := range reflection.Lessons {
		if lesson.Importance >= 0.3 {
			_ = po.Memory.Store(ctx, MemoryEntry{
				ProjectID:  projectID,
				Type:       MemoryLesson,
				Content:    lesson.Description,
				Summary:    lesson.Category + ": " + lesson.Description,
				Tags:       []string{lesson.Category, "reflection"},
				Importance: lesson.Importance,
			})
		}
	}
	for _, rec := range reflection.Recommendations {
		if rec == "" || strings.HasPrefix(rec, "[相似经验/") {
			continue
		}
		_ = po.Memory.Store(ctx, MemoryEntry{
			ProjectID:  projectID,
			Type:       MemoryLesson,
			Content:    rec,
			Summary:    "recommendation: " + rec,
			Tags:       []string{"recommendation", "reflection"},
			Importance: 0.4,
		})
	}
}

// appendMemoryRetrievedHints 抽自原 ExecuteProject 第 4.5 步。
func (po *PlanOrchestrator) appendMemoryRetrievedHints(ctx context.Context, plan *TaskPlan, reflection *ReflectionReport) {
	if po.MemoryRetriever == nil || po.Memory == nil || reflection == nil {
		return
	}
	entries, err := po.Memory.Query(ctx, MemoryQuery{
		ProjectID: plan.ProjectID,
		Limit:     200,
	})
	if err != nil || len(entries) == 0 {
		return
	}
	results := po.MemoryRetriever.Retrieve(entries, plan.Goal, nil, po.memoryRetrieveN)
	for _, r := range results {
		if r.Score <= 0 {
			continue
		}
		summary := r.Entry.Summary
		if summary == "" {
			summary = r.Entry.Content
		}
		reflection.Recommendations = append(reflection.Recommendations,
			"[相似经验/"+r.MatchType+"] "+summary,
		)
	}
}

// ─── helpers ────────────────────────────────────────────────────────────────

func fillTurnsUsed(tasks []SubTaskSnapshot, usage map[string]int) {
	for i := range tasks {
		if u, ok := usage[tasks[i].TaskID]; ok {
			tasks[i].TurnsUsed = u
		}
	}
}

func isExistingTask(taskID string, snap *ReplanSnapshot) bool {
	if snap == nil {
		return false
	}
	for _, t := range snap.CompletedTasks {
		if t.TaskID == taskID {
			return true
		}
	}
	for _, t := range snap.InterruptedTasks {
		if t.TaskID == taskID {
			return true
		}
	}
	for _, t := range snap.PendingTasks {
		if t.TaskID == taskID {
			return true
		}
	}
	return false
}

func replanReasonText(req *ReplanRequest) string {
	switch req.Trigger {
	case TriggerUserModification:
		return req.UserInput
	case TriggerSubFailure:
		return req.SubError
	case TriggerSubFeedback:
		return req.Hint
	}
	return ""
}
