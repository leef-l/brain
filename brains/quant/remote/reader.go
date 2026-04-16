// Package remote provides a RemoteBufferManager that fetches market snapshots
// from the Data sidecar via Kernel's specialist.call_tool RPC, eliminating the
// need for Quant sidecar to embed its own DataBrain.
package remote

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/leef-l/brain/brains/data/ringbuf"
	"github.com/leef-l/brain/sdk/sidecar"
)

// BufferManager implements quant.SnapshotSource by polling the Data sidecar
// for all snapshots via specialist.call_tool → data.get_all_snapshots.
type BufferManager struct {
	caller sidecar.KernelCaller
	logger *slog.Logger

	mu          sync.RWMutex
	snapshots   map[string]ringbuf.MarketSnapshot
	instruments []string
}

// New creates a RemoteBufferManager. Call Start() to begin polling.
func New(caller sidecar.KernelCaller, logger *slog.Logger) *BufferManager {
	return &BufferManager{
		caller:    caller,
		logger:    logger,
		snapshots: make(map[string]ringbuf.MarketSnapshot),
	}
}

// Instruments returns the sorted list of instruments from the last fetch.
func (m *BufferManager) Instruments() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]string, len(m.instruments))
	copy(out, m.instruments)
	return out
}

// Latest returns the most recent snapshot for the given instrument.
func (m *BufferManager) Latest(instID string) (ringbuf.MarketSnapshot, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	snap, ok := m.snapshots[instID]
	return snap, ok
}

// Start begins a background goroutine that polls the Data sidecar at the
// given interval. It blocks until ctx is cancelled.
//
// On startup, specialist.call_tool may not yet be registered on the kernel
// side (registerReverseHandlers runs AFTER runner.Start returns, but
// SetKernelCaller fires DURING the initialize handshake). We retry the
// initial fetch with exponential backoff to bridge this timing gap.
func (m *BufferManager) Start(ctx context.Context, interval time.Duration) {
	// Retry initial fetch with backoff — kernel needs time to register
	// specialist.call_tool after the initialize handshake completes.
	backoff := 500 * time.Millisecond
	for attempt := 0; attempt < 10; attempt++ {
		if ctx.Err() != nil {
			return
		}
		if m.fetch(ctx) {
			break
		}
		m.logger.Info("waiting for kernel RPC handlers", "attempt", attempt+1, "backoff", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 5*time.Second {
			backoff = backoff * 2
		}
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.fetch(ctx)
		}
	}
}

// wireSnapshot is the JSON shape returned by data.get_all_snapshots.
type wireSnapshot struct {
	InstID             string       `json:"inst_id"`
	Timestamp          int64        `json:"ts"`
	CurrentPrice       float64      `json:"price"`
	BidPrice           float64      `json:"bid"`
	AskPrice           float64      `json:"ask"`
	FundingRate        float64      `json:"funding_rate"`
	OpenInterest       float64      `json:"open_interest"`
	Volume24h          float64      `json:"volume_24h"`
	OrderBookImbalance float64      `json:"ob_imbalance"`
	Spread             float64      `json:"spread"`
	TradeFlowToxicity  float64      `json:"tft"`
	BigBuyRatio        float64      `json:"big_buy_ratio"`
	BigSellRatio       float64      `json:"big_sell_ratio"`
	TradeDensityRatio  float64      `json:"trade_density"`
	BuySellRatio       float64      `json:"buy_sell_ratio"`
	FeatureVector      [192]float64 `json:"fv"`
	MLSource           string       `json:"ml_source"`
	MLReady            bool         `json:"ml_ready"`
	MarketRegime       string       `json:"regime"`
	AnomalyLevel       float64      `json:"anomaly"`
	VolPercentile      float64      `json:"vol_pct"`
}

// fetch retrieves snapshots from the Data sidecar. Returns true on success.
func (m *BufferManager) fetch(ctx context.Context) bool {
	var raw json.RawMessage
	err := m.caller.CallKernel(ctx, "specialist.call_tool", map[string]any{
		"target_kind": "data",
		"tool_name":   "data.get_all_snapshots",
		"arguments":   map[string]any{},
	}, &raw)
	if err != nil {
		m.logger.Warn("remote snapshot fetch failed", "err", err)
		return false
	}

	// Parse the tool result — it's wrapped in a tool.Result envelope.
	var envelope struct {
		Output json.RawMessage `json:"output"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		// Try direct parse (no envelope).
		m.parseAndStore(raw)
		return true
	}
	if len(envelope.Output) > 0 {
		m.parseAndStore(envelope.Output)
	} else {
		m.parseAndStore(raw)
	}
	return true
}

func (m *BufferManager) parseAndStore(data json.RawMessage) {
	var resp struct {
		Count     int            `json:"count"`
		Snapshots []wireSnapshot `json:"snapshots"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		m.logger.Warn("remote snapshot parse failed", "err", err)
		return
	}

	newMap := make(map[string]ringbuf.MarketSnapshot, len(resp.Snapshots))
	ids := make([]string, 0, len(resp.Snapshots))
	for _, ws := range resp.Snapshots {
		newMap[ws.InstID] = ringbuf.MarketSnapshot{
			InstID:             ws.InstID,
			Timestamp:          ws.Timestamp,
			CurrentPrice:       ws.CurrentPrice,
			BidPrice:           ws.BidPrice,
			AskPrice:           ws.AskPrice,
			FundingRate:        ws.FundingRate,
			OpenInterest:       ws.OpenInterest,
			Volume24h:          ws.Volume24h,
			OrderBookImbalance: ws.OrderBookImbalance,
			Spread:             ws.Spread,
			TradeFlowToxicity:  ws.TradeFlowToxicity,
			BigBuyRatio:        ws.BigBuyRatio,
			BigSellRatio:       ws.BigSellRatio,
			TradeDensityRatio:  ws.TradeDensityRatio,
			BuySellRatio:       ws.BuySellRatio,
			FeatureVector:      ws.FeatureVector,
			MLSource:           ws.MLSource,
			MLReady:            ws.MLReady,
			MarketRegime:       ws.MarketRegime,
			AnomalyLevel:       ws.AnomalyLevel,
			VolPercentile:      ws.VolPercentile,
		}
		ids = append(ids, ws.InstID)
	}
	sort.Strings(ids)

	m.mu.Lock()
	m.snapshots = newMap
	m.instruments = ids
	m.mu.Unlock()
}
