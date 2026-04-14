package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/leef-l/brain/internal/quant/audit"
	"github.com/leef-l/brain/internal/quant/router"
	"github.com/leef-l/brain/internal/quant/view"
	"github.com/leef-l/brain/persistence"
	"github.com/leef-l/brain/protocol"
	"github.com/leef-l/brain/sidecar"
)

func TestBrainToolsAndExecute(t *testing.T) {
	brain := NewBrain()

	if got := brain.Tools(); len(got) < 4 {
		t.Fatalf("tools=%d, want at least 4", len(got))
	}

	result, err := brain.HandleMethod(context.Background(), "tools/call", mustRawJSON(protocol.ToolCallRequest{
		Name: "quant.global_portfolio",
	}))
	if err != nil {
		t.Fatal(err)
	}
	var toolResult protocol.ToolCallResult
	if err := copyJSON(result, &toolResult); err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := toolResult.DecodeOutput(&payload); err != nil {
		t.Fatal(err)
	}
	if payload["portfolio"] == nil {
		t.Fatal("global_portfolio should include portfolio")
	}

	execResult, err := brain.HandleMethod(context.Background(), "brain/execute", mustRawJSON(sidecar.ExecuteRequest{
		Instruction: "noop",
	}))
	if err != nil {
		t.Fatal(err)
	}
	var resultEnvelope sidecar.ExecuteResult
	if err := copyJSON(execResult, &resultEnvelope); err != nil {
		t.Fatal(err)
	}
	if resultEnvelope.Status != "completed" {
		t.Fatalf("status=%q, want completed", resultEnvelope.Status)
	}
}

func TestRunCycleExecutesAndPersistsTrace(t *testing.T) {
	svc := New()
	svc.aggregator.LongThreshold = 0.2
	svc.aggregator.DominanceFactor = 1.1
	outcome, err := svc.RunCycle(context.Background(), defaultSymbol, nil)
	if err != nil {
		t.Fatalf("RunCycle returned error: %v", err)
	}
	if outcome.Plan.Symbol != defaultSymbol {
		t.Fatalf("symbol=%q, want %q", outcome.Plan.Symbol, defaultSymbol)
	}
	if outcome.Trace.TraceID == "" {
		t.Fatal("trace id should be set")
	}
	if len(outcome.Results) == 0 {
		t.Fatal("expected execution results")
	}
	if outcome.Trace.Outcome == "" {
		t.Fatal("trace outcome should be recorded")
	}
	if len(outcome.Trace.AccountResults) == 0 {
		t.Fatal("trace should include account results")
	}
	if outcome.Trace.Price <= 0 || outcome.Trace.Confidence <= 0 {
		t.Fatalf("trace market metadata missing: %+v", outcome.Trace)
	}
	if outcome.Trace.DominantStrategy == "" {
		t.Fatalf("trace dominant strategy missing: %+v", outcome.Trace)
	}
	if !outcome.Trace.GlobalRiskAllowed {
		t.Fatalf("trace should record successful global risk pass: %+v", outcome.Trace)
	}

	traces, err := svc.TraceQuery(context.Background(), defaultSymbol, 1)
	if err != nil {
		t.Fatalf("TraceQuery returned error: %v", err)
	}
	if len(traces) != 1 {
		t.Fatalf("len(traces)=%d, want 1", len(traces))
	}
	if traces[0].TraceID != outcome.Trace.TraceID {
		t.Fatalf("persisted trace=%q, want %q", traces[0].TraceID, outcome.Trace.TraceID)
	}
	if len(traces[0].AccountResults) == 0 {
		t.Fatal("persisted trace should include account results")
	}
	if traces[0].DominantStrategy == "" || traces[0].Confidence <= 0 {
		t.Fatalf("persisted strategy metadata missing: %+v", traces[0])
	}
}

func TestBuildPlanRejectsPausedInstrument(t *testing.T) {
	svc := New()
	svc.setInstrumentPaused(defaultSymbol, "manual pause", true)

	plan, trace, err := svc.BuildPlan(context.Background(), defaultSymbol, nil)
	if err != nil {
		t.Fatalf("BuildPlan returned error: %v", err)
	}
	if plan.RejectedStage != "control" {
		t.Fatalf("rejected_stage=%q, want control", plan.RejectedStage)
	}
	if len(plan.Candidates) != 0 {
		t.Fatalf("candidates=%d, want 0", len(plan.Candidates))
	}
	if trace.Reason != "instrument paused" {
		t.Fatalf("trace reason=%q, want instrument paused", trace.Reason)
	}
}

func TestBuildPlanFailsClosedWhenReviewUnavailable(t *testing.T) {
	svc := New()
	svc.aggregator.LongThreshold = 0.2
	svc.aggregator.DominanceFactor = 1.1
	svc.views.SetPortfolio(view.PortfolioView{
		TotalEquity:        100000,
		AvailableEquity:    70000,
		OpenPositions:      3,
		LargestPositionPct: 6,
		DailyLossPct:       0.4,
	})

	plan, trace, err := svc.BuildPlan(context.Background(), defaultSymbol, nil)
	if err != nil {
		t.Fatalf("BuildPlan returned error: %v", err)
	}
	if plan.RejectedStage != "review" {
		t.Fatalf("rejected_stage=%q, want review", plan.RejectedStage)
	}
	if len(plan.Candidates) != 0 {
		t.Fatalf("candidates=%d, want 0 after fail-closed review fallback", len(plan.Candidates))
	}
	if !trace.ReviewRequested {
		t.Fatal("trace should mark review requested")
	}
	if trace.ReviewApproved {
		t.Fatal("trace should not approve unavailable review")
	}
	if !strings.Contains(trace.Reason, "central review unavailable") {
		t.Fatalf("trace reason=%q, want central review unavailable", trace.Reason)
	}
}

func TestBuildPlanCanFailOpenWhenConfigured(t *testing.T) {
	svc := New(WithReviewFallbackPolicy("approve"))
	svc.aggregator.LongThreshold = 0.2
	svc.aggregator.DominanceFactor = 1.1
	svc.views.SetPortfolio(view.PortfolioView{
		TotalEquity:        100000,
		AvailableEquity:    70000,
		OpenPositions:      3,
		LargestPositionPct: 6,
		DailyLossPct:       0.4,
	})

	plan, trace, err := svc.BuildPlan(context.Background(), defaultSymbol, nil)
	if err != nil {
		t.Fatalf("BuildPlan returned error: %v", err)
	}
	if plan.RejectedStage != "" {
		t.Fatalf("rejected_stage=%q, want empty", plan.RejectedStage)
	}
	if len(plan.Candidates) == 0 {
		t.Fatal("candidates should remain available when fail-open is explicitly configured")
	}
	if !trace.ReviewRequested || !trace.ReviewApproved {
		t.Fatalf("unexpected review trace: %+v", trace)
	}
	if !strings.Contains(trace.ReviewReason, "central review unavailable") {
		t.Fatalf("review reason=%q, want central review unavailable", trace.ReviewReason)
	}
}

func TestBuildPlanRejectsWhenGlobalRiskExceededBeforeReview(t *testing.T) {
	svc := New()
	svc.aggregator.LongThreshold = 0.2
	svc.aggregator.DominanceFactor = 1.1
	svc.views.SetPortfolio(view.PortfolioView{
		TotalEquity:        1000,
		AvailableEquity:    900,
		OpenPositions:      0,
		LargestPositionPct: 0,
		DailyLossPct:       0.2,
	})

	plan, trace, err := svc.BuildPlan(context.Background(), defaultSymbol, nil)
	if err != nil {
		t.Fatalf("BuildPlan returned error: %v", err)
	}
	if plan.RejectedStage != "global_risk_pre" {
		t.Fatalf("rejected_stage=%q, want global_risk_pre", plan.RejectedStage)
	}
	if len(plan.Candidates) != 0 {
		t.Fatalf("candidates=%d, want 0 after global risk rejection", len(plan.Candidates))
	}
	if !strings.Contains(trace.Reason, "above 5% of equity") {
		t.Fatalf("trace reason=%q, want single position rejection", trace.Reason)
	}
	if trace.GlobalRiskAllowed {
		t.Fatalf("trace should record global risk rejection: %+v", trace)
	}
	if !strings.Contains(trace.GlobalRiskReason, "above 5% of equity") {
		t.Fatalf("trace global risk reason=%q, want single position rejection", trace.GlobalRiskReason)
	}
}

func TestApplyGlobalRiskGuardUsesRequestedStage(t *testing.T) {
	svc := New()
	plan := router.DispatchPlan{
		Symbol:    "BTC-USDT-SWAP",
		Direction: "long",
		Candidates: []router.DispatchCandidate{{
			AccountID:     "paper",
			Symbol:        "BTC-USDT-SWAP",
			Side:          "buy",
			Quantity:      1,
			EntryPrice:    132.5,
			StopLossPrice: 120,
		}},
	}

	rejected := svc.applyGlobalRiskGuard(plan, view.PortfolioView{TotalEquity: 1000}, "global_risk_post")
	if rejected.RejectedStage != "global_risk_post" {
		t.Fatalf("rejected_stage=%q, want global_risk_post", rejected.RejectedStage)
	}
	if len(rejected.Candidates) != 0 {
		t.Fatalf("candidates=%d, want 0 after post-review global risk rejection", len(rejected.Candidates))
	}
}

func TestRestoreRuntimeRehydratesLatestControlAndRecoveryState(t *testing.T) {
	store := audit.NewPersistentStore(persistence.NewMemSignalTraceStore(nil))
	for _, trace := range []audit.SignalTrace{
		{Symbol: "control", Outcome: "pause_trading", RejectedStage: "control", Reason: "manual freeze", CreatedAt: 1000, UpdatedAt: 1000},
		{Symbol: defaultSymbol, Outcome: "pause_instrument", RejectedStage: "control", Reason: "symbol pause", CreatedAt: 1100, UpdatedAt: 1100},
		{Symbol: defaultSymbol, Outcome: "resume_instrument", RejectedStage: "control", Reason: "symbol resume", CreatedAt: 1200, UpdatedAt: 1200},
		{Symbol: "ETH-USDT-SWAP", Outcome: "pause_instrument", RejectedStage: "control", Reason: "eth halt", CreatedAt: 1300, UpdatedAt: 1300},
		{Symbol: "control", Outcome: "recovery_completed", RejectedStage: "recovery", Reason: "accounts=1 recovered=1 failed=0", CreatedAt: 1400, UpdatedAt: 1400},
	} {
		trace := trace
		if err := store.Save(context.Background(), &trace); err != nil {
			t.Fatalf("Save(trace %s): %v", trace.Outcome, err)
		}
	}

	restored := New(WithAuditStore(store))
	if err := restored.RestoreRuntime(context.Background()); err != nil {
		t.Fatalf("RestoreRuntime returned error: %v", err)
	}

	runtime := restored.runtimeSnapshot()
	if !runtime.TradingPaused {
		t.Fatal("trading pause state was not restored")
	}
	if runtime.LastAction != "eth halt" {
		t.Fatalf("last action=%q, want eth halt", runtime.LastAction)
	}
	if runtime.LastRecovery != "accounts=1 recovered=1 failed=0" {
		t.Fatalf("last recovery=%q", runtime.LastRecovery)
	}
	if runtime.LastRecoveryAt != 1400 {
		t.Fatalf("last recovery at=%d, want 1400", runtime.LastRecoveryAt)
	}
	if _, ok := runtime.PausedInstruments[defaultSymbol]; ok {
		t.Fatalf("symbol %s should have been resumed", defaultSymbol)
	}
	if got := runtime.PausedInstruments["ETH-USDT-SWAP"]; got != "eth halt" {
		t.Fatalf("paused instrument state=%q, want eth halt", got)
	}
}

func copyJSON(src any, dst any) error {
	raw, err := json.Marshal(src)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, dst)
}
