package main

import (
	"context"

	"github.com/leef-l/brain/cmd/brain/chat"
	cmds "github.com/leef-l/brain/cmd/brain/command"
	"github.com/leef-l/brain/cmd/brain/config"
	"github.com/leef-l/brain/sdk/events"
	"github.com/leef-l/brain/sdk/kernel"
	"github.com/leef-l/brain/sdk/llm"
	"github.com/leef-l/brain/sdk/loop"
)

func runRun(args []string) int {
	return cmds.RunRun(args, cmds.RunDeps{
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
		BuildManagedRegistry:    buildManagedRegistry,
		RegisterDelegateTool:    registerDelegateToolForEnvironment,
		RegisterBridgeTools:     registerSpecialistBridgeTools,
		BuildSystemPrompt:       chat.BuildSystemPrompt,
		BuildOrchestratorPrompt: chat.BuildOrchestratorPrompt,
		NewBatchPlanner: func() interface{} {
			lm := kernel.NewMemLeaseManager()
			return newBatchPlannerAdapter(lm)
		},
		ExecuteManagedRun: func(ctx context.Context, req cmds.ManagedRunExecution) (*cmds.ManagedRunOutcome, error) {
			mre := managedRunExecution{
				Runtime:       req.Runtime,
				Record:        req.Record,
				Registry:      req.Registry,
				Provider:      req.Provider,
				ProviderName:  req.ProviderName,
				ProviderModel: req.ProviderModel,
				BrainID:       req.BrainID,
				Prompt:        req.Prompt,
				MaxTurns:      req.MaxTurns,
				MaxDuration:   req.MaxDuration,
				Stream:        req.Stream,
				SystemPrompt:  req.SystemPrompt,
			}
			if req.EventBus != nil {
				if eb, ok := req.EventBus.(*events.MemEventBus); ok {
					mre.EventBus = eb
				}
			}
			if req.BatchPlanner != nil {
				if bp, ok := req.BatchPlanner.(loop.ToolBatchPlanner); ok {
					mre.BatchPlanner = bp
				}
			}
			outcome, err := executeManagedRun(ctx, mre)
			if err != nil {
				return nil, err
			}
			return &cmds.ManagedRunOutcome{
				ReplyText:   outcome.ReplyText,
				Summary:     outcome.Summary,
				SummaryJSON: outcome.SummaryJSON,
				PlanID:      outcome.PlanID,
				FinalStatus: outcome.FinalStatus,
			}, nil
		},
	})
}

// extractText concatenates every text block in a ContentBlock slice.
func extractText(blocks []llm.ContentBlock) string {
	out := ""
	for _, b := range blocks {
		if b.Type == "text" {
			out += b.Text
		}
	}
	return out
}
