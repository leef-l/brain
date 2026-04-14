package execution

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	coreexec "github.com/leef-l/brain/internal/execution"
	"github.com/leef-l/brain/internal/quantcontracts"
)

// PriceProvider resolves the current mark price for an instrument.
type PriceProvider func(ctx context.Context, symbol string) (float64, bool)

// PaperOption configures the paper executor adapter.
type PaperOption func(*PaperExecutorAdapter)

// PaperExecutorAdapter keeps one in-memory client per account and translates
// quant dispatch plans into the shared execution intent format.
type PaperExecutorAdapter struct {
	mu             sync.RWMutex
	book           *AccountBook
	defaultAccount string
	priceProvider  PriceProvider
	slippageBps    float64
	feeBps         float64
}

func newPaperExecutorAdapter() *PaperExecutorAdapter {
	return &PaperExecutorAdapter{
		book:        NewAccountBook(),
		slippageBps: 0,
		feeBps:      5,
	}
}

func NewPaperExecutorAdapter(accountIDs ...string) *PaperExecutorAdapter {
	adapter := newPaperExecutorAdapter()
	for _, accountID := range accountIDs {
		adapter.ensureAccount(accountID)
	}
	if len(accountIDs) > 0 {
		adapter.defaultAccount = strings.TrimSpace(accountIDs[0])
	}
	return adapter
}

func WithPriceProvider(provider PriceProvider) PaperOption {
	return func(a *PaperExecutorAdapter) {
		a.priceProvider = provider
	}
}

func WithSlippageBps(bps float64) PaperOption {
	return func(a *PaperExecutorAdapter) {
		if bps < 0 {
			bps = 0
		}
		a.slippageBps = bps
	}
}

func WithFeeBps(bps float64) PaperOption {
	return func(a *PaperExecutorAdapter) {
		if bps < 0 {
			bps = 0
		}
		a.feeBps = bps
	}
}

func NewPaperExecutor(accountIDs []string, opts ...PaperOption) *PaperExecutorAdapter {
	adapter := NewPaperExecutorWithOptions(accountIDs, opts...)
	return adapter
}

func NewPaperExecutorWithOptions(accountIDs []string, opts ...PaperOption) *PaperExecutorAdapter {
	adapter := newPaperExecutorAdapter()
	for _, opt := range opts {
		if opt != nil {
			opt(adapter)
		}
	}
	for _, accountID := range accountIDs {
		adapter.ensureAccount(accountID)
	}
	if len(accountIDs) > 0 {
		adapter.defaultAccount = strings.TrimSpace(accountIDs[0])
	}
	return adapter
}

func (a *PaperExecutorAdapter) ensureAccount(accountID string) *AccountState {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return nil
	}
	if state, ok := a.book.Get(accountID); ok {
		return state
	}
	return a.book.ensure(accountID, func() *AccountState {
		var provider coreexec.PriceProvider
		if a.priceProvider != nil {
			provider = func(ctx context.Context, symbol string) (float64, bool) {
				return a.priceProvider(ctx, symbol)
			}
		}
		backend := coreexec.NewPaperBackend(
			coreexec.WithPaperPriceProvider(provider),
			coreexec.WithPaperSlippageBps(a.slippageBps),
			coreexec.WithPaperFeeBps(a.feeBps),
		)
		state := NewAccountState(accountID, coreexec.NewClient(backend))
		state.SetBackendLabel(backend.Name())
		return state
	})
}

func (a *PaperExecutorAdapter) Accounts() []AccountSnapshot {
	if a == nil || a.book == nil {
		return nil
	}
	return a.book.List()
}

func (a *PaperExecutorAdapter) Account(accountID string) (AccountSnapshot, bool) {
	if a == nil || a.book == nil {
		return AccountSnapshot{}, false
	}
	state, ok := a.book.Get(accountID)
	if !ok {
		return AccountSnapshot{}, false
	}
	return state.Snapshot(), true
}

func (a *PaperExecutorAdapter) PauseAccount(accountID, reason string) bool {
	state := a.ensureAccount(accountID)
	if state == nil {
		return false
	}
	state.MarkPaused(reason)
	return true
}

func (a *PaperExecutorAdapter) ResumeAccount(accountID, reason string) bool {
	state := a.ensureAccount(accountID)
	if state == nil {
		return false
	}
	state.MarkActive(reason)
	return true
}

func (a *PaperExecutorAdapter) MarkRecovering(accountID, reason string) bool {
	state := a.ensureAccount(accountID)
	if state == nil {
		return false
	}
	state.MarkRecovering(reason)
	return true
}

func (a *PaperExecutorAdapter) ExecutePlan(ctx context.Context, plan quantcontracts.DispatchPlan) ([]coreexec.ExecutionResult, error) {
	if a == nil {
		return nil, fmt.Errorf("paper executor is nil")
	}
	if a.book == nil {
		a.book = NewAccountBook()
	}
	results := make([]coreexec.ExecutionResult, 0, len(plan.Candidates))
	for idx, candidate := range plan.Candidates {
		if !candidate.Allowed {
			results = append(results, coreexec.ExecutionResult{
				Status:    coreexec.OrderStatusRejected,
				Timestamp: nowMillis(),
				Error:     firstReason(candidate.RiskReason, "candidate not allowed"),
			})
			continue
		}
		if candidate.ProposedQty <= 0 {
			results = append(results, coreexec.ExecutionResult{
				Status:    coreexec.OrderStatusRejected,
				Timestamp: nowMillis(),
				Error:     "candidate quantity must be positive",
			})
			continue
		}

		accountID := candidate.AccountID
		if accountID == "" {
			accountID = a.defaultAccount
		}
		state := a.ensureAccount(accountID)
		if state == nil {
			return results, fmt.Errorf("account %q is not available", accountID)
		}
		if state.Status() == AccountStatusDisabled {
			results = append(results, coreexec.ExecutionResult{
				Status:    coreexec.OrderStatusRejected,
				Timestamp: nowMillis(),
				Error:     "account disabled",
			})
			continue
		}

		intent, err := buildOrderIntent(plan.TraceID, candidate, idx, snapshotPrice(plan.Snapshot, candidate.Symbol))
		if err != nil {
			results = append(results, coreexec.ExecutionResult{
				Status:    coreexec.OrderStatusRejected,
				Timestamp: nowMillis(),
				Error:     err.Error(),
			})
			state.RecordExecutionResult(coreexec.ExecutionResult{
				Status:    coreexec.OrderStatusRejected,
				Timestamp: nowMillis(),
				Error:     err.Error(),
			})
			continue
		}

		client := state.Client()
		if client == nil {
			err := fmt.Errorf("account %q has no execution client", accountID)
			results = append(results, coreexec.ExecutionResult{
				Status:    coreexec.OrderStatusRejected,
				Timestamp: nowMillis(),
				Error:     err.Error(),
			})
			state.RecordExecutionResult(coreexec.ExecutionResult{
				Status:    coreexec.OrderStatusRejected,
				Timestamp: nowMillis(),
				Error:     err.Error(),
			})
			continue
		}

		result, err := client.Execute(ctx, intent)
		if err != nil {
			state.RecordExecutionResult(coreexec.ExecutionResult{
				Status:    coreexec.OrderStatusRejected,
				Timestamp: nowMillis(),
				Error:     err.Error(),
			})
			return results, err
		}
		state.RecordExecutionResult(result)
		results = append(results, result)
	}
	return results, nil
}

func (a *PaperExecutorAdapter) ReconcileAccount(ctx context.Context, accountID string) (ReconcileResult, error) {
	_ = ctx
	state := a.ensureAccount(accountID)
	if state == nil {
		return ReconcileResult{}, fmt.Errorf("account %q is not available", accountID)
	}
	client := state.Client()
	if client == nil {
		return ReconcileResult{}, fmt.Errorf("account %q has no execution client", accountID)
	}
	snapshot := client.Snapshot()
	now := nowMillis()
	state.RecordReconcile()
	openOrders := 0
	for _, order := range snapshot.Orders {
		switch order.Status {
		case coreexec.OrderStatusAccepted, coreexec.OrderStatusOpen, coreexec.OrderStatusTriggered:
			openOrders++
		}
	}

	result := ReconcileResult{
		AccountID:     state.ID(),
		Status:        state.Snapshot().Status,
		OpenOrders:    openOrders,
		PositionCount: len(snapshot.Positions),
		RecoveredAt:   now,
		Summary:       fmt.Sprintf("open_orders=%d positions=%d", openOrders, len(snapshot.Positions)),
	}
	if result.Status == "" {
		result.Status = AccountStatusActive
	}
	return result, nil
}

func (a *PaperExecutorAdapter) ReconcileAll(ctx context.Context) []ReconcileResult {
	if a == nil || a.book == nil {
		return nil
	}
	accounts := a.book.List()
	results := make([]ReconcileResult, 0, len(accounts))
	for _, account := range accounts {
		result, err := a.ReconcileAccount(ctx, account.AccountID)
		if err != nil {
			results = append(results, ReconcileResult{
				AccountID: account.AccountID,
				Status:    AccountStatusRecovering,
				Error:     err.Error(),
			})
			continue
		}
		results = append(results, result)
	}
	sort.SliceStable(results, func(i, j int) bool {
		return results[i].AccountID < results[j].AccountID
	})
	return results
}

func firstReason(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func snapshotPrice(snapshot *quantcontracts.MarketSnapshot, symbol string) float64 {
	if snapshot == nil {
		return 0
	}
	if snapshot.Symbol != "" && !strings.EqualFold(strings.TrimSpace(snapshot.Symbol), strings.TrimSpace(symbol)) {
		return 0
	}
	switch {
	case snapshot.Mark > 0:
		return snapshot.Mark
	case snapshot.Last > 0:
		return snapshot.Last
	case snapshot.Ask > 0:
		return snapshot.Ask
	case snapshot.Bid > 0:
		return snapshot.Bid
	default:
		return 0
	}
}
