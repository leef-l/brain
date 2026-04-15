package strategy

// tfIndex maps timeframe strings to their index in the 5-TF layout.
var tfIndex = map[string]int{
	"1m": 0, "5m": 1, "15m": 2, "1H": 3, "4H": 4,
}

// LiveFeatureView implements FeatureView by reading from a 192-dim
// feature vector. All methods are O(1) index lookups — no computation.
type LiveFeatureView struct {
	vec     [192]float64
	symbol  string
	price   float64
	mlReady bool
}

// NewLiveFeatureView creates a FeatureView from a snapshot's fields.
func NewLiveFeatureView(vec [192]float64, symbol string, price float64, mlReady bool) *LiveFeatureView {
	return &LiveFeatureView{
		vec:     vec,
		symbol:  symbol,
		price:   price,
		mlReady: mlReady,
	}
}

// ── Price [0:60] — 5 TF × 12 dims ──────────────────────────────

func (v *LiveFeatureView) EMADeviation(tf string, period int) float64 {
	idx, ok := tfIndex[tf]
	if !ok {
		return 0
	}
	base := idx * 12
	switch period {
	case 9:
		return v.vec[base+0]
	case 21:
		return v.vec[base+1]
	case 55:
		return v.vec[base+2]
	}
	return 0
}

func (v *LiveFeatureView) EMACross(tf string) float64 {
	idx, ok := tfIndex[tf]
	if !ok {
		return 0
	}
	return v.vec[idx*12+3]
}

func (v *LiveFeatureView) RSI(tf string) float64 {
	idx, ok := tfIndex[tf]
	if !ok {
		return 0
	}
	return v.vec[idx*12+4]
}

func (v *LiveFeatureView) MACDHistogram(tf string) float64 {
	idx, ok := tfIndex[tf]
	if !ok {
		return 0
	}
	return v.vec[idx*12+5]
}

func (v *LiveFeatureView) BBPosition(tf string) float64 {
	idx, ok := tfIndex[tf]
	if !ok {
		return 0
	}
	return v.vec[idx*12+6]
}

func (v *LiveFeatureView) ATRRatio(tf string) float64 {
	idx, ok := tfIndex[tf]
	if !ok {
		return 0
	}
	return v.vec[idx*12+7]
}

func (v *LiveFeatureView) PriceChange(tf string, bars int) float64 {
	idx, ok := tfIndex[tf]
	if !ok {
		return 0
	}
	base := idx * 12
	switch bars {
	case 5:
		return v.vec[base+8]
	case 20:
		return v.vec[base+9]
	}
	return 0
}

func (v *LiveFeatureView) Volatility(tf string, bars int) float64 {
	idx, ok := tfIndex[tf]
	if !ok {
		return 0
	}
	base := idx * 12
	switch bars {
	case 20:
		return v.vec[base+10]
	}
	// Also available in momentum section for bars 5 and 20
	mBase := 130 + idx*6
	switch bars {
	case 5:
		return v.vec[mBase+3]
	}
	return 0
}

func (v *LiveFeatureView) ADX(tf string) float64 {
	idx, ok := tfIndex[tf]
	if !ok {
		return 0
	}
	return v.vec[idx*12+11]
}

// ── Volume [60:100] — 5 TF × 8 dims ────────────────────────────

func (v *LiveFeatureView) VolumeRatio(tf string) float64 {
	idx, ok := tfIndex[tf]
	if !ok {
		return 0
	}
	return v.vec[60+idx*8+0]
}

func (v *LiveFeatureView) OBVSlope(tf string) float64 {
	idx, ok := tfIndex[tf]
	if !ok {
		return 0
	}
	return v.vec[60+idx*8+1]
}

func (v *LiveFeatureView) VolumePriceCorr(tf string) float64 {
	idx, ok := tfIndex[tf]
	if !ok {
		return 0
	}
	return v.vec[60+idx*8+2]
}

func (v *LiveFeatureView) VolumeBreakout(tf string) bool {
	idx, ok := tfIndex[tf]
	if !ok {
		return false
	}
	return v.vec[60+idx*8+3] > 0.5
}

// ── Microstructure [100:130] ─────────────────────────────────────

func (v *LiveFeatureView) OrderBookImbalance() float64 { return v.vec[100] }
func (v *LiveFeatureView) Spread() float64             { return v.vec[101] }
func (v *LiveFeatureView) TradeFlowToxicity() float64  { return v.vec[110] }
func (v *LiveFeatureView) BigBuyRatio() float64        { return v.vec[111] }
func (v *LiveFeatureView) BigSellRatio() float64       { return v.vec[112] }
func (v *LiveFeatureView) TradeDensityRatio() float64  { return v.vec[113] }
func (v *LiveFeatureView) BuySellRatio() float64       { return v.vec[114] }
func (v *LiveFeatureView) FundingRate() float64        { return v.vec[120] }

// ── Momentum [130:160] — 5 TF × 6 dims ──────────────────────────

func (v *LiveFeatureView) Momentum(tf string, bars int) float64 {
	idx, ok := tfIndex[tf]
	if !ok {
		return 0
	}
	base := 130 + idx*6
	switch bars {
	case 1:
		return v.vec[base+0]
	case 3:
		return v.vec[base+1]
	case 10:
		return v.vec[base+2]
	}
	return 0
}

func (v *LiveFeatureView) VolatilityRatio(tf string) float64 {
	idx, ok := tfIndex[tf]
	if !ok {
		return 0
	}
	return v.vec[130+idx*6+5]
}

// ── Cross-asset [160:176] ────────────────────────────────────────

func (v *LiveFeatureView) BTCExcessReturn() float64 { return v.vec[160] }
func (v *LiveFeatureView) BTCMomentum() float64     { return v.vec[161] }
func (v *LiveFeatureView) ETHMomentum() float64     { return v.vec[165] }
func (v *LiveFeatureView) BTCCorrelation() float64  { return v.vec[167] }
func (v *LiveFeatureView) ETHCorrelation() float64  { return v.vec[168] }

// ── ML-enhanced / fallback [176:192] ─────────────────────────────

func (v *LiveFeatureView) MarketRegime() MarketRegimeProb {
	return MarketRegimeProb{
		Trend:    v.vec[176],
		Range:    v.vec[177],
		Breakout: v.vec[178],
		Panic:    v.vec[179],
	}
}

func (v *LiveFeatureView) VolPrediction() VolPrediction {
	return VolPrediction{
		Vol1H:         v.vec[180],
		Vol4H:         v.vec[181],
		VolPercentile: v.vec[182],
		VolDirection:  v.vec[183],
	}
}

func (v *LiveFeatureView) AnomalyScore() AnomalyScore {
	return AnomalyScore{
		Price:     v.vec[184],
		Volume:    v.vec[185],
		OrderBook: v.vec[186],
		Combined:  v.vec[187],
	}
}

// ── Meta ─────────────────────────────────────────────────────────

func (v *LiveFeatureView) MLReady() bool         { return v.mlReady }
func (v *LiveFeatureView) Symbol() string         { return v.symbol }
func (v *LiveFeatureView) CurrentPrice() float64  { return v.price }
func (v *LiveFeatureView) RawVector() []float64 {
	out := make([]float64, 192)
	copy(out, v.vec[:])
	return out
}
