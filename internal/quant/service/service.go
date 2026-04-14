package service

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/leef-l/brain/agent"
	coreexec "github.com/leef-l/brain/internal/execution"
	"github.com/leef-l/brain/internal/quant/audit"
	qexec "github.com/leef-l/brain/internal/quant/execution"
	"github.com/leef-l/brain/internal/quant/recovery"
	"github.com/leef-l/brain/internal/quant/router"
	"github.com/leef-l/brain/internal/quant/view"
	"github.com/leef-l/brain/internal/quantcontracts"
	"github.com/leef-l/brain/internal/risk"
	"github.com/leef-l/brain/internal/strategy"
	"github.com/leef-l/brain/persistence"
	"github.com/leef-l/brain/protocol"
	"github.com/leef-l/brain/sidecar"
	"github.com/leef-l/brain/tool"
)

const (
	brainVersion  = "0.1.0-skeleton"
	defaultSymbol = "BTC-USDT-SWAP"
)

type GlobalRiskConfig struct {
	MaxGlobalExposurePct   float64
	MaxGlobalSameDirection float64
	MaxGlobalDailyLoss     float64
	MaxSymbolExposure      float64
}

type reviewFallbackPolicy string

const (
	reviewFallbackReject  reviewFallbackPolicy = "reject"
	reviewFallbackApprove reviewFallbackPolicy = "approve"
)

type quantExecutor interface {
	Accounts() []qexec.AccountSnapshot
	PauseAccount(accountID, reason string) bool
	ResumeAccount(accountID, reason string) bool
	MarkRecovering(accountID, reason string) bool
	ExecutePlan(ctx context.Context, plan quantcontracts.DispatchPlan) ([]coreexec.ExecutionResult, error)
	ReconcileAccount(ctx context.Context, accountID string) (qexec.ReconcileResult, error)
}

type options struct {
	auditStore audit.Store
	executor   quantExecutor
	accountIDs []string
	reviewMode reviewFallbackPolicy
	globalRisk GlobalRiskConfig
}

// Option configures the quant service without introducing framework coupling.
type Option func(*options)

type DispatchOutcome struct {
	Plan     router.DispatchPlan        `json:"plan"`
	Trace    audit.SignalTrace          `json:"trace"`
	Results  []coreexec.ExecutionResult `json:"results,omitempty"`
	Recovery recovery.Report            `json:"recovery,omitempty"`
}

// Service owns the Quant fast path state and the public tool surface.
type Service struct {
	views      *view.Store
	audits     audit.Store
	router     *router.Router
	pool       *strategy.Pool
	aggregator strategy.Aggregator
	registry   *tool.MemRegistry
	executor   quantExecutor
	recovery   *recovery.Manager
	reviewMode reviewFallbackPolicy
	guard      risk.Guard
	globalRisk GlobalRiskConfig

	mu      sync.RWMutex
	runtime runtimeState
}

type runtimeState struct {
	TradingPaused     bool              `json:"trading_paused"`
	PausedInstruments map[string]string `json:"paused_instruments,omitempty"`
	LastAction        string            `json:"last_action,omitempty"`
	LastRecovery      string            `json:"last_recovery,omitempty"`
	LastRecoveryAt    int64             `json:"last_recovery_at,omitempty"`
}

// Brain wraps Service with the BrainHandler interfaces expected by sidecar.Run.
type Brain struct {
	svc    *Service
	caller sidecar.KernelCaller
}

func WithAuditStore(store audit.Store) Option {
	return func(opts *options) {
		if store != nil {
			opts.auditStore = store
		}
	}
}

func WithExecutor(executor quantExecutor) Option {
	return func(opts *options) {
		if executor != nil {
			opts.executor = executor
		}
	}
}

func WithAccountIDs(accountIDs ...string) Option {
	return func(opts *options) {
		opts.accountIDs = append([]string(nil), accountIDs...)
	}
}

func WithReviewFallbackPolicy(policy string) Option {
	return func(opts *options) {
		opts.reviewMode = normalizeReviewFallbackPolicy(policy)
	}
}

func WithGlobalRiskConfig(cfg GlobalRiskConfig) Option {
	return func(opts *options) {
		opts.globalRisk = normalizeGlobalRiskConfig(cfg)
	}
}

func New(opts ...Option) *Service {
	cfg := options{
		accountIDs: []string{"paper"},
		reviewMode: reviewFallbackReject,
		globalRisk: defaultGlobalRiskConfig(),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	if cfg.executor == nil {
		cfg.executor = qexec.NewPaperExecutor(cfg.accountIDs)
	}
	if len(cfg.accountIDs) == 0 {
		cfg.accountIDs = accountIDsFromSnapshots(cfg.executor.Accounts())
	}
	if len(cfg.accountIDs) == 0 {
		cfg.accountIDs = []string{"paper"}
	}
	if cfg.auditStore == nil {
		cfg.auditStore = audit.NewPersistentStore(persistence.NewMemSignalTraceStore(nil))
	}

	views := view.NewStore()
	views.UpsertSnapshot(view.NewFixtureSnapshot(defaultSymbol))
	views.SetPortfolio(view.PortfolioView{
		TotalEquity:        100000,
		AvailableEquity:    75000,
		OpenPositions:      1,
		LargestPositionPct: 2.5,
		DailyLossPct:       0.4,
		Note:               "fixture portfolio",
	})

	svc := &Service{
		views:      views,
		audits:     cfg.auditStore,
		router:     router.New(cfg.accountIDs...),
		pool:       strategy.DefaultPool(),
		aggregator: strategy.NewAggregator(),
		registry:   tool.NewMemRegistry(),
		executor:   cfg.executor,
		reviewMode: cfg.reviewMode,
		guard:      risk.DefaultGuard(),
		globalRisk: cfg.globalRisk,
		runtime: runtimeState{
			PausedInstruments: make(map[string]string),
		},
	}
	svc.recovery = recovery.NewManager(svc.executor)
	svc.registerTools()
	return svc
}

func NewBrain(opts ...Option) *Brain {
	return &Brain{svc: New(opts...)}
}

func (b *Brain) Kind() agent.Kind { return agent.KindQuant }

func (b *Brain) Version() string { return brainVersion }

func (b *Brain) Tools() []string { return sidecar.RegistryToolNames(b.svc.registry) }

func (b *Brain) ToolSchemas() []tool.Schema { return sidecar.RegistryToolSchemas(b.svc.registry) }

func (b *Brain) SetKernelCaller(caller sidecar.KernelCaller) { b.caller = caller }

func (b *Brain) RestoreRuntime(ctx context.Context) error {
	if b == nil || b.svc == nil {
		return nil
	}
	return b.svc.RestoreRuntime(ctx)
}

func (b *Brain) HandleMethod(ctx context.Context, method string, params json.RawMessage) (interface{}, error) {
	switch method {
	case "tools/call":
		return sidecar.DispatchToolCall(ctx, params, b.svc.registry, nil)
	case "brain/execute":
		return b.svc.handleExecute(ctx, params, b.caller)
	default:
		return nil, sidecar.ErrMethodNotFound
	}
}

func (s *Service) registerTools() {
	_ = s.registry.Register(newJSONTool(
		quantcontracts.ToolQuantGlobalPortfolio,
		"return the latest portfolio and runtime state",
		`{"type":"object","properties":{},"additionalProperties":false}`,
		tool.RiskSafe,
		func(ctx context.Context, _ json.RawMessage) (any, error) {
			return s.globalPortfolio(ctx), nil
		},
	))
	_ = s.registry.Register(newJSONTool(
		quantcontracts.ToolQuantTraceQuery,
		"query recent SignalTrace records",
		`{"type":"object","properties":{"symbol":{"type":"string"},"limit":{"type":"integer","minimum":1}},"additionalProperties":false}`,
		tool.RiskSafe,
		func(ctx context.Context, raw json.RawMessage) (any, error) {
			return s.traceQuery(ctx, raw)
		},
	))
	_ = s.registry.Register(newJSONTool(
		quantcontracts.ToolQuantPauseTrading,
		"pause all trading for the Quant brain",
		`{"type":"object","properties":{"reason":{"type":"string"}},"required":["reason"],"additionalProperties":false}`,
		tool.RiskMedium,
		func(ctx context.Context, raw json.RawMessage) (any, error) {
			return s.pauseTrading(ctx, raw)
		},
	))
	_ = s.registry.Register(newJSONTool(
		quantcontracts.ToolQuantResumeTrading,
		"resume all trading for the Quant brain",
		`{"type":"object","properties":{"reason":{"type":"string"}},"required":["reason"],"additionalProperties":false}`,
		tool.RiskMedium,
		func(ctx context.Context, raw json.RawMessage) (any, error) {
			return s.resumeTrading(ctx, raw)
		},
	))
	_ = s.registry.Register(newJSONTool(
		quantcontracts.ToolQuantPauseInstrument,
		"pause a single instrument",
		`{"type":"object","properties":{"symbol":{"type":"string"},"reason":{"type":"string"}},"required":["symbol","reason"],"additionalProperties":false}`,
		tool.RiskMedium,
		func(ctx context.Context, raw json.RawMessage) (any, error) {
			return s.pauseInstrument(ctx, raw)
		},
	))
	_ = s.registry.Register(newJSONTool(
		quantcontracts.ToolQuantResumeInstrument,
		"resume a single instrument",
		`{"type":"object","properties":{"symbol":{"type":"string"},"reason":{"type":"string"}},"required":["symbol","reason"],"additionalProperties":false}`,
		tool.RiskMedium,
		func(ctx context.Context, raw json.RawMessage) (any, error) {
			return s.resumeInstrument(ctx, raw)
		},
	))
}

func (s *Service) globalPortfolio(_ context.Context) map[string]any {
	portfolio := s.effectivePortfolioView()
	runtime := s.runtimeSnapshot()

	return map[string]any{
		"portfolio":          portfolio,
		"runtime":            runtime,
		"accounts":           s.accountSnapshots(),
		"snapshot_count":     len(s.views.SnapshotList()),
		"trading_paused":     portfolio.PausedTrading,
		"paused_instruments": portfolio.PausedInstruments,
	}
}

func (s *Service) traceQuery(ctx context.Context, raw json.RawMessage) (any, error) {
	type request struct {
		Symbol string `json:"symbol"`
		Limit  int    `json:"limit"`
	}
	var req request
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, fmt.Errorf("parse trace_query: %w", err)
		}
	}
	traces, err := s.audits.Query(ctx, audit.QueryFilter{
		Symbol: strings.TrimSpace(req.Symbol),
		Limit:  req.Limit,
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"traces": traces, "total": len(traces)}, nil
}

func (s *Service) pauseTrading(ctx context.Context, raw json.RawMessage) (any, error) {
	type request struct {
		Reason string `json:"reason"`
	}
	var req request
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, fmt.Errorf("parse pause_trading: %w", err)
	}
	s.setTradingPaused(true, req.Reason)
	s.applyAccountAction(req.Reason, s.executor.PauseAccount)
	trace := s.newControlTrace("control", "pause_trading", req.Reason)
	_ = s.audits.Save(ctx, &trace)
	return map[string]any{"status": "ok", "paused": true, "reason": req.Reason}, nil
}

func (s *Service) resumeTrading(ctx context.Context, raw json.RawMessage) (any, error) {
	type request struct {
		Reason string `json:"reason"`
	}
	var req request
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, fmt.Errorf("parse resume_trading: %w", err)
	}
	s.setTradingPaused(false, req.Reason)
	s.applyAccountAction(req.Reason, s.executor.ResumeAccount)
	trace := s.newControlTrace("control", "resume_trading", req.Reason)
	_ = s.audits.Save(ctx, &trace)
	return map[string]any{"status": "ok", "paused": false, "reason": req.Reason}, nil
}

func (s *Service) pauseInstrument(ctx context.Context, raw json.RawMessage) (any, error) {
	type request struct {
		Symbol string `json:"symbol"`
		Reason string `json:"reason"`
	}
	var req request
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, fmt.Errorf("parse pause_instrument: %w", err)
	}
	s.setInstrumentPaused(req.Symbol, req.Reason, true)
	trace := s.newControlTrace(req.Symbol, "pause_instrument", req.Reason)
	_ = s.audits.Save(ctx, &trace)
	return map[string]any{"status": "ok", "symbol": req.Symbol, "paused": true, "reason": req.Reason}, nil
}

func (s *Service) resumeInstrument(ctx context.Context, raw json.RawMessage) (any, error) {
	type request struct {
		Symbol string `json:"symbol"`
		Reason string `json:"reason"`
	}
	var req request
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, fmt.Errorf("parse resume_instrument: %w", err)
	}
	s.setInstrumentPaused(req.Symbol, req.Reason, false)
	trace := s.newControlTrace(req.Symbol, "resume_instrument", req.Reason)
	_ = s.audits.Save(ctx, &trace)
	return map[string]any{"status": "ok", "symbol": req.Symbol, "paused": false, "reason": req.Reason}, nil
}

func (s *Service) handleExecute(ctx context.Context, raw json.RawMessage, caller sidecar.KernelCaller) (interface{}, error) {
	var req sidecar.ExecuteRequest
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &req); err != nil {
			return sidecar.ExecuteResult{
				Status: "failed",
				Error:  fmt.Sprintf("parse execute request: %v", err),
				Turns:  1,
			}, nil
		}
	}

	instruction := strings.TrimSpace(req.Instruction)
	switch instruction {
	case "", "noop":
		return sidecar.ExecuteResult{
			Status:  "completed",
			Summary: "noop",
			Turns:   1,
		}, nil
	case quantcontracts.InstructionShutdownPrepare:
		s.setTradingPaused(true, "shutdown_prepare")
		s.applyAccountAction("shutdown_prepare", s.executor.PauseAccount)
		trace := s.newControlTrace("control", "pause_trading", "shutdown_prepare")
		_ = s.audits.Save(ctx, &trace)
		snapshot := s.globalPortfolio(ctx)
		return sidecar.ExecuteResult{
			Status:  "completed",
			Summary: mustJSONString(snapshot),
			Turns:   1,
		}, nil
	case quantcontracts.InstructionCollectReviewInput:
		payload := map[string]any{
			"portfolio":          s.effectivePortfolioView(),
			"accounts":           s.accountSnapshots(),
			"snapshot_count":     len(s.views.SnapshotList()),
			"paused_trading":     s.runtimeSnapshot().TradingPaused,
			"paused_instruments": s.effectivePortfolioView().PausedInstruments,
			"trace_count":        len(s.traceQuerySnapshot("", 0)),
		}
		return sidecar.ExecuteResult{
			Status:  "completed",
			Summary: mustJSONString(payload),
			Turns:   1,
		}, nil
	case quantcontracts.InstructionHealthCheck:
		return sidecar.ExecuteResult{
			Status:  "completed",
			Summary: s.reconcileSummary(),
			Turns:   1,
		}, nil
	default:
		return sidecar.ExecuteResult{
			Status: "failed",
			Error:  fmt.Sprintf("unsupported instruction %q", instruction),
			Turns:  1,
		}, nil
	}
}

func (s *Service) reviewTrade(ctx context.Context, raw json.RawMessage, caller sidecar.KernelCaller) (router.ReviewDecision, error) {
	if caller == nil {
		return s.reviewFallbackDecision("central review unavailable: kernel caller unavailable"), nil
	}

	req := protocol.SpecialistToolCallRequest{
		TargetKind: agent.KindCentral,
		ToolName:   quantcontracts.ToolCentralReviewTrade,
		Arguments:  append(json.RawMessage(nil), raw...),
	}
	var result protocol.ToolCallResult
	if err := caller.CallKernel(ctx, protocol.MethodSpecialistCallTool, req, &result); err != nil {
		return s.reviewFallbackDecision(fmt.Sprintf("central review unavailable: %v", err)), nil
	}
	if result.IsError {
		message := "central review returned tool error"
		if result.Error != nil && strings.TrimSpace(result.Error.Message) != "" {
			message = "central review returned tool error: " + strings.TrimSpace(result.Error.Message)
		}
		return s.reviewFallbackDecision(message), nil
	}

	var external quantcontracts.ReviewDecision
	if err := result.DecodeOutput(&external); err != nil {
		return s.reviewFallbackDecision(fmt.Sprintf("central review decode failed: %v", err)), nil
	}
	decision := router.ReviewDecision{
		Approved:   external.Approved,
		SizeFactor: external.SizeFactor,
		Reason:     external.Reason,
		Reviewer:   firstNonEmpty(external.ReasonCode, "central"),
		ReviewedAt: external.ReviewedAtMillis,
	}
	if decision.Reviewer == "" {
		decision.Reviewer = "central"
	}
	if decision.ReviewedAt == 0 {
		decision.ReviewedAt = time.Now().UTC().UnixMilli()
	}
	return decision, nil
}

func (s *Service) reviewFallbackDecision(reason string) router.ReviewDecision {
	reason = firstNonEmpty(reason, "central review unavailable")
	decision := router.ReviewDecision{
		Approved:   false,
		SizeFactor: 0,
		Reason:     reason,
		Reviewer:   "review-fallback-reject",
		ReviewedAt: time.Now().UTC().UnixMilli(),
	}
	if s != nil && s.reviewMode == reviewFallbackApprove {
		decision.Approved = true
		decision.SizeFactor = 1
		decision.Reviewer = "review-fallback-approve"
	}
	return decision
}

func (s *Service) applyGlobalRiskGuard(plan router.DispatchPlan, portfolio view.PortfolioView, stage string) router.DispatchPlan {
	if plan.RejectedStage != "" || len(plan.Candidates) == 0 {
		return plan
	}
	decision := s.evaluateGlobalRisk(plan, portfolio)
	if decision.Allowed {
		return plan
	}
	plan.RejectedStage = stage
	plan.RejectionReason = decision.Reason
	plan.ReviewRequired = false
	plan.Candidates = nil
	return plan
}

func (s *Service) evaluateGlobalRisk(plan router.DispatchPlan, portfolio view.PortfolioView) risk.Decision {
	equity := portfolio.TotalEquity
	if equity <= 0 {
		return risk.Decision{Allowed: false, Layer: "global", Reason: "total equity is zero", Action: "reject"}
	}
	if portfolio.OpenPositions >= s.guard.MaxConcurrentPositions {
		return risk.Decision{Allowed: false, Layer: "global", Reason: "too many concurrent positions", Action: "reject"}
	}
	if portfolio.DailyLossPct >= s.globalRisk.MaxGlobalDailyLoss {
		return risk.Decision{Allowed: false, Layer: "global", Reason: "global daily loss exceeded", Action: "reject"}
	}

	// PortfolioView still only exposes aggregate state, so we conservatively
	// treat the largest existing position as already consuming exposure budget.
	existingLargest := equity * math.Max(portfolio.LargestPositionPct, 0) / 100
	proposedExposure := 0.0
	proposedSymbolExposure := 0.0
	proposedSameDirection := 0.0

	for _, candidate := range plan.Candidates {
		req := risk.OrderRequest{
			Symbol:        candidate.Symbol,
			Action:        risk.ActionOpen,
			Direction:     strategyDirectionFromSide(candidate.Side),
			EntryPrice:    candidate.EntryPrice,
			StopLoss:      candidate.StopLossPrice,
			Quantity:      candidate.Quantity,
			Notional:      math.Abs(candidate.Quantity * candidate.EntryPrice),
			Leverage:      1,
			AccountEquity: equity,
		}
		if decision := s.guard.CheckOrder(req, risk.PortfolioSnapshot{Equity: equity}); !decision.Allowed {
			decision.Layer = "global"
			return decision
		}

		proposedExposure += req.Notional
		if strings.EqualFold(candidate.Symbol, plan.Symbol) {
			proposedSymbolExposure += req.Notional
		}
		if req.Direction == plan.Direction {
			proposedSameDirection += req.Notional
		}
	}

	totalExposurePct := (existingLargest + proposedExposure) / equity * 100
	if totalExposurePct > s.globalRisk.MaxGlobalExposurePct {
		return risk.Decision{Allowed: false, Layer: "global", Reason: "global exposure exceeded", Action: "reject"}
	}
	sameDirectionPct := (existingLargest + proposedSameDirection) / equity * 100
	if sameDirectionPct > s.globalRisk.MaxGlobalSameDirection {
		return risk.Decision{Allowed: false, Layer: "global", Reason: "global same-direction exposure exceeded", Action: "reject"}
	}
	symbolExposurePct := proposedSymbolExposure / equity * 100
	if symbolExposurePct > s.globalRisk.MaxSymbolExposure {
		return risk.Decision{Allowed: false, Layer: "global", Reason: "symbol exposure exceeded", Action: "reject"}
	}

	return risk.Decision{Allowed: true, Layer: "global", Action: "allow"}
}

func (s *Service) BuildPlan(ctx context.Context, symbol string, caller sidecar.KernelCaller) (router.DispatchPlan, audit.SignalTrace, error) {
	snapshot, ok := s.views.Snapshot(symbol)
	if !ok {
		return router.DispatchPlan{}, audit.SignalTrace{}, fmt.Errorf("snapshot not found: %s", symbol)
	}

	portfolio := s.effectivePortfolioView()
	reviewCtx := strategy.ReviewContext{
		OpenPositions:      portfolio.OpenPositions,
		LargestPositionPct: portfolio.LargestPositionPct,
		DailyLossPct:       portfolio.DailyLossPct,
	}

	signals := s.pool.Compute(snapshot)
	agg := s.aggregator.Aggregate(snapshot, signals, reviewCtx)
	plan := s.router.BuildDispatchPlan(snapshot, agg, portfolio)
	if containsSymbol(portfolio.PausedInstruments, symbol) {
		plan.ReviewRequired = false
		plan.RejectionReason = "instrument paused"
		plan.RejectedStage = "control"
		plan.Candidates = nil
	}
	plan = s.applyGlobalRiskGuard(plan, portfolio, "global_risk_pre")

	reviewRequested := plan.ReviewRequired
	trace := s.traceFromPlan(snapshot, plan, agg)
	if plan.ReviewRequired {
		decision, err := s.reviewTrade(ctx, mustRawJSON(s.reviewRequestFromPlan(plan, snapshot)), caller)
		if err != nil {
			return plan, trace, err
		}
		plan = s.router.ApplyReviewDecision(plan, decision)
		plan = s.applyGlobalRiskGuard(plan, portfolio, "global_risk_post")
		trace = s.traceFromPlan(snapshot, plan, agg)
		trace.ReviewRequested = reviewRequested
		trace.ReviewApproved = decision.Approved
		trace.ReviewReason = decision.Reason
		trace.ReviewSizeFactor = decision.SizeFactor
	} else {
		trace.ReviewRequested = reviewRequested
	}
	_ = s.audits.Save(ctx, &trace)
	return plan, trace, nil
}

func (s *Service) RunCycle(ctx context.Context, symbol string, caller sidecar.KernelCaller) (DispatchOutcome, error) {
	plan, trace, err := s.BuildPlan(ctx, symbol, caller)
	outcome := DispatchOutcome{
		Plan:  plan,
		Trace: trace,
	}
	if err != nil {
		return outcome, err
	}
	if len(plan.Candidates) == 0 {
		return outcome, nil
	}

	results, err := s.dispatchPlan(ctx, plan)
	outcome.Results = results
	trace.AccountResults = mustRawJSON(results)
	trace.UpdatedAt = time.Now().UTC().UnixMilli()
	if err != nil {
		trace.Outcome = "dispatch_failed"
		trace.RejectedStage = firstNonEmpty(trace.RejectedStage, "execute")
		trace.Reason = err.Error()
		s.markPlanAccountsRecovering(plan, err.Error())
		report, recoverErr := s.Recover(ctx)
		outcome.Recovery = report
		if recoverErr != nil {
			err = fmt.Errorf("%w; recovery: %v", err, recoverErr)
		}
		_ = s.audits.Save(ctx, &trace)
		outcome.Trace = trace
		return outcome, err
	}

	trace.Outcome = executionOutcome(results)
	_ = s.audits.Save(ctx, &trace)
	outcome.Trace = trace
	return outcome, nil
}

func (s *Service) traceQuerySnapshot(symbol string, limit int) []audit.SignalTrace {
	traces, _ := s.audits.Query(context.Background(), audit.QueryFilter{Symbol: symbol, Limit: limit})
	return traces
}

func (s *Service) TraceQuery(ctx context.Context, symbol string, limit int) ([]audit.SignalTrace, error) {
	return s.audits.Query(ctx, audit.QueryFilter{Symbol: symbol, Limit: limit})
}

func (s *Service) globalPortfolioView() view.PortfolioView {
	return s.effectivePortfolioView()
}

func (s *Service) setTradingPaused(paused bool, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runtime.TradingPaused = paused
	s.runtime.LastAction = reason
}

func (s *Service) setInstrumentPaused(symbol, reason string, paused bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runtime.PausedInstruments == nil {
		s.runtime.PausedInstruments = make(map[string]string)
	}
	if paused {
		s.runtime.PausedInstruments[strings.TrimSpace(symbol)] = strings.TrimSpace(reason)
	} else {
		delete(s.runtime.PausedInstruments, strings.TrimSpace(symbol))
	}
	s.runtime.LastAction = reason
}

func (s *Service) pausedInstrumentListLocked() []string {
	out := make([]string, 0, len(s.runtime.PausedInstruments))
	for symbol := range s.runtime.PausedInstruments {
		out = append(out, symbol)
	}
	return out
}

func (s *Service) newControlTrace(symbol, action, reason string) audit.SignalTrace {
	return audit.SignalTrace{
		Symbol:        symbol,
		Outcome:       action,
		RejectedStage: "control",
		Reason:        reason,
		CreatedAt:     time.Now().UTC().UnixMilli(),
	}
}

func (s *Service) newRecoveryTrace(report recovery.Report) audit.SignalTrace {
	return audit.SignalTrace{
		Symbol:        "control",
		Outcome:       "recovery_completed",
		RejectedStage: "recovery",
		Reason:        report.Summary,
		CreatedAt:     report.CompletedAt,
		UpdatedAt:     report.CompletedAt,
	}
}

func (s *Service) reconcileSummary() string {
	portfolio := s.effectivePortfolioView()
	runtime := s.runtimeSnapshot()
	payload := map[string]any{
		"portfolio":          portfolio,
		"accounts":           s.accountSnapshots(),
		"snapshot_count":     len(s.views.SnapshotList()),
		"paused_trading":     runtime.TradingPaused,
		"paused_instruments": portfolio.PausedInstruments,
		"last_action":        runtime.LastAction,
		"last_recovery":      runtime.LastRecovery,
		"last_recovery_at":   runtime.LastRecoveryAt,
		"trace_count":        len(s.traceQuerySnapshot("", 0)),
	}
	return mustJSONString(payload)
}

func (s *Service) traceFromPlan(snapshot view.MarketSnapshot, plan router.DispatchPlan, agg strategy.AggregatedSignal) audit.SignalTrace {
	reviewApproved := plan.ReviewSizeFactor > 0 && len(plan.Candidates) > 0
	globalRiskAllowed, globalRiskReason := globalRiskStateFromPlan(plan)
	trace := audit.SignalTrace{
		TraceID:           traceIDForPlan(plan),
		Symbol:            plan.Symbol,
		SnapshotSeq:       plan.SnapshotSeq,
		Direction:         string(plan.Direction),
		Outcome:           traceOutcome(plan),
		Price:             snapshot.CurrentPrice(),
		Confidence:        plan.Confidence,
		DominantStrategy:  dominantStrategyFromSignals(agg.Signals, plan.Direction),
		GlobalRiskAllowed: globalRiskAllowed,
		GlobalRiskReason:  globalRiskReason,
		RejectedStage:     plan.RejectedStage,
		Reason:            firstNonEmpty(plan.RejectionReason, plan.ReviewReason),
		ReviewRequested:   plan.ReviewRequired,
		ReviewApproved:    reviewApproved,
		ReviewReason:      plan.ReviewReason,
		ReviewSizeFactor:  plan.ReviewSizeFactor,
		DraftCandidates:   mustRawJSON(contractCandidates(plan.Candidates)),
		CreatedAt:         plan.CreatedAt,
		UpdatedAt:         plan.CreatedAt,
	}
	trace.PlanJSON = mustRawJSON(plan)
	return trace
}

func (s *Service) reviewRequestFromPlan(plan router.DispatchPlan, snapshot view.MarketSnapshot) quantcontracts.ReviewTradeRequest {
	req := quantcontracts.ReviewTradeRequest{
		TraceID:   traceIDForPlan(plan),
		Symbol:    plan.Symbol,
		Direction: directionFromSide(string(plan.Direction)),
		Snapshot:  contractSnapshot(snapshot),
		Reason:    firstNonEmpty(plan.ReviewReason, plan.RejectionReason),
	}
	req.Candidates = contractCandidates(plan.Candidates)
	return req
}

func traceOutcome(plan router.DispatchPlan) string {
	switch {
	case plan.RejectedStage != "":
		return "rejected"
	case len(plan.Candidates) == 0:
		return "idle"
	default:
		return "planned"
	}
}

func dominantStrategyFromSignals(signals []strategy.Signal, direction strategy.Direction) string {
	bestName := ""
	bestConfidence := -1.0
	for _, signal := range signals {
		if direction != "" && direction != strategy.DirectionHold && signal.Direction != direction {
			continue
		}
		if signal.Confidence > bestConfidence {
			bestName = signal.Strategy
			bestConfidence = signal.Confidence
		}
	}
	if bestName != "" {
		return bestName
	}
	for _, signal := range signals {
		if signal.Confidence > bestConfidence {
			bestName = signal.Strategy
			bestConfidence = signal.Confidence
		}
	}
	return bestName
}

func globalRiskStateFromPlan(plan router.DispatchPlan) (bool, string) {
	switch plan.RejectedStage {
	case "global_risk_pre", "global_risk_post":
		return false, firstNonEmpty(plan.RejectionReason, "global risk rejected")
	case "aggregate", "control":
		return false, "not_evaluated"
	default:
		return true, ""
	}
}

func mustJSONString(v any) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}

func mustRawJSON(v any) json.RawMessage {
	raw, _ := json.Marshal(v)
	return append(json.RawMessage(nil), raw...)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func traceIDForPlan(plan router.DispatchPlan) string {
	return fmt.Sprintf("%s-%d", strings.ToLower(plan.Symbol), plan.SnapshotSeq)
}

func directionFromSide(side string) quantcontracts.Direction {
	switch strings.ToLower(strings.TrimSpace(side)) {
	case "sell", "short":
		return quantcontracts.DirectionShort
	case "buy", "long":
		return quantcontracts.DirectionLong
	default:
		return quantcontracts.DirectionFlat
	}
}

func accountIDsFromSnapshots(accounts []qexec.AccountSnapshot) []string {
	out := make([]string, 0, len(accounts))
	for _, account := range accounts {
		if id := strings.TrimSpace(account.AccountID); id != "" {
			out = append(out, id)
		}
	}
	if len(out) == 0 {
		out = append(out, "paper")
	}
	return out
}

func normalizeReviewFallbackPolicy(policy string) reviewFallbackPolicy {
	switch strings.ToLower(strings.TrimSpace(policy)) {
	case string(reviewFallbackApprove):
		return reviewFallbackApprove
	default:
		return reviewFallbackReject
	}
}

func defaultGlobalRiskConfig() GlobalRiskConfig {
	return GlobalRiskConfig{
		MaxGlobalExposurePct:   50,
		MaxGlobalSameDirection: 30,
		MaxGlobalDailyLoss:     5,
		MaxSymbolExposure:      15,
	}
}

func normalizeGlobalRiskConfig(cfg GlobalRiskConfig) GlobalRiskConfig {
	defaults := defaultGlobalRiskConfig()
	if cfg.MaxGlobalExposurePct <= 0 {
		cfg.MaxGlobalExposurePct = defaults.MaxGlobalExposurePct
	}
	if cfg.MaxGlobalSameDirection <= 0 {
		cfg.MaxGlobalSameDirection = defaults.MaxGlobalSameDirection
	}
	if cfg.MaxGlobalDailyLoss <= 0 {
		cfg.MaxGlobalDailyLoss = defaults.MaxGlobalDailyLoss
	}
	if cfg.MaxSymbolExposure <= 0 {
		cfg.MaxSymbolExposure = defaults.MaxSymbolExposure
	}
	return cfg
}

func strategyDirectionFromSide(side string) strategy.Direction {
	switch strings.ToLower(strings.TrimSpace(side)) {
	case "sell", "short":
		return strategy.DirectionShort
	case "buy", "long":
		return strategy.DirectionLong
	default:
		return strategy.DirectionHold
	}
}

func (s *Service) dispatchPlan(ctx context.Context, plan router.DispatchPlan) ([]coreexec.ExecutionResult, error) {
	if s.executor == nil {
		return nil, fmt.Errorf("executor is not configured")
	}
	return s.executor.ExecutePlan(ctx, quantcontracts.DispatchPlan{
		TraceID:    traceIDForPlan(plan),
		Symbol:     plan.Symbol,
		Direction:  directionFromSide(string(plan.Direction)),
		Snapshot:   contractSnapshotForSymbol(s.views, plan.Symbol),
		Candidates: contractCandidates(plan.Candidates),
		Review: &quantcontracts.ReviewDecision{
			Approved:   plan.RejectedStage != "review",
			Reason:     plan.ReviewReason,
			SizeFactor: plan.ReviewSizeFactor,
		},
	})
}

func (s *Service) Recover(ctx context.Context) (recovery.Report, error) {
	if s.recovery == nil {
		return recovery.Report{}, fmt.Errorf("recovery is not configured")
	}
	report, err := s.recovery.Reconcile(ctx)
	if err == nil {
		s.recordRecovery(report)
		trace := s.newRecoveryTrace(report)
		_ = s.audits.Save(ctx, &trace)
	}
	return report, err
}

func (s *Service) RestoreRuntime(ctx context.Context) error {
	if s == nil || s.audits == nil {
		return nil
	}

	traces, err := s.audits.Query(ctx, audit.QueryFilter{})
	if err != nil {
		return fmt.Errorf("restore runtime from audit traces: %w", err)
	}

	restored := runtimeState{
		PausedInstruments: make(map[string]string),
	}
	sort.SliceStable(traces, func(i, j int) bool {
		ti := traceTimestamp(traces[i])
		tj := traceTimestamp(traces[j])
		if ti == tj {
			if traces[i].CreatedAt == traces[j].CreatedAt {
				return traces[i].TraceID < traces[j].TraceID
			}
			return traces[i].CreatedAt < traces[j].CreatedAt
		}
		return ti < tj
	})
	for _, trace := range traces {
		applyRuntimeTrace(&restored, trace)
	}

	s.mu.Lock()
	s.runtime = restored
	s.mu.Unlock()
	return nil
}

func (s *Service) effectivePortfolioView() view.PortfolioView {
	portfolio := s.views.Portfolio()
	runtime := s.runtimeSnapshot()
	if runtime.TradingPaused {
		portfolio.PausedTrading = true
	}
	portfolio.PausedInstruments = mergeSymbols(portfolio.PausedInstruments, s.pausedInstrumentList(runtime))
	return portfolio
}

func (s *Service) runtimeSnapshot() runtimeState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	clone := s.runtime
	if len(s.runtime.PausedInstruments) > 0 {
		clone.PausedInstruments = make(map[string]string, len(s.runtime.PausedInstruments))
		for symbol, reason := range s.runtime.PausedInstruments {
			clone.PausedInstruments[symbol] = reason
		}
	}
	return clone
}

func (s *Service) accountSnapshots() []qexec.AccountSnapshot {
	if s.executor == nil {
		return nil
	}
	return s.executor.Accounts()
}

func (s *Service) applyAccountAction(reason string, fn func(accountID, reason string) bool) {
	if s.executor == nil || fn == nil {
		return
	}
	for _, account := range s.executor.Accounts() {
		fn(account.AccountID, reason)
	}
}

func (s *Service) markPlanAccountsRecovering(plan router.DispatchPlan, reason string) {
	if s.executor == nil {
		return
	}
	seen := make(map[string]struct{}, len(plan.Candidates))
	for _, candidate := range plan.Candidates {
		accountID := strings.TrimSpace(candidate.AccountID)
		if accountID == "" {
			continue
		}
		if _, ok := seen[accountID]; ok {
			continue
		}
		seen[accountID] = struct{}{}
		s.executor.MarkRecovering(accountID, reason)
	}
}

func (s *Service) recordRecovery(report recovery.Report) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runtime.LastRecovery = report.Summary
	s.runtime.LastRecoveryAt = report.CompletedAt
}

func (s *Service) pausedInstrumentList(runtime runtimeState) []string {
	out := make([]string, 0, len(runtime.PausedInstruments))
	for symbol := range runtime.PausedInstruments {
		out = append(out, symbol)
	}
	return out
}

func applyRuntimeTrace(state *runtimeState, trace audit.SignalTrace) {
	if state == nil {
		return
	}
	if state.PausedInstruments == nil {
		state.PausedInstruments = make(map[string]string)
	}

	outcome := strings.ToLower(strings.TrimSpace(trace.Outcome))
	reason := firstNonEmpty(trace.Reason, trace.Outcome)
	switch outcome {
	case "pause_trading":
		state.TradingPaused = true
		state.LastAction = reason
	case "resume_trading":
		state.TradingPaused = false
		state.LastAction = reason
	case "pause_instrument":
		symbol := strings.TrimSpace(trace.Symbol)
		if symbol != "" {
			state.PausedInstruments[symbol] = reason
		}
		state.LastAction = reason
	case "resume_instrument":
		symbol := strings.TrimSpace(trace.Symbol)
		if symbol != "" {
			delete(state.PausedInstruments, symbol)
		}
		state.LastAction = reason
	case "recovery_completed", "recovery_failed":
		state.LastRecovery = reason
		state.LastRecoveryAt = traceTimestamp(trace)
	}
}

func traceTimestamp(trace audit.SignalTrace) int64 {
	if trace.CreatedAt > 0 {
		return trace.CreatedAt
	}
	return trace.UpdatedAt
}

func contractSnapshotForSymbol(store *view.Store, symbol string) *quantcontracts.MarketSnapshot {
	if store == nil {
		return nil
	}
	snapshot, ok := store.Snapshot(symbol)
	if !ok {
		return nil
	}
	return contractSnapshot(snapshot)
}

func contractSnapshot(snapshot view.MarketSnapshot) *quantcontracts.MarketSnapshot {
	return &quantcontracts.MarketSnapshot{
		Version:         "v1",
		Sequence:        snapshot.WriteSeqValue,
		Provider:        "data",
		Symbol:          snapshot.Symbol(),
		TimestampMillis: snapshot.TimestampValue,
		Last:            snapshot.CurrentPrice(),
		Mark:            snapshot.CurrentPrice(),
		FundingRate:     snapshot.FundingRate(),
		FeatureVector:   snapshot.FeatureVector(),
	}
}

func contractCandidates(candidates []router.DispatchCandidate) []quantcontracts.DispatchCandidate {
	out := make([]quantcontracts.DispatchCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		out = append(out, quantcontracts.DispatchCandidate{
			AccountID:    candidate.AccountID,
			Symbol:       candidate.Symbol,
			Direction:    directionFromSide(candidate.Side),
			ProposedQty:  candidate.Quantity,
			Allowed:      candidate.Quantity > 0,
			RiskReason:   candidate.Reason,
			WeightFactor: candidate.Weight,
		})
	}
	return out
}

func executionOutcome(results []coreexec.ExecutionResult) string {
	if len(results) == 0 {
		return "planned"
	}
	rejected := 0
	filled := 0
	for _, result := range results {
		switch strings.ToLower(result.Status) {
		case coreexec.OrderStatusFilled:
			filled++
		case coreexec.OrderStatusRejected:
			rejected++
		}
	}
	switch {
	case filled == len(results):
		return "executed"
	case rejected == len(results):
		return "rejected"
	default:
		return "partially_executed"
	}
}

func mergeSymbols(left, right []string) []string {
	if len(left) == 0 && len(right) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(left)+len(right))
	out := make([]string, 0, len(left)+len(right))
	for _, symbol := range append(append([]string(nil), left...), right...) {
		symbol = strings.TrimSpace(symbol)
		if symbol == "" {
			continue
		}
		if _, ok := seen[symbol]; ok {
			continue
		}
		seen[symbol] = struct{}{}
		out = append(out, symbol)
	}
	return out
}

func containsSymbol(symbols []string, target string) bool {
	target = strings.TrimSpace(target)
	if target == "" {
		return false
	}
	for _, symbol := range symbols {
		if strings.EqualFold(strings.TrimSpace(symbol), target) {
			return true
		}
	}
	return false
}
