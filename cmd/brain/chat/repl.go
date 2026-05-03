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
	"github.com/leef-l/brain/sdk/agent"
	"github.com/leef-l/brain/sdk/cli"
	brainerrors "github.com/leef-l/brain/sdk/errors"
	"github.com/leef-l/brain/sdk/events"
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
	// MACCS Wave 7+ 多项目管理 — chat 启动期 picker
	projectFlag := fs.String("project", "", "project name (find or create in current workdir)")
	newProjectFlag := fs.String("new-project", "", "force create a new project with this name")
	noProjectFlag := fs.Bool("no-project", false, "skip project picker, do not persist conversation")
	if err := fs.Parse(args); err != nil {
		return cli.ExitUsage
	}

	cfg, cfgErr := deps.LoadConfig()
	config.ApplyDiagnosticEnv(cfg)
	// 把 config.diagnostics.debug.* 同步到 sdk 各包的全局开关。
	// 用户可通过 ~/.brain/config.json 持久化打开调试日志,而不需要每次设环境变量。
	loop.DebugRunner = cfg.DebugRunnerEnabled()
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

	// 先解析 sidecarWorkdir,后建 pool 时统一 SetRunnerWorkdir。
	// helpers.go BuildBrainPool 用 os.Getwd 兜底,但对 chat 来说更精确的是 -workdir flag。
	sidecarWorkdir := *workDir
	if sidecarWorkdir == "" {
		if cwd, err := os.Getwd(); err == nil {
			sidecarWorkdir = cwd
		}
	}

	var pool *kernel.ProcessBrainPool
	var orch *kernel.Orchestrator
	if *brainID == "central" && !deps.WantsMockProvider(*providerFlag, modelInput) {
		pool = deps.BuildBrainPool(cfg)
		if pool != nil {
			pool.SetRunnerWorkdir(sidecarWorkdir)
		}
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

			// MACCS 学习闭环（关键）：把这个持久化的 learner 注入到 bridge.delegate 的
			// ComplexityEstimator，让单步派发的 turn 估算可以查历史数据。否则
			// estimator 用 nil learner，永远走 heuristic，等于学习数据被丢弃。
			//
			// 通过 RegisterEstimatorInjector 让外层（chat_aliases.go）拿到 learner 后
			// 调 bridge.SetDelegateEstimator —— chat 包不直接 import bridge。
			InjectEstimatorWithLearner(learner)

			// 上下文引擎：注入 LLM Summarizer + ProjectStore(MACCS Wave 7+)
			//
			// ProjectStore 注入后,Assemble 在 req.ProjectID 非空时自动调
			// LoadMessages 加载项目历史,实现"中央大脑跨会话保存整个项目对话"
			// (设计意图见 sdk/persistence/project_store.go)。
			// 项目实际选择在 picker 之后,这里只准备 store 引用。
			ctxEngine := kernel.NewDefaultContextEngine()
			if session, err := deps.OpenConfiguredProvider(cfg, "central", modelInput, *providerFlag, *apiKey, *baseURL, *modelFlag); err == nil {
				ctxEngine.Summarizer = session.Provider
				ctxEngine.SummaryModel = session.Model
			}
			if chatRuntime != nil && chatRuntime.Stores != nil && chatRuntime.Stores.ProjectStore != nil {
				ctxEngine.ProjectStore = chatRuntime.Stores.ProjectStore
			}

			leaseManager := kernel.NewMemLeaseManager()
			// Workdir 关键:sidecarWorkdir 已在 chat 入口处解析(-workdir flag,空则 os.Getwd)
			// 并通过 pool.SetRunnerWorkdir 注入到 pool 内部 runner。
			// 这里同样传给 NewOrchestratorWithPool 的 ProcessRunner,保证两条路径一致。
			orch = kernel.NewOrchestratorWithPool(pool, &kernel.ProcessRunner{BinResolver: deps.DefaultBinResolver(), Workdir: sidecarWorkdir}, llmProxy, deps.DefaultBinResolver(), kernel.OrchestratorConfig{},
				kernel.WithSemanticApprover(&kernel.DefaultSemanticApprover{}),
				kernel.WithLearningEngine(learner),
				kernel.WithContextEngine(ctxEngine),
				kernel.WithLeaseManager(leaseManager),
			)
			// 注入 EventBus,启用 Replan 路径(PlanOrchestrator 通过 Subscribe 监听
			// EventReplanRequested,chat REPL.dispatchUserInput 通过 Publish 发布)。
			// chat 模式之前没用 EventBus,Replan 之后必须有,否则分诊路由后事件无消费者。
			orch.EventBus = events.NewMemEventBus()
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
				// 默认静默：sub-tool 的 Run/Done/Fail 只通过 spinner 行 + todo 框反映；
				// /verbose on 时打回原本的三连，便于排障。
				// 失败信息一律落到 ~/.brain/logs/tool-failures.log（由 sdk/tool 装饰器写）。
				if VerboseEnabled() {
					switch ev.Kind {
					case "tool_start":
						fmt.Printf("\r\033[2K\033[2m      [%s] %s.Run: %s\033[0m\n", callerKind, callerKind, trimForDisplay(ev.ToolName+" "+ev.Args, 140))
					case "tool_end":
						if ev.OK {
							fmt.Printf("\r\033[2K\033[2m      [%s] %s.Done: %s %s\033[0m\n", callerKind, callerKind, ev.ToolName, trimForDisplay(ev.Detail, 140))
						} else {
							fmt.Printf("\r\033[2K\033[31m      [%s] %s.Fail: %s — %s\033[0m\n", callerKind, callerKind, ev.ToolName, trimForDisplay(ev.Detail, 140))
						}
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

	// 把 host 的执行边界(workdir + file_policy + allow_commands + allow_delegate)
	// 注入 Orchestrator,Delegate 入口会自动填到没传 Execution 的 DelegateRequest,
	// 让 PlanOrchestrator / ReviewLoop / chat plan 等所有派发路径都遵守同一份
	// 权限边界,堵 file_policy 越过漏洞(文档 27 §6.2 MUST)。
	if orch != nil {
		orch.SetDefaultExecution(e.ExecutionSpec())
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

	// RelevanceClassifier 用 chat 当前 Provider 做 LLM 兜底分类。
	// brain-v3 已支持「不同 brain 不同模型」(LLMProxy.ModelForKind),
	// 这里直接复用 chat session 的默认 provider 不开独立配置。
	classifier := kernel.NewDefaultRelevanceClassifier()
	classifier.Provider = providerSession.Provider
	classifier.Model = providerSession.Model

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
		// 跨 turn 共享检测器 — 必须 chat session 内复用,
		// 否则每 turn 重建 → LoopDetector / CacheBuilder 永远空状态,功能失效。
		Sanitizer:    loop.NewMemSanitizer(),
		LoopDetector: loop.NewMemLoopDetector(),
		CacheBuilder: loop.NewMemCacheBuilder(),
		// Replan 路由分类器
		RelevanceClassifier: classifier,
	}
	state.SwitchMode(mode)

	// 注入持久化 stores(供 project_picker / executor 持久化对话用)
	if chatRuntime != nil && chatRuntime.Stores != nil {
		state.ProjectsStore = chatRuntime.Stores.ProjectsStore
		state.ProjectStore = chatRuntime.Stores.ProjectStore
		state.ProjectMemoryStore = chatRuntime.Stores.ProjectMemoryStore
		state.AuditLogger = chatRuntime.Stores.AuditLogger
	}
	state.CurrentWorkdir = e.Workdir

	// 加载该 workdir 的历史 conversation
	if conv, err := LoadConversation(e.Workdir); err != nil {
		fmt.Fprintf(os.Stderr, "brain chat: load conversation: %v\n", err)
	} else if conv != nil {
		ApplyConversationToState(conv, state)
		fmt.Printf("  \033[2mLoaded %d messages from previous session\033[0m\n", len(state.Messages))
	}

	fmt.Println()
	fmt.Printf("  \033[1mBrain Chat v%s\033[0m  \033[2m(commit %s, built %s)\033[0m\n",
		brain.CLIVersion, shortCommit(brain.BuildCommit), brain.BuildTime)
	fmt.Printf("  \033[2mProvider:\033[0m %s / %s\n", providerSession.Name, providerSession.Model)
	fmt.Printf("  \033[2mBrain:\033[0m    %s\n", *brainID)
	fmt.Printf("  \033[2mMode:\033[0m     %s\n", mode.StyledLabel())
	fmt.Printf("  \033[2mWorkdir:\033[0m  %s\n", e.Workdir)
	fmt.Printf("  \033[2mKeys:\033[0m     Esc cancel latest, Ctrl+C cancel all, Ctrl+D quit, Ctrl+W mode, /help\n")
	if orch != nil {
		fmt.Printf("  \033[2mDelegates:\033[0m %v\n", orch.AvailableKinds())
	}
	// MACCS Wave 7+ /project 命令存在性自检 — 启动就告诉用户这个命令可用,避免误以为没编进去
	fmt.Printf("  \033[2mProject cmds:\033[0m /project [list|new|switch|current|info|rename|delete|save|help]\n")
	fmt.Println()

	// MACCS Wave 7+ 项目选择器:启动时强制让用户选择项目或跳过持久化。
	// 持久化禁用(无 ProjectsStore)或 mock provider 时跳过。
	if state.ProjectsStore != nil && !deps.WantsMockProvider(*providerFlag, modelInput) {
		pick, err := PickProject(context.Background(), ProjectPickerOptions{
			Store:              state.ProjectsStore,
			Workdir:            e.Workdir,
			ExplicitProject:    *projectFlag,
			ExplicitNewProject: *newProjectFlag,
			NoProject:          *noProjectFlag,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "brain chat: project picker: %v\n", err)
			return cli.ExitFailed
		}
		if pick.Project != nil {
			fmt.Printf("  \033[2mProject:\033[0m  %s (id=%s)\n", pick.Project.Name, pick.Project.ID)
		}
		// 统一调 ApplyProjectChange,处理 ContextEngine 升级 + 历史加载 + 状态字段。
		// 这是 MACCS Wave 7+ 持久化闭环的关键 helper,所有 /project 命令切换都用它。
		ApplyProjectChange(state, pick.Project, true)
		if pick.IsNoProject {
			state.IsNoProject = true // ApplyProjectChange 已处理,这里再保险一下
		}
		fmt.Println()
	} else {
		// 没有 store 或 mock 模式 → 直接无项目模式
		state.IsNoProject = true
	}

	if !deps.WantsMockProvider(*providerFlag, modelInput) {
		if diag := RunStartupDiagnostics(providerSession, cfg); diag != "" {
			fmt.Println(diag)
		}
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)

	restore, rawErr := term.EnableRawInput()
	if rawErr != nil {
		defer state.Close()
		exitCode := runChatLineMode(state, providerSession.Provider, brainID, maxTurns, sigCh)
		_ = SaveConversation(SnapshotStateToConversation(state, e.Workdir))
		return exitCode
	}
	defer restore()
	defer state.Close()
	defer func() {
		_ = SaveConversation(SnapshotStateToConversation(state, e.Workdir))
	}()

	eventCh := make(chan ChatEvent, 256)
	stdinCh, stdinErrCh := startAsyncStdinReader()
	progressTicker := time.NewTicker(250 * time.Millisecond)
	defer progressTicker.Stop()

	// LLM 流式 token 最近一次到达时间。spinner 重绘会清当前行，会把流式 token
	// 抹掉，所以最近 800ms 有 token 就暂停 spinner 重绘。
	var lastContentAt time.Time
	// frameDetached 跟踪 prompt frame 是否处于 detached（被清掉、未重画）状态。
	// 流式 ProgressContent 期间一次 detach 后保持 detached，避免每个 token 都
	// 重画一整套 spinner+queue+input frame（之前的行为：每 token 都把整段历史
	// 重打一遍，导致终端出现"<token><完整 frame>"反复堆积）。
	frameDetached := false

	activities := make(map[string]*Activity)
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
		base := BuildPromptHeaderLines(acts, state.QueueDisplayLines(), state.AnyRunning(), completions)
		// Replan 路径:项目模式 + 有正在跑的 plan 时,头部加一行项目级进度。
		// 数据从 PlanOrchestrator.CurrentSnapshot 拿(线程安全 + 实时反映 plan 状态)。
		if statusLine := buildProjectStatusLine(state); statusLine != "" {
			base = append([]string{statusLine}, base...)
		}
		return base
	}
	RenderPromptFrame(session, state.Mode, providerSession.Name, providerSession.Model, e.Workdir, promptHeaderLines(), state.AnyRunning())

	// launchRun 启动一个 run 并创建对应的 Activity。
	launchRun := func(input string) string {
		// 注意:input 始终是原文。预处理在 runChatTurn 内部对"临时发给 LLM 的 messages"做,
		// 不污染 state.Messages / 持久化 / Activity / subtaskCtx。
		id := StartChatRun(state, providerSession.Provider, *brainID, *maxTurns, input, eventCh)
		act := &Activity{RunID: id, Input: input}
		act.Start()
		activities[id] = act
		// 注册当前 run 到全局 chat channel，让 workflow / delegate 工具能 emit 事件回来
		SetActiveChatChan(eventCh, id)
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
		}
		// 清掉全局 active chan（防止下一个 run 之前 stale tool reporter 还往里写）
		ClearActiveChatChan()
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

		// 完成总结：仅当用过 todo 框（说明跑过工作流）时打印一行汇总，
		// 让用户清楚多少任务成功 / 失败 / 总耗时。
		if act != nil && len(act.Todos) > 0 {
			done, fail, total := 0, 0, len(act.Todos)
			for _, td := range act.Todos {
				switch td.State {
				case TodoDone:
					done++
				case TodoFailed:
					fail++
				}
			}
			elapsed := time.Since(act.StartedAt)
			if fail == 0 && done == total {
				fmt.Printf("\033[32m✓ 完成\033[0m  %d/%d 任务  \033[2m耗时 %s\033[0m\n\n", done, total, formatElapsed(elapsed))
			} else if fail > 0 {
				fmt.Printf("\033[31m✗ 部分失败\033[0m  完成 %d  失败 %d  总计 %d  \033[2m耗时 %s\033[0m\n\n", done, fail, total, formatElapsed(elapsed))
			} else {
				fmt.Printf("\033[33m⚠ 未完成\033[0m  完成 %d/%d  \033[2m耗时 %s\033[0m\n\n", done, total, formatElapsed(elapsed))
			}
		}

		// 默认隐藏元数据行；/verbose on 时打回。
		if VerboseEnabled() && rr.Result != nil {
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
		// 队列消息处理:仅当没有 running run 时才出队启动,实现 D2 串行化。
		// Replan 路线决策:不并发同 session 的多 turn(避免 state.Messages /
		// LoopDetector / ContextEngine 并发污染),follow-up 入队列等当前完成。
		// AnyRunning() 仍 true 时跳过出队,等下一轮 select(run 完成事件触发再轮询)。
		if !state.AnyRunning() {
			for {
				queued := state.Dequeue()
				if queued == "" {
					break
				}
				launchRun(queued)
				// 启动一个 run 后跳出内层,让 launchRun 的 goroutine 跑起来,
				// 下次 AnyRunning() == true,后续 queued 继续等。
				break
			}
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
					isContent := ev.Progress.Kind == ProgressContent
					if isContent {
						lastContentAt = time.Now()
					}
					willPrint := willPrintToStdout(ev.Progress)
					// Content 流式 token：第一次到达时 detach 一次后保持 detached；
					// 不在每个 token 间重画 frame。下一个非 content 事件（或
					// progressTicker 在 content 静默后）会负责 render 回来。
					// 非 content 但要打印的事件：常规 detach → print → render。
					if willPrint {
						if !frameDetached {
							DetachPromptFrame(session)
							frameDetached = true
						}
					} else if frameDetached && !isContent {
						// 非打印事件，但 frame 被 content 流 detach 了：先 render 回来
						// 让 spinner/queue 跟得上活动状态。
						RenderPromptFrame(session, state.Mode, providerSession.Name, providerSession.Model, e.Workdir, promptHeaderLines(), state.AnyRunning())
						frameDetached = false
					}
					StreamProgressEvent(*ev.Progress)
					if len(ev.Progress.PreviewLines) > 0 {
						PrintDiffPreviewBlock(ev.Progress.PreviewLines)
					}
					if act, ok := activities[ev.RunID]; ok {
						act.Apply(*ev.Progress)
					}
					// 非 content 的打印事件：打完立即 render 回来。
					// content 事件保持 detached，等 ticker / 下一个非 content 事件 render。
					if willPrint && !isContent {
						RenderPromptFrame(session, state.Mode, providerSession.Name, providerSession.Model, e.Workdir, promptHeaderLines(), state.AnyRunning())
						frameDetached = false
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
			// 只在交互终端刷新；非 tty 静默。
			if !isInteractiveStdout() {
				continue
			}
			// LLM 流式 token 期间彻底禁用 ticker 触发的 frame 重绘：
			// 因为 stream content 是 fmt.Print 直接 append 到屏幕底部，
			// 任何 frame 重绘（即使在 content 静默间隙）都会和 LLM 持续追加
			// 的 token 抢同一行，造成"<token><spinner+queue>...<token><spinner+queue>"
			// 的重复堆积。等 frame 不是 detached（说明 content 流早已结束 +
			// 已 render 回来）时才允许 ticker 重绘 spinner 转场。
			if frameDetached {
				continue
			}
			// 兜底：lastContentAt 800ms 内不刷（仅在 frame 未 detached 但仍有
			// 残余 content 的边界场景生效）。
			if !lastContentAt.IsZero() && time.Since(lastContentAt) < 800*time.Millisecond {
				continue
			}
			// 有 running activity 时，重绘 prompt frame（spinner 在 queue 区）。
			anyRunning := false
			for _, act := range activities {
				if act.Running() {
					anyRunning = true
					break
				}
			}
			if anyRunning {
				RerenderPromptFrame(session, state.Mode, providerSession.Name, providerSession.Model, e.Workdir, promptHeaderLines(), state.AnyRunning())
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

				// ! 前缀 = 直接 shell 命令模式：不发给 LLM，直接在 chat workdir 跑 shell。
				// claude-code 风格的"快速命令"，用于不需要 AI 思考的事情（看文件 / 跑命令）。
				if strings.HasPrefix(input, "!") {
					DetachPromptFrame(session)
					ExecuteShellCommand(strings.TrimPrefix(input, "!"), e.Workdir)
					RenderPromptFrame(session, state.Mode, providerSession.Name, providerSession.Model, e.Workdir, promptHeaderLines(), state.AnyRunning())
					continue
				}

				// Replan-aware 分诊路由:
				// - 没有 running run → 直接启动(原行为)
				// - 项目模式 + running → RelevanceClassifier 分类
				//   Unrelated 入队列 / StatusQuery 即时回 / Modification 触发 replan
				//   Cancel 取消 / Refine 发 brain.feedback.requested
				// - 无项目模式 + running → 入队列(D2 串行化,等当前完成)
				bus := orchEventBus(orch)
				res := dispatchUserInput(state, input, bus, launchRun)
				if res.Hint != "" {
					DetachPromptFrame(session)
					fmt.Println(res.Hint)
					fmt.Println()
					RenderPromptFrame(session, state.Mode, providerSession.Name, providerSession.Model, e.Workdir, promptHeaderLines(), state.AnyRunning())
				}
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

		// ! 前缀 = 直接 shell 命令（不发给 LLM）
		if strings.HasPrefix(input, "!") {
			ExecuteShellCommand(strings.TrimPrefix(input, "!"), "")
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

		// 注意:input 是原文。预处理在 runChatTurn 内部做,只对"临时发给 LLM 的 messages"生效。
		ctx, cancel := config.WithOptionalTimeout(context.Background(), state.RunTimeout)
		result, err := runChatTurn(ctx, state, provider, state.Registry, state.Opts, *brainID, *maxTurns, state.TurnCount, baseMessages, input, state.Sandbox.Primary(), state.RunTimeout, "", nil)
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

		// 默认隐藏元数据行；/verbose 才显示。
		if VerboseEnabled() {
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
}

// isInteractiveStdout 判断 stdout 是否连到一个真实终端。
// pipe / 重定向到文件 / CI 环境时返回 false，spinner 等动态刷新一律静默，
// 避免日志里写满 \r\033[2K 这种 ANSI 控制序列。
func isInteractiveStdout() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

// willPrintToStdout 判断一个 progress event 是否会真的 print 到 stdout（需要 detach
// 输入框避开撞行）。规则：
//   - ProgressContent  → 始终会 print（LLM 流式文本）
//   - ProgressTool*    → 仅 verbose 时打；ToolEnd 失败一行红色摘要也会打
//   - 其他             → 不 print
func willPrintToStdout(p *ProgressEvent) bool {
	if p == nil {
		return false
	}
	switch p.Kind {
	case ProgressContent:
		return p.Text != ""
	case ProgressToolPlan, ProgressToolStart:
		return VerboseEnabled()
	case ProgressToolEnd:
		// 失败默认会打一行红色摘要；成功仅 verbose 才打
		if !p.OK {
			return true
		}
		return VerboseEnabled()
	}
	return false
}

// shortCommit 返回 commit hash 的前 7 位,unknown 时返回 "unknown"。
// 给 chat 启动 banner 用,让用户能直接看到当前 brain.exe 是哪个 commit 编的,
// 一眼判断"是不是新版本",避免"明明 git pull + build 了但行为没变"的歧义。
func shortCommit(commit string) string {
	if commit == "" || commit == "unknown" {
		return "unknown"
	}
	if len(commit) >= 7 {
		return commit[:7]
	}
	return commit
}
