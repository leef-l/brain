package kernel

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/leef-l/brain/sdk/toolpolicy"
)

const (
	envBrowserFeatureGate  = "BRAIN_BROWSER_FEATURE_GATE"
	envBrowserFeatureFlags = "BRAIN_BROWSER_FEATURES"
	envBrowserRuntimeSync  = "BRAIN_BROWSER_RUNTIME_SYNC_FILE"
)

// BrowserRuntimeProjection is the canonical host→child runtime contract for
// browser sidecars. It projects persistence paths, feature gates and a sync
// file location into a single payload that both the host and child can share.
type BrowserRuntimeProjection struct {
	Version            int64             `json:"version,omitempty"`
	BrainDBPath        string            `json:"brain_db_path,omitempty"`
	UIPatternDBPath    string            `json:"ui_pattern_db_path,omitempty"`
	PersistenceDriver  string            `json:"persistence_driver,omitempty"`
	PersistenceDSN     string            `json:"persistence_dsn,omitempty"`
	FeatureGateEnabled bool              `json:"feature_gate_enabled,omitempty"`
	Features           map[string]bool   `json:"features,omitempty"`
	SyncFile           string            `json:"sync_file,omitempty"`
	Metadata           map[string]string `json:"metadata,omitempty"`
	UpdatedAt          time.Time         `json:"updated_at"`
}

// BrowserRuntimeProjectionForDataDir builds the canonical runtime projection
// for a host runtime data directory.
func BrowserRuntimeProjectionForDataDir(runtimeDataDir string, gateEnabled bool, features map[string]bool) BrowserRuntimeProjection {
	baseDir := strings.TrimSpace(runtimeDataDir)
	if baseDir == "" {
		baseDir = filepath.Dir(toolpolicy.ConfigPath())
	}

	out := BrowserRuntimeProjection{
		Version:           time.Now().UTC().UnixNano(),
		BrainDBPath:       filepath.Join(baseDir, "brain.db"),
		UIPatternDBPath:   filepath.Join(baseDir, "ui_patterns.db"),
		PersistenceDriver: sidecarPersistenceType,
		SyncFile:          filepath.Join(baseDir, "browser-runtime.sync.json"),
		UpdatedAt:         time.Now().UTC(),
	}
	out.PersistenceDSN = out.BrainDBPath
	out.FeatureGateEnabled = gateEnabled
	out.Features = cloneFeatureMap(features)
	return out
}

// Env converts the projection into environment variables that can be passed to
// browser sidecars.
func (p BrowserRuntimeProjection) Env() []string {
	var out []string
	if p.BrainDBPath != "" {
		out = append(out, envBrainDBPath+"="+p.BrainDBPath)
	}
	if p.UIPatternDBPath != "" {
		out = append(out, envUIPatternDBPath+"="+p.UIPatternDBPath)
	}
	if p.PersistenceDriver != "" {
		out = append(out, envPersistenceDriver+"="+p.PersistenceDriver)
	}
	if p.PersistenceDSN != "" {
		out = append(out, envPersistenceDSN+"="+p.PersistenceDSN)
	}
	if p.SyncFile != "" {
		out = append(out, envBrowserRuntimeSync+"="+p.SyncFile)
	}
	if p.FeatureGateEnabled {
		out = append(out, envBrowserFeatureGate+"=1")
	}
	if features := joinEnabledFeatures(p.Features); features != "" {
		out = append(out, envBrowserFeatureFlags+"="+features)
	}
	return out
}

// WriteBrowserRuntimeProjectionFile persists the current projection so already
// running child processes can refresh without a restart.
func WriteBrowserRuntimeProjectionFile(path string, projection BrowserRuntimeProjection) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	if projection.Version == 0 {
		projection.Version = time.Now().UTC().UnixNano()
	}
	if projection.UpdatedAt.IsZero() {
		projection.UpdatedAt = time.Now().UTC()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(projection, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// ReadBrowserRuntimeProjectionFile loads the latest browser runtime projection
// written by the host so an already-running child sidecar can refresh itself.
func ReadBrowserRuntimeProjectionFile(path string) (*BrowserRuntimeProjection, error) {
	if strings.TrimSpace(path) == "" {
		return nil, os.ErrNotExist
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var projection BrowserRuntimeProjection
	if err := json.Unmarshal(data, &projection); err != nil {
		return nil, err
	}
	return &projection, nil
}

func cloneFeatureMap(in map[string]bool) map[string]bool {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]bool, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func joinEnabledFeatures(features map[string]bool) string {
	if len(features) == 0 {
		return ""
	}
	keys := make([]string, 0, len(features))
	for key, enabled := range features {
		if enabled && strings.TrimSpace(key) != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}
