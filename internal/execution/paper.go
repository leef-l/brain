package execution

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// PriceProvider resolves the current mark price for a symbol.
type PriceProvider func(ctx context.Context, symbol string) (float64, bool)

// PaperOption configures the in-memory Paper backend.
type PaperOption func(*PaperBackend)

// PaperBackend simulates fills and keeps all state in memory.
type PaperBackend struct {
	priceProvider PriceProvider
	slippageBps   float64
	feeBps        float64
}

func NewPaperBackend(opts ...PaperOption) *PaperBackend {
	backend := &PaperBackend{
		slippageBps: 0,
		feeBps:      5,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(backend)
		}
	}
	return backend
}

func WithPaperPriceProvider(provider PriceProvider) PaperOption {
	return func(b *PaperBackend) {
		b.priceProvider = provider
	}
}

func WithPaperSlippageBps(bps float64) PaperOption {
	return func(b *PaperBackend) {
		b.slippageBps = math.Max(0, bps)
	}
}

func WithPaperFeeBps(bps float64) PaperOption {
	return func(b *PaperBackend) {
		b.feeBps = math.Max(0, bps)
	}
}

func (b *PaperBackend) Name() string { return "paper" }

func (b *PaperBackend) Execute(ctx context.Context, state *MemoryState, intent OrderIntent) (ExecutionResult, error) {
	now := nowUnix()
	intent = normalizeIntent(intent)
	if intent.Timestamp == 0 {
		intent.Timestamp = now
	}
	if err := validateIntent(intent); err != nil {
		return b.reject(state, now, intent, err.Error()), nil
	}

	price, hasPrice, err := b.resolvePrice(ctx, intent)
	if err != nil {
		return b.reject(state, now, intent, err.Error()), nil
	}
	if intent.OrderType == OrderTypeMarket && !hasPrice {
		return b.reject(state, now, intent, "market order requires a reference price"), nil
	}

	order := state.PutOrder(now, intent, OrderStatusAccepted)
	if order.Intent.ID == "" {
		return ExecutionResult{}, fmt.Errorf("paper state returned empty order id")
	}

	if fillable, fillPrice := b.shouldFill(intent, price, hasPrice); fillable {
		fillPrice = b.applySlippage(fillPrice, intent)
		fillQty := intent.Quantity
		fee := b.calcFee(fillPrice, fillQty)
		if _, err := state.ApplyFill(now, intent, fillPrice, fillQty, fee); err != nil {
			return b.reject(state, now, intent, err.Error()), nil
		}

		_, _ = state.UpdateOrder(now, order.Intent.ID, func(record *OrderRecord) error {
			record.Status = OrderStatusFilled
			record.FillPrice = fillPrice
			record.FillQty = fillQty
			record.Fee = fee
			return nil
		})

		return ExecutionResult{
			OrderID:     order.Intent.ID,
			ClientOrdID: intent.ClientOrdID,
			Status:      OrderStatusFilled,
			FillPrice:   fillPrice,
			FillQty:     fillQty,
			Fee:         fee,
			Timestamp:   now,
		}, nil
	}

	_, _ = state.UpdateOrder(now, order.Intent.ID, func(record *OrderRecord) error {
		record.Status = OrderStatusOpen
		return nil
	})

	return ExecutionResult{
		OrderID:     order.Intent.ID,
		ClientOrdID: intent.ClientOrdID,
		Status:      OrderStatusOpen,
		Timestamp:   now,
	}, nil
}

func (b *PaperBackend) ProcessPriceTick(ctx context.Context, state *MemoryState, symbol string, markPrice float64) ([]ExecutionResult, error) {
	now := nowUnix()
	_ = ctx

	openOrders := state.ListOpenOrders(symbol)
	results := make([]ExecutionResult, 0, len(openOrders))
	for _, order := range openOrders {
		fillable, fillPrice := b.shouldFill(order.Intent, markPrice, true)
		if !fillable {
			continue
		}

		fillPrice = b.applySlippage(fillPrice, order.Intent)
		fillQty := order.Intent.Quantity
		fee := b.calcFee(fillPrice, fillQty)
		if _, err := state.ApplyFill(now, order.Intent, fillPrice, fillQty, fee); err != nil {
			continue
		}
		_, _ = state.UpdateOrder(now, order.Intent.ID, func(record *OrderRecord) error {
			record.Status = OrderStatusFilled
			record.FillPrice = fillPrice
			record.FillQty = fillQty
			record.Fee = fee
			return nil
		})
		results = append(results, ExecutionResult{
			OrderID:     order.Intent.ID,
			ClientOrdID: order.Intent.ClientOrdID,
			Status:      OrderStatusFilled,
			FillPrice:   fillPrice,
			FillQty:     fillQty,
			Fee:         fee,
			Timestamp:   now,
		})
	}
	return results, nil
}

func (b *PaperBackend) shouldFill(intent OrderIntent, markPrice float64, hasPrice bool) (bool, float64) {
	if !hasPrice {
		return false, 0
	}

	switch intent.OrderType {
	case "", OrderTypeMarket:
		return true, markPrice
	case OrderTypeLimit:
		limit, err := strconv.ParseFloat(strings.TrimSpace(intent.Price), 64)
		if err != nil || limit <= 0 {
			return false, 0
		}
		switch intent.Side {
		case OrderSideBuy:
			return markPrice <= limit, limit
		case OrderSideSell:
			return markPrice >= limit, limit
		}
		return false, 0
	case OrderTypeStopLoss:
		trigger, err := strconv.ParseFloat(strings.TrimSpace(intent.StopLoss), 64)
		if err != nil || trigger <= 0 {
			return false, 0
		}
		return stopTriggered(intent, markPrice, trigger), markPrice
	case OrderTypeTakeProfit:
		trigger, err := strconv.ParseFloat(strings.TrimSpace(intent.TakeProfit), 64)
		if err != nil || trigger <= 0 {
			return false, 0
		}
		return takeProfitTriggered(intent, markPrice, trigger), markPrice
	default:
		return false, 0
	}
}

func stopTriggered(intent OrderIntent, markPrice, trigger float64) bool {
	switch intent.Side {
	case OrderSideBuy:
		return markPrice >= trigger
	case OrderSideSell:
		return markPrice <= trigger
	default:
		return false
	}
}

func takeProfitTriggered(intent OrderIntent, markPrice, trigger float64) bool {
	switch intent.Side {
	case OrderSideBuy:
		return markPrice <= trigger
	case OrderSideSell:
		return markPrice >= trigger
	default:
		return false
	}
}

func (b *PaperBackend) reject(state *MemoryState, now int64, intent OrderIntent, reason string) ExecutionResult {
	intent = normalizeIntent(intent)
	order := state.PutOrder(now, intent, OrderStatusRejected)
	_, _ = state.UpdateOrder(now, order.Intent.ID, func(record *OrderRecord) error {
		record.Status = OrderStatusRejected
		record.Error = reason
		return nil
	})
	return ExecutionResult{
		OrderID:     order.Intent.ID,
		ClientOrdID: intent.ClientOrdID,
		Status:      OrderStatusRejected,
		Timestamp:   now,
		Error:       reason,
	}
}

func (b *PaperBackend) resolvePrice(ctx context.Context, intent OrderIntent) (float64, bool, error) {
	if b.priceProvider != nil {
		if price, ok := b.priceProvider(ctx, intent.Symbol); ok {
			return price, true, nil
		}
	}

	switch intent.OrderType {
	case OrderTypeMarket, "", OrderTypeStopLoss, OrderTypeTakeProfit:
		if intent.Price == "" {
			return 0, false, nil
		}
		price, err := strconv.ParseFloat(strings.TrimSpace(intent.Price), 64)
		if err != nil || price <= 0 {
			return 0, false, fmt.Errorf("invalid reference price %q", intent.Price)
		}
		return price, true, nil
	case OrderTypeLimit, OrderTypeStopLimit:
		return 0, false, nil
	default:
		return 0, false, fmt.Errorf("unsupported order type %q", intent.OrderType)
	}
}

func (b *PaperBackend) applySlippage(price float64, intent OrderIntent) float64 {
	if b.slippageBps <= 0 {
		return price
	}
	ratio := b.slippageBps / 10000
	switch intent.Side {
	case OrderSideBuy:
		return price * (1 + ratio)
	case OrderSideSell:
		return price * (1 - ratio)
	default:
		return price
	}
}

func (b *PaperBackend) calcFee(price float64, qty string) float64 {
	q, err := strconv.ParseFloat(strings.TrimSpace(qty), 64)
	if err != nil || q <= 0 {
		return 0
	}
	return price * q * (b.feeBps / 10000)
}

func validateIntent(intent OrderIntent) error {
	if intent.Symbol == "" {
		return fmt.Errorf("symbol is required")
	}
	if intent.Side != OrderSideBuy && intent.Side != OrderSideSell {
		return fmt.Errorf("side must be %q or %q", OrderSideBuy, OrderSideSell)
	}
	if intent.PosSide != PosSideLong && intent.PosSide != PosSideShort {
		return fmt.Errorf("pos_side must be %q or %q", PosSideLong, PosSideShort)
	}
	if _, err := parseQuantity(intent.Quantity); err != nil {
		return err
	}
	switch intent.OrderType {
	case "", OrderTypeMarket, OrderTypeLimit, OrderTypeStopLoss, OrderTypeTakeProfit, OrderTypeStopMarket, OrderTypeStopLimit:
	default:
		return fmt.Errorf("unsupported order type %q", intent.OrderType)
	}
	return nil
}

func nowUnix() int64 {
	return time.Now().UnixMilli()
}
