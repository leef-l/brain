package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/leef-l/brain/sdk/tool"
)

type approvalKind string

const (
	approvalSandbox approvalKind = "sandbox"
	approvalTool    approvalKind = "tool"
)

type approvalRequest struct {
	kind       approvalKind
	toolName   string
	toolRisk   tool.Risk
	args       json.RawMessage
	outsideDir string
	answerCh   chan bool
}

type approvalPrompter func(ctx context.Context, req approvalRequest) bool

func handleApprovalRequest(session *lineReadSession, kb *keybindings,
	mode chatMode, providerName, model, workdir string,
	queueLines []string, running bool, req approvalRequest,
	stdinCh <-chan []byte, stdinErrCh <-chan error) {

	detachPromptFrame(session)

	approved := false
	switch req.kind {
	case approvalSandbox:
		fmt.Println("\033[1;31m! Sandbox: path is outside working directory\033[0m")
		if req.toolName != "" {
			fmt.Printf("  \033[2mTool:\033[0m   %s\n", req.toolName)
		}
		fmt.Printf("  \033[2mTarget:\033[0m %s\n", req.outsideDir)
		fmt.Println()

		result := RunSelectorWithChan([]SelectorOption{
			{Label: "Allow once", Value: "once"},
			{Label: "Allow for this session", Value: "always"},
			{Label: "Deny", Value: "deny"},
			{Label: "Provide feedback...", Value: "feedback", IsInput: true},
		}, stdinCh, stdinErrCh)
		fmt.Println()

		switch result.Value {
		case "once", "always":
			approved = true
		}

	case approvalTool:
		riskLabel := "read"
		switch req.toolRisk {
		case tool.RiskMedium:
			riskLabel = "write"
		case tool.RiskHigh:
			riskLabel = "execute"
		}
		fmt.Printf("\033[1;33m? AI wants to %s - %s\033[0m\n", riskLabel, req.toolName)
		if preview := buildPreExecPreview(workdir, req.toolName, req.args, 14); len(preview) > 0 {
			printDiffPreviewBlock(preview)
		} else {
			printToolArgs(req.args)
		}

		result := RunSelectorWithChan([]SelectorOption{
			{Label: "Allow", Value: "allow"},
			{Label: "Allow always for this session", Value: "always"},
			{Label: "Deny", Value: "deny"},
			{Label: "Provide feedback...", Value: "feedback", IsInput: true},
		}, stdinCh, stdinErrCh)
		fmt.Println()

		switch result.Value {
		case "allow", "always":
			approved = true
		}
	}

	select {
	case req.answerCh <- approved:
	default:
	}

	renderPromptFrame(session, mode, providerName, model, workdir, queueLines, running)
}

func printToolArgs(args json.RawMessage) {
	var m map[string]interface{}
	if json.Unmarshal(args, &m) != nil {
		return
	}
	for k, v := range m {
		s := fmt.Sprintf("%v", v)
		if len(s) > 80 {
			s = s[:77] + "..."
		}
		fmt.Printf("  \033[2m%s:\033[0m %s\n", k, s)
	}
}
