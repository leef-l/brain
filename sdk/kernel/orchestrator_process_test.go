package kernel

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/leef-l/brain/sdk/agent"
	"github.com/leef-l/brain/sdk/llm"
	"github.com/leef-l/brain/sdk/sidecar"
)

func TestOrchestratorDelegate_ProcessRunner_RealBrainCodeBinary(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subprocess integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tmp := t.TempDir()
	binPath := filepath.Join(tmp, "brain-code")
	build := exec.CommandContext(ctx, "go", "build", "-o", binPath, "./brains/code/cmd")
	build.Dir = filepath.Clean(filepath.Join("..", ".."))
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build brain-code: %v\n%s", err, out)
	}

	cfgPath := filepath.Join(tmp, "config.json")
	cfgJSON := `{
  "tool_profiles": {
    "delegated_code": {
      "include": ["code.*"]
    }
  },
  "active_tools": {
    "delegate.code": "delegated_code"
  }
}`
	if err := os.WriteFile(cfgPath, []byte(cfgJSON), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	resolver := func(kind agent.Kind) (string, error) {
		return binPath, nil
	}
	provider := llm.NewMockProvider("mock")
	provider.QueueText("real subprocess delegate ok")

	runner := &ProcessRunner{
		BinResolver: resolver,
		Env: append(os.Environ(),
			"BRAIN_CONFIG="+cfgPath,
		),
		InitTimeout:     10 * time.Second,
		ShutdownTimeout: 5 * time.Second,
	}
	orch := NewOrchestrator(runner, &LLMProxy{
		ProviderFactory: func(kind agent.Kind) llm.Provider {
			if kind != agent.KindCode {
				return nil
			}
			return provider
		},
	}, resolver)

	result, err := orch.Delegate(ctx, &SubtaskRequest{
		TaskID:      "process-1",
		TargetKind:  agent.KindCode,
		Instruction: "say hello from the subprocess code brain",
		Budget: &SubtaskBudget{
			MaxTurns: 2,
			Timeout:  20 * time.Second,
		},
	})
	if err != nil {
		t.Fatalf("Delegate: %v", err)
	}
	defer func() {
		_ = orch.Shutdown(context.Background())
	}()

	if result.Status != "completed" {
		t.Fatalf("status=%q, want completed (error=%s)", result.Status, result.Error)
	}

	var output sidecar.ExecuteResult
	if err := json.Unmarshal(result.Output, &output); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	if output.Summary != "real subprocess delegate ok" {
		t.Fatalf("summary=%q, want real subprocess delegate ok", output.Summary)
	}
	if len(provider.Requests()) == 0 {
		t.Fatalf("expected at least one llm request from subprocess sidecar")
	}
}
