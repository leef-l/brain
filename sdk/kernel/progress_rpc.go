package kernel

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/leef-l/brain/sdk/agent"
)

// ProgressReport 是 sidecar 汇报进度的 RPC 请求体。
type ProgressReport struct {
	TaskID         string      `json:"task_id"`
	RunID          string      `json:"run_id"`
	BrainKind      agent.Kind  `json:"brain_kind"`
	Percent        float64     `json:"percent"`
	CurrentAction  string      `json:"current_action"`
	CompletedItems []string    `json:"completed_items"`
	RemainingItems []string    `json:"remaining_items"`
	Confidence     float64     `json:"confidence"`
	IssuesFound    []PlanIssue `json:"issues_found"`
	ReportedAt     time.Time   `json:"reported_at"`
}

// ProgressQueryRequest 是查询进度的请求体。
type ProgressQueryRequest struct {
	ProjectID string `json:"project_id"`
}

// ProgressQueryResponse 是查询进度的响应体。
type ProgressQueryResponse struct {
	Progress ProjectProgress `json:"progress"`
}

// ProgressHandler 处理进度相关的 RPC 请求。
// 它持有 ProjectProgress 引用，接收 sidecar 的进度汇报并更新全局进度。
type ProgressHandler struct {
	progress *ProjectProgress
}

// NewProgressHandler 创建进度 RPC 处理器。
func NewProgressHandler(progress *ProjectProgress) *ProgressHandler {
	return &ProgressHandler{progress: progress}
}

// HandleReport 处理 progress/report 请求。
func (h *ProgressHandler) HandleReport(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var report ProgressReport
	if err := json.Unmarshal(params, &report); err != nil {
		return nil, fmt.Errorf("unmarshal ProgressReport: %w", err)
	}

	if report.ReportedAt.IsZero() {
		report.ReportedAt = time.Now().UTC()
	}

	// 更新 ProjectProgress
	h.progress.UpdateRun(RunProgress{
		RunID:       report.RunID,
		TaskID:      report.TaskID,
		BrainKind:   report.BrainKind,
		Status:      "running",
		LastSummary: report.CurrentAction,
		Confidence:  report.Confidence,
		StartedAt:   report.ReportedAt,
	})

	return map[string]string{"status": "ok"}, nil
}

// HandleQuery 处理 progress/query 请求。
func (h *ProgressHandler) HandleQuery(ctx context.Context, params json.RawMessage) (interface{}, error) {
	if h.progress == nil {
		return nil, fmt.Errorf("no progress tracker available")
	}

	snap := h.progress.Snapshot()
	return &ProgressQueryResponse{Progress: snap}, nil
}
