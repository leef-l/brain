package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/leef-l/brain/sdk/agent"
	"github.com/leef-l/brain/sdk/kernel"
	"github.com/leef-l/brain/sdk/tool"
)

func TestRegisterDelegateToolForEnvironment_AllowsWithFilePolicy(t *testing.T) {
	orch := testOrchestratorWithCodeSidecar(t)
	env := newExecutionEnvironment(t.TempDir(), modeAcceptEdits, nil, nil, false)
	if err := applyFilePolicy(env, &filePolicyInput{AllowEdit: []string{"*.go"}}); err != nil {
		t.Fatalf("applyFilePolicy: %v", err)
	}

	reg := tool.NewMemRegistry()
	registerDelegateToolForEnvironment(reg, orch, env)
	if _, ok := reg.Lookup("central.delegate"); !ok {
		t.Fatal("central.delegate should remain available when execution policy is propagated")
	}
}

func TestRegisterDelegateToolForEnvironment_AllowsWithoutFilePolicy(t *testing.T) {
	orch := testOrchestratorWithCodeSidecar(t)
	env := newExecutionEnvironment(t.TempDir(), modeAcceptEdits, nil, nil, false)

	reg := tool.NewMemRegistry()
	registerDelegateToolForEnvironment(reg, orch, env)
	if _, ok := reg.Lookup("central.delegate"); !ok {
		t.Fatal("central.delegate should be registered when no fine-grained file policy is active")
	}
}

func testOrchestratorWithCodeSidecar(t *testing.T) *kernel.Orchestrator {
	t.Helper()

	sidecarPath := filepath.Join(t.TempDir(), "brain-code")
	if err := os.WriteFile(sidecarPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}

	return kernel.NewOrchestrator(nil, nil, func(kind agent.Kind) (string, error) {
		if kind == agent.KindCode {
			return sidecarPath, nil
		}
		return "", fmt.Errorf("unsupported kind: %s", kind)
	})
}
