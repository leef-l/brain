package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/leef-l/brain/cmd/brain/dashboard"
	"github.com/leef-l/brain/sdk/agent"
	"github.com/leef-l/brain/sdk/events"
	"github.com/leef-l/brain/sdk/kernel"
	"github.com/leef-l/brain/sdk/kernel/mcpadapter"
	"github.com/leef-l/brain/sdk/persistence"
	"github.com/leef-l/brain/sdk/protocol"
	"github.com/leef-l/brain/sdk/tool"
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

// learningProviderAdapter 组合 PatternLibrary + LearningStore 给 Dashboard。
// 复用 brain-v3 已有数据源,不缓存、不另建统计表。调用开销由 dashboard 端控制。
type learningProviderAdapter struct {
	patternLib *tool.PatternLibrary
	learning   persistence.LearningStore
}

func (a *learningProviderAdapter) LearningOverview() dashboard.LearningOverview {
	out := dashboard.LearningOverview{}
	if a == nil {
		return out
	}
	if a.patternLib != nil {
		for _, p := range a.patternLib.List("") {
			stat := dashboard.PatternStat{
				ID:           p.ID,
				Category:     p.Category,
				Source:       p.Source,
				MatchCount:   p.Stats.MatchCount,
				SuccessCount: p.Stats.SuccessCount,
				FailureCount: p.Stats.FailureCount,
				SuccessRate:  p.Stats.SuccessRate(),
			}
			if !p.Stats.LastHitAt.IsZero() {
				stat.LastHitAt = p.Stats.LastHitAt.UTC().Format(time.RFC3339)
			}
			out.Patterns = append(out.Patterns, stat)
		}
	}
	if a.learning != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if sums, err := a.learning.ListDailySummaries(ctx, 14); err == nil {
			for _, s := range sums {
				out.Daily = append(out.Daily, dashboard.DailySummaryStat{
					Date:        s.Date,
					RunsTotal:   s.RunsTotal,
					RunsFailed:  s.RunsFailed,
					BrainCounts: s.BrainCounts,
					SummaryText: s.SummaryText,
				})
			}
		}
		if seqs, err := a.learning.ListInteractionSequences(ctx, "", 500); err == nil {
			// 按 brain_kind 聚合
			byBrain := map[string]*dashboard.InteractionStat{}
			for _, s := range seqs {
				stat, ok := byBrain[s.BrainKind]
				if !ok {
					stat = &dashboard.InteractionStat{BrainKind: s.BrainKind}
					byBrain[s.BrainKind] = stat
				}
				stat.Count++
				if s.Outcome == "success" {
					stat.Successes++
				}
			}
			for _, s := range byBrain {
				out.Interactions = append(out.Interactions, *s)
			}
		}
	}
	return out
}

func registerDashboardRoutes(mux *http.ServeMux, mgr *runManager, pool *kernel.ProcessBrainPool, mcpPool *mcpadapter.MCPBrainPool, bus *events.MemEventBus, cfg *brainConfig, startTime time.Time, leaseManager *kernel.MemLeaseManager, learnP dashboard.LearningProvider) *dashboard.WSHub {
	var orch *kernel.Orchestrator
	if pool != nil {
		orch = kernel.NewOrchestratorWithPool(pool, &kernel.ProcessRunner{BinResolver: defaultBinResolver()}, &kernel.LLMProxy{}, defaultBinResolver(), kernel.OrchestratorConfig{})
	}

	quantCaller := dashboard.QuantToolCaller(func(ctx context.Context, toolName string, args map[string]interface{}) (json.RawMessage, error) {
		if orch == nil {
			return nil, fmt.Errorf("quant sidecar not available")
		}
		if !orch.CanDelegate(agent.KindQuant) {
			return nil, fmt.Errorf("quant sidecar not available")
		}
		argsJSON, _ := json.Marshal(args)
		res, err := orch.CallTool(ctx, &protocol.SpecialistToolCallRequest{
			TargetKind: agent.KindQuant,
			ToolName:   toolName,
			Arguments:  argsJSON,
		})
		if err != nil {
			return nil, err
		}
		if res.IsError {
			if res.Error != nil {
				return nil, fmt.Errorf("%s", res.Error.Message)
			}
			return nil, fmt.Errorf("tool call failed")
		}
		return res.CanonicalOutput(), nil
	})

	return dashboard.RegisterRoutes(mux, &runManagerAdapter{mgr: mgr}, pool, mcpPool, bus, cfg, startTime, &leaseProviderAdapter{lm: leaseManager}, learnP, quantCaller)
}
