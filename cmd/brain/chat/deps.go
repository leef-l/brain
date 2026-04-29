package chat

import (
	"context"
	"encoding/json"

	"github.com/leef-l/brain/cmd/brain/cliruntime"
	"github.com/leef-l/brain/cmd/brain/config"
	"github.com/leef-l/brain/cmd/brain/env"
	"github.com/leef-l/brain/cmd/brain/provider"
	"github.com/leef-l/brain/sdk/agent"
	"github.com/leef-l/brain/sdk/kernel"
	"github.com/leef-l/brain/sdk/llm"
	"github.com/leef-l/brain/sdk/loop"
	"github.com/leef-l/brain/sdk/tool"
	"github.com/leef-l/brain/sdk/toolpolicy"
)

// Deps bundles all external dependencies that the chat package cannot
// import directly (because they live in the main package or would cause
// circular imports). The main package injects concrete implementations.
type Deps struct {
	LoadConfig            func() (*config.Config, error)
	ParseModelConfigJSON  func(raw string) (*config.ModelConfigInput, error)
	ParseFilePolicyJSON   func(raw string) (*config.FilePolicyInput, error)
	ResolveFilePolicyInput func(cfg *config.Config, input *config.FilePolicyInput) *config.FilePolicyInput
	HasModelConfigOverrides func(input *config.ModelConfigInput) bool
	WantsMockProvider     func(flag string, input *config.ModelConfigInput) bool
	PrintConfigSetupGuide func()
	ResolvePermissionMode func(flagValue string, cfg *config.Config, preferChatMode ...bool) (env.PermissionMode, error)
	NewExecutionEnv       func(workdir string, mode env.PermissionMode, cfg *config.Config, approver env.ApprovalPrompter, interactive bool) *env.Environment
	ApplyFilePolicy       func(e *env.Environment, input *config.FilePolicyInput) error
	BuildBrainPool        func(cfg *config.Config) *kernel.ProcessBrainPool
	DefaultBinResolver    func() func(kind agent.Kind) (string, error)
	NewDefaultCLIRuntime  func(brainKind string) (*cliruntime.Runtime, error)
	SaveRunCheckpoint     func(ctx context.Context, k *cliruntime.Runtime, rec *cliruntime.RunRecord, state string, turnIndex int, turnUUID string) error
	SaveRunUsage          func(ctx context.Context, k *cliruntime.Runtime, rec *cliruntime.RunRecord, provider, model string, result *loop.RunResult) error
	SaveRunPlan           func(ctx context.Context, k *cliruntime.Runtime, rec *cliruntime.RunRecord, payload map[string]interface{}) (int64, error)

	// Bridge tool registration (still injected because bridge/ is a sibling package)
	RegisterDelegateTool func(reg tool.Registry, orch *kernel.Orchestrator, e *env.Environment)
	RegisterBridgeTools  func(reg tool.Registry, orch *kernel.Orchestrator)
	RegisterWorkflowTool func(reg tool.Registry, orch *kernel.Orchestrator)

	// Provider
	OpenConfiguredProvider func(cfg *config.Config, brainKind string, input *config.ModelConfigInput, flagProvider, flagKey, flagURL, flagModel string) (provider.Session, error)
	OpenMockProvider       func(reply string) provider.Session
}

// deps is the package-level dependency holder, set once by Init.
var deps Deps

// Init sets the package-level dependencies. Must be called before RunChat.
func Init(d Deps) {
	deps = d
}

// filterRegistryWithConfig applies tool policy filtering.
func filterRegistryWithConfig(reg tool.Registry, cfg *config.Config, scopes ...string) tool.Registry {
	return toolpolicy.FilterRegistry(reg, config.PolicyConfig(cfg), scopes...)
}

// BuildToolSchemas extracts ToolSchema list from a registry.
func BuildToolSchemas(reg tool.Registry) []llm.ToolSchema {
	var schemas []llm.ToolSchema
	for _, t := range reg.List() {
		s := t.Schema()
		schemas = append(schemas, llm.ToolSchema{
			Name:        s.Name,
			Description: s.Description,
			InputSchema: s.InputSchema,
		})
	}
	return schemas
}

// fmtJSON marshals any value to JSON bytes.
func fmtJSON(v interface{}) []byte {
	raw, _ := json.Marshal(v)
	return raw
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
