package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/leef-l/brain/sdk/cli"
	brainerrors "github.com/leef-l/brain/sdk/errors"
	"github.com/leef-l/brain/sdk/kernel"
	"github.com/leef-l/brain/sdk/llm"
	"github.com/leef-l/brain/sdk/tool"
)

// runRun implements `brain run` — the primary command for running an
// agent loop that talks to a real LLM and can read/write files, search
// code, and execute shell commands.
//
// Provider selection:
//
//	--provider mock → uses MockProvider (for testing / CI)
//	otherwise       → resolves a real provider session from
//	                  model-config JSON, flags, config, and env
//
// Provider resolution priority:
//
//  1. structured --model-config-json payload
//  2. explicit flags (--provider/--api-key/--base-url/--model)
//  3. ~/.brain/config.json active_provider / providers.<name>
//  4. ANTHROPIC_API_KEY environment variable
//
// File mutation policy:
//
//	--file-policy-json constrains read/create/edit/delete operations inside
//	workdir. Command-like tools still run in the sandboxed workdir, but
//	their real file diff is validated against the same policy.
//
// See 27-CLI命令契约.md §6 and docs/v2生产级实施计划.md Phase C.
func runRun(args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	prompt := fs.String("prompt", "hello from brain run", "user prompt to send to the LLM")
	reply := fs.String("reply", "hello from mock provider", "pre-canned assistant reply (mock mode only)")
	brainID := fs.String("brain", "central", "brain identifier")
	maxTurns := fs.Int("max-turns", 4, "maximum number of turns")
	stream := fs.Bool("stream", false, "enable streaming mode")
	jsonOut := fs.Bool("json", true, "emit a JSON run summary to stdout")
	provider := fs.String("provider", "anthropic", "LLM provider: anthropic or mock")
	apiKey := fs.String("api-key", "", "API key (overrides env and config)")
	baseURL := fs.String("base-url", "", "API base URL (default: https://api.anthropic.com)")
	model := fs.String("model", "", "model name (overrides config)")
	modelConfigJSON := fs.String("model-config-json", "", "structured model config JSON override")
	modeFlag := fs.String("mode", "", "permission mode: plan, default, accept-edits, auto, restricted, bypass-permissions")
	workDir := fs.String("workdir", "", "working directory sandbox (default: current directory)")
	filePolicyJSON := fs.String("file-policy-json", "", "fine-grained file mutation policy JSON")
	timeoutFlag := fs.String("timeout", "", "overall run timeout (e.g. 5m, 30m, 0 to disable)")
	if err := fs.Parse(args); err != nil {
		return cli.ExitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "brain run: unexpected argument %q\n", fs.Arg(0))
		return cli.ExitUsage
	}

	cfg, cfgErr := loadConfig()
	modelInput, err := parseModelConfigJSON(*modelConfigJSON)
	if err != nil {
		fmt.Fprintf(os.Stderr, "brain run: %v\n", err)
		return cli.ExitUsage
	}
	filePolicyInput, err := parseFilePolicyJSON(*filePolicyJSON)
	if err != nil {
		fmt.Fprintf(os.Stderr, "brain run: %v\n", err)
		return cli.ExitUsage
	}
	filePolicyInput = resolveFilePolicyInput(cfg, filePolicyInput)
	mode, err := resolvePermissionMode(*modeFlag, cfg, true)
	if err != nil {
		fmt.Fprintf(os.Stderr, "brain run: %v\n", err)
		return cli.ExitUsage
	}
	timeout, err := resolveRunTimeoutWithConfig(cfg, *timeoutFlag, 5*time.Minute)
	if err != nil {
		fmt.Fprintf(os.Stderr, "brain run: %v\n", err)
		return cli.ExitUsage
	}
	ctx, cancel := withOptionalTimeout(context.Background(), timeout)
	defer cancel()

	env := newExecutionEnvironment(*workDir, mode, cfg, nil, false)
	if err := applyFilePolicy(env, filePolicyInput); err != nil {
		fmt.Fprintf(os.Stderr, "brain run: %v\n", err)
		return cli.ExitUsage
	}
	if mode == modeRestricted && env.filePolicy == nil {
		fmt.Fprintln(os.Stderr, "brain run: restricted mode requires file_policy (config or --file-policy-json)")
		return cli.ExitUsage
	}
	explicitProviderInput := hasModelConfigOverrides(modelInput) || strings.TrimSpace(*apiKey) != "" || strings.TrimSpace(*baseURL) != "" || strings.TrimSpace(*model) != ""

	runtime, err := newDefaultCLIRuntime(*brainID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "brain run: runtime: %v\n", err)
		return cli.ExitSoftware
	}

	// --- Register real tools with shared permission/sandbox policy ---
	// --- Orchestrator for specialist delegation (central brain only) ---
	var orch *kernel.Orchestrator
	if *brainID == "central" && !wantsMockProvider(*provider, modelInput) {
		orch = buildOrchestrator(orchestratorConfig{
			cfg:         cfg,
			modelConfig: modelInput,
			provider:    *provider,
			apiKey:      *apiKey,
			baseURL:     *baseURL,
			model:       *model,
		})
	}
	runtime.Kernel.ToolRegistry = buildManagedRegistry(cfg, env, *brainID, func(reg tool.Registry) {
		registerDelegateToolForEnvironment(reg, orch, env)
		registerSpecialistBridgeTools(reg, orch)
	})

	// --- Provider selection ---
	providerSession := openMockProvider(*reply)
	if !wantsMockProvider(*provider, modelInput) {
		if cfg == nil && !explicitProviderInput && os.Getenv("ANTHROPIC_API_KEY") == "" {
			if cfgErr != nil {
				fmt.Fprintf(os.Stderr, "brain run: %v\n", cfgErr)
			} else {
				printConfigSetupGuide()
			}
			return cli.ExitFailed
		}
		var err error
		providerSession, err = openConfiguredProvider(cfg, *brainID, modelInput, *provider, *apiKey, *baseURL, *model)
		if err != nil {
			fmt.Fprintf(os.Stderr, "brain run: %v (use --api-key, ANTHROPIC_API_KEY env, or brain config set providers.<name>.api_key <key>)\n", err)
			printConfigSetupGuide()
			return cli.ExitFailed
		}
	}

	runRec, err := runtime.RunStore.create(*brainID, *prompt, string(mode), env.workdir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "brain run: create run record: %v\n", err)
		return cli.ExitSoftware
	}

	// --- System prompt ---
	systemPrompt := buildSystemPrompt(mode, env.sandbox)
	if orch != nil {
		systemPrompt += buildOrchestratorPrompt(orch, runtime.Kernel.ToolRegistry)
	}

	outcome, err := executeManagedRun(ctx, managedRunExecution{
		Runtime:       runtime,
		Record:        runRec,
		Registry:      runtime.Kernel.ToolRegistry,
		Provider:      providerSession.Provider,
		ProviderName:  providerSession.Name,
		ProviderModel: providerSession.Model,
		BrainID:       *brainID,
		Prompt:        *prompt,
		MaxTurns:      *maxTurns,
		MaxDuration:   timeout,
		Stream:        *stream,
		SystemPrompt:  systemPrompt,
	})

	// Shutdown orchestrator if started.
	if orch != nil {
		_ = orch.Shutdown(context.Background())
	}

	if err != nil {
		return failRun(err, "execute run")
	}

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(outcome.Summary); err != nil {
			fmt.Fprintf(os.Stderr, "brain run: encode summary: %v\n", err)
			return cli.ExitSoftware
		}
	} else {
		fmt.Printf("run %s %s reply=%q\n", runRec.ID, outcome.FinalStatus, outcome.ReplyText)
	}
	return cli.ExitOK
}

// extractText concatenates every text block in a ContentBlock slice.
// Non-text blocks (tool_use, tool_result) are skipped.
func extractText(blocks []llm.ContentBlock) string {
	out := ""
	for _, b := range blocks {
		if b.Type == "text" {
			out += b.Text
		}
	}
	return out
}

// failRun prints a BrainError-aware error line and returns the CLI exit
// code appropriate for the error class.
func failRun(err error, context string) int {
	var be *brainerrors.BrainError
	if errors.As(err, &be) {
		fmt.Fprintf(os.Stderr, "brain run: %s: [%s] %s\n", context, be.ErrorCode, be.Message)
	} else {
		fmt.Fprintf(os.Stderr, "brain run: %s: %v\n", context, err)
	}
	return cli.ExitFailed
}
