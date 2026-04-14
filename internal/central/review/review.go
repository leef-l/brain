package review

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/leef-l/brain/internal/quantcontracts"
)

type Request = quantcontracts.ReviewTradeRequest
type Decision = quantcontracts.ReviewDecision

type Config struct {
	DefaultApproved   bool
	DefaultReason     string
	DefaultSizeFactor float64
}

type State struct {
	LastRequest  Request  `json:"last_request,omitempty"`
	LastDecision Decision `json:"last_decision,omitempty"`
	LastRunAtMS  int64    `json:"last_run_at_ms,omitempty"`
}

type Service struct {
	mu    sync.RWMutex
	cfg   Config
	state State
	now   func() time.Time
}

func New(cfg Config) *Service {
	if cfg.DefaultReason == "" {
		cfg.DefaultReason = "central review skeleton"
	}
	if cfg.DefaultSizeFactor <= 0 {
		cfg.DefaultSizeFactor = 1
	}
	return &Service{
		cfg: cfg,
		now: time.Now,
	}
}

func (s *Service) Evaluate(ctx context.Context, req Request) (Decision, error) {
	_ = ctx
	decision := Decision{
		Approved:         s.cfg.DefaultApproved,
		Reason:           s.cfg.DefaultReason,
		ReasonCode:       "reviewed",
		SizeFactor:       s.cfg.DefaultSizeFactor,
		ReviewedAtMillis: s.now().UTC().UnixMilli(),
	}
	if strings.TrimSpace(req.Symbol) == "" {
		decision.Approved = false
		decision.Reason = "symbol is required"
		decision.ReasonCode = "invalid_request"
		decision.SizeFactor = 0
		s.record(req, decision)
		return decision, nil
	}
	if strings.TrimSpace(string(req.Direction)) == "" {
		decision.Approved = false
		decision.Reason = fmt.Sprintf("direction is required for %s", req.Symbol)
		decision.ReasonCode = "invalid_request"
		decision.SizeFactor = 0
		s.record(req, decision)
		return decision, nil
	}
	if len(req.Candidates) == 0 {
		decision.Approved = false
		decision.Reason = "no candidates to review"
		decision.ReasonCode = "empty_plan"
		decision.SizeFactor = 0
		s.record(req, decision)
		return decision, nil
	}

	decision.Approved = true
	decision.Reason = firstNonEmpty(decision.Reason, "review accepted")
	decision.ReasonCode = "approved"
	decision.SizeFactor = normalizeFactor(decision.SizeFactor)
	if decision.SizeFactor == 0 {
		decision.SizeFactor = 1
	}
	s.record(req, decision)
	return decision, nil
}

func (s *Service) State() State {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneState(s.state)
}

func (s *Service) RestoreState(state State) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = cloneState(state)
}

func (s *Service) record(req Request, decision Decision) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.LastRequest = cloneRequest(req)
	s.state.LastDecision = cloneDecision(decision)
	s.state.LastRunAtMS = s.now().UTC().UnixMilli()
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func normalizeFactor(v float64) float64 {
	if v <= 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func cloneState(state State) State {
	return State{
		LastRequest:  cloneRequest(state.LastRequest),
		LastDecision: cloneDecision(state.LastDecision),
		LastRunAtMS:  state.LastRunAtMS,
	}
}

func cloneRequest(req Request) Request {
	out := req
	if req.Snapshot != nil {
		snapshot := *req.Snapshot
		if len(req.Snapshot.FeatureVector) > 0 {
			snapshot.FeatureVector = append([]float64(nil), req.Snapshot.FeatureVector...)
		}
		if len(req.Snapshot.Quality.Warnings) > 0 {
			snapshot.Quality.Warnings = append([]string(nil), req.Snapshot.Quality.Warnings...)
		}
		out.Snapshot = &snapshot
	}
	if len(req.Candidates) > 0 {
		out.Candidates = append([]quantcontracts.DispatchCandidate(nil), req.Candidates...)
	}
	return out
}

func cloneDecision(decision Decision) Decision {
	out := decision
	if len(decision.Actions) > 0 {
		out.Actions = append([]string(nil), decision.Actions...)
	}
	return out
}
