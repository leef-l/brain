// plan_orchestrator_replan_test.go — Replan 集成测试
//
// 项目铁律:本仓库禁 go test / go vet,只用 go build。本测试供后续 CI 用,
// 当下不会被执行。但写在这里有两个作用:
//   1. 锚定 ExecuteProjectWithReplan 的接口契约 + 关键边界
//   2. 未来 CI 启用时立即可跑

package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/leef-l/brain/sdk/agent"
	"github.com/leef-l/brain/sdk/events"
)

// TestExecuteProjectWithReplan_NoReplanRequested 验证基础路径:
// 没有 EventReplanRequested 事件时,ExecuteProjectWithReplan 行为等同于
// 原 ExecuteProject(单次执行 + 反思 + 完成)。
func TestExecuteProjectWithReplan_NoReplanRequested(t *testing.T) {
	po := newTestPlanOrchestrator(t)
	plan := newTestPlan("p1", []string{"task-a", "task-b"})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := po.ExecuteProjectWithReplan(ctx, plan)
	if err != nil {
		t.Fatalf("ExecuteProjectWithReplan returned err: %v", err)
	}
	if result == nil {
		t.Fatal("result is nil")
	}
	// PlanResult 来自 ExecuteTaskPlan,应该有 task 结果(测试 mock orchestrator 返回 completed)
	if result.PlanResult == nil {
		t.Error("PlanResult is nil — ExecuteTaskPlan 应该正常返回")
	}
}

// TestExecuteProjectWithReplan_TriggerReplan 验证 EventReplanRequested 触发后:
//  1. ExecuteTaskPlan 干净退出
//  2. snapshot 被收集
//  3. ReplanCapableDesigner 被调用
//  4. 新 plan.Version 自增
//  5. EventReplanCompleted 发布
func TestExecuteProjectWithReplan_TriggerReplan(t *testing.T) {
	po := newTestPlanOrchestrator(t)
	plan := newTestPlan("p2", []string{"task-a"})

	// 订阅 replan 完成事件
	completedCh := make(chan events.Event, 1)
	go func() {
		ch, unsub := po.Orchestrator.EventBus.Subscribe(context.Background(), "p2")
		defer unsub()
		for ev := range ch {
			if ev.Type == EventReplanCompleted {
				select {
				case completedCh <- ev:
				default:
				}
				return
			}
		}
	}()

	// 100ms 后发布 replan 请求(让 ExecuteTaskPlan 进入 layer 跑)
	go func() {
		time.Sleep(100 * time.Millisecond)
		req := &ReplanRequest{
			PlanID:    plan.PlanID,
			ProjectID: plan.ProjectID,
			Trigger:   TriggerUserModification,
			UserInput: "改成 SQLite",
			At:        time.Now(),
		}
		po.Orchestrator.EventBus.Publish(context.Background(), events.Event{
			ExecutionID: plan.ProjectID,
			Type:        EventReplanRequested,
			Data:        MarshalReplanRequest(req),
		})
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := po.ExecuteProjectWithReplan(ctx, plan)
	if err != nil {
		t.Fatalf("ExecuteProjectWithReplan returned err: %v", err)
	}
	if result == nil {
		t.Fatal("result is nil")
	}

	// 验证 replan.completed 事件被发布
	select {
	case ev := <-completedCh:
		// 验证事件 payload 含 version_after
		var payload map[string]interface{}
		_ = json.Unmarshal(ev.Data, &payload)
		if v, ok := payload["version_after"].(float64); !ok || v < 2 {
			t.Errorf("version_after should be >= 2, got %v", payload["version_after"])
		}
	case <-time.After(2 * time.Second):
		t.Error("EventReplanCompleted not received within 2s")
	}
}

// TestExecuteProjectWithReplan_LimitExceeded 验证连续触发 replan 超过硬上限时,
// 最终标记 PlanFailed 不再尝试。
func TestExecuteProjectWithReplan_LimitExceeded(t *testing.T) {
	po := newTestPlanOrchestrator(t)
	plan := newTestPlan("p3", []string{"task-a"})

	// 开后台 goroutine 反复发布 replan 事件
	stopCh := make(chan struct{})
	go func() {
		for {
			select {
			case <-stopCh:
				return
			case <-time.After(50 * time.Millisecond):
				req := &ReplanRequest{
					PlanID:    plan.PlanID,
					ProjectID: plan.ProjectID,
					Trigger:   TriggerUserModification,
					UserInput: "再改",
					At:        time.Now(),
				}
				po.Orchestrator.EventBus.Publish(context.Background(), events.Event{
					ExecutionID: plan.ProjectID,
					Type:        EventReplanRequested,
					Data:        MarshalReplanRequest(req),
				})
			}
		}
	}()
	defer close(stopCh)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result, err := po.ExecuteProjectWithReplan(ctx, plan)
	// 超过 maxReplansPerProject 应该返回 ExecError 标记或 err
	if err == nil && result != nil && result.ExecError == nil {
		t.Errorf("超过 replan 上限应有错误指示,实际 err=%v ExecError=%v", err, result.ExecError)
	}
}

// TestSnapshotState_PartialFiles 验证 markRunningTasksInterrupted +
// snapshotState 能正确把 running task 的 PartialFiles / AbortReason 收集进 snapshot。
func TestSnapshotState_PartialFiles(t *testing.T) {
	po := newTestPlanOrchestrator(t)
	plan := newTestPlan("p4", []string{"task-a"})

	// 模拟 task-a 被中断:Status=Interrupted + PartialFiles + AbortReason
	plan.SubTasks[0].Status = PlanTaskInterrupted
	plan.SubTasks[0].PartialFiles = []string{"server/main.go", "server/api/handler.go"}
	plan.SubTasks[0].AbortReason = "user_modification"

	progress := NewProjectProgress(plan.ProjectID, plan.PlanID)
	req := &ReplanRequest{
		Trigger:   TriggerUserModification,
		UserInput: "test",
	}

	snap := po.snapshotState(context.Background(), plan, progress, req)
	if len(snap.InterruptedTasks) != 1 {
		t.Fatalf("expected 1 interrupted task, got %d", len(snap.InterruptedTasks))
	}
	st := snap.InterruptedTasks[0]
	if len(st.PartialFiles) != 2 {
		t.Errorf("expected 2 partial files, got %d", len(st.PartialFiles))
	}
	if st.AbortReason != "user_modification" {
		t.Errorf("expected abort_reason=user_modification, got %q", st.AbortReason)
	}
}

// TestMarkRunningTasksInterrupted_OnlyAffectsRunning 验证只标记 Running 任务,
// Completed / Failed 保持原状态。
func TestMarkRunningTasksInterrupted_OnlyAffectsRunning(t *testing.T) {
	plan := newTestPlan("p5", []string{"a", "b", "c", "d"})
	plan.SubTasks[0].Status = PlanTaskCompleted
	plan.SubTasks[1].Status = PlanTaskRunning
	plan.SubTasks[2].Status = PlanTaskFailed
	plan.SubTasks[3].Status = PlanTaskRunning

	markRunningTasksInterrupted(plan, "test")

	if plan.SubTasks[0].Status != PlanTaskCompleted {
		t.Errorf("Completed task should not change, got %s", plan.SubTasks[0].Status)
	}
	if plan.SubTasks[1].Status != PlanTaskInterrupted {
		t.Errorf("Running task should be Interrupted, got %s", plan.SubTasks[1].Status)
	}
	if plan.SubTasks[2].Status != PlanTaskFailed {
		t.Errorf("Failed task should not change, got %s", plan.SubTasks[2].Status)
	}
	if plan.SubTasks[3].Status != PlanTaskInterrupted {
		t.Errorf("Running task should be Interrupted, got %s", plan.SubTasks[3].Status)
	}
}

func TestCtxAbortReason_NilSafe(t *testing.T) {
	if got := ctxAbortReason(nil); got != "" {
		t.Errorf("nil ctx should return empty string, got %q", got)
	}

	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(errors.New("custom reason"))
	if got := ctxAbortReason(ctx); got != "custom reason" {
		t.Errorf("expected custom reason, got %q", got)
	}
}

// ─── test helpers ───────────────────────────────────────────────────────────

// newTestPlanOrchestrator 构造一个最小可用的 PlanOrchestrator,
// 仅用于本文件的测试。
//
// Designer 用 DefaultDesignGenerator(实现 ReplanCapableDesigner)。
// Parser 用 DefaultRequirementParser。
// Orchestrator 用 mock(本测试不验证实际 sidecar 派发,只验证 Replan 流程)。
func newTestPlanOrchestrator(_ *testing.T) *PlanOrchestrator {
	bus := events.NewMemEventBus()
	mockOrch := &Orchestrator{
		EventBus: bus,
	}
	po := &PlanOrchestrator{
		Orchestrator:    mockOrch,
		Memory:          NewMemProjectMemory(),
		Estimator:       NewComplexityEstimatorWithTransfer(nil, NewTransferLearner()),
		ProgressStore:   NewMemoryProgressStore(),
		ExperienceStore: NewMemExperienceStore(),
	}
	po.SetReplanComponents(NewDefaultDesignGenerator(), NewDefaultRequirementParser())
	return po
}

// newTestPlan 构造一个含若干 SubTask 的最小 TaskPlan。
func newTestPlan(projectID string, taskIDs []string) *TaskPlan {
	plan := NewTaskPlan(projectID, "test goal: "+projectID)
	for i, id := range taskIDs {
		plan.AddSubTask(PlanSubTask{
			TaskID:         id,
			Name:           "test-task-" + id,
			Kind:           agent.Kind("code"),
			Instruction:    "test instruction for " + id,
			EstimatedTurns: 3,
			RetryPolicy:    RetryPolicy{MaxRetries: 0},
		})
		_ = i
	}
	plan.Status = PlanActive
	_ = plan.ComputeParallelLayers()
	return plan
}
