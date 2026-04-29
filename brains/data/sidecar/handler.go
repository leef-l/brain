package sidecar

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	data "github.com/leef-l/brain/brains/data"
	"github.com/leef-l/brain/sdk/agent"
	"github.com/leef-l/brain/sdk/diaglog"
	"github.com/leef-l/brain/sdk/kernel"
	"github.com/leef-l/brain/sdk/sidecar"
	"github.com/leef-l/brain/sdk/tool"
	"github.com/leef-l/brain/sdk/toolpolicy"

	brain "github.com/leef-l/brain"
)

// dataHandler implements sidecar.BrainHandler and sidecar.ToolSchemaProvider
// for the data brain sidecar.
type dataHandler struct {
	db       *data.DataBrain
	registry tool.Registry
	logger   *slog.Logger
	learner  *data.DataBrainLearner
	caller   sidecar.KernelCaller
}

// NewHandler creates a data sidecar handler.
// db must already be created (but may or may not be started).
func NewHandler(db *data.DataBrain, logger *slog.Logger) *dataHandler {
	if logger == nil {
		logger = slog.Default()
	}

	reg := tool.NewMemRegistry()
	reg.Register(newGetCandlesTool(db))
	reg.Register(newGetAllSnapshotsTool(db))
	reg.Register(newGetSnapshotTool(db))
	reg.Register(newGetFeatureVectorTool(db))
	reg.Register(newProviderHealthTool(db))
	reg.Register(newValidationStatsTool(db))
	reg.Register(newBackfillStatusTool(db))
	reg.Register(newActiveInstrumentsTool(db))
	reg.Register(newReplayStartTool(db))
	reg.Register(newReplayStopTool(db))

	var filtered tool.Registry = reg
	if cfg, err := toolpolicy.Load(""); err != nil {
		logger.Warn("load tool policy failed", "err", err)
	} else {
		filtered = toolpolicy.FilterRegistry(reg, cfg, toolpolicy.ToolScopesForDelegate(string(agent.KindData))...)
	}

	h := &dataHandler{
		db:       db,
		registry: filtered,
		logger:   logger,
		learner:  data.NewDataBrainLearner(db),
	}

	// 注册校验拒绝回调：上报到 Kernel（通过 diaglog 和 brain/progress）
	db.OnValidationRejected(func(result data.ValidationResult) {
		h.logger.Warn("validation rejected",
			"symbol", result.Symbol,
			"reason", result.Reason,
			"alert_type", result.AlertType,
		)
		// 通过 trace.emit 风格上报到 Kernel
		diaglog.Logf("brain", "kind=%s trace=validation_rejected symbol=%s reason=%s alert_type=%s",
			string(agent.KindData),
			result.Symbol,
			result.Reason,
			result.AlertType,
		)
		// 如果 KernelCaller 可用，通过 brain/progress 上报
		if h.caller != nil {
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				_ = h.caller.CallKernel(ctx, "brain/progress", map[string]any{
					"brain":       string(agent.KindData),
					"event":       "validation_rejected",
					"symbol":      result.Symbol,
					"reason":      result.Reason,
					"alert_type":  result.AlertType,
					"timestamp":   time.Now().UnixMilli(),
				}, nil)
			}()
		}
	})

	return h
}

// ---------------------------------------------------------------------------
// sidecar.BrainHandler
// ---------------------------------------------------------------------------

func (h *dataHandler) Kind() agent.Kind { return agent.KindData }
func (h *dataHandler) Version() string  { return brain.SDKVersion }
func (h *dataHandler) Tools() []string  { return sidecar.RegistryToolNames(h.registry) }
func (h *dataHandler) SetKernelCaller(caller sidecar.KernelCaller) {
	h.caller = caller
}

// ---------------------------------------------------------------------------
// sidecar.ToolSchemaProvider
// ---------------------------------------------------------------------------

func (h *dataHandler) ToolSchemas() []tool.Schema {
	return sidecar.RegistryToolSchemas(h.registry)
}

// ---------------------------------------------------------------------------
// HandleMethod
// ---------------------------------------------------------------------------

func (h *dataHandler) HandleMethod(ctx context.Context, method string, params json.RawMessage) (interface{}, error) {
	switch method {
	case "tools/call":
		return sidecar.DispatchToolCall(ctx, params, h.registry, nil)
	case "brain/execute":
		return h.handleExecute(ctx, params)
	case "brain/metrics":
		return h.learner.ExportMetrics(), nil
	case "brain/learn":
		return h.handleLearn(ctx, params)
	default:
		return nil, sidecar.ErrMethodNotFound
	}
}

// handleExecute dispatches brain/execute requests by instruction field.
func (h *dataHandler) handleExecute(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var req sidecar.ExecuteRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return &sidecar.ExecuteResult{
			Status: "failed",
			Error:  fmt.Sprintf("parse request: %v", err),
		}, nil
	}

	start := time.Now()
	var result *sidecar.ExecuteResult
	var execErr error
	diaglog.Logf("brain", "kind=%s instruction=%s execute_start", h.Kind(), req.Instruction)

	switch req.Instruction {
	case "health":
		result, execErr = h.execHealth()
	case "active_instruments":
		result, execErr = h.execActiveInstruments()
	case "snapshot":
		result, execErr = h.execSnapshot(req.Context)
	case "feature_vector":
		result, execErr = h.execFeatureVector(req.Context)
	default:
		result = h.execNaturalLanguage(ctx, &req)
	}

	h.learner.RecordOutcome(ctx, kernel.TaskOutcome{
		TaskType: "data." + req.Instruction,
		Success:  result != nil && result.Status == "completed",
		Duration: time.Since(start),
	})
	if result != nil {
		diaglog.Logf("brain", "kind=%s instruction=%s status=%s duration=%s err=%v", h.Kind(), req.Instruction, result.Status, time.Since(start), execErr)
	} else {
		diaglog.Logf("brain", "kind=%s instruction=%s nil_result duration=%s err=%v", h.Kind(), req.Instruction, time.Since(start), execErr)
	}

	return result, execErr
}

func (h *dataHandler) execNaturalLanguage(ctx context.Context, req *sidecar.ExecuteRequest) *sidecar.ExecuteResult {
	if h.caller == nil {
		return &sidecar.ExecuteResult{
			Status: "failed",
			Error:  fmt.Sprintf("unknown instruction: %s", req.Instruction),
		}
	}

	systemPrompt := `You are a specialist Data Brain for real-time market data queries.

Your tools:
- data.active_instruments: list currently active instruments available in this runtime
- data.get_snapshot: latest market snapshot for one instrument
- data.get_candles: recent OHLCV history
- data.get_feature_vector: regime and feature vector
- data.provider_health: data provider health
- data.validation_stats: data quality metrics
- data.backfill_status: historical backfill progress

RULES:
- Use only the instruments that actually exist in the runtime.
- If the requested asset is unavailable, say that clearly instead of inventing a symbol.
- Prefer data.active_instruments first when the user asks for an unsupported or unclear market.
- Return concrete values when tools provide them; otherwise state the exact limitation.
- Do not claim browser/web search results. You are a market-data specialist, not a browser.`

	maxTurns := 6
	if req.Budget != nil && req.Budget.MaxTurns > 0 {
		maxTurns = req.Budget.MaxTurns
	}
	return sidecar.RunAgentLoopWithContext(ctx, h.caller, h.registry, systemPrompt, req.Instruction, maxTurns, req.Context)
}

// ---------------------------------------------------------------------------
// brain/execute instruction handlers
// ---------------------------------------------------------------------------

// handleLearn handles brain/learn RPC from Orchestrator.
func (h *dataHandler) handleLearn(_ context.Context, params json.RawMessage) (interface{}, error) {
	var payload struct {
		TaskType string  `json:"task_type"`
		Success  bool    `json:"success"`
		Duration float64 `json:"duration"` // seconds
	}
	if err := json.Unmarshal(params, &payload); err != nil {
		return nil, fmt.Errorf("parse learn payload: %w", err)
	}
	if err := h.learner.RecordOutcome(context.Background(), kernel.TaskOutcome{
		TaskType: payload.TaskType,
		Success:  payload.Success,
		Duration: time.Duration(payload.Duration * float64(time.Second)),
	}); err != nil {
		h.logger.Warn("brain/learn failed", "err", err)
		return nil, err
	}
	return map[string]string{"status": "ok"}, nil
}

func (h *dataHandler) execHealth() (*sidecar.ExecuteResult, error) {
	health := h.db.Health()
	d, _ := json.Marshal(health)
	return &sidecar.ExecuteResult{
		Status:  "completed",
		Summary: string(d),
	}, nil
}

func (h *dataHandler) execActiveInstruments() (*sidecar.ExecuteResult, error) {
	instruments := h.db.ActiveInstruments()
	d, _ := json.Marshal(map[string]any{
		"count":       len(instruments),
		"instruments": instruments,
	})
	return &sidecar.ExecuteResult{
		Status:  "completed",
		Summary: string(d),
	}, nil
}

func (h *dataHandler) execSnapshot(rawCtx json.RawMessage) (*sidecar.ExecuteResult, error) {
	var input struct {
		InstrumentID string `json:"instrument_id"`
	}
	if len(rawCtx) > 0 {
		_ = json.Unmarshal(rawCtx, &input)
	}
	if input.InstrumentID == "" {
		return &sidecar.ExecuteResult{
			Status: "failed",
			Error:  "instrument_id is required in context",
		}, nil
	}

	snap, ok := h.db.Buffers().Latest(input.InstrumentID)
	if !ok {
		return &sidecar.ExecuteResult{
			Status: "failed",
			Error:  fmt.Sprintf("no snapshot for %s", input.InstrumentID),
		}, nil
	}

	d, _ := json.Marshal(map[string]any{
		"instrument_id": snap.InstID,
		"price":         snap.CurrentPrice,
		"bid":           snap.BidPrice,
		"ask":           snap.AskPrice,
		"regime":        snap.MarketRegime,
		"anomaly":       snap.AnomalyLevel,
	})
	return &sidecar.ExecuteResult{
		Status:  "completed",
		Summary: string(d),
	}, nil
}

func (h *dataHandler) execFeatureVector(rawCtx json.RawMessage) (*sidecar.ExecuteResult, error) {
	var input struct {
		InstrumentID string `json:"instrument_id"`
	}
	if len(rawCtx) > 0 {
		_ = json.Unmarshal(rawCtx, &input)
	}
	if input.InstrumentID == "" {
		return &sidecar.ExecuteResult{
			Status: "failed",
			Error:  "instrument_id is required in context",
		}, nil
	}

	output := h.db.FeatureVector(input.InstrumentID)
	d, _ := json.Marshal(map[string]any{
		"instrument_id":  input.InstrumentID,
		"market_regime":  output.MarketRegimeLabel(),
		"anomaly_level":  output.AnomalyLevel(),
		"vol_percentile": output.VolPercentile(),
		"ml_source":      output.MLSource,
	})
	return &sidecar.ExecuteResult{
		Status:  "completed",
		Summary: string(d),
	}, nil
}
