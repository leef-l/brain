package kernel

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/leef-l/brain/agent"
)

func TestOrchestratorCanDelegate_ResolvesKindsLazily(t *testing.T) {
	tmp := t.TempDir()
	binPath := filepath.Join(tmp, "brain-quant")
	if err := os.WriteFile(binPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	orch := NewOrchestrator(nil, nil, func(kind agent.Kind) (string, error) {
		if kind == agent.KindQuant {
			return binPath, nil
		}
		return "", os.ErrNotExist
	})

	if !orch.available[agent.KindQuant] {
		t.Fatalf("quant should be pre-probed into available map")
	}
	if !orch.CanDelegate(agent.KindQuant) {
		t.Fatalf("CanDelegate(quant) = false, want true")
	}
}

func TestHandleSpecialistCallToolFrom_UsesQuantAuthorizer(t *testing.T) {
	ctx := context.Background()
	runner := newScriptedRunner()
	runner.queue(agent.KindCentral, &scriptedSidecar{})

	orch := NewOrchestrator(runner, nil, func(kind agent.Kind) (string, error) {
		if kind == agent.KindCentral {
			return "/bin/brain-central", nil
		}
		return "", os.ErrNotExist
	})
	orch.available[agent.KindCentral] = true
	orch.SetSpecialistToolCallAuthorizer(NewStaticSpecialistToolCallAuthorizer([]SpecialistToolCallRule{
		{
			Caller:       agent.KindQuant,
			Target:       agent.KindCentral,
			ToolPrefixes: []string{"central.review_trade", "central.account_error"},
		},
	}))

	handler := orch.HandleSpecialistCallToolFrom(agent.KindQuant)
	if _, err := handler(ctx, []byte(`{
		"target_kind":"central",
		"tool_name":"central.data_alert",
		"arguments":{"type":"bad_route"}
	}`)); err == nil {
		t.Fatalf("HandleSpecialistCallToolFrom should reject quant -> central.data_alert")
	}
}
