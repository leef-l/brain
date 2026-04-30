package kernel

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/leef-l/brain/sdk/diaglog"
)

// ---------------------------------------------------------------------------
// TaskPlanResult — ExecuteTaskPlan 的返回结果
// ---------------------------------------------------------------------------

// TaskPlanResult 是 ExecuteTaskPlan 的返回结果。
type TaskPlanResult struct {
	PlanID         string                     `json:"plan_id"`
	CompletedTasks int                        `json:"completed_tasks"`
	FailedTasks    int                        `json:"failed_tasks"`
	TotalTasks     int                        `json:"total_tasks"`
	Results        map[string]*DelegateResult `json:"results"`
	Duration       time.Duration              `json:"duration"`
}

// TaskPlanReporter 是 TaskPlan 执行过程中的可选事件回调。
// eventType: "plan.task.started" | "plan.task.completed" | "plan.task.failed" | "plan.task.retrying" | "plan.layer.started" | "plan.layer.completed"
type TaskPlanReporter func(eventType string, taskID string, status string, detail string)

// ---------------------------------------------------------------------------
// ExecuteTaskPlan — 按拓扑分层执行 TaskPlan
// ---------------------------------------------------------------------------

// ExecuteTaskPlan 按 TaskPlan 的拓扑分层执行所有子任务。
// 同一层内的子任务并行执行（通过 DelegateBatch），层间串行。
// progress 可选，用于实时更新项目进度。
// reporter 可选，用于事件回调。
func (o *Orchestrator) ExecuteTaskPlan(ctx context.Context, plan *TaskPlan, progress *ProjectProgress, reporter TaskPlanReporter) (*TaskPlanResult, error) {
	start := time.Now()

	if plan == nil {
		return nil, fmt.Errorf("task plan is nil")
	}

	diaglog.Info("execute_task_plan", "plan execution start",
		"plan_id", plan.PlanID,
		"sub_tasks", len(plan.SubTasks),
	)

	// 1. 计算拓扑分层
	if err := plan.ComputeParallelLayers(); err != nil {
		return nil, fmt.Errorf("compute parallel layers: %w", err)
	}

	// 2. 设置 plan 状态
	plan.Status = PlanActive
	plan.UpdatedAt = time.Now()

	// 3. 如果 progress 不为 nil，设置阶段
	if progress != nil {
		progress.SetPhase(PhaseExecuting)
	}

	// 构建 taskID → SubTask 的索引，方便快速查找
	taskIndex := make(map[string]*PlanSubTask, len(plan.SubTasks))
	for i := range plan.SubTasks {
		taskIndex[plan.SubTasks[i].TaskID] = &plan.SubTasks[i]
	}

	// 收集所有结果
	allResults := make(map[string]*DelegateResult)
	var resultsMu sync.Mutex

	totalCompleted := 0
	totalFailed := 0

	// 4. 按拓扑分层逐层执行
	for layerIdx, layer := range plan.ParallelLayers {
		if ctx.Err() != nil {
			break
		}

		diaglog.Info("execute_task_plan", "layer start",
			"plan_id", plan.PlanID,
			"layer", layerIdx,
			"tasks", len(layer),
		)

		if reporter != nil {
			reporter("plan.layer.started", fmt.Sprintf("layer-%d", layerIdx),
				"running", fmt.Sprintf("layer %d with %d tasks", layerIdx, len(layer)))
		}

		// 构建本层的 DelegateBatchRequest
		batchReq := &DelegateBatchRequest{
			Requests: make([]*DelegateRequest, 0, len(layer)),
		}

		// 记录 layer 中 taskID 到 batch index 的映射
		layerTaskIDs := make([]string, 0, len(layer))

		for _, taskID := range layer {
			subTask, ok := taskIndex[taskID]
			if !ok {
				diaglog.Warn("execute_task_plan", "task not found in index",
					"plan_id", plan.PlanID,
					"task_id", taskID,
				)
				continue
			}

			// 标记任务为 running
			plan.UpdateTaskStatus(taskID, PlanTaskRunning)

			if reporter != nil {
				reporter("plan.task.started", taskID, "running", subTask.Instruction)
			}

			// 更新 progress 的 ActiveRuns
			if progress != nil {
				progress.UpdateRun(RunProgress{
					RunID:     fmt.Sprintf("%s-%s", plan.PlanID, taskID),
					TaskID:    taskID,
					TaskName:  subTask.Name,
					BrainKind: subTask.Kind,
					Status:    "running",
					MaxTurns:  subTask.EstimatedTurns,
					StartedAt: time.Now(),
				})
			}

			// 构建 DelegateRequest
			delegateReq := &DelegateRequest{
				TaskID:      taskID,
				TargetKind:  subTask.Kind,
				Instruction: subTask.Instruction,
			}
			if subTask.EstimatedTurns > 0 {
				delegateReq.Budget = &SubtaskBudget{
					MaxTurns: subTask.EstimatedTurns,
				}
			}

			batchReq.Requests = append(batchReq.Requests, delegateReq)
			layerTaskIDs = append(layerTaskIDs, taskID)
		}

		if len(batchReq.Requests) == 0 {
			continue
		}

		// 执行本层的 DelegateBatch
		batchResult, err := o.DelegateBatch(ctx, batchReq)
		if err != nil {
			diaglog.Error("execute_task_plan", "layer batch failed",
				"plan_id", plan.PlanID,
				"layer", layerIdx,
				"err", err,
			)
			// DelegateBatch 本身极少返回 error（内部会处理），这里标记所有任务失败
			for _, taskID := range layerTaskIDs {
				plan.UpdateTaskStatus(taskID, PlanTaskFailed)
				totalFailed++
				if reporter != nil {
					reporter("plan.task.failed", taskID, "failed", err.Error())
				}
			}
			continue
		}

		// 处理本层结果
		var retryTasks []int // 需要重试的任务在 layerTaskIDs 中的索引
		for i, taskID := range layerTaskIDs {
			if i >= len(batchResult.Results) {
				continue
			}
			result := batchResult.Results[i]
			subTask := taskIndex[taskID]

			resultsMu.Lock()
			allResults[taskID] = result
			resultsMu.Unlock()

			if result != nil && result.Status == "completed" {
				// 任务成功
				plan.UpdateTaskStatus(taskID, PlanTaskCompleted)
				totalCompleted++

				// 设置子任务结果
				subTask.Result = &PlanTaskResult{
					Output: string(result.Output),
				}

				if reporter != nil {
					reporter("plan.task.completed", taskID, "completed", string(result.Output))
				}

				// 更新 progress
				if progress != nil {
					duration := time.Duration(0)
					if subTask.StartedAt != nil {
						duration = time.Since(*subTask.StartedAt)
					}
					progress.CompleteTask(taskID, TaskSummary{
						TaskID:      taskID,
						TaskName:    subTask.Name,
						BrainKind:   subTask.Kind,
						Duration:    duration,
						TurnsUsed:   result.Usage.Turns,
						Success:     true,
						CompletedAt: time.Now(),
					})
				}
			} else {
				// 任务失败 — 检查是否需要重试
				errMsg := ""
				if result != nil {
					errMsg = result.Error
				}

				if subTask.RetryPolicy.MaxRetries > subTask.RetryCount {
					// 需要重试
					retryTasks = append(retryTasks, i)
					subTask.RetryCount++
					if reporter != nil {
						reporter("plan.task.retrying", taskID, "retrying",
							fmt.Sprintf("retry %d/%d: %s", subTask.RetryCount, subTask.RetryPolicy.MaxRetries, errMsg))
					}
				} else {
					// 不再重试，标记失败
					plan.UpdateTaskStatus(taskID, PlanTaskFailed)
					totalFailed++

					subTask.Result = &PlanTaskResult{
						Output: errMsg,
						Issues: []PlanIssue{{
							Severity:    "critical",
							Category:    "execution",
							Description: errMsg,
						}},
					}

					if reporter != nil {
						reporter("plan.task.failed", taskID, "failed", errMsg)
					}

					// 更新 progress
					if progress != nil {
						duration := time.Duration(0)
						if subTask.StartedAt != nil {
							duration = time.Since(*subTask.StartedAt)
						}
						progress.CompleteTask(taskID, TaskSummary{
							TaskID:      taskID,
							TaskName:    subTask.Name,
							BrainKind:   subTask.Kind,
							Duration:    duration,
							Success:     false,
							CompletedAt: time.Now(),
						})
					}
				}
			}
		}

		// 重试失败的任务
		if len(retryTasks) > 0 {
			o.retryFailedTasks(ctx, plan, taskIndex, layerTaskIDs, retryTasks,
				allResults, &resultsMu, &totalCompleted, &totalFailed, progress, reporter)
		}

		if reporter != nil {
			reporter("plan.layer.completed", fmt.Sprintf("layer-%d", layerIdx),
				"completed", fmt.Sprintf("completed=%d failed=%d", totalCompleted, totalFailed))
		}

		diaglog.Info("execute_task_plan", "layer completed",
			"plan_id", plan.PlanID,
			"layer", layerIdx,
			"total_completed", totalCompleted,
			"total_failed", totalFailed,
		)
	}

	// 5. 汇总结果
	duration := time.Since(start)

	if totalFailed > 0 {
		plan.Status = PlanFailed
	} else {
		plan.Status = PlanCompleted
	}
	plan.UpdatedAt = time.Now()

	// 更新 progress 阶段
	if progress != nil {
		if plan.Status == PlanCompleted {
			progress.SetPhase(PhaseAccepting)
		} else {
			progress.SetPhase(PhaseReworking)
		}
	}

	diaglog.Info("execute_task_plan", "plan execution finished",
		"plan_id", plan.PlanID,
		"status", plan.Status,
		"completed", totalCompleted,
		"failed", totalFailed,
		"total", len(plan.SubTasks),
		"duration", duration,
	)

	return &TaskPlanResult{
		PlanID:         plan.PlanID,
		CompletedTasks: totalCompleted,
		FailedTasks:    totalFailed,
		TotalTasks:     len(plan.SubTasks),
		Results:        allResults,
		Duration:       duration,
	}, nil
}

// retryFailedTasks 重试本层中失败的任务。
// 每个任务按其 RetryPolicy.BackoffBase 等待后单独重试。
func (o *Orchestrator) retryFailedTasks(
	ctx context.Context,
	plan *TaskPlan,
	taskIndex map[string]*PlanSubTask,
	layerTaskIDs []string,
	retryIndices []int,
	allResults map[string]*DelegateResult,
	resultsMu *sync.Mutex,
	totalCompleted *int,
	totalFailed *int,
	progress *ProjectProgress,
	reporter TaskPlanReporter,
) {
	retryBatch := &DelegateBatchRequest{
		Requests: make([]*DelegateRequest, 0, len(retryIndices)),
	}
	retryTaskIDs := make([]string, 0, len(retryIndices))

	for _, idx := range retryIndices {
		taskID := layerTaskIDs[idx]
		subTask := taskIndex[taskID]

		// 应用退避延迟
		if subTask.RetryPolicy.BackoffBase > 0 {
			backoff := subTask.RetryPolicy.BackoffBase * time.Duration(subTask.RetryCount)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
		}

		// 重新标记为 running
		plan.UpdateTaskStatus(taskID, PlanTaskRunning)

		delegateReq := &DelegateRequest{
			TaskID:      taskID,
			TargetKind:  subTask.Kind,
			Instruction: subTask.Instruction,
		}
		if subTask.EstimatedTurns > 0 {
			delegateReq.Budget = &SubtaskBudget{
				MaxTurns: subTask.EstimatedTurns,
			}
		}

		retryBatch.Requests = append(retryBatch.Requests, delegateReq)
		retryTaskIDs = append(retryTaskIDs, taskID)
	}

	retryResult, err := o.DelegateBatch(ctx, retryBatch)
	if err != nil {
		// 重试整体失败，全部标记失败
		for _, taskID := range retryTaskIDs {
			plan.UpdateTaskStatus(taskID, PlanTaskFailed)
			*totalFailed++
			if reporter != nil {
				reporter("plan.task.failed", taskID, "failed", fmt.Sprintf("retry batch error: %v", err))
			}
		}
		return
	}

	for i, taskID := range retryTaskIDs {
		if i >= len(retryResult.Results) {
			continue
		}
		result := retryResult.Results[i]
		subTask := taskIndex[taskID]

		resultsMu.Lock()
		allResults[taskID] = result
		resultsMu.Unlock()

		if result != nil && result.Status == "completed" {
			plan.UpdateTaskStatus(taskID, PlanTaskCompleted)
			*totalCompleted++

			subTask.Result = &PlanTaskResult{
				Output: string(result.Output),
			}

			if reporter != nil {
				reporter("plan.task.completed", taskID, "completed", string(result.Output))
			}

			if progress != nil {
				duration := time.Duration(0)
				if subTask.StartedAt != nil {
					duration = time.Since(*subTask.StartedAt)
				}
				progress.CompleteTask(taskID, TaskSummary{
					TaskID:      taskID,
					TaskName:    subTask.Name,
					BrainKind:   subTask.Kind,
					Duration:    duration,
					TurnsUsed:   result.Usage.Turns,
					Success:     true,
					CompletedAt: time.Now(),
				})
			}
		} else {
			plan.UpdateTaskStatus(taskID, PlanTaskFailed)
			*totalFailed++

			errMsg := ""
			if result != nil {
				errMsg = result.Error
			}

			subTask.Result = &PlanTaskResult{
				Output: errMsg,
				Issues: []PlanIssue{{
					Severity:    "critical",
					Category:    "execution",
					Description: fmt.Sprintf("failed after %d retries: %s", subTask.RetryCount, errMsg),
				}},
			}

			if reporter != nil {
				reporter("plan.task.failed", taskID, "failed",
					fmt.Sprintf("exhausted retries (%d/%d): %s", subTask.RetryCount, subTask.RetryPolicy.MaxRetries, errMsg))
			}

			if progress != nil {
				duration := time.Duration(0)
				if subTask.StartedAt != nil {
					duration = time.Since(*subTask.StartedAt)
				}
				progress.CompleteTask(taskID, TaskSummary{
					TaskID:      taskID,
					TaskName:    subTask.Name,
					BrainKind:   subTask.Kind,
					Duration:    duration,
					Success:     false,
					CompletedAt: time.Now(),
				})
			}
		}
	}
}
