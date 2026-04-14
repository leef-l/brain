package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSandboxedSearch_DefaultPathUsesSandboxPrimaryWhenMissing(t *testing.T) {
	sandboxDir := t.TempDir()
	cwdDir := t.TempDir()

	if err := os.WriteFile(filepath.Join(sandboxDir, "inside.txt"), []byte("needle-in-sandbox\n"), 0o644); err != nil {
		t.Fatalf("write sandbox file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cwdDir, "outside.txt"), []byte("needle-in-cwd\n"), 0o644); err != nil {
		t.Fatalf("write cwd file: %v", err)
	}

	prevWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(cwdDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(prevWD)
	})

	wrapped := WrapSandbox(NewSearchTool("code"), NewSandbox(sandboxDir))
	args, _ := json.Marshal(map[string]any{"pattern": "needle-in-sandbox"})
	result, err := wrapped.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("execute search: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Output)
	}

	var out searchOutput
	if err := json.Unmarshal(result.Output, &out); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if out.Total != 1 {
		t.Fatalf("total=%d, want 1", out.Total)
	}
	if len(out.Matches) != 1 || out.Matches[0].File != "inside.txt" {
		t.Fatalf("matches=%+v, want inside.txt from sandbox workdir", out.Matches)
	}
}

func TestSandboxedSearch_DefaultPathUsesSandboxPrimaryWhenEmpty(t *testing.T) {
	sandboxDir := t.TempDir()
	cwdDir := t.TempDir()

	if err := os.WriteFile(filepath.Join(sandboxDir, "inside.txt"), []byte("needle-empty-path\n"), 0o644); err != nil {
		t.Fatalf("write sandbox file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cwdDir, "outside.txt"), []byte("needle-in-cwd\n"), 0o644); err != nil {
		t.Fatalf("write cwd file: %v", err)
	}

	prevWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(cwdDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(prevWD)
	})

	wrapped := WrapSandbox(NewSearchTool("code"), NewSandbox(sandboxDir))
	args, _ := json.Marshal(map[string]any{
		"pattern": "needle-empty-path",
		"path":    "",
	})
	result, err := wrapped.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("execute search: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Output)
	}

	var out searchOutput
	if err := json.Unmarshal(result.Output, &out); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if out.Total != 1 {
		t.Fatalf("total=%d, want 1", out.Total)
	}
	if len(out.Matches) != 1 || out.Matches[0].File != "inside.txt" {
		t.Fatalf("matches=%+v, want inside.txt from sandbox workdir", out.Matches)
	}
}
