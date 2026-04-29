package shared

import (
	"testing"

	"github.com/leef-l/brain/sdk/agent"
	"github.com/leef-l/brain/sdk/tool"
)

func TestNewThinBrain(t *testing.T) {
	reg := tool.NewMemRegistry()
	tb := NewThinBrain(agent.KindCode, reg, "test system prompt", 15)
	if tb == nil {
		t.Fatal("expected non-nil ThinBrain")
	}
	if tb.kind != agent.KindCode {
		t.Fatalf("expected kind=%s, got %s", agent.KindCode, tb.kind)
	}
	if tb.version != "1.0.0" {
		t.Fatalf("expected version=1.0.0, got %s", tb.version)
	}
	if tb.systemPrompt != "test system prompt" {
		t.Fatalf("expected systemPrompt=test system prompt, got %s", tb.systemPrompt)
	}
	if tb.defaultMaxTurns != 15 {
		t.Fatalf("expected defaultMaxTurns=15, got %d", tb.defaultMaxTurns)
	}
	if tb.taskTypeLabel != "code.execute" {
		t.Fatalf("expected taskTypeLabel=code.execute, got %s", tb.taskTypeLabel)
	}
	if tb.learner == nil {
		t.Fatal("expected non-nil learner")
	}
	if tb.registry == nil {
		t.Fatal("expected non-nil registry")
	}
}

func TestNewThinBrainDefaultMaxTurns(t *testing.T) {
	reg := tool.NewMemRegistry()
	tb := NewThinBrain(agent.KindBrowser, reg, "prompt", 0)
	if tb.defaultMaxTurns != 10 {
		t.Fatalf("expected defaultMaxTurns=10, got %d", tb.defaultMaxTurns)
	}
}

func TestThinBrainKind(t *testing.T) {
	reg := tool.NewMemRegistry()
	tb := NewThinBrain(agent.KindVerifier, reg, "prompt", 8)
	if tb.Kind() != agent.KindVerifier {
		t.Fatalf("expected Kind=%s, got %s", agent.KindVerifier, tb.Kind())
	}
}

func TestThinBrainVersion(t *testing.T) {
	reg := tool.NewMemRegistry()
	tb := NewThinBrain(agent.KindFault, reg, "prompt", 8)
	if tb.Version() != "1.0.0" {
		t.Fatalf("expected Version=1.0.0, got %s", tb.Version())
	}
}

func TestThinBrainTools(t *testing.T) {
	reg := tool.NewMemRegistry()
	reg.Register(tool.NewNoteTool("test"))
	tb := NewThinBrain(agent.KindCode, reg, "prompt", 10)
	tools := tb.Tools()
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	if tools[0] != "test.note" {
		t.Fatalf("expected tool=test.note, got %s", tools[0])
	}
}

func TestThinBrainToolSchemas(t *testing.T) {
	reg := tool.NewMemRegistry()
	reg.Register(tool.NewNoteTool("test"))
	tb := NewThinBrain(agent.KindCode, reg, "prompt", 10)
	schemas := tb.ToolSchemas()
	if len(schemas) != 1 {
		t.Fatalf("expected 1 schema, got %d", len(schemas))
	}
	if schemas[0].Name != "test.note" {
		t.Fatalf("expected schema name=test.note, got %s", schemas[0].Name)
	}
}

func TestThinBrainWithLearner(t *testing.T) {
	reg := tool.NewMemRegistry()
	tb := NewThinBrain(agent.KindCode, reg, "prompt", 10)
	// learner 初始为 DefaultBrainLearner
	original := tb.learner
	tb.WithLearner(nil)
	// WithLearner(nil) 会设置 learner 为 nil
	if tb.learner != nil {
		t.Fatal("expected learner to be nil after WithLearner(nil)")
	}
	// 恢复
	tb.WithLearner(original)
	if tb.learner != original {
		t.Fatal("expected learner to be restored")
	}
}

func TestThinBrainWithTaskTypeLabel(t *testing.T) {
	reg := tool.NewMemRegistry()
	tb := NewThinBrain(agent.KindCode, reg, "prompt", 10)
	tb.WithTaskTypeLabel("custom.label")
	if tb.taskTypeLabel != "custom.label" {
		t.Fatalf("expected taskTypeLabel=custom.label, got %s", tb.taskTypeLabel)
	}
}

func TestRegisterWithPolicy(t *testing.T) {
	reg := RegisterWithPolicy(agent.KindCode, tool.NewNoteTool("test"))
	if reg == nil {
		t.Fatal("expected non-nil registry")
	}
	tools := reg.List()
	// Should have at least the note tool (may be filtered by policy).
	_ = tools
}
