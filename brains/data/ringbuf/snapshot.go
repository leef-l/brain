package ringbuf

// MarketSnapshot -- Quant Brain 消费的数据单元
type MarketSnapshot struct {
	// Header
	SeqNum    uint64 // 递增序号
	InstID    string // 品种 ID
	Timestamp int64  // 数据时间戳（毫秒）

	// 价格
	CurrentPrice float64
	BidPrice     float64
	AskPrice     float64
	FundingRate  float64
	OpenInterest float64
	Volume24h    float64

	// 微观结构指标
	OrderBookImbalance float64 // [-1, 1]
	Spread             float64
	TradeFlowToxicity  float64 // [0, 1]
	BigBuyRatio        float64
	BigSellRatio       float64 // v2
	TradeDensityRatio  float64
	BuySellRatio       float64 // v2

	// 特征向量
	FeatureVector [192]float64

	// v2: 元信息 — 便捷读取特征向量中的关键维度
	MLSource      string  // 特征 [176:192] 来源: "fallback" / "onnx_v1" / ...
	MLReady       bool    // ML 模型是否在线
	MarketRegime  string  // 当前主导市场状态: "trend"/"range"/"breakout"/"panic"
	AnomalyLevel  float64 // 综合异常分 [0, 1] — 来自 [187]
	VolPercentile float64 // 波动率百分位 [0, 1] — 来自 [182]
}
