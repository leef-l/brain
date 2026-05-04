package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/leef-l/brain/cmd/brain/agentpipe"
	"github.com/leef-l/brain/cmd/brain/cliruntime"
	"github.com/leef-l/brain/cmd/brain/config"
	"github.com/leef-l/brain/cmd/brain/env"
	"github.com/leef-l/brain/sdk/llm"
	"github.com/leef-l/brain/sdk/loop"
	"github.com/leef-l/brain/sdk/runtimeaudit"
	"github.com/leef-l/brain/sdk/tool"
)

type RunResult struct {
	Result       *loop.RunResult
	Err          error
	ReplyText    string
	Canceled     bool
	BaseMsgCount int // 执行前 state.Messages 的长度，用于追加而非覆盖
}

// StartChatRun 启动一个新的 chat run，返回分配的 runID。
// 事件（progress + result）通过 eventCh 统一发送。
func StartChatRun(state *State, provider llm.Provider, brainID string, maxTurns int,
	input string, eventCh chan<- ChatEvent) string {

	state.TurnCount++
	turnIndex := state.TurnCount

	baseMessages := make([]llm.Message, len(state.Messages))
	copy(baseMessages, state.Messages)
	baseMsgCount := len(state.Messages)
	registry := state.Registry
	opts := state.Opts
	runtime, _ := deps.NewDefaultCLIRuntime(brainID)
	var runRec *cliruntime.RunRecord
	if runtime != nil {
		runRec, _ = runtime.RunStore.Create(brainID, input, string(state.Mode), state.Sandbox.Primary())
	}

	ctx, cancel := config.WithOptionalTimeout(context.Background(), state.RunTimeout)
	// 把 ProjectID 写入 ctx,让 runChatTurn / bridge.delegate 透传到 DelegateRequest。
	if state.CurrentProject != nil && !state.IsNoProject {
		ctx = withProjectID(ctx, state.CurrentProject.ID)
	}
	runID := state.StartRun(input, cancel)

	go func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintf(os.Stderr, "\n[brain panic] run %s: %v\n", runID, r)
				select {
				case eventCh <- ChatEvent{RunID: runID, Type: "result", Result: &RunResult{
					Err:          fmt.Errorf("internal panic: %v", r),
					BaseMsgCount: baseMsgCount,
				}}:
				case <-ctx.Done():
				}
			}
			state.RemoveRun(runID)
		}()

		if runtime != nil && runRec != nil {
			_ = deps.SaveRunCheckpoint(ctx, runtime, runRec, "running", 0, runRec.ID+"-start")
			ctx = runtimeaudit.WithSink(ctx, runtimeaudit.SinkFunc(func(_ context.Context, ev runtimeaudit.Event) {
				_ = runtime.RunStore.AppendEvent(runRec.ID, ev.Type, ev.Message, append(json.RawMessage(nil), ev.Data...))
			}))
		}

		// MACCS 三模式统一抽象:根据 IntentClassifier 判断走 simple 还是 plan 路径。
		// - IntentProject: central + 项目级需求 → PlanRunner.Execute (七阶段闭环带 review/反思/反馈)
		// - IntentSimple : 直接走 Runner (轻量单 turn,日常问答和小任务)
		intent := agentpipe.NewDefaultIntentClassifier().Classify(input)
		var result *loop.RunResult
		var err error
		// 降级日志:意图是 project 但前置条件不满足时给用户/调试可见的提示。
		if intent == agentpipe.IntentProject {
			if brainID != "central" {
				fmt.Fprintf(os.Stderr, "chat: intent=project but brain=%s, downgrading to invocation (PlanRunner only runs on central)\n", brainID)
			} else if state.Orchestrator == nil {
				fmt.Fprintf(os.Stderr, "chat: intent=project but Orchestrator=nil, downgrading to invocation\n")
			}
		}
		if intent == agentpipe.IntentProject && brainID == "central" && state.Orchestrator != nil {
			// 项目级路径:用 PlanRunner 跑 PlanOrchestrator 全流程。
			result, err = runChatPlanFlow(ctx, state, input, runID, eventCh)
			// ErrPlanFallback:Parser/Designer 解析失败,自动降级到 simple 路径,
			// 避免 fallbackPlan 单 task 仍跑七阶段闭环浪费 token。
			//
			// 但用户原意是"做项目"(IntentProject 命中),simple Invocation
			// 单 turn 容易输出"建议方案"而非"开始做"。把 input 包装成
			// "逐步实现 + 先列计划再做"的 prompt,保住"做"的语义。
			if errors.Is(err, agentpipe.ErrPlanFallback) {
				fmt.Fprintf(os.Stderr, "chat: plan parse failed, downgrading to simple invocation\n")
				wrapped := wrapForFallback(input)
				result, err = runChatTurn(ctx, state, provider, registry, opts, brainID, maxTurns,
					turnIndex, baseMessages, wrapped, state.Sandbox.Primary(), state.RunTimeout, runID, eventCh)
			}
		} else {
			// 简单路径:走原 Runner 链路(agentpipe.Invocation)
			result, err = runChatTurn(ctx, state, provider, registry, opts, brainID, maxTurns,
				turnIndex, baseMessages, input, state.Sandbox.Primary(), state.RunTimeout, runID, eventCh)
		}
		if runtime != nil && runRec != nil {
			persistChatTurn(ctx, runtime, runRec, provider.Name(), input, state.Mode, state.Sandbox.Primary(), opts.System, result, err)
		}
		// MACCS Wave 7+ 项目级持久化:把本 turn 的 user + assistant 消息写到
		// project_conversations 表,并更新 last_active_at。
		// 仅在选了项目(非无项目模式)时执行。
		persistChatTurnToProject(ctx, state, input, result, err)
		rr := RunResult{
			Result:       result,
			Err:          err,
			Canceled:     ctx.Err() == context.Canceled,
			BaseMsgCount: baseMsgCount,
		}
		if result != nil {
			rr.ReplyText = extractAssistantReply(result.FinalMessages)
		}
		select {
		case eventCh <- ChatEvent{RunID: runID, Type: "result", Result: &rr}:
		case <-ctx.Done():
		}
	}()

	return runID
}

// runChatTurn 是 chat 模式下单次 user turn 的入口,薄壳封装 agentpipe.Invocation。
//
// 三模式统一抽象后(2026-05-03 重构),所有 Runner 配置/Workdir/nudge/sanitize
// 修复都集中在 agentpipe 一处,这里只做 chat 特定的:
//   - DispatchHint(关键词强制 delegate hint,可选)
//   - PreprocessUserInput(去填充音,只对当前 turn 临时 messages 生效)
//   - LiveReporter(把 tool start/end 推到 chat UI 的 todo 框)
//   - ChatCentralBrain flag(central 模式触发 nudge 重试)
func runChatTurn(ctx context.Context, state *State, provider llm.Provider, registry tool.Registry,
	opts loop.RunOptions, brainID string, maxTurns int, turnIndex int,
	baseMessages []llm.Message, input, workdir string, maxDuration time.Duration,
	runID string, eventCh chan<- ChatEvent) (*loop.RunResult, error) {

	// 收集 SystemBlocks:base + DispatchHint(central 模式)
	systemBlocks := append([]llm.SystemBlock(nil), opts.System...)
	if brainID == "central" {
		if hint := DispatchHint(input); hint != "" {
			systemBlocks = append(systemBlocks, llm.SystemBlock{Text: hint, Cache: false})
		}
	}
	// agentpipe.Invocation 把 SystemPrompt 当首块,SystemBlocks 追加。
	// 这里我们已经合并好 — 直接全部传 SystemBlocks,SystemPrompt 留空。
	var systemPrompt string
	if len(systemBlocks) > 0 {
		systemPrompt = systemBlocks[0].Text
		systemBlocks = systemBlocks[1:]
	}

	// Token-saving P2-C:对发给 LLM 的临时 messages 应用预处理。
	// 长粘贴摘要(LongPasteThresholdChars 命中)后,llmInput 含 [PASTE id=xxx] 标记,
	// 与原文不再语义等价。result.FinalMessages 会累积进 state.Messages 和持久化,
	// 因此执行后必须把摘要那条 user 消息替换回原文版,避免污染历史。
	llmInput, summarized := PreprocessUserInput(input, DefaultPreprocessConfig)
	userMsgIndex := len(baseMessages) // result.FinalMessages 中 user 消息的位置
	messages := append(baseMessages, llm.Message{
		Role:    "user",
		Content: []llm.ContentBlock{{Type: "text", Text: llmInput}},
	})

	reporter := &LiveReporter{RunID: runID, Ch: eventCh, Workdir: workdir}

	inv := &agentpipe.Invocation{
		Provider:         provider,
		Registry:         registry,
		BrainID:          brainID,
		Messages:         messages,
		SystemPrompt:     systemPrompt,
		SystemBlocks:     systemBlocks,
		MaxTurns:         maxTurns,
		MaxDuration:      config.EffectiveRunMaxDuration(maxDuration, 5*time.Minute),
		RunID:            fmt.Sprintf("chat-%d-%s", turnIndex, time.Now().UTC().Format("150405")),
		UserUtterance:    input,
		ProjectID:        projectIDFromContext(ctx),
		TurnIndex:        turnIndex,
		Stream:           true,
		ChatCentralBrain: brainID == "central",
		ToolObserver:     reporter,
		StreamConsumer:   reporter,
		// 跨 turn 共享检测器:LoopDetector 检测重复 tool call、CacheBuilder 维护
		// prompt cache key、Sanitizer 清洗 tool 输出。每 turn 新建会清空状态,
		// 检测器永远跨 turn 看不到上一 turn 的工具调用,功能性失效。
		Sanitizer:    state.Sanitizer,
		LoopDetector: state.LoopDetector,
		CacheBuilder: state.CacheBuilder,
	}

	result, err := inv.Execute(ctx)
	// 把 FinalMessages 中送进 LLM 的摘要 user 消息替换回原文版,避免污染 state.Messages 与持久化。
	// 仅在确实做了长粘贴摘要时替换;只去填充音的情况(语义等价)不动,避免无谓开销。
	if summarized && result != nil && userMsgIndex < len(result.FinalMessages) {
		if result.FinalMessages[userMsgIndex].Role == "user" {
			result.FinalMessages[userMsgIndex] = llm.Message{
				Role:    "user",
				Content: []llm.ContentBlock{{Type: "text", Text: input}},
			}
		}
	}
	return result, err
}

// runChatPlanFlow 处理 chat 模式下的项目级需求(IntentProject)。
// 走 PlanRunner.Execute → PlanOrchestrator 七阶段闭环。
//
// 返回值的 *loop.RunResult 是为了和 simple 路径 API 一致。
// 项目执行结果(progress/reflection/lessons)透过 events / chat UI 单独呈现。
//
// 注意:PlanOrchestrator 内部会调多次 Delegate,每次 delegate 都是一个完整 Run,
// 这里返回的 RunResult 是最后一次 delegate 的 Run 摘要,不代表整个 project 的执行细节。
//
// PlanRunner 挂在 state 上(非包级 var),保证多 chat session 不会串状态。
func runChatPlanFlow(ctx context.Context, state *State, input, runID string, eventCh chan<- ChatEvent) (*loop.RunResult, error) {
	// 用 sync.Once 防止并发 turn 同时构造 PlanRunner 造成 race / 实例覆盖。
	state.PlanRunnerOnce.Do(func() {
		state.PlanRunner = agentpipe.NewPlanRunner(state.Orchestrator)
		// 把 AuditLogger 注入,启用 Replan 事件持久化(写 audit_events 表)。
		// 必须在 PlanRunner.ensurePlanOrch 调 SetAuditLogger 之前赋值,
		// 而 ensurePlanOrch 是 lazy 在 ExecuteWithInput 时调,这里赋值时机正确。
		state.PlanRunner.AuditLogger = state.AuditLogger
	})
	projectID := ""
	if state.CurrentProject != nil && !state.IsNoProject {
		projectID = state.CurrentProject.ID
	}

	// 通知 chat UI 正在执行项目级流程
	if eventCh != nil {
		select {
		case eventCh <- ChatEvent{RunID: runID, Type: "plan.started"}:
		default:
		}
	}

	// P0 权限击穿修复:把 chat 的权限上下文(PLAN/RESTRICTED/AcceptEdits + sandbox)
	// 作为 ExtraInstruction 注入到每个 SubTask,下游 sidecar 在 brain/execute
	// 层就看到约束,不会误用 fs_write/shell 等高权限工具。
	extraInstr := ""
	if len(state.Opts.System) > 0 {
		var parts []string
		for _, blk := range state.Opts.System {
			if blk.Text != "" {
				parts = append(parts, blk.Text)
			}
		}
		extraInstr = strings.Join(parts, "\n\n")
	}

	projResult, err := state.PlanRunner.ExecuteWithInput(ctx, agentpipe.PlanInput{
		ProjectID:        projectID,
		Goal:             input,
		ExtraInstruction: extraInstr,
	})
	// PlanRunner 当前不返回 RunResult — 这里构造一个 minimal RunResult 让上层 persist 流程能继续。
	// 真正的项目结果细节通过 ProjectExecutionResult 渲染(后续 chat UI 增强可补)。
	//
	// Budget 必须从 ProjectExecutionResult 各 SubTask 的 Usage 累加,
	// 否则持久化会写 turns=0/cost=0 的伪数据,污染 Dashboard / L2 学习。
	rr := &loop.RunResult{
		Run: &loop.Run{
			ID:      runID,
			BrainID: "central",
			State:   loop.StateCompleted,
			Budget:  agentpipe.AggregatePlanBudget(projResult),
		},
	}
	if err != nil {
		rr.Run.State = loop.StateFailed
	}
	if projResult != nil {
		// 把 reflection summary 作为 assistant reply,让 chat UI 能渲染
		summary := fmt.Sprintf("项目 %s 执行完成 (阶段: %s, 完成度: %.0f%%, 耗时: %s)",
			projResult.Progress.ProjectID,
			projResult.Progress.Phase,
			projResult.Progress.OverallPercent,
			projResult.Duration,
		)
		if projResult.Reflection != nil {
			summary += fmt.Sprintf("\n\n反思要点:%d 条\n推荐改进:%d 条",
				len(projResult.Reflection.Lessons),
				len(projResult.Reflection.Recommendations),
			)
		}
		rr.FinalMessages = []llm.Message{{
			Role: "assistant",
			Content: []llm.ContentBlock{{
				Type: "text",
				Text: summary,
			}},
		}}
	}
	return rr, err
}

func persistChatTurn(ctx context.Context, runtime *cliruntime.Runtime, runRec *cliruntime.RunRecord, providerName, input string, mode env.PermissionMode, workdir string, system []llm.SystemBlock, result *loop.RunResult, err error) {
	if runtime == nil || runRec == nil {
		return
	}
	if err != nil {
		errJSON, _ := json.Marshal(map[string]string{"error": err.Error()})
		status := "failed"
		if ctx.Err() == context.Canceled {
			status = "canceled"
		}
		_ = cliruntime.SaveRunCheckpointWithMessages(ctx, runtime, runRec, status, 0, runRec.ID+"-"+status, nil, system)
		_, _ = runtime.RunStore.Finish(runRec.ID, status, errJSON, err.Error())
		return
	}

	finalTurnIndex := 0
	finalTurnUUID := runRec.ID + "-completed"
	if n := len(result.Turns); n > 0 && result.Turns[n-1] != nil && result.Turns[n-1].Turn != nil {
		finalTurnIndex = result.Turns[n-1].Turn.Index
		if result.Turns[n-1].Turn.UUID != "" {
			finalTurnUUID = result.Turns[n-1].Turn.UUID
		}
	}
	_ = cliruntime.SaveRunCheckpointWithMessages(ctx, runtime, runRec, string(result.Run.State), finalTurnIndex, finalTurnUUID, result.FinalMessages, system)
	_ = deps.SaveRunUsage(ctx, runtime, runRec, providerName, "", result)

	replyText := extractAssistantReply(result.FinalMessages)
	planID, _ := deps.SaveRunPlan(ctx, runtime, runRec, map[string]interface{}{
		"chat_turn":       true,
		"run_id":          result.Run.ID,
		"store_run_id":    runRec.StoreRunID,
		"brain_id":        result.Run.BrainID,
		"prompt":          input,
		"state":           string(result.Run.State),
		"turns":           result.Run.Budget.UsedTurns,
		"llm_calls":       result.Run.Budget.UsedLLMCalls,
		"tool_calls":      result.Run.Budget.UsedToolCalls,
		"provider":        providerName,
		"permission_mode": string(mode),
		"workdir":         workdir,
	})
	summary, _ := json.Marshal(map[string]interface{}{
		"chat_turn":    true,
		"run_id":       runRec.ID,
		"store_run_id": runRec.StoreRunID,
		"brain_id":     result.Run.BrainID,
		"state":        string(result.Run.State),
		"turns":        result.Run.Budget.UsedTurns,
		"llm_calls":    result.Run.Budget.UsedLLMCalls,
		"tool_calls":   result.Run.Budget.UsedToolCalls,
		"elapsed_ms":   result.Run.Budget.ElapsedTime.Milliseconds(),
		"reply":        replyText,
		"provider":     providerName,
		"plan_id":      planID,
	})
	_, _ = runtime.RunStore.Finish(runRec.ID, string(result.Run.State), summary, "")
}

func extractAssistantReply(messages []llm.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "assistant" {
			return extractText(messages[i].Content)
		}
	}
	return ""
}

// projectIDContextKey 是 ProjectID 在 context.Value 中的 key。
type projectIDContextKey struct{}

// withProjectID 把 ProjectID 写入 ctx,供下游(runChatTurn / bridge.delegate)读取。
func withProjectID(ctx context.Context, projectID string) context.Context {
	if projectID == "" {
		return ctx
	}
	return context.WithValue(ctx, projectIDContextKey{}, projectID)
}

// projectIDFromContext 从 ctx 读取 ProjectID。无值返回 ""。
func projectIDFromContext(ctx context.Context) string {
	if v := ctx.Value(projectIDContextKey{}); v != nil {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// ProjectIDFromContext 暴露给 bridge 包的导出版本。
func ProjectIDFromContext(ctx context.Context) string {
	return projectIDFromContext(ctx)
}

// wrapForFallback 把项目级 prompt 包装成"逐步实现"指令。
//
// 用于 IntentProject → ErrPlanFallback 降级路径:用户原意是"做项目",
// 但 PlanRunner 解析失败,只能走 simple Invocation。直接传原文的话
// LLM 单 turn 容易只回"我建议..."而不真正开始做。包装成"逐步实现 +
// 先列计划再做"的 prompt 保住用户原意,Invocation 内部可以多 turn 推进。
func wrapForFallback(original string) string {
	return "请逐步实现以下需求,先列出实现计划再开始动手做(可以分多步,可以调用工具创建文件 / 编辑代码 / 验证):\n\n" + original
}

// persistChatTurnToProject 把本 turn 的 user + assistant 消息写入 project_conversations
// 并更新 projects.last_active_at。MACCS Wave 7+ 多项目持久化的核心写入点。
// 任意失败 silent(stderr 打印),不阻塞主流程。
//
// 价值过滤(避免垃圾对话污染项目记忆):
//   - 用户输入是取消/算了/byebye 等终止意图 → 跳过
//   - run 既无文件产出又无工具调用 + 用户输入极短(< 8 chars 且无中文实词)→ 跳过
//   - 用户已经做了 /project save 等显式保存动作不在这里管(那走另一条路径)
func persistChatTurnToProject(ctx context.Context, state *State, input string, result *loop.RunResult, runErr error) {
	if state == nil || state.IsNoProject || state.CurrentProject == nil {
		return
	}
	if state.ProjectStore == nil || state.ProjectsStore == nil {
		return
	}
	if !shouldPersistTurn(input, result, runErr) {
		return
	}
	pid := state.CurrentProject.ID

	// 写 user message
	userMsg := llm.Message{
		Role:    "user",
		Content: []llm.ContentBlock{{Type: "text", Text: input}},
	}
	toSave := []llm.Message{userMsg}

	// 写 assistant 回复(若有)
	if runErr == nil && result != nil && len(result.FinalMessages) > 0 {
		// 取最后一条 assistant 消息(往后追加 user 之后的所有 assistant 内容也行,
		// 但 chat 单 turn 通常只产出一条 assistant 终态)
		for i := len(result.FinalMessages) - 1; i >= 0; i-- {
			if result.FinalMessages[i].Role == "assistant" {
				toSave = append(toSave, result.FinalMessages[i])
				break
			}
		}
	}

	if err := state.ProjectStore.SaveMessages(ctx, pid, toSave); err != nil {
		fmt.Fprintf(os.Stderr, "brain chat: persist project messages: %v\n", err)
	}
	if err := state.ProjectsStore.UpdateLastActive(ctx, pid, time.Now()); err != nil {
		fmt.Fprintf(os.Stderr, "brain chat: update project last_active: %v\n", err)
	}
}

// cancelMarkers 是用户输入中表示"取消/算了/退出"的终止意图标记。
// 命中之一就视为本 turn 不值得持久化(用户已经放弃)。
var cancelMarkers = []string{
	"算了", "取消", "撤回", "不要了", "byebye", "bye bye", "退出",
	"nevermind", "never mind", "cancel", "forget it", "skip it", "abort",
}

// shouldPersistTurn 判断本 turn 是否值得写到项目记忆。
// 三个过滤层:
//  1. 用户取消意图 → 不写
//  2. 既无 tool_use 又无 assistant 文本(运行直接失败,留下垃圾)→ 不写
//  3. 单字符 / 短噪音输入(s / n / yes 等)+ 0 工具 → 不写
func shouldPersistTurn(input string, result *loop.RunResult, runErr error) bool {
	trimmed := strings.TrimSpace(input)
	low := strings.ToLower(trimmed)

	// 1. 用户明确取消
	for _, m := range cancelMarkers {
		if strings.Contains(low, m) {
			return false
		}
	}

	// 2. run 失败 + 0 工具调用 + 0 assistant 输出 = 完全垃圾
	if runErr != nil && (result == nil || len(result.FinalMessages) <= 1) {
		return false
	}

	// 3. 短噪音输入(单字 s/n/y 等 picker 选项)+ 0 工具 → 不持久化
	// 8 chars 阈值兼顾英文短语和中文(中文一般 1 字 3 字节)
	if len(trimmed) < 8 {
		toolCalls := 0
		if result != nil {
			toolCalls = result.Run.Budget.UsedToolCalls
		}
		if toolCalls == 0 {
			return false
		}
	}

	return true
}
