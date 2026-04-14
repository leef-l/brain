package recovery

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	qexec "github.com/leef-l/brain/internal/quant/execution"
)

// Target is the minimal capability required by the recovery coordinator.
type Target interface {
	Accounts() []qexec.AccountSnapshot
	ReconcileAccount(ctx context.Context, accountID string) (qexec.ReconcileResult, error)
}

// Report summarizes a recovery pass across all tracked accounts.
type Report struct {
	StartedAt   int64                   `json:"started_at"`
	CompletedAt int64                   `json:"completed_at"`
	Results     []qexec.ReconcileResult `json:"results,omitempty"`
	Recovered   int                     `json:"recovered,omitempty"`
	Failed      int                     `json:"failed,omitempty"`
	Summary     string                  `json:"summary,omitempty"`
}

// Manager orchestrates the minimal reconcile flow after a crash or restart.
type Manager struct {
	target Target
	now    func() time.Time
}

func NewManager(target Target) *Manager {
	return &Manager{
		target: target,
		now:    time.Now,
	}
}

func (m *Manager) Reconcile(ctx context.Context) (Report, error) {
	if m == nil || m.target == nil {
		return Report{}, fmt.Errorf("recovery target is not initialized")
	}

	accounts := m.target.Accounts()
	report := Report{
		StartedAt: m.now().UTC().UnixMilli(),
	}
	for _, account := range accounts {
		if !needsRecovery(account) {
			continue
		}
		result, err := m.target.ReconcileAccount(ctx, account.AccountID)
		if err != nil {
			report.Failed++
			report.Results = append(report.Results, qexec.ReconcileResult{
				AccountID: account.AccountID,
				Status:    qexec.AccountStatusRecovering,
				Error:     err.Error(),
			})
			continue
		}
		report.Results = append(report.Results, result)
		if strings.EqualFold(string(result.Status), string(qexec.AccountStatusActive)) {
			report.Recovered++
		}
	}
	report.CompletedAt = m.now().UTC().UnixMilli()
	sort.SliceStable(report.Results, func(i, j int) bool {
		return report.Results[i].AccountID < report.Results[j].AccountID
	})
	report.Summary = fmt.Sprintf("accounts=%d recovered=%d failed=%d", len(accounts), report.Recovered, report.Failed)
	return report, nil
}

func (m *Manager) ReconcileAccount(ctx context.Context, accountID string) (qexec.ReconcileResult, error) {
	if m == nil || m.target == nil {
		return qexec.ReconcileResult{}, fmt.Errorf("recovery target is not initialized")
	}
	return m.target.ReconcileAccount(ctx, accountID)
}

func needsRecovery(account qexec.AccountSnapshot) bool {
	switch account.Status {
	case qexec.AccountStatusRecovering, qexec.AccountStatusPaused, qexec.AccountStatusDisabled:
		return true
	}
	return account.OpenOrders > 0 || account.PositionCount > 0
}
