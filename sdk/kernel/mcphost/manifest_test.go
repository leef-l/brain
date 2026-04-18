package mcphost

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateManifest_Valid(t *testing.T) {
	m := &BrainManifest{
		Kind: "filesystem",
		Runtime: RuntimeSpec{
			Type: "mcp-backed",
			MCPServers: []ManifestMCPServer{
				{
					Name:    "fs",
					Command: "npx",
					Args:    []string{"-y", "@modelcontextprotocol/server-filesystem", "/tmp"},
				},
			},
		},
	}
	if err := ValidateManifest(m); err != nil {
		t.Fatalf("ValidateManifest: %v", err)
	}
	// Check defaults were applied.
	srv := m.Runtime.MCPServers[0]
	if srv.ToolPrefix != "mcp.fs." {
		t.Errorf("ToolPrefix=%q, want mcp.fs.", srv.ToolPrefix)
	}
	if srv.RestartPolicy != "on-failure" {
		t.Errorf("RestartPolicy=%q, want on-failure", srv.RestartPolicy)
	}
	if srv.MaxRestarts != 3 {
		t.Errorf("MaxRestarts=%d, want 3", srv.MaxRestarts)
	}
	if srv.StartupTimeoutMs != 10000 {
		t.Errorf("StartupTimeoutMs=%d, want 10000", srv.StartupTimeoutMs)
	}
	if srv.HealthCheckIntervalMs != 30000 {
		t.Errorf("HealthCheckIntervalMs=%d, want 30000", srv.HealthCheckIntervalMs)
	}
}

func TestValidateManifest_WrongRuntimeType(t *testing.T) {
	m := &BrainManifest{
		Runtime: RuntimeSpec{
			Type: "native",
			MCPServers: []ManifestMCPServer{
				{Name: "x", Command: "x"},
			},
		},
	}
	if err := ValidateManifest(m); err == nil {
		t.Fatal("expected error for wrong runtime type")
	}
}

func TestValidateManifest_EmptyServers(t *testing.T) {
	m := &BrainManifest{
		Runtime: RuntimeSpec{
			Type:       "mcp-backed",
			MCPServers: nil,
		},
	}
	if err := ValidateManifest(m); err == nil {
		t.Fatal("expected error for empty mcp_servers")
	}
}

func TestValidateManifest_EmptyName(t *testing.T) {
	m := &BrainManifest{
		Runtime: RuntimeSpec{
			Type: "mcp-backed",
			MCPServers: []ManifestMCPServer{
				{Name: "", Command: "x"},
			},
		},
	}
	if err := ValidateManifest(m); err == nil {
		t.Fatal("expected error for empty server name")
	}
}

func TestValidateManifest_DuplicateName(t *testing.T) {
	m := &BrainManifest{
		Runtime: RuntimeSpec{
			Type: "mcp-backed",
			MCPServers: []ManifestMCPServer{
				{Name: "fs", Command: "a"},
				{Name: "fs", Command: "b"},
			},
		},
	}
	if err := ValidateManifest(m); err == nil {
		t.Fatal("expected error for duplicate server name")
	}
}

func TestValidateManifest_EmptyCommand(t *testing.T) {
	m := &BrainManifest{
		Runtime: RuntimeSpec{
			Type: "mcp-backed",
			MCPServers: []ManifestMCPServer{
				{Name: "fs", Command: ""},
			},
		},
	}
	if err := ValidateManifest(m); err == nil {
		t.Fatal("expected error for empty command")
	}
}

func TestValidateManifest_InvalidRestartPolicy(t *testing.T) {
	m := &BrainManifest{
		Runtime: RuntimeSpec{
			Type: "mcp-backed",
			MCPServers: []ManifestMCPServer{
				{Name: "fs", Command: "x", RestartPolicy: "bogus"},
			},
		},
	}
	if err := ValidateManifest(m); err != nil {
		t.Fatalf("should auto-fix invalid restart policy, got error: %v", err)
	}
	if m.Runtime.MCPServers[0].RestartPolicy != "on-failure" {
		t.Errorf("RestartPolicy=%q, want on-failure (auto-fixed)", m.Runtime.MCPServers[0].RestartPolicy)
	}
}

func TestValidateManifest_ExplicitToolPrefix(t *testing.T) {
	m := &BrainManifest{
		Runtime: RuntimeSpec{
			Type: "mcp-backed",
			MCPServers: []ManifestMCPServer{
				{Name: "fs", Command: "x", ToolPrefix: "custom."},
			},
		},
	}
	if err := ValidateManifest(m); err != nil {
		t.Fatal(err)
	}
	if m.Runtime.MCPServers[0].ToolPrefix != "custom." {
		t.Errorf("ToolPrefix=%q, want custom.", m.Runtime.MCPServers[0].ToolPrefix)
	}
}

func TestLoadManifest_FileNotFound(t *testing.T) {
	_, err := LoadManifest("/nonexistent/manifest.json")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoadManifest_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	os.WriteFile(path, []byte("not json"), 0644)

	_, err := LoadManifest(path)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestLoadManifest_Valid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	data := `{
		"schema_version": 1,
		"kind": "filesystem",
		"name": "Filesystem Brain",
		"runtime": {
			"type": "mcp-backed",
			"mcp_servers": [
				{
					"name": "fs",
					"command": "npx",
					"args": ["-y", "@modelcontextprotocol/server-filesystem"]
				}
			]
		}
	}`
	os.WriteFile(path, []byte(data), 0644)

	m, err := LoadManifest(path)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if m.Kind != "filesystem" {
		t.Errorf("Kind=%q, want filesystem", m.Kind)
	}
	if len(m.Runtime.MCPServers) != 1 {
		t.Fatalf("expected 1 server, got %d", len(m.Runtime.MCPServers))
	}
}

func TestToMCPServerSpec(t *testing.T) {
	ms := &ManifestMCPServer{
		Name:       "fs",
		Command:    "npx",
		Args:       []string{"-y", "server-fs"},
		ToolPrefix: "mcp.fs.",
		Env:        map[string]string{"NODE_ENV": "production"},
	}
	spec := ms.ToMCPServerSpec()
	if spec.Name != "fs" {
		t.Errorf("Name=%q", spec.Name)
	}
	if spec.BinPath != "npx" {
		t.Errorf("BinPath=%q", spec.BinPath)
	}
	if !spec.AutoStart {
		t.Error("expected AutoStart=true")
	}
}

func TestSanitizeName(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"mcp.fs.", "mcp.fs"},
		{"mcp/github.", "mcp_github"},
		{"simple", "simple"},
	}
	for _, tt := range tests {
		if got := sanitizeName(tt.in); got != tt.want {
			t.Errorf("sanitizeName(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
