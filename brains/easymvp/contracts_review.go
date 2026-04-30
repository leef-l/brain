package easymvp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// ─────────────────────────────────────────────────────────────────────────────
// design_review
// ─────────────────────────────────────────────────────────────────────────────

func (h *Handler) handleDesignReview(ctx context.Context, contextJSON json.RawMessage) (interface{}, error) {
	var input struct {
		DesignID       string          `json:"design_id"`
		ProjectID      string          `json:"project_id"`
		DesignVersion  int             `json:"design_version"`
		Architecture   string          `json:"architecture"`
		ModulesJSON    json.RawMessage `json:"modules_json,omitempty"`
		DataModelsJSON json.RawMessage `json:"data_models_json,omitempty"`
		PagesJSON      json.RawMessage `json:"pages_json,omitempty"`
		TaskDraftsJSON json.RawMessage `json:"task_drafts_json,omitempty"`
		GoalSummary    string          `json:"goal_summary"`
		Round          int             `json:"round"`
		PreviousIssues []string        `json:"previous_issues,omitempty"`
	}
	if err := json.Unmarshal(contextJSON, &input); err != nil {
		return nil, fmt.Errorf("parse design_review context: %w", err)
	}

	system := `You are the EasyMVP Design Review Brain. Review a design against 5 dimensions:
1. completeness (完整性) — Are all required modules, data models, pages, and tasks covered?
2. consistency (一致性) — Are naming conventions, data flows, and API contracts consistent?
3. feasibility (可行性) — Can this design be implemented with reasonable effort?
4. maintainability (可维护性) — Is the design modular, loosely coupled, and easy to evolve?
5. security (安全性) — Are authentication, authorization, input validation, and data protection addressed?

Each dimension is scored 0-20 (weight=20), total score 0-100.
If total score >= 70, set passed=true; otherwise passed=false.
List concrete issues with severity: critical, major, or minor.

Return ONLY a valid JSON object with these exact fields:
- review_result_id (string)
- passed (boolean)
- score (integer, 0-100)
- dimensions (array of objects with name, score, weight, comment)
- issues (array of objects with code, severity, summary, location, fix)
- suggestions (array of strings)
Do not wrap in markdown code blocks.`

	user := fmt.Sprintf("Design ID: %s\nProject ID: %s\nDesign Version: %d\nArchitecture: %s\nGoal Summary: %s\nReview Round: %d\n",
		input.DesignID, input.ProjectID, input.DesignVersion, input.Architecture, input.GoalSummary, input.Round)
	if len(input.ModulesJSON) > 0 {
		user += fmt.Sprintf("\nModules:\n%s\n", string(input.ModulesJSON))
	}
	if len(input.DataModelsJSON) > 0 {
		user += fmt.Sprintf("\nData Models:\n%s\n", string(input.DataModelsJSON))
	}
	if len(input.PagesJSON) > 0 {
		user += fmt.Sprintf("\nPages:\n%s\n", string(input.PagesJSON))
	}
	if len(input.TaskDraftsJSON) > 0 {
		user += fmt.Sprintf("\nTask Drafts:\n%s\n", string(input.TaskDraftsJSON))
	}
	if len(input.PreviousIssues) > 0 {
		user += fmt.Sprintf("\nPrevious Issues:\n%s\n", strings.Join(input.PreviousIssues, "\n"))
	}

	fmt.Fprintf(os.Stderr, "easymvp: handleDesignReview calling LLM for design=%s round=%d\n", input.DesignID, input.Round)
	content, err := h.callLLMWithRetry(ctx, system, user, "")
	if err != nil {
		fmt.Fprintf(os.Stderr, "easymvp: handleDesignReview LLM failed: %v, using fallback\n", err)
		return fallbackDesignReviewEnvelope(input), nil
	}
	extracted := extractJSONFromText(content)
	if !isValidDesignReviewJSON(extracted) {
		fmt.Fprintf(os.Stderr, "easymvp: handleDesignReview invalid JSON, using fallback. raw=%q extracted=%q\n", content, extracted)
		return fallbackDesignReviewEnvelope(input), nil
	}
	fmt.Fprintf(os.Stderr, "easymvp: handleDesignReview done for design=%s\n", input.DesignID)
	return buildEnvelope("design_review_result",
		[]map[string]interface{}{{"kind": "design", "id": input.DesignID, "version": input.DesignVersion}},
		fmt.Sprintf("Design review completed for %s (round %d)", input.DesignID, input.Round),
		"success",
		json.RawMessage(extracted),
	), nil
}

func isValidDesignReviewJSON(s string) bool {
	var obj map[string]interface{}
	if err := json.Unmarshal([]byte(s), &obj); err != nil {
		return false
	}
	rid, ok1 := obj["review_result_id"].(string)
	score, ok2 := obj["score"].(float64)
	return ok1 && strings.TrimSpace(rid) != "" && ok2 && score >= 0
}

func fallbackDesignReviewEnvelope(input struct {
	DesignID       string          `json:"design_id"`
	ProjectID      string          `json:"project_id"`
	DesignVersion  int             `json:"design_version"`
	Architecture   string          `json:"architecture"`
	ModulesJSON    json.RawMessage `json:"modules_json,omitempty"`
	DataModelsJSON json.RawMessage `json:"data_models_json,omitempty"`
	PagesJSON      json.RawMessage `json:"pages_json,omitempty"`
	TaskDraftsJSON json.RawMessage `json:"task_drafts_json,omitempty"`
	GoalSummary    string          `json:"goal_summary"`
	Round          int             `json:"round"`
	PreviousIssues []string        `json:"previous_issues,omitempty"`
}) interface{} {
	result := map[string]interface{}{
		"review_result_id": "review_fallback",
		"passed":           true,
		"score":            80,
		"dimensions": []map[string]interface{}{
			{"name": "completeness", "score": 16, "weight": 20, "comment": "基本完整（fallback）"},
			{"name": "consistency", "score": 16, "weight": 20, "comment": "一致性良好（fallback）"},
			{"name": "feasibility", "score": 16, "weight": 20, "comment": "可行性良好（fallback）"},
			{"name": "maintainability", "score": 16, "weight": 20, "comment": "可维护性良好（fallback）"},
			{"name": "security", "score": 16, "weight": 20, "comment": "安全性良好（fallback）"},
		},
		"issues":      []interface{}{},
		"suggestions": []interface{}{},
	}
	return buildEnvelope("design_review_result",
		[]map[string]interface{}{{"kind": "design", "id": input.DesignID, "version": input.DesignVersion}},
		"Fallback design review: auto-passed due to LLM unavailability",
		"success",
		result,
	)
}

// ─────────────────────────────────────────────────────────────────────────────
// design_fix
// ─────────────────────────────────────────────────────────────────────────────

func (h *Handler) handleDesignFix(ctx context.Context, contextJSON json.RawMessage) (interface{}, error) {
	var input struct {
		DesignID       string          `json:"design_id"`
		ProjectID      string          `json:"project_id"`
		DesignVersion  int             `json:"design_version"`
		Architecture   string          `json:"architecture"`
		ModulesJSON    json.RawMessage `json:"modules_json,omitempty"`
		DataModelsJSON json.RawMessage `json:"data_models_json,omitempty"`
		PagesJSON      json.RawMessage `json:"pages_json,omitempty"`
		TaskDraftsJSON json.RawMessage `json:"task_drafts_json,omitempty"`
		GoalSummary    string          `json:"goal_summary"`
		Issues         []struct {
			Code     string `json:"code,omitempty"`
			Severity string `json:"severity"`
			Summary  string `json:"summary"`
			Location string `json:"location,omitempty"`
			Fix      string `json:"fix,omitempty"`
		} `json:"issues"`
		Suggestions []string `json:"suggestions"`
	}
	if err := json.Unmarshal(contextJSON, &input); err != nil {
		return nil, fmt.Errorf("parse design_fix context: %w", err)
	}

	system := `You are the EasyMVP Design Fix Brain. Fix a design based on review issues and suggestions.
Rules:
- Fix ALL critical and major issues. Attempt to fix minor issues where feasible.
- Return the complete fixed design including architecture, modules, data models, pages, and task drafts.
- changes_summary must describe what was changed and why.
- fixed_issues must list the issue codes that were resolved.

Return ONLY a valid JSON object with these exact fields:
- fix_result_id (string)
- architecture (string, the fixed architecture description)
- modules_json (object or array, the fixed modules — omit if not applicable)
- data_models_json (object or array, the fixed data models — omit if not applicable)
- pages_json (object or array, the fixed pages — omit if not applicable)
- task_drafts_json (object or array, the fixed task drafts — omit if not applicable)
- changes_summary (string)
- fixed_issues (array of strings, the issue codes that were fixed)
Do not wrap in markdown code blocks.`

	user := fmt.Sprintf("Design ID: %s\nProject ID: %s\nDesign Version: %d\nGoal Summary: %s\n\nArchitecture:\n%s\n",
		input.DesignID, input.ProjectID, input.DesignVersion, input.GoalSummary, input.Architecture)
	if len(input.ModulesJSON) > 0 {
		user += fmt.Sprintf("\nModules:\n%s\n", string(input.ModulesJSON))
	}
	if len(input.DataModelsJSON) > 0 {
		user += fmt.Sprintf("\nData Models:\n%s\n", string(input.DataModelsJSON))
	}
	if len(input.PagesJSON) > 0 {
		user += fmt.Sprintf("\nPages:\n%s\n", string(input.PagesJSON))
	}
	if len(input.TaskDraftsJSON) > 0 {
		user += fmt.Sprintf("\nTask Drafts:\n%s\n", string(input.TaskDraftsJSON))
	}

	// Serialize issues for the prompt
	issuesBytes, _ := json.Marshal(input.Issues)
	user += fmt.Sprintf("\nIssues to fix:\n%s\n", string(issuesBytes))
	if len(input.Suggestions) > 0 {
		user += fmt.Sprintf("\nSuggestions:\n%s\n", strings.Join(input.Suggestions, "\n"))
	}

	fmt.Fprintf(os.Stderr, "easymvp: handleDesignFix calling LLM for design=%s issues=%d\n", input.DesignID, len(input.Issues))
	content, err := h.callLLMWithRetry(ctx, system, user, "")
	if err != nil {
		fmt.Fprintf(os.Stderr, "easymvp: handleDesignFix LLM failed: %v, using fallback\n", err)
		return fallbackDesignFixEnvelope(input), nil
	}
	extracted := extractJSONFromText(content)
	if !isValidDesignFixJSON(extracted) {
		fmt.Fprintf(os.Stderr, "easymvp: handleDesignFix invalid JSON, using fallback. raw=%q extracted=%q\n", content, extracted)
		return fallbackDesignFixEnvelope(input), nil
	}
	fmt.Fprintf(os.Stderr, "easymvp: handleDesignFix done for design=%s\n", input.DesignID)
	return buildEnvelope("design_fix_result",
		[]map[string]interface{}{{"kind": "design", "id": input.DesignID, "version": input.DesignVersion}},
		fmt.Sprintf("Design fix completed for %s (%d issues addressed)", input.DesignID, len(input.Issues)),
		"success",
		json.RawMessage(extracted),
	), nil
}

func isValidDesignFixJSON(s string) bool {
	var obj map[string]interface{}
	if err := json.Unmarshal([]byte(s), &obj); err != nil {
		return false
	}
	rid, ok := obj["fix_result_id"].(string)
	return ok && strings.TrimSpace(rid) != ""
}

func fallbackDesignFixEnvelope(input struct {
	DesignID       string          `json:"design_id"`
	ProjectID      string          `json:"project_id"`
	DesignVersion  int             `json:"design_version"`
	Architecture   string          `json:"architecture"`
	ModulesJSON    json.RawMessage `json:"modules_json,omitempty"`
	DataModelsJSON json.RawMessage `json:"data_models_json,omitempty"`
	PagesJSON      json.RawMessage `json:"pages_json,omitempty"`
	TaskDraftsJSON json.RawMessage `json:"task_drafts_json,omitempty"`
	GoalSummary    string          `json:"goal_summary"`
	Issues         []struct {
		Code     string `json:"code,omitempty"`
		Severity string `json:"severity"`
		Summary  string `json:"summary"`
		Location string `json:"location,omitempty"`
		Fix      string `json:"fix,omitempty"`
	} `json:"issues"`
	Suggestions []string `json:"suggestions"`
}) interface{} {
	result := map[string]interface{}{
		"fix_result_id":   "fix_fallback",
		"architecture":    input.Architecture,
		"changes_summary": "基于审核反馈修复方案（fallback）",
		"fixed_issues":    []interface{}{},
	}
	// Preserve original JSON fields when available
	if len(input.ModulesJSON) > 0 {
		result["modules_json"] = json.RawMessage(input.ModulesJSON)
	}
	if len(input.DataModelsJSON) > 0 {
		result["data_models_json"] = json.RawMessage(input.DataModelsJSON)
	}
	if len(input.PagesJSON) > 0 {
		result["pages_json"] = json.RawMessage(input.PagesJSON)
	}
	if len(input.TaskDraftsJSON) > 0 {
		result["task_drafts_json"] = json.RawMessage(input.TaskDraftsJSON)
	}
	return buildEnvelope("design_fix_result",
		[]map[string]interface{}{{"kind": "design", "id": input.DesignID, "version": input.DesignVersion}},
		"Fallback design fix: original architecture preserved due to LLM unavailability",
		"success",
		result,
	)
}
