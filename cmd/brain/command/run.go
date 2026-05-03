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
	"github.com/leef-l/brain/sdk/loop"
	"github.com/leef-l/brain/sdk/persistence"
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

	// Orchestrator 让 run 模式能根据用户输入意图自动分流到 PlanOrchestrator。
	// 项目级需求(IntentProject)走七阶段闭环;简单单步任务直接 Runner.Execute。
	// nil 时退化为永远 simple 路径(向后兼容 mock / solo 场景)。
	Orchestrator *kernel.Orchestrator
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
	workflowFlag := fs.String("workflow", "", "path to workflow JSON file (DAG execution mode)")
	// MACCS Wave 7+ 项目级持久化(run 模式)
	projectFlag := fs.String("project", "", "project name (find or create in current workdir)")
	noProjectFlag := fs.Bool("no-project", false, "do not persist this run to any project")
	if err := fs.Parse(args); err != nil {
		return cli.ExitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "brain run: unexpected argument %q\n", fs.Arg(0))
		return cli.ExitUsage
	}

	cfg, cfgErr := deps.LoadConfig()
	config.ApplyDiagnosticEnv(cfg)
	// 同步 debug 开关(从 config.diagnostics.debug.* 持久化配置)
	loop.DebugRunner = cfg.DebugRunnerEnabled()
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
			// 修复:helpers.buildBrainPool 用 os.Getwd 兜底,
			// run 拿到 pool 后必须用 e.Workdir 覆盖,确保 sidecar cwd = sandbox 解析后的 workdir。
			pool.SetRunnerWorkdir(e.Workdir)
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
			var learner *kernel.LearningEngine
			if runtime.Stores != nil && runtime.Stores.LearningStore != nil {
				learner = kernel.NewLearningEngineWithStore(runtime.Stores.LearningStore)
				if err := learner.Load(context.Background()); err != nil {
					fmt.Fprintf(os.Stderr, "brain run: warning: load learning data: %v\n", err)
				}
			} else {
				learner = kernel.NewLearningEngine()
			}
			defer func() {
				_ = learner.Save(context.Background())
			}()
			// 上下文引擎：注入 LLM Summarizer
			ctxEngine := kernel.NewDefaultContextEngine()
			if session, err := provider.OpenConfigured(cfg, "central", modelInput, *providerFlag, *apiKey, *baseURL, *model); err == nil {
				ctxEngine.Summarizer = session.Provider
				ctxEngine.SummaryModel = session.Model
			}

			leaseManager := kernel.NewMemLeaseManager()
			// Workdir:见 chat/repl.go 同样位置注释。让 sidecar 写相对路径落到用户目录。
			// WithDefaultExecution 把 host 的 file_policy 注入,确保 PlanOrchestrator
			// 等派发路径下子 sidecar 继承同一份执行边界(文档 27 §6.2 MUST)。
			orch = kernel.NewOrchestratorWithPool(pool, &kernel.ProcessRunner{BinResolver: deps.DefaultBinResolver(), Workdir: e.Workdir}, llmProxy, deps.DefaultBinResolver(), kernel.OrchestratorConfig{},
				kernel.WithSemanticApprover(&kernel.DefaultSemanticApprover{}),
				kernel.WithLearningEngine(learner),
				kernel.WithContextEngine(ctxEngine),
				kernel.WithLeaseManager(leaseManager),
				kernel.WithDefaultExecution(e.ExecutionSpec()),
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

	// Workflow 模式：直接执行 DAG，不走单 run loop
	if *workflowFlag != "" {
		if orch == nil {
			fmt.Fprintln(os.Stderr, "brain run: workflow mode requires central brain with pool")
			return cli.ExitUsage
		}
		data, err := os.ReadFile(*workflowFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "brain run: read workflow file: %v\n", err)
			return cli.ExitUsage
		}
		var wf kernel.Workflow
		if err := json.Unmarshal(data, &wf); err != nil {
			fmt.Fprintf(os.Stderr, "brain run: parse workflow JSON: %v\n", err)
			return cli.ExitUsage
		}
		if wf.ID == "" {
			wf.ID = fmt.Sprintf("wf-cli-%d", time.Now().UnixNano())
		}

		reporter := func(eventType, nodeID, status, output, errMsg string) {
			switch eventType {
			case "workflow.node.started":
				fmt.Fprintf(os.Stderr, "[workflow] node %s started\n", nodeID)
			case "workflow.node.completed":
				fmt.Fprintf(os.Stderr, "[workflow] node %s completed\n", nodeID)
			case "workflow.node.failed":
				fmt.Fprintf(os.Stderr, "[workflow] node %s failed: %s\n", nodeID, errMsg)
			}
		}

		result, err := orch.ExecuteWorkflow(ctx, &wf, reporter)
		if err != nil {
			return failRun(err, "execute workflow")
		}

		if *jsonOut {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			_ = enc.Encode(result)
		} else {
			fmt.Printf("workflow %s %s\n", wf.ID, result.State)
			for nid, nr := range result.Nodes {
				statusIcon := "✓"
				if nr.State != kernel.StateCompleted {
					statusIcon = "✗"
				}
				fmt.Printf("  %s %s: %s\n", statusIcon, nid, nr.State)
				if nr.Error != "" {
					fmt.Printf("      error: %s\n", nr.Error)
				}
			}
		}
		return cli.ExitOK
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
		Orchestrator:  orch, // 用于 IntentClassifier → PlanRunner 分流
	})

	if err != nil {
		return failRun(err, "execute run")
	}

	// MACCS Wave 7+ 项目级持久化(run 模式):
	// 仅在 --no-project=false (默认) 且 stores 配置就绪时执行。
	// 默认行为:
	//   --project NAME 显式指定 -> 在当前 workdir 找/建该项目
	//   --no-project   显式禁用 -> 跳过持久化(单 run 兼容旧行为)
	//   都不传        -> 跳过持久化(避免污染未声明意图的目录)
	if !*noProjectFlag && *projectFlag != "" && runtime != nil && runtime.Stores != nil &&
		runtime.Stores.ProjectsStore != nil && runtime.Stores.ProjectStore != nil {
		persistRunToProject(ctx, runtime.Stores, *projectFlag, e.Workdir, *prompt, outcome)
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

// persistRunToProject 把单次 run 的对话写入项目持久化层。
// 项目不存在时按 (workdir, name) 自动创建。任意失败 silent。
func persistRunToProject(ctx context.Context, stores *persistence.ClosableStores,
	projectName, workdir, prompt string, outcome *ManagedRunOutcome) {
	if stores == nil || projectName == "" || workdir == "" {
		return
	}
	// 找/建项目
	p, _ := stores.ProjectsStore.FindByName(ctx, workdir, projectName)
	if p == nil {
		p = &persistence.ProjectMeta{Workdir: workdir, Name: projectName}
		if err := stores.ProjectsStore.Create(ctx, p); err != nil {
			fmt.Fprintf(os.Stderr, "brain run: create project: %v\n", err)
			return
		}
	}

	// 写 user + assistant 终态
	msgs := []llm.Message{
		{Role: "user", Content: []llm.ContentBlock{{Type: "text", Text: prompt}}},
	}
	if outcome != nil && outcome.ReplyText != "" {
		msgs = append(msgs, llm.Message{
			Role:    "assistant",
			Content: []llm.ContentBlock{{Type: "text", Text: outcome.ReplyText}},
		})
	}
	if err := stores.ProjectStore.SaveMessages(ctx, p.ID, msgs); err != nil {
		fmt.Fprintf(os.Stderr, "brain run: persist project messages: %v\n", err)
		return
	}
	_ = stores.ProjectsStore.UpdateLastActive(ctx, p.ID, time.Now())
}

// budgetExhaustedCodes 是所有 budget 类错误码的集合。
// 命中任一 → CLI 返回 ExitBudgetExhausted(3),与 27-CLI命令契约 §6.6 + §18 对齐。
var budgetExhaustedCodes = map[string]struct{}{
	brainerrors.CodeBudgetTurnsExhausted:     {},
	brainerrors.CodeBudgetCostExhausted:      {},
	brainerrors.CodeBudgetToolCallsExhausted: {},
	brainerrors.CodeBudgetLLMCallsExhausted:  {},
	brainerrors.CodeBudgetTimeoutExhausted:   {},
}

func failRun(err error, ctxLabel string) int {
	// 优先按错误类型决定退出码,与 27 §18 v1 冻结契约对齐:
	//   2 = canceled       (用户中断 / context.Canceled)
	//   3 = budget exhausted(任一 budget_* 错误码命中)
	//   1 = ExitFailed     (其他失败)
	if errors.Is(err, context.Canceled) {
		fmt.Fprintf(os.Stderr, "brain run: %s: canceled\n", ctxLabel)
		return cli.ExitCanceled
	}
	var be *brainerrors.BrainError
	if errors.As(err, &be) {
		fmt.Fprintf(os.Stderr, "brain run: %s: [%s] %s\n", ctxLabel, be.ErrorCode, be.Message)
		if _, isBudget := budgetExhaustedCodes[be.ErrorCode]; isBudget {
			return cli.ExitBudgetExhausted
		}
	} else {
		fmt.Fprintf(os.Stderr, "brain run: %s: %v\n", ctxLabel, err)
	}
	return cli.ExitFailed
}
