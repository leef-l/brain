package sidecar

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/leef-l/brain/sdk/executionpolicy"
	"github.com/leef-l/brain/sdk/tool"
)

// ExecuteRequest is the payload of a brain/execute RPC call.
type ExecuteRequest struct {
	TaskID      string                         `json:"task_id"`
	Instruction string                         `json:"instruction"`
	Context     json.RawMessage                `json:"context,omitempty"`
	Budget      *ExecuteBudget                 `json:"budget,omitempty"`
	Execution   *executionpolicy.ExecutionSpec `json:"execution,omitempty"`
}

// ExecuteBudget constrains the sidecar Agent Loop.
type ExecuteBudget struct {
	MaxTurns int `json:"max_turns,omitempty"`
}

// ExecuteResult is the response returned after brain/execute completes.
type ExecuteResult struct {
	Status  string `json:"status"` // "completed", "failed"
	Summary string `json:"summary,omitempty"`
	Error   string `json:"error,omitempty"`
	Turns   int    `json:"turns"`
}

// --- LLM request/response types for reverse RPC ---

// llmRequest is the payload sent to the Kernel via llm.complete.
type llmRequest struct {
	System    []systemBlock `json:"system,omitempty"`
	Messages  []message     `json:"messages"`
	Tools     []toolSchema  `json:"tools,omitempty"`
	MaxTokens int           `json:"max_tokens,omitempty"`
}

type systemBlock struct {
	Text string `json:"text"`
}

type message struct {
	Role    string         `json:"role"`
	Content []contentBlock `json:"content"`
}

type contentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	ToolName  string          `json:"tool_name,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	Output    json.RawMessage `json:"output,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

type toolSchema struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// llmResponse is the payload received from the Kernel via llm.complete.
type llmResponse struct {
	ID         string         `json:"id"`
	Model      string         `json:"model"`
	StopReason string         `json:"stop_reason"`
	Content    []contentBlock `json:"content"`
}

// RunAgentLoop executes the sidecar-side Agent Loop:
//  1. Send instruction + history to LLM via llm.complete reverse RPC
//  2. Parse tool_use blocks from the response
//  3. Execute tools locally
//  4. Append tool_result to history
//  5. Repeat until no tool_use or max turns reached
//
// This is shared by code, verifier, and browser sidecars.
func RunAgentLoop(ctx context.Context, caller KernelCaller, registry tool.Registry,
	systemPrompt string, instruction string, maxTurns int) *ExecuteResult {

	if maxTurns <= 0 {
		maxTurns = 10
	}

	// Build tool schemas from registry.
	var tools []toolSchema
	for _, t := range registry.List() {
		s := t.Schema()
		tools = append(tools, toolSchema{
			Name:        s.Name,
			Description: s.Description,
			InputSchema: s.InputSchema,
		})
	}

	// Initial messages: user instruction.
	messages := []message{
		{
			Role: "user",
			Content: []contentBlock{
				{Type: "text", Text: instruction},
			},
		},
	}

	var lastReply string

	for turn := 0; turn < maxTurns; turn++ {
		// Call LLM via reverse RPC.
		req := llmRequest{
			System:    []systemBlock{{Text: systemPrompt}},
			Messages:  messages,
			Tools:     tools,
			MaxTokens: 4096,
		}

		var resp llmResponse
		if err := caller.CallKernel(ctx, "llm.complete", req, &resp); err != nil {
			return &ExecuteResult{
				Status: "failed",
				Error:  fmt.Sprintf("llm.complete: %v", err),
				Turns:  turn,
			}
		}

		// Extract text and tool_use blocks.
		var textParts []string
		var toolCalls []contentBlock
		for _, block := range resp.Content {
			switch block.Type {
			case "text":
				textParts = append(textParts, block.Text)
			case "tool_use":
				toolCalls = append(toolCalls, block)
			}
		}

		if len(textParts) > 0 {
			lastReply = ""
			for _, t := range textParts {
				if lastReply != "" {
					lastReply += "\n"
				}
				lastReply += t
			}
		}

		// Append assistant message.
		messages = append(messages, message{
			Role:    "assistant",
			Content: resp.Content,
		})

		// No tool calls → done.
		if len(toolCalls) == 0 || resp.StopReason == "end_turn" {
			return &ExecuteResult{
				Status:  "completed",
				Summary: lastReply,
				Turns:   turn + 1,
			}
		}

		// Execute each tool call.
		var toolResults []contentBlock
		for _, tc := range toolCalls {
			toolID := tc.ID
			if toolID == "" {
				toolID = tc.ToolUseID
			}
			toolName := tc.Name
			if toolName == "" {
				toolName = tc.ToolName
			}

			t, ok := registry.Lookup(toolName)
			if !ok {
				toolResults = append(toolResults, contentBlock{
					Type:      "tool_result",
					ToolUseID: toolID,
					Content:   marshalTextContent("tool not found: " + toolName),
					IsError:   true,
				})
				continue
			}

			brainLabel := toolName
			if tools := registry.List(); len(tools) > 0 {
				brainLabel = tools[0].Schema().Brain
			}
			fmt.Fprintf(os.Stderr, "  [%s] executing %s\n", brainLabel, toolName)

			result, err := t.Execute(ctx, tc.Input)
			if err != nil {
				toolResults = append(toolResults, contentBlock{
					Type:      "tool_result",
					ToolUseID: toolID,
					Content:   marshalTextContent("tool error: " + err.Error()),
					IsError:   true,
				})
				continue
			}

			toolResults = append(toolResults, contentBlock{
				Type:      "tool_result",
				ToolUseID: toolID,
				Content:   marshalToolOutput(result.Output),
				IsError:   result.IsError,
			})
		}

		// Append tool results as a user message.
		messages = append(messages, message{
			Role:    "user",
			Content: toolResults,
		})
	}

	return &ExecuteResult{
		Status:  "completed",
		Summary: lastReply,
		Turns:   maxTurns,
	}
}

// marshalTextContent builds a safe JSON content array with a single text block.
// It uses json.Marshal to escape special characters, preventing JSON injection.
func marshalTextContent(text string) json.RawMessage {
	escaped, _ := json.Marshal(text)
	return json.RawMessage(fmt.Sprintf(`[{"type":"text","text":%s}]`, escaped))
}

// marshalToolOutput builds a JSON content array wrapping a tool's raw output.
// If output is already valid JSON, it is embedded directly; otherwise it is
// treated as a plain text string and safely escaped.
func marshalToolOutput(output json.RawMessage) json.RawMessage {
	if json.Valid(output) {
		return json.RawMessage(fmt.Sprintf(`[{"type":"text","text":%s}]`, output))
	}
	return marshalTextContent(string(output))
}
