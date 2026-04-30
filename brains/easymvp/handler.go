package easymvp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/leef-l/brain/sdk/agent"
	"github.com/leef-l/brain/sdk/sidecar"
)

// Handler implements sidecar.BrainHandler for the easymvp domain brain.
type Handler struct {
	caller sidecar.KernelCaller
}

var _ sidecar.BrainHandler = (*Handler)(nil)
var _ sidecar.RichBrainHandler = (*Handler)(nil)

func NewHandler() *Handler {
	return &Handler{}
}

func (h *Handler) Kind() agent.Kind { return agent.KindEasyMVP }
func (h *Handler) Version() string  { return "1.0.0" }

func (h *Handler) Tools() []string {
	return nil
}

func (h *Handler) SetKernelCaller(caller sidecar.KernelCaller) {
	h.caller = caller
}

func (h *Handler) HandleMethod(ctx context.Context, method string, params json.RawMessage) (interface{}, error) {
	switch method {
	case "brain/execute":
		return h.handleExecute(ctx, params)
	}
	return nil, sidecar.ErrMethodNotFound
}

func (h *Handler) handleExecute(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var req struct {
		Instruction string          `json:"instruction"`
		Context     json.RawMessage `json:"context"`
		ExecutionID string          `json:"execution_id,omitempty"`
		Budget      struct {
			MaxTurns int `json:"max_turns"`
		} `json:"budget"`
	}
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, fmt.Errorf("parse params: %w", err)
	}

	contractKind := extractContractKind(req.Instruction)
	fmt.Fprintf(os.Stderr, "easymvp: handleExecute contractKind=%s execution_id=%s\n", contractKind, req.ExecutionID)
	if contractKind == "" {
		return nil, fmt.Errorf("cannot extract contract kind from instruction: %s", req.Instruction)
	}

	switch contractKind {
	// architect_chat is an INTERNAL AUXILIARY capability, not part of the
	// formal 5-core-contract set. It is kept for conversational UX but
	// should NOT be advertised in brain.json capabilities.
	case "architect_chat":
		return h.handleArchitectChat(ctx, req.Context, req.ExecutionID)
	case "plan_review":
		return h.handlePlanReview(ctx, req.Context)
	case "plan_compile":
		return h.handlePlanCompile(ctx, req.Context)
	case "plan_redesign":
		return h.handlePlanRedesign(ctx, req.Context)
	case "repair_design":
		return h.handleRepairDesign(ctx, req.Context)
	case "acceptance_mapping":
		return h.handleAcceptanceMapping(ctx, req.Context)
	case "completion_adjudication":
		return h.handleCompletionAdjudication(ctx, req.Context)
	case "workspace_explanation":
		return h.handleWorkspaceExplanation(ctx, req.Context)
	case "requirement_analysis":
		return h.handleRequirementAnalysis(ctx, req.Context, req.ExecutionID)
	case "solution_design":
		return h.handleSolutionDesign(ctx, req.Context, req.ExecutionID)
	case "design_review":
		return h.handleDesignReview(ctx, req.Context)
	case "design_fix":
		return h.handleDesignFix(ctx, req.Context)
	default:
		return nil, fmt.Errorf("unsupported contract kind: %s", contractKind)
	}
}

func extractContractKind(instruction string) string {
	// Format: "[contract:architect_chat] ..."
	if !strings.HasPrefix(instruction, "[contract:") {
		return ""
	}
	end := strings.Index(instruction, "]")
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(instruction[len("[contract:"):end])
}
