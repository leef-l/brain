package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/leef-l/brain/agent"
	"github.com/leef-l/brain/kernel"
	"github.com/leef-l/brain/llm"
)

// bgCtx returns a context with a 30-second timeout for CLI operations.
func bgCtx() context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	_ = cancel
	return ctx
}

// defaultBinResolver returns a BinResolver that searches for sidecar
// binaries next to the current executable and in PATH.
func defaultBinResolver() func(kind agent.Kind) (string, error) {
	selfPath, _ := os.Executable()
	selfDir := filepath.Dir(selfPath)

	return func(kind agent.Kind) (string, error) {
		names := sidecarBinaryNamesForOS(kind, runtime.GOOS)

		// Check next to the current binary.
		for _, name := range names {
			candidate := filepath.Join(selfDir, name)
			if _, err := os.Stat(candidate); err == nil {
				return candidate, nil
			}
		}

		// Check PATH.
		for _, name := range names {
			if path, err := exec.LookPath(name); err == nil {
				return path, nil
			}
		}

		return "", fmt.Errorf("sidecar binary %q not found", names[0])
	}
}

func sidecarBinaryNamesForOS(kind agent.Kind, goos string) []string {
	name := fmt.Sprintf("brain-%s", kind)
	if goos == "windows" {
		return []string{name + ".exe", name}
	}
	return []string{name}
}

// orchestratorConfig holds parameters for building an Orchestrator.
type orchestratorConfig struct {
	cfg         *brainConfig
	modelConfig *modelConfigInput
	provider    string
	apiKey      string
	baseURL     string
	model       string
}

// buildOrchestrator creates an Orchestrator with LLM proxy for specialist
// brain delegation. Returns nil if no specialist binaries are found.
// This is shared between `brain chat` and `brain run`.
func buildOrchestrator(oc orchestratorConfig) *kernel.Orchestrator {
	binResolver := defaultBinResolver()

	llmProxy := &kernel.LLMProxy{
		ProviderFactory: func(kind agent.Kind) llm.Provider {
			if wantsMockProvider(oc.provider, oc.modelConfig) {
				return nil
			}
			session, err := openConfiguredProvider(oc.cfg, string(kind), oc.modelConfig, oc.provider, oc.apiKey, oc.baseURL, oc.model)
			if err != nil {
				return nil
			}
			return session.Provider
		},
	}

	runner := &kernel.ProcessRunner{BinResolver: binResolver}
	orch := kernel.NewOrchestrator(runner, llmProxy, binResolver)

	if len(orch.AvailableKinds()) == 0 {
		return nil
	}
	return orch
}
