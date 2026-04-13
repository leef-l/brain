package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/leef-l/brain/tool"
)

type stubTool struct {
	name  string
	brain string
}

func (s stubTool) Name() string { return s.name }

func (s stubTool) Schema() tool.Schema {
	return tool.Schema{
		Name:        s.name,
		Description: s.name,
		InputSchema: json.RawMessage(`{"type":"object"}`),
		Brain:       s.brain,
	}
}

func (s stubTool) Risk() tool.Risk { return tool.RiskSafe }

func (s stubTool) Execute(context.Context, json.RawMessage) (*tool.Result, error) {
	return &tool.Result{Output: json.RawMessage(`"ok"`)}, nil
}

func TestFilterRegistryWithConfig_MergesScopedProfiles(t *testing.T) {
	reg := tool.NewMemRegistry()
	for _, name := range []string{
		"central.delegate",
		"code.read_file",
		"code.search",
		"code.shell_exec",
		"code.write_file",
	} {
		if err := reg.Register(stubTool{name: name, brain: "code"}); err != nil {
			t.Fatalf("register %s: %v", name, err)
		}
	}

	cfg := &brainConfig{
		ToolProfiles: map[string]*toolProfileConfig{
			"coding": {
				Include: []string{"code.*", "central.delegate"},
			},
			"no-shell": {
				Exclude: []string{"*.shell_exec"},
			},
		},
		ActiveTools: map[string]string{
			"chat":                 "coding",
			"chat.central.default": "no-shell",
		},
	}

	filtered := filterRegistryWithConfig(reg, cfg, toolScopesForChat("central", modeDefault)...)

	var got []string
	for _, t := range filtered.List() {
		got = append(got, t.Name())
	}

	want := []string{
		"central.delegate",
		"code.read_file",
		"code.search",
		"code.write_file",
	}
	if len(got) != len(want) {
		t.Fatalf("filtered tools len=%d, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("filtered[%d]=%q, want %q (all=%v)", i, got[i], want[i], got)
		}
	}
}

func TestConfigSetAndUnset_ToolProfilesAndActiveTools(t *testing.T) {
	cfg := &brainConfig{}

	if err := configSet(cfg, "tool_profiles.safe.include", "code.read_file, code.search"); err != nil {
		t.Fatalf("set include: %v", err)
	}
	if err := configSet(cfg, "tool_profiles.safe.exclude", "*.shell_exec"); err != nil {
		t.Fatalf("set exclude: %v", err)
	}
	if err := configSet(cfg, "active_tools.chat.central.default", "safe"); err != nil {
		t.Fatalf("set active_tools: %v", err)
	}

	m := configToMap(cfg)
	if got := m["tool_profiles.safe.include"]; got != "code.read_file,code.search" {
		t.Fatalf("include=%q, want code.read_file,code.search", got)
	}
	if got := m["tool_profiles.safe.exclude"]; got != "*.shell_exec" {
		t.Fatalf("exclude=%q, want *.shell_exec", got)
	}
	if got := m["active_tools.chat.central.default"]; got != "safe" {
		t.Fatalf("active_tools=%q, want safe", got)
	}

	configUnset(cfg, "tool_profiles.safe.include")
	if _, ok := configGet(cfg, "tool_profiles.safe.include"); ok {
		t.Fatalf("tool_profiles.safe.include should be unset")
	}

	configUnset(cfg, "active_tools.chat.central.default")
	if _, ok := configGet(cfg, "active_tools.chat.central.default"); ok {
		t.Fatalf("active_tools.chat.central.default should be unset")
	}
}

func TestConfigSet_ActiveToolsRejectsUnknownProfile(t *testing.T) {
	cfg := &brainConfig{}
	err := configSet(cfg, "active_tools.chat.central.default", "missing")
	if err == nil {
		t.Fatalf("expected unknown profile validation error")
	}
}

func TestConfigSet_ActiveToolsRejectsInvalidScope(t *testing.T) {
	cfg := &brainConfig{
		ToolProfiles: map[string]*toolProfileConfig{
			"safe": {Include: []string{"code.*"}},
		},
	}
	err := configSet(cfg, "active_tools.chat central.default", "safe")
	if err == nil {
		t.Fatalf("expected invalid scope validation error")
	}
}

func TestConfigUnset_ToolProfilePrunesActiveTools(t *testing.T) {
	cfg := &brainConfig{}
	if err := configSet(cfg, "tool_profiles.safe.include", "code.*"); err != nil {
		t.Fatalf("set tool profile: %v", err)
	}
	if err := configSet(cfg, "active_tools.delegate.code", "safe"); err != nil {
		t.Fatalf("set active tools: %v", err)
	}

	configUnset(cfg, "tool_profiles.safe")
	if _, ok := configGet(cfg, "active_tools.delegate.code"); ok {
		t.Fatalf("active_tools.delegate.code should be pruned after removing referenced profile")
	}
}
