// metacognition.go — central.metacognition 工具，把 MACCS 元认知能力暴露给中央大脑 LLM。
//
// 设计要点（对应 MACCS v2.2 智能编排理念）：
//   - 不在 prompt 里硬编码"批次/依赖/粒度"规则（那是填鸭，会过期）
//   - 让中央大脑通过这个工具按需查询系统状态：
//       complexity   → 任务复杂度预估（ComplexityEstimator）
//       memory       → 历史相似经验检索（MemoryRetriever / ProjectMemory）
//       pattern      → 已沉淀的模式库（PatternExtractor.AllPatterns）
//       brain_status → 当前 brain 池可用性 / 负载（BrainPool）
//       budget       → 当前预算剩余（DynamicBudgetPool）
//       reflect      → 让系统帮 LLM 反思一份 plan 草稿（MetaCognitiveEngine.Reflect）
//   - 中央大脑自己决定何时查、查什么、怎么用 —— 这才是"智能"
package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/leef-l/brain/sdk/agent"
	"github.com/leef-l/brain/sdk/kernel"
	"github.com/leef-l/brain/sdk/tool"
)

// metacognitionTool 是 central.metacognition 工具的实现。
//
// 它持有 Orchestrator 引用以便按需访问其内部组件（pool / learner / 等）。
// 真正的 PlanOrchestrator 组件（Estimator / MemoryRetriever / Reflector / PatternExtractor）
// 通过 SetPlanOrchestrator 注入；nil 时降级为只返回 brain_status / budget。
type metacognitionTool struct {
	orch     *kernel.Orchestrator
	planOrch *kernel.PlanOrchestrator // 可选：未注入时只支持 brain_status / budget 查询
}

// SetPlanOrchestrator 注入 PlanOrchestrator 引用，让 metacognition 能访问全部 MACCS 组件。
func (t *metacognitionTool) SetPlanOrchestrator(po *kernel.PlanOrchestrator) {
	t.planOrch = po
}

// NewMetacognitionTool 构造工具实例。
// orch 必须非 nil；planOrch 可选，未提供时元认知工具仍可查 brain 池状态等基础信息。
func NewMetacognitionTool(orch *kernel.Orchestrator) tool.Tool {
	return &metacognitionTool{orch: orch}
}

func (t *metacognitionTool) Name() string { return "central.metacognition" }

func (t *metacognitionTool) Schema() tool.Schema {
	return tool.Schema{
		Name: "central.metacognition",
		Description: "中央大脑专属的元认知查询工具。在拆 DAG / 决定如何编排前调用，可获得：" +
			"任务复杂度预估、历史相似经验、已沉淀的模式、当前 brain 可用性 + 负载、剩余预算、" +
			"或让系统帮你反思一份 plan 草稿。**强烈建议**在做非 trivial 编排决策前调用一次，" +
			"避免凭空设计 DAG 然后撞资源约束失败。",
		Brain: "central",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"query": {
					"type": "string",
					"enum": ["complexity", "memory", "pattern", "brain_status", "budget", "reflect"],
					"description": "查询类型：complexity=预估任务复杂度，memory=检索相似历史经验，pattern=查已沉淀模式库，brain_status=当前各 brain 实例状态，budget=剩余预算快照，reflect=让系统反思你给的 plan 草稿"
				},
				"goal": {
					"type": "string",
					"description": "用户原始目标或子任务描述，complexity / memory 必填"
				},
				"category": {
					"type": "string",
					"description": "pattern 查询时的 category 过滤（architecture / workflow / tool_usage / brain_selection / error_handling），可省略"
				},
				"top_k": {
					"type": "integer",
					"description": "memory / pattern 返回的最大条数，默认 5"
				}
			},
			"required": ["query"]
		}`),
	}
}

func (t *metacognitionTool) Risk() tool.Risk { return tool.RiskSafe }

func (t *metacognitionTool) Execute(ctx context.Context, args json.RawMessage) (*tool.Result, error) {
	var params struct {
		Query    string `json:"query"`
		Goal     string `json:"goal"`
		Category string `json:"category"`
		TopK     int    `json:"top_k"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return &tool.Result{
			Output:  json.RawMessage(fmt.Sprintf(`"invalid arguments: %v"`, err)),
			IsError: true,
		}, nil
	}
	if params.TopK <= 0 {
		params.TopK = 5
	}

	switch params.Query {
	case "brain_status":
		return t.queryBrainStatus()
	case "budget":
		return t.queryBudget()
	case "complexity":
		return t.queryComplexity(params.Goal)
	case "memory":
		return t.queryMemory(ctx, params.Goal, params.TopK)
	case "pattern":
		return t.queryPattern(params.Category, params.TopK)
	case "reflect":
		return t.queryReflect(ctx, params.Goal)
	default:
		return &tool.Result{
			Output:  json.RawMessage(fmt.Sprintf(`"unknown query type %q"`, params.Query)),
			IsError: true,
		}, nil
	}
}

// queryBrainStatus 返回所有可用 brain kind + 是否在线。
// 中央大脑用这个判断"当前能委派给哪些 brain，需要先 brain_manage 启动哪些"。
func (t *metacognitionTool) queryBrainStatus() (*tool.Result, error) {
	if t.orch == nil {
		return jsonResult(map[string]interface{}{"available": []string{}, "note": "orchestrator unavailable"})
	}
	kinds := t.orch.AvailableKinds()
	names := make([]string, 0, len(kinds))
	for _, k := range kinds {
		names = append(names, string(k))
	}
	sort.Strings(names)
	return jsonResult(map[string]interface{}{
		"available_brains": names,
		"count":            len(names),
		"hint": "每个 brain 当前是单实例。多个节点指向同一 brain 时无法同层并行，需要用 depends_on 串行" +
			"（这不是规则，是资源约束的客观事实 —— 你查到这个状态后自己决定怎么处理）",
	})
}

// queryBudget 返回当前 PlanOrchestrator 的预算池状态。
// 中央大脑用这个判断"还能塞多少 turn 给新任务，是否需要拆得更细"。
func (t *metacognitionTool) queryBudget() (*tool.Result, error) {
	if t.planOrch == nil || t.planOrch.BudgetPool == nil {
		return jsonResult(map[string]interface{}{
			"note":           "no active project; budget pool not initialized",
			"default_per_node": 10,
		})
	}
	pool := t.planOrch.BudgetPool
	return jsonResult(map[string]interface{}{
		"total":     pool.Total(),
		"remaining": pool.Remaining(),
		"used":      pool.Total() - pool.Remaining(),
		"hint":      "若 remaining 接近耗尽，应避免新增节点或拆得更细",
	})
}

// queryComplexity 用 ComplexityEstimator 预估给定目标的复杂度。
// 中央大脑用这个判断"任务多大、需要多少 turn、应该拆几层"。
func (t *metacognitionTool) queryComplexity(goal string) (*tool.Result, error) {
	if strings.TrimSpace(goal) == "" {
		return &tool.Result{
			Output:  json.RawMessage(`"goal is required for complexity query"`),
			IsError: true,
		}, nil
	}
	if t.planOrch == nil || t.planOrch.Estimator == nil {
		return jsonResult(map[string]interface{}{
			"note": "estimator unavailable; falling back to heuristic guess",
			"hint": "无 ComplexityEstimator 时请基于经验自己估：1 个文件 / 5-10 turn 是常见基线",
		})
	}
	// 用 PlanSubTask 包装 goal 让 Estimator 跑（仅用于预估，不会真创建任务）
	probe := kernel.PlanSubTask{
		Name:        goal,
		Instruction: goal,
		Kind:        agent.KindCode, // 默认按 code 估算（最常见）
	}
	est := t.planOrch.Estimator.Estimate(probe)
	return jsonResult(map[string]interface{}{
		"estimated_turns":   est.EstimatedTurns,
		"estimated_tokens":  est.EstimatedTokens,
		"estimated_time_ms": est.EstimatedTime.Milliseconds(),
		"confidence":        est.Confidence,
		"source":            est.Source, // heuristic / learning / transfer
		"hint":              "estimated_turns > 25 时考虑拆成多节点；< 5 turns 时可合并到现有节点",
	})
}

// queryMemory 从 ProjectMemory 检索相似历史经验。
// 中央大脑用这个看"以前类似任务怎么拆的、踩过什么坑"。
func (t *metacognitionTool) queryMemory(ctx context.Context, goal string, topK int) (*tool.Result, error) {
	if strings.TrimSpace(goal) == "" {
		return &tool.Result{
			Output:  json.RawMessage(`"goal is required for memory query"`),
			IsError: true,
		}, nil
	}
	if t.planOrch == nil || t.planOrch.Memory == nil || t.planOrch.MemoryRetriever == nil {
		return jsonResult(map[string]interface{}{
			"results": []interface{}{},
			"note":    "no project memory configured",
		})
	}
	entries, err := t.planOrch.Memory.Query(ctx, kernel.MemoryQuery{Limit: 200})
	if err != nil {
		return jsonResult(map[string]interface{}{
			"error": err.Error(),
		})
	}
	results := t.planOrch.MemoryRetriever.Retrieve(entries, goal, nil, topK)
	out := make([]map[string]interface{}, 0, len(results))
	for _, r := range results {
		out = append(out, map[string]interface{}{
			"summary":    r.Entry.Summary,
			"content":    r.Entry.Content,
			"tags":       r.Entry.Tags,
			"importance": r.Entry.Importance,
			"score":      r.Score,
			"match_type": r.MatchType,
		})
	}
	return jsonResult(map[string]interface{}{
		"goal":    goal,
		"results": out,
		"count":   len(out),
		"hint":    "若有高分相似经验，参考它的拆分方式 / 避坑点",
	})
}

// queryPattern 返回 PatternLibrary 中的已沉淀模式（4 类：architecture / workflow / tool_usage / brain_selection）。
// 中央大脑用这个看"系统对什么样的任务结构有过成功记录"。
func (t *metacognitionTool) queryPattern(category string, topK int) (*tool.Result, error) {
	if t.planOrch == nil || t.planOrch.PatternExtractor == nil {
		return jsonResult(map[string]interface{}{
			"results": []interface{}{},
			"note":    "no pattern library configured",
		})
	}
	patterns := t.planOrch.PatternExtractor.TopPatterns(category, topK)
	out := make([]map[string]interface{}, 0, len(patterns))
	for _, p := range patterns {
		out = append(out, map[string]interface{}{
			"name":         p.Name,
			"category":     p.Category,
			"description":  p.Description,
			"success_rate": p.SuccessRate,
			"frequency":    p.Frequency,
			"confidence":   p.Confidence,
		})
	}
	return jsonResult(map[string]interface{}{
		"category": category,
		"results":  out,
		"count":    len(out),
		"hint":     "高 confidence + 高 success_rate 的模式可以直接复用",
	})
}

// queryReflect 让 MetaCognitiveEngine 反思一份 plan 草稿（提供经验教训 + 推荐）。
// 注：当前不解析 plan JSON，仅返回 reflector 引用以便未来扩展（避免接口走样）。
// 中央大脑现阶段可以直接通过 memory / pattern 查询自我反思。
func (t *metacognitionTool) queryReflect(_ context.Context, goal string) (*tool.Result, error) {
	return jsonResult(map[string]interface{}{
		"note": "reflect query is reserved for future use; currently inspect memory + pattern instead",
		"goal": goal,
	})
}

// jsonResult 包装一个 map 为 tool.Result（JSON 输出，非错误）。
func jsonResult(data map[string]interface{}) (*tool.Result, error) {
	bytes, err := json.Marshal(data)
	if err != nil {
		return &tool.Result{
			Output:  json.RawMessage(fmt.Sprintf(`"marshal failed: %v"`, err)),
			IsError: true,
		}, nil
	}
	return &tool.Result{Output: bytes, IsError: false}, nil
}
