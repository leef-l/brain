package kernel

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/leef-l/brain/sdk/agent"
	"github.com/leef-l/brain/sdk/protocol"
)

func TestMergeProcessEnv_OverridesAndAppends(t *testing.T) {
	got := mergeProcessEnv([]string{"A=1", "B=2"}, []string{"B=3", "C=4"})
	want := []string{"A=1", "B=3", "C=4"}
	if !slices.Equal(got, want) {
		t.Fatalf("mergeProcessEnv()=%v, want %v", got, want)
	}
}

func TestProcessBrainPool_UsesRegistrationBinaryAndEnv(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subprocess integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tmp := t.TempDir()
	binPath := filepath.Join(tmp, "brain-code")
	if runtime.GOOS == "windows" {
		binPath += ".exe"
	}
	build := exec.CommandContext(ctx, "go", "build", "-o", binPath, "./brains/code/cmd")
	build.Dir = filepath.Clean(filepath.Join("..", ".."))
	if out, err := build.CombinedOutput(); err != nil {
		t.Skipf("build brain-code: %v\n%s", err, out)
	}

	cfgPath := filepath.Join(tmp, "config.json")
	cfgJSON := `{
  "tool_profiles": {
    "delegated_code": {
      "include": ["code.note"]
    }
  },
  "active_tools": {
    "delegate.code": "delegated_code"
  }
}`
	if err := os.WriteFile(cfgPath, []byte(cfgJSON), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	runner := &ProcessRunner{
		BinResolver: func(kind agent.Kind) (string, error) {
			return "", os.ErrNotExist
		},
		InitTimeout:     10 * time.Second,
		ShutdownTimeout: 5 * time.Second,
	}
	pool := NewProcessBrainPool(runner, runner.BinResolver, OrchestratorConfig{
		Brains: []BrainRegistration{
			{
				Kind:   agent.KindCode,
				Binary: binPath,
				Env:    []string{"BRAIN_CONFIG=" + cfgPath},
			},
		},
	})
	defer func() {
		_ = pool.Shutdown(context.Background())
	}()

	ag, err := pool.GetBrain(ctx, agent.KindCode)
	if err != nil {
		t.Fatalf("GetBrain: %v", err)
	}

	rpcAgent, ok := ag.(agent.RPCAgent)
	if !ok {
		t.Fatalf("agent type %T does not expose RPC session", ag)
	}
	rpc, ok := rpcAgent.RPC().(protocol.BidirRPC)
	if !ok {
		t.Fatalf("rpc type %T, want protocol.BidirRPC", rpcAgent.RPC())
	}

	var toolsResp struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := rpc.Call(ctx, "tools/list", map[string]any{}, &toolsResp); err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	got := make([]string, 0, len(toolsResp.Tools))
	for _, spec := range toolsResp.Tools {
		got = append(got, spec.Name)
	}
	if !slices.Equal(got, []string{"code.note"}) {
		raw, _ := json.Marshal(toolsResp)
		t.Fatalf("tools/list=%s, want only code.note", raw)
	}
}

func TestProcessBrainPool_FallsBackToResolverWhenRegistrationBinaryMissing(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subprocess integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tmp := t.TempDir()
	binPath := filepath.Join(tmp, "brain-code")
	if runtime.GOOS == "windows" {
		binPath += ".exe"
	}
	build := exec.CommandContext(ctx, "go", "build", "-o", binPath, "./brains/code/cmd")
	build.Dir = filepath.Clean(filepath.Join("..", ".."))
	if out, err := build.CombinedOutput(); err != nil {
		t.Skipf("build brain-code: %v\n%s", err, out)
	}

	cfgPath := filepath.Join(tmp, "config.json")
	cfgJSON := `{
  "tool_profiles": {
    "delegated_code": {
      "include": ["code.note"]
    }
  },
  "active_tools": {
    "delegate.code": "delegated_code"
  }
}`
	if err := os.WriteFile(cfgPath, []byte(cfgJSON), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	runner := &ProcessRunner{
		BinResolver: func(kind agent.Kind) (string, error) {
			if kind != agent.KindCode {
				return "", os.ErrNotExist
			}
			return binPath, nil
		},
		InitTimeout:     10 * time.Second,
		ShutdownTimeout: 5 * time.Second,
	}
	pool := NewProcessBrainPool(runner, runner.BinResolver, OrchestratorConfig{
		Brains: []BrainRegistration{
			{
				Kind:   agent.KindCode,
				Binary: filepath.Join(tmp, "missing-brain-code-sidecar"),
				Env:    []string{"BRAIN_CONFIG=" + cfgPath},
			},
		},
	})
	defer func() {
		_ = pool.Shutdown(context.Background())
	}()

	ag, err := pool.GetBrain(ctx, agent.KindCode)
	if err != nil {
		t.Fatalf("GetBrain: %v", err)
	}

	rpcAgent, ok := ag.(agent.RPCAgent)
	if !ok {
		t.Fatalf("agent type %T does not expose RPC session", ag)
	}
	rpc, ok := rpcAgent.RPC().(protocol.BidirRPC)
	if !ok {
		t.Fatalf("rpc type %T, want protocol.BidirRPC", rpcAgent.RPC())
	}

	var toolsResp struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := rpc.Call(ctx, "tools/list", map[string]any{}, &toolsResp); err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	if len(toolsResp.Tools) != 1 || toolsResp.Tools[0].Name != "code.note" {
		raw, _ := json.Marshal(toolsResp)
		t.Fatalf("tools/list=%s, want only code.note", raw)
	}
}

func TestSourcePackageForKind(t *testing.T) {
	cases := []struct {
		kind    agent.Kind
		pkgPath string
		binName string
		ok      bool
	}{
		{kind: agent.KindBrowser, pkgPath: "brains/browser/cmd", binName: "brain-browser", ok: true},
		{kind: agent.KindData, pkgPath: "brains/data/cmd/brain-data-sidecar", binName: "brain-data-sidecar", ok: true},
		{kind: agent.KindQuant, pkgPath: "brains/quant/cmd/brain-quant-sidecar", binName: "brain-quant-sidecar", ok: true},
		{kind: agent.Kind("unknown"), ok: false},
	}

	for _, tc := range cases {
		pkgPath, binName, ok := sourcePackageForKind(tc.kind)
		if ok != tc.ok || pkgPath != tc.pkgPath || binName != tc.binName {
			t.Fatalf("sourcePackageForKind(%q)=(%q,%q,%v), want (%q,%q,%v)",
				tc.kind, pkgPath, binName, ok, tc.pkgPath, tc.binName, tc.ok)
		}
	}
}

func TestProcessRunner_ResolveExecutablePath_PreservesExplicitBinary(t *testing.T) {
	tmp := t.TempDir()
	explicit := filepath.Join(tmp, "brain-code")
	if err := os.WriteFile(explicit, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write explicit binary: %v", err)
	}

	runner := &ProcessRunner{
		BinPath: explicit,
		BinResolver: func(kind agent.Kind) (string, error) {
			return "", os.ErrNotExist
		},
	}

	got, err := runner.resolveExecutablePath(agent.KindCode)
	if err != nil {
		t.Fatalf("resolveExecutablePath: %v", err)
	}
	if got != explicit {
		t.Fatalf("resolveExecutablePath()=%q, want %q", got, explicit)
	}
}
