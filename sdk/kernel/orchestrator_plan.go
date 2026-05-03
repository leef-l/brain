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
			// Replan 路径:层间 ctx cancel 时,把仍处于 running 的 SubTask 标记
			// PlanTaskInterrupted + 写 AbortReason + 从 PartialFilesTracker 提取
			// 已写入但未完成的文件路径。已被 DelegateBatch 完成的 SubTask
			//(Completed/Failed)状态不动。
			o.markRunningTasksInterrupted(plan, ctxAbortReason(ctx))
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

		// L2 序列学习：对同层（无依赖）任务按历史推荐顺序重排。
		// nil learner 或样本不足时直接保持原顺序，不影响正确性。
		if o.learner != nil && len(layer) > 1 {
			// 将 layer 内 taskID 转为 []TaskStep
			steps := make([]TaskStep, 0, len(layer))
			for _, taskID := range layer {
				if sub, ok := taskIndex[taskID]; ok {
					steps = append(steps, TaskStep{
						BrainKind:   sub.Kind,
						TaskType:    string(sub.Kind), // PlanSubTask 无独立 TaskType，用 Kind 代替
						ContextSize: len(sub.Instruction),
					})
				}
			}
			recommended := o.learner.RecommendOrder(steps)
			if len(recommended) == len(layer) {
				// 按推荐顺序重建 layer（taskID 列表）
				reordered := make([]string, 0, len(layer))
				used := make(map[int]bool, len(layer))
				for _, s := range recommended {
					for i, taskID := range layer {
						if used[i] {
							continue
						}
						sub, ok := taskIndex[taskID]
						if !ok {
							continue
						}
						if sub.Kind == s.BrainKind && string(sub.Kind) == s.TaskType {
							reordered = append(reordered, taskID)
							used[i] = true
							break
						}
					}
				}
				// 若有未命中的（多个同类型 Kind）追加到末尾
				for i, taskID := range layer {
					if !used[i] {
						reordered = append(reordered, taskID)
					}
				}
				if len(reordered) == len(layer) {
					layer = reordered
					diaglog.Info("plan", "applied sequence learning recommendation",
						"plan_id", plan.PlanID,
						"layer", layerIdx,
						"tasks", len(layer),
					)
				}
			}
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
				Workdir:     plan.Workdir, // workdir 端到端贯穿
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

				// 任务正常完成,清掉 PartialFilesTracker 累积(节省内存)。
				// task 失败也清(用户没必要 /restore 失败 task 的部分输出)。
				if o.partialFiles != nil {
					o.partialFiles.Clear(taskID)
				}

				// 设置子任务结果
				subTask.Result = &PlanTaskResult{
					Output: string(result.Output),
				}

				// MACCS 1.10：任务级审核闭环。reviewLoop 非 nil 时拿审核报告写入
				// subTask.Result.Review，供 reflection / 下一轮 plan 利用。
				// 失败 / 异常不阻塞主流程。
				//
				// 走 getReviewLoop 避免与 PlanOrchestrator 并发回写 race。
				if rl := o.getReviewLoop(); rl != nil {
					if review, rerr := rl.SubmitReview(ctx, *subTask, result.Output); rerr == nil && review != nil {
						subTask.Result.Review = review
						if !review.Passed {
							for _, iss := range review.Issues {
								subTask.Result.Issues = append(subTask.Result.Issues, PlanIssue{
									Severity:    iss.Severity,
									Category:    iss.Category,
									Description: iss.Description,
									SuggestedFix: iss.SuggestedFix,
								})
							}
						}
					} else if rerr != nil {
						diaglog.Warn("execute_task_plan", "review submit failed",
							"task_id", taskID, "err", rerr)
					}
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
					// 需要重试。失败原因多半是 turns_exhausted —— 同 budget 重试必然再败。
					// 这里没有直接的 Estimator 引用（Orchestrator 不持有，在 PlanOrchestrator 上）。
					// 由 PlanOrchestrator 上层的 retry 包装做 budget 调整；本函数只标记重试。
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

					// 失败 task 也清 PartialFilesTracker(用户不应 /restore 失败 task 的部分输出,
					// 那只会让 LLM 再次困惑)
					if o.partialFiles != nil {
						o.partialFiles.Clear(taskID)
					}

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

// markRunningTasksInterrupted 把所有 Status=Running 的 SubTask 标记为 Interrupted,
// 供 Replan 路径感知"哪些任务被中途打断"。
//
// 不修改 Completed / Failed 任务(它们已经定型)。
// AbortReason 来自 ctx.Cause(Go 1.20+ 的 context.WithCancelCause 派生)。
//
// 同时从 Orchestrator.partialFiles tracker 读出每个被中断 task 写过的文件路径,
// 填入 SubTask.PartialFiles,供 PlanOrchestrator.snapshotState 备份用。
// Orchestrator nil 时退化为单纯标记(测试场景兼容)。
func (o *Orchestrator) markRunningTasksInterrupted(plan *TaskPlan, reason string) {
	if plan == nil {
		return
	}
	now := time.Now()
	for i := range plan.SubTasks {
		if plan.SubTasks[i].Status == PlanTaskRunning {
			plan.SubTasks[i].Status = PlanTaskInterrupted
			plan.SubTasks[i].AbortReason = reason
			plan.SubTasks[i].CompletedAt = &now

			// 从 PartialFilesTracker 提取已写文件路径
			if o != nil && o.partialFiles != nil {
				files := o.partialFiles.Get(plan.SubTasks[i].TaskID)
				if len(files) > 0 {
					// 合并已有 PartialFiles(防止覆盖之前累积值)
					existing := plan.SubTasks[i].PartialFiles
					seen := make(map[string]bool, len(existing))
					for _, p := range existing {
						seen[p] = true
					}
					for _, p := range files {
						if !seen[p] {
							existing = append(existing, p)
							seen[p] = true
						}
					}
					plan.SubTasks[i].PartialFiles = existing
				}
			}
		}
	}
}

// ctxAbortReason 从 ctx.Cause 提取 cancel 原因字符串。
// Go 1.20+ 支持 context.WithCancelCause / context.Cause。
// 对于普通 ctx.Cancel(),返回 "context canceled"。
// PlanOrchestrator.replan 路径用 context.WithCancelCause(parent, ReplanCause{...}) 派生。
func ctxAbortReason(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	cause := context.Cause(ctx)
	if cause == nil {
		return "ctx_canceled"
	}
	return cause.Error()
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
			Workdir:     plan.Workdir, // workdir 端到端贯穿
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
