package tool

import (
	"context"
	"encoding/json"
	"fmt"
)

// TaskCompleteTool is a meta-tool that the orchestrator calls to explicitly
// signal that a task has been completed. It provides a clean completion signal
// so the agent loop can terminate successfully instead of looping on status
// checks or verification tools.
type TaskCompleteTool struct {
	brainKind string
}

// NewTaskCompleteTool creates a task completion tool for the given brain.
func NewTaskCompleteTool(brainKind string) *TaskCompleteTool {
	return &TaskCompleteTool{brainKind: brainKind}
}

func (t *TaskCompleteTool) Name() string { return t.brainKind + ".task_complete" }
func (t *TaskCompleteTool) Risk() Risk   { return RiskSafe }

func (t *TaskCompleteTool) Schema() Schema {
	return Schema{
		Name:        t.Name(),
		Description: "Signal that the current task is fully complete. Call this tool when you have finished all required work and there are no further actions needed. Provide a concise summary of what was accomplished.",
		Brain:       t.brainKind,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"summary": {
					"type": "string",
					"description": "A concise summary of what was accomplished in this task."
				}
			},
			"required": ["summary"]
		}`),
		OutputSchema: json.RawMessage(`true`),
	}
}

func (t *TaskCompleteTool) Execute(ctx context.Context, args json.RawMessage) (*Result, error) {
	var input struct {
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return &Result{
			Output:  json.RawMessage(fmt.Sprintf(`"invalid arguments: %s"`, err.Error())),
			IsError: true,
		}, nil
	}
	return &Result{
		Output: json.RawMessage(fmt.Sprintf(`{"status":"completed","summary":%q}`, input.Summary)),
	}, nil
}

var _ Tool = (*TaskCompleteTool)(nil)
