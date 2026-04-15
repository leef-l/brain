package main

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
	"github.com/leef-l/brain/sdk/cli"
	"github.com/leef-l/brain/sdk/kernel"
	"github.com/leef-l/brain/sdk/llm"
	"github.com/leef-l/brain/sdk/loop"
)

// ---------------------------------------------------------------------------
// runChat — interactive REPL (entry point)
// ---------------------------------------------------------------------------

func runChat(args []string) int {
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

	cfg, cfgErr := loadConfig()
	modelInput, err := parseModelConfigJSON(*modelConfigJSON)
	if err != nil {
		fmt.Fprintf(os.Stderr, "brain chat: %v\n", err)
		return cli.ExitUsage
	}
	filePolicyInput, err := parseFilePolicyJSON(*filePolicyJSON)
	if err != nil {
		fmt.Fprintf(os.Stderr, "brain chat: %v\n", err)
		return cli.ExitUsage
	}
	filePolicyInput = resolveFilePolicyInput(cfg, filePolicyInput)

	explicitProviderInput := hasModelConfigOverrides(modelInput) || strings.TrimSpace(*apiKey) != "" || strings.TrimSpace(*baseURL) != "" || strings.TrimSpace(*modelFlag) != ""
	if cfg == nil && !wantsMockProvider(*providerFlag, modelInput) && !explicitProviderInput && os.Getenv("ANTHROPIC_API_KEY") == "" {
		if cfgErr != nil {
			fmt.Fprintf(os.Stderr, "brain chat: %v\n", cfgErr)
		} else {
			printConfigSetupGuide()
		}
		return cli.ExitFailed
	}

	mode, err := resolvePermissionMode(*modeFlag, cfg, true)
	if err != nil {
		fmt.Fprintf(os.Stderr, "brain chat: %v\n", err)
		return cli.ExitUsage
	}
	timeout, err := resolveRunTimeoutWithConfig(cfg, *timeoutFlag, 5*time.Minute)
	if err != nil {
		fmt.Fprintf(os.Stderr, "brain chat: %v\n", err)
		return cli.ExitUsage
	}

	// Build orchestrator for specialist brain delegation.
	var orch *kernel.Orchestrator
	if *brainID == "central" && !wantsMockProvider(*providerFlag, modelInput) {
		orch = buildOrchestrator(orchestratorConfig{
			cfg:         cfg,
			modelConfig: modelInput,
			provider:    *providerFlag,
			apiKey:      *apiKey,
			baseURL:     *baseURL,
			model:       *modelFlag,
		})
	}

	env := newExecutionEnvironment(*workDir, mode, cfg, nil, true)
	if err := applyFilePolicy(env, filePolicyInput); err != nil {
		fmt.Fprintf(os.Stderr, "brain chat: %v\n", err)
		return cli.ExitUsage
	}
	if mode == modeRestricted && env.filePolicy == nil {
		fmt.Fprintln(os.Stderr, "brain chat: restricted mode requires file_policy (config or --file-policy-json)")
		return cli.ExitUsage
	}

	providerSession := openMockProvider("hello from mock provider")
	if !wantsMockProvider(*providerFlag, modelInput) {
		providerSession, err = openConfiguredProvider(cfg, *brainID, modelInput, *providerFlag, *apiKey, *baseURL, *modelFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "brain chat: %v\n", err)
			fmt.Fprintln(os.Stderr, "  Set via: brain config set providers.<name>.api_key <key>")
			return cli.ExitFailed
		}
	}

	// Load configurable key bindings.
	kb := loadKeybindings()

	// Initialize chat state.
	state := &chatState{
		cfg:          cfg,
		brainID:      *brainID,
		env:          env,
		kb:           kb,
		sandbox:      env.sandbox,
		sandboxCfg:   env.sandboxCfg,
		orchestrator: orch,
		approvalCh:   make(chan approvalRequest),
		runTimeout:   timeout,
	}
	state.switchMode(mode)

	// Banner.
	fmt.Println()
	fmt.Printf("  \033[1mBrain Chat v%s\033[0m\n", brain.CLIVersion)
	fmt.Printf("  \033[2mProvider:\033[0m %s / %s\n", providerSession.Name, providerSession.Model)
	fmt.Printf("  \033[2mBrain:\033[0m    %s\n", *brainID)
	fmt.Printf("  \033[2mMode:\033[0m     %s\n", mode.styledLabel())
	fmt.Printf("  \033[2mWorkdir:\033[0m  %s\n", env.workdir)
	fmt.Printf("  \033[2mKeys:\033[0m     Esc cancel, Ctrl+D quit, Ctrl+W mode, /help\n")
	if orch != nil {
		fmt.Printf("  \033[2mDelegates:\033[0m %v\n", orch.AvailableKinds())
	}
	fmt.Println()

	// Startup diagnostics: verify provider connectivity.
	if !wantsMockProvider(*providerFlag, modelInput) {
		if diag := runStartupDiagnostics(providerSession, cfg); diag != "" {
			fmt.Println(diag)
		}
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)

	// Enable raw terminal input so we can intercept key bindings.
	restore, rawErr := enableRawInput()
	if rawErr != nil {
		// Fallback: raw mode not available (e.g. piped input).
		return runChatLineMode(state, providerSession.Provider, brainID, maxTurns, sigCh)
	}
	defer restore()

	resultCh := make(chan chatRunResult, 1)
	progressCh := make(chan chatProgressEvent, 128)
	stdinCh, stdinErrCh := startAsyncStdinReader()
	progressTicker := time.NewTicker(250 * time.Millisecond)
	defer progressTicker.Stop()

	running := false
	activity := &chatActivity{}
	lastProgressSecond := int64(-1)
	session := newLineReadSession(kb, 0)
	session.history = loadHistory()
	session.historyIndex = len(session.history)
	promptHeaderLines := func() []string {
		return buildPromptHeaderLines(activity, state.queueDisplayLines(), running)
	}
	renderPromptFrame(session, state.mode, providerSession.Name, providerSession.Model, env.workdir, promptHeaderLines(), running)

	for {
		// Process queued messages when idle.
		if !running {
			if queued := state.dequeue(); queued != "" {
				activity.start()
				lastProgressSecond = -1
				detachPromptFrame(session)
				printUserMessage(queued)
				startChatRun(state, providerSession.Provider, *brainID, *maxTurns, queued, resultCh, progressCh)
				running = true
				renderPromptFrame(session, state.mode, providerSession.Name, providerSession.Model, env.workdir, promptHeaderLines(), running)
			}
		}

		select {
		case <-sigCh:
			// Ctrl+C via signal: ignored (matching Claude Code behavior).
			// Escape handles cancellation instead.
			continue

		case req := <-state.approvalCh:
			handleApprovalRequest(session, kb, state.mode, providerSession.Name, providerSession.Model, env.workdir, promptHeaderLines(), running, req, stdinCh, stdinErrCh)
			continue

		case rr := <-resultCh:
			running = false
			handleChatRunResult(state, providerSession.Provider, *brainID, *maxTurns, session, state.mode, providerSession.Name, providerSession.Model, env.workdir, promptHeaderLines(), &running, rr, resultCh, progressCh, activity, stdinCh, stdinErrCh)
			continue

		case ev := <-progressCh:
			if running && len(ev.previewLines) > 0 {
				detachPromptFrame(session)
				printDiffPreviewBlock(ev.previewLines)
			}
			if running && activity.apply(ev) {
				rerenderPromptFrame(session, state.mode, providerSession.Name, providerSession.Model, env.workdir, promptHeaderLines(), running)
			}
			continue

		case <-progressTicker.C:
			if running && activity.running() {
				sec := int64(time.Since(activity.startedAt) / time.Second)
				if sec != lastProgressSecond {
					lastProgressSecond = sec
					rerenderPromptFrame(session, state.mode, providerSession.Name, providerSession.Model, env.workdir, promptHeaderLines(), running)
				}
			}
			continue

		case readErr := <-stdinErrCh:
			detachPromptFrame(session)
			if readErr != nil && readErr != io.EOF {
				fmt.Fprintf(os.Stderr, "brain chat: %v\n", readErr)
				return cli.ExitFailed
			}
			fmt.Println("Bye!")
			return cli.ExitOK

		case data := <-stdinCh:
			line, action, done, err := session.consume(data)
			if err != nil {
				detachPromptFrame(session)
				fmt.Fprintf(os.Stderr, "brain chat: %v\n", err)
				return cli.ExitFailed
			}
			if !done {
				// Always rerender when input changes — slash completion
				// hints need to update as the user types.
				currentInput := strings.TrimSpace(session.editor().string())
				completions := slashCompletionLines(currentInput)
				headerLines := buildPromptHeaderLines(activity, state.queueDisplayLines(), running, completions)
				rerenderPromptFrame(session, state.mode, providerSession.Name, providerSession.Model, env.workdir, headerLines, running)
				continue
			}

			switch action {
			case actionQuit:
				detachPromptFrame(session)
				fmt.Println("Bye!")
				return cli.ExitOK

			case actionEscape:
				// Claude Code behavior:
				// - Running + empty input → cancel running task
				// - Idle + has input → clear input
				// - Idle + no input → do nothing
				input := strings.TrimSpace(line)
				if running {
					if input == "" {
						state.cancelCurrentRun()
						activity.stop()
						resetPromptInput(session)
						detachPromptFrame(session)
						fmt.Println("  \033[1;33m! Cancelled\033[0m")
						fmt.Println()
						running = false
						renderPromptFrame(session, state.mode, providerSession.Name, providerSession.Model, env.workdir, promptHeaderLines(), running)
					} else {
						// Has draft text while running: clear the draft
						resetPromptInput(session)
						rerenderPromptFrame(session, state.mode, providerSession.Name, providerSession.Model, env.workdir, promptHeaderLines(), running)
					}
				} else {
					if input != "" {
						resetPromptInput(session)
						rerenderPromptFrame(session, state.mode, providerSession.Name, providerSession.Model, env.workdir, promptHeaderLines(), running)
					}
					// else: idle + no input → do nothing
				}
				continue

			case actionCancel:
				// Legacy cancel action (if user configured a cancel key that isn't Ctrl+C).
				// Behaves same as Escape.
				input := strings.TrimSpace(line)
				resetPromptInput(session)
				if running {
					state.cancelCurrentRun()
					activity.stop()
					detachPromptFrame(session)
					fmt.Println("  \033[1;33m! Cancelled\033[0m")
					fmt.Println()
					running = false
					renderPromptFrame(session, state.mode, providerSession.Name, providerSession.Model, env.workdir, promptHeaderLines(), running)
				} else if input != "" {
					rerenderPromptFrame(session, state.mode, providerSession.Name, providerSession.Model, env.workdir, promptHeaderLines(), running)
				}
				continue

			case actionCycle:
				nextMode := cycleMode(state.mode)
				state.switchMode(nextMode)
				rerenderPromptFrame(session, state.mode, providerSession.Name, providerSession.Model, env.workdir, promptHeaderLines(), running)
				continue

			case actionEnter, actionQueue:
				input := strings.TrimSpace(line)
				frameDetached := false
				resetPromptInput(session)
				if input == "" {
					rerenderPromptFrame(session, state.mode, providerSession.Name, providerSession.Model, env.workdir, promptHeaderLines(), running)
					continue
				}
				session.addHistory(input)
				appendHistory(input)

				if strings.HasPrefix(input, "/") {
					detachPromptFrame(session)
					frameDetached = true
					handled, shouldQuit := handleSlashCommand(input, state)
					if shouldQuit {
						fmt.Println("Bye!")
						return cli.ExitOK
					}
					if handled {
						renderPromptFrame(session, state.mode, providerSession.Name, providerSession.Model, env.workdir, promptHeaderLines(), running)
						continue
					}
				}

				if running {
					state.enqueue(input)
					if frameDetached {
						renderPromptFrame(session, state.mode, providerSession.Name, providerSession.Model, env.workdir, promptHeaderLines(), running)
					} else {
						rerenderPromptFrame(session, state.mode, providerSession.Name, providerSession.Model, env.workdir, promptHeaderLines(), running)
					}
					continue
				}

				activity.start()
				lastProgressSecond = -1
				if !frameDetached {
					detachPromptFrame(session)
				}
				printUserMessage(input)
				startChatRun(state, providerSession.Provider, *brainID, *maxTurns, input, resultCh, progressCh)
				running = true
				renderPromptFrame(session, state.mode, providerSession.Name, providerSession.Model, env.workdir, promptHeaderLines(), running)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func hasDraftInput(session *lineReadSession) bool {
	return strings.TrimSpace(session.editor().string()) != ""
}

func resetPromptInput(session *lineReadSession) {
	session.pending = nil
	session.leaveHistoryBrowse()
	session.ed.runes = nil
	session.ed.pos = 0
}

func startAsyncStdinReader() (<-chan []byte, <-chan error) {
	dataCh := make(chan []byte, 64)
	errCh := make(chan error, 1)

	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				// After initial read, drain any immediately available bytes
				// so escape sequences arrive as one chunk.
				for {
					ready, _ := waitForStdinReady(1 * time.Millisecond)
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

// ---------------------------------------------------------------------------
// runChatLineMode — fallback for non-TTY input (pipes, tests)
// ---------------------------------------------------------------------------

func runChatLineMode(state *chatState, provider llm.Provider, brainID *string, maxTurns *int, sigCh chan os.Signal) int {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	for {
		printPrompt(state.mode)

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
			handled, shouldQuit := handleSlashCommand(input, state)
			if shouldQuit {
				fmt.Println("Bye!")
				return cli.ExitOK
			}
			if handled {
				continue
			}
		}

		printUserMessage(input)

		state.turnCount++
		baseMessages := make([]llm.Message, len(state.messages))
		copy(baseMessages, state.messages)

		ctx, cancel := withOptionalTimeout(context.Background(), state.runTimeout)
		result, err := runChatTurn(ctx, provider, state.registry, state.opts, *brainID, *maxTurns, state.turnCount, baseMessages, input, state.sandbox.Primary(), state.runTimeout, nil)
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

		// Surface LLM errors that Runner.Execute buries in TurnResults.
		if result.Run.State == loop.StateFailed {
			if errMsg := lastTurnError(result); errMsg != "" {
				fmt.Fprintf(os.Stderr, "\033[1;31m! Error: %s\033[0m\n\n", errMsg)
				continue
			}
		}

		state.messages = result.FinalMessages
		replyText := extractAssistantReply(result.FinalMessages)
		if replyText != "" {
			printAssistantMessage(replyText)
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
