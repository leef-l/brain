// dispatch.go — 用户输入分诊路由
//
// 设计动机:
//   原 launchRun 永远新启 goroutine,导致 user 在 run 进行中输入新消息时
//   并发污染 state.Messages / ProjectMemory / LoopDetector 等共享状态,
//   是 "central 宣告循环" 死锁的根源之一。
//
//   新逻辑:
//     1. 没有 running run → 直接 launchRun(原行为)
//     2. 有 running run + 已选项目 → RelevanceClassifier 分诊
//     3. 有 running run + 无项目模式 → 入队列(等当前 run 完成后串行处理)
//
// Replan 触发链:
//   user 输入 "改成 SQLite"
//     → RelevanceClassifier.Classify → Modification
//     → enqueueModification(text)
//     → cooldown 计时器 3s 后聚合所有缓冲文本
//     → publishReplanRequested(EventBus)
//     → PlanOrchestrator.subscribeReplanRequests 收到
//     → cancel runCtx + snapshot + replan + newPlan + 继续执行
//
// 设计文档:Replan-基于现有持久化与EventBus的渐进路线.md §2.5

package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/leef-l/brain/sdk/events"
	"github.com/leef-l/brain/sdk/kernel"
)

// replanCooldown 是收到 Modification 到真正发布 replan.requested 的延迟。
// 期间到达的所有 Modification 文本会被合并成单条,避免用户连发"改 X / 还要改 Y"
// 触发 replan 风暴(每次 replan 都要 LLM 生成新 plan,代价高)。
const replanCooldown = 3 * time.Second

// orchEventBus 安全提取 Orchestrator 的 EventBus,nil orch 返回 nil。
// chat REPL 把 orch 当成 *kernel.Orchestrator 传进来,nil 时(mock provider 路径)
// 不发事件直接降级。
func orchEventBus(orch *kernel.Orchestrator) events.EventBus {
	if orch == nil {
		return nil
	}
	return orch.EventBus
}

// buildProjectStatusLine 构造 chat REPL 顶部固定的一行项目级进度展示。
//
// 显示场景(都满足才显示):
//   - 已选项目模式(state.CurrentProject != nil)
//   - PlanRunner 已构造且有正在跑的 plan(CurrentSnapshot.Empty == false)
//
// 输出格式: "📊 项目X v2 | 阶段:executing | ✓3 ⟳2 ○4 ✗0 (45%)"
//
// 性能:每次 RenderPromptFrame 都调一次,但读 CurrentSnapshot 用 RWMutex
// 读锁 + 数据 copy,开销极小。
func buildProjectStatusLine(state *State) string {
	if state == nil || state.CurrentProject == nil || state.IsNoProject {
		return ""
	}
	if state.PlanRunner == nil {
		return ""
	}
	po := state.PlanRunner.PlanOrch()
	if po == nil {
		return ""
	}
	snap := po.CurrentSnapshot()
	if snap.Empty {
		return ""
	}

	versionPart := ""
	if snap.Version > 1 {
		// v1 是初始,v2+ 表示经历过 replan
		versionPart = fmt.Sprintf(" v%d", snap.Version)
	}
	phase := string(snap.Phase)
	if phase == "" {
		phase = string(snap.Status)
	}
	return fmt.Sprintf("\033[2m📊 %s%s | %s | ✓%d ⟳%d ○%d ✗%d (%.0f%%)\033[0m",
		state.CurrentProject.Name, versionPart, phase,
		len(snap.CompletedTasks), len(snap.RunningTasks),
		len(snap.PendingTasks), len(snap.FailedTasks),
		snap.OverallPercent)
}

// dispatchResult 表示分诊后选择的动作,主要供 chat REPL 决定是否需要刷新 UI
// 或提示用户。
type dispatchResult struct {
	// Launched 表示已经启动新 run(无 running 时直接走原路径)
	Launched bool
	// LaunchedRunID 启动的 runID,Launched=true 时填
	LaunchedRunID string
	// Hint 给用户的提示文本,UI 渲染用(可空)
	Hint string
	// Relevance 分类结果(调试用,debug 模式下打印)
	Relevance kernel.Relevance
}

// dispatchUserInput 是 user input 进入 chat 后的统一分诊入口。
//
// 调用方:repl.go ActionEnter / ActionQueue 路径,在所有 / @run-X / ! 等特殊
// 前缀处理之后调本函数。
//
// 不返回 error — 任何分类失败 / 事件发布失败都降级为 Unrelated 行为
// (入队列 / 启动 run),不阻塞 user 操作。
func dispatchUserInput(state *State, input string, eventBus events.EventBus, launch func(string) string) dispatchResult {
	res := dispatchResult{Relevance: kernel.RelevanceUnrelated}

	// 1. 没有 running run → 直接启动(原行为)
	if !state.AnyRunning() {
		id := launch(input)
		res.Launched = true
		res.LaunchedRunID = id
		return res
	}

	// 2. 有 running run + 无项目模式 → 入队列(D2 串行化)
	//    无项目模式下没有 PlanOrchestrator,无法 replan,只能等当前 run 完成
	if state.IsNoProject || state.CurrentProject == nil {
		state.Enqueue(input)
		res.Hint = fmt.Sprintf("正在执行 %d 个任务,新输入已加入队列(完成后处理)", state.RunningCount())
		return res
	}

	// 3. 项目模式 + 有 running run → 走分类器
	classifier := state.RelevanceClassifier
	if classifier == nil {
		// 无分类器(启动失败兜底):入队列保守处理
		state.Enqueue(input)
		res.Hint = "正在执行,新输入已加入队列"
		return res
	}

	// 用 5s 超时 ctx 调分类器(LLM 兜底耗时)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	rctx := buildRelevanceContext(state)
	verdict := classifier.Classify(ctx, input, rctx)
	res.Relevance = verdict.Kind

	switch verdict.Kind {
	case kernel.RelevanceUnrelated:
		state.Enqueue(input)
		res.Hint = fmt.Sprintf("[unrelated] 已加入队列(当前 %d 任务运行中)", state.RunningCount())

	case kernel.RelevanceStatusQuery:
		// 即时打印进度摘要,不打断,不进对话历史
		res.Hint = renderProjectStatusSummary(state)

	case kernel.RelevanceModification:
		// 缓冲 + cooldown:连续 modification 合并成一次 replan
		enqueueModificationToBuffer(state, input)
		res.Hint = fmt.Sprintf("[modification] 已收到修改请求,%ds 内合并后重新规划\n"+
			"  (中断时未完成文件会备份到 .brain/partial/<task_id>/,可用 /restore 恢复)",
			int(replanCooldown.Seconds()))
		// 触发 cooldown 定时器(已存在则重置)
		armReplanCooldown(state, eventBus)

	case kernel.RelevanceCancel:
		canceled := state.CancelAllRuns()
		if canceled {
			res.Hint = "[cancel] 已取消所有任务"
		} else {
			res.Hint = "[cancel] 当前无可取消任务"
		}

	case kernel.RelevanceRefine:
		// Refine 走 brain.feedback.requested 事件,sub agent 自己决定是否吸收
		// 不打断当前 run
		publishRefineHint(eventBus, state.CurrentProject.ID, input)
		res.Hint = "[refine] 已发送补充指令"
	}

	return res
}

// buildRelevanceContext 从 state 构造分类器需要的上下文。
//
// 数据来源(优先级从高到低):
//   1. PlanOrchestrator.CurrentSnapshot — 真实 plan.SubTasks 状态分桶
//   2. ActiveRuns — 兜底,只有 input 文本不知道任务名
//
// LLM 兜底分类时这些字段进 system prompt,信息越准分类越靠谱。
func buildRelevanceContext(state *State) kernel.RelevanceContext {
	rctx := kernel.RelevanceContext{}
	if state.CurrentProject != nil {
		rctx.PlanGoal = state.CurrentProject.Name
	}

	// 优先从 PlanOrchestrator 拿真实 plan 状态
	if state.PlanRunner != nil {
		if po := state.PlanRunner.PlanOrch(); po != nil {
			snap := po.CurrentSnapshot()
			if !snap.Empty {
				if snap.Goal != "" {
					rctx.PlanGoal = snap.Goal
				}
				for _, t := range snap.RunningTasks {
					rctx.RunningTaskNames = append(rctx.RunningTaskNames, t.Name+" ("+t.Kind+")")
				}
				for _, t := range snap.CompletedTasks {
					rctx.CompletedTaskNames = append(rctx.CompletedTaskNames, t.Name)
				}
				return rctx
			}
		}
	}

	// 兜底:从 ActiveRuns 读 input 作为 RunningTaskNames(轮廓)
	state.RunsMu.Lock()
	for _, h := range state.ActiveRuns {
		if h != nil && h.Input != "" {
			short := h.Input
			if len(short) > 60 {
				short = short[:60] + "..."
			}
			rctx.RunningTaskNames = append(rctx.RunningTaskNames, short)
		}
	}
	state.RunsMu.Unlock()
	return rctx
}

// enqueueModificationToBuffer 把 modification 文本加入 state 的合并缓冲区。
func enqueueModificationToBuffer(state *State, text string) {
	state.ReplanCooldownMu.Lock()
	defer state.ReplanCooldownMu.Unlock()
	state.ModificationBuffer = append(state.ModificationBuffer, text)
}

// armReplanCooldown 启动 / 重置 replan cooldown 定时器。
//
// cooldown 末尾合并 ModificationBuffer 所有文本,发一次 replan.requested 事件。
//
// 重置逻辑:每次新 modification 到达都重置定时器,让连续输入"延后"统一处理。
// 例如 user 在 1s 内连发 3 条修改,只触发 1 次 replan(在最后一条后 3s)。
func armReplanCooldown(state *State, eventBus events.EventBus) {
	state.ReplanCooldownMu.Lock()
	defer state.ReplanCooldownMu.Unlock()

	// 已有定时器 → 停止重置
	if state.ReplanCooldownTimer != nil {
		state.ReplanCooldownTimer.Stop()
	}

	state.ReplanCooldownAt = time.Now()
	state.ReplanCooldownTimer = time.AfterFunc(replanCooldown, func() {
		flushModificationBuffer(state, eventBus)
	})
}

// flushModificationBuffer 在 cooldown 末尾被调,合并 buffer 文本发布 replan 事件。
func flushModificationBuffer(state *State, eventBus events.EventBus) {
	state.ReplanCooldownMu.Lock()
	if len(state.ModificationBuffer) == 0 {
		state.ReplanCooldownMu.Unlock()
		return
	}
	merged := strings.Join(state.ModificationBuffer, "; ")
	state.ModificationBuffer = nil
	state.ReplanCooldownTimer = nil
	state.ReplanCooldownMu.Unlock()

	if state.CurrentProject == nil || eventBus == nil {
		return
	}

	req := &kernel.ReplanRequest{
		ProjectID: state.CurrentProject.ID,
		Trigger:   kernel.TriggerUserModification,
		UserInput: merged,
		At:        time.Now(),
	}
	eventBus.Publish(context.Background(), events.Event{
		ExecutionID: state.CurrentProject.ID,
		Type:        kernel.EventReplanRequested,
		Data:        kernel.MarshalReplanRequest(req),
	})
}

// publishRefineHint 把 Refine 类输入作为 brain.feedback.requested 事件发布,
// 让正在跑的 sub agent 在下一个 turn 看到补充指令。
//
// 注意:brain.feedback.requested 是 MACCS 5.3 active learning 通道,
// 当前 PlanOrchestrator.consumeFeedbackRequests 只把它存为 lesson 进 ProjectMemory。
// Phase 3 不改 sub 行为,Refine hint 落库后下一轮 plan 自动从 MemoryRetriever
// 检索到。后续 PR 可让 sub agent 实时订阅本通道。
func publishRefineHint(eventBus events.EventBus, projectID, hint string) {
	if eventBus == nil || projectID == "" {
		return
	}
	payload := map[string]interface{}{
		"brain_kind": "central",
		"reason":     "user_refine",
		"question":   hint,
	}
	data, _ := json.Marshal(payload)
	eventBus.Publish(context.Background(), events.Event{
		ExecutionID: projectID,
		Type:        "brain.feedback.requested",
		Data:        data,
	})
}

// renderProjectStatusSummary 即时打印项目级进度摘要(StatusQuery 触发)。
//
// 优先用 PlanOrchestrator.CurrentSnapshot 获得真实 plan 状态分桶;
// 没有正在跑的 plan(snapshot.Empty)则退化到 ActiveRuns 视图。
func renderProjectStatusSummary(state *State) string {
	var b strings.Builder
	if state.CurrentProject != nil {
		b.WriteString(fmt.Sprintf("📊 项目: %s\n", state.CurrentProject.Name))
	}

	// 尝试从 PlanOrchestrator 拿真实快照
	if state.PlanRunner != nil {
		if po := state.PlanRunner.PlanOrch(); po != nil {
			snap := po.CurrentSnapshot()
			if !snap.Empty {
				return renderPlanSnapshot(state, snap)
			}
		}
	}

	// 兜底:渲染 ActiveRuns
	state.RunsMu.Lock()
	if len(state.ActiveRuns) == 0 {
		b.WriteString("  当前无任务运行\n")
	} else {
		b.WriteString(fmt.Sprintf("  正在执行 %d 个任务:\n", len(state.ActiveRuns)))
		for id, h := range state.ActiveRuns {
			if h == nil {
				continue
			}
			elapsed := time.Since(h.StartedAt).Round(time.Second)
			input := h.Input
			if len(input) > 50 {
				input = input[:50] + "..."
			}
			b.WriteString(fmt.Sprintf("    ⟳ %s [%s] %s\n", id, elapsed, input))
		}
	}
	state.RunsMu.Unlock()

	if state.RunningCount() == 0 && state.CurrentProject != nil {
		b.WriteString("  (输入 \"继续\" 恢复未完成任务)\n")
	}

	return b.String()
}

// renderPlanSnapshot 把 PlanSnapshot 渲染为可读 chat 输出。
func renderPlanSnapshot(state *State, snap kernel.PlanSnapshot) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("📊 项目: %s\n", state.CurrentProject.Name))
	b.WriteString(fmt.Sprintf("   计划 v%d  状态 %s  阶段 %s  完成度 %.0f%%\n",
		snap.Version, snap.Status, snap.Phase, snap.OverallPercent))
	b.WriteString(fmt.Sprintf("   ✓ 完成 %d  ⟳ 进行 %d  ○ 待执行 %d  ✗ 失败 %d\n",
		len(snap.CompletedTasks), len(snap.RunningTasks), len(snap.PendingTasks), len(snap.FailedTasks)))

	if len(snap.CompletedTasks) > 0 {
		b.WriteString("   已完成:\n")
		for _, t := range snap.CompletedTasks {
			b.WriteString(fmt.Sprintf("     ✓ %s (%s)\n", t.Name, t.Kind))
		}
	}
	if len(snap.RunningTasks) > 0 {
		b.WriteString("   正在做:\n")
		for _, t := range snap.RunningTasks {
			b.WriteString(fmt.Sprintf("     ⟳ %s (%s)\n", t.Name, t.Kind))
		}
	}
	if len(snap.PendingTasks) > 0 && len(snap.PendingTasks) <= 5 {
		b.WriteString("   等待执行:\n")
		for _, t := range snap.PendingTasks {
			b.WriteString(fmt.Sprintf("     ○ %s (%s)\n", t.Name, t.Kind))
		}
	} else if len(snap.PendingTasks) > 5 {
		b.WriteString(fmt.Sprintf("   等待执行: %d 个任务\n", len(snap.PendingTasks)))
	}
	return b.String()
}
