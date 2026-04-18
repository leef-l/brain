package main

import (
	"net/http"
	"time"

	"github.com/leef-l/brain/cmd/brain/dashboard"
	"github.com/leef-l/brain/sdk/events"
	"github.com/leef-l/brain/sdk/kernel"
)

// runManagerAdapter adapts *runManager to dashboard.RunManager interface.
type runManagerAdapter struct {
	mgr *runManager
}

func (a *runManagerAdapter) RunningCount() int { return a.mgr.runningCount() }
func (a *runManagerAdapter) TotalCount() int   { return len(a.mgr.list()) }

// leaseProviderAdapter adapts *kernel.MemLeaseManager to dashboard.LeaseProvider.
type leaseProviderAdapter struct {
	lm *kernel.MemLeaseManager
}

func (a *leaseProviderAdapter) ActiveLeases() []dashboard.LeaseSnapshot {
	if a.lm == nil {
		return nil
	}
	snaps := a.lm.Snapshot()
	out := make([]dashboard.LeaseSnapshot, len(snaps))
	for i, s := range snaps {
		out[i] = dashboard.LeaseSnapshot{
			ID:          s.ID,
			Capability:  s.Capability,
			ResourceKey: s.ResourceKey,
			AccessMode:  string(s.AccessMode),
		}
	}
	return out
}

func registerDashboardRoutes(mux *http.ServeMux, mgr *runManager, pool *kernel.ProcessBrainPool, bus *events.MemEventBus, cfg *brainConfig, startTime time.Time, leaseManager *kernel.MemLeaseManager) {
	dashboard.RegisterRoutes(mux, &runManagerAdapter{mgr: mgr}, pool, bus, cfg, startTime, &leaseProviderAdapter{lm: leaseManager})
}
