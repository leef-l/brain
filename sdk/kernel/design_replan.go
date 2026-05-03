// design_replan.go — DesignGenerator 的 Replan 扩展
//
// 设计动机:
//   现有 DesignGenerator.Generate(spec) 只接受 RequirementSpec(用户原始需求),
//   不知道"哪些已完成 / 哪些被中断 / 用户中途说了什么"。Replan 需要这些上下文
//   才能产出"在原计划基础上的修正方案"而非"全新方案"。
//
//   不修改现有 DesignGenerator 接口(避免破坏 Wave 3 EasyMVP 链路),
//   独立加一个 ReplanCapableDesigner 接口 + DefaultDesignGenerator 的实现,
//   PlanOrchestrator 通过 type assertion 探测能力。
//
// 设计文档:Replan-基于现有持久化与EventBus的渐进路线.md §2.4

package kernel

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/leef-l/brain/sdk/agent"
	"github.com/leef-l/brain/sdk/llm"
)

// ReplanInput 是 GenerateWithModification 的输入上下文。
type ReplanInput struct {
	// OriginalSpec 用户最初提交的需求规格。可为 nil(纯 fallback plan 路径)。
	OriginalSpec *RequirementSpec

	// OriginalPlan 当前正在执行的 plan(被中断的)。必填。
	OriginalPlan *TaskPlan

	// Snapshot 触发 replan 时的项目状态(已完成 / 中断 / 待执行 + 项目记忆)。必填。
	Snapshot *ReplanSnapshot

	// Trigger 触发原因。必填。
	Trigger ReplanTrigger

	// UserModification 用户修改文本。Trigger == TriggerUserModification 时填。
	UserModification string

	// SubError 子任务错误信息。Trigger == TriggerSubFailure 时填。
	SubError string

	// SubHint 子任务建议。Trigger == TriggerSubFeedback 时填。
	SubHint string
}

// ReplanCapableDesigner 是带"基于现有 plan 修正方案"能力的 DesignGenerator。
//
// 实现方式:
//   - 启发式版本(LLM 不可用时):基于关键词在 OriginalPlan 上做局部修改。
//   - LLM 版本(Provider 已注入时):调 LLM 生成完整 DesignProposal。
//
// PlanOrchestrator.replan 路径:
//
//	if rd, ok := designer.(ReplanCapableDesigner); ok {
//	    proposal, err = rd.GenerateWithModification(ctx, in)
//	}
type ReplanCapableDesigner interface {
	GenerateWithModification(ctx context.Context, in ReplanInput) (*DesignProposal, error)
}

// SetLLMProvider 给 DefaultDesignGenerator 注入 LLM Provider 启用 LLM 重规划。
// 不注入时 GenerateWithModification 走启发式路径。
func (g *DefaultDesignGenerator) SetLLMProvider(p llm.Provider, model string) {
	g.llmProvider = p
	g.llmModel = model
}

// GenerateWithModification 基于现有 plan + snapshot + trigger 生成新方案。
//
// 优先级:
//  1. LLM 路径(Provider 已注入)— 调 LLM 输出完整 DesignProposal JSON
//  2. 启发式路径(LLM 不可用)— 在 OriginalPlan 上做最小局部修改
//
// 返回的 proposal 中:
//   - TaskBreakdown 包含**所有**任务(已完成的标记 BrainKind="completed_marker"
//     给后续 ToTaskPlan 识别);上层调 ToTaskPlan 后会把已完成任务设为
//     PlanTaskCompleted 并复用 Result。
//   - Description 含 rationale,供 chat UI 渲染"为什么改成这样"。
func (g *DefaultDesignGenerator) GenerateWithModification(ctx context.Context, in ReplanInput) (*DesignProposal, error) {
	if in.OriginalPlan == nil {
		return nil, fmt.Errorf("design replan: OriginalPlan is required")
	}
	if in.Snapshot == nil {
		return nil, fmt.Errorf("design replan: Snapshot is required")
	}

	// LLM 路径(优先)
	if g.llmProvider != nil {
		proposal, err := g.generateWithLLM(ctx, in)
		if err == nil {
			return proposal, nil
		}
		// LLM 失败 → 降级启发式,记录原因到 proposal.Description
		// 不直接 return error,避免单次 LLM 抖动让整个 Replan 流失败
		return g.generateHeuristic(in, fmt.Sprintf("(LLM fallback due to: %v)", err)), nil
	}

	// 启发式路径
	return g.generateHeuristic(in, ""), nil
}

// generateHeuristic 启发式重规划 — 不调 LLM,基于关键词在 OriginalPlan 上做局部修改。
//
// 策略:
//   - 已完成任务 → 保留(标记 marker)
//   - 中断任务 → 重启 instruction 加上 trigger 上下文
//   - 待执行任务 → 全部保留
//   - 不新增任务(启发式没有判断能力)
//
// 这是降级路径。生产环境应该总是注入 LLM Provider。
func (g *DefaultDesignGenerator) generateHeuristic(in ReplanInput, fallbackNote string) *DesignProposal {
	plan := in.OriginalPlan
	specID := plan.ProjectID
	title := fmt.Sprintf("%s (replan v%d)", plan.Goal, plan.Version+1)

	proposal := NewDesignProposal(specID, title)
	proposal.Description = buildReplanRationale(in) + fallbackNote
	proposal.Architecture = ArchitectureDecision{
		Pattern:   "preserve_original",
		Rationale: "启发式 replan 保留原架构,仅修改任务粒度",
	}

	// 重新构造 TaskBreakdown
	for i, st := range plan.SubTasks {
		dt := DesignTask{
			TaskID:      st.TaskID,
			Name:        st.Name,
			Description: st.Instruction,
			BrainKind:   string(st.Kind),
			Priority:    1,
			EstTurns:    st.EstimatedTurns,
		}
		if i > 0 {
			dt.DependsOn = []string{plan.SubTasks[i-1].TaskID}
		}

		switch st.Status {
		case PlanTaskCompleted:
			// 标记为已完成,ToTaskPlan 会跳过执行
			dt.BrainKind = string(st.Kind) + ":completed"
		case PlanTaskInterrupted:
			// 中断的任务追加 trigger context 到 instruction,提示 sub 注意修正
			dt.Description = appendReplanContext(st.Instruction, in)
			if in.Trigger == TriggerUserModification {
				dt.EstTurns = max2(st.EstimatedTurns, 5) // 多给点 budget
			}
		}

		proposal.AddTask(dt)
	}

	// 风险:启发式版没有 ReplanLLM 智能,标记为 medium
	proposal.AddRisk(RiskItem{
		RiskID:      "replan-heuristic",
		Severity:    "medium",
		Probability: "likely",
		Description: "启发式 replan 未充分理解用户修改意图",
		Mitigation:  "如有 LLM Provider 应配置后重启",
	})

	proposal.EstimatedBudget = g.computeBudget(proposal)
	proposal.ComputeScore()
	return proposal
}

// generateWithLLM 调 LLM 生成 DesignProposal JSON。
func (g *DefaultDesignGenerator) generateWithLLM(ctx context.Context, in ReplanInput) (*DesignProposal, error) {
	system := buildReplanSystemPrompt()
	userPrompt := buildReplanUserPrompt(in)

	req := &llm.ChatRequest{
		System: []llm.SystemBlock{{Text: system, Cache: true}},
		Messages: []llm.Message{{
			Role:    "user",
			Content: []llm.ContentBlock{{Type: "text", Text: userPrompt}},
		}},
		Model:     g.llmModel,
		MaxTokens: 4000, // DesignProposal JSON 可能较长
	}

	resp, err := g.llmProvider.Complete(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("replan llm: %w", err)
	}

	var raw string
	for _, b := range resp.Content {
		if b.Type == "text" {
			raw = b.Text
			break
		}
	}
	if raw == "" {
		return nil, fmt.Errorf("replan llm: empty response")
	}

	jsonStr := extractFirstJSON(raw)
	if jsonStr == "" {
		return nil, fmt.Errorf("replan llm: no JSON in response")
	}

	// LLM 输出格式:简化版 DesignProposal,字段名小写,只含必要项
	// 解析后填充内部 ID / 时间戳 / Score 等字段
	var llmOut struct {
		Title     string `json:"title"`
		Rationale string `json:"rationale"`
		Tasks     []struct {
			TaskID      string   `json:"task_id"`
			Name        string   `json:"name"`
			Instruction string   `json:"instruction"`
			BrainKind   string   `json:"brain_kind"`
			DependsOn   []string `json:"depends_on,omitempty"`
			EstTurns    int      `json:"est_turns"`
			Status      string   `json:"status,omitempty"` // "completed" / "modify" / "new"
		} `json:"tasks"`
	}
	if err := json.Unmarshal([]byte(jsonStr), &llmOut); err != nil {
		return nil, fmt.Errorf("replan llm: parse: %w", err)
	}

	plan := in.OriginalPlan
	specID := plan.ProjectID
	title := llmOut.Title
	if title == "" {
		title = fmt.Sprintf("%s (replan v%d)", plan.Goal, plan.Version+1)
	}

	proposal := NewDesignProposal(specID, title)
	proposal.Description = llmOut.Rationale
	proposal.Architecture = ArchitectureDecision{
		Pattern:   "llm_replan",
		Rationale: "LLM 重规划,基于用户修改 / sub 反馈调整任务",
	}

	for i, t := range llmOut.Tasks {
		brainKind := t.BrainKind
		if t.Status == "completed" {
			brainKind = brainKind + ":completed"
		}
		dt := DesignTask{
			TaskID:      t.TaskID,
			Name:        t.Name,
			Description: t.Instruction,
			BrainKind:   brainKind,
			Priority:    1,
			DependsOn:   t.DependsOn,
			EstTurns:    t.EstTurns,
		}
		if dt.EstTurns <= 0 {
			dt.EstTurns = 5
		}
		if dt.TaskID == "" {
			dt.TaskID = fmt.Sprintf("dt-replan-%d", i+1)
		}
		proposal.AddTask(dt)
	}

	proposal.EstimatedBudget = g.computeBudget(proposal)
	proposal.ComputeScore()
	return proposal, nil
}

// buildReplanSystemPrompt 构造 LLM system prompt(cache=true 复用)。
func buildReplanSystemPrompt() string {
	return `你是软件项目重规划者。原计划遇到问题(用户中途修改 / 子任务出错 / 子任务建议),
需要基于当前状态生成新方案。

要求:
1. 不重做已完成任务(在 tasks[].status 标记 "completed")
2. 必要时调整被中断任务的 instruction(尤其是用户修改触发的)
3. 可以新增任务,可以删除原计划但未启动的任务
4. 任务之间用 depends_on 表达依赖

只输出 JSON,不解释,不加 markdown 围栏:
{
  "title": "<新方案标题>",
  "rationale": "<改动理由,1-3 句>",
  "tasks": [
    {
      "task_id": "<原 ID 或 dt-replan-N>",
      "name": "<任务名>",
      "instruction": "<给 sub agent 的指令>",
      "brain_kind": "code|browser|data|verifier|...",
      "depends_on": ["前置 task_id"],
      "est_turns": 5,
      "status": "completed|modify|new"
    }
  ]
}`
}

// buildReplanUserPrompt 构造 user prompt,含 OriginalPlan / Snapshot / trigger。
func buildReplanUserPrompt(in ReplanInput) string {
	var b strings.Builder
	plan := in.OriginalPlan

	b.WriteString("【原始项目目标】\n")
	b.WriteString(plan.Goal)
	b.WriteString("\n\n")

	if len(in.Snapshot.CompletedTasks) > 0 {
		b.WriteString("【已完成的子任务】(保留,不重做)\n")
		for _, st := range in.Snapshot.CompletedTasks {
			b.WriteString(fmt.Sprintf("- %s (%s): %s\n", st.TaskID, st.Name, summarizeOutput(st.OutputSummary)))
		}
		b.WriteString("\n")
	}

	if len(in.Snapshot.InterruptedTasks) > 0 {
		b.WriteString("【被中断的子任务】(可能需要修改 instruction 或换 brain)\n")
		for _, st := range in.Snapshot.InterruptedTasks {
			b.WriteString(fmt.Sprintf("- %s (%s): %s\n", st.TaskID, st.Name, st.Instruction))
			if len(st.PartialFiles) > 0 {
				b.WriteString(fmt.Sprintf("  partial files: %s\n", strings.Join(st.PartialFiles, ", ")))
			}
			if st.AbortReason != "" {
				b.WriteString(fmt.Sprintf("  abort reason: %s\n", st.AbortReason))
			}
		}
		b.WriteString("\n")
	}

	if len(in.Snapshot.PendingTasks) > 0 {
		b.WriteString("【未启动的子任务】(可保留可删除可替换)\n")
		for _, st := range in.Snapshot.PendingTasks {
			b.WriteString(fmt.Sprintf("- %s (%s): %s\n", st.TaskID, st.Name, st.Instruction))
		}
		b.WriteString("\n")
	}

	b.WriteString("【触发原因】\n")
	switch in.Trigger {
	case TriggerUserModification:
		// UserModification 可能是单条或多条编号列表(joinModifications 输出)。
		// 单条:"改成 SQLite";多条:"1. 改成 SQLite\n2. 前端改用 Vue"。
		if strings.Contains(in.UserModification, "\n") {
			b.WriteString("用户连续提出多条独立的修改诉求(请逐条理解并综合调整方案,不是单一指令):\n")
			b.WriteString(in.UserModification)
			b.WriteString("\n")
		} else {
			b.WriteString(fmt.Sprintf("用户中途要求: \"%s\"\n", in.UserModification))
		}
	case TriggerSubFailure:
		b.WriteString(fmt.Sprintf("子任务出错: %s\n", in.SubError))
	case TriggerSubFeedback:
		b.WriteString(fmt.Sprintf("子任务建议: %s\n", in.SubHint))
	}
	b.WriteString("\n")

	if len(in.Snapshot.MemoryHints) > 0 {
		b.WriteString("【项目记忆中的相关经验】\n")
		for _, h := range in.Snapshot.MemoryHints {
			b.WriteString("- ")
			b.WriteString(h)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	b.WriteString("请输出 JSON。")
	return b.String()
}

// buildReplanRationale 给启发式 / fallback proposal 生成可读的改动理由。
func buildReplanRationale(in ReplanInput) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Replan triggered: %s\n", in.Trigger))
	switch in.Trigger {
	case TriggerUserModification:
		b.WriteString(fmt.Sprintf("User modification: %s\n", in.UserModification))
	case TriggerSubFailure:
		b.WriteString(fmt.Sprintf("Sub failure: %s\n", in.SubError))
	case TriggerSubFeedback:
		b.WriteString(fmt.Sprintf("Sub hint: %s\n", in.SubHint))
	}
	b.WriteString(fmt.Sprintf("Original plan version: %d → %d\n", in.OriginalPlan.Version, in.OriginalPlan.Version+1))
	return b.String()
}

// appendReplanContext 把 trigger 上下文追加到 sub instruction 末尾,
// 让 sub LLM 在重新执行时知道用户的修改意图。
func appendReplanContext(original string, in ReplanInput) string {
	switch in.Trigger {
	case TriggerUserModification:
		return original + "\n\n--- 用户中途修改要求 ---\n" + in.UserModification
	case TriggerSubFeedback:
		return original + "\n\n--- 上次执行的反思 ---\n" + in.SubHint
	}
	return original
}

// summarizeOutput 把 SubTask 的长输出截断成 LLM 可读的摘要(最多 200 字符)。
func summarizeOutput(s string) string {
	const maxLen = 200
	s = strings.TrimSpace(s)
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// max2 返回两数中较大者。
func max2(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// ToReplanTaskPlan 把含 ":completed" marker 的 DesignProposal 转成 TaskPlan,
// 已完成任务的 SubTask.Status = PlanTaskCompleted,从原 plan 复用 Result。
//
// PlanOrchestrator 在 replan 完成后调本方法构造新 plan(替代普通 ToTaskPlan)。
// originalPlan 用于查询已完成任务的原 Result。
func (g *DefaultDesignGenerator) ToReplanTaskPlan(proposal *DesignProposal, originalPlan *TaskPlan) *TaskPlan {
	if proposal == nil {
		return nil
	}
	plan := NewTaskPlan(proposal.SpecID, proposal.Title)
	plan.Workdir = originalPlan.Workdir
	plan.Version = originalPlan.Version + 1
	plan.Budget = PlanBudget{
		TotalTurns:  proposal.EstimatedBudget.TotalTurns,
		TotalTokens: proposal.EstimatedBudget.TotalTurns * 4000,
	}

	// 建索引:原 plan 的 task → 原 task
	originalIndex := make(map[string]*PlanSubTask)
	for i := range originalPlan.SubTasks {
		originalIndex[originalPlan.SubTasks[i].TaskID] = &originalPlan.SubTasks[i]
	}

	deps := make(map[string][]string)
	for _, dt := range proposal.TaskBreakdown {
		brainKind := dt.BrainKind
		isCompleted := strings.HasSuffix(brainKind, ":completed")
		if isCompleted {
			brainKind = strings.TrimSuffix(brainKind, ":completed")
		}

		st := PlanSubTask{
			TaskID:         dt.TaskID,
			Name:           dt.Name,
			Kind:           agent.Kind(brainKind),
			Instruction:    dt.Description,
			EstimatedTurns: dt.EstTurns,
			RetryPolicy:    RetryPolicy{MaxRetries: 2},
		}

		// B9: 设置 ReplanOrigin 标记任务来源,publishReplanCompleted 据此精确计数。
		// 优先级:
		//   isCompleted -> kept(已完成保留)
		//   原 plan 中存在同 task_id -> modified(改了 instruction / kind)
		//   不在原 plan -> added(新加任务)
		_, existsInOrig := originalIndex[dt.TaskID]
		switch {
		case isCompleted:
			st.ReplanOrigin = "kept"
			st.Status = PlanTaskCompleted
			if orig, ok := originalIndex[dt.TaskID]; ok && orig.Result != nil {
				st.Result = orig.Result
				st.StartedAt = orig.StartedAt
				now := time.Now()
				st.CompletedAt = &now
			}
		case existsInOrig:
			st.ReplanOrigin = "modified"
		default:
			st.ReplanOrigin = "added"
		}

		plan.AddSubTask(st)
		if len(dt.DependsOn) > 0 {
			deps[dt.TaskID] = dt.DependsOn
		}
	}
	plan.Dependencies = deps
	_ = plan.ComputeParallelLayers()
	return plan
}
