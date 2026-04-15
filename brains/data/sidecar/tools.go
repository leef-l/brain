// Package sidecar implements the data brain's sidecar interface layer.
//
// It wraps DataBrain's runtime state as tool.Tool implementations that can
// be called by the Kernel via the specialist.call_tool path, or by other
// brains (e.g. Quant Brain) for market data queries.
//
// Tool inventory (Doc 36 §13):
//
//	data.get_candles          — query historical K-line data
//	data.get_snapshot         — latest Ring Buffer market snapshot
//	data.get_feature_vector   — full 192-dim feature vector
//	data.provider_health      — data source health status
//	data.validation_stats     — data quality metrics
//	data.backfill_status      — backfill progress per instrument
//	data.active_instruments   — current active instrument list
//	data.replay_start         — start historical replay (backtest mode)
//	data.replay_stop          — stop active replay
package sidecar

import (
	"context"
	"encoding/json"
	"fmt"

	data "github.com/leef-l/brain/brains/data"
	"github.com/leef-l/brain/brains/data/feature"
	"github.com/leef-l/brain/sdk/tool"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func marshalResult(v any) (*tool.Result, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return &tool.Result{
			Output:  json.RawMessage(fmt.Sprintf(`{"error":%q}`, err.Error())),
			IsError: true,
		}, nil
	}
	return &tool.Result{Output: b}, nil
}

func errorResult(msg string) (*tool.Result, error) {
	return &tool.Result{
		Output:  json.RawMessage(fmt.Sprintf(`{"error":%q}`, msg)),
		IsError: true,
	}, nil
}

// ---------------------------------------------------------------------------
// data.get_candles
// ---------------------------------------------------------------------------

type getCandlesTool struct{ db *data.DataBrain }

func newGetCandlesTool(db *data.DataBrain) tool.Tool { return &getCandlesTool{db: db} }

func (t *getCandlesTool) Name() string    { return "data.get_candles" }
func (t *getCandlesTool) Risk() tool.Risk { return tool.RiskSafe }
func (t *getCandlesTool) Schema() tool.Schema {
	return tool.Schema{
		Name:        "data.get_candles",
		Description: "查询指定品种和时间框架的历史 K 线数据（最近 500 根）。",
		Brain:       "data",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"instrument_id": {"type": "string", "description": "品种 ID，如 BTC-USDT-SWAP"},
				"timeframe":     {"type": "string", "description": "时间框架：1m, 5m, 15m, 1H, 4H"}
			},
			"required": ["instrument_id", "timeframe"]
		}`),
	}
}

func (t *getCandlesTool) Execute(ctx context.Context, args json.RawMessage) (*tool.Result, error) {
	var input struct {
		InstrumentID string `json:"instrument_id"`
		Timeframe    string `json:"timeframe"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return errorResult("invalid arguments: " + err.Error())
	}
	if input.InstrumentID == "" || input.Timeframe == "" {
		return errorResult("instrument_id and timeframe are required")
	}

	candles := t.db.Candles(input.InstrumentID, input.Timeframe)
	if len(candles) == 0 {
		return marshalResult(map[string]any{
			"instrument_id": input.InstrumentID,
			"timeframe":     input.Timeframe,
			"count":         0,
			"candles":       []any{},
		})
	}

	type candleOut struct {
		Timestamp int64   `json:"ts"`
		Open      float64 `json:"o"`
		High      float64 `json:"h"`
		Low       float64 `json:"l"`
		Close     float64 `json:"c"`
		Volume    float64 `json:"vol"`
	}

	// Return last 100 candles to keep response size reasonable
	start := 0
	if len(candles) > 100 {
		start = len(candles) - 100
	}
	out := make([]candleOut, 0, len(candles)-start)
	for _, c := range candles[start:] {
		out = append(out, candleOut{
			Timestamp: c.Timestamp,
			Open:      c.Open,
			High:      c.High,
			Low:       c.Low,
			Close:     c.Close,
			Volume:    c.Volume,
		})
	}

	return marshalResult(map[string]any{
		"instrument_id": input.InstrumentID,
		"timeframe":     input.Timeframe,
		"count":         len(out),
		"total":         len(candles),
		"candles":       out,
	})
}

// ---------------------------------------------------------------------------
// data.get_snapshot
// ---------------------------------------------------------------------------

type getSnapshotTool struct{ db *data.DataBrain }

func newGetSnapshotTool(db *data.DataBrain) tool.Tool { return &getSnapshotTool{db: db} }

func (t *getSnapshotTool) Name() string    { return "data.get_snapshot" }
func (t *getSnapshotTool) Risk() tool.Risk { return tool.RiskSafe }
func (t *getSnapshotTool) Schema() tool.Schema {
	return tool.Schema{
		Name:        "data.get_snapshot",
		Description: "查询指定品种的 Ring Buffer 最新市场快照，包含价格、微观结构指标。",
		Brain:       "data",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"instrument_id": {"type": "string", "description": "品种 ID"}
			},
			"required": ["instrument_id"]
		}`),
	}
}

func (t *getSnapshotTool) Execute(ctx context.Context, args json.RawMessage) (*tool.Result, error) {
	var input struct {
		InstrumentID string `json:"instrument_id"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return errorResult("invalid arguments: " + err.Error())
	}
	if input.InstrumentID == "" {
		return errorResult("instrument_id is required")
	}

	snap, ok := t.db.Buffers().Latest(input.InstrumentID)
	if !ok {
		return errorResult(fmt.Sprintf("no snapshot for %s", input.InstrumentID))
	}

	return marshalResult(map[string]any{
		"instrument_id":        snap.InstID,
		"timestamp":            snap.Timestamp,
		"seq_num":              snap.SeqNum,
		"current_price":        snap.CurrentPrice,
		"bid":                  snap.BidPrice,
		"ask":                  snap.AskPrice,
		"funding_rate":         snap.FundingRate,
		"open_interest":        snap.OpenInterest,
		"volume_24h":           snap.Volume24h,
		"orderbook_imbalance":  snap.OrderBookImbalance,
		"spread":               snap.Spread,
		"trade_flow_toxicity":  snap.TradeFlowToxicity,
		"big_buy_ratio":        snap.BigBuyRatio,
		"big_sell_ratio":       snap.BigSellRatio,
		"trade_density_ratio":  snap.TradeDensityRatio,
		"buy_sell_ratio":       snap.BuySellRatio,
		"ml_source":            snap.MLSource,
		"ml_ready":             snap.MLReady,
		"market_regime":        snap.MarketRegime,
		"anomaly_level":        snap.AnomalyLevel,
		"vol_percentile":       snap.VolPercentile,
	})
}

// ---------------------------------------------------------------------------
// data.get_feature_vector
// ---------------------------------------------------------------------------

type getFeatureVectorTool struct{ db *data.DataBrain }

func newGetFeatureVectorTool(db *data.DataBrain) tool.Tool { return &getFeatureVectorTool{db: db} }

func (t *getFeatureVectorTool) Name() string    { return "data.get_feature_vector" }
func (t *getFeatureVectorTool) Risk() tool.Risk { return tool.RiskSafe }
func (t *getFeatureVectorTool) Schema() tool.Schema {
	return tool.Schema{
		Name:        "data.get_feature_vector",
		Description: "获取指定品种的完整 192 维特征向量，包含价格/量/微观结构/动量/跨品种/ML 特征。",
		Brain:       "data",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"instrument_id": {"type": "string", "description": "品种 ID"}
			},
			"required": ["instrument_id"]
		}`),
	}
}

func (t *getFeatureVectorTool) Execute(ctx context.Context, args json.RawMessage) (*tool.Result, error) {
	var input struct {
		InstrumentID string `json:"instrument_id"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return errorResult("invalid arguments: " + err.Error())
	}
	if input.InstrumentID == "" {
		return errorResult("instrument_id is required")
	}

	output := t.db.FeatureVector(input.InstrumentID)

	// Break vector into named segments for readability
	vec := output.Vector
	return marshalResult(map[string]any{
		"instrument_id":  input.InstrumentID,
		"dimension":      feature.VectorDim,
		"ml_source":      output.MLSource,
		"ml_ready":       output.MLReady,
		"market_regime":  output.MarketRegimeLabel(),
		"anomaly_level":  output.AnomalyLevel(),
		"vol_percentile": output.VolPercentile(),
		"segments": map[string]any{
			"price":          vec[0:60],
			"volume":         vec[60:100],
			"microstructure": vec[100:130],
			"momentum":       vec[130:160],
			"cross_asset":    vec[160:176],
			"ml_enhanced":    vec[176:192],
		},
	})
}

// ---------------------------------------------------------------------------
// data.provider_health
// ---------------------------------------------------------------------------

type providerHealthTool struct{ db *data.DataBrain }

func newProviderHealthTool(db *data.DataBrain) tool.Tool { return &providerHealthTool{db: db} }

func (t *providerHealthTool) Name() string    { return "data.provider_health" }
func (t *providerHealthTool) Risk() tool.Risk { return tool.RiskSafe }
func (t *providerHealthTool) Schema() tool.Schema {
	return tool.Schema{
		Name:        "data.provider_health",
		Description: "查询数据源健康状态（WebSocket 连接、延迟、错误计数等）。",
		Brain:       "data",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
	}
}

func (t *providerHealthTool) Execute(ctx context.Context, _ json.RawMessage) (*tool.Result, error) {
	ph := t.db.ProviderHealth()
	health := t.db.Health()

	result := map[string]any{
		"brain_health": health,
	}
	if ph != nil {
		result["provider"] = map[string]any{
			"status":      ph.Status,
			"latency_ms":  ph.Latency.Milliseconds(),
			"last_event":  ph.LastEvent.UnixMilli(),
			"error_count": ph.ErrorCount,
		}
	} else {
		result["provider"] = map[string]any{
			"status": "not_configured",
		}
	}

	return marshalResult(result)
}

// ---------------------------------------------------------------------------
// data.validation_stats
// ---------------------------------------------------------------------------

type validationStatsTool struct{ db *data.DataBrain }

func newValidationStatsTool(db *data.DataBrain) tool.Tool { return &validationStatsTool{db: db} }

func (t *validationStatsTool) Name() string    { return "data.validation_stats" }
func (t *validationStatsTool) Risk() tool.Risk { return tool.RiskSafe }
func (t *validationStatsTool) Schema() tool.Schema {
	return tool.Schema{
		Name:        "data.validation_stats",
		Description: "查询数据质量验证统计（拒绝数、PG 写入数、错误数、特征计算耗时）。",
		Brain:       "data",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
	}
}

func (t *validationStatsTool) Execute(ctx context.Context, _ json.RawMessage) (*tool.Result, error) {
	health := t.db.Health()
	return marshalResult(map[string]any{
		"validator_rejected": health["validator_rejected"],
		"pg_writes":          health["pg_writes"],
		"pg_errors":          health["pg_errors"],
		"ringbuf_writes":     health["ringbuf_writes"],
		"ws_messages":        health["ws_messages"],
		"feature_compute_ms": health["feature_compute_ms"],
	})
}

// ---------------------------------------------------------------------------
// data.backfill_status
// ---------------------------------------------------------------------------

type backfillStatusTool struct{ db *data.DataBrain }

func newBackfillStatusTool(db *data.DataBrain) tool.Tool { return &backfillStatusTool{db: db} }

func (t *backfillStatusTool) Name() string    { return "data.backfill_status" }
func (t *backfillStatusTool) Risk() tool.Risk { return tool.RiskSafe }
func (t *backfillStatusTool) Schema() tool.Schema {
	return tool.Schema{
		Name:        "data.backfill_status",
		Description: "查询回填进度（每个品种+时间框架的最新时间戳和条数）。",
		Brain:       "data",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"instrument_id": {"type": "string", "description": "可选，指定品种。不传则返回所有活跃品种。"},
				"timeframe":     {"type": "string", "description": "可选，指定时间框架。不传则返回所有。"}
			}
		}`),
	}
}

func (t *backfillStatusTool) Execute(ctx context.Context, args json.RawMessage) (*tool.Result, error) {
	var input struct {
		InstrumentID string `json:"instrument_id"`
		Timeframe    string `json:"timeframe"`
	}
	if len(args) > 0 {
		_ = json.Unmarshal(args, &input)
	}

	st := t.db.Store()
	if st == nil {
		return errorResult("no persistent store configured")
	}

	timeframes := []string{"1m", "5m", "15m", "1H", "4H"}
	if input.Timeframe != "" {
		timeframes = []string{input.Timeframe}
	}

	instruments := t.db.ActiveInstruments()
	if input.InstrumentID != "" {
		instruments = []string{input.InstrumentID}
	}

	type progressOut struct {
		InstID    string `json:"instrument_id"`
		Timeframe string `json:"timeframe"`
		LatestTS  int64  `json:"latest_ts"`
		BarCount  int    `json:"bar_count"`
	}

	var results []progressOut
	for _, inst := range instruments {
		for _, tf := range timeframes {
			p, err := st.GetProgress(ctx, inst, tf)
			if err != nil || p == nil {
				continue
			}
			results = append(results, progressOut{
				InstID:    p.InstID,
				Timeframe: p.Timeframe,
				LatestTS:  p.LatestTS,
				BarCount:  p.BarCount,
			})
		}
	}

	return marshalResult(map[string]any{
		"count":    len(results),
		"progress": results,
	})
}

// ---------------------------------------------------------------------------
// data.active_instruments
// ---------------------------------------------------------------------------

type activeInstrumentsTool struct{ db *data.DataBrain }

func newActiveInstrumentsTool(db *data.DataBrain) tool.Tool {
	return &activeInstrumentsTool{db: db}
}

func (t *activeInstrumentsTool) Name() string    { return "data.active_instruments" }
func (t *activeInstrumentsTool) Risk() tool.Risk { return tool.RiskSafe }
func (t *activeInstrumentsTool) Schema() tool.Schema {
	return tool.Schema{
		Name:        "data.active_instruments",
		Description: "查询当前活跃的交易品种列表和 Ring Buffer 中的品种数量。",
		Brain:       "data",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
	}
}

func (t *activeInstrumentsTool) Execute(ctx context.Context, _ json.RawMessage) (*tool.Result, error) {
	activeList := t.db.ActiveInstruments()
	bufferInstruments := t.db.Buffers().Instruments()

	return marshalResult(map[string]any{
		"active_count":    len(activeList),
		"active_list":     activeList,
		"buffer_count":    len(bufferInstruments),
		"buffer_list":     bufferInstruments,
	})
}

// ---------------------------------------------------------------------------
// data.replay_start
// ---------------------------------------------------------------------------

type replayStartTool struct{ db *data.DataBrain }

func newReplayStartTool(db *data.DataBrain) tool.Tool { return &replayStartTool{db: db} }

func (t *replayStartTool) Name() string    { return "data.replay_start" }
func (t *replayStartTool) Risk() tool.Risk { return tool.RiskMedium }
func (t *replayStartTool) Schema() tool.Schema {
	return tool.Schema{
		Name:        "data.replay_start",
		Description: "启动历史数据回放（回测模式），从 PG 读取历史 K 线并以事件流方式重放。",
		Brain:       "data",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"instrument_ids": {"type": "array", "items": {"type": "string"}, "description": "回放品种列表"},
				"timeframes":     {"type": "array", "items": {"type": "string"}, "description": "回放时间框架列表"},
				"from_ts":        {"type": "number", "description": "起始时间戳（毫秒）"},
				"to_ts":          {"type": "number", "description": "结束时间戳（毫秒），0 = 到现在"},
				"speed":          {"type": "number", "description": "回放速度：0=最快, 1.0=实时, 10.0=10倍速"}
			},
			"required": ["instrument_ids", "from_ts"]
		}`),
	}
}

func (t *replayStartTool) Execute(ctx context.Context, args json.RawMessage) (*tool.Result, error) {
	// Replay requires a store and is non-trivial to implement at the sidecar
	// level (the DataBrain manages its own provider lifecycle). For now, return
	// a not-yet-implemented response. This placeholder satisfies the Doc 36 §13
	// interface contract; the full implementation will wire into DataBrain's
	// provider swap mechanism.
	return marshalResult(map[string]any{
		"status":  "not_implemented",
		"message": "replay_start requires DataBrain provider swap — use standalone replay mode via data-brain CLI",
	})
}

// ---------------------------------------------------------------------------
// data.replay_stop
// ---------------------------------------------------------------------------

type replayStopTool struct{ db *data.DataBrain }

func newReplayStopTool(db *data.DataBrain) tool.Tool { return &replayStopTool{db: db} }

func (t *replayStopTool) Name() string    { return "data.replay_stop" }
func (t *replayStopTool) Risk() tool.Risk { return tool.RiskMedium }
func (t *replayStopTool) Schema() tool.Schema {
	return tool.Schema{
		Name:        "data.replay_stop",
		Description: "停止当前活跃的历史回放。",
		Brain:       "data",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
	}
}

func (t *replayStopTool) Execute(ctx context.Context, _ json.RawMessage) (*tool.Result, error) {
	return marshalResult(map[string]any{
		"status":  "not_implemented",
		"message": "replay_stop requires DataBrain provider swap — use standalone replay mode via data-brain CLI",
	})
}
