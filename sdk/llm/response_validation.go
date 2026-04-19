package llm

import (
	"fmt"
	"strings"

	brainerrors "github.com/leef-l/brain/sdk/errors"
)

// ValidateToolUseResponse enforces the minimum tool_use wire contract shared by
// all providers. Malformed tool_use payloads are treated as retryable upstream
// provider errors so callers can transparently retry instead of dispatching an
// empty tool name into the runner.
func ValidateToolUseResponse(provider string, resp *ChatResponse) error {
	if resp == nil {
		return nil
	}

	var toolUseCount int
	for _, block := range resp.Content {
		if block.Type != "tool_use" {
			continue
		}
		toolUseCount++
		switch {
		case strings.TrimSpace(block.ToolUseID) == "":
			return malformedToolUseError(provider, "missing tool_use_id")
		case strings.TrimSpace(block.ToolName) == "":
			return malformedToolUseError(provider, "missing tool_name")
		}
	}

	if resp.StopReason == "tool_use" && toolUseCount == 0 {
		return malformedToolUseError(provider, "stop_reason=tool_use without tool_use block")
	}

	return nil
}

func malformedToolUseError(provider, detail string) error {
	return brainerrors.New(brainerrors.CodeLLMUpstream5xx,
		brainerrors.WithMessage(fmt.Sprintf("%s: malformed tool_use response: %s", provider, detail)),
	)
}
