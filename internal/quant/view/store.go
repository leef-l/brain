package view

import (
	"sort"
	"sync"
	"time"

	"github.com/leef-l/brain/internal/strategy"
)

// MarketSnapshot is the read-only market state consumed by Quant Brain.
// It satisfies strategy.MarketView so the existing strategy package can run
// without an extra adapter layer.
type MarketSnapshot struct {
	SymbolValue             string
	TimeframeValue          string
	WriteSeqValue           uint64
	TimestampValue          int64
	CurrentPriceValue       float64
	FeatureVectorValue      []float64
	CandlesByTimeframe      map[string][]strategy.Candle
	FundingRateValue        float64
	OrderBookImbalanceValue float64
	TradeFlowToxicityValue  float64
	BigBuyRatioValue        float64
	TradeDensityRatioValue  float64
	SimilarityWinRateValue  float64
}

func NewFixtureSnapshot(symbol string) MarketSnapshot {
	now := time.Now().UTC().UnixMilli()
	candles1H := make([]strategy.Candle, 0, 72)
	candles4H := make([]strategy.Candle, 0, 72)
	base := 100.0
	for i := 0; i < 72; i++ {
		open := base + float64(i)*0.45
		close := open + 0.30
		high := close + 0.20
		low := open - 0.20
		volume := 1000.0 + float64(i)*15
		candles1H = append(candles1H, strategy.Candle{
			Timestamp: now - int64(71-i)*3600*1000,
			Open:      open,
			High:      high,
			Low:       low,
			Close:     close,
			Volume:    volume,
		})
		candles4H = append(candles4H, strategy.Candle{
			Timestamp: now - int64(71-i)*4*3600*1000,
			Open:      open,
			High:      high + 0.40,
			Low:       low - 0.40,
			Close:     close + 0.10,
			Volume:    volume * 4,
		})
	}

	return MarketSnapshot{
		SymbolValue:        symbol,
		TimeframeValue:     "1H",
		WriteSeqValue:      1,
		TimestampValue:     now,
		CurrentPriceValue:  132.5,
		FeatureVectorValue: []float64{0.12, 0.18, 0.21, 0.33, 0.44, 0.51, 0.61, 0.72},
		CandlesByTimeframe: map[string][]strategy.Candle{
			"1H": append([]strategy.Candle(nil), candles1H...),
			"4H": append([]strategy.Candle(nil), candles4H...),
			"tick": {
				{
					Timestamp: now - 2000,
					Open:      132.0,
					High:      132.8,
					Low:       131.8,
					Close:     132.5,
					Volume:    2200,
				},
				{
					Timestamp: now - 1000,
					Open:      132.5,
					High:      133.0,
					Low:       132.2,
					Close:     132.9,
					Volume:    2400,
				},
				{
					Timestamp: now,
					Open:      132.9,
					High:      133.2,
					Low:       132.6,
					Close:     133.0,
					Volume:    2600,
				},
			},
		},
		FundingRateValue:        0.0003,
		OrderBookImbalanceValue: 0.48,
		TradeFlowToxicityValue:  0.72,
		BigBuyRatioValue:        0.76,
		TradeDensityRatioValue:  2.40,
		SimilarityWinRateValue:  0.58,
	}
}

func (s MarketSnapshot) Symbol() string { return s.SymbolValue }

func (s MarketSnapshot) Timeframe() string {
	if s.TimeframeValue != "" {
		return s.TimeframeValue
	}
	return "1H"
}

func (s MarketSnapshot) Candles(timeframe string) []strategy.Candle {
	if timeframe == "" {
		timeframe = s.Timeframe()
	}
	if len(s.CandlesByTimeframe) == 0 {
		return nil
	}
	out := append([]strategy.Candle(nil), s.CandlesByTimeframe[timeframe]...)
	return out
}

func (s MarketSnapshot) CurrentPrice() float64 { return s.CurrentPriceValue }

func (s MarketSnapshot) FeatureVector() []float64 {
	return append([]float64(nil), s.FeatureVectorValue...)
}

func (s MarketSnapshot) FundingRate() float64 { return s.FundingRateValue }

func (s MarketSnapshot) OrderBookImbalance() float64 { return s.OrderBookImbalanceValue }

func (s MarketSnapshot) TradeFlowToxicity() float64 { return s.TradeFlowToxicityValue }

func (s MarketSnapshot) BigBuyRatio() float64 { return s.BigBuyRatioValue }

func (s MarketSnapshot) TradeDensityRatio() float64 { return s.TradeDensityRatioValue }

func (s MarketSnapshot) SimilarityWinRate() float64 { return s.SimilarityWinRateValue }

// PortfolioView is the read-only account summary consumed by Quant Brain.
type PortfolioView struct {
	AsOf               int64    `json:"as_of"`
	TotalEquity        float64  `json:"total_equity"`
	AvailableEquity    float64  `json:"available_equity"`
	OpenPositions      int      `json:"open_positions"`
	LargestPositionPct float64  `json:"largest_position_pct"`
	DailyLossPct       float64  `json:"daily_loss_pct"`
	PausedTrading      bool     `json:"paused_trading"`
	PausedInstruments  []string `json:"paused_instruments,omitempty"`
	Note               string   `json:"note,omitempty"`
}

// Store keeps the latest snapshot and portfolio view in memory.
type Store struct {
	mu        sync.RWMutex
	snapshots map[string]MarketSnapshot
	portfolio PortfolioView
}

func NewStore() *Store {
	return &Store{
		snapshots: make(map[string]MarketSnapshot),
	}
}

func (s *Store) UpsertSnapshot(snapshot MarketSnapshot) {
	if snapshot.SymbolValue == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if snapshot.TimestampValue == 0 {
		snapshot.TimestampValue = time.Now().UTC().UnixMilli()
	}
	s.snapshots[snapshot.SymbolValue] = snapshot
}

func (s *Store) Snapshot(symbol string) (MarketSnapshot, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	snapshot, ok := s.snapshots[symbol]
	return snapshot, ok
}

func (s *Store) SnapshotList() []MarketSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]MarketSnapshot, 0, len(s.snapshots))
	for _, snapshot := range s.snapshots {
		out = append(out, snapshot)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].WriteSeqValue == out[j].WriteSeqValue {
			return out[i].SymbolValue < out[j].SymbolValue
		}
		return out[i].WriteSeqValue > out[j].WriteSeqValue
	})
	return out
}

func (s *Store) SetPortfolio(view PortfolioView) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if view.AsOf == 0 {
		view.AsOf = time.Now().UTC().UnixMilli()
	}
	s.portfolio = view
}

func (s *Store) Portfolio() PortfolioView {
	s.mu.RLock()
	defer s.mu.RUnlock()
	view := s.portfolio
	view.PausedInstruments = append([]string(nil), view.PausedInstruments...)
	return view
}
