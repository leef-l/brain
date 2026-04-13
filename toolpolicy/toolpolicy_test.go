package toolpolicy

import (
	"os"
	"path/filepath"
	"testing"
)

// --- SplitCSV ---

func TestSplitCSV(t *testing.T) {
	cases := []struct {
		input string
		want  int
	}{
		{"a,b,c", 3},
		{" a , b ", 2},
		{"", 0},
		{"single", 1},
		{",,,", 0},
	}
	for _, c := range cases {
		got := SplitCSV(c.input)
		if len(got) != c.want {
			t.Errorf("SplitCSV(%q) = %v (len %d), want len %d", c.input, got, len(got), c.want)
		}
	}
}

// --- ValidateProfileName ---

func TestValidateProfileName(t *testing.T) {
	valid := []string{"default", "my-profile", "profile_1", "a"}
	for _, name := range valid {
		if err := ValidateProfileName(name); err != nil {
			t.Errorf("ValidateProfileName(%q) = %v", name, err)
		}
	}

	invalid := []string{"", "  ", "UPPER", "has space", "a.b", "a/b"}
	for _, name := range invalid {
		if err := ValidateProfileName(name); err == nil {
			t.Errorf("ValidateProfileName(%q) should fail", name)
		}
	}
}

// --- ValidateScope ---

func TestValidateScope(t *testing.T) {
	valid := []string{"chat", "run.code", "delegate.browser"}
	for _, scope := range valid {
		if err := ValidateScope(scope); err != nil {
			t.Errorf("ValidateScope(%q) = %v", scope, err)
		}
	}

	invalid := []string{"", "HAS_UPPER", "a b"}
	for _, scope := range invalid {
		if err := ValidateScope(scope); err == nil {
			t.Errorf("ValidateScope(%q) should fail", scope)
		}
	}
}

// --- ValidatePatterns ---

func TestValidatePatterns(t *testing.T) {
	if err := ValidatePatterns([]string{"code.*", "browser.navigate"}); err != nil {
		t.Errorf("valid patterns: %v", err)
	}
	if err := ValidatePatterns([]string{""}); err == nil {
		t.Error("empty pattern should fail")
	}
	if err := ValidatePatterns([]string{"[bad"}); err == nil {
		t.Error("invalid pattern should fail")
	}
	if err := ValidatePatterns(nil); err != nil {
		t.Errorf("nil patterns: %v", err)
	}
}

// --- ValidateConfig ---

func TestValidateConfig_Nil(t *testing.T) {
	if err := ValidateConfig(nil); err != nil {
		t.Errorf("nil config: %v", err)
	}
}

func TestValidateConfig_Valid(t *testing.T) {
	cfg := &Config{
		ToolProfiles: map[string]*Profile{
			"default": {Include: []string{"code.*"}, Exclude: []string{"code.delete_file"}},
		},
		ActiveTools: map[string]string{
			"run": "default",
		},
	}
	if err := ValidateConfig(cfg); err != nil {
		t.Errorf("valid config: %v", err)
	}
}

func TestValidateConfig_NullProfile(t *testing.T) {
	cfg := &Config{
		ToolProfiles: map[string]*Profile{
			"default": nil,
		},
	}
	if err := ValidateConfig(cfg); err == nil {
		t.Error("null profile should fail")
	}
}

func TestValidateConfig_UnknownProfile(t *testing.T) {
	cfg := &Config{
		ToolProfiles: map[string]*Profile{
			"default": {Include: []string{"*"}},
		},
		ActiveTools: map[string]string{
			"run": "nonexistent",
		},
	}
	if err := ValidateConfig(cfg); err == nil {
		t.Error("unknown profile reference should fail")
	}
}

func TestValidateConfig_InvalidProfileName(t *testing.T) {
	cfg := &Config{
		ToolProfiles: map[string]*Profile{
			"INVALID": {Include: []string{"*"}},
		},
	}
	if err := ValidateConfig(cfg); err == nil {
		t.Error("invalid profile name should fail")
	}
}

func TestValidateConfig_InvalidPattern(t *testing.T) {
	cfg := &Config{
		ToolProfiles: map[string]*Profile{
			"default": {Include: []string{"[bad"}},
		},
	}
	if err := ValidateConfig(cfg); err == nil {
		t.Error("invalid pattern should fail")
	}
}

func TestValidateConfig_EmptyActiveTools(t *testing.T) {
	cfg := &Config{
		ToolProfiles: map[string]*Profile{
			"default": {Include: []string{"*"}},
		},
		ActiveTools: map[string]string{
			"run": "",
		},
	}
	if err := ValidateConfig(cfg); err == nil {
		t.Error("empty active_tools value should fail")
	}
}

// --- MatchesAny ---

func TestMatchesAny(t *testing.T) {
	if !MatchesAny("code.read_file", []string{"code.*"}) {
		t.Error("expected match for code.*")
	}
	if MatchesAny("browser.navigate", []string{"code.*"}) {
		t.Error("expected no match")
	}
	if !MatchesAny("anything", []string{"*"}) {
		t.Error("wildcard * should match anything")
	}
	if !MatchesAny("exact", []string{"exact"}) {
		t.Error("exact match should work")
	}
	if MatchesAny("test", []string{}) {
		t.Error("empty patterns should not match")
	}
	if MatchesAny("test", []string{"  "}) {
		t.Error("whitespace pattern should not match")
	}
}

// --- ToolAllowed ---

func TestToolAllowed(t *testing.T) {
	if !ToolAllowed("anything", nil, nil) {
		t.Error("no filters should allow all")
	}
	if ToolAllowed("browser.navigate", []string{"code.*"}, nil) {
		t.Error("should be excluded by include filter")
	}
	if !ToolAllowed("code.read_file", []string{"code.*"}, nil) {
		t.Error("should pass include filter")
	}
	if ToolAllowed("code.delete_file", nil, []string{"code.delete_file"}) {
		t.Error("should be excluded by exclude filter")
	}
	if ToolAllowed("code.delete_file", []string{"code.*"}, []string{"code.delete_file"}) {
		t.Error("exclude should override include")
	}
}

// --- MergePatterns ---

func TestMergePatterns(t *testing.T) {
	cfg := &Config{
		ToolProfiles: map[string]*Profile{
			"a": {Include: []string{"code.*"}, Exclude: []string{"code.delete_file"}},
			"b": {Include: []string{"browser.*"}},
		},
	}
	include, exclude := MergePatterns(cfg, []string{"a", "b"})
	if len(include) != 2 {
		t.Errorf("include len = %d, want 2", len(include))
	}
	if len(exclude) != 1 {
		t.Errorf("exclude len = %d, want 1", len(exclude))
	}
}

func TestMergePatterns_NilProfile(t *testing.T) {
	cfg := &Config{
		ToolProfiles: map[string]*Profile{
			"a": {Include: []string{"*"}},
		},
	}
	include, exclude := MergePatterns(cfg, []string{"a", "nonexistent"})
	if len(include) != 1 {
		t.Errorf("include len = %d", len(include))
	}
	if len(exclude) != 0 {
		t.Errorf("exclude len = %d", len(exclude))
	}
}

// --- ActiveProfiles ---

func TestActiveProfiles(t *testing.T) {
	cfg := &Config{
		ActiveTools: map[string]string{
			"chat":     "default",
			"run.code": "code-only",
		},
	}
	profiles := ActiveProfiles(cfg, "chat", "run.code")
	if len(profiles) != 2 {
		t.Fatalf("got %d profiles, want 2", len(profiles))
	}
}

func TestActiveProfiles_NilConfig(t *testing.T) {
	profiles := ActiveProfiles(nil, "chat")
	if profiles != nil {
		t.Error("nil config should return nil")
	}
}

func TestActiveProfiles_Dedup(t *testing.T) {
	cfg := &Config{
		ActiveTools: map[string]string{
			"chat": "default",
			"run":  "default",
		},
	}
	profiles := ActiveProfiles(cfg, "chat", "run")
	if len(profiles) != 1 {
		t.Errorf("duplicate profiles should be deduped, got %d", len(profiles))
	}
}

func TestActiveProfiles_MissingScope(t *testing.T) {
	cfg := &Config{
		ActiveTools: map[string]string{
			"chat": "default",
		},
	}
	profiles := ActiveProfiles(cfg, "nonexistent")
	if len(profiles) != 0 {
		t.Errorf("missing scope should return empty, got %d", len(profiles))
	}
}

// --- PruneMissingProfiles ---

func TestPruneMissingProfiles(t *testing.T) {
	cfg := &Config{
		ToolProfiles: map[string]*Profile{
			"default": {Include: []string{"*"}},
		},
		ActiveTools: map[string]string{
			"chat": "default,missing",
		},
	}
	PruneMissingProfiles(cfg)
	if cfg.ActiveTools["chat"] != "default" {
		t.Errorf("ActiveTools[chat] = %q, want %q", cfg.ActiveTools["chat"], "default")
	}
}

func TestPruneMissingProfiles_AllMissing(t *testing.T) {
	cfg := &Config{
		ToolProfiles: map[string]*Profile{},
		ActiveTools: map[string]string{
			"chat": "missing",
		},
	}
	PruneMissingProfiles(cfg)
	if cfg.ActiveTools != nil {
		t.Error("all missing should clear ActiveTools")
	}
}

func TestPruneMissingProfiles_Nil(t *testing.T) {
	PruneMissingProfiles(nil) // should not panic
}

// --- ToolScopes ---

func TestToolScopesForChat(t *testing.T) {
	scopes := ToolScopesForChat("code", "default")
	expected := []string{"chat", "chat.code", "chat.default", "chat.code.default"}
	if len(scopes) != len(expected) {
		t.Fatalf("got %v, want %v", scopes, expected)
	}
	for i, s := range scopes {
		if s != expected[i] {
			t.Errorf("scope[%d] = %q, want %q", i, s, expected[i])
		}
	}
}

func TestToolScopesForChat_Empty(t *testing.T) {
	scopes := ToolScopesForChat("", "")
	if len(scopes) != 1 || scopes[0] != "chat" {
		t.Errorf("got %v, want [chat]", scopes)
	}
}

func TestToolScopesForRun(t *testing.T) {
	scopes := ToolScopesForRun("browser")
	if len(scopes) != 2 || scopes[1] != "run.browser" {
		t.Errorf("got %v", scopes)
	}
}

func TestToolScopesForRun_Empty(t *testing.T) {
	scopes := ToolScopesForRun("")
	if len(scopes) != 1 || scopes[0] != "run" {
		t.Errorf("got %v", scopes)
	}
}

func TestToolScopesForDelegate(t *testing.T) {
	scopes := ToolScopesForDelegate("verifier")
	if len(scopes) != 2 || scopes[1] != "delegate.verifier" {
		t.Errorf("got %v", scopes)
	}
}

// --- Load ---

func TestLoad_MissingFile(t *testing.T) {
	cfg, err := Load("/nonexistent/path/config.json")
	if err != nil {
		t.Errorf("missing file should return nil, nil: %v", err)
	}
	if cfg != nil {
		t.Error("missing file should return nil config")
	}
}

func TestLoad_ValidFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	data := `{
		"tool_profiles": {
			"default": {"include": ["code.*"]}
		},
		"active_tools": {
			"run": "default"
		}
	}`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if len(cfg.ToolProfiles) != 1 {
		t.Errorf("ToolProfiles len = %d", len(cfg.ToolProfiles))
	}
}

func TestLoad_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte("{invalid"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestLoad_InvalidConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	data := `{
		"tool_profiles": {
			"INVALID_NAME": {"include": ["*"]}
		}
	}`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Error("expected validation error")
	}
}

// --- ConfigPath ---

func TestConfigPath_Default(t *testing.T) {
	// Clear env override
	old := os.Getenv("BRAIN_CONFIG")
	os.Unsetenv("BRAIN_CONFIG")
	defer os.Setenv("BRAIN_CONFIG", old)

	path := ConfigPath()
	if path == "" {
		t.Error("ConfigPath() should not be empty")
	}
}

func TestConfigPath_EnvOverride(t *testing.T) {
	old := os.Getenv("BRAIN_CONFIG")
	os.Setenv("BRAIN_CONFIG", "/custom/path.json")
	defer os.Setenv("BRAIN_CONFIG", old)

	if ConfigPath() != "/custom/path.json" {
		t.Errorf("ConfigPath() = %q", ConfigPath())
	}
}
