package easymvp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// ─────────────────────────────────────────────────────────────────────────────
// solution_design
// ─────────────────────────────────────────────────────────────────────────────

func (h *Handler) handleSolutionDesign(ctx context.Context, contextJSON json.RawMessage, executionID string) (interface{}, error) {
	fmt.Fprintf(os.Stderr, "easymvp: handleSolutionDesign called execution_id=%s\n", executionID)

	var input struct {
		ProjectID          string `json:"project_id"`
		GoalSummary        string `json:"goal_summary"`
		RequirementID      string `json:"requirement_id"`
		RequirementDocJSON string `json:"requirement_doc_json"`
		Instruction        string `json:"instruction"`
	}
	if err := json.Unmarshal(contextJSON, &input); err != nil {
		fmt.Fprintf(os.Stderr, "easymvp: handleSolutionDesign unmarshal error: %v\n", err)
		return nil, fmt.Errorf("parse solution_design context: %w", err)
	}
	fmt.Fprintf(os.Stderr, "easymvp: handleSolutionDesign input parsed, project_id=%q goal=%q requirement_id=%q\n",
		input.ProjectID, input.GoalSummary, input.RequirementID)

	system := `You are the EasyMVP Solution Design Brain. Based on the project goal and requirement document, design a complete technical solution.
Return ONLY a valid JSON object with these exact fields:
- architecture (string, architecture description covering technology stack, deployment topology, and key design decisions)
- modules_json (string, a JSON-encoded array of module objects each with name, responsibility, and dependencies)
- data_models_json (string, a JSON-encoded array of data model objects each with name, fields, and relationships)
- pages_json (string, a JSON-encoded array of page/screen objects each with name, route, and components)
- task_drafts_json (string, a JSON-encoded array of task draft objects each with task_key, name, phase, summary, brain_kind, and role_type)
- summary (string, one-sentence summary of the overall solution design)
IMPORTANT: modules_json, data_models_json, pages_json, and task_drafts_json must be JSON strings (nested JSON serialized as strings), NOT raw objects/arrays.
Do not wrap in markdown code blocks.`

	user := fmt.Sprintf("Project ID: %s\nProject Goal: %s\nRequirement ID: %s\n\nRequirement Document:\n%s\n\nInstruction: %s",
		input.ProjectID, input.GoalSummary, input.RequirementID, input.RequirementDocJSON, input.Instruction)

	fmt.Fprintf(os.Stderr, "easymvp: handleSolutionDesign calling LLM execution_id=%s...\n", executionID)
	content, err := h.callLLMWithRetry(ctx, system, user, executionID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "easymvp: handleSolutionDesign LLM failed: %v, using fallback\n", err)
		return fallbackSolutionDesignEnvelope(input), nil
	}

	extracted := extractJSONFromText(content)
	if !isValidSolutionDesignJSON(extracted) {
		fmt.Fprintf(os.Stderr, "easymvp: handleSolutionDesign invalid JSON, using fallback. raw=%q extracted=%q\n", content, extracted)
		return fallbackSolutionDesignEnvelope(input), nil
	}

	fmt.Fprintf(os.Stderr, "easymvp: handleSolutionDesign LLM ok, building envelope...\n")
	result := buildEnvelope("solution_design",
		[]map[string]interface{}{
			{"kind": "project", "id": input.ProjectID, "version": 1},
			{"kind": "requirement", "id": input.RequirementID, "version": 1},
		},
		fmt.Sprintf("Solution design generated for requirement %s", input.RequirementID),
		"success",
		json.RawMessage(extracted),
	)
	fmt.Fprintf(os.Stderr, "easymvp: handleSolutionDesign done\n")
	return result, nil
}

// isValidSolutionDesignJSON checks that the extracted JSON is an object
// containing non-empty "architecture" and "summary" fields.
func isValidSolutionDesignJSON(s string) bool {
	var obj map[string]interface{}
	if err := json.Unmarshal([]byte(s), &obj); err != nil {
		return false
	}
	architecture, ok1 := obj["architecture"].(string)
	summary, ok2 := obj["summary"].(string)
	return ok1 && strings.TrimSpace(architecture) != "" && ok2 && strings.TrimSpace(summary) != ""
}

func fallbackSolutionDesignEnvelope(input struct {
	ProjectID          string `json:"project_id"`
	GoalSummary        string `json:"goal_summary"`
	RequirementID      string `json:"requirement_id"`
	RequirementDocJSON string `json:"requirement_doc_json"`
	Instruction        string `json:"instruction"`
}) interface{} {
	goalDesc := input.GoalSummary
	if goalDesc == "" {
		goalDesc = "项目目标"
	}

	modulesJSON, _ := json.Marshal([]map[string]interface{}{
		{"name": "core", "responsibility": fmt.Sprintf("核心业务逻辑 - %s", goalDesc), "dependencies": []string{}},
	})
	dataModelsJSON, _ := json.Marshal([]map[string]interface{}{
		{"name": "BaseEntity", "fields": []string{"id", "created_at", "updated_at"}, "relationships": []string{}},
	})
	pagesJSON, _ := json.Marshal([]map[string]interface{}{
		{"name": "首页", "route": "/", "components": []string{"Header", "Main", "Footer"}},
	})
	taskDraftsJSON, _ := json.Marshal([]map[string]interface{}{
		{"task_key": "task_init", "name": "项目初始化", "phase": "setup", "summary": fmt.Sprintf("初始化项目基础结构 - %s", goalDesc), "brain_kind": "code", "role_type": "developer"},
		{"task_key": "task_core", "name": "核心功能实现", "phase": "implementation", "summary": fmt.Sprintf("实现核心业务逻辑 - %s", goalDesc), "brain_kind": "code", "role_type": "developer"},
	})

	result := map[string]interface{}{
		"architecture":     fmt.Sprintf("基于%s的单体架构方案，包含核心业务模块和基础数据模型（fallback）。", goalDesc),
		"modules_json":     string(modulesJSON),
		"data_models_json": string(dataModelsJSON),
		"pages_json":       string(pagesJSON),
		"task_drafts_json": string(taskDraftsJSON),
		"summary":          fmt.Sprintf("基于目标「%s」生成的默认方案设计（fallback）。", goalDesc),
	}
	return buildEnvelope("solution_design",
		[]map[string]interface{}{
			{"kind": "project", "id": input.ProjectID, "version": 1},
			{"kind": "requirement", "id": input.RequirementID, "version": 1},
		},
		"Fallback solution design: default plan due to LLM unavailability",
		"success",
		result,
	)
}
