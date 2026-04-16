package risk

import (
	"testing"
	"time"

	"github.com/leef-l/brain/brains/quant/strategy"
)

func TestGuardRejectsTooMuchLeverage(t *testing.T) {
	guard := DefaultGuard()
	decision := guard.CheckOrder(OrderRequest{
		Symbol:     "BTC-USDT-SWAP",
		Action:     ActionOpen,
		Direction:  strategy.DirectionLong,
		EntryPrice: 100,
		StopLoss:   95,
		Notional:   100,
		Leverage:   25,
		ATR:        3,
	}, PortfolioSnapshot{Equity: 1000})
	if decision.Allowed {
		t.Fatalf("decision allowed, want reject")
	}
}

func TestGuardPortfolioCorrelatedSymbol(t *testing.T) {
	guard := DefaultGuard()
	decision := guard.CheckPortfolio(OrderRequest{
		Symbol:     "ETH-USDT-SWAP",
		Action:     ActionOpen,
		Direction:  strategy.DirectionLong,
		EntryPrice: 200,
		StopLoss:   190,
		Notional:   50,
		Leverage:   5,
		ATR:        5,
	}, PortfolioSnapshot{
		Equity: 1000,
		Positions: []Position{
			{Symbol: "BTC-USDT-SWAP", Direction: strategy.DirectionLong, Notional: 100},
		},
		CorrelatedGroups: map[string][]string{
			"majors": []string{"BTC-USDT-SWAP", "ETH-USDT-SWAP"},
		},
	})
	if decision.Allowed {
		t.Fatalf("decision allowed, want reject")
	}
}

func TestGuardCircuitPause(t *testing.T) {
	guard := DefaultGuard()
	decision := guard.CheckCircuitBreaker(CircuitSnapshot{
		VolatilityPercentile: 99.1,
	})
	if decision.Allowed {
		t.Fatalf("decision allowed, want pause")
	}
	if decision.Action != "pause" {
		t.Fatalf("action = %s, want pause", decision.Action)
	}
}

func TestPositionSizer(t *testing.T) {
	sizer := DefaultPositionSizer()
	result, err := sizer.Size(SizeRequest{
		AccountEquity: 10000,
		Signal: strategy.Signal{
			Entry: 100,
		},
		WinRate: 0.55,
		AvgWin:  2.0,
		AvgLoss: 1.0,
	})
	if err != nil {
		t.Fatalf("Size() error: %v", err)
	}
	if result.RiskFraction < 0.005 || result.RiskFraction > 0.05 {
		t.Fatalf("risk fraction = %.4f out of bounds", result.RiskFraction)
	}
	if result.Quantity <= 0 {
		t.Fatalf("quantity = %.4f, want positive", result.Quantity)
	}
}

func TestGuardEvaluatePasses(t *testing.T) {
	guard := DefaultGuard()
	decision := guard.Evaluate(OrderRequest{
		Symbol:     "SOL-USDT-SWAP",
		Action:     ActionOpen,
		Direction:  strategy.DirectionLong,
		EntryPrice: 100,
		StopLoss:   95,
		Notional:   40,
		Leverage:   3,
		ATR:        3,
	}, PortfolioSnapshot{
		Equity:    1000,
		Positions: nil,
	}, CircuitSnapshot{})
	if !decision.Allowed {
		t.Fatalf("decision rejected: %s (%s)", decision.Reason, decision.Layer)
	}
}

func TestGuardCircuitMemoryAlert(t *testing.T) {
	guard := DefaultGuard()
	decision := guard.CheckCircuitBreaker(CircuitSnapshot{
		MemoryGB: 40,
	})
	if decision.Action != "alert" {
		t.Fatalf("action = %s, want alert", decision.Action)
	}
	if decision.Allowed {
		t.Fatalf("decision allowed, want alert rejection")
	}
}

func TestGuardPauseDuration(t *testing.T) {
	guard := DefaultGuard()
	decision := guard.CheckCircuitBreaker(CircuitSnapshot{ExecutorFailureStreak: 3})
	if decision.PauseFor != 10*time.Minute {
		t.Fatalf("pause = %s, want 10m", decision.PauseFor)
	}
}

// ── AdaptiveGuard tests ─────────────────────────────────────────

type fakeFeatureView struct {
	volPercentile float64
	volDirection  float64
}

func (f *fakeFeatureView) EMADeviation(tf string, period int) float64 { return 0 }
func (f *fakeFeatureView) EMACross(tf string) float64                 { return 0 }
func (f *fakeFeatureView) RSI(tf string) float64                      { return 0 }
func (f *fakeFeatureView) MACDHistogram(tf string) float64            { return 0 }
func (f *fakeFeatureView) BBPosition(tf string) float64               { return 0 }
func (f *fakeFeatureView) ATRRatio(tf string) float64                 { return 0 }
func (f *fakeFeatureView) PriceChange(tf string, bars int) float64    { return 0 }
func (f *fakeFeatureView) Volatility(tf string, bars int) float64     { return 0 }
func (f *fakeFeatureView) ADX(tf string) float64                      { return 0 }
func (f *fakeFeatureView) VolumeRatio(tf string) float64              { return 0 }
func (f *fakeFeatureView) OBVSlope(tf string) float64                 { return 0 }
func (f *fakeFeatureView) VolumePriceCorr(tf string) float64          { return 0 }
func (f *fakeFeatureView) VolumeBreakout(tf string) bool              { return false }
func (f *fakeFeatureView) OrderBookImbalance() float64                { return 0 }
func (f *fakeFeatureView) Spread() float64                            { return 0 }
func (f *fakeFeatureView) TradeFlowToxicity() float64                 { return 0 }
func (f *fakeFeatureView) BigBuyRatio() float64                       { return 0 }
func (f *fakeFeatureView) BigSellRatio() float64                      { return 0 }
func (f *fakeFeatureView) TradeDensityRatio() float64                 { return 0 }
func (f *fakeFeatureView) BuySellRatio() float64                      { return 0 }
func (f *fakeFeatureView) FundingRate() float64                       { return 0 }
func (f *fakeFeatureView) Momentum(tf string, bars int) float64       { return 0 }
func (f *fakeFeatureView) VolatilityRatio(tf string) float64          { return 0 }
func (f *fakeFeatureView) BTCExcessReturn() float64                   { return 0 }
func (f *fakeFeatureView) BTCMomentum() float64                       { return 0 }
func (f *fakeFeatureView) ETHMomentum() float64                       { return 0 }
func (f *fakeFeatureView) BTCCorrelation() float64                    { return 0 }
func (f *fakeFeatureView) ETHCorrelation() float64                    { return 0 }
func (f *fakeFeatureView) MLReady() bool                              { return false }
func (f *fakeFeatureView) Symbol() string                             { return "TEST" }
func (f *fakeFeatureView) CurrentPrice() float64                      { return 100 }
func (f *fakeFeatureView) RawVector() []float64                       { return nil }

func (f *fakeFeatureView) MarketRegime() strategy.MarketRegimeProb {
	return strategy.MarketRegimeProb{}
}
func (f *fakeFeatureView) VolPrediction() strategy.VolPrediction {
	return strategy.VolPrediction{VolPercentile: f.volPercentile, VolDirection: f.volDirection}
}
func (f *fakeFeatureView) AnomalyScore() strategy.AnomalyScore {
	return strategy.AnomalyScore{}
}

type fakeMarketView struct {
	fv strategy.FeatureView
}

func (v fakeMarketView) Symbol() string                         { return "TEST" }
func (v fakeMarketView) Timeframe() string                      { return "1H" }
func (v fakeMarketView) Candles(tf string) []strategy.Candle    { return nil }
func (v fakeMarketView) CurrentPrice() float64                  { return 100 }
func (v fakeMarketView) FeatureVector() []float64               { return nil }
func (v fakeMarketView) FundingRate() float64                   { return 0 }
func (v fakeMarketView) OrderBookImbalance() float64            { return 0 }
func (v fakeMarketView) TradeFlowToxicity() float64             { return 0 }
func (v fakeMarketView) BigBuyRatio() float64                   { return 0 }
func (v fakeMarketView) BigSellRatio() float64                  { return 0 }
func (v fakeMarketView) TradeDensityRatio() float64             { return 0 }
func (v fakeMarketView) SimilarityWinRate() float64             { return 0 }
func (v fakeMarketView) Feature() strategy.FeatureView          { return v.fv }
func (v fakeMarketView) HasFeatureView() bool                   { return v.fv != nil }

func TestAdaptiveGuardHighVolTightens(t *testing.T) {
	ag := DefaultAdaptiveGuard()
	baseMaxPos := ag.Base.MaxSinglePositionPct

	view := fakeMarketView{fv: &fakeFeatureView{volPercentile: 0.80}}
	req := OrderRequest{
		Symbol:     "BTC-USDT-SWAP",
		Action:     ActionOpen,
		Direction:  strategy.DirectionLong,
		EntryPrice: 100,
		StopLoss:   95,
		Notional:   baseMaxPos * 10 * 0.65, // between tightened and original limit
		Leverage:   3,
		ATR:        3,
	}
	portfolio := PortfolioSnapshot{Equity: 1000}

	// With high vol view, should tighten MaxSinglePositionPct to 5*0.6=3%
	decision := ag.Evaluate(req, portfolio, CircuitSnapshot{}, view)
	if decision.Allowed {
		t.Fatal("expected rejection under high vol tightened limits")
	}
}

func TestAdaptiveGuardNoFeatureViewUsesBase(t *testing.T) {
	ag := DefaultAdaptiveGuard()
	req := OrderRequest{
		Symbol:     "SOL-USDT-SWAP",
		Action:     ActionOpen,
		Direction:  strategy.DirectionLong,
		EntryPrice: 100,
		StopLoss:   95,
		Notional:   40,
		Leverage:   3,
		ATR:        3,
	}
	portfolio := PortfolioSnapshot{Equity: 1000}

	// Without FeatureView, uses base Guard
	decision := ag.Evaluate(req, portfolio, CircuitSnapshot{}, nil)
	if !decision.Allowed {
		t.Fatalf("expected allow with base guard, got: %s", decision.Reason)
	}
}

func TestAdaptiveGuardExtremeVol(t *testing.T) {
	ag := DefaultAdaptiveGuard()
	view := fakeMarketView{fv: &fakeFeatureView{volPercentile: 0.95}}

	// MaxConcurrentPositions = 10 * 0.4 = 4
	portfolio := PortfolioSnapshot{
		Equity: 10000,
		Positions: []Position{
			{Symbol: "BTC-USDT-SWAP", Direction: strategy.DirectionLong, Notional: 100},
			{Symbol: "ETH-USDT-SWAP", Direction: strategy.DirectionShort, Notional: 100},
			{Symbol: "DOGE-USDT-SWAP", Direction: strategy.DirectionLong, Notional: 100},
			{Symbol: "XRP-USDT-SWAP", Direction: strategy.DirectionShort, Notional: 100},
		},
	}
	req := OrderRequest{
		Symbol:     "SOL-USDT-SWAP",
		Action:     ActionOpen,
		Direction:  strategy.DirectionLong,
		EntryPrice: 100,
		StopLoss:   95,
		Notional:   50,
		Leverage:   3,
		ATR:        3,
	}
	decision := ag.Evaluate(req, portfolio, CircuitSnapshot{}, view)
	if decision.Allowed {
		t.Fatal("expected rejection: too many positions under extreme vol")
	}
}

// ── BayesianSizer tests ─────────────────────────────────────────

func TestBayesianSizerNoSamples(t *testing.T) {
	bs := DefaultBayesianSizer()
	result, err := bs.Size(BayesianSizeRequest{
		AccountEquity: 10000,
		Signal:        strategy.Signal{Entry: 100},
		Samples:       0,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RiskFraction != bs.SmallSampleFraction {
		t.Fatalf("fraction = %.4f, want %.4f", result.RiskFraction, bs.SmallSampleFraction)
	}
}

func TestBayesianSizerFewSamples(t *testing.T) {
	bs := DefaultBayesianSizer()
	result, err := bs.Size(BayesianSizeRequest{
		AccountEquity: 10000,
		Signal:        strategy.Signal{Entry: 100},
		WinRate:       0.80, // looks great but only 5 trades
		AvgWin:        2.0,
		AvgLoss:       1.0,
		Samples:       5,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// With only 5 samples, sampleConfidence = 5/30 ≈ 0.17, should yield smaller fraction
	// than full-sample Kelly would produce
	if result.RiskFraction > 0.03 {
		t.Fatalf("fraction = %.4f, want small due to few samples", result.RiskFraction)
	}
}

// ── GlobalRiskGuard tests ────────────────────────────────────────

func TestGlobalGuardAllowsNormal(t *testing.T) {
	g := NewGlobalRiskGuard(DefaultGlobalRiskConfig())
	snap := GlobalSnapshot{
		TotalEquity: 100000,
		Positions: []Position{
			{Symbol: "BTC-USDT-SWAP", Direction: strategy.DirectionLong, Notional: 5000},
		},
	}
	req := OrderRequest{
		Symbol:    "ETH-USDT-SWAP",
		Direction: strategy.DirectionLong,
		Notional:  3000,
	}
	d := g.Evaluate(req, snap)
	if !d.Allowed {
		t.Fatalf("expected allow, got: %s", d.Reason)
	}
}

func TestGlobalGuardRejectsExposure(t *testing.T) {
	g := NewGlobalRiskGuard(DefaultGlobalRiskConfig())
	snap := GlobalSnapshot{
		TotalEquity: 10000,
		Positions: []Position{
			{Symbol: "BTC-USDT-SWAP", Direction: strategy.DirectionLong, Notional: 3000},
			{Symbol: "ETH-USDT-SWAP", Direction: strategy.DirectionLong, Notional: 1500},
		},
	}
	req := OrderRequest{
		Symbol:    "SOL-USDT-SWAP",
		Direction: strategy.DirectionLong,
		Notional:  1000,
	}
	// totalExposure = 3000+1500+1000 = 5500, 55% > 50%
	d := g.Evaluate(req, snap)
	if d.Allowed {
		t.Fatal("expected reject: global exposure > 50%")
	}
	if d.Layer != "global" {
		t.Fatalf("layer = %s, want global", d.Layer)
	}
}

func TestGlobalGuardRejectsSymbolConcentration(t *testing.T) {
	g := NewGlobalRiskGuard(DefaultGlobalRiskConfig())
	snap := GlobalSnapshot{
		TotalEquity: 10000,
		Positions: []Position{
			// Two accounts both holding BTC
			{Symbol: "BTC-USDT-SWAP", Direction: strategy.DirectionLong, Notional: 1000},
		},
	}
	req := OrderRequest{
		Symbol:    "BTC-USDT-SWAP",
		Direction: strategy.DirectionLong,
		Notional:  600,
	}
	// BTC total = 1000+600 = 1600, 16% > 15%
	d := g.Evaluate(req, snap)
	if d.Allowed {
		t.Fatal("expected reject: symbol exposure > 15%")
	}
}

func TestGlobalGuardRejectsDailyLoss(t *testing.T) {
	g := NewGlobalRiskGuard(DefaultGlobalRiskConfig())
	snap := GlobalSnapshot{
		TotalEquity: 10000,
		DailyPnL: map[string]float64{
			"account-1": -300,
			"account-2": -250,
		},
	}
	req := OrderRequest{
		Symbol:    "BTC-USDT-SWAP",
		Direction: strategy.DirectionLong,
		Notional:  100,
	}
	// dailyLoss = 550, 5.5% > 5%
	d := g.Evaluate(req, snap)
	if d.Allowed {
		t.Fatal("expected reject: global daily loss > 5%")
	}
}

func TestGlobalGuardRejectsSameDirection(t *testing.T) {
	g := NewGlobalRiskGuard(DefaultGlobalRiskConfig())
	snap := GlobalSnapshot{
		TotalEquity: 10000,
		Positions: []Position{
			{Symbol: "BTC-USDT-SWAP", Direction: strategy.DirectionLong, Notional: 1500},
			{Symbol: "ETH-USDT-SWAP", Direction: strategy.DirectionLong, Notional: 1000},
		},
	}
	req := OrderRequest{
		Symbol:    "SOL-USDT-SWAP",
		Direction: strategy.DirectionLong,
		Notional:  600,
	}
	// longExposure = 1500+1000+600 = 3100, 31% > 30%
	d := g.Evaluate(req, snap)
	if d.Allowed {
		t.Fatal("expected reject: same direction > 30%")
	}
}

func TestBayesianSizerFullSamples(t *testing.T) {
	bs := DefaultBayesianSizer()
	result, err := bs.Size(BayesianSizeRequest{
		AccountEquity: 10000,
		Signal:        strategy.Signal{Entry: 100},
		WinRate:       0.60,
		AvgWin:        2.0,
		AvgLoss:       1.0,
		Samples:       100,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// With 100 samples and 60% win rate, should have reasonable Kelly
	if result.RiskFraction < 0.005 || result.RiskFraction > 0.05 {
		t.Fatalf("fraction = %.4f, out of expected range", result.RiskFraction)
	}
	if result.KellyFraction <= 0 {
		t.Fatalf("kelly = %.4f, want positive", result.KellyFraction)
	}
}
