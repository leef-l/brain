package manifest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// ---------- helper ----------

func validManifest() *Manifest {
	return &Manifest{
		SchemaVersion: 1,
		Kind:          "quant-sidecar",
		Name:          "Quant Brain",
		BrainVersion:  "3.0.0",
		Capabilities:  []string{"signal.rsi", "signal.macd"},
		Runtime: RuntimeSpec{
			Type:       RuntimeNative,
			Entrypoint: "./bin/quant",
		},
	}
}

func writeTempJSON(t *testing.T, dir, name string, v any) string {
	t.Helper()
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0644); err != nil {
		t.Fatal(err)
	}
	return p
}

// ---------- LoadFromFile ----------

func TestLoadFromFile_JSON(t *testing.T) {
	dir := t.TempDir()
	m := validManifest()
	p := writeTempJSON(t, dir, "brain.json", m)

	loaded, err := LoadFromFile(p)
	if err != nil {
		t.Fatalf("LoadFromFile 失败: %v", err)
	}
	if loaded.Kind != m.Kind {
		t.Errorf("kind = %q, want %q", loaded.Kind, m.Kind)
	}
	if loaded.SourcePath == "" {
		t.Error("SourcePath 应被注入")
	}
}

func TestLoadFromFile_YAML(t *testing.T) {
	dir := t.TempDir()
	yamlContent := `
schema_version: 1
kind: data-sidecar
name: Data Brain
brain_version: "3.0.0"
capabilities:
  - market.tick
runtime:
  type: native
  entrypoint: ./bin/data
`
	p := filepath.Join(dir, "brain.yaml")
	if err := os.WriteFile(p, []byte(yamlContent), 0644); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadFromFile(p)
	if err != nil {
		t.Fatalf("LoadFromFile YAML 失败: %v", err)
	}
	if loaded.Kind != "data-sidecar" {
		t.Errorf("kind = %q, want %q", loaded.Kind, "data-sidecar")
	}
}

func TestLoadFromFile_NotExist(t *testing.T) {
	_, err := LoadFromFile("/tmp/does-not-exist-brain.json")
	if err == nil {
		t.Error("应返回错误")
	}
}

func TestLoadFromFile_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "brain.json")
	if err := os.WriteFile(p, []byte("{bad json"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadFromFile(p)
	if err == nil {
		t.Error("应返回 JSON 解析错误")
	}
}

// ---------- LoadFromDir ----------

func TestLoadFromDir_Found(t *testing.T) {
	dir := t.TempDir()
	writeTempJSON(t, dir, "brain.json", validManifest())

	loaded, err := LoadFromDir(dir)
	if err != nil {
		t.Fatalf("LoadFromDir 失败: %v", err)
	}
	if loaded.Kind != "quant-sidecar" {
		t.Errorf("kind = %q", loaded.Kind)
	}
}

func TestLoadFromDir_NotFound(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadFromDir(dir)
	if err == nil {
		t.Error("空目录应返回错误")
	}
}

// ---------- LoadAll ----------

func TestLoadAll(t *testing.T) {
	base := t.TempDir()
	sub1 := filepath.Join(base, "a")
	sub2 := filepath.Join(base, "b")
	os.MkdirAll(sub1, 0755)
	os.MkdirAll(sub2, 0755)

	m1 := validManifest()
	m1.Kind = "brain-a"
	writeTempJSON(t, sub1, "brain.json", m1)

	m2 := validManifest()
	m2.Kind = "brain-b"
	writeTempJSON(t, sub2, "brain.json", m2)

	all, err := LoadAll(base)
	if err != nil {
		t.Fatalf("LoadAll 失败: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("len = %d, want 2", len(all))
	}
}

// ---------- Validate ----------

func TestValidate_Valid(t *testing.T) {
	errs := Validate(validManifest())
	if len(errs) != 0 {
		t.Errorf("合法 manifest 不应有错误，got %v", errs)
	}
}

func TestValidate_EmptyManifest(t *testing.T) {
	errs := Validate(&Manifest{})
	// 应至少检出: schema_version, kind, name, brain_version, capabilities, runtime.type
	if len(errs) < 5 {
		t.Errorf("空 manifest 至少 5 个错误，got %d: %v", len(errs), errs)
	}
}

func TestValidate_SchemaVersionZero(t *testing.T) {
	m := validManifest()
	m.SchemaVersion = 0
	errs := Validate(m)
	if !hasField(errs, "schema_version") {
		t.Error("应检出 schema_version 错误")
	}
}

func TestValidate_MCPBacked_NoServers(t *testing.T) {
	m := validManifest()
	m.Runtime = RuntimeSpec{Type: RuntimeMCPBacked}
	errs := Validate(m)
	if !hasField(errs, "runtime.mcp_servers") {
		t.Error("mcp-backed 无 mcp_servers 应报错")
	}
}

func TestValidate_MCPBacked_WithServers(t *testing.T) {
	m := validManifest()
	m.Runtime = RuntimeSpec{
		Type: RuntimeMCPBacked,
		MCPServers: []MCPServerBinding{
			{Name: "s1", Command: "cmd", ToolPrefix: "pfx"},
		},
	}
	errs := Validate(m)
	if hasField(errs, "runtime.mcp_servers") {
		t.Error("mcp-backed 配了 mcp_servers 不应报错")
	}
}

func TestValidate_Native_NoEntrypoint(t *testing.T) {
	m := validManifest()
	m.Runtime = RuntimeSpec{Type: RuntimeNative}
	errs := Validate(m)
	if !hasField(errs, "runtime.entrypoint") {
		t.Error("native 无 entrypoint 应报错")
	}
}

func TestValidate_InvalidRuntimeType(t *testing.T) {
	m := validManifest()
	m.Runtime = RuntimeSpec{Type: "alien"}
	errs := Validate(m)
	if !hasField(errs, "runtime.type") {
		t.Error("无效 runtime.type 应报错")
	}
}

func TestValidate_Wasm(t *testing.T) {
	m := validManifest()
	m.Runtime = RuntimeSpec{Type: RuntimeWasm}
	errs := Validate(m)
	// wasm 类型目前无额外约束，不应因 entrypoint/mcp_servers 报错
	if hasField(errs, "runtime.entrypoint") || hasField(errs, "runtime.mcp_servers") {
		t.Error("wasm 不应要求 entrypoint 或 mcp_servers")
	}
}

func TestValidate_Docker(t *testing.T) {
	m := validManifest()
	m.Runtime = RuntimeSpec{Type: RuntimeDocker}
	errs := Validate(m)
	if hasField(errs, "runtime.entrypoint") || hasField(errs, "runtime.mcp_servers") {
		t.Error("docker 不应要求 entrypoint 或 mcp_servers")
	}
}

// ---------- Registry ----------

func TestRegistry_RegisterAndGet(t *testing.T) {
	r := NewRegistry()
	m := validManifest()
	if err := r.Register(m); err != nil {
		t.Fatal(err)
	}

	got, ok := r.Get(m.Kind)
	if !ok || got.Name != m.Name {
		t.Error("Get 失败")
	}
}

func TestRegistry_DuplicateRegister(t *testing.T) {
	r := NewRegistry()
	m := validManifest()
	_ = r.Register(m)
	if err := r.Register(m); err == nil {
		t.Error("重复注册应返回错误")
	}
}

func TestRegistry_List(t *testing.T) {
	r := NewRegistry()
	m1 := validManifest()
	m1.Kind = "kind-a"
	m2 := validManifest()
	m2.Kind = "kind-b"
	_ = r.Register(m1)
	_ = r.Register(m2)

	if len(r.List()) != 2 {
		t.Errorf("List len = %d, want 2", len(r.List()))
	}
}

func TestRegistry_Remove(t *testing.T) {
	r := NewRegistry()
	m := validManifest()
	_ = r.Register(m)
	r.Remove(m.Kind)

	if _, ok := r.Get(m.Kind); ok {
		t.Error("Remove 后不应找到")
	}
}

func TestRegistry_FindByCapability(t *testing.T) {
	r := NewRegistry()

	m1 := validManifest()
	m1.Kind = "a"
	m1.Capabilities = []string{"signal.rsi", "signal.macd"}

	m2 := validManifest()
	m2.Kind = "b"
	m2.Capabilities = []string{"market.tick"}

	m3 := validManifest()
	m3.Kind = "c"
	m3.Capabilities = []string{"signal.rsi", "risk.var"}

	_ = r.Register(m1)
	_ = r.Register(m2)
	_ = r.Register(m3)

	found := r.FindByCapability("signal.rsi")
	if len(found) != 2 {
		t.Errorf("FindByCapability(signal.rsi) = %d, want 2", len(found))
	}

	found2 := r.FindByCapability("nonexistent")
	if len(found2) != 0 {
		t.Errorf("FindByCapability(nonexistent) = %d, want 0", len(found2))
	}
}

func TestRegistry_GetNotFound(t *testing.T) {
	r := NewRegistry()
	_, ok := r.Get("nope")
	if ok {
		t.Error("空注册表 Get 应返回 false")
	}
}

// ---------- helper ----------

func hasField(errs []ValidationError, field string) bool {
	for _, e := range errs {
		if e.Field == field {
			return true
		}
	}
	return false
}
