package execution

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// OrderRecord stores the intent plus the mutable execution state.
type OrderRecord struct {
	Intent    OrderIntent `json:"intent"`
	Status    string      `json:"status"`
	FillPrice float64     `json:"fill_price,omitempty"`
	FillQty   string      `json:"fill_qty,omitempty"`
	Fee       float64     `json:"fee,omitempty"`
	Error     string      `json:"error,omitempty"`
	CreatedAt int64       `json:"created_at"`
	UpdatedAt int64       `json:"updated_at"`
}

// Position represents a single symbol/posSide exposure in memory.
type Position struct {
	Symbol        string  `json:"symbol"`
	PosSide       string  `json:"pos_side"`
	Quantity      float64 `json:"quantity"`
	AvgPrice      float64 `json:"avg_price"`
	RealizedPnL   float64 `json:"realized_pnl"`
	UnrealizedPnL float64 `json:"unrealized_pnl"`
	UpdatedAt     int64   `json:"updated_at"`
}

// StateSnapshot is a read-only view of the in-memory execution state.
type StateSnapshot struct {
	Orders    []OrderRecord `json:"orders"`
	Positions []Position    `json:"positions"`
}

// MemoryState keeps order and position state in memory.
type MemoryState struct {
	mu        sync.RWMutex
	nextID    int64
	orders    map[string]*OrderRecord
	clientOrd map[string]string
	positions map[string]*Position
}

func NewMemoryState() *MemoryState {
	return &MemoryState{
		orders:    make(map[string]*OrderRecord),
		clientOrd: make(map[string]string),
		positions: make(map[string]*Position),
	}
}

func (s *MemoryState) Snapshot() StateSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	orders := make([]OrderRecord, 0, len(s.orders))
	for _, record := range s.orders {
		orders = append(orders, *cloneOrderRecord(record))
	}
	sort.Slice(orders, func(i, j int) bool {
		if orders[i].CreatedAt == orders[j].CreatedAt {
			return orders[i].Intent.ID < orders[j].Intent.ID
		}
		return orders[i].CreatedAt < orders[j].CreatedAt
	})

	positions := make([]Position, 0, len(s.positions))
	for _, pos := range s.positions {
		positions = append(positions, *clonePosition(pos))
	}
	sort.Slice(positions, func(i, j int) bool {
		if positions[i].Symbol == positions[j].Symbol {
			return positions[i].PosSide < positions[j].PosSide
		}
		return positions[i].Symbol < positions[j].Symbol
	})

	return StateSnapshot{
		Orders:    orders,
		Positions: positions,
	}
}

func (s *MemoryState) PutOrder(now int64, intent OrderIntent, status string) OrderRecord {
	s.mu.Lock()
	defer s.mu.Unlock()

	intent = normalizeIntent(intent)
	if intent.ID == "" {
		intent.ID = s.nextOrderIDLocked()
	}
	record := &OrderRecord{
		Intent:    intent,
		Status:    status,
		CreatedAt: now,
		UpdatedAt: now,
	}
	s.orders[intent.ID] = record
	if intent.ClientOrdID != "" {
		s.clientOrd[intent.ClientOrdID] = intent.ID
	}
	return *cloneOrderRecord(record)
}

func (s *MemoryState) UpdateOrder(now int64, orderID string, fn func(*OrderRecord) error) (OrderRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	record, ok := s.orders[orderID]
	if !ok {
		return OrderRecord{}, fmt.Errorf("order %q not found", orderID)
	}
	if err := fn(record); err != nil {
		return OrderRecord{}, err
	}
	record.UpdatedAt = now
	if record.Intent.ClientOrdID != "" {
		s.clientOrd[record.Intent.ClientOrdID] = record.Intent.ID
	}
	return *cloneOrderRecord(record), nil
}

func (s *MemoryState) OrderByID(orderID string) (OrderRecord, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	record, ok := s.orders[orderID]
	if !ok {
		return OrderRecord{}, false
	}
	return *cloneOrderRecord(record), true
}

func (s *MemoryState) OrderByClientOrdID(clientOrdID string) (OrderRecord, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	orderID, ok := s.clientOrd[clientOrdID]
	if !ok {
		return OrderRecord{}, false
	}
	record, ok := s.orders[orderID]
	if !ok {
		return OrderRecord{}, false
	}
	return *cloneOrderRecord(record), true
}

func (s *MemoryState) ListOpenOrders(symbol string) []OrderRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()

	orders := make([]OrderRecord, 0)
	for _, record := range s.orders {
		if symbol != "" && record.Intent.Symbol != symbol {
			continue
		}
		switch record.Status {
		case OrderStatusAccepted, OrderStatusOpen, OrderStatusTriggered:
			orders = append(orders, *cloneOrderRecord(record))
		}
	}
	sort.Slice(orders, func(i, j int) bool {
		if orders[i].CreatedAt == orders[j].CreatedAt {
			return orders[i].Intent.ID < orders[j].Intent.ID
		}
		return orders[i].CreatedAt < orders[j].CreatedAt
	})
	return orders
}

func (s *MemoryState) CancelOrder(now int64, orderID string) (OrderRecord, error) {
	return s.UpdateOrder(now, orderID, func(record *OrderRecord) error {
		if record.Status == OrderStatusFilled || record.Status == OrderStatusCancelled || record.Status == OrderStatusRejected {
			return nil
		}
		record.Status = OrderStatusCancelled
		return nil
	})
}

func (s *MemoryState) ApplyFill(now int64, intent OrderIntent, fillPrice float64, fillQty string, fee float64) (Position, error) {
	qty, err := parseQuantity(fillQty)
	if err != nil {
		return Position{}, err
	}
	signedQty, err := signedQuantity(intent, qty)
	if err != nil {
		return Position{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	key := positionKey(intent.Symbol, intent.PosSide)
	pos := s.positions[key]
	if pos == nil {
		pos = &Position{Symbol: intent.Symbol, PosSide: intent.PosSide}
		s.positions[key] = pos
	}

	newQty, newAvg, realized, err := updatePosition(intent.PosSide, pos.Quantity, pos.AvgPrice, signedQty, fillPrice)
	if err != nil {
		return Position{}, err
	}
	pos.Quantity = newQty
	pos.AvgPrice = newAvg
	pos.RealizedPnL += realized
	pos.UpdatedAt = now

	if pos.Quantity == 0 {
		delete(s.positions, key)
		return Position{Symbol: intent.Symbol, PosSide: intent.PosSide, UpdatedAt: now}, nil
	}

	return *clonePosition(pos), nil
}

func (s *MemoryState) upsertOrderFillLocked(now int64, orderID string, fillPrice float64, fillQty string, fee float64, status string, errText string) (OrderRecord, error) {
	record, ok := s.orders[orderID]
	if !ok {
		return OrderRecord{}, fmt.Errorf("order %q not found", orderID)
	}
	record.Status = status
	record.FillPrice = fillPrice
	record.FillQty = fillQty
	record.Fee = fee
	record.Error = errText
	record.UpdatedAt = now
	return *cloneOrderRecord(record), nil
}

func (s *MemoryState) markAcceptedLocked(now int64, orderID string) (OrderRecord, error) {
	record, ok := s.orders[orderID]
	if !ok {
		return OrderRecord{}, fmt.Errorf("order %q not found", orderID)
	}
	record.Status = OrderStatusAccepted
	record.UpdatedAt = now
	return *cloneOrderRecord(record), nil
}

func (s *MemoryState) nextOrderIDLocked() string {
	s.nextID++
	return fmt.Sprintf("paper-%d", s.nextID)
}

func positionKey(symbol, posSide string) string {
	return strings.ToLower(symbol) + "|" + strings.ToLower(posSide)
}

func normalizeIntent(intent OrderIntent) OrderIntent {
	intent.Symbol = strings.TrimSpace(intent.Symbol)
	intent.Side = strings.ToLower(strings.TrimSpace(intent.Side))
	intent.PosSide = strings.ToLower(strings.TrimSpace(intent.PosSide))
	intent.OrderType = strings.ToLower(strings.TrimSpace(intent.OrderType))
	intent.TimeInForce = strings.ToUpper(strings.TrimSpace(intent.TimeInForce))
	intent.Price = strings.TrimSpace(intent.Price)
	intent.StopLoss = strings.TrimSpace(intent.StopLoss)
	intent.TakeProfit = strings.TrimSpace(intent.TakeProfit)
	intent.Quantity = strings.TrimSpace(intent.Quantity)
	intent.ClientOrdID = strings.TrimSpace(intent.ClientOrdID)
	intent.ID = strings.TrimSpace(intent.ID)
	return intent
}

func cloneOrderRecord(in *OrderRecord) *OrderRecord {
	if in == nil {
		return nil
	}
	copy := *in
	return &copy
}

func clonePosition(in *Position) *Position {
	if in == nil {
		return nil
	}
	copy := *in
	return &copy
}

func parseQuantity(raw string) (float64, error) {
	qty, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		return 0, fmt.Errorf("parse quantity %q: %w", raw, err)
	}
	if qty <= 0 {
		return 0, fmt.Errorf("quantity must be positive: %q", raw)
	}
	return qty, nil
}

func signedQuantity(intent OrderIntent, qty float64) (float64, error) {
	switch intent.PosSide {
	case PosSideLong:
		switch intent.Side {
		case OrderSideBuy:
			return qty, nil
		case OrderSideSell:
			return -qty, nil
		}
	case PosSideShort:
		switch intent.Side {
		case OrderSideSell:
			return qty, nil
		case OrderSideBuy:
			return -qty, nil
		}
	}
	return 0, fmt.Errorf("unsupported side/pos_side combination: side=%q pos_side=%q", intent.Side, intent.PosSide)
}

func updatePosition(posSide string, currentQty, currentAvg, deltaQty, fillPrice float64) (float64, float64, float64, error) {
	if deltaQty == 0 {
		return currentQty, currentAvg, 0, nil
	}

	if deltaQty > 0 {
		nextQty := currentQty + deltaQty
		if nextQty == 0 {
			return 0, 0, 0, nil
		}
		weighted := currentAvg*currentQty + fillPrice*deltaQty
		return nextQty, weighted / nextQty, 0, nil
	}

	closing := abs(deltaQty)
	if closing > currentQty {
		return 0, 0, 0, fmt.Errorf("position flip is not supported in the paper state")
	}

	nextQty := currentQty - closing
	var realized float64
	switch posSide {
	case PosSideLong:
		realized = closing * (fillPrice - currentAvg)
	case PosSideShort:
		realized = closing * (currentAvg - fillPrice)
	default:
		return 0, 0, 0, fmt.Errorf("unknown pos side %q", posSide)
	}
	if nextQty == 0 {
		return 0, 0, realized, nil
	}
	return nextQty, currentAvg, realized, nil
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
