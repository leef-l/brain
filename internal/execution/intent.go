package execution

// OrderIntent describes a trading instruction emitted by brain-trader.
// It intentionally stays compact and serializable so it can cross
// process boundaries without additional dependencies.
type OrderIntent struct {
	ID          string `json:"id"`
	Symbol      string `json:"symbol"`
	Side        string `json:"side"`
	PosSide     string `json:"pos_side"`
	OrderType   string `json:"order_type"`
	Leverage    int    `json:"leverage"`
	Quantity    string `json:"quantity"`
	Price       string `json:"price,omitempty"`
	StopLoss    string `json:"stop_loss,omitempty"`
	TakeProfit  string `json:"take_profit,omitempty"`
	TimeInForce string `json:"time_in_force,omitempty"`
	ClientOrdID string `json:"client_ord_id,omitempty"`
	Timestamp   int64  `json:"timestamp"`
}

// ExecutionResult reports the outcome of a single order execution attempt.
type ExecutionResult struct {
	OrderID     string  `json:"order_id,omitempty"`
	ClientOrdID string  `json:"client_ord_id,omitempty"`
	Status      string  `json:"status"`
	FillPrice   float64 `json:"fill_price,omitempty"`
	FillQty     string  `json:"fill_qty,omitempty"`
	Fee         float64 `json:"fee,omitempty"`
	Timestamp   int64   `json:"timestamp"`
	Error       string  `json:"error,omitempty"`
}

const (
	OrderSideBuy  = "buy"
	OrderSideSell = "sell"

	PosSideLong  = "long"
	PosSideShort = "short"

	OrderTypeMarket      = "market"
	OrderTypeLimit       = "limit"
	OrderTypeStopLoss    = "stop_loss"
	OrderTypeTakeProfit  = "take_profit"
	OrderTypeStopMarket  = "stop_market"
	OrderTypeStopLimit   = "stop_limit"
	OrderTypeUnknown     = "unknown"
	OrderStatusFilled    = "filled"
	OrderStatusPartial   = "partially_filled"
	OrderStatusRejected  = "rejected"
	OrderStatusCancelled = "cancelled"
	OrderStatusAccepted  = "accepted"
	OrderStatusOpen      = "open"
	OrderStatusTriggered = "triggered"
)
