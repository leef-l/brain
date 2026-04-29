package chat

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"

	brain "github.com/leef-l/brain"
	"github.com/leef-l/brain/cmd/brain/config"
	"github.com/leef-l/brain/cmd/brain/env"
	"github.com/leef-l/brain/cmd/brain/term"
	brainerrors "github.com/leef-l/brain/sdk/errors"
	"github.com/leef-l/brain/sdk/agent"
	"github.com/leef-l/brain/sdk/cli"
	"github.com/leef-l/brain/sdk/kernel"
	"github.com/leef-l/brain/sdk/llm"
	"github.com/leef-l/brain/sdk/loop"
	"github.com/leef-l/brain/sdk/tool"
)

func RunChat(args []string) int {
	fs := flag.NewFlagSet("chat", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	brainID := fs.String("brain", "central", "brain identifier (central, code, verifier)")
	maxTurns := fs.Int("max-turns", 40, "max turns per user message")
	providerFlag := fs.String("provider", "", "LLM provider/profile name, or mock")
	apiKey := fs.String("api-key", "", "API key (overrides env and config)")
	baseURL := fs.String("base-url", "", "API base URL")
	modelFlag := fs.String("model", "", "model name (overrides config)")
	modelConfigJSON := fs.String("model-config-json", "", "structured model config JSON override")
	modeFlag := fs.String("mode", "", "permission mode: plan, default, accept-edits, auto, restricted, bypass-permissions")
	workDir := fs.String("workdir", "", "working directory sandbox (default: current directory)")
	filePolicyJSON := fs.String("file-policy-json", "", "fine-grained file mutation policy JSON")
	timeoutFlag := fs.String("timeout", "", "per-turn timeout (e.g. 5m, 30m, 0 to disable)")
	if err := fs.Parse(args); err != nil {
		return cli.ExitUsage
	}

	cfg, cfgErr := deps.LoadConfig()
	config.ApplyDiagnosticEnv(cfg)
	modelInput, err := deps.ParseModelConfigJSON(*modelConfigJSON)
	if err != nil {
		fmt.Fprintf(os.Stderr, "brain chat: %v\n", err)
		return cli.ExitUsage
	}
	filePolicyInput, err := deps.ParseFilePolicyJSON(*filePolicyJSON)
	if err != nil {
		fmt.Fprintf(os.Stderr, "brain chat: %v\n", err)
		return cli.ExitUsage
	}
	filePolicyInput = deps.ResolveFilePolicyInput(cfg, filePolicyInput)

	explicitProviderInput := deps.HasModelConfigOverrides(modelInput) || strings.TrimSpace(*apiKey) != "" || strings.TrimSpace(*baseURL) != "" || strings.TrimSpace(*modelFlag) != ""
	if cfg == nil && !deps.WantsMockProvider(*providerFlag, modelInput) && !explicitProviderInput && os.Getenv("ANTHROPIC_API_KEY") == "" {
		if cfgErr != nil {
			fmt.Fprintf(os.Stderr, "brain chat: %v\n", cfgErr)
		} else {
			deps.PrintConfigSetupGuide()
		}
		return cli.ExitFailed
	}

	mode, err := deps.ResolvePermissionMode(*modeFlag, cfg, true)
	if err != nil {
		fmt.Fprintf(os.Stderr, "brain chat: %v\n", err)
		return cli.ExitUsage
	}
	timeout, err := config.ResolveRunTimeoutWithConfig(cfg, *timeoutFlag, 5*time.Minute)
	if err != nil {
		fmt.Fprintf(os.Stderr, "brain chat: %v\n", err)
		return cli.ExitUsage
	}

	chatRuntime, _ := deps.NewDefaultCLIRuntime(*brainID)

	var pool *kernel.ProcessBrainPool
	var orch *kernel.Orchestrator
	if *brainID == "central" && !deps.WantsMockProvider(*providerFlag, modelInput) {
		pool = deps.BuildBrainPool(cfg)
		if pool != nil {
			llmProxy := &kernel.LLMProxy{
				ProviderFactory: func(kind agent.Kind) llm.Provider {
					session, err := deps.OpenConfiguredProvider(cfg, string(kind), modelInput, *providerFlag, *apiKey, *baseURL, *modelFlag)
					if err != nil {
						return nil
					}
					return session.Provider
				},
			}
			var learner *kernel.LearningEngine
			if chatRuntime != nil && chatRuntime.Stores != nil && chatRuntime.Stores.LearningStore != nil {
				learner = kernel.NewLearningEngineWithStore(chatRuntime.Stores.LearningStore)
				if err := learner.Load(context.Background()); err != nil {
					fmt.Fprintf(os.Stderr, "brain chat: warning: load learning data: %v\n", err)
				}
			} else {
				learner = kernel.NewLearningEngine()
			}
			defer func() {
				_ = learner.Save(context.Background())
			}()
			// 上下文引擎：注入 LLM Summarizer
			ctxEngine := kernel.NewDefaultContextEngine()
			if session, err := deps.OpenConfiguredProvider(cfg, "central", modelInput, *providerFlag, *apiKey, *baseURL, *modelFlag); err == nil {
				ctxEngine.Summarizer = session.Provider
				ctxEngine.SummaryModel = session.Model
			}

			leaseManager := kernel.NewMemLeaseManager()
			orch = kernel.NewOrchestratorWithPool(pool, &kernel.ProcessRunner{BinResolver: deps.DefaultBinResolver()}, llmProxy, deps.DefaultBinResolver(), kernel.OrchestratorConfig{},
				kernel.WithSemanticApprover(&kernel.DefaultSemanticApprover{}),
				kernel.WithLearningEngine(learner),
				kernel.WithContextEngine(ctxEngine),
				kernel.WithLeaseManager(leaseManager),
			)
			// 专家 sidecar 的 brain/progress 事件直接打到屏幕,让用户能
			// 实时看到 subtask 里每个 tool 的 start/end,避免 central.delegate
			// 长时间静默的尴尬。
			orch.SetBrainProgressHandler(func(ctx context.Context, callerKind string, params json.RawMessage) {
				var ev struct {
					Kind     string `json:"kind"`
					ToolName string `json:"tool_name"`
					Args     string `json:"args"`
					Detail   string `json:"detail"`
					OK       bool   `json:"ok"`
				}
				if err := json.Unmarshal(params, &ev); err != nil {
					return
				}
				switch ev.Kind {
				case "tool_start":
					fmt.Printf("\033[2m      [%s] %s.Run: %s\033[0m\n", callerKind, callerKind, trimForDisplay(ev.ToolName+" "+ev.Args, 140))
				case "tool_end":
					if ev.OK {
						fmt.Printf("\033[2m      [%s] %s.Done: %s %s\033[0m\n", callerKind, callerKind, ev.ToolName, trimForDisplay(ev.Detail, 140))
					} else {
						fmt.Printf("\033[31m      [%s] %s.Fail: %s — %s\033[0m\n", callerKind, callerKind, ev.ToolName, trimForDisplay(ev.Detail, 140))
					}
				}
			})

			// 把 sidecar 反向 RPC 的人工求助请求桥接到 tool 包注入的协调器
			// (见 RunChat 末尾的 tool.SetHumanTakeoverCoordinator):
			// sidecar -> kernel reverse RPC -> 这里 -> chat coord -> /resume 解锁。
			orch.SetHumanTakeoverHandler(func(ctx context.Context, callerKind string, params json.RawMessage) (interface{}, error) {
				coord := tool.CurrentHumanTakeoverCoordinator()
				if coord == nil {
					return tool.HumanTakeoverResponse{
						Outcome: tool.HumanOutcomeAborted,
						Note:    "no human coordinator configured in chat host",
					}, nil
				}
				var req tool.HumanTakeoverRequest
				if err := json.Unmarshal(params, &req); err != nil {
					return nil, fmt.Errorf("unmarshal HumanTakeoverRequest: %w", err)
				}
				if req.BrainKind == "" {
					req.BrainKind = callerKind
				}
				return coord.RequestTakeover(ctx, req), nil
			})
		}
	}
	defer func() {
		if pool != nil {
			_ = pool.Shutdown(context.Background())
		}
	}()

	e := deps.NewExecutionEnv(*workDir, mode, cfg, nil, true)
	if err := deps.ApplyFilePolicy(e, filePolicyInput); err != nil {
		fmt.Fprintf(os.Stderr, "brain chat: %v\n", err)
		return cli.ExitUsage
	}
	if mode == env.ModeRestricted && e.FilePolicy == nil {
		fmt.Fprintln(os.Stderr, "brain chat: restricted mode requires file_policy (config or --file-policy-json)")
		return cli.ExitUsage
	}

	providerSession := deps.OpenMockProvider("hello from mock provider")
	if !deps.WantsMockProvider(*providerFlag, modelInput) {
		providerSession, err = deps.OpenConfiguredProvider(cfg, *brainID, modelInput, *providerFlag, *apiKey, *baseURL, *modelFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "brain chat: %v\n", err)
			fmt.Fprintln(os.Stderr, "  Set via: brain config set providers.<name>.api_key <key>")
			return cli.ExitFailed
		}
	}

	kb := term.LoadKeybindings()

	humanCoord := NewChatHumanCoordinator()
	tool.SetHumanTakeoverCoordinator(humanCoord)

	state := &State{
		Cfg:          cfg,
		BrainID:      *brainID,
		Env:          e,
		KB:           kb,
		Sandbox:      e.Sandbox,
		SandboxCfg:   e.SandboxCfg,
		Orchestrator: orch,
		ApprovalCh:   make(chan env.ApprovalRequest),
		RunTimeout:   timeout,
		HumanCoord:   humanCoord,
	}
	state.SwitchMode(mode)

	// 加载该 workdir 的历史 conversation
	if conv, err := LoadConversation(e.Workdir); err != nil {
		fmt.Fprintf(os.Stderr, "brain chat: load conversation: %v\n", err)
	} else if conv != nil {
		ApplyConversationToState(conv, state)
		fmt.Printf("  \033[2mLoaded %d messages from previous session\033[0m\n", len(state.Messages))
	}

	fmt.Println()
	fmt.Printf("  \033[1mBrain Chat v%s\033[0m\n", brain.CLIVersion)
	fmt.Printf("  \033[2mProvider:\033[0m %s / %s\n", providerSession.Name, providerSession.Model)
	fmt.Printf("  \033[2mBrain:\033[0m    %s\n", *brainID)
	fmt.Printf("  \033[2mMode:\033[0m     %s\n", mode.StyledLabel())
	fmt.Printf("  \033[2mWorkdir:\033[0m  %s\n", e.Workdir)
	fmt.Printf("  \033[2mKeys:\033[0m     Esc cancel latest, Ctrl+C cancel all, Ctrl+D quit, Ctrl+W mode, /help\n")
	if orch != nil {
		fmt.Printf("  \033[2mDelegates:\033[0m %v\n", orch.AvailableKinds())
	}
	fmt.Println()

	if !deps.WantsMockProvider(*providerFlag, modelInput) {
		if diag := RunStartupDiagnostics(providerSession, cfg); diag != "" {
			fmt.Println(diag)
		}
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)

	restore, rawErr := term.EnableRawInput()
	if rawErr != nil {
		exitCode := runChatLineMode(state, providerSession.Provider, brainID, maxTurns, sigCh)
		_ = SaveConversation(SnapshotStateToConversation(state, e.Workdir))
		return exitCode
	}
	defer restore()
	defer func() {
		_ = SaveConversation(SnapshotStateToConversation(state, e.Workdir))
	}()

	eventCh := make(chan ChatEvent, 256)
	stdinCh, stdinErrCh := startAsyncStdinReader()
	progressTicker := time.NewTicker(250 * time.Millisecond)
	defer progressTicker.Stop()

	activities := make(map[string]*Activity)
	lastProgressSecond := make(map[string]int64)
	session := term.NewLineReadSession(kb, 0)
	session.History = LoadHistory()
	session.HistoryIndex = len(session.History)
	session.MuteEcho = false

	promptHeaderLines := func() []string {
		currentInput := strings.TrimSpace(session.Editor().String())
		completions := SlashCompletionLines(currentInput)
		acts := make([]*Activity, 0, len(activities))
		for _, a := range activities {
			acts = append(acts, a)
		}
		return BuildPromptHeaderLines(acts, state.QueueDisplayLines(), state.AnyRunning(), completions)
	}
	RenderPromptFrame(session, state.Mode, providerSession.Name, providerSession.Model, e.Workdir, promptHeaderLines(), state.AnyRunning())

	// launchRun 启动一个 run 并创建对应的 Activity。
	launchRun := func(input string) string {
		id := StartChatRun(state, providerSession.Provider, *brainID, *maxTurns, input, eventCh)
		act := &Activity{RunID: id, Input: input}
		act.Start()
		activities[id] = act
		lastProgressSecond[id] = -1
		DetachPromptFrame(session)
		PrintUserMessage(input)
		ResetStreamClock()
		session.MuteEcho = true
		return id
	}

	// handleRunResult 处理单个 run 的完成事件。
	handleRunResult := func(id string, rr RunResult) {
		act, ok := activities[id]
		if ok {
			act.Stop()
			delete(activities, id)
			delete(lastProgressSecond, id)
		}
		state.RemoveRun(id)

		DetachPromptFrame(session)
		defer RenderPromptFrame(session, state.Mode, providerSession.Name, providerSession.Model, e.Workdir, promptHeaderLines(), state.AnyRunning())
		defer func() {
			if !state.AnyRunning() && state.PlanResumeAfterRun {
				state.PlanResumeAfterRun = false
				state.SwitchMode(env.ModePlan)
				fmt.Println("  \033[2m(returned to plan mode)\033[0m")
				fmt.Println()
			}
		}()

		if rr.Canceled {
			fmt.Printf("  \033[1;33m! Cancelled [%s]\033[0m\n", id)
			fmt.Println()
			return
		}
		if rr.Err != nil {
			fmt.Fprintf(os.Stderr, "\033[1;31m! Error [%s]: %s\033[0m\n\n", id, formatRunError(rr.Err))
			return
		}

		if rr.Result != nil && rr.Result.Run.State == loop.StateFailed {
			errMsg := LastTurnError(rr.Result)
			if errMsg == "" {
				errMsg = "unexpected error: run failed"
			}
			fmt.Fprintf(os.Stderr, "\033[1;31m! Error [%s]: %s\033[0m\n\n", id, formatRunErrorMsg(errMsg, rr.Result))
			return
		}

		// 追加新增消息到 state.Messages（不覆盖，避免冲掉其他并行 run 的结果）
		if rr.Result != nil {
			if rr.BaseMsgCount < len(rr.Result.FinalMessages) {
				newMsgs := rr.Result.FinalMessages[rr.BaseMsgCount:]
				state.Messages = append(state.Messages, newMsgs...)
			}
		}

		replyText := rr.ReplyText
		if act != nil && strings.TrimSpace(replyText) == "" {
			replyText = strings.TrimSpace(act.Content.String())
		}
		if strings.TrimSpace(replyText) == "" {
			if rr.Result != nil {
				replyText = BuildToolCallSummary(rr.Result.FinalMessages)
			}
		}

		if shouldPrintAssistantReply(act.Content.String(), replyText) {
			PrintAssistantMessage(replyText)
		}

		if rr.Result != nil {
			elapsed := rr.Result.Run.Budget.ElapsedTime.Milliseconds()
			unit := "ms"
			val := elapsed
			if elapsed >= 1000 {
				unit = "s"
				val = elapsed / 1000
			}
			fmt.Printf("\033[2m[%s turns:%d llm:%d tools:%d %d%s]\033[0m\n\n",
				id,
				rr.Result.Run.Budget.UsedTurns,
				rr.Result.Run.Budget.UsedLLMCalls,
				rr.Result.Run.Budget.UsedToolCalls,
				val, unit)
		}

		if state.Mode != env.ModeAuto && shouldShowResponseSelector(state.BrainID, replyText) {
			outcome := showResponseSelector(state.Mode, stdinCh, stdinErrCh)
			if outcome.followUp != "" {
				if outcome.planProceed && state.Mode == env.ModePlan {
					state.SwitchMode(env.ModeAcceptEdits)
					state.PlanResumeAfterRun = true
				}
				launchRun(outcome.followUp)
			}
		}
	}

	for {
		// 队列中的消息直接启动，不等当前 run 完成（支持并行）
		for {
			queued := state.Dequeue()
			if queued == "" {
				break
			}
			launchRun(queued)
		}

		select {
		case <-sigCh:
			continue

		case ev := <-humanCoord.Events():
			running := state.AnyRunning()
			if !running {
				DetachPromptFrame(session)
			}
			switch ev.Kind {
			case "requested":
				PrintRequested(ev.Request)
			case "resolved":
				PrintResolved(ev.Response)
			}
			if !running {
				RenderPromptFrame(session, state.Mode, providerSession.Name, providerSession.Model, e.Workdir, promptHeaderLines(), running)
			}
			continue

		case req := <-state.ApprovalCh:
			HandleApprovalRequest(state, session, kb, state.Mode, providerSession.Name, providerSession.Model, e.Workdir, promptHeaderLines(), state.AnyRunning(), req, stdinCh, stdinErrCh)
			continue

		case ev := <-eventCh:
			switch ev.Type {
			case "progress":
				if ev.Progress != nil {
					StreamProgressEvent(*ev.Progress)
					if len(ev.Progress.PreviewLines) > 0 {
						PrintDiffPreviewBlock(ev.Progress.PreviewLines)
					}
					if act, ok := activities[ev.RunID]; ok {
						act.Apply(*ev.Progress)
					}
				}
			case "result":
				if ev.Result != nil {
					handleRunResult(ev.RunID, *ev.Result)
				}
				if !state.AnyRunning() {
					session.MuteEcho = false
				}
			}
			continue

		case <-progressTicker.C:
			for id, act := range activities {
				if act.Running() {
					sec := int64(time.Since(act.StartedAt) / time.Second)
					if sec != lastProgressSecond[id] {
						lastProgressSecond[id] = sec
						if sec > 0 && sec%5 == 0 {
							fmt.Printf("\033[2m  [%s] %s\033[0m\n", id, act.StatusLine())
						}
					}
				}
			}
			continue

		case readErr := <-stdinErrCh:
			DetachPromptFrame(session)
			if readErr != nil && readErr != io.EOF {
				fmt.Fprintf(os.Stderr, "brain chat: %v\n", readErr)
				return cli.ExitFailed
			}
			fmt.Println("Bye!")
			return cli.ExitOK

		case data := <-stdinCh:
			line, action, done, err := session.Consume(data)
			if err != nil {
				DetachPromptFrame(session)
				fmt.Fprintf(os.Stderr, "brain chat: %v\n", err)
				return cli.ExitFailed
			}
			if !done {
				RerenderPromptFrame(session, state.Mode, providerSession.Name, providerSession.Model, e.Workdir, promptHeaderLines(), state.AnyRunning())
				continue
			}

			switch action {
			case term.ActionQuit:
				DetachPromptFrame(session)
				fmt.Println("Bye!")
				return cli.ExitOK

			case term.ActionEscape:
				input := strings.TrimSpace(line)
				if state.AnyRunning() {
					if input == "" {
						latestID := state.LatestRunID()
						if latestID != "" {
							state.CancelRun(latestID)
							if act, ok := activities[latestID]; ok {
								act.Stop()
							}
						}
						resetPromptInput(session)
						DetachPromptFrame(session)
						fmt.Println("  \033[1;33m! Cancelled latest\033[0m")
						fmt.Println()
						if !state.AnyRunning() {
							session.MuteEcho = false
						}
						RenderPromptFrame(session, state.Mode, providerSession.Name, providerSession.Model, e.Workdir, promptHeaderLines(), state.AnyRunning())
					} else {
						resetPromptInput(session)
						RerenderPromptFrame(session, state.Mode, providerSession.Name, providerSession.Model, e.Workdir, promptHeaderLines(), state.AnyRunning())
					}
				} else {
					if input != "" {
						resetPromptInput(session)
						RerenderPromptFrame(session, state.Mode, providerSession.Name, providerSession.Model, e.Workdir, promptHeaderLines(), state.AnyRunning())
					}
				}
				continue

			case term.ActionCancel:
				input := strings.TrimSpace(line)
				resetPromptInput(session)
				if state.AnyRunning() {
					state.CancelAllRuns()
					for id, act := range activities {
						_ = id
						act.Stop()
					}
					state.ClearQueue()
					DetachPromptFrame(session)
					fmt.Println("  \033[1;33m! Cancelled all\033[0m")
					fmt.Println()
					session.MuteEcho = false
					RenderPromptFrame(session, state.Mode, providerSession.Name, providerSession.Model, e.Workdir, promptHeaderLines(), state.AnyRunning())
				} else if input != "" {
					RerenderPromptFrame(session, state.Mode, providerSession.Name, providerSession.Model, e.Workdir, promptHeaderLines(), state.AnyRunning())
				}
				continue

			case term.ActionCycle:
				nextMode := env.CycleMode(state.Mode)
				state.SwitchMode(nextMode)
				RerenderPromptFrame(session, state.Mode, providerSession.Name, providerSession.Model, e.Workdir, promptHeaderLines(), state.AnyRunning())
				continue

			case term.ActionEnter, term.ActionQueue:
				input := strings.TrimSpace(line)
				resetPromptInput(session)
				if input == "" {
					RerenderPromptFrame(session, state.Mode, providerSession.Name, providerSession.Model, e.Workdir, promptHeaderLines(), state.AnyRunning())
					continue
				}
				session.AddHistory(input)
				AppendHistory(input)

				// 消息路由：@run-X msg → 纠正指定 run
				if strings.HasPrefix(input, "@run-") {
					parts := strings.SplitN(input, " ", 2)
					targetID := strings.TrimSpace(parts[0])
					var msg string
					if len(parts) > 1 {
						msg = strings.TrimSpace(parts[1])
					}
					if msg != "" {
						// 取消旧 run 并启动新 run（继承上下文）
						state.CancelRun(targetID)
						if act, ok := activities[targetID]; ok {
							act.Stop()
						}
						launchRun(msg)
						continue
					}
				}

				if strings.HasPrefix(input, "/") {
					DetachPromptFrame(session)
					handled, shouldQuit := HandleSlashCommand(input, state)
					if shouldQuit {
						fmt.Println("Bye!")
						return cli.ExitOK
					}
					if handled {
						RenderPromptFrame(session, state.Mode, providerSession.Name, providerSession.Model, e.Workdir, promptHeaderLines(), state.AnyRunning())
						continue
					}
				}

				// 任何消息都直接启动新 run（并行模式）
				launchRun(input)
			}
		}
	}
}

// formatRunError 把 budget/loop 等技术错误翻译成人话 + 恢复建议。
func formatRunError(err error) string {
	if err == nil {
		return ""
	}
	var be *brainerrors.BrainError
	if !errors.As(err, &be) {
		return err.Error()
	}
	switch be.ErrorCode {
	case brainerrors.CodeBudgetTurnsExhausted:
		return "任务步骤数达到上限（提示：重启 brain 时加 --max-turns 60 可放宽限制）"
	case brainerrors.CodeBudgetCostExhausted:
		return "费用达到上限"
	case brainerrors.CodeBudgetToolCallsExhausted:
		return "工具调用次数达到上限"
	case brainerrors.CodeBudgetLLMCallsExhausted:
		return "LLM 调用次数达到上限"
	case brainerrors.CodeBudgetTimeoutExhausted:
		return "任务执行超时"
	case brainerrors.CodeAgentLoopDetected:
		return "检测到循环（AI 在重复相同操作）"
	default:
		return be.Message
	}
}

// formatRunErrorMsg 根据 TurnResult 中的原始错误消息做友好化。
func formatRunErrorMsg(raw string, result *loop.RunResult) string {
	if strings.Contains(raw, "budget.turns_exhausted") {
		used, max := "", ""
		if result != nil {
			used = fmt.Sprintf("%d", result.Run.Budget.UsedTurns)
			max = fmt.Sprintf("%d", result.Run.Budget.MaxTurns)
		}
		msg := "任务步骤数达到上限"
		if used != "" && max != "" {
			msg += fmt.Sprintf("（已用 %s/%s steps）", used, max)
		}
		msg += "；提示：加 --max-turns 60 可放宽限制"
		return msg
	}
	if strings.Contains(raw, "budget.cost_exhausted") {
		return "费用达到上限"
	}
	if strings.Contains(raw, "budget.timeout_exhausted") {
		return "任务执行超时"
	}
	return raw
}

func resetPromptInput(session *term.LineReadSession) {
	session.Pending = nil
	session.LeaveHistoryBrowse()
	session.Ed.Runes = nil
	session.Ed.Pos = 0
	// 重置行占用计数:新的 prompt 行从 0 行残留开始,下次 RedrawFull
	// 不会再去清理上一条输入留下的"多行残影"(那些已经不在新 prompt
	// 附近了,清了会破坏历史输出)。
	session.Ed.LastEndRow = 0
	session.Ed.LastCursorRow = 0
}

func startAsyncStdinReader() (<-chan []byte, <-chan error) {
	dataCh := make(chan []byte, 64)
	errCh := make(chan error, 1)

	go func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintf(os.Stderr, "brain chat: stdin reader panic: %v\n", r)
				errCh <- fmt.Errorf("stdin reader panic: %v", r)
				close(dataCh)
			}
		}()
		buf := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				for {
					ready, _ := term.WaitForStdinReady(1 * time.Millisecond)
					if !ready {
						break
					}
					extra := make([]byte, 4096)
					m, _ := os.Stdin.Read(extra)
					if m == 0 {
						break
					}
					buf = append(buf[:n], extra[:m]...)
					n += m
				}
				chunk := make([]byte, n)
				copy(chunk, buf[:n])
				dataCh <- chunk
			}
			if err != nil {
				errCh <- err
				close(dataCh)
				return
			}
			if n == 0 {
				errCh <- nil
				close(dataCh)
				return
			}
		}
	}()

	return dataCh, errCh
}

func runChatLineMode(state *State, provider llm.Provider, brainID *string, maxTurns *int, sigCh chan os.Signal) int {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	for {
		PrintPrompt(state.Mode)

		inputCh := make(chan string, 1)
		errCh := make(chan error, 1)
		go func() {
			if scanner.Scan() {
				inputCh <- scanner.Text()
			} else {
				errCh <- scanner.Err()
			}
		}()

		var input string
		select {
		case <-sigCh:
			fmt.Println("\nBye!")
			return cli.ExitOK
		case err := <-errCh:
			if err != nil {
				fmt.Fprintf(os.Stderr, "\nread error: %v\n", err)
			}
			fmt.Println("\nBye!")
			return cli.ExitOK
		case input = <-inputCh:
		}

		input = strings.TrimSpace(input)
		if input == "" {
			continue
		}

		if strings.HasPrefix(input, "/") {
			handled, shouldQuit := HandleSlashCommand(input, state)
			if shouldQuit {
				fmt.Println("Bye!")
				return cli.ExitOK
			}
			if handled {
				continue
			}
		}

		PrintUserMessage(input)

		state.TurnCount++
		baseMessages := make([]llm.Message, len(state.Messages))
		copy(baseMessages, state.Messages)

		ctx, cancel := config.WithOptionalTimeout(context.Background(), state.RunTimeout)
		result, err := runChatTurn(ctx, provider, state.Registry, state.Opts, *brainID, *maxTurns, state.TurnCount, baseMessages, input, state.Sandbox.Primary(), state.RunTimeout, "", nil)
		canceled := ctx.Err() == context.Canceled
		cancel()

		if canceled {
			fmt.Println()
			fmt.Println("  \033[1;33m! Cancelled\033[0m")
			fmt.Println()
			continue
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "\033[1;31m! Error: %v\033[0m\n\n", err)
			continue
		}

		if result.Run.State == loop.StateFailed {
			if errMsg := LastTurnError(result); errMsg != "" {
				fmt.Fprintf(os.Stderr, "\033[1;31m! Error: %s\033[0m\n\n", errMsg)
				continue
			}
		}

		if len(baseMessages) < len(result.FinalMessages) {
			state.Messages = append(state.Messages, result.FinalMessages[len(baseMessages):]...)
		}
		replyText := extractAssistantReply(result.FinalMessages)
		if replyText != "" {
			PrintAssistantMessage(replyText)
		}

		elapsed := result.Run.Budget.ElapsedTime.Milliseconds()
		unit := "ms"
		val := elapsed
		if elapsed >= 1000 {
			unit = "s"
			val = elapsed / 1000
		}
		fmt.Printf("\033[2m[turns:%d llm:%d tools:%d %d%s]\033[0m\n\n",
			result.Run.Budget.UsedTurns,
			result.Run.Budget.UsedLLMCalls,
			result.Run.Budget.UsedToolCalls,
			val, unit)
	}
}
