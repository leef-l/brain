package chat

import (
	"encoding/json"
	"fmt"

	"github.com/leef-l/brain/cmd/brain/diff"
	"github.com/leef-l/brain/cmd/brain/env"
	"github.com/leef-l/brain/cmd/brain/term"
	"github.com/leef-l/brain/sdk/tool"
)

func HandleApprovalRequest(state *State, session *term.LineReadSession, kb *term.Keybindings,
	mode env.PermissionMode, providerName, model, workdir string,
	queueLines []string, running bool, req env.ApprovalRequest,
	stdinCh <-chan []byte, stdinErrCh <-chan error) {

	switch req.Kind {
	case env.ApprovalTool:
		if state.IsToolSessionApproved(req.ToolName) {
			select {
			case req.AnswerCh <- true:
			default:
			}
			return
		}
	case env.ApprovalSandbox:
		if state.IsSandboxEscapeApproved(req.OutsideDir) {
			select {
			case req.AnswerCh <- true:
			default:
			}
			return
		}
	}

	DetachPromptFrame(session)

	approved := false
	switch req.Kind {
	case env.ApprovalSandbox:
		fmt.Println("\033[1;31m! Sandbox: path is outside working directory\033[0m")
		if req.ToolName != "" {
			fmt.Printf("  \033[2mTool:\033[0m   %s\n", req.ToolName)
		}
		fmt.Printf("  \033[2mTarget:\033[0m %s\n", req.OutsideDir)
		fmt.Println()

		result := term.RunSelectorWithChan([]term.SelectorOption{
			{Label: "Allow once", Value: "once"},
			{Label: "Allow for this session", Value: "always"},
			{Label: "Deny", Value: "deny"},
			{Label: "Provide feedback...", Value: "feedback", IsInput: true},
		}, stdinCh, stdinErrCh)
		fmt.Println()

		switch result.Value {
		case "once":
			approved = true
		case "always":
			approved = true
			state.ApproveSandboxEscapeForSession(req.OutsideDir)
		}

	case env.ApprovalTool:
		riskLabel := "read"
		switch req.ToolRisk {
		case tool.RiskMedium:
			riskLabel = "write"
		case tool.RiskHigh:
			riskLabel = "execute"
		}
		fmt.Printf("\033[1;33m? AI wants to %s - %s\033[0m\n", riskLabel, req.ToolName)
		if preview := diff.BuildPreExecPreview(workdir, req.ToolName, req.Args, 14); len(preview) > 0 {
			PrintDiffPreviewBlock(preview)
		} else {
			printToolArgs(req.Args)
		}

		result := term.RunSelectorWithChan([]term.SelectorOption{
			{Label: "Allow", Value: "allow"},
			{Label: "Allow always for this session", Value: "always"},
			{Label: "Deny", Value: "deny"},
			{Label: "Provide feedback...", Value: "feedback", IsInput: true},
		}, stdinCh, stdinErrCh)
		fmt.Println()

		switch result.Value {
		case "allow":
			approved = true
		case "always":
			approved = true
			state.ApproveToolForSession(req.ToolName)
		}
	}

	select {
	case req.AnswerCh <- approved:
	default:
	}

	RenderPromptFrame(session, mode, providerName, model, workdir, queueLines, running)
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
