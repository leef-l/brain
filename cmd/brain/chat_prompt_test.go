package main

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/leef-l/brain/sdk/agent"
	"github.com/leef-l/brain/sdk/kernel"
	"github.com/leef-l/brain/sdk/tool"
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

func TestBuildOrchestratorPrompt_PrefersBrowserOverShellExecForWebTasks(t *testing.T) {
	root := t.TempDir()
	for _, item := range []struct {
		kind agent.Kind
		name string
	}{
		{kind: agent.KindBrowser, name: "brain-browser"},
		{kind: agent.KindCode, name: "brain-code"},
	} {
		path := root + "/" + item.name
		if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatalf("write %s sidecar: %v", item.kind, err)
		}
	}
	orch := kernel.NewOrchestrator(nil, nil, func(kind agent.Kind) (string, error) {
		switch kind {
		case agent.KindBrowser:
			return root + "/brain-browser", nil
		case agent.KindCode:
			return root + "/brain-code", nil
		default:
			return "", fmt.Errorf("unsupported kind: %s", kind)
		}
	})

	env := newExecutionEnvironment(t.TempDir(), modeAcceptEdits, nil, nil, false)
	reg := tool.NewMemRegistry()
	registerDelegateToolForEnvironment(reg, orch, env)

	prompt := buildOrchestratorPrompt(orch, reg)
	if !strings.Contains(prompt, "delegate to the browser brain instead of using shell_exec + curl/wget") {
		t.Fatalf("expected browser-over-shell guidance, got %q", prompt)
	}
	if !strings.Contains(prompt, "Never treat shell_exec HTTP fetches as a substitute for browser delegation") {
		t.Fatalf("expected explicit curl/wget prohibition, got %q", prompt)
	}
	if !strings.Contains(prompt, "report the browser failure clearly instead of retrying the same web task through shell_exec") {
		t.Fatalf("expected no-shell fallback guidance after browser failure, got %q", prompt)
	}
}
