package main

import (
	"os"

	"github.com/leef-l/brain/cmd/brain/bridge"
	"github.com/leef-l/brain/cmd/brain/chat"
	"github.com/leef-l/brain/cmd/brain/cliruntime"
	"github.com/leef-l/brain/cmd/brain/config"
	"github.com/leef-l/brain/cmd/brain/provider"
	"github.com/leef-l/brain/sdk/kernel"
)

func init() {
	// /verbose 切换时同步 cliruntime 的 shell stream 开关
	chat.RegisterVerboseHook(func(on bool) {
		cliruntime.SetVerboseShellStream(on)
	})

	// MACCS 学习闭环：每次 RunChat 启动时把持久化 learner 注入 estimator，
	// 让 estimator 查历史 turn 数据估算（而不是退化为 heuristic）。
	chat.RegisterEstimatorInjector(func(learner interface{}) {
		le, _ := learner.(*kernel.LearningEngine)
		bridge.SetDelegateEstimator(kernel.NewComplexityEstimator(le))
	})
	// 默认 nil learner（如 chat 没启动 / 没持久化 store 时），保证 bridge 有 fallback estimator
	bridge.SetDelegateEstimator(kernel.NewComplexityEstimator(nil))

	// 把 bridge 包的钩子接到 chat 包的实现：
	//   - VerbosePrint：/verbose 模式下显示 bridge 工具的"啰嗦提示"
	//   - WorkflowProgressHook：workflow 节点状态喂给当前 chat Activity 的 Todos 面板
	bridge.VerbosePrint = func(line string) {
		if chat.VerboseEnabled() {
			_, _ = os.Stderr.WriteString(line)
		}
	}
	bridge.WorkflowProgressHook = func(event, nodeID, nodeName, brainKind, detail string) {
		switch event {
		case "init":
			chat.EmitWorkflowNodeInit(nodeID, nodeName, brainKind)
		case "running":
			chat.EmitWorkflowNodeState(nodeID, "running", "")
		case "completed":
			chat.EmitWorkflowNodeState(nodeID, "done", "")
		case "failed":
			chat.EmitWorkflowNodeState(nodeID, "failed", detail)
		case "skipped":
			chat.EmitWorkflowNodeState(nodeID, "skipped", "")
		}
	}

	chat.Init(chat.Deps{
		LoadConfig:              loadConfig,
		ParseModelConfigJSON:    config.ParseModelConfigJSON,
		ParseFilePolicyJSON:     config.ParseFilePolicyJSON,
		ResolveFilePolicyInput:  resolveFilePolicyInput,
		HasModelConfigOverrides: config.HasModelConfigOverrides,
		WantsMockProvider:       config.WantsMockProvider,
		PrintConfigSetupGuide:   printConfigSetupGuide,
		ResolvePermissionMode:   resolvePermissionMode,
		NewExecutionEnv:         newExecutionEnvironment,
		ApplyFilePolicy:         applyFilePolicy,
		BuildBrainPool:          buildBrainPool,
		DefaultBinResolver:      defaultBinResolver,
		NewDefaultCLIRuntime:    newDefaultCLIRuntime,
		SaveRunCheckpoint:       saveRunCheckpoint,
		SaveRunUsage:            saveRunUsage,
		SaveRunPlan:             saveRunPlan,
		RegisterDelegateTool:    registerDelegateToolForEnvironment,
		RegisterBridgeTools:     registerSpecialistBridgeTools,
		RegisterWorkflowTool:    registerWorkflowToolForEnvironment,
		OpenConfiguredProvider:  provider.OpenConfigured,
		OpenMockProvider:        provider.OpenMock,
	})
}

var (
	buildSystemPrompt       = chat.BuildSystemPrompt
	buildOrchestratorPrompt = chat.BuildOrchestratorPrompt
	registryHasTool         = chat.RegistryHasTool
	loadHistory             = chat.LoadHistory
	saveHistory             = chat.SaveHistory
	appendHistory           = chat.AppendHistory
	allSlashCommands        = chat.AllSlashCommands
	slashCompletionLines    = chat.SlashCompletionLines
)

func runChat(args []string) int {
	return chat.RunChat(args)
}
