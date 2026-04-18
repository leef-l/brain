// manifest.go — BrainManifest types and loading/validation for mcp-backed brains.
//
// Design reference: 35-MCP-backed-Runtime设计.md §2
package mcphost

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// ---------------------------------------------------------------------------
// BrainManifest — top-level manifest for an mcp-backed brain
// ---------------------------------------------------------------------------

// BrainManifest is the parsed manifest.json for an mcp-backed brain.
type BrainManifest struct {
	SchemaVersion int         `json:"schema_version"`
	Kind          string      `json:"kind"`
	Name          string      `json:"name"`
	BrainVersion  string      `json:"brain_version,omitempty"`
	Description   string      `json:"description,omitempty"`
	Capabilities  []string    `json:"capabilities,omitempty"`
	TaskPatterns  []string    `json:"task_patterns,omitempty"`
	Runtime       RuntimeSpec `json:"runtime"`
	Policy        PolicySpec  `json:"policy,omitempty"`
	Health        HealthSpec  `json:"health,omitempty"`
}

// RuntimeSpec describes the runtime section of the manifest.
type RuntimeSpec struct {
	// Type must be "mcp-backed" for this package to accept it.
	Type string `json:"type"`

	// Entrypoint is the binary path for the brain host process.
	Entrypoint string `json:"entrypoint,omitempty"`

	// MCPServers lists the MCP server declarations.
	MCPServers []ManifestMCPServer `json:"mcp_servers"`
}

// ManifestMCPServer describes one MCP server declaration in the manifest.
type ManifestMCPServer struct {
	// Name is the unique identifier within this brain.
	Name string `json:"name"`

	// Command is the executable to launch (absolute path or PATH-resolvable).
	Command string `json:"command"`

	// Args are arguments passed to Command.
	Args []string `json:"args,omitempty"`

	// Env is additional environment variables. nil inherits parent env.
	Env map[string]string `json:"env,omitempty"`

	// ToolPrefix is prepended to every tool name exposed by this server.
	// If empty, auto-filled as "mcp.<name>.".
	ToolPrefix string `json:"tool_prefix,omitempty"`

	// StartupTimeoutMs is the timeout for the MCP initialize handshake.
	// Default: 10000 (10s).
	StartupTimeoutMs int `json:"startup_timeout_ms,omitempty"`

	// HealthCheckIntervalMs is the interval for health check pings.
	// Default: 30000 (30s).
	HealthCheckIntervalMs int `json:"health_check_interval_ms,omitempty"`

	// RestartPolicy decides behavior on MCP server crash.
	// Values: "never", "on-failure", "always". Default: "on-failure".
	RestartPolicy string `json:"restart_policy,omitempty"`

	// MaxRestarts is the maximum automatic restart count. Default: 3.
	MaxRestarts int `json:"max_restarts,omitempty"`

	// ConcurrencyOverrides allows per-tool concurrency spec overrides.
	ConcurrencyOverrides []ConcurrencyOverride `json:"concurrency_overrides,omitempty"`
}

// ConcurrencyOverride allows a manifest to override the auto-inferred
// ToolConcurrencySpec for a specific MCP tool.
type ConcurrencyOverride struct {
	Tool                string  `json:"tool"`
	Capability          string  `json:"capability"`
	ResourceKeyTemplate string  `json:"resource_key_template"`
	AccessMode          string  `json:"access_mode"`
	Scope               string  `json:"scope,omitempty"`
	AcquireTimeout      float64 `json:"acquire_timeout,omitempty"`
}

// PolicySpec describes the policy section of the manifest.
type PolicySpec struct {
	ApprovalClass string `json:"approval_class,omitempty"`
	PoolStrategy  string `json:"pool_strategy,omitempty"`
	ToolScope     string `json:"tool_scope,omitempty"`
}

// HealthSpec describes the health section of the manifest.
type HealthSpec struct {
	StartupTimeoutMs int `json:"startup_timeout_ms,omitempty"`
	PingIntervalMs   int `json:"ping_interval_ms,omitempty"`
}

// ---------------------------------------------------------------------------
// LoadManifest
// ---------------------------------------------------------------------------

// LoadManifest reads and parses a brain manifest from the given file path.
func LoadManifest(path string) (*BrainManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("mcphost: read manifest %s: %w", path, err)
	}

	var m BrainManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("mcphost: parse manifest %s: %w", path, err)
	}

	if err := ValidateManifest(&m); err != nil {
		return nil, err
	}

	return &m, nil
}

// ---------------------------------------------------------------------------
// ValidateManifest
// ---------------------------------------------------------------------------

// ValidateManifest checks the manifest against the rules defined in
// 35-MCP-backed-Runtime设计.md §2.4.
func ValidateManifest(m *BrainManifest) error {
	if m.Runtime.Type != "mcp-backed" {
		return fmt.Errorf("mcphost: runtime.type must be 'mcp-backed', got %q", m.Runtime.Type)
	}
	if len(m.Runtime.MCPServers) == 0 {
		return fmt.Errorf("mcphost: runtime.mcp_servers must not be empty")
	}

	names := make(map[string]bool)
	for i := range m.Runtime.MCPServers {
		srv := &m.Runtime.MCPServers[i]

		if srv.Name == "" {
			return fmt.Errorf("mcphost: mcp_servers[%d].name must not be empty", i)
		}
		if names[srv.Name] {
			return fmt.Errorf("mcphost: duplicate mcp_servers name %q", srv.Name)
		}
		names[srv.Name] = true

		if srv.Command == "" {
			return fmt.Errorf("mcphost: mcp_servers[%d].command must not be empty (name=%s)", i, srv.Name)
		}

		// Auto-fill defaults.
		if srv.ToolPrefix == "" {
			srv.ToolPrefix = "mcp." + srv.Name + "."
		}
		if srv.RestartPolicy == "" {
			srv.RestartPolicy = "on-failure"
		}
		if srv.RestartPolicy != "never" && srv.RestartPolicy != "on-failure" && srv.RestartPolicy != "always" {
			srv.RestartPolicy = "on-failure"
		}
		if srv.MaxRestarts <= 0 {
			srv.MaxRestarts = 3
		}
		if srv.StartupTimeoutMs <= 0 {
			srv.StartupTimeoutMs = 10000
		}
		if srv.HealthCheckIntervalMs <= 0 {
			srv.HealthCheckIntervalMs = 30000
		}
	}

	return nil
}

// ToMCPServerSpec converts a ManifestMCPServer to the simpler MCPServerSpec
// used by AdapterManager.
func (ms *ManifestMCPServer) ToMCPServerSpec() MCPServerSpec {
	return MCPServerSpec{
		Name:       ms.Name,
		BinPath:    ms.Command,
		Args:       ms.Args,
		Env:        ms.Env,
		ToolPrefix: ms.ToolPrefix,
		AutoStart:  true,
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// sanitizeName strips dots and slashes from a name for use in capability keys.
func sanitizeName(s string) string {
	s = strings.TrimSuffix(s, ".")
	s = strings.ReplaceAll(s, "/", "_")
	return s
}
