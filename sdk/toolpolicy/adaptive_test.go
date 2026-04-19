package toolpolicy

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/leef-l/brain/sdk/tool"
)

func makeTestRegistry() tool.Registry {
	reg := tool.NewMemRegistry()
	for _, name := range []string{"code.read", "code.write", "code.delete", "shell.exec", "browser.open"} {
		reg.Register(&stubTool{name: name})
	}
	return reg
}

type stubTool struct {
	name string
}

func (s *stubTool) Name() string        { return s.name }
func (s *stubTool) Schema() tool.Schema { return tool.Schema{Name: s.name} }
func (s *stubTool) Risk() tool.Risk     { return tool.RiskLow }
func (s *stubTool) Execute(_ context.Context, _ json.RawMessage) (*tool.Result, error) {
	return &tool.Result{}, nil
}

func TestAdaptivePolicyEvaluateNoConfig(t *testing.T) {
	p := NewAdaptivePolicy(nil)
	reg := makeTestRegistry()
	result := p.Evaluate(EvalRequest{}, reg)
	if len(result.List()) != 5 {
		t.Errorf("expected 5 tools, got %d", len(result.List()))
	}
}

func TestAdaptivePolicyEvaluateWithProfile(t *testing.T) {
	cfg := &Config{
		ToolProfiles: map[string]*Profile{
			"readonly": {Include: []string{"code.read", "browser.*"}},
		},
		ActiveTools: map[string]string{
			"run": "readonly",
		},
	}
	p := NewAdaptivePolicy(cfg)
	reg := makeTestRegistry()
	result := p.Evaluate(EvalRequest{Mode: "run"}, reg)
	names := toolNames(result)
	if len(names) != 2 {
		t.Errorf("expected 2 tools, got %d: %v", len(names), names)
	}
}

func TestAdaptivePolicyOverride(t *testing.T) {
	cfg := &Config{
		ToolProfiles: map[string]*Profile{
			"readonly": {Include: []string{"code.read"}},
			"full":     {Include: []string{"*"}},
		},
		ActiveTools: map[string]string{
			"run": "readonly",
		},
	}
	p := NewAdaptivePolicy(cfg)
	reg := makeTestRegistry()

	// 初始：只有 code.read
	result := p.Evaluate(EvalRequest{Mode: "run"}, reg)
	if len(result.List()) != 1 {
		t.Fatalf("initial: expected 1, got %d", len(result.List()))
	}

	// 运行时覆盖为 full
	p.Override("run", "full")
	result = p.Evaluate(EvalRequest{Mode: "run"}, reg)
	if len(result.List()) != 5 {
		t.Errorf("after override: expected 5, got %d", len(result.List()))
	}

	// 清除覆盖
	p.ClearOverride("run")
	result = p.Evaluate(EvalRequest{Mode: "run"}, reg)
	if len(result.List()) != 1 {
		t.Errorf("after clear: expected 1, got %d", len(result.List()))
	}
}

func TestAdaptivePolicyRecordAndSuggest(t *testing.T) {
	p := NewAdaptivePolicy(nil)

	p.RecordOutcome("code.read", "review", true)
	p.RecordOutcome("code.read", "review", true)
	p.RecordOutcome("code.read", "review", true)
	p.RecordOutcome("shell.exec", "review", true)
	p.RecordOutcome("shell.exec", "review", false)
	p.RecordOutcome("browser.open", "review", false)

	suggestions := p.Suggest("review")
	if len(suggestions) != 3 {
		t.Fatalf("suggest count = %d, want 3", len(suggestions))
	}
	// code.read 100% 成功率应排第一
	if suggestions[0] != "code.read" {
		t.Errorf("first suggestion = %s, want code.read", suggestions[0])
	}
}

func TestAdaptivePolicyAutoDisableLowSuccess(t *testing.T) {
	cfg := &Config{
		ToolProfiles: map[string]*Profile{
			"all": {Include: []string{"*"}},
		},
		ActiveTools: map[string]string{
			"run": "all",
		},
	}
	p := NewAdaptivePolicy(cfg)
	reg := makeTestRegistry()

	// 让 shell.exec 在 "deploy" 任务类型下成功率极低
	for i := 0; i < 10; i++ {
		p.RecordOutcome("shell.exec", "deploy", false)
	}
	p.RecordOutcome("shell.exec", "deploy", false) // 0/11 = 0%

	result := p.Evaluate(EvalRequest{Mode: "run", TaskType: "deploy"}, reg)
	names := toolNames(result)
	for _, n := range names {
		if n == "shell.exec" {
			t.Error("shell.exec should be auto-disabled (0% success rate)")
		}
	}
	// 其他工具应该仍然可用
	if len(names) != 4 {
		t.Errorf("expected 4 tools (5 minus disabled), got %d", len(names))
	}
}

func toolNames(reg tool.Registry) []string {
	var names []string
	for _, t := range reg.List() {
		names = append(names, t.Name())
	}
	return names
}
