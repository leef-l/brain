// Package kernel — WorkflowEngine 实现 DAG 编排。
//
// Workflow 将多个 Brain 任务组织为有向无环图（DAG），节点是 TaskExecution，
// 边定义了节点间的数据传递方式（materialized 通过 CAS、streaming 通过 Pipe）。
// WorkflowEngine.Execute 按拓扑序执行 DAG，同层节点并行执行。
package kernel

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/leef-l/brain/sdk/flow"
)

// ---------------------------------------------------------------------------
// 错误定义
// ---------------------------------------------------------------------------

var (
	ErrWorkflowCycle    = errors.New("workflow contains a cycle")
	ErrWorkflowEmpty    = errors.New("workflow has no nodes")
	ErrNodeNotFound     = errors.New("dependency references unknown node")
	ErrEdgeMismatch     = errors.New("edge references unknown node")
)

// ---------------------------------------------------------------------------
// WorkflowNode / WorkflowEdge / Workflow
// ---------------------------------------------------------------------------

// WorkflowNode 是 DAG 中的一个节点，对应一次 Brain 任务执行。
type WorkflowNode struct {
	ID        string   `json:"id"`
	BrainID   string   `json:"brain_id"`
	Prompt    string   `json:"prompt"`
	DependsOn []string `json:"depends_on,omitempty"` // 依赖的节点 ID
}

// WorkflowEdge 定义节点间的数据传递。
type WorkflowEdge struct {
	From string        `json:"from"` // 源节点 ID
	To   string        `json:"to"`   // 目标节点 ID
	Mode flow.EdgeType `json:"mode"` // materialized 或 streaming
}

// Workflow 是一个 DAG 任务图。
type Workflow struct {
	ID    string         `json:"id"`
	Nodes []WorkflowNode `json:"nodes"`
	Edges []WorkflowEdge `json:"edges"`
}

// ---------------------------------------------------------------------------
// WorkflowResult
// ---------------------------------------------------------------------------

// NodeResult 单个节点的执行结果。
type NodeResult struct {
	NodeID    string         `json:"node_id"`
	Output    string         `json:"output"`
	Ref       flow.Ref       `json:"ref,omitempty"`
	State     ExecutionState `json:"state"`
	Error     string         `json:"error,omitempty"`
	StartedAt time.Time      `json:"started_at"`
	EndedAt   time.Time      `json:"ended_at"`
}

// WorkflowResult 是整个 DAG 执行的结果。
type WorkflowResult struct {
	WorkflowID string                `json:"workflow_id"`
	Nodes      map[string]NodeResult `json:"nodes"`
	State      ExecutionState        `json:"state"`
}

// ---------------------------------------------------------------------------
// NodeExecutor — 可替换的节点执行函数，方便测试
// ---------------------------------------------------------------------------

// NodeExecutor 执行单个节点，接收 prompt（可能含上游拼接输入），返回输出字符串。
type NodeExecutor func(ctx context.Context, node WorkflowNode, input string) (string, error)

// ---------------------------------------------------------------------------
// WorkflowEngine
// ---------------------------------------------------------------------------

// WorkflowEngine 执行 DAG 工作流。
type WorkflowEngine struct {
	store    flow.Store   // CAS 存储：materialized edge 数据传递
	executor NodeExecutor // 实际执行节点的函数（可注入 mock）
}

// NewWorkflowEngine 创建 WorkflowEngine。
func NewWorkflowEngine(store flow.Store, executor NodeExecutor) *WorkflowEngine {
	return &WorkflowEngine{
		store:    store,
		executor: executor,
	}
}

// ---------------------------------------------------------------------------
// Execute — 核心：拓扑排序 + 分层并行执行
// ---------------------------------------------------------------------------

// Execute 按拓扑序执行 DAG。返回 WorkflowResult 或错误。
func (e *WorkflowEngine) Execute(ctx context.Context, wf *Workflow) (*WorkflowResult, error) {
	if len(wf.Nodes) == 0 {
		return nil, ErrWorkflowEmpty
	}

	// 1. 构建索引
	nodeMap := make(map[string]*WorkflowNode, len(wf.Nodes))
	for i := range wf.Nodes {
		nodeMap[wf.Nodes[i].ID] = &wf.Nodes[i]
	}

	// 验证 DependsOn 引用的节点存在
	for _, n := range wf.Nodes {
		for _, dep := range n.DependsOn {
			if _, ok := nodeMap[dep]; !ok {
				return nil, fmt.Errorf("%w: node %q depends on %q", ErrNodeNotFound, n.ID, dep)
			}
		}
	}

	// 验证 Edge 引用的节点存在
	edgeMap := make(map[string][]WorkflowEdge) // to-node -> edges
	for _, edge := range wf.Edges {
		if _, ok := nodeMap[edge.From]; !ok {
			return nil, fmt.Errorf("%w: edge from %q", ErrEdgeMismatch, edge.From)
		}
		if _, ok := nodeMap[edge.To]; !ok {
			return nil, fmt.Errorf("%w: edge to %q", ErrEdgeMismatch, edge.To)
		}
		edgeMap[edge.To] = append(edgeMap[edge.To], edge)
	}

	// 2. 拓扑排序（Kahn's algorithm）
	layers, err := topoSort(wf.Nodes)
	if err != nil {
		return nil, err
	}

	// 3. 分层执行
	result := &WorkflowResult{
		WorkflowID: wf.ID,
		Nodes:      make(map[string]NodeResult, len(wf.Nodes)),
		State:      StateRunning,
	}

	// nodeRefs 保存每个节点输出在 CAS 中的 Ref
	nodeRefs := make(map[string]flow.Ref)
	var mu sync.Mutex

	for _, layer := range layers {
		if err := ctx.Err(); err != nil {
			result.State = StateCanceled
			return result, err
		}

		var wg sync.WaitGroup
		errCh := make(chan error, len(layer))

		for _, nodeID := range layer {
			wg.Add(1)
			go func(nid string) {
				defer wg.Done()

				node := nodeMap[nid]
				nr := NodeResult{
					NodeID:    nid,
					StartedAt: time.Now().UTC(),
				}

				// 收集上游输入
				input, inputErr := e.collectInputs(ctx, nid, edgeMap[nid], nodeRefs, &mu)
				if inputErr != nil {
					nr.State = StateFailed
					nr.Error = inputErr.Error()
					nr.EndedAt = time.Now().UTC()
					mu.Lock()
					result.Nodes[nid] = nr
					mu.Unlock()
					errCh <- fmt.Errorf("node %s input error: %w", nid, inputErr)
					return
				}

				// 执行节点
				output, execErr := e.executor(ctx, *node, input)
				nr.EndedAt = time.Now().UTC()

				if execErr != nil {
					nr.State = StateFailed
					nr.Error = execErr.Error()
					mu.Lock()
					result.Nodes[nid] = nr
					mu.Unlock()
					errCh <- fmt.Errorf("node %s failed: %w", nid, execErr)
					return
				}

				nr.Output = output
				nr.State = StateCompleted

				// 将输出写入 CAS
				ref, casErr := e.store.Put(ctx, []byte(output))
				if casErr != nil {
					nr.State = StateFailed
					nr.Error = casErr.Error()
					mu.Lock()
					result.Nodes[nid] = nr
					mu.Unlock()
					errCh <- fmt.Errorf("node %s CAS put error: %w", nid, casErr)
					return
				}
				nr.Ref = ref

				mu.Lock()
				result.Nodes[nid] = nr
				nodeRefs[nid] = ref
				mu.Unlock()
			}(nodeID)
		}

		wg.Wait()
		close(errCh)

		// 检查本层是否有错误
		var layerErr error
		for e := range errCh {
			if layerErr == nil {
				layerErr = e
			}
		}
		if layerErr != nil {
			result.State = StateFailed
			return result, layerErr
		}
	}

	result.State = StateCompleted
	return result, nil
}

// collectInputs 从 CAS 中读取上游节点的输出，拼接为输入字符串。
func (e *WorkflowEngine) collectInputs(
	ctx context.Context,
	nodeID string,
	edges []WorkflowEdge,
	nodeRefs map[string]flow.Ref,
	mu *sync.Mutex,
) (string, error) {
	if len(edges) == 0 {
		return "", nil
	}

	var parts []string
	for _, edge := range edges {
		mu.Lock()
		ref, ok := nodeRefs[edge.From]
		mu.Unlock()
		if !ok {
			return "", fmt.Errorf("upstream node %q has no output ref", edge.From)
		}

		data, err := e.store.Get(ctx, ref)
		if err != nil {
			return "", fmt.Errorf("CAS get for node %q output: %w", edge.From, err)
		}
		parts = append(parts, string(data))
	}

	// 多个输入用换行分隔
	result := ""
	for i, p := range parts {
		if i > 0 {
			result += "\n"
		}
		result += p
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// 拓扑排序（Kahn's algorithm）
// ---------------------------------------------------------------------------

// topoSort 对节点进行拓扑排序，返回分层的节点 ID。
// 同一层的节点没有相互依赖，可以并行执行。
// 如果存在环路，返回 ErrWorkflowCycle。
func topoSort(nodes []WorkflowNode) ([][]string, error) {
	if len(nodes) == 0 {
		return nil, nil
	}

	// 构建入度表和邻接表
	inDegree := make(map[string]int, len(nodes))
	successors := make(map[string][]string) // node -> 依赖它的节点列表

	for _, n := range nodes {
		if _, ok := inDegree[n.ID]; !ok {
			inDegree[n.ID] = 0
		}
		for _, dep := range n.DependsOn {
			inDegree[n.ID]++
			successors[dep] = append(successors[dep], n.ID)
		}
	}

	// Kahn's algorithm，按层输出
	var layers [][]string
	var queue []string

	for id, deg := range inDegree {
		if deg == 0 {
			queue = append(queue, id)
		}
	}

	visited := 0
	for len(queue) > 0 {
		layer := queue
		queue = nil
		layers = append(layers, layer)
		visited += len(layer)

		for _, id := range layer {
			for _, succ := range successors[id] {
				inDegree[succ]--
				if inDegree[succ] == 0 {
					queue = append(queue, succ)
				}
			}
		}
	}

	if visited != len(nodes) {
		return nil, ErrWorkflowCycle
	}

	return layers, nil
}
