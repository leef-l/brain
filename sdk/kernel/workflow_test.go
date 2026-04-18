package kernel

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/leef-l/brain/sdk/flow"
)

// mockExecutor 返回一个记录调用顺序的 NodeExecutor。
func mockExecutor(log *[]string, mu *sync.Mutex) NodeExecutor {
	return func(ctx context.Context, node WorkflowNode, input string) (string, error) {
		mu.Lock()
		*log = append(*log, node.ID)
		mu.Unlock()

		output := fmt.Sprintf("output-%s", node.ID)
		if input != "" {
			output = fmt.Sprintf("%s(input:%s)", output, input)
		}
		return output, nil
	}
}

// TestWorkflowLinearDAG 测试线性 DAG：A → B → C
func TestWorkflowLinearDAG(t *testing.T) {
	store := flow.NewMemStore()
	var log []string
	var mu sync.Mutex
	engine := NewWorkflowEngine(store, mockExecutor(&log, &mu))

	wf := &Workflow{
		ID: "linear",
		Nodes: []WorkflowNode{
			{ID: "A", BrainID: "brain-a", Prompt: "do A"},
			{ID: "B", BrainID: "brain-b", Prompt: "do B", DependsOn: []string{"A"}},
			{ID: "C", BrainID: "brain-c", Prompt: "do C", DependsOn: []string{"B"}},
		},
		Edges: []WorkflowEdge{
			{From: "A", To: "B", Mode: flow.EdgeMaterialized},
			{From: "B", To: "C", Mode: flow.EdgeMaterialized},
		},
	}

	result, err := engine.Execute(context.Background(), wf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.State != StateCompleted {
		t.Fatalf("expected completed, got %s", result.State)
	}

	// 线性 DAG 必须严格按序执行
	if len(log) != 3 {
		t.Fatalf("expected 3 executions, got %d", len(log))
	}
	if log[0] != "A" || log[1] != "B" || log[2] != "C" {
		t.Fatalf("expected order [A B C], got %v", log)
	}

	// B 应该收到 A 的输出
	bResult := result.Nodes["B"]
	if !strings.Contains(bResult.Output, "output-A") {
		t.Errorf("B should contain A's output, got: %s", bResult.Output)
	}

	// C 应该收到 B 的输出（含 A 的传递）
	cResult := result.Nodes["C"]
	if !strings.Contains(cResult.Output, "output-B") {
		t.Errorf("C should contain B's output, got: %s", cResult.Output)
	}
}

// TestWorkflowParallelDAG 测试并行 DAG：A→C, B→C
func TestWorkflowParallelDAG(t *testing.T) {
	store := flow.NewMemStore()
	var log []string
	var mu sync.Mutex
	engine := NewWorkflowEngine(store, mockExecutor(&log, &mu))

	wf := &Workflow{
		ID: "parallel",
		Nodes: []WorkflowNode{
			{ID: "A", BrainID: "brain-a", Prompt: "do A"},
			{ID: "B", BrainID: "brain-b", Prompt: "do B"},
			{ID: "C", BrainID: "brain-c", Prompt: "do C", DependsOn: []string{"A", "B"}},
		},
		Edges: []WorkflowEdge{
			{From: "A", To: "C", Mode: flow.EdgeMaterialized},
			{From: "B", To: "C", Mode: flow.EdgeMaterialized},
		},
	}

	result, err := engine.Execute(context.Background(), wf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.State != StateCompleted {
		t.Fatalf("expected completed, got %s", result.State)
	}

	// A 和 B 在第一层（可并行），C 在第二层
	if len(log) != 3 {
		t.Fatalf("expected 3 executions, got %d", len(log))
	}

	// C 必须在 A 和 B 之后
	cIdx := -1
	for i, id := range log {
		if id == "C" {
			cIdx = i
		}
	}
	if cIdx < 2 {
		t.Fatalf("C should execute after A and B, got order: %v", log)
	}

	// C 应该收到 A 和 B 的输出
	cResult := result.Nodes["C"]
	if !strings.Contains(cResult.Output, "output-A") || !strings.Contains(cResult.Output, "output-B") {
		t.Errorf("C should contain both A and B outputs, got: %s", cResult.Output)
	}
}

// TestWorkflowCycleDetection 测试环路检测：A→B→C→A
func TestWorkflowCycleDetection(t *testing.T) {
	store := flow.NewMemStore()
	var log []string
	var mu sync.Mutex
	engine := NewWorkflowEngine(store, mockExecutor(&log, &mu))

	wf := &Workflow{
		ID: "cycle",
		Nodes: []WorkflowNode{
			{ID: "A", BrainID: "brain-a", Prompt: "do A", DependsOn: []string{"C"}},
			{ID: "B", BrainID: "brain-b", Prompt: "do B", DependsOn: []string{"A"}},
			{ID: "C", BrainID: "brain-c", Prompt: "do C", DependsOn: []string{"B"}},
		},
		Edges: []WorkflowEdge{
			{From: "A", To: "B", Mode: flow.EdgeMaterialized},
			{From: "B", To: "C", Mode: flow.EdgeMaterialized},
			{From: "C", To: "A", Mode: flow.EdgeMaterialized},
		},
	}

	_, err := engine.Execute(context.Background(), wf)
	if err == nil {
		t.Fatal("expected cycle detection error, got nil")
	}
	if err != ErrWorkflowCycle {
		t.Fatalf("expected ErrWorkflowCycle, got: %v", err)
	}

	// 不应有任何节点被执行
	if len(log) != 0 {
		t.Fatalf("expected no executions, got %d: %v", len(log), log)
	}
}

// TestWorkflowEmpty 测试空 DAG
func TestWorkflowEmpty(t *testing.T) {
	store := flow.NewMemStore()
	engine := NewWorkflowEngine(store, nil)

	wf := &Workflow{
		ID:    "empty",
		Nodes: nil,
	}

	_, err := engine.Execute(context.Background(), wf)
	if err != ErrWorkflowEmpty {
		t.Fatalf("expected ErrWorkflowEmpty, got: %v", err)
	}
}

// TestWorkflowSingleNode 测试单节点 DAG
func TestWorkflowSingleNode(t *testing.T) {
	store := flow.NewMemStore()
	var log []string
	var mu sync.Mutex
	engine := NewWorkflowEngine(store, mockExecutor(&log, &mu))

	wf := &Workflow{
		ID: "single",
		Nodes: []WorkflowNode{
			{ID: "A", BrainID: "brain-a", Prompt: "do A"},
		},
	}

	result, err := engine.Execute(context.Background(), wf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.State != StateCompleted {
		t.Fatalf("expected completed, got %s", result.State)
	}
	if len(log) != 1 || log[0] != "A" {
		t.Fatalf("expected [A], got %v", log)
	}
}

// TestWorkflowContextCancel 测试 context 取消
func TestWorkflowContextCancel(t *testing.T) {
	store := flow.NewMemStore()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即取消

	var log []string
	var mu sync.Mutex
	engine := NewWorkflowEngine(store, mockExecutor(&log, &mu))

	wf := &Workflow{
		ID: "cancel",
		Nodes: []WorkflowNode{
			{ID: "A", BrainID: "brain-a", Prompt: "do A"},
		},
	}

	result, err := engine.Execute(ctx, wf)
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
	if result.State != StateCanceled {
		t.Fatalf("expected canceled state, got %s", result.State)
	}
}

// TestWorkflowInvalidDependency 测试引用不存在的依赖
func TestWorkflowInvalidDependency(t *testing.T) {
	store := flow.NewMemStore()
	var log []string
	var mu sync.Mutex
	engine := NewWorkflowEngine(store, mockExecutor(&log, &mu))

	wf := &Workflow{
		ID: "invalid-dep",
		Nodes: []WorkflowNode{
			{ID: "A", BrainID: "brain-a", Prompt: "do A", DependsOn: []string{"Z"}},
		},
	}

	_, err := engine.Execute(context.Background(), wf)
	if err == nil {
		t.Fatal("expected error for invalid dependency")
	}
	if !strings.Contains(err.Error(), "Z") {
		t.Fatalf("error should mention missing node Z, got: %v", err)
	}
}

// TestWorkflowDiamondDAG 测试菱形 DAG：A→B, A→C, B→D, C→D
func TestWorkflowDiamondDAG(t *testing.T) {
	store := flow.NewMemStore()
	var log []string
	var mu sync.Mutex
	engine := NewWorkflowEngine(store, mockExecutor(&log, &mu))

	wf := &Workflow{
		ID: "diamond",
		Nodes: []WorkflowNode{
			{ID: "A", BrainID: "brain-a", Prompt: "do A"},
			{ID: "B", BrainID: "brain-b", Prompt: "do B", DependsOn: []string{"A"}},
			{ID: "C", BrainID: "brain-c", Prompt: "do C", DependsOn: []string{"A"}},
			{ID: "D", BrainID: "brain-d", Prompt: "do D", DependsOn: []string{"B", "C"}},
		},
		Edges: []WorkflowEdge{
			{From: "A", To: "B", Mode: flow.EdgeMaterialized},
			{From: "A", To: "C", Mode: flow.EdgeMaterialized},
			{From: "B", To: "D", Mode: flow.EdgeMaterialized},
			{From: "C", To: "D", Mode: flow.EdgeMaterialized},
		},
	}

	result, err := engine.Execute(context.Background(), wf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.State != StateCompleted {
		t.Fatalf("expected completed, got %s", result.State)
	}
	if len(result.Nodes) != 4 {
		t.Fatalf("expected 4 results, got %d", len(result.Nodes))
	}

	// D 必须最后执行
	dIdx := -1
	for i, id := range log {
		if id == "D" {
			dIdx = i
		}
	}
	if dIdx < 3 {
		t.Fatalf("D should be last, got order: %v", log)
	}
}
