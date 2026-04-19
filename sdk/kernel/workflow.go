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

// StreamingNodeExecutor 执行单个节点，通过 writer 逐帧输出。
// writer 是一个回调函数，每次调用写入一帧数据。执行完成后返回 error。
type StreamingNodeExecutor func(ctx context.Context, node WorkflowNode, input string, writer func(chunk []byte) error) error

// ---------------------------------------------------------------------------
// WorkflowEngine
// ---------------------------------------------------------------------------

// WorkflowEngine 执行 DAG 工作流。
type WorkflowEngine struct {
	store             flow.Store             // CAS 存储：materialized edge 数据传递
	executor          NodeExecutor           // 实际执行节点的函数（可注入 mock）
	streamingExecutor StreamingNodeExecutor  // 流式节点执行器（可选）
	pipes             *flow.PipeRegistry     // streaming edge 的 pipe 管理
}

// NewWorkflowEngine 创建 WorkflowEngine。
func NewWorkflowEngine(store flow.Store, executor NodeExecutor) *WorkflowEngine {
	return &WorkflowEngine{
		store:    store,
		executor: executor,
		pipes:    flow.NewPipeRegistry(),
	}
}

// SetStreamingExecutor 设置流式节点执行器。
// 如果设置了该执行器，streaming edge 的前驱节点会使用它来逐帧输出。
// 如果未设置，会使用普通 executor 的输出一次性写入 pipe。
func (e *WorkflowEngine) SetStreamingExecutor(exec StreamingNodeExecutor) {
	e.streamingExecutor = exec
}

// ---------------------------------------------------------------------------
// Execute — 核心：拓扑排序 + 分层并行执行
// ---------------------------------------------------------------------------

// Execute 按拓扑序执行 DAG。返回 WorkflowResult 或错误。
//
// 对于 materialized edge，前驱节点完成后将输出写入 CAS，后继节点从 CAS 读取。
// 对于 streaming edge，前驱和后继通过 pipe 并行执行：前驱逐帧写入 pipe，
// 后继从 pipe 实时读取，无需等前驱完成。
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

	// 验证 Edge 引用的节点存在，构建 edgeMap
	edgeMap := make(map[string][]WorkflowEdge) // to-node -> edges
	outEdgeMap := make(map[string][]WorkflowEdge) // from-node -> edges
	for _, edge := range wf.Edges {
		if _, ok := nodeMap[edge.From]; !ok {
			return nil, fmt.Errorf("%w: edge from %q", ErrEdgeMismatch, edge.From)
		}
		if _, ok := nodeMap[edge.To]; !ok {
			return nil, fmt.Errorf("%w: edge to %q", ErrEdgeMismatch, edge.To)
		}
		edgeMap[edge.To] = append(edgeMap[edge.To], edge)
		outEdgeMap[edge.From] = append(outEdgeMap[edge.From], edge)
	}

	// 2. 为所有 streaming edge 创建 pipe
	streamingEdges := make(map[string]WorkflowEdge) // edgeID -> edge
	for _, edge := range wf.Edges {
		if edge.Mode == flow.EdgeStreaming {
			edgeID := edge.From + "->" + edge.To
			streamingEdges[edgeID] = edge
			if _, err := e.pipes.Create(edgeID, 64); err != nil {
				return nil, fmt.Errorf("create pipe for edge %s: %w", edgeID, err)
			}
		}
	}
	// 确保退出时清理所有 pipe
	defer e.pipes.CloseAll()

	// 3. 拓扑排序（Kahn's algorithm）
	layers, err := topoSort(wf.Nodes)
	if err != nil {
		return nil, err
	}

	// 4. 识别 streaming 节点对：前驱和后继可以并行执行
	// streamingConsumers 记录哪些后继节点有 streaming 入边
	streamingConsumers := make(map[string]bool)
	for _, edge := range wf.Edges {
		if edge.Mode == flow.EdgeStreaming {
			streamingConsumers[edge.To] = true
		}
	}

	// 5. 分层执行
	result := &WorkflowResult{
		WorkflowID: wf.ID,
		Nodes:      make(map[string]NodeResult, len(wf.Nodes)),
		State:      StateRunning,
	}

	nodeRefs := make(map[string]flow.Ref)
	var mu sync.Mutex

	// streamingDone 跟踪 streaming 后继节点的完成状态
	streamingDone := make(map[string]chan struct{})
	streamingErrors := make(map[string]error)

	for _, layer := range layers {
		if err := ctx.Err(); err != nil {
			result.State = StateCanceled
			return result, err
		}

		var wg sync.WaitGroup
		errCh := make(chan error, len(layer)*2) // 可能有额外的 streaming consumer goroutine

		for _, nodeID := range layer {
			nid := nodeID

			// 如果该节点是 streaming consumer 且已经在前驱层被启动了，跳过
			if streamingConsumers[nid] {
				mu.Lock()
				_, alreadyStarted := streamingDone[nid]
				mu.Unlock()
				if alreadyStarted {
					// 等待已启动的 streaming consumer 完成
					wg.Add(1)
					go func() {
						defer wg.Done()
						mu.Lock()
						ch := streamingDone[nid]
						mu.Unlock()
						<-ch
						mu.Lock()
						if err := streamingErrors[nid]; err != nil {
							errCh <- err
						}
						mu.Unlock()
					}()
					continue
				}
			}

			wg.Add(1)
			go func(nid string) {
				defer wg.Done()

				node := nodeMap[nid]

				// 检查该节点是否有 streaming 出边
				hasStreamingOut := false
				for _, oe := range outEdgeMap[nid] {
					if oe.Mode == flow.EdgeStreaming {
						hasStreamingOut = true
						break
					}
				}

				if hasStreamingOut {
					// Streaming 前驱：启动前驱节点 + 并行启动后继 consumer
					e.executeStreamingProducer(ctx, nid, node, edgeMap, outEdgeMap,
						nodeMap, nodeRefs, &mu, result, streamingDone, streamingErrors, errCh, &wg)
				} else {
					// 普通节点：materialized edge
					e.executeNormalNode(ctx, nid, node, edgeMap, nodeRefs, &mu, result, errCh)
				}
			}(nid)
		}

		wg.Wait()
		close(errCh)

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

// executeNormalNode 执行普通节点（materialized edge 或无出边）。
func (e *WorkflowEngine) executeNormalNode(
	ctx context.Context,
	nid string,
	node *WorkflowNode,
	edgeMap map[string][]WorkflowEdge,
	nodeRefs map[string]flow.Ref,
	mu *sync.Mutex,
	result *WorkflowResult,
	errCh chan<- error,
) {
	nr := NodeResult{
		NodeID:    nid,
		StartedAt: time.Now().UTC(),
	}

	input, inputErr := e.collectInputs(ctx, nid, edgeMap[nid], nodeRefs, mu)
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
}

// executeStreamingProducer 执行 streaming 前驱节点，同时并行启动后继 consumer。
func (e *WorkflowEngine) executeStreamingProducer(
	ctx context.Context,
	nid string,
	node *WorkflowNode,
	edgeMap map[string][]WorkflowEdge,
	outEdgeMap map[string][]WorkflowEdge,
	nodeMap map[string]*WorkflowNode,
	nodeRefs map[string]flow.Ref,
	mu *sync.Mutex,
	result *WorkflowResult,
	streamingDone map[string]chan struct{},
	streamingErrors map[string]error,
	errCh chan<- error,
	wg *sync.WaitGroup,
) {
	nr := NodeResult{
		NodeID:    nid,
		StartedAt: time.Now().UTC(),
	}

	// 收集 materialized 上游输入（streaming 入边通过 pipe 读取）
	input, inputErr := e.collectInputs(ctx, nid, edgeMap[nid], nodeRefs, mu)
	if inputErr != nil {
		nr.State = StateFailed
		nr.Error = inputErr.Error()
		nr.EndedAt = time.Now().UTC()
		mu.Lock()
		result.Nodes[nid] = nr
		mu.Unlock()
		// 关闭所有出边的 pipe，防止 consumer 永久阻塞
		for _, oe := range outEdgeMap[nid] {
			if oe.Mode == flow.EdgeStreaming {
				e.pipes.Close(oe.From + "->" + oe.To)
			}
		}
		errCh <- fmt.Errorf("node %s input error: %w", nid, inputErr)
		return
	}

	// 先启动 streaming 后继 consumer（在前驱写数据之前就开始监听）
	for _, oe := range outEdgeMap[nid] {
		if oe.Mode != flow.EdgeStreaming {
			continue
		}
		consumerID := oe.To
		edgeID := oe.From + "->" + oe.To

		mu.Lock()
		if _, exists := streamingDone[consumerID]; exists {
			mu.Unlock()
			continue
		}
		done := make(chan struct{})
		streamingDone[consumerID] = done
		mu.Unlock()

		// 在启动 goroutine 前获取 pipe 引用，避免与 producer Close 竞态
		pipe, ok := e.pipes.Get(edgeID)
		if !ok {
			continue
		}

		wg.Add(1)
		go func(cid, eid string, doneCh chan struct{}, pipe *flow.PipeBackend) {
			defer wg.Done()
			defer close(doneCh)

			consumerNode := nodeMap[cid]
			cnr := NodeResult{
				NodeID:    cid,
				StartedAt: time.Now().UTC(),
			}

			// 读取流式输入
			var streamInput []byte
			for {
				chunk, readErr := pipe.Read(ctx)
				if readErr != nil {
					break // EOF 或其他错误
				}
				streamInput = append(streamInput, chunk...)
			}

			// 执行 consumer 节点
			output, execErr := e.executor(ctx, *consumerNode, string(streamInput))
			cnr.EndedAt = time.Now().UTC()

			if execErr != nil {
				cnr.State = StateFailed
				cnr.Error = execErr.Error()
				mu.Lock()
				result.Nodes[cid] = cnr
				streamingErrors[cid] = fmt.Errorf("node %s failed: %w", cid, execErr)
				mu.Unlock()
				return
			}

			cnr.Output = output
			cnr.State = StateCompleted

			ref, casErr := e.store.Put(ctx, []byte(output))
			if casErr != nil {
				cnr.State = StateFailed
				cnr.Error = casErr.Error()
				mu.Lock()
				result.Nodes[cid] = cnr
				streamingErrors[cid] = fmt.Errorf("node %s CAS put error: %w", cid, casErr)
				mu.Unlock()
				return
			}
			cnr.Ref = ref

			mu.Lock()
			result.Nodes[cid] = cnr
			nodeRefs[cid] = ref
			mu.Unlock()
		}(consumerID, edgeID, done, pipe)
	}

	// 执行前驱节点
	if e.streamingExecutor != nil {
		// 使用 streaming executor，逐帧写入所有 streaming pipe
		execErr := e.streamingExecutor(ctx, *node, input, func(chunk []byte) error {
			for _, oe := range outEdgeMap[nid] {
				if oe.Mode != flow.EdgeStreaming {
					continue
				}
				edgeID := oe.From + "->" + oe.To
				pipe, ok := e.pipes.Get(edgeID)
				if !ok {
					continue
				}
				if err := pipe.Write(ctx, chunk); err != nil {
					return err
				}
			}
			return nil
		})

		nr.EndedAt = time.Now().UTC()

		// 关闭所有 streaming 出边的 pipe
		for _, oe := range outEdgeMap[nid] {
			if oe.Mode == flow.EdgeStreaming {
				e.pipes.Close(oe.From + "->" + oe.To)
			}
		}

		if execErr != nil {
			nr.State = StateFailed
			nr.Error = execErr.Error()
			mu.Lock()
			result.Nodes[nid] = nr
			mu.Unlock()
			errCh <- fmt.Errorf("node %s failed: %w", nid, execErr)
			return
		}

		// streaming executor 不返回完整 output，记录为空
		nr.State = StateCompleted
		ref, casErr := e.store.Put(ctx, []byte(""))
		if casErr == nil {
			nr.Ref = ref
			mu.Lock()
			nodeRefs[nid] = ref
			mu.Unlock()
		}
	} else {
		// 使用普通 executor，完成后一次性写入所有 streaming pipe
		output, execErr := e.executor(ctx, *node, input)
		nr.EndedAt = time.Now().UTC()

		if execErr != nil {
			nr.State = StateFailed
			nr.Error = execErr.Error()
			mu.Lock()
			result.Nodes[nid] = nr
			mu.Unlock()
			// 关闭 pipe 让 consumer 退出
			for _, oe := range outEdgeMap[nid] {
				if oe.Mode == flow.EdgeStreaming {
					e.pipes.Close(oe.From + "->" + oe.To)
				}
			}
			errCh <- fmt.Errorf("node %s failed: %w", nid, execErr)
			return
		}

		nr.Output = output
		nr.State = StateCompleted

		// 将输出写入所有 streaming pipe
		for _, oe := range outEdgeMap[nid] {
			if oe.Mode != flow.EdgeStreaming {
				continue
			}
			edgeID := oe.From + "->" + oe.To
			pipe, ok := e.pipes.Get(edgeID)
			if ok {
				pipe.Write(ctx, []byte(output))
			}
		}
		// 关闭 pipe 通知 consumer 数据写完
		for _, oe := range outEdgeMap[nid] {
			if oe.Mode == flow.EdgeStreaming {
				e.pipes.Close(oe.From + "->" + oe.To)
			}
		}

		// 写入 CAS
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
	}

	if nr.State == StateCompleted {
		mu.Lock()
		result.Nodes[nid] = nr
		mu.Unlock()
	}
}

// collectInputs 从 CAS 中读取上游节点的输出（仅 materialized edge），拼接为输入字符串。
// streaming edge 的输入通过 pipe 直接读取，不经过此方法。
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
		// streaming edge 的输入通过 pipe 读取，跳过
		if edge.Mode == flow.EdgeStreaming {
			continue
		}

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
