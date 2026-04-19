package kernel

import (
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestInjectSidecarPersistenceEnv_UsesHostRuntimePath(t *testing.T) {
	t.Setenv("BRAIN_CONFIG", filepath.Join("/tmp", "brain-home", "config.json"))

	got := injectSidecarPersistenceEnv([]string{"EXISTING=1"})
	wantEntries := []string{
		"EXISTING=1",
		"BRAIN_DB_PATH=" + filepath.Join("/tmp", "brain-home", "brain.db"),
		"BRAIN_UI_PATTERN_DB_PATH=" + filepath.Join("/tmp", "brain-home", "ui_patterns.db"),
		"BRAIN_PERSISTENCE_DRIVER=sqlite",
		"BRAIN_PERSISTENCE_DSN=" + filepath.Join("/tmp", "brain-home", "brain.db"),
		"BRAIN_BROWSER_RUNTIME_SYNC_FILE=" + filepath.Join("/tmp", "brain-home", "browser-runtime.sync.json"),
	}
	for _, want := range wantEntries {
		if !slices.Contains(got, want) {
			t.Fatalf("injectSidecarPersistenceEnv()=%v, missing %q", got, want)
		}
	}
}

func TestInjectSidecarPersistenceEnv_PreservesExplicitValues(t *testing.T) {
	t.Setenv("BRAIN_CONFIG", filepath.Join("/tmp", "brain-home", "config.json"))

	base := []string{
		"BRAIN_DB_PATH=/custom/brain.db",
		"BRAIN_UI_PATTERN_DB_PATH=/custom/ui_patterns.db",
		"BRAIN_PERSISTENCE_DRIVER=sqlite",
		"BRAIN_PERSISTENCE_DSN=/custom/brain.db",
		"BRAIN_BROWSER_RUNTIME_SYNC_FILE=/custom/browser-runtime.sync.json",
	}
	got := injectSidecarPersistenceEnv(base)
	if !slices.Equal(got, base) {
		t.Fatalf("injectSidecarPersistenceEnv()=%v, want unchanged %v", got, base)
	}
}

func TestSidecarPersistenceEnvForDataDir_UsesProvidedRuntimeDir(t *testing.T) {
	got := SidecarPersistenceEnvForDataDir(filepath.Join("/tmp", "runtime"))
	want := []string{
		"BRAIN_DB_PATH=" + filepath.Join("/tmp", "runtime", "brain.db"),
		"BRAIN_UI_PATTERN_DB_PATH=" + filepath.Join("/tmp", "runtime", "ui_patterns.db"),
		"BRAIN_PERSISTENCE_DRIVER=sqlite",
		"BRAIN_PERSISTENCE_DSN=" + filepath.Join("/tmp", "runtime", "brain.db"),
		"BRAIN_BROWSER_RUNTIME_SYNC_FILE=" + filepath.Join("/tmp", "runtime", "browser-runtime.sync.json"),
	}
	if !slices.Equal(got, want) {
		t.Fatalf("SidecarPersistenceEnvForDataDir()=%v, want %v", got, want)
	}
}

func TestMergeEnvLists_PreservesHostAndRunnerEnv(t *testing.T) {
	got := mergeEnvLists([]string{"HOST_LICENSE=1"}, []string{"BRAIN_DB_PATH=/runtime/brain.db"})
	want := []string{"HOST_LICENSE=1", "BRAIN_DB_PATH=/runtime/brain.db"}
	if !slices.Equal(got, want) {
		t.Fatalf("mergeEnvLists()=%v, want %v", got, want)
	}
}

func TestWriteAndReadBrowserRuntimeProjectionFile_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "browser-runtime.sync.json")
	want := BrowserRuntimeProjection{
		Version:            42,
		BrainDBPath:        "/tmp/brain.db",
		UIPatternDBPath:    "/tmp/ui_patterns.db",
		PersistenceDriver:  "sqlite",
		PersistenceDSN:     "/tmp/brain.db",
		FeatureGateEnabled: true,
		Features:           map[string]bool{"browser-pro.intelligence": true},
		SyncFile:           path,
		UpdatedAt:          time.Now().UTC().Round(0),
	}
	if err := WriteBrowserRuntimeProjectionFile(path, want); err != nil {
		t.Fatalf("WriteBrowserRuntimeProjectionFile: %v", err)
	}
	got, err := ReadBrowserRuntimeProjectionFile(path)
	if err != nil {
		t.Fatalf("ReadBrowserRuntimeProjectionFile: %v", err)
	}
	if got == nil {
		t.Fatal("ReadBrowserRuntimeProjectionFile() = nil")
	}
	if got.Version != want.Version || got.BrainDBPath != want.BrainDBPath || got.SyncFile != want.SyncFile {
		t.Fatalf("projection round-trip = %+v, want %+v", got, want)
	}
	if !got.FeatureGateEnabled || !got.Features["browser-pro.intelligence"] {
		t.Fatalf("projection features lost after round-trip: %+v", got)
	}
}

func TestBrowserRuntimeProjectionForDataDir_ProjectsFeatureGate(t *testing.T) {
	got := BrowserRuntimeProjectionForDataDir(filepath.Join("/tmp", "runtime"), true, map[string]bool{
		"browser-pro.intelligence": true,
		"browser-pro.evidence":     true,
	})
	env := got.Env()
	want := []string{
		"BRAIN_BROWSER_FEATURE_GATE=1",
		"BRAIN_BROWSER_FEATURES=browser-pro.evidence,browser-pro.intelligence",
		"BRAIN_BROWSER_RUNTIME_SYNC_FILE=" + filepath.Join("/tmp", "runtime", "browser-runtime.sync.json"),
	}
	for _, item := range want {
		if !slices.Contains(env, item) {
			t.Fatalf("projection env=%v, missing %q", env, item)
		}
	}
}
