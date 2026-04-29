package sidecar

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/leef-l/brain/sdk/diaglog"
	brainerrors "github.com/leef-l/brain/sdk/errors"
	"github.com/leef-l/brain/sdk/executionpolicy"
	"github.com/leef-l/brain/sdk/llm"
	"github.com/leef-l/brain/sdk/loop"
	"github.com/leef-l/brain/sdk/protocol"
	"github.com/leef-l/brain/sdk/tool"
	"github.com/leef-l/brain/sdk/toolpolicy"
)

// ExecuteRequest is the payload of a brain/execute RPC call.
type ExecuteRequest struct {
	TaskID      string                         `json:"task_id"`
	Instruction string                         `json:"instruction"`
	Context     json.RawMessage                `json:"context,omitempty"`
	Subtask     *protocol.SubtaskContext       `json:"subtask,omitempty"`
	Budget      *ExecuteBudget                 `json:"budget,omitempty"`
	Execution   *executionpolicy.ExecutionSpec `json:"execution,omitempty"`
	// PipeID 用于 Workflow streaming edge 的跨进程流式传输。
	// 非空时，sidecar 会在 tool 执行后通过 brain/stream/write 将输出实时写入 host 端的 PipeRegistry。
	PipeID string `json:"pipe_id,omitempty"`
	// ExecutionID 用于端到端流式事件关联，host 生成后传给 sidecar，
	// sidecar 通过 brain/progress Notify 将实时事件回传时携带此 ID。
	ExecutionID string `json:"execution_id,omitempty"`
}

// ExecuteBudget constrains the sidecar Agent Loop.
type ExecuteBudget struct {
	MaxTurns     int     `json:"max_turns,omitempty"`
	MaxCostUSD   float64 `json:"max_cost_usd,omitempty"`
	MaxLLMCalls  int     `json:"max_llm_calls,omitempty"`
	MaxToolCalls int     `json:"max_tool_calls,omitempty"`
	MaxDurationS int     `json:"max_duration_seconds,omitempty"`
}

// ExecuteResult is the response returned after brain/execute completes.
type ExecuteResult struct {
	Status       string              `json:"status"` // "completed", "failed", "canceled"
	Summary      string              `json:"summary,omitempty"`
	Error        string              `json:"error,omitempty"`
	Turns        int                 `json:"turns"`
	Artifacts    []ArtifactRef       `json:"artifacts,omitempty"`
	Verification *VerificationResult `json:"verification,omitempty"`
	FaultSummary *FaultSummary       `json:"fault_summary,omitempty"`
}

// ArtifactRef is a minimal structured reference to a side effect or inspectable
// artifact produced during execution. The payload itself stays in the original
// tool_result; this struct only points to it so downstream adapters do not have
// to parse Summary prose.
type ArtifactRef struct {
	Kind        string `json:"kind"`
	Tool        string `json:"tool,omitempty"`
	Locator     string `json:"locator,omitempty"`
	MIMEType    string `json:"mime_type,omitempty"`
	Encoding    string `json:"encoding,omitempty"`
	Description string `json:"description,omitempty"`
}

// VerificationResult captures structured acceptance/verification checks when
// the sidecar can observe them from tool outputs.
type VerificationResult struct {
	SourceTool string              `json:"source_tool,omitempty"`
	PatternID  string              `json:"pattern_id,omitempty"`
	Passed     *bool               `json:"passed,omitempty"`
	Checks     []VerificationCheck `json:"checks,omitempty"`
}

// VerificationCheck is one named verification check.
type VerificationCheck struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Reason string `json:"reason,omitempty"`
}

// FaultSummary is a normalized, best-effort failure summary extracted from the
// last relevant tool_result or terminal turn error.
type FaultSummary struct {
	Source     string         `json:"source,omitempty"`
	Tool       string         `json:"tool,omitempty"`
	Code       string         `json:"code,omitempty"`
	Message    string         `json:"message,omitempty"`
	Route      string         `json:"route,omitempty"`
	PageHealth string         `json:"page_health,omitempty"`
	Anomalies  []FaultAnomaly `json:"anomalies,omitempty"`
}

// FaultAnomaly is one anomaly item normalized for downstream consumers.
type FaultAnomaly struct {
	Type     string `json:"type,omitempty"`
	Subtype  string `json:"subtype,omitempty"`
	Severity string `json:"severity,omitempty"`
}

// --- LLM request/response types for reverse RPC ---

// llmRequest is the payload sent to the Kernel via llm.complete.
type llmRequest struct {
	System      []systemBlock `json:"system,omitempty"`
	Messages    []message     `json:"messages"`
	Tools       []toolSchema  `json:"tools,omitempty"`
	Model       string        `json:"model,omitempty"`
	ToolChoice  string        `json:"tool_choice,omitempty"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
	StreamID    string        `json:"stream_id,omitempty"`
	ExecutionID string        `json:"execution_id,omitempty"`
}

type systemBlock struct {
	Text  string `json:"text"`
	Cache bool   `json:"cache,omitempty"`
}

type message struct {
	Role    string         `json:"role"`
	Content []contentBlock `json:"content"`
}

type contentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	ToolName  string          `json:"tool_name,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	Output    json.RawMessage `json:"output,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

type toolSchema struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// llmResponse is the payload received from the Kernel via llm.complete.
type llmResponse struct {
	ID         string         `json:"id"`
	Model      string         `json:"model"`
	StopReason string         `json:"stop_reason"`
	Content    []contentBlock `json:"content"`
	Usage      *llmUsageWire  `json:"usage,omitempty"`
}

// llmUsageWire 是反向 RPC 里可选的 Usage 字段（由 Kernel 侧 llm.complete 填充）。
type llmUsageWire struct {
	InputTokens         int     `json:"input_tokens,omitempty"`
	OutputTokens        int     `json:"output_tokens,omitempty"`
	CacheReadTokens     int     `json:"cache_read_tokens,omitempty"`
	CacheCreationTokens int     `json:"cache_creation_tokens,omitempty"`
	CostUSD             float64 `json:"cost_usd,omitempty"`
}

// RunAgentLoop 是 sidecar 的 Agent Loop 入口（向后兼容签名）。
func RunAgentLoop(ctx context.Context, caller KernelCaller, registry tool.Registry,
	systemPrompt string, instruction string, maxTurns int) *ExecuteResult {
	return RunAgentLoopWithContext(ctx, caller, registry, systemPrompt, instruction, maxTurns, nil)
}

// RunAgentLoopWithContext 向后兼容签名，内部委托给 RunAgentLoopFull。
func RunAgentLoopWithContext(ctx context.Context, caller KernelCaller, registry tool.Registry,
	systemPrompt string, instruction string, maxTurns int, extraContext json.RawMessage) *ExecuteResult {
	b := &ExecuteBudget{MaxTurns: maxTurns}
	return RunAgentLoopFull(ctx, caller, registry, systemPrompt, instruction, b, extraContext, nil, "")
}

// RunAgentLoopFull 是完整版 Agent Loop 入口。接受完整 ExecuteBudget，
// 接入 MessageCompressor 实现上下文自动压缩。
// observer 为 nil 时使用默认的 stderrToolObserver。
// executionID 用于端到端流式事件关联；为空时不启用流式输出。
func RunAgentLoopFull(ctx context.Context, caller KernelCaller, registry tool.Registry,
	systemPrompt string, instruction string, eb *ExecuteBudget, extraContext json.RawMessage, observer loop.ToolObserver, executionID string) *ExecuteResult {

	maxTurns := 10
	if eb != nil && eb.MaxTurns > 0 {
		maxTurns = eb.MaxTurns
	}

	provider := NewKernelLLMProvider(caller, "kernel", executionID)

	const defaultTokenBudget = 100_000

	if observer == nil {
		observer = StderrToolObserver{}
	}
	if executionID != "" {
		observer = &progressToolObserver{base: observer, executionID: executionID}
	}
	runner := &loop.Runner{
		Provider:     provider,
		ToolRegistry: registry,
		Sanitizer:    loop.NewMemSanitizer(),
		LoopDetector: loop.NewMemLoopDetector(),
		CacheBuilder: loop.NewMemCacheBuilder(),
		ToolObserver: observer,
		TokenBudget:       defaultTokenBudget,
		MessageCompressor: loop.DefaultMessageCompressor,
	}

	brainKindEarly := brainKindFromSystem(systemPrompt)
	if brainKindEarly == "browser" {
		runner.PreTurnStateHook = newBrowserStageHook(registry)
	}

	budgetMaxCost := 5.0
	budgetMaxLLM := maxTurns * 2
	budgetMaxTool := maxTurns * 4
	budgetMaxDur := 10 * time.Minute
	if eb != nil {
		if eb.MaxCostUSD > 0 {
			budgetMaxCost = eb.MaxCostUSD
		}
		if eb.MaxLLMCalls > 0 {
			budgetMaxLLM = eb.MaxLLMCalls
		}
		if eb.MaxToolCalls > 0 {
			budgetMaxTool = eb.MaxToolCalls
		}
		if eb.MaxDurationS > 0 {
			budgetMaxDur = time.Duration(eb.MaxDurationS) * time.Second
		}
	}

	budget := loop.Budget{
		MaxTurns:     maxTurns,
		MaxCostUSD:   budgetMaxCost,
		MaxLLMCalls:  budgetMaxLLM,
		MaxToolCalls: budgetMaxTool,
		MaxDuration:  budgetMaxDur,
	}
	runID := fmt.Sprintf("sidecar-%d", time.Now().UnixNano())
	run := loop.NewRun(runID, "sidecar", budget)

	// Task #13: 把本次 run 绑到进程内的 InteractionRecorder。browser 工具装饰器
	// 在每次 Execute 后会追加一条 RecordedAction;loop 结束时通过已注入的
	// sink(通常是 LearningEngine)把序列持久化到 LearningStore,供
	// ui_pattern_learn 聚类。没注入 sink 时该机制整体 no-op。
	brainKind := brainKindEarly
	tool.BindRecorder(ctx, runID, brainKind, instruction)

	ensureSidecarOutcomeSink()

	// L1+L2: 把 systemPrompt 分两层。仅一层时简化为单块 SystemBlock 且 cache=true，
	// 让 Prompt Cache 在每一轮自动复用。
	system := []llm.SystemBlock{{Text: systemPrompt, Cache: true}}

	// 构造 tool schemas（传给 LLM 看）。
	var tools []llm.ToolSchema
	for _, t := range registry.List() {
		s := t.Schema()
		tools = append(tools, llm.ToolSchema{
			Name:        s.Name,
			Description: s.Description,
			InputSchema: s.InputSchema,
		})
	}

	// 初始 messages：若有 extraContext（中央装配的上下文），插到 instruction 之前。
	var messages []llm.Message
	if len(extraContext) > 0 && json.Valid(extraContext) {
		contextText := summarizeContext(extraContext)
		if contextText != "" {
			messages = append(messages, llm.Message{
				Role: "user",
				Content: []llm.ContentBlock{
					{Type: "text", Text: "Context provided by coordinator:\n" + contextText},
				},
			})
			messages = append(messages, llm.Message{
				Role: "assistant",
				Content: []llm.ContentBlock{
					{Type: "text", Text: "Acknowledged. I will use this context when executing the task."},
				},
			})
		}
	}
	messages = append(messages, llm.Message{
		Role:    "user",
		Content: []llm.ContentBlock{{Type: "text", Text: instruction}},
	})

	opts := loop.RunOptions{
		System:     system,
		Tools:      tools,
		ToolChoice: "auto",
		MaxTokens:  4096,
		Stream:     executionID != "",
	}

	if executionID != "" {
		runner.StreamConsumer = newExecutionStreamConsumer(executionID)
	}

	result, err := runner.Execute(ctx, run, messages, opts)
	if err != nil {
		diaglog.Logf("tool", "run_id=%s runner_execute_failed err=%v", runID, err)
		_ = tool.FinishRecorder(ctx, "failure")
		return &ExecuteResult{
			Status: "failed",
			Error:  fmt.Sprintf("runner.Execute: %v", err),
			Turns:  0,
			FaultSummary: &FaultSummary{
				Source:  "runner",
				Message: fmt.Sprintf("runner.Execute: %v", err),
			},
		}
	}

	// 从 FinalMessages 提取最后一条 assistant 文本作为 summary。
	summary := extractLastAssistantText(result.FinalMessages)

	status := "completed"
	outcome := "success"
	switch result.Run.State {
	case loop.StateFailed:
		status = "failed"
		outcome = "failure"
	case loop.StateCanceled:
		status = "canceled"
		outcome = "failure"
	}
	_ = tool.FinishRecorder(ctx, outcome)

	out := &ExecuteResult{
		Status:  status,
		Summary: summary,
		Turns:   result.Run.Budget.UsedTurns,
	}
	// 如果最后一个 Turn 带错误，回填到 Error 字段。
	if n := len(result.Turns); n > 0 && result.Turns[n-1] != nil && result.Turns[n-1].Error != nil {
		out.Error = result.Turns[n-1].Error.Error()
	}
	applyStructuredOutputs(out, result)
	if out.Error == "" && out.Status != "completed" {
		switch {
		case out.FaultSummary != nil && strings.TrimSpace(out.FaultSummary.Message) != "":
			out.Error = strings.TrimSpace(out.FaultSummary.Message)
		case strings.TrimSpace(out.Summary) != "":
			out.Error = strings.TrimSpace(out.Summary)
		default:
			out.Error = "sidecar execution failed"
		}
	}
	diaglog.Logf("tool", "run_id=%s status=%s turns=%d error=%s", runID, out.Status, out.Turns, out.Error)
	return out
}

// brainKindFromSystem 尝试从 systemPrompt 里识别本次 run 属于哪个基础大脑。
// 返回 "browser"/"code"/"data"/"verifier"/"fault"/"central",否则 ""。
// 用于 InteractionSequence.BrainKind 字段。
func brainKindFromSystem(systemPrompt string) string {
	lp := strings.ToLower(systemPrompt)
	switch {
	case strings.Contains(lp, "browser brain"), strings.Contains(lp, "browser agent"),
		strings.Contains(lp, "browser.snapshot"), strings.Contains(lp, "browser.open"):
		return "browser"
	case strings.Contains(lp, "code brain"), strings.Contains(lp, "code agent"):
		return "code"
	case strings.Contains(lp, "data brain"):
		return "data"
	case strings.Contains(lp, "verifier brain"):
		return "verifier"
	case strings.Contains(lp, "fault brain"):
		return "fault"
	case strings.Contains(lp, "central brain"):
		return "central"
	}
	return ""
}

// summarizeContext 把 ExecuteRequest.Context 的 JSON 简化为纯文本。
// 支持几种常见形态：
//   - string（直接返回）
//   - {"text": "..."} / {"summary": "..."}（提取该字段）
//   - 其它 → JSON 文本化
func summarizeContext(raw json.RawMessage) string {
	// 尝试直接解析为字符串
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		return asString
	}
	// 尝试解析为 map，找 "text" 或 "summary" 字段
	var asMap map[string]interface{}
	if err := json.Unmarshal(raw, &asMap); err == nil {
		if s, ok := asMap["text"].(string); ok && s != "" {
			return s
		}
		if s, ok := asMap["summary"].(string); ok && s != "" {
			return s
		}
	}
	// 退化：直接用 JSON 文本
	return string(raw)
}

func extractLastAssistantText(messages []llm.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != "assistant" {
			continue
		}
		out := ""
		for _, b := range messages[i].Content {
			if b.Type == "text" && b.Text != "" {
				if out != "" {
					out += "\n"
				}
				out += b.Text
			}
		}
		return out
	}
	return ""
}

type toolResultSnapshot struct {
	ToolUseID    string
	ToolName     string
	ToolInput    json.RawMessage
	Output       json.RawMessage
	IsError      bool
	CollectCode  string
	CollectError string
}

func applyStructuredOutputs(out *ExecuteResult, runResult *loop.RunResult) {
	if out == nil || runResult == nil {
		return
	}
	toolResults := collectToolResults(runResult.FinalMessages)
	if artifacts := collectArtifacts(toolResults); len(artifacts) > 0 {
		out.Artifacts = artifacts
	}
	if verification := collectVerification(toolResults); verification != nil {
		out.Verification = verification
	}
	if fault := collectFaultSummary(toolResults, runResult.Turns); fault != nil {
		out.FaultSummary = fault
	}
}

func collectToolResults(messages []llm.Message) []toolResultSnapshot {
	toolNames := map[string]string{}
	toolInputs := map[string]json.RawMessage{}
	var out []toolResultSnapshot
	for _, msg := range messages {
		for _, block := range msg.Content {
			switch block.Type {
			case "tool_use":
				switch {
				case block.ToolUseID == "":
					continue
				case block.ToolName == "":
					out = append(out, toolResultSnapshot{
						ToolUseID:    block.ToolUseID,
						CollectCode:  "empty_tool_name",
						CollectError: "tool_use missing tool_name",
					})
				case toolNames[block.ToolUseID] != "":
					out = append(out, toolResultSnapshot{
						ToolUseID:    block.ToolUseID,
						ToolName:     block.ToolName,
						ToolInput:    append(json.RawMessage(nil), block.Input...),
						CollectCode:  "duplicate_tool_use_id",
						CollectError: "duplicate tool_use_id: " + block.ToolUseID,
					})
				default:
					toolNames[block.ToolUseID] = block.ToolName
					if len(block.Input) > 0 {
						toolInputs[block.ToolUseID] = append(json.RawMessage(nil), block.Input...)
					}
				}
			case "tool_result":
				toolName := toolNames[block.ToolUseID]
				snapshot := toolResultSnapshot{
					ToolUseID: block.ToolUseID,
					ToolName:  toolName,
					Output:    append(json.RawMessage(nil), block.Output...),
					IsError:   block.IsError,
				}
				if len(toolInputs[block.ToolUseID]) > 0 {
					snapshot.ToolInput = append(json.RawMessage(nil), toolInputs[block.ToolUseID]...)
				}
				switch {
				case block.ToolUseID == "":
					snapshot.CollectCode = "orphan_tool_result"
					snapshot.CollectError = "tool_result missing tool_use_id"
				case toolName == "":
					snapshot.CollectCode = "orphan_tool_result"
					snapshot.CollectError = "tool_result references unknown tool_use_id: " + block.ToolUseID
				case len(block.Output) == 0:
					snapshot.CollectCode = "empty_tool_result"
					snapshot.CollectError = "tool_result missing output payload"
				}
				out = append(out, snapshot)
			}
		}
	}
	return out
}

func collectArtifacts(results []toolResultSnapshot) []ArtifactRef {
	var artifacts []ArtifactRef
	for _, tr := range results {
		obj := decodeJSONObject(tr.Output)
		input := decodeJSONObject(tr.ToolInput)
		switch tr.ToolName {
		case "browser.open":
			if path := stringValue(obj["target_id"]); path != "" {
				artifacts = append(artifacts, ArtifactRef{
					Kind:        "page_ref",
					Tool:        tr.ToolName,
					Locator:     path,
					Description: "opened page target reference returned by browser.open",
				})
			}
		case "browser.navigate":
			if stringValue(input["action"]) == "list_tabs" {
				if _, ok := decodeJSONArray(tr.Output); ok {
					artifacts = append(artifacts, ArtifactRef{
						Kind:        "tab_catalog",
						Tool:        tr.ToolName,
						Locator:     "tabs",
						Description: "browser tab inventory returned by browser.navigate list_tabs",
					})
				}
			}
		case "browser.pattern_match":
			if _, ok := obj["matches"].([]interface{}); ok {
				artifacts = append(artifacts, ArtifactRef{
					Kind:        "pattern_matches",
					Tool:        tr.ToolName,
					Locator:     "matches",
					Description: "pattern match candidates returned by browser.pattern_match",
				})
			}
		case "browser.pattern_list":
			if _, ok := obj["patterns"].([]interface{}); ok {
				artifacts = append(artifacts, ArtifactRef{
					Kind:        "pattern_catalog",
					Tool:        tr.ToolName,
					Locator:     "patterns",
					Description: "pattern inventory returned by browser.pattern_list",
				})
			}
		case "browser.request_anomaly_fix":
			if _, ok := obj["recovery"].([]interface{}); ok {
				artifacts = append(artifacts, ArtifactRef{
					Kind:        "recovery_plan",
					Tool:        tr.ToolName,
					Locator:     "recovery",
					Description: "structured anomaly recovery plan returned by browser.request_anomaly_fix",
				})
			}
		case "browser.changes":
			if _, ok := obj["records"].([]interface{}); ok {
				artifacts = append(artifacts, ArtifactRef{
					Kind:        "dom_changes",
					Tool:        tr.ToolName,
					Locator:     "records",
					Description: "mutation observer change records returned by browser.changes",
				})
			}
		case "browser.snapshot":
			if _, ok := obj["elements"].([]interface{}); ok {
				artifacts = append(artifacts, ArtifactRef{
					Kind:        "snapshot",
					Tool:        tr.ToolName,
					Locator:     "elements",
					Description: "interactive page snapshot returned by browser.snapshot",
				})
			}
		case "browser.iframe":
			action := stringValue(obj["action"])
			if action == "list" {
				if _, ok := obj["frames"].([]interface{}); ok {
					artifacts = append(artifacts, ArtifactRef{
						Kind:        "frame_catalog",
						Tool:        tr.ToolName,
						Locator:     "frames",
						Description: "iframe inventory returned by browser.iframe list",
					})
				}
			}
			if action == "snapshot" {
				if _, ok := obj["elements"].([]interface{}); ok {
					artifacts = append(artifacts, ArtifactRef{
						Kind:        "frame_snapshot",
						Tool:        tr.ToolName,
						Locator:     "elements",
						Description: "inline structural frame snapshot returned by browser.iframe",
					})
				}
			}
		case "browser.understand":
			if _, ok := obj["elements"].([]interface{}); ok {
				artifacts = append(artifacts, ArtifactRef{
					Kind:        "semantic_annotations",
					Tool:        tr.ToolName,
					Locator:     "elements",
					Description: "semantic element annotations returned by browser.understand",
				})
			}
		case "browser.check_anomaly", "browser.check_anomaly_v2":
			if obj["page_health"] != nil || obj["anomalies"] != nil {
				artifacts = append(artifacts, ArtifactRef{
					Kind:        "anomaly_report",
					Tool:        tr.ToolName,
					Locator:     "anomalies",
					Description: "structured anomaly report returned by browser anomaly checker",
				})
			}
		case "browser.sitemap":
			if _, ok := obj["pages"].([]interface{}); ok {
				artifacts = append(artifacts, ArtifactRef{
					Kind:        "sitemap",
					Tool:        tr.ToolName,
					Locator:     "pages",
					Description: "site crawl result returned by browser.sitemap",
				})
			}
			if _, ok := obj["route_patterns"].([]interface{}); ok {
				artifacts = append(artifacts, ArtifactRef{
					Kind:        "route_patterns",
					Tool:        tr.ToolName,
					Locator:     "route_patterns",
					Description: "mined route patterns returned by browser.sitemap",
				})
			}
		case "browser.frame":
			if stringValue(obj["action"]) == "snapshot" {
				if _, ok := obj["elements"].([]interface{}); ok {
					artifacts = append(artifacts, ArtifactRef{
						Kind:        "frame_snapshot",
						Tool:        tr.ToolName,
						Locator:     "elements",
						Description: "inline structural frame snapshot returned by browser.frame",
					})
				}
			}
		case "browser.network":
			action := stringValue(obj["action"])
			if action == "list" {
				if _, ok := obj["entries"].([]interface{}); ok {
					artifacts = append(artifacts, ArtifactRef{
						Kind:        "network_trace",
						Tool:        tr.ToolName,
						Locator:     "entries",
						Description: "observed network entries returned by browser.network",
					})
				}
			}
			if action == "get" || action == "wait_for" {
				if obj["entry"] != nil {
					artifacts = append(artifacts, ArtifactRef{
						Kind:        "network_entry",
						Tool:        tr.ToolName,
						Locator:     "entry",
						Description: fmt.Sprintf("matched network entry returned by browser.network (%s)", action),
					})
				}
			}
			if (action == "get" || action == "wait_for") && obj["body"] != nil {
				artifacts = append(artifacts, ArtifactRef{
					Kind:        "response_body",
					Tool:        tr.ToolName,
					Locator:     "body",
					MIMEType:    stringValue(obj["body_mime"]),
					Description: fmt.Sprintf("inline response body returned by browser.network (%s)", action),
				})
			}
		case "browser.screenshot":
			if data, _ := obj["data"].(string); data != "" {
				artifacts = append(artifacts, ArtifactRef{
					Kind:        "screenshot",
					Tool:        tr.ToolName,
					Locator:     "data",
					MIMEType:    mimeTypeFromFormat(stringValue(obj["format"])),
					Encoding:    stringValue(obj["encoding"]),
					Description: "inline screenshot returned by browser.screenshot",
				})
			}
		case "browser.visual_inspect":
			shot, _ := obj["screenshot"].(map[string]interface{})
			if data, _ := shot["data"].(string); data != "" {
				artifacts = append(artifacts, ArtifactRef{
					Kind:        "screenshot",
					Tool:        tr.ToolName,
					Locator:     "screenshot.data",
					MIMEType:    mimeTypeFromFormat(stringValue(shot["format"])),
					Encoding:    stringValue(shot["encoding"]),
					Description: "inline screenshot bundled by browser.visual_inspect",
				})
			}
			if _, ok := obj["snapshot"]; ok {
				artifacts = append(artifacts, ArtifactRef{
					Kind:        "snapshot",
					Tool:        tr.ToolName,
					Locator:     "snapshot",
					Description: "bundled structural snapshot returned by browser.visual_inspect",
				})
			}
			if _, ok := obj["semantics"].([]interface{}); ok {
				artifacts = append(artifacts, ArtifactRef{
					Kind:        "semantic_annotations",
					Tool:        tr.ToolName,
					Locator:     "semantics",
					Description: "cached semantic annotations bundled by browser.visual_inspect",
				})
			}
		case "browser.downloads":
			rawFiles, _ := obj["files"].([]interface{})
			for _, item := range rawFiles {
				file, _ := item.(map[string]interface{})
				path := stringValue(file["path"])
				if path == "" {
					continue
				}
				desc := "downloaded file reported by browser.downloads"
				if name := stringValue(file["name"]); name != "" {
					desc = fmt.Sprintf("downloaded file %s reported by browser.downloads", name)
				}
				artifacts = append(artifacts, ArtifactRef{
					Kind:        "download",
					Tool:        tr.ToolName,
					Locator:     path,
					Description: desc,
				})
			}
		case "browser.storage":
			if stringValue(obj["action"]) == "export" {
				if written := stringValue(obj["written"]); written != "" {
					artifacts = append(artifacts, ArtifactRef{
						Kind:        "storage_state",
						Tool:        tr.ToolName,
						Locator:     written,
						MIMEType:    "application/json",
						Description: "browser storage export written by browser.storage",
					})
				} else if _, ok := obj["state"].(map[string]interface{}); ok {
					artifacts = append(artifacts, ArtifactRef{
						Kind:        "storage_state",
						Tool:        tr.ToolName,
						Locator:     "state",
						MIMEType:    "application/json",
						Description: "inline browser storage export returned by browser.storage",
					})
				}
			}
		case "browser.upload_file":
			rawFiles, _ := obj["files"].([]interface{})
			for _, item := range rawFiles {
				path, _ := item.(string)
				if path == "" {
					continue
				}
				artifacts = append(artifacts, ArtifactRef{
					Kind:        "upload_file",
					Tool:        tr.ToolName,
					Locator:     path,
					Description: "file path uploaded through browser.upload_file",
				})
			}
		case "browser.fill_form":
			if _, ok := obj["results"].([]interface{}); ok {
				artifacts = append(artifacts, ArtifactRef{
					Kind:        "form_fill_results",
					Tool:        tr.ToolName,
					Locator:     "results",
					Description: "per-field form fill results returned by browser.fill_form",
				})
			}
		case "verifier.check_output":
			if diff := stringValue(obj["diff"]); diff != "" {
				artifacts = append(artifacts, ArtifactRef{
					Kind:        "diff",
					Tool:        tr.ToolName,
					Locator:     "diff",
					MIMEType:    "text/plain",
					Description: "inline diff returned by verifier.check_output",
				})
			}
		case "verifier.browser_action":
			action := stringValue(input["action"])
			params, _ := input["params"].(map[string]interface{})
			switch action {
			case "open":
				if path := stringValue(obj["target_id"]); path != "" {
					artifacts = append(artifacts, ArtifactRef{
						Kind:        "page_ref",
						Tool:        tr.ToolName,
						Locator:     path,
						Description: "proxied page target reference returned by verifier.browser_action open",
					})
				}
			case "navigate":
				if stringValue(params["action"]) == "list_tabs" {
					if _, ok := decodeJSONArray(tr.Output); ok {
						artifacts = append(artifacts, ArtifactRef{
							Kind:        "tab_catalog",
							Tool:        tr.ToolName,
							Locator:     "tabs",
							Description: "proxied browser tab inventory returned by verifier.browser_action navigate",
						})
					}
				}
			case "screenshot":
				if data, _ := obj["data"].(string); data != "" {
					artifacts = append(artifacts, ArtifactRef{
						Kind:        "screenshot",
						Tool:        tr.ToolName,
						Locator:     "data",
						MIMEType:    mimeTypeFromFormat(stringValue(obj["format"])),
						Encoding:    stringValue(obj["encoding"]),
						Description: "proxied screenshot returned by verifier.browser_action",
					})
				}
			case "upload_file":
				rawFiles, _ := obj["files"].([]interface{})
				for _, item := range rawFiles {
					path, _ := item.(string)
					if path == "" {
						continue
					}
					artifacts = append(artifacts, ArtifactRef{
						Kind:        "upload_file",
						Tool:        tr.ToolName,
						Locator:     path,
						Description: "file path uploaded through verifier.browser_action",
					})
				}
			}
		default:
			switch {
			case strings.HasSuffix(tr.ToolName, ".list_files"):
				if _, ok := obj["paths"].([]interface{}); ok {
					artifacts = append(artifacts, ArtifactRef{
						Kind:        "file_list",
						Tool:        tr.ToolName,
						Locator:     "paths",
						Description: "file path list returned by list_files tool",
					})
				}
			case strings.HasSuffix(tr.ToolName, ".search"):
				if _, ok := obj["matches"].([]interface{}); ok {
					artifacts = append(artifacts, ArtifactRef{
						Kind:        "search_matches",
						Tool:        tr.ToolName,
						Locator:     "matches",
						Description: "structured search matches returned by search tool",
					})
				}
			case strings.HasSuffix(tr.ToolName, ".read_file"):
				if path := stringValue(obj["path"]); path != "" {
					artifacts = append(artifacts, ArtifactRef{
						Kind:        "file_content",
						Tool:        tr.ToolName,
						Locator:     path,
						Description: "file content read by read_file tool",
					})
				}
			case strings.HasSuffix(tr.ToolName, ".write_file"):
				if path := stringValue(obj["path"]); path != "" {
					artifacts = append(artifacts, ArtifactRef{
						Kind:        "file",
						Tool:        tr.ToolName,
						Locator:     path,
						Description: "file written by write_file tool",
					})
				}
			case strings.HasSuffix(tr.ToolName, ".edit_file"):
				if path := stringValue(obj["path"]); path != "" {
					artifacts = append(artifacts, ArtifactRef{
						Kind:        "file",
						Tool:        tr.ToolName,
						Locator:     path,
						Description: "file edited by edit_file tool",
					})
				}
			}
		}
	}
	return artifacts
}

func collectVerification(results []toolResultSnapshot) *VerificationResult {
	verification := &VerificationResult{}
	sourceSet := map[string]bool{}
	var sourceOrder []string
	overall := true
	seenCheck := false

	for _, tr := range results {
		var (
			partial *VerificationResult
			ok      bool
		)
		switch tr.ToolName {
		case "browser.pattern_exec":
			partial, ok = verificationFromPatternExec(tr.Output)
		case "browser.fill_form":
			partial, ok = verificationFromFillForm(tr.Output)
		case "browser.wait":
			partial, ok = verificationFromBrowserWait(tr.Output)
		case "browser.select":
			partial, ok = verificationFromBrowserSelect(tr.Output)
		case "browser.upload_file":
			partial, ok = verificationFromUploadFile(tr.Output)
		case "wait.network_idle":
			partial, ok = verificationFromWaitNetworkIdle(tr.Output)
		case "browser.check_anomaly", "browser.check_anomaly_v2":
			partial, ok = verificationFromAnomalyCheck(tr.ToolName, tr.Output)
		case "verifier.check_output":
			partial, ok = verificationFromCheckOutput(tr.Output)
		case "verifier.run_tests":
			partial, ok = verificationFromRunTests(tr.Output)
		case "verifier.browser_action":
			partial, ok = verificationFromVerifierBrowserAction(tr.ToolInput, tr.Output)
		default:
			continue
		}
		if !ok || partial == nil {
			continue
		}
		if partial.PatternID != "" {
			verification.PatternID = partial.PatternID
		}
		if partial.SourceTool != "" && !sourceSet[partial.SourceTool] {
			sourceSet[partial.SourceTool] = true
			sourceOrder = append(sourceOrder, partial.SourceTool)
		}
		for _, check := range partial.Checks {
			verification.Checks = append(verification.Checks, check)
			seenCheck = true
			if !check.Passed {
				overall = false
			}
		}
		if len(partial.Checks) == 0 && partial.Passed != nil {
			seenCheck = true
			if !*partial.Passed {
				overall = false
			}
		}
	}
	if len(sourceOrder) == 0 {
		return nil
	}
	verification.SourceTool = strings.Join(sourceOrder, ",")
	if seenCheck {
		verification.Passed = boolPtr(overall)
	}
	return verification
}

func verificationFromPatternExec(raw json.RawMessage) (*VerificationResult, bool) {
	var payload struct {
		PatternID      string `json:"pattern_id"`
		Success        bool   `json:"success"`
		PostConditions []struct {
			Type   string `json:"type"`
			OK     bool   `json:"ok"`
			Reason string `json:"reason"`
		} `json:"post_conditions"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, false
	}
	verification := &VerificationResult{
		SourceTool: "browser.pattern_exec",
		PatternID:  payload.PatternID,
	}
	if len(payload.PostConditions) > 0 {
		verification.Checks = make([]VerificationCheck, 0, len(payload.PostConditions))
		for _, pc := range payload.PostConditions {
			verification.Checks = append(verification.Checks, VerificationCheck{
				Name:   pc.Type,
				Passed: pc.OK,
				Reason: pc.Reason,
			})
		}
		return verification, true
	}
	verification.Passed = boolPtr(payload.Success)
	return verification, true
}

func verificationFromFillForm(raw json.RawMessage) (*VerificationResult, bool) {
	var payload struct {
		Filled  *int     `json:"filled"`
		Missing []string `json:"missing"`
		Results []struct {
			Key    string `json:"key"`
			Status string `json:"status"`
			Error  string `json:"error"`
		} `json:"results"`
		Submitted bool `json:"submitted"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, false
	}
	if payload.Filled == nil && payload.Missing == nil && len(payload.Results) == 0 && !payload.Submitted {
		return nil, false
	}

	reason := fmt.Sprintf("filled=%d", derefInt(payload.Filled))
	if len(payload.Missing) > 0 {
		reason += "; missing=" + strings.Join(payload.Missing, ",")
	}

	verification := &VerificationResult{
		SourceTool: "browser.fill_form",
		Checks: []VerificationCheck{
			{
				Name:   "browser.fill_form.fields_resolved",
				Passed: len(payload.Missing) == 0,
				Reason: reason,
			},
		},
	}
	for _, result := range payload.Results {
		status := strings.TrimSpace(result.Status)
		if result.Key == "" || status == "" {
			continue
		}
		reason := "status=" + status
		if result.Error != "" {
			reason += "; " + result.Error
		}
		verification.Checks = append(verification.Checks, VerificationCheck{
			Name:   "browser.fill_form.field:" + result.Key,
			Passed: status == "ok",
			Reason: reason,
		})
	}
	if payload.Submitted {
		verification.Checks = append(verification.Checks, VerificationCheck{
			Name:   "browser.fill_form.submitted",
			Passed: true,
			Reason: "submit=true",
		})
	}
	return verification, true
}

func verificationFromWaitNetworkIdle(raw json.RawMessage) (*VerificationResult, bool) {
	var payload struct {
		Status *string `json:"status"`
		Forced *bool   `json:"forced"`
		IdleMS *int    `json:"idle_ms"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, false
	}
	if payload.Status == nil || *payload.Status == "" {
		return nil, false
	}

	passed := strings.EqualFold(*payload.Status, "idle")
	reasonParts := []string{"status=" + *payload.Status}
	if payload.IdleMS != nil {
		reasonParts = append(reasonParts, fmt.Sprintf("idle_ms=%d", *payload.IdleMS))
	}
	if payload.Forced != nil {
		reasonParts = append(reasonParts, fmt.Sprintf("forced=%t", *payload.Forced))
	}
	return &VerificationResult{
		SourceTool: "wait.network_idle",
		Checks: []VerificationCheck{
			{
				Name:   "wait.network_idle.status",
				Passed: passed,
				Reason: strings.Join(reasonParts, "; "),
			},
		},
	}, true
}

func verificationFromBrowserWait(raw json.RawMessage) (*VerificationResult, bool) {
	var payload struct {
		Status    *string `json:"status"`
		Condition string  `json:"condition"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, false
	}
	if payload.Status == nil || *payload.Status == "" || payload.Condition == "" {
		return nil, false
	}

	return &VerificationResult{
		SourceTool: "browser.wait",
		Checks: []VerificationCheck{
			{
				Name:   "browser.wait.condition",
				Passed: strings.EqualFold(*payload.Status, "ok"),
				Reason: fmt.Sprintf("status=%s; condition=%s", *payload.Status, payload.Condition),
			},
		},
	}, true
}

func verificationFromBrowserSelect(raw json.RawMessage) (*VerificationResult, bool) {
	var payload struct {
		Status *string `json:"status"`
		Value  string  `json:"value"`
		Text   string  `json:"text"`
		Index  *int    `json:"index"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, false
	}
	if payload.Status == nil || *payload.Status == "" {
		return nil, false
	}

	reasonParts := []string{"status=" + *payload.Status}
	if payload.Value != "" {
		reasonParts = append(reasonParts, "value="+payload.Value)
	}
	if payload.Text != "" {
		reasonParts = append(reasonParts, "text="+payload.Text)
	}
	if payload.Index != nil {
		reasonParts = append(reasonParts, fmt.Sprintf("index=%d", *payload.Index))
	}

	return &VerificationResult{
		SourceTool: "browser.select",
		Checks: []VerificationCheck{
			{
				Name:   "browser.select.applied",
				Passed: strings.EqualFold(*payload.Status, "ok"),
				Reason: strings.Join(reasonParts, "; "),
			},
		},
	}, true
}

func verificationFromUploadFile(raw json.RawMessage) (*VerificationResult, bool) {
	var payload struct {
		Status *string  `json:"status"`
		Files  []string `json:"files"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, false
	}
	if payload.Status == nil || *payload.Status == "" {
		return nil, false
	}

	statusPassed := strings.EqualFold(*payload.Status, "ok")
	checks := []VerificationCheck{
		{
			Name:   "browser.upload_file.status",
			Passed: statusPassed,
			Reason: fmt.Sprintf("status=%s; files=%d", *payload.Status, len(payload.Files)),
		},
	}
	if len(payload.Files) > 0 {
		checks = append(checks, VerificationCheck{
			Name:   "browser.upload_file.files_attached",
			Passed: true,
			Reason: strings.Join(payload.Files, ","),
		})
	}

	return &VerificationResult{
		SourceTool: "browser.upload_file",
		Checks:     checks,
	}, true
}

func verificationFromAnomalyCheck(toolName string, raw json.RawMessage) (*VerificationResult, bool) {
	obj := decodeJSONObject(raw)
	if obj == nil {
		return nil, false
	}
	container := anomalyContainerObject(obj)
	if container == nil {
		return nil, false
	}
	containerRaw, err := json.Marshal(container)
	if err != nil {
		return nil, false
	}

	var payload struct {
		PageHealth string `json:"page_health"`
		Anomalies  []struct {
			Type        string `json:"type"`
			Subtype     string `json:"subtype"`
			Severity    string `json:"severity"`
			Description string `json:"description"`
		} `json:"anomalies"`
	}
	if err := json.Unmarshal(containerRaw, &payload); err != nil {
		return nil, false
	}
	if payload.PageHealth == "" && len(payload.Anomalies) == 0 {
		return nil, false
	}
	verification := &VerificationResult{
		SourceTool: toolName,
	}
	if payload.PageHealth != "" {
		passed := strings.EqualFold(payload.PageHealth, "healthy")
		reason := payload.PageHealth
		if len(payload.Anomalies) > 0 {
			reason = fmt.Sprintf("%s (%d anomalies)", payload.PageHealth, len(payload.Anomalies))
		}
		verification.Checks = append(verification.Checks, VerificationCheck{
			Name:   toolName + ".page_health",
			Passed: passed,
			Reason: reason,
		})
	}
	for _, a := range payload.Anomalies {
		name := toolName + ".anomaly:" + a.Type
		if a.Subtype != "" {
			name += "/" + a.Subtype
		}
		reasonParts := make([]string, 0, 2)
		if a.Severity != "" {
			reasonParts = append(reasonParts, "severity="+a.Severity)
		}
		if a.Description != "" {
			reasonParts = append(reasonParts, a.Description)
		}
		verification.Checks = append(verification.Checks, VerificationCheck{
			Name:   name,
			Passed: false,
			Reason: strings.Join(reasonParts, "; "),
		})
	}
	return verification, true
}

func verificationFromCheckOutput(raw json.RawMessage) (*VerificationResult, bool) {
	var payload struct {
		Match *bool  `json:"match"`
		Mode  string `json:"mode"`
		Diff  string `json:"diff"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, false
	}
	if payload.Mode == "" || payload.Match == nil {
		return nil, false
	}
	reason := "mode=" + payload.Mode
	if payload.Diff != "" {
		reason += "; " + payload.Diff
	}
	return &VerificationResult{
		SourceTool: "verifier.check_output",
		Checks: []VerificationCheck{
			{
				Name:   "verifier.check_output.match",
				Passed: *payload.Match,
				Reason: reason,
			},
		},
	}, true
}

func verificationFromRunTests(raw json.RawMessage) (*VerificationResult, bool) {
	var payload struct {
		ExitCode *int  `json:"exit_code"`
		Passed   *bool `json:"passed"`
		TimedOut bool  `json:"timed_out"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, false
	}
	if payload.Passed == nil {
		return nil, false
	}
	reason := "exit_code=unknown"
	if payload.ExitCode != nil {
		reason = fmt.Sprintf("exit_code=%d", *payload.ExitCode)
	}
	if payload.TimedOut {
		reason += "; timed_out=true"
	}
	return &VerificationResult{
		SourceTool: "verifier.run_tests",
		Checks: []VerificationCheck{
			{
				Name:   "verifier.run_tests.passed",
				Passed: *payload.Passed,
				Reason: reason,
			},
		},
	}, true
}

func verificationFromVerifierBrowserAction(inputRaw, outputRaw json.RawMessage) (*VerificationResult, bool) {
	var input struct {
		Action string `json:"action"`
	}
	if err := json.Unmarshal(inputRaw, &input); err != nil {
		return nil, false
	}
	switch input.Action {
	case "wait":
		verification, ok := verificationFromBrowserWait(outputRaw)
		if !ok || verification == nil {
			return nil, false
		}
		verification.SourceTool = "verifier.browser_action"
		for i := range verification.Checks {
			if strings.HasPrefix(verification.Checks[i].Name, "browser.wait.") {
				verification.Checks[i].Name = strings.Replace(verification.Checks[i].Name, "browser.wait.", "verifier.browser_action.wait.", 1)
			}
		}
		return verification, true
	case "upload_file":
		verification, ok := verificationFromUploadFile(outputRaw)
		if !ok || verification == nil {
			return nil, false
		}
		verification.SourceTool = "verifier.browser_action"
		for i := range verification.Checks {
			if strings.HasPrefix(verification.Checks[i].Name, "browser.upload_file.") {
				verification.Checks[i].Name = strings.Replace(verification.Checks[i].Name, "browser.upload_file.", "verifier.browser_action.upload_file.", 1)
			}
		}
		return verification, true
	case "select":
		verification, ok := verificationFromBrowserSelect(outputRaw)
		if !ok || verification == nil {
			return nil, false
		}
		verification.SourceTool = "verifier.browser_action"
		for i := range verification.Checks {
			if strings.HasPrefix(verification.Checks[i].Name, "browser.select.") {
				verification.Checks[i].Name = strings.Replace(verification.Checks[i].Name, "browser.select.", "verifier.browser_action.select.", 1)
			}
		}
		return verification, true
	default:
		return nil, false
	}
}

func collectFaultSummary(results []toolResultSnapshot, turns []*loop.TurnResult) *FaultSummary {
	var toolFault *FaultSummary
	for i := len(results) - 1; i >= 0; i-- {
		if fault := faultFromToolResult(results[i]); fault != nil {
			toolFault = fault
			break
		}
	}
	for i := len(turns) - 1; i >= 0; i-- {
		if turns[i] == nil || turns[i].Error == nil {
			continue
		}
		route := firstNonEmpty(
			inferFaultRouteFromCode(turns[i].Error.ErrorCode),
			inferFaultRoute(turns[i].Error.Message),
		)
		fault := &FaultSummary{
			Source:  "turn",
			Code:    turns[i].Error.ErrorCode,
			Message: turns[i].Error.Message,
			Route:   route,
		}
		if toolFault != nil && fault.Tool == "" {
			fault.Tool = toolFault.Tool
		}
		contextFault := toolFault
		if contextFault == nil || (contextFault.PageHealth == "" && len(contextFault.Anomalies) == 0) {
			contextFault = latestContextualToolFault(results)
		}
		if contextFault != nil {
			if fault.PageHealth == "" {
				fault.PageHealth = contextFault.PageHealth
			}
			if len(fault.Anomalies) == 0 && len(contextFault.Anomalies) > 0 {
				fault.Anomalies = contextFault.Anomalies
			}
		}
		return fault
	}
	return toolFault
}

func latestContextualToolFault(results []toolResultSnapshot) *FaultSummary {
	for i := len(results) - 1; i >= 0; i-- {
		fault := faultFromToolResult(results[i])
		if fault == nil {
			continue
		}
		if fault.PageHealth != "" || len(fault.Anomalies) > 0 {
			return fault
		}
	}
	return nil
}

func faultFromToolResult(tr toolResultSnapshot) *FaultSummary {
	if tr.CollectCode != "" {
		return &FaultSummary{
			Source:  "sidecar",
			Tool:    tr.ToolName,
			Code:    tr.CollectCode,
			Message: tr.CollectError,
			Route:   "protocol_mismatch",
		}
	}

	obj := decodeJSONObject(tr.Output)

	if fault := faultFromCommandLikeToolResult(tr, obj); fault != nil {
		return fault
	}

	if tr.ToolName == "browser.pattern_exec" {
		var payload struct {
			Success          bool   `json:"success"`
			Error            string `json:"error"`
			AbortedByAnomaly string `json:"aborted_by_anomaly"`
		}
		if err := json.Unmarshal(tr.Output, &payload); err == nil && (!payload.Success || payload.Error != "" || payload.AbortedByAnomaly != "") {
			code := structuredCode(obj)
			fault := &FaultSummary{
				Source:  "tool_result",
				Tool:    tr.ToolName,
				Code:    code,
				Message: payload.Error,
				Route:   firstNonEmpty(inferFaultRouteFromCode(code), inferFaultRoute(payload.Error)),
			}
			if payload.AbortedByAnomaly != "" {
				fault.Anomalies = []FaultAnomaly{{Type: payload.AbortedByAnomaly}}
				if fault.Route == "" || fault.Route == "human_intervention" {
					fault.Route = "abort"
				}
			}
			if fault.Code == "" && payload.AbortedByAnomaly != "" {
				fault.Code = "aborted_by_anomaly"
			}
			if fault.Code == "" {
				fault.Code = inferFaultCode(payload.Error, fault.Route)
			}
			if fault.Message != "" || len(fault.Anomalies) > 0 || fault.Route != "" {
				return fault
			}
		}
	}

	anomalies, pageHealth := extractFaultAnomalies(obj)
	if len(anomalies) > 0 || tr.IsError {
		code := structuredCode(obj)
		fault := &FaultSummary{
			Source:     "tool_result",
			Tool:       tr.ToolName,
			Code:       code,
			Message:    extractMessage(obj, tr.Output),
			PageHealth: pageHealth,
			Anomalies:  anomalies,
		}
		if len(anomalies) > 0 {
			fault.Route = "anomaly_detected"
			if fault.Code == "" {
				fault.Code = "anomaly_detected"
			}
		}
		if tr.ToolName == "verifier.check_output" && fault.Route == "" {
			fault.Route = "verification_failed"
			if fault.Code == "" {
				fault.Code = "verification_failed"
			}
		}
		if fault.Route == "" && tr.IsError {
			fault.Route = inferFaultRouteFromCode(fault.Code)
		}
		if fault.Route == "" && tr.IsError {
			fault.Route = inferFaultRoute(fault.Message)
		}
		if fault.Code == "" {
			fault.Code = inferFaultCode(fault.Message, fault.Route)
		}
		if fault.Message != "" || len(fault.Anomalies) > 0 || fault.PageHealth != "" || fault.Code != "" || fault.Route != "" {
			return fault
		}
	}

	return nil
}

func faultFromCommandLikeToolResult(tr toolResultSnapshot, obj map[string]interface{}) *FaultSummary {
	if obj == nil {
		return nil
	}

	isRunTests := tr.ToolName == "verifier.run_tests"
	isShellExec := strings.HasSuffix(tr.ToolName, ".shell_exec")
	if !isRunTests && !isShellExec {
		return nil
	}

	var payload struct {
		Stdout   string `json:"stdout"`
		Stderr   string `json:"stderr"`
		ExitCode int    `json:"exit_code"`
		TimedOut bool   `json:"timed_out"`
		Passed   *bool  `json:"passed"`
	}
	if err := json.Unmarshal(tr.Output, &payload); err != nil {
		return nil
	}

	_, hasExitCode := obj["exit_code"]
	_, hasTimedOut := obj["timed_out"]
	_, hasPassed := obj["passed"]
	if !hasExitCode && !hasTimedOut && !hasPassed {
		return nil
	}

	fault := &FaultSummary{
		Source:  "tool_result",
		Tool:    tr.ToolName,
		Message: commandFaultMessage(isRunTests, payload.Stdout, payload.Stderr, payload.ExitCode, payload.TimedOut),
	}

	switch {
	case isRunTests && payload.TimedOut:
		fault.Route = "timeout"
		fault.Code = "test_timeout"
	case isRunTests && payload.Passed != nil && !*payload.Passed:
		fault.Route = "verification_failed"
		fault.Code = "test_failed"
	case isRunTests && payload.ExitCode != 0:
		fault.Route = "verification_failed"
		fault.Code = "test_failed"
	case isShellExec && payload.TimedOut:
		fault.Route = "timeout"
		fault.Code = "command_timeout"
	case isShellExec && payload.ExitCode != 0:
		fault.Route = "command_failed"
		fault.Code = "command_exit_nonzero"
	default:
		return nil
	}

	if structured := structuredCode(obj); structured != "" {
		fault.Code = structured
	}
	return fault
}

func extractFaultAnomalies(obj map[string]interface{}) ([]FaultAnomaly, string) {
	container := anomalyContainerObject(obj)
	rawAnomalies, _ := container["anomalies"].([]interface{})
	if len(rawAnomalies) == 0 {
		return nil, stringValue(container["page_health"])
	}
	out := make([]FaultAnomaly, 0, len(rawAnomalies))
	for _, item := range rawAnomalies {
		entry, _ := item.(map[string]interface{})
		if entry == nil {
			continue
		}
		out = append(out, FaultAnomaly{
			Type:     stringValue(entry["type"]),
			Subtype:  stringValue(entry["subtype"]),
			Severity: stringValue(entry["severity"]),
		})
	}
	return out, stringValue(container["page_health"])
}

func anomalyContainerObject(obj map[string]interface{}) map[string]interface{} {
	if obj == nil {
		return nil
	}
	if nested, ok := obj["_anomalies"].(map[string]interface{}); ok {
		return nested
	}
	return obj
}

func extractMessage(obj map[string]interface{}, raw json.RawMessage) string {
	if nested := nestedErrorObject(obj); nested != nil {
		for _, key := range []string{"message", "error", "summary"} {
			if msg := stringValue(nested[key]); msg != "" {
				return msg
			}
		}
	}
	for _, key := range []string{"error", "message", "summary", "diff", "status"} {
		if msg := stringValue(obj[key]); msg != "" {
			return msg
		}
	}
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		return asString
	}
	return ""
}

func structuredCode(obj map[string]interface{}) string {
	if nested := nestedErrorObject(obj); nested != nil {
		for _, key := range []string{"error_code", "code"} {
			if code := stringValue(nested[key]); code != "" {
				return code
			}
		}
	}
	for _, key := range []string{"error_code", "code"} {
		if code := stringValue(obj[key]); code != "" {
			return code
		}
	}
	return ""
}

func nestedErrorObject(obj map[string]interface{}) map[string]interface{} {
	if obj == nil {
		return nil
	}
	nested, _ := obj["error"].(map[string]interface{})
	return nested
}

func inferFaultRoute(msg string) string {
	switch {
	case strings.HasPrefix(msg, "human_intervention:"):
		return "human_intervention"
	case strings.HasPrefix(msg, "aborted_by_anomaly:"):
		return "abort"
	case strings.HasPrefix(msg, "retry exhausted"):
		return "retry"
	case strings.Contains(msg, "fallback_pattern"):
		return "fallback_pattern"
	case strings.HasPrefix(msg, "command execution denied:"):
		return "policy_denied"
	case strings.HasPrefix(msg, "no browser session:"):
		return "precondition_failed"
	case strings.HasPrefix(msg, "invalid arguments:"),
		strings.HasPrefix(msg, "unknown browser action:"),
		strings.HasSuffix(msg, " is required"),
		strings.HasSuffix(msg, "is required"):
		return "input_invalid"
	default:
		return ""
	}
}

func inferFaultRouteFromCode(code string) string {
	switch code {
	case brainerrors.CodeToolTimeout, brainerrors.CodeDeadlineExceeded:
		return "timeout"
	case brainerrors.CodeBudgetTimeoutExhausted:
		return "budget_exhausted"
	case brainerrors.CodeToolInputInvalid, brainerrors.CodeInvalidParams:
		return "input_invalid"
	case brainerrors.CodeBudgetTurnsExhausted, brainerrors.CodeBudgetCostExhausted,
		brainerrors.CodeBudgetToolCallsExhausted, brainerrors.CodeBudgetLLMCallsExhausted:
		return "budget_exhausted"
	case brainerrors.CodeAgentLoopDetected, brainerrors.CodeReflectionBudgetExhausted:
		return "loop_detected"
	case brainerrors.CodeShuttingDown:
		return "shutting_down"
	case brainerrors.CodeLicenseNotFound, brainerrors.CodeLicenseInvalidSignature,
		brainerrors.CodeLicenseExpired, brainerrors.CodeLicenseNotYetValid,
		brainerrors.CodeLicenseBrainNotAllowed, brainerrors.CodeLicenseFeatureNotAllowed,
		brainerrors.CodeLicenseSchemaUnsupported:
		return "license_denied"
	case brainerrors.CodePolicyGateDenied, brainerrors.CodeToolSandboxDenied, brainerrors.CodeLLMSafetyRefused:
		return "policy_denied"
	case brainerrors.CodeToolSanitizeFailed:
		return "sanitize_failed"
	case brainerrors.CodeWorkflowPrecondition:
		return "precondition_failed"
	case brainerrors.CodeToolNotFound:
		return "tool_not_found"
	case brainerrors.CodeToolExecutionFailed:
		return "tool_execution_failed"
	case brainerrors.CodeUnknown:
		return "unknown_error"
	case brainerrors.CodePanicked, brainerrors.CodeAssertionFailed, brainerrors.CodeInvariantViolated:
		return "internal_bug"
	case "verification_failed", "test_failed":
		return "verification_failed"
	case "command_failed", "command_exit_nonzero":
		return "command_failed"
	case "command_timeout", "test_timeout":
		return "timeout"
	default:
		return ""
	}
}

func inferFaultCode(msg, route string) string {
	switch {
	case strings.HasPrefix(msg, "human_intervention:aborted"):
		return "human_intervention_aborted"
	case strings.HasPrefix(msg, "human_intervention:no_coordinator"):
		return "human_intervention_no_coordinator"
	case strings.HasPrefix(msg, "human_intervention:"):
		return "human_intervention"
	case strings.HasPrefix(msg, "aborted_by_anomaly:"):
		return "aborted_by_anomaly"
	case strings.HasPrefix(msg, "retry exhausted"):
		return "retry_exhausted"
	case strings.Contains(msg, "fallback_pattern action without fallback_id"):
		return "fallback_pattern_missing_id"
	case strings.Contains(msg, "fallback pattern not found"):
		return "fallback_pattern_not_found"
	case strings.HasPrefix(msg, "command execution denied:"):
		return brainerrors.CodeToolSandboxDenied
	case strings.HasPrefix(msg, "no browser session:"):
		return brainerrors.CodeWorkflowPrecondition
	case strings.HasPrefix(msg, "invalid arguments:"),
		strings.HasPrefix(msg, "unknown browser action:"),
		strings.HasSuffix(msg, " is required"),
		strings.HasSuffix(msg, "is required"):
		return brainerrors.CodeToolInputInvalid
	}

	switch route {
	case "timeout":
		return brainerrors.CodeDeadlineExceeded
	case "shutting_down":
		return brainerrors.CodeShuttingDown
	case "license_denied":
		return brainerrors.CodeLicenseFeatureNotAllowed
	case "sanitize_failed":
		return brainerrors.CodeToolSanitizeFailed
	case "tool_not_found":
		return brainerrors.CodeToolNotFound
	case "tool_execution_failed":
		return brainerrors.CodeToolExecutionFailed
	case "input_invalid":
		return brainerrors.CodeToolInputInvalid
	case "precondition_failed":
		return brainerrors.CodeWorkflowPrecondition
	case "unknown_error":
		return brainerrors.CodeUnknown
	case "verification_failed":
		return "verification_failed"
	case "command_failed":
		return "command_exit_nonzero"
	}
	return ""
}

func commandFaultMessage(isRunTests bool, stdout, stderr string, exitCode int, timedOut bool) string {
	kind := "shell command"
	if isRunTests {
		kind = "test command"
	}

	summary := fmt.Sprintf("%s failed with exit_code=%d", kind, exitCode)
	if timedOut {
		summary = fmt.Sprintf("%s timed out (exit_code=%d)", kind, exitCode)
	}

	if detail := strings.TrimSpace(firstNonEmpty(stderr, stdout)); detail != "" {
		return summary + ": " + detail
	}
	return summary
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func decodeJSONObject(raw json.RawMessage) map[string]interface{} {
	var obj map[string]interface{}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil
	}
	return obj
}

func decodeJSONArray(raw json.RawMessage) ([]interface{}, bool) {
	var arr []interface{}
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil, false
	}
	return arr, true
}

func stringValue(v interface{}) string {
	s, _ := v.(string)
	return s
}

func mimeTypeFromFormat(format string) string {
	switch strings.ToLower(format) {
	case "jpeg", "jpg":
		return "image/jpeg"
	case "png":
		return "image/png"
	default:
		return ""
	}
}

func boolPtr(v bool) *bool { return &v }

func derefInt(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}

// ---------------------------------------------------------------------------
// stderrToolObserver：把工具执行生命周期打到 stderr，和旧版行为一致。
// ---------------------------------------------------------------------------

// StderrToolObserver 把工具执行生命周期打到 stderr，和旧版行为一致。
type StderrToolObserver struct{}

func (StderrToolObserver) OnToolStart(_ context.Context, _ *loop.Run, _ *loop.Turn, toolName string, _ json.RawMessage) {
	fmt.Fprintf(os.Stderr, "  [sidecar] executing %s\n", toolName)
	diaglog.Logf("tool", "tool=%s start", toolName)
}

func (StderrToolObserver) OnToolEnd(ctx context.Context, _ *loop.Run, _ *loop.Turn, toolName string, ok bool, _ json.RawMessage) {
	if !ok {
		fmt.Fprintf(os.Stderr, "  [sidecar] tool %s FAILED\n", toolName)
		diaglog.Logf("tool", "tool=%s failed", toolName)
	} else {
		diaglog.Logf("tool", "tool=%s ok", toolName)
	}
	// P3.5:把工具级别的结果也喂给 SequenceRecorder 的 turn 窗口。
	// pattern_exec 已在自身 Execute 里写过一次;这里补齐其他工具的失败
	// 信号,让 toolpolicy.DecideBrowserStage 能看到"连续 N turn 无进展"。
	// 只记 error——成功工具太多,写进去会把错误窗口冲掉。
	if !ok && toolName != "browser.pattern_exec" {
		tool.RecordTurnOutcome(ctx, "error")
	}
}

// StreamingToolObserver 在 tool 执行后通过 brain/stream/write 将输出实时发送到 host。
// 用于 Workflow streaming edge 的跨进程流式传输。
type StreamingToolObserver struct {
	PipeID string
	Base   loop.ToolObserver // 可选的底层 observer（如 StderrToolObserver）
}

func (s *StreamingToolObserver) OnToolStart(ctx context.Context, run *loop.Run, turn *loop.Turn, toolName string, input json.RawMessage) {
	if s.Base != nil {
		s.Base.OnToolStart(ctx, run, turn, toolName, input)
	}
}

func (s *StreamingToolObserver) OnToolEnd(ctx context.Context, run *loop.Run, turn *loop.Turn, toolName string, ok bool, output json.RawMessage) {
	if s.Base != nil {
		s.Base.OnToolEnd(ctx, run, turn, toolName, ok, output)
	}
	if ok && s.PipeID != "" {
		EmitStreamChunk(ctx, s.PipeID, output)
	}
}

// progressToolObserver 包装一个 ToolObserver，并在工具生命周期事件发生时
// 通过 brain/progress Notify 将事件实时推送到 host Brain。
type progressToolObserver struct {
	base        loop.ToolObserver
	executionID string
}

func (o *progressToolObserver) OnToolStart(ctx context.Context, run *loop.Run, turn *loop.Turn, toolName string, input json.RawMessage) {
	if o.base != nil {
		o.base.OnToolStart(ctx, run, turn, toolName, input)
	}
	if o.executionID != "" {
		EmitProgress(ctx, ProgressEvent{
			Kind:        "tool_start",
			ExecutionID: o.executionID,
			ToolName:    toolName,
			Args:        string(input),
		})
	}
}

func (o *progressToolObserver) OnToolEnd(ctx context.Context, run *loop.Run, turn *loop.Turn, toolName string, ok bool, output json.RawMessage) {
	if o.base != nil {
		o.base.OnToolEnd(ctx, run, turn, toolName, ok, output)
	}
	if o.executionID != "" {
		EmitProgress(ctx, ProgressEvent{
			Kind:        "tool_end",
			ExecutionID: o.executionID,
			ToolName:    toolName,
			OK:          ok,
			Detail:      string(output),
		})
	}
}

// ---------------------------------------------------------------------------
// 旧版辅助（供 kernel_provider.go 复用）
// ---------------------------------------------------------------------------

// marshalTextContent builds a safe JSON content array with a single text block.
func marshalTextContent(text string) json.RawMessage {
	escaped, _ := json.Marshal(text)
	return json.RawMessage(fmt.Sprintf(`[{"type":"text","text":%s}]`, escaped))
}

// marshalToolOutput wraps a tool's raw output in a content array.
func marshalToolOutput(output json.RawMessage) json.RawMessage {
	if !json.Valid(output) {
		return marshalTextContent(string(output))
	}
	escaped, _ := json.Marshal(string(output))
	return json.RawMessage(fmt.Sprintf(`[{"type":"text","text":%s}]`, escaped))
}

// newBrowserStageHook 返回一个 loop.PreTurnStateHook。它仍按 P3.5 规则在每
// turn 前自动决定 BrowserStage,但不再据此裁剪工具集。
//
// 浏览器大脑必须始终拥有完整 browser.* 工具集;否则像滑块验证码、拖拽、
// 视觉检查这类能力会在某些 turn 被“按阶段隐藏”,导致模型误判为工具不存在。
// 因此 stage 现在只用于轻量引导(例如首轮优先 browser.open),不再影响
// schema 暴露面和真实 dispatch 面。
//
// 触发输入仍来自 SequenceRecorder 的三条信号:
//   - 最近 pattern_match top score(browser.pattern_match 写入)
//   - 最近 turn outcome 窗口("error" 由 stderrToolObserver 在非 pattern_exec
//     工具失败时写入;pattern_exec 自身在执行器里写)
//   - PendingApprovalClass(保留口子,当前 hook 不预判下一步动作 class,
//     所以恒为 "")
//
// 决策返回空字符串("保持上一轮")时,hook 沿用前一轮缓存的 stage,避免
// tool_choice 在边界分数附近抖动。第一轮没有上一轮——按
// DecideBrowserStage 的规则 4:无 pattern_match 数据 → new_page。
func newBrowserStageHook(registry tool.Registry) func(ctx context.Context, run *loop.Run, turnIndex int) (*loop.PreTurnState, error) {
	// Runner calls PreTurnStateHook serially — no mutex needed.
	lastStage := ""
	tools := make([]llm.ToolSchema, 0, len(registry.List()))
	for _, t := range registry.List() {
		s := t.Schema()
		tools = append(tools, llm.ToolSchema{
			Name:        s.Name,
			Description: s.Description,
			InputSchema: s.InputSchema,
		})
	}

	return func(ctx context.Context, _ *loop.Run, turnIndex int) (*loop.PreTurnState, error) {
		in := toolpolicy.DecisionInput{
			RecentPatternScores: tool.RecentPatternScores(ctx),
			RecentTurnOutcomes:  tool.RecentTurnOutcomes(ctx),
			// PendingApprovalClass 目前 hook 无法可靠预判下一步 action
			// 的审批级别(那是 LLM 在本轮决定的)。保留字段以便未来
			// 接入 AdaptiveToolPolicy 的 RecordOutcome 链路时扩展。
		}
		stage := toolpolicy.DecideBrowserStage(in, toolpolicy.DecisionThresholds{})
		if stage == "" {
			stage = lastStage
		}
		if stage == "" {
			// 没有上一轮也没有任何信号 → 沿用基线 registry / tools。
			return nil, nil
		}
		lastStage = stage

		return &loop.PreTurnState{
			Tools:      tools,
			Registry:   registry,
			ToolChoice: browserStageToolChoice(stage, turnIndex),
		}, nil
	}
}

func browserStageToolChoice(stage string, turnIndex int) string {
	if stage == toolpolicy.BrowserStageNewPage && turnIndex == 1 {
		return "browser.open"
	}
	return ""
}

// sidecarOutcomeSink 是 sidecar 进程内的轻量 tool.OutcomeSink 实现。
// 记录每个工具的成功/失败次数，进程生命周期内有效。
type sidecarOutcomeSink struct {
	mu    sync.Mutex
	stats map[string]*toolStat
}

type toolStat struct {
	total   int
	success int
}

func (s *sidecarOutcomeSink) RecordOutcome(toolName string, _ string, success bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.stats[toolName]
	if !ok {
		st = &toolStat{}
		s.stats[toolName] = st
	}
	st.total++
	if success {
		st.success++
	}
}

var (
	sidecarSinkOnce sync.Once
	sidecarSink     *sidecarOutcomeSink
)

func ensureSidecarOutcomeSink() {
	sidecarSinkOnce.Do(func() {
		sidecarSink = &sidecarOutcomeSink{stats: map[string]*toolStat{}}
		tool.SetOutcomeSink(sidecarSink)
	})
}
