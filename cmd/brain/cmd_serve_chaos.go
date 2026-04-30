package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/leef-l/brain/sdk/kernel"
)

// chaosCreateRequest 是 POST /v1/chaos/experiments 的请求体。
//
// fault_type 可选值：
//   - "delay"  — 注入网络延迟（对应 FaultNetworkDelay）
//   - "error"  — 注入 LLM API 错误（对应 FaultLLMError）
//   - "panic"  — 注入进程崩溃（仅 dev 模式，对应 FaultBrainCrash）
//
// intensity 为 0-1.0 之间的浮点数，控制延迟量/触发概率。
// duration_seconds 为实验持续秒数，0 表示使用默认 30 秒。
type chaosCreateRequest struct {
	FaultType       string  `json:"fault_type"`
	Intensity       float64 `json:"intensity"`
	DurationSeconds int     `json:"duration_seconds"`
	Target          string  `json:"target,omitempty"` // 可选目标描述，默认 "rpc"
}

// handleChaosCreate 处理 POST /v1/chaos/experiments。
// 创建并立即启动一个混沌实验（异步后台运行，不阻塞 HTTP 响应）。
func handleChaosCreate(w http.ResponseWriter, r *http.Request, chaos *kernel.ChaosEngine) {
	if chaos == nil {
		writeChaosJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "chaos engine not available",
		})
		return
	}
	if r.Method != http.MethodPost {
		writeChaosJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	var req chaosCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeChaosJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}

	// 映射 fault_type 字符串到内部 FaultType + 注入器
	var ft kernel.FaultType
	switch strings.ToLower(req.FaultType) {
	case "delay":
		ft = kernel.FaultNetworkDelay
		// 延迟注入器：若尚未注册，自动注册默认 5s 最大延迟
		chaos.RegisterInjector(kernel.FaultNetworkDelay, kernel.NewDelayInjector(5*time.Second))
	case "error":
		ft = kernel.FaultLLMError
		chaos.RegisterInjector(kernel.FaultLLMError, kernel.NewErrorInjector())
	case "panic":
		ft = kernel.FaultBrainCrash
		chaos.RegisterInjector(kernel.FaultBrainCrash, kernel.NewPanicInjector(false)) // devMode=false 降级为 NoOp
	default:
		writeChaosJSON(w, http.StatusBadRequest, map[string]string{
			"error": "fault_type must be one of: delay|error|panic",
		})
		return
	}

	if req.DurationSeconds <= 0 {
		req.DurationSeconds = 30
	}
	if req.Target == "" {
		req.Target = "rpc"
	}
	intensity := req.Intensity
	if intensity <= 0 {
		intensity = 0.5
	}

	// 启用混沌引擎（幂等，已 Enable 则无副作用）
	chaos.Enable()

	exp := chaos.CreateExperiment(
		"http-api-experiment",
		ft,
		req.Target,
		time.Duration(req.DurationSeconds)*time.Second,
		intensity,
	)

	// 在后台 goroutine 中运行实验，避免阻塞 HTTP handler
	go func() {
		_, _ = chaos.RunExperiment(r.Context(), exp.ExperimentID)
	}()

	writeChaosJSON(w, http.StatusCreated, map[string]interface{}{
		"experiment_id":    exp.ExperimentID,
		"fault_type":       string(exp.FaultType),
		"intensity":        exp.Intensity,
		"duration_seconds": req.DurationSeconds,
		"target":           exp.Target,
		"status":           "started",
	})
}

// handleChaosStop 处理 DELETE /v1/chaos/experiments/{id}。
// 提前终止指定实验，移除已注入的故障。
func handleChaosStop(w http.ResponseWriter, r *http.Request, chaos *kernel.ChaosEngine, expID string) {
	if chaos == nil {
		writeChaosJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "chaos engine not available",
		})
		return
	}
	if r.Method != http.MethodDelete {
		writeChaosJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if expID == "" {
		writeChaosJSON(w, http.StatusBadRequest, map[string]string{"error": "missing experiment id"})
		return
	}

	if err := chaos.StopExperiment(expID); err != nil {
		writeChaosJSON(w, http.StatusInternalServerError, map[string]string{
			"error": err.Error(),
		})
		return
	}

	writeChaosJSON(w, http.StatusOK, map[string]string{
		"status":        "stopped",
		"experiment_id": expID,
	})
}

// handleChaosHistory 处理 GET /v1/chaos/history。
// 返回所有注入/移除事件的历史审计列表。
func handleChaosHistory(w http.ResponseWriter, r *http.Request, chaos *kernel.ChaosEngine) {
	if chaos == nil {
		writeChaosJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "chaos engine not available",
		})
		return
	}
	if r.Method != http.MethodGet {
		writeChaosJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	history := chaos.InjectionHistory()
	experiments := chaos.AllExperiments()
	summary := chaos.Summary()

	writeChaosJSON(w, http.StatusOK, map[string]interface{}{
		"history":     history,
		"experiments": experiments,
		"summary":     summary,
		"enabled":     chaos.IsEnabled(),
	})
}

// writeChaosJSON 是 cmd_serve_chaos.go 内部使用的 JSON 响应工具。
func writeChaosJSON(w http.ResponseWriter, status int, body interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
