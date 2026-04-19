package command

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/leef-l/brain/cmd/brain/cliruntime"
	"github.com/leef-l/brain/cmd/brain/config"
	"github.com/leef-l/brain/cmd/brain/env"
	"github.com/leef-l/brain/cmd/brain/provider"
	"github.com/leef-l/brain/sdk/agent"
	"github.com/leef-l/brain/sdk/cli"
	brainerrors "github.com/leef-l/brain/sdk/errors"
	"github.com/leef-l/brain/sdk/kernel"
	"github.com/leef-l/brain/sdk/llm"
	"github.com/leef-l/brain/sdk/tool"
)

type RunDeps struct {
	LoadConfig              ConfigLoader
	ParseModelConfigJSON    func(raw string) (*config.ModelConfigInput, error)
	ParseFilePolicyJSON     func(raw string) (*config.FilePolicyInput, error)
	ResolveFilePolicyInput  func(cfg *config.Config, input *config.FilePolicyInput) *config.FilePolicyInput
	HasModelConfigOverrides func(input *config.ModelConfigInput) bool
	WantsMockProvider       func(flag string, input *config.ModelConfigInput) bool
	PrintConfigSetupGuide   func()
	ResolvePermissionMode   func(flagValue string, cfg *config.Config, preferChatMode ...bool) (env.PermissionMode, error)
	NewExecutionEnv         func(workdir string, mode env.PermissionMode, cfg *config.Config, approver env.ApprovalPrompter, interactive bool) *env.Environment
	ApplyFilePolicy         func(e *env.Environment, input *config.FilePolicyInput) error
	BuildBrainPool          func(cfg *config.Config) *kernel.ProcessBrainPool
	DefaultBinResolver      func() func(kind agent.Kind) (string, error)
	NewDefaultCLIRuntime    func(brainKind string) (*cliruntime.Runtime, error)
	BuildManagedRegistry    func(cfg *config.Config, e *env.Environment, brainKind string, registerExtra func(tool.Registry)) tool.Registry
	RegisterDelegateTool    func(reg tool.Registry, orch *kernel.Orchestrator, e *env.Environment)
	RegisterBridgeTools     func(reg tool.Registry, orch *kernel.Orchestrator)
	BuildSystemPrompt       func(mode env.PermissionMode, sb *tool.Sandbox) string
	BuildOrchestratorPrompt func(orch *kernel.Orchestrator, reg tool.Registry) string
	ExecuteManagedRun       func(ctx context.Context, req ManagedRunExecution) (*ManagedRunOutcome, error)

	// NewBatchPlanner 创建 ToolBatchPlanner（含 ResourceLocker）。
	// 可选，nil 时 brain run 不启用并行分组和资源锁保护。
	NewBatchPlanner func() interface{} // 返回 loop.ToolBatchPlanner
}

type ManagedRunExecution struct {
	Runtime       *cliruntime.Runtime
	Record        *cliruntime.RunRecord
	Registry      tool.Registry
	Provider      llm.Provider
	ProviderName  string
	ProviderModel string
	BrainID       string
	Prompt        string
	MaxTurns      int
	MaxDuration   time.Duration
	Stream        bool
	SystemPrompt  string
	EventBus      interface{} // *events.MemEventBus
	BatchPlanner  interface{} // loop.ToolBatchPlanner
}

type ManagedRunOutcome struct {
	Result      interface{} // *loop.RunResult
	ReplyText   string
	Summary     map[string]interface{}
	SummaryJSON json.RawMessage
	PlanID      int64
	FinalStatus string
}

func RunRun(args []string, deps RunDeps) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	prompt := fs.String("prompt", "hello from brain run", "user prompt to send to the LLM")
	reply := fs.String("reply", "hello from mock provider", "pre-canned assistant reply (mock mode only)")
	brainID := fs.String("brain", "central", "brain identifier")
	maxTurns := fs.Int("max-turns", 4, "maximum number of turns")
	stream := fs.Bool("stream", false, "enable streaming mode")
	jsonOut := fs.Bool("json", true, "emit a JSON run summary to stdout")
	providerFlag := fs.String("provider", "", "LLM provider/profile name, or mock (default: config active_provider)")
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

	cfg, cfgErr := deps.LoadConfig()
	config.ApplyDiagnosticEnv(cfg)
	modelInput, err := deps.ParseModelConfigJSON(*modelConfigJSON)
	if err != nil {
		fmt.Fprintf(os.Stderr, "brain run: %v\n", err)
		return cli.ExitUsage
	}
	filePolicyInput, err := deps.ParseFilePolicyJSON(*filePolicyJSON)
	if err != nil {
		fmt.Fprintf(os.Stderr, "brain run: %v\n", err)
		return cli.ExitUsage
	}
	filePolicyInput = deps.ResolveFilePolicyInput(cfg, filePolicyInput)
	mode, err := deps.ResolvePermissionMode(*modeFlag, cfg, true)
	if err != nil {
		fmt.Fprintf(os.Stderr, "brain run: %v\n", err)
		return cli.ExitUsage
	}
	timeout, err := config.ResolveRunTimeoutWithConfig(cfg, *timeoutFlag, 5*time.Minute)
	if err != nil {
		fmt.Fprintf(os.Stderr, "brain run: %v\n", err)
		return cli.ExitUsage
	}
	ctx, cancel := config.WithOptionalTimeout(context.Background(), timeout)
	defer cancel()

	e := deps.NewExecutionEnv(*workDir, mode, cfg, nil, false)
	if err := deps.ApplyFilePolicy(e, filePolicyInput); err != nil {
		fmt.Fprintf(os.Stderr, "brain run: %v\n", err)
		return cli.ExitUsage
	}
	if mode == env.ModeRestricted && e.FilePolicy == nil {
		fmt.Fprintln(os.Stderr, "brain run: restricted mode requires file_policy (config or --file-policy-json)")
		return cli.ExitUsage
	}
	explicitProviderInput := deps.HasModelConfigOverrides(modelInput) || strings.TrimSpace(*apiKey) != "" || strings.TrimSpace(*baseURL) != "" || strings.TrimSpace(*model) != ""

	runtime, err := deps.NewDefaultCLIRuntime(*brainID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "brain run: runtime: %v\n", err)
		return cli.ExitSoftware
	}

	var pool *kernel.ProcessBrainPool
	var orch *kernel.Orchestrator
	if *brainID == "central" && !deps.WantsMockProvider(*providerFlag, modelInput) {
		pool = deps.BuildBrainPool(cfg)
		if pool != nil {
			defer func() { _ = pool.Shutdown(context.Background()) }()
			llmProxy := &kernel.LLMProxy{
				ProviderFactory: func(kind agent.Kind) llm.Provider {
					session, err := provider.OpenConfigured(cfg, string(kind), modelInput, *providerFlag, *apiKey, *baseURL, *model)
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
	runtime.Kernel.ToolRegistry = deps.BuildManagedRegistry(cfg, e, *brainID, func(reg tool.Registry) {
		deps.RegisterDelegateTool(reg, orch, e)
		deps.RegisterBridgeTools(reg, orch)
	})

	providerSession := provider.OpenMock(*reply)
	if !deps.WantsMockProvider(*providerFlag, modelInput) {
		if cfg == nil && !explicitProviderInput && os.Getenv("ANTHROPIC_API_KEY") == "" {
			if cfgErr != nil {
				fmt.Fprintf(os.Stderr, "brain run: %v\n", cfgErr)
			} else {
				deps.PrintConfigSetupGuide()
			}
			return cli.ExitFailed
		}
		var err error
		providerSession, err = provider.OpenConfigured(cfg, *brainID, modelInput, *providerFlag, *apiKey, *baseURL, *model)
		if err != nil {
			fmt.Fprintf(os.Stderr, "brain run: %v (use --api-key, ANTHROPIC_API_KEY env, or brain config set providers.<name>.api_key <key>)\n", err)
			deps.PrintConfigSetupGuide()
			return cli.ExitFailed
		}
	}

	runRec, err := runtime.RunStore.Create(*brainID, *prompt, string(mode), e.Workdir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "brain run: create run record: %v\n", err)
		return cli.ExitSoftware
	}

	systemPrompt := deps.BuildSystemPrompt(mode, e.Sandbox)
	if orch != nil {
		systemPrompt += deps.BuildOrchestratorPrompt(orch, runtime.Kernel.ToolRegistry)
	}

	// 创建 BatchPlanner（含 ResourceLocker），可选。
	var batchPlanner interface{}
	if deps.NewBatchPlanner != nil {
		batchPlanner = deps.NewBatchPlanner()
	}

	outcome, err := deps.ExecuteManagedRun(ctx, ManagedRunExecution{
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
		BatchPlanner:  batchPlanner,
	})

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

func failRun(err error, context string) int {
	var be *brainerrors.BrainError
	if errors.As(err, &be) {
		fmt.Fprintf(os.Stderr, "brain run: %s: [%s] %s\n", context, be.ErrorCode, be.Message)
	} else {
		fmt.Fprintf(os.Stderr, "brain run: %s: %v\n", context, err)
	}
	return cli.ExitFailed
}
