package chat

import (
	"bufio"
	"context"
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
	"github.com/leef-l/brain/sdk/kernel"
	"github.com/leef-l/brain/sdk/llm"
	"github.com/leef-l/brain/sdk/loop"
	"github.com/leef-l/brain/sdk/tool"
)

func RunChat(args []string) int {
	fs := flag.NewFlagSet("chat", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	brainID := fs.String("brain", "central", "brain identifier (central, code, verifier)")
	maxTurns := fs.Int("max-turns", 20, "max turns per user message")
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
			orch = kernel.NewOrchestratorWithPool(pool, &kernel.ProcessRunner{BinResolver: deps.DefaultBinResolver()}, llmProxy, deps.DefaultBinResolver(), kernel.OrchestratorConfig{},
				kernel.WithSemanticApprover(&kernel.DefaultSemanticApprover{}),
				kernel.WithLearningEngine(kernel.NewLearningEngine()),
			)
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

	fmt.Println()
	fmt.Printf("  \033[1mBrain Chat v%s\033[0m\n", brain.CLIVersion)
	fmt.Printf("  \033[2mProvider:\033[0m %s / %s\n", providerSession.Name, providerSession.Model)
	fmt.Printf("  \033[2mBrain:\033[0m    %s\n", *brainID)
	fmt.Printf("  \033[2mMode:\033[0m     %s\n", mode.StyledLabel())
	fmt.Printf("  \033[2mWorkdir:\033[0m  %s\n", e.Workdir)
	fmt.Printf("  \033[2mKeys:\033[0m     Esc cancel, Ctrl+D quit, Ctrl+W mode, /help\n")
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
		return runChatLineMode(state, providerSession.Provider, brainID, maxTurns, sigCh)
	}
	defer restore()

	resultCh := make(chan RunResult, 1)
	progressCh := make(chan ProgressEvent, 128)
	stdinCh, stdinErrCh := startAsyncStdinReader()
	progressTicker := time.NewTicker(250 * time.Millisecond)
	defer progressTicker.Stop()

	running := false
	activity := &Activity{}
	var lastProgressSecond int64 = -1
	_ = lastProgressSecond
	session := term.NewLineReadSession(kb, 0)
	session.History = LoadHistory()
	session.HistoryIndex = len(session.History)
	promptHeaderLines := func() []string {
		currentInput := strings.TrimSpace(session.Editor().String())
		completions := SlashCompletionLines(currentInput)
		return BuildPromptHeaderLines(activity, state.QueueDisplayLines(), running, completions)
	}
	RenderPromptFrame(session, state.Mode, providerSession.Name, providerSession.Model, e.Workdir, promptHeaderLines(), running)

	for {
		if !running {
			if queued := state.Dequeue(); queued != "" {
				activity.Start()
				lastProgressSecond = -1
				DetachPromptFrame(session)
				PrintUserMessage(queued)
				StartChatRun(state, providerSession.Provider, *brainID, *maxTurns, queued, resultCh, progressCh)
				running = true
				RenderPromptFrame(session, state.Mode, providerSession.Name, providerSession.Model, e.Workdir, promptHeaderLines(), running)
			}
		}

		select {
		case <-sigCh:
			continue

		case req := <-state.ApprovalCh:
			HandleApprovalRequest(state, session, kb, state.Mode, providerSession.Name, providerSession.Model, e.Workdir, promptHeaderLines(), running, req, stdinCh, stdinErrCh)
			continue

		case rr := <-resultCh:
			running = false
			HandleChatRunResult(state, providerSession.Provider, *brainID, *maxTurns, session, state.Mode, providerSession.Name, providerSession.Model, e.Workdir, promptHeaderLines(), &running, rr, resultCh, progressCh, activity, stdinCh, stdinErrCh)
			continue

		case ev := <-progressCh:
			if running && len(ev.PreviewLines) > 0 {
				DetachPromptFrame(session)
				PrintDiffPreviewBlock(ev.PreviewLines)
			}
			// running 期间不再每条 progress 都 rerender 整个 prompt frame。
			// 频繁的 clear+redraw 会把流式 LLM 输出反复"清掉+重写",在窄终端
			// 上造成文字重复叠加。仅应用内部状态,不碰终端。
			if running {
				activity.Apply(ev)
			}
			continue

		case <-progressTicker.C:
			// LLM 正在输出时不做任何终端写操作。秒数不刷新比"每秒闪一下
			// 引发重影"更可忍受。下一轮会从架构层面换掉这个多行 prompt
			// frame 方案。
			if running && activity.Running() {
				sec := int64(time.Since(activity.StartedAt) / time.Second)
				lastProgressSecond = sec
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
				// 正在输入过程中(每收到一个字符就进到这里)。字符回显已经
				// 由 LineReadSession.Consume 内部的 fast-path(fmt.Print(r))
				// 完成。
				//
				// 不能无条件触发 RerenderPromptFrame:那会清掉整个多行 prompt
				// frame + 重画,每次重画把"已经打出的字符"连带擦掉又重新写
				// 一遍,视觉上就是每输入一个字就多出一行残影。
				//
				// slash 补全需要动态显示候选项,只在当前输入以 / 开头时才
				// Rerender,普通聊天输入完全走 Consume 的原地回显路径。
				if strings.HasPrefix(strings.TrimSpace(session.Editor().String()), "/") {
					RerenderPromptFrame(session, state.Mode, providerSession.Name, providerSession.Model, e.Workdir, promptHeaderLines(), running)
				}
				continue
			}

			switch action {
			case term.ActionQuit:
				DetachPromptFrame(session)
				fmt.Println("Bye!")
				return cli.ExitOK

			case term.ActionEscape:
				input := strings.TrimSpace(line)
				if running {
					if input == "" {
						state.CancelCurrentRun()
						activity.Stop()
						resetPromptInput(session)
						DetachPromptFrame(session)
						fmt.Println("  \033[1;33m! Cancelled\033[0m")
						fmt.Println()
						running = false
						RenderPromptFrame(session, state.Mode, providerSession.Name, providerSession.Model, e.Workdir, promptHeaderLines(), running)
					} else {
						resetPromptInput(session)
						RerenderPromptFrame(session, state.Mode, providerSession.Name, providerSession.Model, e.Workdir, promptHeaderLines(), running)
					}
				} else {
					if input != "" {
						resetPromptInput(session)
						RerenderPromptFrame(session, state.Mode, providerSession.Name, providerSession.Model, e.Workdir, promptHeaderLines(), running)
					}
				}
				continue

			case term.ActionCancel:
				input := strings.TrimSpace(line)
				resetPromptInput(session)
				if running {
					state.CancelCurrentRun()
					activity.Stop()
					DetachPromptFrame(session)
					fmt.Println("  \033[1;33m! Cancelled\033[0m")
					fmt.Println()
					running = false
					RenderPromptFrame(session, state.Mode, providerSession.Name, providerSession.Model, e.Workdir, promptHeaderLines(), running)
				} else if input != "" {
					RerenderPromptFrame(session, state.Mode, providerSession.Name, providerSession.Model, e.Workdir, promptHeaderLines(), running)
				}
				continue

			case term.ActionCycle:
				nextMode := env.CycleMode(state.Mode)
				state.SwitchMode(nextMode)
				RerenderPromptFrame(session, state.Mode, providerSession.Name, providerSession.Model, e.Workdir, promptHeaderLines(), running)
				continue

			case term.ActionEnter, term.ActionQueue:
				input := strings.TrimSpace(line)
				frameDetached := false
				resetPromptInput(session)
				if input == "" {
					RerenderPromptFrame(session, state.Mode, providerSession.Name, providerSession.Model, e.Workdir, promptHeaderLines(), running)
					continue
				}
				session.AddHistory(input)
				AppendHistory(input)

				if strings.HasPrefix(input, "/") {
					DetachPromptFrame(session)
					frameDetached = true
					handled, shouldQuit := HandleSlashCommand(input, state)
					if shouldQuit {
						fmt.Println("Bye!")
						return cli.ExitOK
					}
					if handled {
						RenderPromptFrame(session, state.Mode, providerSession.Name, providerSession.Model, e.Workdir, promptHeaderLines(), running)
						continue
					}
				}

				if running {
					state.Enqueue(input)
					if frameDetached {
						RenderPromptFrame(session, state.Mode, providerSession.Name, providerSession.Model, e.Workdir, promptHeaderLines(), running)
					} else {
						RerenderPromptFrame(session, state.Mode, providerSession.Name, providerSession.Model, e.Workdir, promptHeaderLines(), running)
					}
					continue
				}

				activity.Start()
				lastProgressSecond = -1
				if !frameDetached {
					DetachPromptFrame(session)
				}
				PrintUserMessage(input)
				StartChatRun(state, providerSession.Provider, *brainID, *maxTurns, input, resultCh, progressCh)
				running = true
				RenderPromptFrame(session, state.Mode, providerSession.Name, providerSession.Model, e.Workdir, promptHeaderLines(), running)
			}
		}
	}
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
		result, err := runChatTurn(ctx, provider, state.Registry, state.Opts, *brainID, *maxTurns, state.TurnCount, baseMessages, input, state.Sandbox.Primary(), state.RunTimeout, nil)
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

		state.Messages = result.FinalMessages
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
