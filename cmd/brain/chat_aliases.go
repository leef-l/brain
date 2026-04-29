package main

import (
	"github.com/leef-l/brain/cmd/brain/chat"
	"github.com/leef-l/brain/cmd/brain/config"
	"github.com/leef-l/brain/cmd/brain/provider"
)

func init() {
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
