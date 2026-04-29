package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/leef-l/brain/sdk/kernel"
	"github.com/leef-l/brain/sdk/tool"
)

// WorkflowTool lets the LLM submit a DAG workflow for parallel execution.
type WorkflowTool struct {
	Orchestrator *kernel.Orchestrator
	Reporter     kernel.WorkflowNodeReporter // optional progress reporter
}

var _ tool.Tool = (*WorkflowTool)(nil)

func (t *WorkflowTool) Name() string { return "central.submit_workflow" }

func (t *WorkflowTool) Schema() tool.Schema {
	return tool.Schema{
		Name:        "central.submit_workflow",
		Description: "Submit a DAG workflow to parallelize multiple independent brain tasks. Each node runs on a specific brain (e.g. code, browser, data). Nodes at the same layer execute in parallel. Use this when the user's request naturally splits into several subtasks that can run simultaneously or in a specific order.",
		Brain:       "central",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"workflow": {
					"type": "object",
					"description": "Workflow DAG definition. Example: {\"id\":\"wf-1\",\"nodes\":[{\"id\":\"A\",\"brain_id\":\"code\",\"prompt\":\"write tests\"},{\"id\":\"B\",\"brain_id\":\"browser\",\"prompt\":\"open example.com\"}],\"edges\":[]}",
					"properties": {
						"id": {"type": "string"},
						"nodes": {
							"type": "array",
							"items": {
								"type": "object",
								"properties": {
									"id": {"type": "string"},
									"brain_id": {"type": "string", "description": "Target brain kind: code, browser, data, verifier, quant, fault"},
									"prompt": {"type": "string", "description": "Task instruction for this node"},
									"depends_on": {"type": "array", "items": {"type": "string"}, "description": "IDs of nodes that must complete before this one starts"},
									"required_caps": {"type": "array", "items": {"type": "string"}},
									"preferred_caps": {"type": "array", "items": {"type": "string"}},
									"task_type": {"type": "string"}
								},
								"required": ["id", "brain_id", "prompt"]
							}
						},
						"edges": {
							"type": "array",
							"items": {
								"type": "object",
								"properties": {
									"from": {"type": "string"},
									"to": {"type": "string"},
									"mode": {"type": "string", "enum": ["materialized", "streaming"], "description": "materialized = wait for full output; streaming = pass output as it arrives"}
								},
								"required": ["from", "to", "mode"]
							}
						}
					},
					"required": ["id", "nodes"]
				}
			},
			"required": ["workflow"]
		}`),
		OutputSchema: json.RawMessage(`true`),
	}
}

func (t *WorkflowTool) Risk() tool.Risk { return tool.RiskMedium }

func (t *WorkflowTool) Execute(ctx context.Context, args json.RawMessage) (*tool.Result, error) {
	var input struct {
		Workflow kernel.Workflow `json:"workflow"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return &tool.Result{
			Output:  json.RawMessage(fmt.Sprintf(`"invalid arguments: %s"`, err.Error())),
			IsError: true,
		}, nil
	}

	if len(input.Workflow.Nodes) == 0 {
		return &tool.Result{
			Output:  json.RawMessage(`"workflow has no nodes"`),
			IsError: true,
		}, nil
	}
	if input.Workflow.ID == "" {
		input.Workflow.ID = fmt.Sprintf("wf-tool-%d", time.Now().UnixNano())
	}

	reporter := t.Reporter
	if reporter == nil {
		reporter = func(eventType, nodeID, status, output, errMsg string) {
			switch eventType {
			case "workflow.node.started":
				fmt.Printf("\033[2m    → workflow node %s started\033[0m\n", nodeID)
			case "workflow.node.completed":
				fmt.Printf("\033[2m    → workflow node %s completed\033[0m\n", nodeID)
			case "workflow.node.failed":
				fmt.Printf("\033[2m    → workflow node %s failed: %s\033[0m\n", nodeID, errMsg)
			}
		}
	}

	fmt.Printf("\033[2m    → submitting workflow %s (%d nodes)...\033[0m\n", input.Workflow.ID, len(input.Workflow.Nodes))

	result, err := t.Orchestrator.ExecuteWorkflow(ctx, &input.Workflow, reporter)
	if err != nil {
		return &tool.Result{
			Output:  json.RawMessage(fmt.Sprintf(`"workflow execution failed: %s"`, err.Error())),
			IsError: true,
		}, nil
	}

	// Build a concise summary for the LLM.
	summary := map[string]interface{}{
		"workflow_id": input.Workflow.ID,
		"state":       result.State,
		"nodes":       make(map[string]interface{}, len(result.Nodes)),
	}
	for nid, nr := range result.Nodes {
		nodeSummary := map[string]interface{}{
			"state": nr.State,
		}
		if nr.Error != "" {
			nodeSummary["error"] = nr.Error
		} else {
			// Truncate large outputs for LLM context efficiency.
			out := nr.Output
			if len(out) > 800 {
				out = out[:800] + "... (truncated)"
			}
			nodeSummary["output"] = out
		}
		summary["nodes"].(map[string]interface{})[nid] = nodeSummary
	}

	summaryJSON, _ := json.Marshal(summary)
	return &tool.Result{Output: summaryJSON}, nil
}

// RegisterWorkflowTool registers the central.submit_workflow tool if orchestrator is available.
func RegisterWorkflowTool(reg tool.Registry, orch *kernel.Orchestrator, reporter kernel.WorkflowNodeReporter) {
	if reg == nil || orch == nil {
		return
	}
	_ = reg.Register(&WorkflowTool{Orchestrator: orch, Reporter: reporter})
}
