package execution

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	coreexec "github.com/leef-l/brain/internal/execution"
	"github.com/leef-l/brain/internal/quantcontracts"
)

// AccountStatus keeps the quant-layer execution lifecycle explicit and small.
type AccountStatus string

const (
	AccountStatusActive     AccountStatus = "active"
	AccountStatusPaused     AccountStatus = "paused"
	AccountStatusRecovering AccountStatus = "recovering"
	AccountStatusDisabled   AccountStatus = "disabled"
)

// AccountSnapshot is the quant-facing view of a single execution account.
type AccountSnapshot struct {
	AccountID        string        `json:"account_id"`
	Status           AccountStatus `json:"status"`
	Reason           string        `json:"reason,omitempty"`
	Backend          string        `json:"backend,omitempty"`
	OpenOrders       int           `json:"open_orders,omitempty"`
	PositionCount    int           `json:"position_count,omitempty"`
	LastOrderID      string        `json:"last_order_id,omitempty"`
	LastExecStatus   string        `json:"last_exec_status,omitempty"`
	LastReconcileAt  int64         `json:"last_reconcile_at,omitempty"`
	LastActivityAt   int64         `json:"last_activity_at,omitempty"`
	LastRecoveredAt  int64         `json:"last_recovered_at,omitempty"`
	LastError        string        `json:"last_error,omitempty"`
	UnderlyingOrders int           `json:"underlying_orders,omitempty"`
	UnderlyingPos    int           `json:"underlying_positions,omitempty"`
	UpdatedAt        int64         `json:"updated_at,omitempty"`
}

// ReconcileResult summarizes one account recovery pass.
type ReconcileResult struct {
	AccountID     string        `json:"account_id"`
	Status        AccountStatus `json:"status"`
	OpenOrders    int           `json:"open_orders,omitempty"`
	PositionCount int           `json:"position_count,omitempty"`
	RecoveredAt   int64         `json:"recovered_at,omitempty"`
	Summary       string        `json:"summary,omitempty"`
	Error         string        `json:"error,omitempty"`
}

// AccountState owns the mutable state for a single account.
type AccountState struct {
	mu      sync.RWMutex
	id      string
	core    *coreexec.Client
	backend string

	status         AccountStatus
	reason         string
	lastOrderID    string
	lastExecStatus string
	lastError      string
	lastActivityAt int64
	lastReconAt    int64
	lastRecovered  int64
	updatedAt      int64
}

// NewAccountState binds a quant account to a concrete execution client.
func NewAccountState(accountID string, client *coreexec.Client) *AccountState {
	return &AccountState{
		id:     strings.TrimSpace(accountID),
		core:   client,
		status: AccountStatusActive,
	}
}

func (s *AccountState) SetBackendLabel(label string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.backend = strings.TrimSpace(label)
	s.updatedAt = nowMillis()
}

func (s *AccountState) ID() string {
	if s == nil {
		return ""
	}
	return s.id
}

func (s *AccountState) Client() *coreexec.Client {
	if s == nil {
		return nil
	}
	return s.core
}

func (s *AccountState) Status() AccountStatus {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.status
}

func (s *AccountState) MarkActive(reason string) {
	s.setStatus(AccountStatusActive, reason)
}

func (s *AccountState) MarkPaused(reason string) {
	s.setStatus(AccountStatusPaused, reason)
}

func (s *AccountState) MarkRecovering(reason string) {
	s.setStatus(AccountStatusRecovering, reason)
}

func (s *AccountState) MarkDisabled(reason string) {
	s.setStatus(AccountStatusDisabled, reason)
}

func (s *AccountState) RecordExecutionResult(result coreexec.ExecutionResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastOrderID = strings.TrimSpace(result.OrderID)
	s.lastExecStatus = strings.TrimSpace(result.Status)
	s.lastError = strings.TrimSpace(result.Error)
	s.lastActivityAt = result.Timestamp
	s.updatedAt = nowMillis()
}

func (s *AccountState) RecordReconcile() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := nowMillis()
	s.lastReconAt = now
	s.lastRecovered = now
	s.updatedAt = now
	if s.status == AccountStatusRecovering {
		s.status = AccountStatusActive
	}
}

func (s *AccountState) Snapshot() AccountSnapshot {
	if s == nil {
		return AccountSnapshot{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	var (
		openOrders    int
		totalOrders   int
		positionCount int
	)
	if s.core != nil {
		snap := s.core.Snapshot()
		totalOrders = len(snap.Orders)
		for _, order := range snap.Orders {
			switch order.Status {
			case coreexec.OrderStatusAccepted, coreexec.OrderStatusOpen, coreexec.OrderStatusTriggered:
				openOrders++
			}
		}
		positionCount = len(snap.Positions)
	}

	return AccountSnapshot{
		AccountID:        s.id,
		Status:           s.status,
		Reason:           s.reason,
		Backend:          s.backend,
		OpenOrders:       openOrders,
		PositionCount:    positionCount,
		LastOrderID:      s.lastOrderID,
		LastExecStatus:   s.lastExecStatus,
		LastReconcileAt:  s.lastReconAt,
		LastActivityAt:   s.lastActivityAt,
		LastRecoveredAt:  s.lastRecovered,
		LastError:        s.lastError,
		UnderlyingOrders: totalOrders,
		UnderlyingPos:    positionCount,
		UpdatedAt:        s.updatedAt,
	}
}

func (s *AccountState) setStatus(status AccountStatus, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = status
	s.reason = strings.TrimSpace(reason)
	s.updatedAt = nowMillis()
}

func nowMillis() int64 { return time.Now().UTC().UnixMilli() }

// AccountBook stores the account states owned by Quant execution.
type AccountBook struct {
	mu       sync.RWMutex
	accounts map[string]*AccountState
	order    []string
}

func NewAccountBook() *AccountBook {
	return &AccountBook{
		accounts: make(map[string]*AccountState),
	}
}

func (b *AccountBook) Upsert(state *AccountState) {
	if b == nil || state == nil || strings.TrimSpace(state.ID()) == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.accounts[state.ID()]; !ok {
		b.order = append(b.order, state.ID())
	}
	b.accounts[state.ID()] = state
}

func (b *AccountBook) Get(accountID string) (*AccountState, bool) {
	if b == nil {
		return nil, false
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	state, ok := b.accounts[strings.TrimSpace(accountID)]
	return state, ok
}

func (b *AccountBook) List() []AccountSnapshot {
	if b == nil {
		return nil
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]AccountSnapshot, 0, len(b.accounts))
	for _, id := range b.order {
		if state, ok := b.accounts[id]; ok {
			out = append(out, state.Snapshot())
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].AccountID < out[j].AccountID
	})
	return out
}

func (b *AccountBook) ensure(accountID string, factory func() *AccountState) *AccountState {
	if b == nil {
		return nil
	}
	id := strings.TrimSpace(accountID)
	if id == "" {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if state, ok := b.accounts[id]; ok {
		return state
	}
	state := factory()
	if state == nil {
		return nil
	}
	b.accounts[id] = state
	b.order = append(b.order, id)
	return state
}

func buildOrderIntent(traceID string, candidate quantcontracts.DispatchCandidate, sequence int, referencePrice float64) (coreexec.OrderIntent, error) {
	direction := strings.ToLower(strings.TrimSpace(string(candidate.Direction)))
	var side, posSide string
	switch direction {
	case string(quantcontracts.DirectionLong):
		side = coreexec.OrderSideBuy
		posSide = coreexec.PosSideLong
	case string(quantcontracts.DirectionShort):
		side = coreexec.OrderSideSell
		posSide = coreexec.PosSideShort
	default:
		return coreexec.OrderIntent{}, fmt.Errorf("unsupported direction %q", candidate.Direction)
	}

	qty := strings.TrimSpace(fmt.Sprintf("%g", candidate.ProposedQty))
	if qty == "" || qty == "0" {
		return coreexec.OrderIntent{}, fmt.Errorf("quantity must be positive")
	}

	return coreexec.OrderIntent{
		ID:          makeClientOrderID(traceID, candidate.AccountID, candidate.Symbol, sequence),
		Symbol:      strings.TrimSpace(candidate.Symbol),
		Side:        side,
		PosSide:     posSide,
		OrderType:   coreexec.OrderTypeMarket,
		Quantity:    qty,
		Price:       formatReferencePrice(referencePrice),
		ClientOrdID: makeClientOrderID(traceID, candidate.AccountID, candidate.Symbol, sequence),
		Timestamp:   nowMillis(),
	}, nil
}

func makeClientOrderID(traceID, accountID, symbol string, sequence int) string {
	traceID = strings.TrimSpace(traceID)
	accountID = strings.TrimSpace(accountID)
	symbol = strings.TrimSpace(symbol)
	if traceID == "" {
		traceID = "trace"
	}
	if accountID == "" {
		accountID = "account"
	}
	if symbol == "" {
		symbol = "symbol"
	}
	return fmt.Sprintf("%s-%s-%s-%d", traceID, accountID, symbol, sequence)
}

func formatReferencePrice(price float64) string {
	if price <= 0 {
		return ""
	}
	return strings.TrimSpace(fmt.Sprintf("%g", price))
}
