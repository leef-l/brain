package main

import (
	"strings"
	"testing"

	"github.com/leef-l/brain/tool"
)

func TestBuildOrchestratorPrompt_OmitsDelegateInstructionsWhenToolUnavailable(t *testing.T) {
	orch := testOrchestratorWithCodeSidecar(t)
	reg := tool.NewMemRegistry()

	prompt := buildOrchestratorPrompt(orch, reg)
	if prompt != "" {
		t.Fatalf("expected empty prompt when central.delegate is unavailable, got %q", prompt)
	}
}

func TestBuildOrchestratorPrompt_IncludesDelegateInstructionsWhenToolAvailable(t *testing.T) {
	orch := testOrchestratorWithCodeSidecar(t)
	env := newExecutionEnvironment(t.TempDir(), modeAcceptEdits, nil, nil, false)
	reg := tool.NewMemRegistry()
	registerDelegateToolForEnvironment(reg, orch, env)

	prompt := buildOrchestratorPrompt(orch, reg)
	if !strings.Contains(prompt, "Use the `central.delegate` tool") {
		t.Fatalf("expected delegation instructions in prompt, got %q", prompt)
	}
}
