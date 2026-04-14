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
	TradeDensityRatio  float64

	// 特征向量
	FeatureVector [192]float64
}
