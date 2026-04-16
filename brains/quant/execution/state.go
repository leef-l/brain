package execution

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// snowflakeGen generates Twitter-style snowflake IDs.
// Layout: 41-bit timestamp (ms) | 10-bit machine/account | 12-bit sequence.
// Epoch: 2024-01-01 00:00:00 UTC.
type snowflakeGen struct {
	machineID int64
	sequence  atomic.Int64
	lastMs    atomic.Int64
}

const snowflakeEpoch = 1704067200000 // 2024-01-01 UTC in millis

func newSnowflakeGen(machineID int64) *snowflakeGen {
	return &snowflakeGen{machineID: machineID & 0x3FF} // 10 bits
}

func (g *snowflakeGen) Next() int64 {
	now := time.Now().UnixMilli() - snowflakeEpoch
	last := g.lastMs.Load()
	if now == last {
		seq := g.sequence.Add(1) & 0xFFF // 12 bits
		if seq == 0 {
			// Sequence exhausted for this ms, spin until next ms.
			for now <= last {
				now = time.Now().UnixMilli() - snowflakeEpoch
			}
		}
		g.lastMs.Store(now)
		return (now << 22) | (g.machineID << 12) | seq
	}
	g.lastMs.Store(now)
	g.sequence.Store(0)
	return (now << 22) | (g.machineID << 12)
}

// hashAccountID produces a stable 10-bit machine ID from account name.
func hashAccountID(accountID string) int64 {
	var h int64
	for _, c := range accountID {
		h = h*31 + int64(c)
	}
	if h < 0 {
		h = -h
	}
	return h & 0x3FF
}

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
	MarkPrice     float64 `json:"mark_price"`
	RealizedPnL   float64 `json:"realized_pnl"`
	UnrealizedPnL float64 `json:"unrealized_pnl"`
	Margin        float64 `json:"margin"`
	Leverage      int     `json:"leverage"`
	UpdatedAt     int64   `json:"updated_at"`
}

// StateSnapshot is a read-only view of the in-memory execution state.
type StateSnapshot struct {
	Orders                []OrderRecord `json:"orders"`
	Positions             []Position    `json:"positions"`
	CumulativeRealizedPnL float64       `json:"cumulative_realized_pnl"`
	CumulativeFees        float64       `json:"cumulative_fees"`
}

// MemoryState keeps order and position state in memory.
type MemoryState struct {
	mu        sync.RWMutex
	idPrefix  string // account-specific prefix for human-readable order IDs
	nextID    int64  // legacy counter (only used for SetNextID/NextID persistence compat)
	snowflake *snowflakeGen // snowflake ID generator for globally unique order IDs
	orders    map[string]*OrderRecord
	clientOrd map[string]string
	positions map[string]*Position

	// Cumulative realized PnL across all closed positions. When a position
	// is fully closed, its RealizedPnL is added here before deletion.
	// This ensures QueryBalance reflects profits/losses from past trades.
	cumulativeRealizedPnL float64
	// Cumulative fees paid across all fills.
	cumulativeFees float64
}

// NewMemoryState creates a new MemoryState with default snowflake generator.
func NewMemoryState() *MemoryState {
	return &MemoryState{
		snowflake: newSnowflakeGen(0),
		orders:    make(map[string]*OrderRecord),
		clientOrd: make(map[string]string),
		positions: make(map[string]*Position),
	}
}

// NewMemoryStateWithPrefix creates a MemoryState with an account-specific ID
// prefix and a snowflake generator seeded from the account ID. This ensures
// order IDs are globally unique across multiple paper exchanges.
func NewMemoryStateWithPrefix(prefix string) *MemoryState {
	return &MemoryState{
		idPrefix:  prefix,
		snowflake: newSnowflakeGen(hashAccountID(prefix)),
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
		Orders:                orders,
		Positions:             positions,
		CumulativeRealizedPnL: s.cumulativeRealizedPnL,
		CumulativeFees:        s.cumulativeFees,
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

// UpdateStopLossPrice finds the open stop_loss order for a symbol/posSide
// and updates its trigger price. Returns true if an order was updated.
// Used by the trailing stop mechanism.
func (s *MemoryState) UpdateStopLossPrice(now int64, symbol, posSide string, newSL float64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, record := range s.orders {
		if record.Status != OrderStatusOpen && record.Status != OrderStatusAccepted {
			continue
		}
		if record.Intent.OrderType != OrderTypeStopLoss {
			continue
		}
		if !strings.EqualFold(record.Intent.Symbol, symbol) || !strings.EqualFold(record.Intent.PosSide, posSide) {
			continue
		}
		record.Intent.StopLoss = strconv.FormatFloat(newSL, 'f', -1, 64)
		record.UpdatedAt = now
		return true
	}
	return false
}

// UpdateTakeProfitPrice finds the open take_profit order for a symbol/posSide
// and updates its trigger price. Used by trailing take-profit.
func (s *MemoryState) UpdateTakeProfitPrice(now int64, symbol, posSide string, newTP float64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, record := range s.orders {
		if record.Status != OrderStatusOpen && record.Status != OrderStatusAccepted {
			continue
		}
		if record.Intent.OrderType != OrderTypeTakeProfit {
			continue
		}
		if !strings.EqualFold(record.Intent.Symbol, symbol) || !strings.EqualFold(record.Intent.PosSide, posSide) {
			continue
		}
		record.Intent.TakeProfit = strconv.FormatFloat(newTP, 'f', -1, 64)
		record.UpdatedAt = now
		return true
	}
	return false
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
	s.cumulativeFees += fee

	if pos.Quantity == 0 {
		// Preserve realized PnL before deleting the position.
		s.cumulativeRealizedPnL += pos.RealizedPnL
		delete(s.positions, key)
		return Position{Symbol: intent.Symbol, PosSide: intent.PosSide, RealizedPnL: pos.RealizedPnL, UpdatedAt: now}, nil
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
	id := s.snowflake.Next()
	s.nextID++ // keep legacy counter in sync for NextID() persistence
	if s.idPrefix != "" {
		return fmt.Sprintf("p-%s-%d", s.idPrefix, id)
	}
	return fmt.Sprintf("p-%d", id)
}

// UpdateMarkPrice updates a position's mark price, unrealized PnL, and margin.
func (s *MemoryState) UpdateMarkPrice(now int64, symbol, posSide string, markPrice float64, leverage int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := positionKey(symbol, posSide)
	pos, ok := s.positions[key]
	if !ok || pos.Quantity == 0 {
		return
	}

	pos.MarkPrice = markPrice
	switch pos.PosSide {
	case PosSideLong:
		pos.UnrealizedPnL = pos.Quantity * (markPrice - pos.AvgPrice)
	case PosSideShort:
		pos.UnrealizedPnL = pos.Quantity * (pos.AvgPrice - markPrice)
	}

	lev := leverage
	if lev <= 0 {
		lev = pos.Leverage
	}
	if lev <= 0 {
		lev = 1
	}
	pos.Leverage = lev
	pos.Margin = pos.Quantity * markPrice / float64(lev)
	pos.UpdatedAt = now
}

// RestorePositions loads positions from persistence into memory.
// Existing positions are cleared first.
func (s *MemoryState) RestorePositions(positions []Position) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Clear existing.
	for k := range s.positions {
		delete(s.positions, k)
	}

	for _, p := range positions {
		if p.Quantity == 0 {
			continue
		}
		cp := p // copy
		key := positionKey(p.Symbol, p.PosSide)
		s.positions[key] = &cp
	}
}

// RestoreOrders loads open orders from persistence into memory.
// Existing orders and client order mappings are cleared first.
// Only open/accepted/triggered orders should be passed.
func (s *MemoryState) RestoreOrders(orders []OrderRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Clear existing orders and client order mappings.
	for k := range s.orders {
		delete(s.orders, k)
	}
	for k := range s.clientOrd {
		delete(s.clientOrd, k)
	}

	for _, o := range orders {
		cp := o // copy
		s.orders[o.Intent.ID] = &cp
		if o.Intent.ClientOrdID != "" {
			s.clientOrd[o.Intent.ClientOrdID] = o.Intent.ID
		}
	}
}

// SetNextID sets the order ID counter for resumption after restart.
// nextOrderIDLocked pre-increments, so pass the persisted nextID directly.
func (s *MemoryState) SetNextID(id int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id > s.nextID {
		s.nextID = id
	}
}

// NextID returns the current nextID counter value (for persistence).
func (s *MemoryState) NextID() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.nextID
}

// SetCumulativeRealizedPnL sets the cumulative realized PnL (for restoration from trade history).
func (s *MemoryState) SetCumulativeRealizedPnL(pnl float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cumulativeRealizedPnL = pnl
}

// SetCumulativeFees sets the cumulative fees (for restoration from trade history).
func (s *MemoryState) SetCumulativeFees(fees float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cumulativeFees = fees
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
