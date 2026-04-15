package sidecar

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	data "github.com/leef-l/brain/brains/data"
	"github.com/leef-l/brain/sdk/agent"
	"github.com/leef-l/brain/sdk/sidecar"
	"github.com/leef-l/brain/sdk/tool"

	brain "github.com/leef-l/brain"
)

// dataHandler implements sidecar.BrainHandler and sidecar.ToolSchemaProvider
// for the data brain sidecar.
type dataHandler struct {
	db       *data.DataBrain
	registry tool.Registry
	logger   *slog.Logger
}

// NewHandler creates a data sidecar handler.
// db must already be created (but may or may not be started).
func NewHandler(db *data.DataBrain, logger *slog.Logger) *dataHandler {
	if logger == nil {
		logger = slog.Default()
	}

	reg := tool.NewMemRegistry()
	reg.Register(newGetCandlesTool(db))
	reg.Register(newGetSnapshotTool(db))
	reg.Register(newGetFeatureVectorTool(db))
	reg.Register(newProviderHealthTool(db))
	reg.Register(newValidationStatsTool(db))
	reg.Register(newBackfillStatusTool(db))
	reg.Register(newActiveInstrumentsTool(db))
	reg.Register(newReplayStartTool(db))
	reg.Register(newReplayStopTool(db))

	return &dataHandler{
		db:       db,
		registry: reg,
		logger:   logger,
	}
}

// ---------------------------------------------------------------------------
// sidecar.BrainHandler
// ---------------------------------------------------------------------------

func (h *dataHandler) Kind() agent.Kind { return agent.KindData }
func (h *dataHandler) Version() string  { return brain.SDKVersion }
func (h *dataHandler) Tools() []string  { return sidecar.RegistryToolNames(h.registry) }

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

	switch req.Instruction {
	case "health":
		return h.execHealth()
	case "active_instruments":
		return h.execActiveInstruments()
	case "snapshot":
		return h.execSnapshot(req.Context)
	case "feature_vector":
		return h.execFeatureVector(req.Context)
	default:
		return &sidecar.ExecuteResult{
			Status: "failed",
			Error:  fmt.Sprintf("unknown instruction: %s", req.Instruction),
		}, nil
	}
}

// ---------------------------------------------------------------------------
// brain/execute instruction handlers
// ---------------------------------------------------------------------------

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
