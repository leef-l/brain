package main

import (
	"os"
	"path/filepath"
	"testing"
)

func evalPath(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", path, err)
	}
	return resolved
}

func TestResolveServeRunWorkdir_Confined(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "task")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	inside = evalPath(t, inside)

	got, err := resolveServeRunWorkdir(root, "task", serveWorkdirPolicyConfined)
	if err != nil {
		t.Fatalf("resolve inside: %v", err)
	}
	if evalPath(t, got) != inside {
		t.Fatalf("got %q, want %q", got, inside)
	}

	outside := t.TempDir()
	if _, err := resolveServeRunWorkdir(root, outside, serveWorkdirPolicyConfined); err == nil {
		t.Fatal("expected confined policy to reject absolute outside directory")
	}
	if _, err := resolveServeRunWorkdir(root, "../", serveWorkdirPolicyConfined); err == nil {
		t.Fatal("expected confined policy to reject escaping relative path")
	}
}

func TestResolveServeRunWorkdir_OpenAllowsAbsoluteOutside(t *testing.T) {
	root := t.TempDir()
	outside := evalPath(t, t.TempDir())

	got, err := resolveServeRunWorkdir(root, outside, serveWorkdirPolicyOpen)
	if err != nil {
		t.Fatalf("resolve open absolute: %v", err)
	}
	if evalPath(t, got) != outside {
		t.Fatalf("got %q, want %q", got, outside)
	}
}

func TestResolveServeWorkdirPolicy_DefaultAndConfig(t *testing.T) {
	got, err := resolveServeWorkdirPolicy("", nil)
	if err != nil {
		t.Fatalf("default policy: %v", err)
	}
	if got != serveWorkdirPolicyConfined {
		t.Fatalf("default policy=%q, want confined", got)
	}

	cfg := &brainConfig{ServeWorkdirPolicy: string(serveWorkdirPolicyOpen)}
	got, err = resolveServeWorkdirPolicy("", cfg)
	if err != nil {
		t.Fatalf("config policy: %v", err)
	}
	if got != serveWorkdirPolicyOpen {
		t.Fatalf("config policy=%q, want open", got)
	}
}
