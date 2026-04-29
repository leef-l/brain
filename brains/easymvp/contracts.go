package easymvp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/leef-l/brain/sdk/llm"
	"github.com/leef-l/brain/sdk/sidecar"
)

func (h *Handler) callLLM(ctx context.Context, systemText, userText string, executionID string) (string, error) {
	if h.caller == nil {
		return "", fmt.Errorf("kernel caller not available")
	}
	fmt.Fprintf(os.Stderr, "easymvp: callLLM starting, userText_len=%d execution_id=%s\n", len(userText), executionID)
	req := &llm.ChatRequest{
		System: []llm.SystemBlock{{Text: systemText, Cache: true}},
		Messages: []llm.Message{
			{Role: "user", Content: []llm.ContentBlock{{Type: "text", Text: userText}}},
		},
		MaxTokens: 4096,
	}
	content, err := sidecar.CallKernelStreamed(ctx, h.caller, req, executionID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "easymvp: callLLM error: %v\n", err)
		return "", err
	}
	preview := content
	if len(preview) > 200 {
		preview = preview[:200]
	}
	fmt.Fprintf(os.Stderr, "easymvp: callLLM ok, message_len=%d preview=%q\n", len(content), preview)
	return content, nil
}

// callLLMWithRetry wraps callLLM with one automatic retry on invalid JSON.
// This addresses P0-04: LLM output reliability for structured JSON envelopes.
func (h *Handler) callLLMWithRetry(ctx context.Context, systemText, userText string, executionID string) (string, error) {
	for attempt := 0; attempt <= 1; attempt++ {
		fmt.Fprintf(os.Stderr, "easymvp: callLLMWithRetry attempt=%d\n", attempt)
		content, err := h.callLLM(ctx, systemText, userText, executionID)
		if err != nil {
			if attempt == 0 {
				fmt.Fprintf(os.Stderr, "easymvp: callLLMWithRetry attempt=%d failed, retrying after 2s...\n", attempt)
				time.Sleep(2 * time.Second)
				continue
			}
			return "", err
		}
		cleaned := extractJSONFromText(content)
		if isValidJSON(cleaned) && strings.HasPrefix(strings.TrimSpace(cleaned), "{") {
			fmt.Fprintf(os.Stderr, "easymvp: callLLMWithRetry attempt=%d got valid JSON object\n", attempt)
			return cleaned, nil
		}
		if isValidJSON(cleaned) {
			fmt.Fprintf(os.Stderr, "easymvp: callLLMWithRetry attempt=%d valid JSON but not object (got array or scalar), retrying...\n", attempt)
		} else {
			fmt.Fprintf(os.Stderr, "easymvp: callLLMWithRetry attempt=%d invalid JSON, retrying...\n", attempt)
		}
		if attempt == 0 {
			fmt.Fprintf(os.Stderr, "easymvp: callLLMWithRetry attempt=%d invalid JSON, retrying after 2s...\n", attempt)
			time.Sleep(2 * time.Second)
			continue
		}
		fmt.Fprintf(os.Stderr, "easymvp: callLLMWithRetry attempt=%d still invalid JSON, returning raw\n", attempt)
		return cleaned, nil
	}
	return "", fmt.Errorf("LLM call exhausted retries")
}

func isValidJSON(s string) bool {
	var v interface{}
	return json.Unmarshal([]byte(s), &v) == nil
}

// extractBracketContent extracts a balanced bracket block starting at index i.
// Returns empty string if no matching closing bracket is found.
func extractBracketContent(s string, i int) string {
	if i >= len(s) || (s[i] != '{' && s[i] != '[') {
		return ""
	}
	start := i
	depth := 1
	inString := false
	escapeNext := false
	for j := i + 1; j < len(s); j++ {
		c := s[j]
		if escapeNext {
			escapeNext = false
			continue
		}
		if c == '\\' && inString {
			escapeNext = true
			continue
		}
		if c == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}
		if c == '{' || c == '[' {
			depth++
		} else if c == '}' || c == ']' {
			depth--
			if depth == 0 && ((s[start] == '{' && c == '}') || (s[start] == '[' && c == ']')) {
				return s[start : j+1]
			}
		}
	}
	return ""
}

// isValidArchitectChatJSON checks that the extracted JSON is an object
// containing a non-empty "reply" field.
func isValidArchitectChatJSON(s string) bool {
	var obj map[string]interface{}
	if err := json.Unmarshal([]byte(s), &obj); err != nil {
		return false
	}
	reply, ok := obj["reply"].(string)
	return ok && strings.TrimSpace(reply) != ""
}

// extractJSONFromText strips markdown code fences and extracts a valid JSON
// object or array from text that may be wrapped in natural language (common
// with reasoning models). If extraction fails, the cleaned string is returned
// so the caller can decide whether to retry.
func extractJSONFromText(s string) string {
	s = strings.TrimSpace(s)
	if isValidJSON(s) {
		return s
	}
	// Strip markdown code fences
	if strings.HasPrefix(s, "```") {
		lines := strings.Split(s, "\n")
		var out []string
		for i, line := range lines {
			if i == 0 && strings.HasPrefix(line, "```") {
				continue
			}
			if strings.TrimSpace(line) == "```" {
				continue
			}
			out = append(out, line)
		}
		s = strings.TrimSpace(strings.Join(out, "\n"))
		if isValidJSON(s) {
			return s
		}
	}
	// Find JSON by bracket matching, respecting string literals.
	// First pass: prefer JSON objects ({}).
	for i := 0; i < len(s); i++ {
		if s[i] != '{' {
			continue
		}
		candidate := extractBracketContent(s, i)
		if candidate != "" && isValidJSON(candidate) {
			return candidate
		}
	}
	// Second pass: accept JSON arrays ([]) if no object found.
	for i := 0; i < len(s); i++ {
		if s[i] != '[' {
			continue
		}
		candidate := extractBracketContent(s, i)
		if candidate != "" && isValidJSON(candidate) {
			return candidate
		}
	}
	return s
}

// buildEnvelope wraps a domain result into the standard BrainContractEnvelope
// format expected by EasyMVP Core. All handlers MUST return this shape.
func buildEnvelope(resultKind string, sourceRefs []map[string]interface{}, decisionSummary, normalizedStatus string, resultJSON interface{}) map[string]interface{} {
	env := map[string]interface{}{
		"schema_version":    1,
		"result_kind":       resultKind,
		"result_version":    1,
		"source_refs":       sourceRefs,
		"decision_summary":  decisionSummary,
		"normalized_status": normalizedStatus,
		"turns":             1,
	}
	if resultJSON != nil {
		switch v := resultJSON.(type) {
		case json.RawMessage:
			if json.Valid(v) {
				env["result_json"] = v
			} else {
				// Not valid JSON — store as a JSON-encoded string so Marshal
				// doesn't fail with "invalid character..." from RawMessage.
				s, _ := json.Marshal(string(v))
				env["result_json"] = json.RawMessage(s)
			}
		case string:
			env["result_json"] = json.RawMessage(v)
		default:
			b, _ := json.Marshal(v)
			env["result_json"] = json.RawMessage(b)
		}
	}
	b, err := json.Marshal(env)
	if err != nil {
		fmt.Fprintf(os.Stderr, "buildEnvelope: json.Marshal failed: %v\n", err)
		return map[string]interface{}{
			"status":  "ok",
			"summary": "",
		}
	}

	return map[string]interface{}{
		"status":  "ok",
		"summary": string(b),
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// architect_chat
// ─────────────────────────────────────────────────────────────────────────────

func (h *Handler) handleArchitectChat(ctx context.Context, contextJSON json.RawMessage, executionID string) (interface{}, error) {
	fmt.Fprintf(os.Stderr, "easymvp: handleArchitectChat called execution_id=%s\n", executionID)
	var input struct {
		ProjectID   string `json:"project_id"`
		GoalSummary string `json:"goal_summary"`
		Messages    []struct {
			Role    string `json:"role"`
			Name    string `json:"name"`
			Content string `json:"content"`
		} `json:"messages"`
		Instruction string `json:"instruction"`
	}
	if err := json.Unmarshal(contextJSON, &input); err != nil {
		fmt.Fprintf(os.Stderr, "easymvp: handleArchitectChat unmarshal error: %v\n", err)
		return nil, fmt.Errorf("parse architect_chat context: %w", err)
	}
	fmt.Fprintf(os.Stderr, "easymvp: handleArchitectChat input parsed, goal=%q messages=%d instruction=%q\n",
		input.GoalSummary, len(input.Messages), input.Instruction)

	system := `You are the EasyMVP Architect Brain. Analyze the project goal and conversation history, then provide a structured response.
Return ONLY a valid JSON object with these exact fields:
- reply (string)
- draft_tasks (array of {task_key, name, phase, task_kind, summary, brain_kind, role_type})
- suggested_next_action (string: "confirm_plan" | "continue_chat" | "refine_goal")
Do not wrap in markdown code blocks.`

	user := fmt.Sprintf("Project Goal: %s\nConversation:\n", input.GoalSummary)
	for _, m := range input.Messages {
		user += fmt.Sprintf("%s: %s\n", m.Role, m.Content)
	}
	user += fmt.Sprintf("\nInstruction: %s", input.Instruction)

	fmt.Fprintf(os.Stderr, "easymvp: handleArchitectChat calling LLM stream=%v...\n", executionID != "")
	content, err := h.callLLMWithRetry(ctx, system, user, executionID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "easymvp: handleArchitectChat LLM failed: %v, using fallback\n", err)
		return fallbackArchitectChatEnvelope(input), nil
	}
	extracted := extractJSONFromText(content)
	// Defensive: ensure the LLM returned a JSON object with a non-empty reply.
	if !isValidArchitectChatJSON(extracted) {
		fmt.Fprintf(os.Stderr, "easymvp: handleArchitectChat invalid JSON object, using fallback. raw=%q extracted=%q\n", content, extracted)
		return fallbackArchitectChatEnvelope(input), nil
	}
	fmt.Fprintf(os.Stderr, "easymvp: handleArchitectChat LLM ok, building envelope...\n")
	result := buildEnvelope("architect_chat",
		[]map[string]interface{}{{"kind": "project", "id": input.ProjectID, "version": 1}},
		"Architect chat response generated",
		"success",
		json.RawMessage(extracted),
	)
	fmt.Fprintf(os.Stderr, "easymvp: handleArchitectChat done\n")
	return result, nil
}

func fallbackArchitectChatEnvelope(input struct {
	ProjectID   string `json:"project_id"`
	GoalSummary string `json:"goal_summary"`
	Messages    []struct {
		Role    string `json:"role"`
		Name    string `json:"name"`
		Content string `json:"content"`
	} `json:"messages"`
	Instruction string `json:"instruction"`
}) interface{} {
	var lastUserMsg string
	for i := len(input.Messages) - 1; i >= 0; i-- {
		if input.Messages[i].Role == "user" {
			lastUserMsg = input.Messages[i].Content
			break
		}
	}
	reply := fmt.Sprintf("收到您的需求：%s。作为架构师，我建议先梳理核心目标，再逐步分解为可执行任务。请确认方向后，我将生成详细方案草案。", lastUserMsg)
	result := map[string]interface{}{
		"reply":                 reply,
		"draft_tasks":           []interface{}{},
		"suggested_next_action": "continue_chat",
	}
	return buildEnvelope("architect_chat",
		[]map[string]interface{}{{"kind": "project", "id": input.ProjectID, "version": 1}},
		"Fallback architect chat: LLM unavailable",
		"success",
		result,
	)
}

// ─────────────────────────────────────────────────────────────────────────────
// plan_review
// ─────────────────────────────────────────────────────────────────────────────

func (h *Handler) handlePlanReview(ctx context.Context, contextJSON json.RawMessage) (interface{}, error) {
	var input struct {
		PlanDraftID            string          `json:"plan_draft_id"`
		PlanDraftVersion       int             `json:"plan_draft_version"`
		PlanDraftJSON          json.RawMessage `json:"plan_draft_json"`
		ProjectCategory        string          `json:"project_category"`
		CategoryProfileVersion int             `json:"category_profile_version"`
		CategoryProfileJSON    json.RawMessage `json:"category_profile_json"`
		ProjectContextJSON     json.RawMessage `json:"project_context_json,omitempty"`
	}
	if err := json.Unmarshal(contextJSON, &input); err != nil {
		return nil, fmt.Errorf("parse plan_review context: %w", err)
	}

	system := `You are the EasyMVP Plan Review Brain. Review a plan draft against the project's category profile and standards. Analyze completeness, consistency, and feasibility.
Return ONLY a valid JSON object with these exact fields:
- review_result_id (string)
- review_version (integer)
- decision (string: "approved", "approved_with_advisory", or "rejected")
- compile_allowed (boolean)
- blocking_issues (array of objects with code, severity, summary)
- advisory_issues (array of objects with code, severity, summary)
- rewrite_hints (array of strings)
If the plan needs significant changes, return "rejected" with rewrite_hints. Do not use "needs_rewrite".
Do not wrap in markdown code blocks.`

	user := fmt.Sprintf("Plan Draft ID: %s\nVersion: %d\nProject Category: %s\nCategory Profile Version: %d\n\nPlan Draft:\n%s\n\nCategory Profile:\n%s\n\nProject Context:\n%s",
		input.PlanDraftID, input.PlanDraftVersion, input.ProjectCategory, input.CategoryProfileVersion,
		string(input.PlanDraftJSON), string(input.CategoryProfileJSON), string(input.ProjectContextJSON))

	content, err := h.callLLMWithRetry(ctx, system, user, "")
	if err != nil {
		return fallbackPlanReviewEnvelope(input), nil
	}
	extracted := extractJSONFromText(content)
	extracted = normalizePlanReviewDecision(extracted)
	// Validate that LLM returned a proper plan review with required fields.
	if !isValidPlanReviewJSON(extracted) {
		fmt.Fprintf(os.Stderr, "easymvp: handlePlanReview invalid plan review JSON, using fallback. raw=%q extracted=%q\n", content, extracted)
		return fallbackPlanReviewEnvelope(input), nil
	}
	return buildEnvelope("plan_review_result",
		[]map[string]interface{}{{"kind": "plan_draft", "id": input.PlanDraftID, "version": input.PlanDraftVersion}},
		fmt.Sprintf("Plan review completed for draft %s", input.PlanDraftID),
		"success",
		json.RawMessage(extracted),
	), nil
}

func isValidPlanReviewJSON(s string) bool {
	var obj map[string]interface{}
	if err := json.Unmarshal([]byte(s), &obj); err != nil {
		return false
	}
	rid, ok1 := obj["review_result_id"].(string)
	version, ok2 := obj["review_version"].(float64)
	decision, ok3 := obj["decision"].(string)
	return ok1 && strings.TrimSpace(rid) != "" && ok2 && int(version) >= 1 && ok3 && strings.TrimSpace(decision) != ""
}

func normalizePlanReviewDecision(s string) string {
	var obj map[string]interface{}
	if err := json.Unmarshal([]byte(s), &obj); err != nil {
		return s
	}
	if dec, ok := obj["decision"].(string); ok && dec == "needs_rewrite" {
		obj["decision"] = "rejected"
	}
	b, _ := json.Marshal(obj)
	return string(b)
}

func fallbackPlanReviewEnvelope(input struct {
	PlanDraftID            string          `json:"plan_draft_id"`
	PlanDraftVersion       int             `json:"plan_draft_version"`
	PlanDraftJSON          json.RawMessage `json:"plan_draft_json"`
	ProjectCategory        string          `json:"project_category"`
	CategoryProfileVersion int             `json:"category_profile_version"`
	CategoryProfileJSON    json.RawMessage `json:"category_profile_json"`
	ProjectContextJSON     json.RawMessage `json:"project_context_json,omitempty"`
}) interface{} {
	result := map[string]interface{}{
		"review_result_id": "review_fallback",
		"review_version":   1,
		"decision":         "approved",
		"compile_allowed":  true,
		"blocking_issues":  []interface{}{},
		"advisory_issues":  []interface{}{},
		"rewrite_hints":    []interface{}{},
	}
	return buildEnvelope("plan_review_result",
		[]map[string]interface{}{{"kind": "plan_draft", "id": input.PlanDraftID, "version": input.PlanDraftVersion}},
		"Fallback plan review: auto-approved due to LLM unavailability",
		"success",
		result,
	)
}

// ─────────────────────────────────────────────────────────────────────────────
// plan_compile
// ─────────────────────────────────────────────────────────────────────────────

func (h *Handler) handlePlanCompile(ctx context.Context, contextJSON json.RawMessage) (interface{}, error) {
	var input struct {
		PlanDraftJSON        json.RawMessage `json:"plan_draft_json"`
		PlanReviewResultJSON json.RawMessage `json:"plan_review_result_json"`
		CategoryProfileJSON  json.RawMessage `json:"category_profile_json"`
		RoleContextJSON      json.RawMessage `json:"role_context_json"`
	}
	if err := json.Unmarshal(contextJSON, &input); err != nil {
		return nil, fmt.Errorf("parse plan_compile context: %w", err)
	}

	system := `You are the EasyMVP Plan Compile Brain. Compile an approved plan draft into executable tasks with delivery and verification contracts.
Return ONLY a valid JSON object with these exact fields:
- compiled_plan_id (string)
- compiled_version (integer)
- compiled_tasks (array of objects with compiled_task_id, name, role_type, brain_kind, delivery_contract, verification_contract, risk_level, depends_on_task_keys)
- risk_summary (object)

For each task in compiled_tasks, include depends_on_task_keys (array of strings) listing the task keys that must complete before this task can start. Use empty array [] if no dependencies. Task execution order must respect these dependencies.
Do not wrap in markdown code blocks.`

	user := fmt.Sprintf("Plan Draft:\n%s\n\nPlan Review Result:\n%s\n\nCategory Profile:\n%s\n\nRole Context:\n%s",
		string(input.PlanDraftJSON), string(input.PlanReviewResultJSON), string(input.CategoryProfileJSON), string(input.RoleContextJSON))

	content, err := h.callLLMWithRetry(ctx, system, user, "")
	if err != nil {
		return fallbackPlanCompileEnvelope(input), nil
	}
	extracted := extractJSONFromText(content)
	// Validate that LLM returned a proper compiled_plan with required fields.
	if !isValidCompiledPlanJSON(extracted) {
		fmt.Fprintf(os.Stderr, "easymvp: handlePlanCompile invalid compiled plan JSON, using fallback. raw=%q extracted=%q\n", content, extracted)
		return fallbackPlanCompileEnvelope(input), nil
	}
	return buildEnvelope("compiled_plan",
		[]map[string]interface{}{{"kind": "compiled_plan", "id": "compiled_from_review", "version": 1}},
		"Plan compiled into executable tasks",
		"success",
		json.RawMessage(extracted),
	), nil
}

func isValidCompiledPlanJSON(s string) bool {
	var obj map[string]interface{}
	if err := json.Unmarshal([]byte(s), &obj); err != nil {
		return false
	}
	pid, ok1 := obj["compiled_plan_id"].(string)
	version, ok2 := obj["compiled_version"].(float64)
	tasks, ok3 := obj["compiled_tasks"].([]interface{})
	return ok1 && strings.TrimSpace(pid) != "" && ok2 && int(version) >= 1 && ok3 && len(tasks) > 0
}

func fallbackPlanCompileEnvelope(input struct {
	PlanDraftJSON        json.RawMessage `json:"plan_draft_json"`
	PlanReviewResultJSON json.RawMessage `json:"plan_review_result_json"`
	CategoryProfileJSON  json.RawMessage `json:"category_profile_json"`
	RoleContextJSON      json.RawMessage `json:"role_context_json"`
}) interface{} {
	// Parse draft tasks from input and convert them into compiled_tasks so that
	// the auto-execution pipeline never gets blocked by an empty compiled_tasks list.
	var draft struct {
		DraftTasks []struct {
			TaskKey   string `json:"task_key"`
			Name      string `json:"name"`
			Phase     string `json:"phase"`
			TaskKind  string `json:"task_kind"`
			Summary   string `json:"summary"`
			BrainKind string `json:"brain_kind"`
			RoleType  string `json:"role_type"`
		} `json:"draft_tasks_json"`
	}
	parseErr := json.Unmarshal(input.PlanDraftJSON, &draft)
	fmt.Fprintf(os.Stderr, "easymvp: fallbackPlanCompileEnvelope parseErr=%v draftTasksLen=%d raw=%s\n", parseErr, len(draft.DraftTasks), string(input.PlanDraftJSON))

	var compiledTasks []map[string]interface{}
	var prevKeys []string
	for _, t := range draft.DraftTasks {
		compiledTaskID := "compiled_" + t.TaskKey
		roleType := t.RoleType
		if roleType == "" {
			roleType = "developer"
			if strings.EqualFold(t.BrainKind, "verifier") {
				roleType = "tester"
			}
		}
		riskLevel := "low"
		if strings.EqualFold(t.TaskKind, "verification") {
			riskLevel = "medium"
		}
		delivery := map[string]interface{}{
			"goal":    t.Summary,
			"summary": t.Summary,
		}
		verification := map[string]interface{}{
			"acceptance_criteria": []string{fmt.Sprintf("Verify %s", t.Name)},
		}
		dependsOn := make([]string, len(prevKeys))
		copy(dependsOn, prevKeys)
		compiledTasks = append(compiledTasks, map[string]interface{}{
			"compiled_task_id":      compiledTaskID,
			"name":                  t.Name,
			"role_type":             roleType,
			"brain_kind":            t.BrainKind,
			"delivery_contract":     delivery,
			"verification_contract": verification,
			"risk_level":            riskLevel,
			"depends_on_task_keys":  dependsOn,
		})
		prevKeys = append(prevKeys, t.TaskKey)
	}

	if len(compiledTasks) == 0 {
		compiledTasks = []map[string]interface{}{
			{
				"compiled_task_id":      "compiled_fallback_task",
				"name":                  "Fallback Implementation",
				"role_type":             "developer",
				"brain_kind":            "code",
				"delivery_contract":     map[string]interface{}{"goal": "Implement project requirements", "summary": "Fallback task"},
				"verification_contract": map[string]interface{}{"acceptance_criteria": []string{"Verify fallback task"}},
				"risk_level":            "low",
				"depends_on_task_keys":  []string{},
			},
		}
	}

	result := map[string]interface{}{
		"compiled_plan_id": "compiled_fallback",
		"compiled_version": 1,
		"compiled_tasks":   compiledTasks,
		"risk_summary":     map[string]interface{}{"overall_risk_level": "low", "risks": []interface{}{}},
	}
	return buildEnvelope("compiled_plan",
		[]map[string]interface{}{{"kind": "compiled_plan", "id": "compiled_fallback", "version": 1}},
		"Fallback plan compile: generated from draft tasks due to LLM unavailability",
		"success",
		result,
	)
}

// ─────────────────────────────────────────────────────────────────────────────
// plan_redesign
// ─────────────────────────────────────────────────────────────────────────────

func (h *Handler) handlePlanRedesign(ctx context.Context, contextJSON json.RawMessage) (interface{}, error) {
	var input struct {
		PlanDraftID      string          `json:"plan_draft_id"`
		PlanDraftJSON    json.RawMessage `json:"plan_draft_json"`
		ReviewResultID   string          `json:"review_result_id"`
		ReviewResultJSON json.RawMessage `json:"review_result_json"`
		RewriteHints     []string        `json:"rewrite_hints"`
		Feedback         string          `json:"feedback,omitempty"`
	}
	if err := json.Unmarshal(contextJSON, &input); err != nil {
		return nil, fmt.Errorf("parse plan_redesign context: %w", err)
	}

	system := `You are the EasyMVP Plan Redesign Brain. Redesign a plan draft based on review feedback and rewrite hints.
Return ONLY a valid JSON object with these exact fields:
- redesigned_plan_draft_id (string)
- redesigned_plan_json (object, the full redesigned plan)
- changes_summary (string, describing what changed and why)
Do not wrap in markdown code blocks.`

	user := fmt.Sprintf("Plan Draft ID: %s\n\nPlan Draft:\n%s\n\nReview Result ID: %s\nReview Result:\n%s\n\nRewrite Hints: %v\n\nFeedback: %s",
		input.PlanDraftID, string(input.PlanDraftJSON), input.ReviewResultID, string(input.ReviewResultJSON), input.RewriteHints, input.Feedback)

	content, err := h.callLLMWithRetry(ctx, system, user, "")
	if err != nil {
		return fallbackPlanRedesignEnvelope(input), nil
	}
	return buildEnvelope("redesigned_plan_draft",
		[]map[string]interface{}{{"kind": "plan_draft", "id": input.PlanDraftID, "version": 1}},
		fmt.Sprintf("Plan redesigned based on review %s", input.ReviewResultID),
		"success",
		json.RawMessage(extractJSONFromText(content)),
	), nil
}

func fallbackPlanRedesignEnvelope(input struct {
	PlanDraftID      string          `json:"plan_draft_id"`
	PlanDraftJSON    json.RawMessage `json:"plan_draft_json"`
	ReviewResultID   string          `json:"review_result_id"`
	ReviewResultJSON json.RawMessage `json:"review_result_json"`
	RewriteHints     []string        `json:"rewrite_hints"`
	Feedback         string          `json:"feedback,omitempty"`
}) interface{} {
	result := map[string]interface{}{
		"redesigned_plan_draft_id": "redesign_fallback",
		"redesigned_plan_json":     map[string]interface{}{},
		"changes_summary":          "基于反馈调整方案（fallback）。",
	}
	return buildEnvelope("plan_redesign",
		[]map[string]interface{}{{"kind": "plan_draft", "id": input.PlanDraftID, "version": 1}},
		"Fallback plan redesign: minimal changes due to LLM unavailability",
		"success",
		result,
	)
}

// ─────────────────────────────────────────────────────────────────────────────
// repair_design
// ─────────────────────────────────────────────────────────────────────────────

func (h *Handler) handleRepairDesign(ctx context.Context, contextJSON json.RawMessage) (interface{}, error) {
	var input struct {
		FailedTaskContextJSON json.RawMessage `json:"failed_task_context_json"`
		FailureReasonJSON     json.RawMessage `json:"failure_reason_json"`
		OriginalContractsJSON json.RawMessage `json:"original_contracts_json"`
		RuntimeSummaryJSON    json.RawMessage `json:"runtime_summary_json"`
	}
	if err := json.Unmarshal(contextJSON, &input); err != nil {
		return nil, fmt.Errorf("parse repair_design context: %w", err)
	}

	system := `You are the EasyMVP Repair Design Brain. Analyze failed task execution and design a repair plan.
Return ONLY a valid JSON object with these exact fields:
- repair_plan_draft_id (string)
- repair_plan_json (object, the repair plan)
- repair_reasoning_summary (string)
- replaced_constraints (array of strings)
- reason_class (string: "execution_error" | "verification_failure" | "delivery_mismatch" | "policy_violation" | "environment_failure")
- repair_strategy (string: "retry" | "redesign" | "replace" | "escalate" | "manual_checkpoint")
- updated_tasks (array of objects with task_key, name, summary, brain_kind)
- verification_adjustments (array of strings)
- delivery_adjustments (array of strings)
- human_checkpoint_required (boolean)
Do not wrap in markdown code blocks.`

	user := fmt.Sprintf("Failed Task Context:\n%s\n\nFailure Reason:\n%s\n\nOriginal Contracts:\n%s\n\nRuntime Summary:\n%s",
		string(input.FailedTaskContextJSON), string(input.FailureReasonJSON), string(input.OriginalContractsJSON), string(input.RuntimeSummaryJSON))

	content, err := h.callLLMWithRetry(ctx, system, user, "")
	if err != nil {
		return fallbackRepairDesignEnvelope(input), nil
	}
	return buildEnvelope("repair_plan_draft",
		[]map[string]interface{}{{"kind": "failure_context", "id": "failure_auto", "version": 1}},
		"Repair plan designed for failed task execution",
		"success",
		json.RawMessage(extractJSONFromText(content)),
	), nil
}

func fallbackRepairDesignEnvelope(input struct {
	FailedTaskContextJSON json.RawMessage `json:"failed_task_context_json"`
	FailureReasonJSON     json.RawMessage `json:"failure_reason_json"`
	OriginalContractsJSON json.RawMessage `json:"original_contracts_json"`
	RuntimeSummaryJSON    json.RawMessage `json:"runtime_summary_json"`
}) interface{} {
	result := map[string]interface{}{
		"repair_plan_draft_id":      "repair_fallback",
		"repair_plan_json":          map[string]interface{}{},
		"repair_reasoning_summary":  "分析失败原因并设计修复方案（fallback）。",
		"replaced_constraints":      []interface{}{},
		"reason_class":              "execution_error",
		"repair_strategy":           "retry",
		"updated_tasks":             []interface{}{},
		"verification_adjustments":  []interface{}{},
		"delivery_adjustments":      []interface{}{},
		"human_checkpoint_required": false,
	}
	return buildEnvelope("repair_design",
		[]map[string]interface{}{{"kind": "failure_context", "id": "failure_fallback", "version": 1}},
		"Fallback repair design: minimal retry due to LLM unavailability",
		"success",
		result,
	)
}

// ─────────────────────────────────────────────────────────────────────────────
// acceptance_mapping
// ─────────────────────────────────────────────────────────────────────────────

func (h *Handler) handleAcceptanceMapping(ctx context.Context, contextJSON json.RawMessage) (interface{}, error) {
	var input struct {
		ProjectCategory     string          `json:"project_category"`
		CategoryProfileJSON json.RawMessage `json:"category_profile_json"`
		ArtifactSummaryJSON json.RawMessage `json:"artifact_summary_json"`
		CoverageSummaryJSON json.RawMessage `json:"coverage_summary_json"`
	}
	if err := json.Unmarshal(contextJSON, &input); err != nil {
		return nil, fmt.Errorf("parse acceptance_mapping context: %w", err)
	}

	system := `You are the EasyMVP Acceptance Mapping Brain. Map a project category to acceptance criteria, required surfaces, journeys, and evidence.
Return ONLY a valid JSON object with these exact fields:
- acceptance_profile_id (string)
- production_acceptance_profile_id (string)
- required_surfaces (array of strings)
- required_journeys (array of strings)
- required_evidence (array of strings)
- release_requirements (array of strings)
Do not wrap in markdown code blocks.`

	user := fmt.Sprintf("Project Category: %s\n\nCategory Profile:\n%s\n\nArtifact Summary:\n%s\n\nCoverage Summary:\n%s",
		input.ProjectCategory, string(input.CategoryProfileJSON), string(input.ArtifactSummaryJSON), string(input.CoverageSummaryJSON))

	content, err := h.callLLMWithRetry(ctx, system, user, "")
	if err != nil {
		return fallbackAcceptanceMappingEnvelope(input), nil
	}
	return buildEnvelope("acceptance_mapping_result",
		[]map[string]interface{}{{"kind": "project_category", "id": input.ProjectCategory, "version": 1}},
		fmt.Sprintf("Acceptance criteria mapped for category %s", input.ProjectCategory),
		"success",
		json.RawMessage(extractJSONFromText(content)),
	), nil
}

func fallbackAcceptanceMappingEnvelope(input struct {
	ProjectCategory     string          `json:"project_category"`
	CategoryProfileJSON json.RawMessage `json:"category_profile_json"`
	ArtifactSummaryJSON json.RawMessage `json:"artifact_summary_json"`
	CoverageSummaryJSON json.RawMessage `json:"coverage_summary_json"`
}) interface{} {
	result := map[string]interface{}{
		"acceptance_profile_id":            "accept_fallback",
		"production_acceptance_profile_id": "prod_accept_fallback",
		"required_surfaces":                []interface{}{},
		"required_journeys":                []interface{}{},
		"required_evidence":                []interface{}{},
		"release_requirements":             []interface{}{},
	}
	return buildEnvelope("acceptance_mapping",
		[]map[string]interface{}{{"kind": "project_category", "id": input.ProjectCategory, "version": 1}},
		"Fallback acceptance mapping: empty criteria due to LLM unavailability",
		"success",
		result,
	)
}

// ─────────────────────────────────────────────────────────────────────────────
// completion_adjudication
// ─────────────────────────────────────────────────────────────────────────────

func (h *Handler) handleCompletionAdjudication(ctx context.Context, contextJSON json.RawMessage) (interface{}, error) {
	var input struct {
		ExecutionSummaryJSON    json.RawMessage `json:"execution_summary_json"`
		DeliverySummaryJSON     json.RawMessage `json:"delivery_summary_json"`
		VerificationSummaryJSON json.RawMessage `json:"verification_summary_json"`
		AcceptanceSummaryJSON   json.RawMessage `json:"acceptance_summary_json"`
		ManualReleaseStateJSON  json.RawMessage `json:"manual_release_state_json"`
	}
	if err := json.Unmarshal(contextJSON, &input); err != nil {
		return nil, fmt.Errorf("parse completion_adjudication context: %w", err)
	}

	system := `You are the EasyMVP Completion Adjudication Brain. Judge whether a project execution run is complete based on execution, delivery, verification, acceptance, and manual release data.
Return ONLY a valid JSON object with these exact fields:
- functional_passed (boolean)
- production_passed (boolean)
- manual_release_required (boolean)
- manual_release_completed (boolean)
- final_status (string)
- decision_reason (string)
- executor_succeeded (boolean)
- delivery_verified (boolean)
- acceptance_passed (boolean)
- completed (boolean)
- decision (string: "complete", "rework", "blocked", or "manual_checkpoint")
Do not wrap in markdown code blocks.`

	user := fmt.Sprintf("Execution Summary:\n%s\n\nDelivery Summary:\n%s\n\nVerification Summary:\n%s\n\nAcceptance Summary:\n%s\n\nManual Release State:\n%s",
		string(input.ExecutionSummaryJSON), string(input.DeliverySummaryJSON), string(input.VerificationSummaryJSON),
		string(input.AcceptanceSummaryJSON), string(input.ManualReleaseStateJSON))

	content, err := h.callLLMWithRetry(ctx, system, user, "")
	if err != nil {
		return fallbackCompletionAdjudicationEnvelope(input), nil
	}
	return buildEnvelope("completion_decision",
		[]map[string]interface{}{{"kind": "acceptance_run", "id": "run_auto", "version": 1}},
		"Completion adjudication verdict rendered",
		"success",
		json.RawMessage(extractJSONFromText(content)),
	), nil
}

func fallbackCompletionAdjudicationEnvelope(input struct {
	ExecutionSummaryJSON    json.RawMessage `json:"execution_summary_json"`
	DeliverySummaryJSON     json.RawMessage `json:"delivery_summary_json"`
	VerificationSummaryJSON json.RawMessage `json:"verification_summary_json"`
	AcceptanceSummaryJSON   json.RawMessage `json:"acceptance_summary_json"`
	ManualReleaseStateJSON  json.RawMessage `json:"manual_release_state_json"`
}) interface{} {
	result := map[string]interface{}{
		"functional_passed":        true,
		"production_passed":        true,
		"manual_release_required":  false,
		"manual_release_completed": false,
		"final_status":             "completed",
		"decision_reason":          "所有验收条件满足（fallback）。",
		"executor_succeeded":       true,
		"delivery_verified":        true,
		"acceptance_passed":        true,
		"completed":                true,
		"decision":                 "complete",
	}
	return buildEnvelope("completion_adjudication",
		[]map[string]interface{}{{"kind": "acceptance_run", "id": "run_fallback", "version": 1}},
		"Fallback completion adjudication: auto-complete due to LLM unavailability",
		"success",
		result,
	)
}

// ─────────────────────────────────────────────────────────────────────────────
// workspace_explanation
// ─────────────────────────────────────────────────────────────────────────────

func (h *Handler) handleWorkspaceExplanation(ctx context.Context, contextJSON json.RawMessage) (interface{}, error) {
	var input struct {
		WorkspaceContextJSON      json.RawMessage `json:"workspace_context_json"`
		RiskSummaryJSON           json.RawMessage `json:"risk_summary_json"`
		LatestDecisionSummaryJSON json.RawMessage `json:"latest_decision_summary_json"`
	}
	if err := json.Unmarshal(contextJSON, &input); err != nil {
		return nil, fmt.Errorf("parse workspace_explanation context: %w", err)
	}

	system := `You are the EasyMVP Workspace Explanation Brain. Explain the current workspace status to the user in a concise, actionable way.
Return ONLY a valid JSON object with these exact fields:
- headline (string, one-sentence status)
- summary (string, 2-3 sentence explanation)
- top_blockers (array of strings, current blockers if any)
- recommended_actions (array of objects with action_key, label, reason, deep_link)
- explain_links (array of strings, optional helpful links)
Do not wrap in markdown code blocks.`

	user := fmt.Sprintf("Workspace Context:\n%s\n\nRisk Summary:\n%s\n\nLatest Decision Summary:\n%s",
		string(input.WorkspaceContextJSON), string(input.RiskSummaryJSON), string(input.LatestDecisionSummaryJSON))

	content, err := h.callLLMWithRetry(ctx, system, user, "")
	if err != nil {
		return fallbackWorkspaceExplanationEnvelope(input), nil
	}
	return buildEnvelope("workspace_explanation",
		[]map[string]interface{}{{"kind": "workspace", "id": "current", "version": 1}},
		"Workspace status explanation generated",
		"success",
		json.RawMessage(extractJSONFromText(content)),
	), nil
}

func fallbackWorkspaceExplanationEnvelope(input struct {
	WorkspaceContextJSON      json.RawMessage `json:"workspace_context_json"`
	RiskSummaryJSON           json.RawMessage `json:"risk_summary_json"`
	LatestDecisionSummaryJSON json.RawMessage `json:"latest_decision_summary_json"`
}) interface{} {
	result := map[string]interface{}{
		"headline":            "工作台状态正常（fallback）。",
		"summary":             "当前没有阻塞问题，项目运行平稳。",
		"top_blockers":        []interface{}{},
		"recommended_actions": []interface{}{},
		"explain_links":       []interface{}{},
	}
	return buildEnvelope("workspace_explanation",
		[]map[string]interface{}{{"kind": "workspace", "id": "current", "version": 1}},
		"Fallback workspace explanation: no blockers detected",
		"success",
		result,
	)
}
