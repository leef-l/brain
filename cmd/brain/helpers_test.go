package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/leef-l/brain/sdk/agent"
	"github.com/leef-l/brain/sdk/kernel/manifest"
	"github.com/leef-l/brain/sdk/tool"
)

func TestSidecarBinaryNamesForOS(t *testing.T) {
	t.Run("unix", func(t *testing.T) {
		got := sidecarBinaryNamesForOS(agent.KindCode, "linux")
		want := []string{"brain-code-sidecar", "brain-code"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("sidecarBinaryNamesForOS(code, linux) = %v, want %v", got, want)
		}
	})

	t.Run("unix_data", func(t *testing.T) {
		got := sidecarBinaryNamesForOS(agent.KindData, "linux")
		want := []string{"brain-data-sidecar", "brain-data"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("sidecarBinaryNamesForOS(data, linux) = %v, want %v", got, want)
		}
	})

	t.Run("windows", func(t *testing.T) {
		got := sidecarBinaryNamesForOS(agent.KindCode, "windows")
		want := []string{"brain-code-sidecar.exe", "brain-code-sidecar", "brain-code.exe", "brain-code"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("sidecarBinaryNamesForOS(code, windows) = %v, want %v", got, want)
		}
	})
}

func TestResolveManifestEntrypoint_RelativeToManifestDir(t *testing.T) {
	m := &manifest.Manifest{
		SourcePath: filepath.Join("/tmp", "brains", "browser", "brain.json"),
		Runtime: manifest.RuntimeSpec{
			Entrypoint: "bin/brain-browser-sidecar",
		},
	}

	got := resolveManifestEntrypoint(m)
	want := filepath.Join("/tmp", "brains", "browser", "bin", "brain-browser-sidecar")
	if got != want {
		t.Fatalf("resolveManifestEntrypoint()=%q, want %q", got, want)
	}
}

func TestManifestEnvSlice_SortsDeterministically(t *testing.T) {
	got := manifestEnvSlice(map[string]string{
		"BRAIN_CONFIG": "/tmp/brain.yaml",
		"ALPHA":        "1",
	})
	want := []string{
		"ALPHA=1",
		"BRAIN_CONFIG=/tmp/brain.yaml",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("manifestEnvSlice()=%v, want %v", got, want)
	}
}

func TestConfigureBrowserRuntimeEnv_ProjectsFeatureGateAndSyncFile(t *testing.T) {
	dir := t.TempDir()
	prev := tool.CurrentBrowserFeatureGateConfig()
	t.Cleanup(func() { tool.SetBrowserFeatureGate(&prev) })
	tool.SetBrowserFeatureGate(&tool.BrowserFeatureGateConfig{
		Enabled:  true,
		Features: map[string]bool{"browser-pro.intelligence": true},
	})

	configureBrowserRuntimeEnv(dir)

	if got := os.Getenv("BRAIN_BROWSER_FEATURE_GATE"); got != "1" {
		t.Fatalf("BRAIN_BROWSER_FEATURE_GATE=%q, want 1", got)
	}
	if got := os.Getenv("BRAIN_BROWSER_FEATURES"); got != "browser-pro.intelligence" {
		t.Fatalf("BRAIN_BROWSER_FEATURES=%q, want browser-pro.intelligence", got)
	}
	if got := os.Getenv("BRAIN_BROWSER_RUNTIME_SYNC_FILE"); got != filepath.Join(dir, "browser-runtime.sync.json") {
		t.Fatalf("BRAIN_BROWSER_RUNTIME_SYNC_FILE=%q, want %q", got, filepath.Join(dir, "browser-runtime.sync.json"))
	}
}
