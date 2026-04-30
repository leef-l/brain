package main

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/leef-l/brain/sdk/kernel"
)

// interruptRequest 是 POST /v1/runs/{id}/interrupt 的请求体。
//
// action 必填，可选值：
//   - "stop"    立即终止 Run，下一 turn 退出并标记为 failed
//   - "pause"   暂停 Run，下一 turn 落 checkpoint 后退出（可被 resume 唤醒）
//   - "restart" 提示 Runner 重启当前 turn（当前实现等价于继续，由后续策略扩展）
//   - "resume"  清除已存中断信号（撤销之前的 pause/stop）
//
// reason 可选，会作为审计字段写入信号。
type interruptRequest struct {
	Action string `json:"action"`
	Reason string `json:"reason,omitempty"`
}

// handleInterruptRun 处理 POST /v1/runs/{id}/interrupt。
// 依赖 mgr.interrupter；为 nil 则返回 503。
func handleInterruptRun(w http.ResponseWriter, r *http.Request, mgr *runManager, runID string) {
	if mgr == nil || mgr.interrupter == nil {
		http.Error(w, `{"error":"interrupt checker not available"}`, http.StatusServiceUnavailable)
		return
	}
	if runID == "" {
		http.Error(w, `{"error":"missing run id"}`, http.StatusBadRequest)
		return
	}

	var req interruptRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid json body"}`, http.StatusBadRequest)
		return
	}
	action := strings.ToLower(strings.TrimSpace(req.Action))

	// resume 是“清除中断信号”，而不是发送新的；单独处理。
	if action == "resume" {
		if err := mgr.interrupter.Clear(r.Context(), runID); err != nil {
			writeInterruptJSON(w, http.StatusInternalServerError, map[string]string{
				"error": err.Error(),
			})
			return
		}
		writeInterruptJSON(w, http.StatusOK, map[string]string{
			"status": "cleared",
			"run_id": runID,
		})
		return
	}

	var ka kernel.InterruptAction
	switch action {
	case "stop":
		ka = kernel.InterruptActionStop
	case "pause":
		ka = kernel.InterruptActionPause
	case "restart":
		ka = kernel.InterruptActionRestart
	default:
		http.Error(w, `{"error":"action must be one of: stop|pause|restart|resume"}`, http.StatusBadRequest)
		return
	}

	signal := kernel.NewInterruptSignal(
		kernel.InterruptEmergencyStop, // 默认归类为紧急停止；后续可按 reason 细化
		ka,
		req.Reason,
		"http",
	)
	signal.AffectedTasks = []string{runID}

	if err := mgr.interrupter.Send(r.Context(), signal); err != nil {
		writeInterruptJSON(w, http.StatusInternalServerError, map[string]string{
			"error": err.Error(),
		})
		return
	}

	writeInterruptJSON(w, http.StatusAccepted, map[string]string{
		"status":    "accepted",
		"signal_id": signal.SignalID,
		"run_id":    runID,
		"action":    action,
	})
}

// writeInterruptJSON 是本文件内部使用的 JSON 响应小工具，
// 与 human_takeover_coord.go 中签名不同的 writeJSON 区分开。
func writeInterruptJSON(w http.ResponseWriter, status int, body interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
