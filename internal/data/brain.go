package data

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/leef-l/brain/agent"
	"github.com/leef-l/brain/internal/data/model"
	"github.com/leef-l/brain/internal/data/service"
	"github.com/leef-l/brain/internal/quantcontracts"
	"github.com/leef-l/brain/protocol"
	"github.com/leef-l/brain/sidecar"
	"github.com/leef-l/brain/tool"
)

type Brain struct {
	svc *service.Service
}

func NewBrain(cfg service.Config) *Brain {
	return &Brain{svc: service.New(cfg)}
}

func (b *Brain) Kind() agent.Kind { return agent.KindData }

func (b *Brain) Version() string {
	return "0.1.0"
}

func (b *Brain) Tools() []string {
	return []string{
		model.ToolGetSnapshot,
		model.ToolGetFeatureVector,
		model.ToolGetCandles,
		model.ToolProviderHealth,
		model.ToolValidationStats,
	}
}

func (b *Brain) ToolSchemas() []tool.Schema {
	return []tool.Schema{
		{
			Name:        model.ToolGetSnapshot,
			Description: "Return the latest normalized market snapshot for a symbol.",
			Brain:       string(agent.KindData),
			InputSchema: json.RawMessage(`{"type":"object","properties":{"symbol":{"type":"string"}},"required":["symbol"],"additionalProperties":false}`),
			OutputSchema: json.RawMessage(`{
  "type":"object",
  "properties":{"snapshot":{"type":"object"}},
  "additionalProperties":true
}`),
		},
		{
			Name:         model.ToolGetFeatureVector,
			Description:  "Return the latest feature vector for a symbol.",
			Brain:        string(agent.KindData),
			InputSchema:  json.RawMessage(`{"type":"object","properties":{"symbol":{"type":"string"}},"required":["symbol"],"additionalProperties":false}`),
			OutputSchema: json.RawMessage(`{"type":"object","additionalProperties":true}`),
		},
		{
			Name:         model.ToolGetCandles,
			Description:  "Return the latest candles for a symbol and interval.",
			Brain:        string(agent.KindData),
			InputSchema:  json.RawMessage(`{"type":"object","properties":{"symbol":{"type":"string"},"interval":{"type":"string"}},"required":["symbol","interval"],"additionalProperties":false}`),
			OutputSchema: json.RawMessage(`{"type":"object","additionalProperties":true}`),
		},
		{
			Name:         model.ToolProviderHealth,
			Description:  "Return provider health and lag information.",
			Brain:        string(agent.KindData),
			InputSchema:  json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
			OutputSchema: json.RawMessage(`{"type":"object","additionalProperties":true}`),
		},
		{
			Name:         model.ToolValidationStats,
			Description:  "Return validator counters for the data brain.",
			Brain:        string(agent.KindData),
			InputSchema:  json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
			OutputSchema: json.RawMessage(`{"type":"object","additionalProperties":true}`),
		},
	}
}

func (b *Brain) HandleMethod(ctx context.Context, method string, params json.RawMessage) (interface{}, error) {
	switch method {
	case "tools/call":
		var req protocol.ToolCallRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return dataToolFailure("", "parse_request", fmt.Sprintf("parse request: %v", err)), nil
		}
		return b.handleToolsCall(ctx, req), nil
	case "brain/execute":
		return b.handleExecute(ctx, params), nil
	default:
		return nil, sidecar.ErrMethodNotFound
	}
}

func (b *Brain) HandleTool(ctx context.Context, name string, payload []byte) ([]byte, error) {
	return b.svc.HandleTool(ctx, name, payload)
}

func (b *Brain) Health(ctx context.Context) model.Health {
	return b.svc.Health(ctx)
}

func (b *Brain) Service() *service.Service {
	return b.svc
}

func (b *Brain) RestoreState(ctx context.Context) error {
	if b == nil || b.svc == nil {
		return nil
	}
	return b.svc.RestoreState(ctx)
}

func (b *Brain) handleToolsCall(ctx context.Context, req protocol.ToolCallRequest) *protocol.ToolCallResult {
	switch req.Name {
	case model.ToolGetSnapshot:
		var query quantcontracts.SnapshotQuery
		if err := json.Unmarshal(nonEmptyObject(req.Arguments), &query); err != nil {
			return dataToolFailure(req.Name, "invalid_arguments", fmt.Sprintf("parse snapshot query: %v", err))
		}
		snapshot, ok := b.svc.LatestSnapshot(query.Symbol)
		if !ok {
			return dataToolFailure(req.Name, "not_found", "snapshot not found")
		}
		return dataToolSuccess(req.Name, quantcontracts.SnapshotQueryResult{
			Snapshot: toContractSnapshot(snapshot),
		})

	case model.ToolGetFeatureVector:
		var query quantcontracts.FeatureVectorQuery
		if err := json.Unmarshal(nonEmptyObject(req.Arguments), &query); err != nil {
			return dataToolFailure(req.Name, "invalid_arguments", fmt.Sprintf("parse feature_vector query: %v", err))
		}
		vector, ok := b.svc.FeatureVector(query.Symbol)
		if !ok {
			return dataToolFailure(req.Name, "not_found", "feature vector not found")
		}
		return dataToolSuccess(req.Name, quantcontracts.FeatureVectorResult{
			Symbol: query.Symbol,
			Vector: vector,
		})

	case model.ToolGetCandles:
		var query quantcontracts.CandleQuery
		if err := json.Unmarshal(nonEmptyObject(req.Arguments), &query); err != nil {
			return dataToolFailure(req.Name, "invalid_arguments", fmt.Sprintf("parse candle query: %v", err))
		}
		candles, ok := b.svc.Candles(query.Symbol, query.Interval)
		if !ok {
			return dataToolFailure(req.Name, "not_found", "candles not found")
		}
		return dataToolSuccess(req.Name, quantcontracts.CandleQueryResult{
			Symbol:   query.Symbol,
			Interval: query.Interval,
			Candles:  toContractCandles(candles),
		})

	case model.ToolProviderHealth:
		return dataToolSuccess(req.Name, quantcontracts.ProviderHealthResult{
			Providers: toContractProviderHealth(b.svc.ProviderHealth(ctx)),
		})

	case model.ToolValidationStats:
		return dataToolSuccess(req.Name, map[string]any{
			"stats": b.svc.Health(ctx).ValidationStats,
		})

	default:
		return dataToolFailure(req.Name, "tool_not_found", fmt.Sprintf("unsupported data tool %q", req.Name))
	}
}

func (b *Brain) handleExecute(ctx context.Context, params json.RawMessage) *sidecar.ExecuteResult {
	var req sidecar.ExecuteRequest
	if len(params) > 0 {
		if err := json.Unmarshal(params, &req); err != nil {
			return &sidecar.ExecuteResult{
				Status: "failed",
				Error:  fmt.Sprintf("parse execute request: %v", err),
			}
		}
	}

	switch req.Instruction {
	case quantcontracts.InstructionCollectReviewInput,
		quantcontracts.InstructionReplayStart,
		quantcontracts.InstructionReplayStop,
		quantcontracts.InstructionShutdownPrepare,
		quantcontracts.InstructionHealthCheck:
		ready, reason, health := b.svc.Ready(ctx)
		raw, _ := json.Marshal(health)
		status := "completed"
		errMsg := ""
		if !ready {
			status = "failed"
			errMsg = reason
		}
		return &sidecar.ExecuteResult{
			Status:  status,
			Summary: string(raw),
			Error:   errMsg,
			Turns:   0,
		}
	default:
		return &sidecar.ExecuteResult{
			Status: "failed",
			Error:  fmt.Sprintf("unsupported data instruction %q", req.Instruction),
		}
	}
}

func dataToolFailure(name, code, message string) *protocol.ToolCallResult {
	return &protocol.ToolCallResult{
		Tool:    name,
		IsError: true,
		Error: &protocol.ToolCallError{
			Code:    code,
			Message: message,
		},
		Content: []protocol.ToolCallContent{{Type: "text", Text: message}},
	}
}

func dataToolSuccess(name string, value any) *protocol.ToolCallResult {
	raw, _ := json.Marshal(value)
	return &protocol.ToolCallResult{
		Tool:   name,
		Output: raw,
		Content: []protocol.ToolCallContent{{
			Type: "text",
			Text: string(raw),
		}},
	}
}

func nonEmptyObject(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`{}`)
	}
	return raw
}

func toContractSnapshot(snapshot model.MarketSnapshot) *quantcontracts.MarketSnapshot {
	return &quantcontracts.MarketSnapshot{
		Version:         "v1",
		Sequence:        snapshot.WriteSeq,
		Provider:        snapshot.Provider,
		Topic:           snapshot.Topic,
		Symbol:          snapshot.Symbol,
		TimestampMillis: snapshot.Timestamp,
		Last:            snapshot.Price,
		Volume24h:       snapshot.Volume,
		FeatureVector:   append([]float64(nil), snapshot.FeatureVector...),
		Quality: quantcontracts.SnapshotQuality{
			ProviderState: snapshot.ProviderState,
			Warnings:      filterWarnings(snapshot.ValidationNote),
		},
	}
}

func toContractCandles(candles []model.Candle) []quantcontracts.Candle {
	out := make([]quantcontracts.Candle, 0, len(candles))
	for _, candle := range candles {
		out = append(out, quantcontracts.Candle{
			OpenTimeMillis: candle.Timestamp,
			Open:           candle.Open,
			High:           candle.High,
			Low:            candle.Low,
			Close:          candle.Close,
			Volume:         candle.Volume,
		})
	}
	return out
}

func toContractProviderHealth(healths []model.ProviderHealth) []quantcontracts.ProviderHealth {
	out := make([]quantcontracts.ProviderHealth, 0, len(healths))
	for _, health := range healths {
		out = append(out, quantcontracts.ProviderHealth{
			Provider:  health.Name,
			Status:    health.State,
			LatencyMS: health.LagMs,
			Detail:    health.Detail,
		})
	}
	return out
}

func filterWarnings(note string) []string {
	if note == "" {
		return nil
	}
	return []string{note}
}
