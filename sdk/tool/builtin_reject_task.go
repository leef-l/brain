package tool

import (
	"context"
	"encoding/json"

	brainerrors "github.com/leef-l/brain/sdk/errors"
)

// RejectTaskTool is a built-in tool for the central brain to reject a task
// with a reason. It is part of the Brain Kernel v1 specification described
// in 02-BrainKernel设计.md §6.3.
type RejectTaskTool struct {
	brainKind string
	onReject  func(ctx context.Context, reason string) error
}

// rejectTaskInput is the JSON-decoded input structure for RejectTaskTool.
type rejectTaskInput struct {
	Reason string `json:"reason"`
}

// NewRejectTaskTool constructs a RejectTaskTool registered to the given
// brainKind. The onReject callback is optional; if non-nil it will be
// invoked with the rejection reason when Execute is called.
func NewRejectTaskTool(brainKind string, onReject func(ctx context.Context, reason string) error) *RejectTaskTool {
	return &RejectTaskTool{
		brainKind: brainKind,
		onReject:  onReject,
	}
}

// Name returns the globally unique tool name in the format "{brainKind}.reject_task".
// See 02-BrainKernel设计.md §6.1 "命名规范铁律".
func (t *RejectTaskTool) Name() string {
	return t.brainKind + ".reject_task"
}

// Schema returns the LLM-facing schema for the reject_task tool. It requires
// a reason parameter. See 02-BrainKernel设计.md §6.1.
func (t *RejectTaskTool) Schema() Schema {
	return Schema{
		Name:        t.Name(),
		Description: "Reject the current task with a reason",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "reason": {
      "type": "string"
    }
  },
  "required": ["reason"]
}`),
		OutputSchema: rejectTaskOutputSchema,
		Brain:        t.brainKind,
	}
}

// Risk returns RiskLow because rejection is a low-impact action within
// the task domain. See 02-BrainKernel设计.md §6.
func (t *RejectTaskTool) Risk() Risk {
	return RiskLow
}

// Execute parses the input as a JSON object containing a reason field.
// If the reason is empty, it returns a BrainError with CodeToolInputInvalid.
// If onReject is non-nil, it calls the callback with the reason.
// On success, it returns a Result with {"status":"rejected","reason":"..."}.
// See 02-BrainKernel设计.md §6.1 and 22-Agent-Loop规格.md §7.
func (t *RejectTaskTool) Execute(ctx context.Context, args json.RawMessage) (*Result, error) {
	var input rejectTaskInput
	if err := json.Unmarshal(args, &input); err != nil {
		return nil, brainerrors.Wrap(err, brainerrors.CodeToolInputInvalid,
			brainerrors.WithMessage("failed to parse reject_task input"),
		)
	}

	if input.Reason == "" {
		return nil, brainerrors.New(brainerrors.CodeToolInputInvalid,
			brainerrors.WithMessage("reason must not be empty"),
		)
	}

	// Call the optional onReject hook
	if t.onReject != nil {
		if err := t.onReject(ctx, input.Reason); err != nil {
			return nil, err
		}
	}

	// Build response JSON
	response := map[string]interface{}{
		"status": "rejected",
		"reason": input.Reason,
	}
	outputBytes, _ := json.Marshal(response)

	return &Result{
		Output:  json.RawMessage(outputBytes),
		IsError: false,
	}, nil
}
