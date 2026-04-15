// Package exchange defines the venue-agnostic trading interface.
// Concrete implementations (OKX, IBKR, CTP, Paper) live behind this
// abstraction so the quant brain's core logic stays market-neutral.
package exchange

import (
	"context"
	"time"
)

// Capabilities describes what a specific exchange/market supports.
// The quant brain uses this to filter signals and adapt risk parameters.
type Capabilities struct {
	CanShort         bool    // 能否做空
	MaxLeverage      int     // 最大杠杆（无杠杆 = 1）
	HasFundingRate   bool    // 是否有资金费率（加密永续合约）
	HasOrderBook     bool    // 是否有实时盘口
	MinOrderSize     float64 // 最小下单量
	TickSize         float64 // 最小价格变动
	SettlementDays   int     // 结算天数（加密=0, A股=1, 美股=2）
	TradingHours     []TradingSession
	BaseCurrency     string // 结算币种（USDT, CNY, USD, HKD）
	CrossAssetAnchor string // 跨资产锚定（加密=BTC, A股=000300, 美股=SPY）
}

// TradingSession describes a continuous trading window.
type TradingSession struct {
	Open  TimeOfDay
	Close TimeOfDay
	Zone  string // IANA timezone (e.g. "Asia/Shanghai", "America/New_York")
}

// TimeOfDay is hour:minute in local time.
type TimeOfDay struct {
	Hour   int
	Minute int
}

// Is24x7 returns true if the exchange trades around the clock.
func (c Capabilities) Is24x7() bool {
	return len(c.TradingHours) == 0
}

// BalanceInfo holds account balance from the exchange.
type BalanceInfo struct {
	Equity       float64 // 总权益
	Available    float64 // 可用余额
	Margin       float64 // 已用保证金
	UnrealizedPL float64 // 未实现盈亏
	Currency     string  // 币种
}

// PositionInfo holds a single position from the exchange.
type PositionInfo struct {
	Symbol       string
	Side         string  // "long" / "short"
	Quantity     float64
	AvgPrice     float64
	MarkPrice    float64
	UnrealizedPL float64
	Leverage     int
	UpdatedAt    time.Time
}

// OrderResult is the exchange's response to an order placement.
type OrderResult struct {
	OrderID   string
	Status    string  // "filled", "open", "rejected", etc.
	FillPrice float64
	FillQty   float64
	Fee       float64
	Error     string
	Timestamp time.Time
}

// PlaceOrderParams is the venue-neutral order request.
type PlaceOrderParams struct {
	Symbol      string
	Side        string  // "buy" / "sell"
	PosSide     string  // "long" / "short" / "" (for spot/stock)
	Type        string  // "market" / "limit"
	Quantity    float64
	Price       float64 // 0 for market orders
	StopLoss    float64
	TakeProfit  float64
	Leverage    int
	TimeInForce string // "GTC", "IOC", etc.
	ClientID    string // client-assigned ID
}

// Exchange is the venue-agnostic interface for trading operations.
// Each concrete exchange (OKX, IBKR, Paper, etc.) implements this.
type Exchange interface {
	// Name returns the exchange identifier (e.g. "okx", "ibkr", "paper").
	Name() string

	// Capabilities returns what this exchange supports.
	Capabilities() Capabilities

	// QueryBalance returns the current account balance.
	QueryBalance(ctx context.Context) (BalanceInfo, error)

	// QueryPositions returns all open positions.
	QueryPositions(ctx context.Context) ([]PositionInfo, error)

	// PlaceOrder submits an order to the exchange.
	PlaceOrder(ctx context.Context, params PlaceOrderParams) (OrderResult, error)

	// CancelOrder cancels an open order by ID.
	CancelOrder(ctx context.Context, symbol, orderID string) error

	// IsOpen returns whether the exchange is currently in a trading session.
	IsOpen() bool
}

// TickFeeder is an optional interface for exchanges that process price ticks
// to trigger stop-loss / take-profit on simulated orders (e.g. PaperExchange).
type TickFeeder interface {
	ProcessPriceTick(ctx context.Context, symbol string, price float64) ([]OrderResult, error)
}
