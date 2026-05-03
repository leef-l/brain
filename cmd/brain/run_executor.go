package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/leef-l/brain/cmd/brain/agentpipe"
	"github.com/leef-l/brain/sdk/events"
	"github.com/leef-l/brain/sdk/kernel"
	"github.com/leef-l/brain/sdk/llm"
	"github.com/leef-l/brain/sdk/loop"
	"github.com/leef-l/brain/sdk/protocol"
	"github.com/leef-l/brain/sdk/runtimeaudit"
	"github.com/leef-l/brain/sdk/tool"
)

type managedRunExecution struct {
	Runtime           *cliRuntime
	Record            *persistedRunRecord
	Registry          tool.Registry
	Provider          llm.Provider
	ProviderName      string
	ProviderModel     string
	BrainID           string
	Prompt            string
	MaxTurns          int
	MaxDuration       time.Duration
	Stream            bool
	SystemPrompt      string
	EventBus          *events.MemEventBus                                                                  // 可选，非 nil 时双写事件到 EventBus
	BatchPlanner      loop.ToolBatchPlanner                                                                // 可选，非 nil 时启用并行工具分组
	MessageCompressor func(ctx context.Context, messages []llm.Message, budget int) ([]llm.Message, error) // 可选，消息压缩
	TokenBudget       int                                                                                  // 消息 token 预算
	InterruptChecker  loop.RunInterruptChecker                                                             // 可选，非 nil 时 Runner 每 turn 检查中断信号
	Orchestrator      *kernel.Orchestrator                                                                 // 可选,IntentClassifier 触发 PlanRunner 用
}

type managedRunOutcome struct {
	Result      *loop.RunResult
	ReplyText   string
	Summary     map[string]interface{}
	SummaryJSON json.RawMessage
	PlanID      int64
	FinalStatus string
}

func executeManagedRun(ctx context.Context, req managedRunExecution) (outcome *managedRunOutcome, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("run panic: %v", r)
		}
	}()
	parentRunID := ""
	if req.Record != nil {
		parentRunID = req.Record.ID
	}

	// Audit sink:把 audit 事件双写到 RunStore 和 EventBus(SSE)。
	// agentpipe 用 ctx 注入,这里只构造 SinkFunc。
	auditSink := runtimeaudit.SinkFunc(func(sinkCtx context.Context, ev runtimeaudit.Event) {
		if req.Runtime == nil || req.Runtime.RunStore == nil || req.Record == nil {
			return
		}
		_ = req.Runtime.RunStore.AppendEvent(req.Record.ID, ev.Type, ev.Message, append(json.RawMessage(nil), ev.Data...))
		if req.EventBus != nil {
			req.EventBus.Publish(sinkCtx, events.Event{
				ExecutionID: req.Record.ID,
				Type:        ev.Type,
				Data:        append(json.RawMessage(nil), ev.Data...),
			})
		}
	})

	_ = req.Runtime.RunStore.AppendEvent(req.Record.ID, "run.started", "run started", json.RawMessage(fmtJSON(map[string]interface{}{
		"brain":          req.BrainID,
		"provider":       req.ProviderName,
		"provider_model": req.ProviderModel,
	})))

	if err := saveRunCheckpoint(ctx, req.Runtime, req.Record, "running", 0, req.Record.ID+"-start"); err != nil {
		return nil, err
	}

	// 三模式统一抽象 — IntentClassifier 决定走 simple Runner 还是 PlanRunner。
	intent := agentpipe.NewDefaultIntentClassifier().Classify(req.Prompt)
	// 降级日志:用户/调试可见的提示。
	if intent == agentpipe.IntentProject {
		if req.BrainID != "central" {
			fmt.Fprintf(os.Stderr, "run: intent=project but brain=%s, downgrading to invocation (PlanRunner only runs on central)\n", req.BrainID)
		} else if req.Orchestrator == nil {
			fmt.Fprintf(os.Stderr, "run: intent=project but Orchestrator=nil, downgrading to invocation\n")
		}
	}
	var result *loop.RunResult
	// runSimple 接受 prompt 参数,因为 fallback 路径需要用 wrap 后的 prompt
	// 保住"做"的语义(IntentProject → ErrPlanFallback 时 LLM 单 turn
	// 容易只回"建议"而非"动手做",wrap 让 LLM 知道要逐步实现)。
	runSimple := func(prompt string) (*loop.RunResult, error) {
		inv := &agentpipe.Invocation{
			Provider:          req.Provider,
			Registry:          req.Registry,
			BrainID:           req.BrainID,
			Messages:          []llm.Message{{Role: "user", Content: []llm.ContentBlock{{Type: "text", Text: prompt}}}},
			SystemPrompt:      req.SystemPrompt,
			MaxTurns:          req.MaxTurns,
			MaxDuration:       req.MaxDuration,
			RunID:             req.Record.ID,
			UserUtterance:     req.Prompt, // 仍用原文做 SubtaskContext.UserUtterance
			ParentRunID:       parentRunID,
			Stream:            req.Stream,
			ToolObserver:      &runtimeToolObserver{runtime: req.Runtime, runID: req.Record.ID},
			BatchPlanner:      req.BatchPlanner,
			MessageCompressor: req.MessageCompressor,
			TokenBudget:       req.TokenBudget,
			InterruptChecker:  req.InterruptChecker,
			AuditSink:         auditSink,
			// run 模式不强制 ChatCentralBrain — 单 prompt 单 LLM,允许纯文本答复
		}
		return inv.Execute(ctx)
	}

	if intent == agentpipe.IntentProject && req.BrainID == "central" && req.Orchestrator != nil {
		// 项目级:跑 PlanRunner 七阶段闭环。返回 minimal RunResult 让后续 persist 流程继续。
		ctx = kernel.WithSubtaskContext(ctx, &protocol.SubtaskContext{UserUtterance: req.Prompt, ParentRunID: parentRunID})
		ctx = runtimeaudit.WithSink(ctx, auditSink)
		pr := agentpipe.NewPlanRunner(req.Orchestrator)
		// 必须 Close 释放 PlanOrchestrator 的 consumeFeedbackRequests goroutine,
		// 否则 run/serve 长跑期间每个项目级 run 都漏一个 EventBus 订阅 goroutine。
		defer pr.Close()
		// P0 权限击穿修复:把 host 的 SystemPrompt(含 mode/sandbox 约束)注入
		// 到每个 SubTask 的 Instruction 前缀,确保下游 sidecar 看到权限边界。
		projResult, projErr := pr.ExecuteWithInput(ctx, agentpipe.PlanInput{
			ProjectID:        "",
			Goal:             req.Prompt,
			ExtraInstruction: req.SystemPrompt,
		})
		// ErrPlanFallback:Parser/Designer 解析失败,走 simple 路径,避免
		// fallbackPlan 单 task 跑七阶段闭环浪费。
		// 用户原意是"做项目",降级时把 prompt 包装成"逐步实现"指令保住语义。
		if errors.Is(projErr, agentpipe.ErrPlanFallback) {
			fmt.Fprintf(os.Stderr, "run: plan parse failed, downgrading to simple invocation\n")
			wrapped := "请逐步实现以下需求,先列出实现计划再开始动手做(可以分多步,可以调用工具创建文件 / 编辑代码 / 验证):\n\n" + req.Prompt
			result, err = runSimple(wrapped)
		} else {
			err = projErr
			// 把项目结果包装成 RunResult,reply 含项目执行 summary
			state := loop.StateCompleted
			if err != nil {
				state = loop.StateFailed
			}
			// Budget 从 ProjectExecutionResult 各 SubTask 的 Usage 累加,
			// 避免持久化伪数据(turns=0/cost=0)污染 RunStore / 学习。
			result = &loop.RunResult{
				Run: &loop.Run{
					ID:      req.Record.ID,
					BrainID: req.BrainID,
					State:   state,
					Budget:  agentpipe.AggregatePlanBudget(projResult),
				},
			}
			if projResult != nil {
				summary := fmt.Sprintf("项目 %s 执行完成 (阶段: %s, 完成度: %.0f%%, 耗时: %s)",
					projResult.Progress.ProjectID,
					projResult.Progress.Phase,
					projResult.Progress.OverallPercent,
					projResult.Duration,
				)
				if projResult.Reflection != nil {
					summary += fmt.Sprintf("\n反思要点:%d 条\n推荐改进:%d 条",
						len(projResult.Reflection.Lessons),
						len(projResult.Reflection.Recommendations),
					)
				}
				result.FinalMessages = []llm.Message{{
					Role:    "assistant",
					Content: []llm.ContentBlock{{Type: "text", Text: summary}},
				}}
			}
		}
	} else {
		// 简单单步路径(IntentSimple,直接传 req.Prompt)
		result, err = runSimple(req.Prompt)
	}
	if err != nil {
		errJSON, _ := json.Marshal(map[string]string{"error": err.Error()})
		status := "failed"
		if ctx.Err() == context.Canceled {
			status = "cancelled"
		}
		_, _ = req.Runtime.RunStore.Finish(req.Record.ID, status, errJSON, err.Error())
		return nil, err
	}

	finalTurnIndex := 0
	finalTurnUUID := req.Record.ID + "-completed"
	if n := len(result.Turns); n > 0 && result.Turns[n-1] != nil && result.Turns[n-1].Turn != nil {
		finalTurnIndex = result.Turns[n-1].Turn.Index
		if result.Turns[n-1].Turn.UUID != "" {
			finalTurnUUID = result.Turns[n-1].Turn.UUID
		}
	}

	finalStatus := string(result.Run.State)
	// 构造 system blocks 供 checkpoint 持久化(与 agentpipe 内部一致)
	systemBlocks := []llm.SystemBlock{{Text: req.SystemPrompt, Cache: true}}
	if err := saveRunCheckpointWithMessages(ctx, req.Runtime, req.Record, finalStatus, finalTurnIndex, finalTurnUUID, result.FinalMessages, systemBlocks); err != nil {
		return nil, err
	}
	if err := saveRunUsage(ctx, req.Runtime, req.Record, req.ProviderName, req.ProviderModel, result); err != nil {
		return nil, err
	}

	replyText := ""
	for i := len(result.FinalMessages) - 1; i >= 0; i-- {
		msg := result.FinalMessages[i]
		if msg.Role == "assistant" {
			replyText = extractText(msg.Content)
			break
		}
	}

	planID, err := saveRunPlan(ctx, req.Runtime, req.Record, map[string]interface{}{
		"run_id":         result.Run.ID,
		"store_run_id":   req.Record.StoreRunID,
		"brain_id":       result.Run.BrainID,
		"prompt":         req.Prompt,
		"state":          finalStatus,
		"turns":          result.Run.Budget.UsedTurns,
		"llm_calls":      result.Run.Budget.UsedLLMCalls,
		"tool_calls":     result.Run.Budget.UsedToolCalls,
		"provider":       req.ProviderName,
		"provider_model": req.ProviderModel,
	})
	if err != nil {
		return nil, err
	}

	summary := map[string]interface{}{
		"run_id":       result.Run.ID,
		"store_run_id": req.Record.StoreRunID,
		"brain_id":     result.Run.BrainID,
		"state":        finalStatus,
		"turns":        result.Run.Budget.UsedTurns,
		"llm_calls":    result.Run.Budget.UsedLLMCalls,
		"tool_calls":   result.Run.Budget.UsedToolCalls,
		"elapsed_ms":   result.Run.Budget.ElapsedTime.Milliseconds(),
		"reply":        replyText,
		"provider":     req.ProviderName,
		"plan_id":      planID,
	}
	summaryJSON, _ := json.Marshal(summary)
	if _, err := req.Runtime.RunStore.Finish(req.Record.ID, finalStatus, summaryJSON, ""); err != nil {
		return nil, err
	}

	return &managedRunOutcome{
		Result:      result,
		ReplyText:   replyText,
		Summary:     summary,
		SummaryJSON: summaryJSON,
		PlanID:      planID,
		FinalStatus: finalStatus,
	}, nil
}

func managedRunBudget(maxTurns int, maxDuration time.Duration) loop.Budget {
	return loop.Budget{
		MaxTurns:     maxTurns,
		MaxCostUSD:   2.0,
		MaxLLMCalls:  maxTurns * 2,
		MaxToolCalls: maxTurns * 4,
		MaxDuration:  effectiveRunMaxDuration(maxDuration, 0),
	}
}

type runtimeToolObserver struct {
	runtime  *cliRuntime
	runID    string
	eventBus *events.MemEventBus // 可选，非 nil 时双写
}

func (o *runtimeToolObserver) OnToolStart(_ context.Context, _ *loop.Run, _ *loop.Turn, toolName string, input json.RawMessage) {
	if o == nil || o.runtime == nil || o.runtime.RunStore == nil {
		return
	}
	data := map[string]interface{}{
		"tool":  toolName,
		"input": json.RawMessage(append([]byte(nil), input...)),
	}
	_ = o.runtime.RunStore.AppendEvent(o.runID, "tool.start", toolName, json.RawMessage(fmtJSON(data)))
}

func (o *runtimeToolObserver) OnToolEnd(_ context.Context, _ *loop.Run, _ *loop.Turn, toolName string, ok bool, output json.RawMessage) {
	if o == nil || o.runtime == nil || o.runtime.RunStore == nil {
		return
	}
	data := map[string]interface{}{
		"tool":   toolName,
		"ok":     ok,
		"output": json.RawMessage(append([]byte(nil), output...)),
	}
	_ = o.runtime.RunStore.AppendEvent(o.runID, "tool.end", toolName, json.RawMessage(fmtJSON(data)))
}

func fmtJSON(v interface{}) []byte {
	raw, _ := json.Marshal(v)
	return raw
}
