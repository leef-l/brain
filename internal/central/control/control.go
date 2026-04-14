package control

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/leef-l/brain/internal/quantcontracts"
)

type Config struct {
	AutoPauseOnCriticalAlert bool
	AutoPauseOnAccountError  bool
	DefaultPauseReason       string
}

type State struct {
	TradingPaused        bool              `json:"trading_paused"`
	PauseReason          string            `json:"pause_reason,omitempty"`
	PausedInstruments    map[string]string `json:"paused_instruments,omitempty"`
	LastAction           string            `json:"last_action,omitempty"`
	LastReason           string            `json:"last_reason,omitempty"`
	LastConfigScope      string            `json:"last_config_scope,omitempty"`
	LastConfigPatch      json.RawMessage   `json:"last_config_patch,omitempty"`
	LastReviewOutcome    string            `json:"last_review_outcome,omitempty"`
	LastReviewReason     string            `json:"last_review_reason,omitempty"`
	LastReviewSizeFactor float64           `json:"last_review_size_factor,omitempty"`
	UpdatedAtMS          int64             `json:"updated_at_ms,omitempty"`
}

type EmergencyActionRequest struct {
	Action string `json:"action"`
	Symbol string `json:"symbol,omitempty"`
	Reason string `json:"reason,omitempty"`
}

type ConfigUpdateRequest struct {
	Scope  string          `json:"scope,omitempty"`
	Reason string          `json:"reason,omitempty"`
	Patch  json.RawMessage `json:"patch,omitempty"`
}

type Result struct {
	Action string `json:"action"`
	OK     bool   `json:"ok"`
	Reason string `json:"reason,omitempty"`
	State  State  `json:"state"`
}

type Service struct {
	mu  sync.RWMutex
	cfg Config
	st  State
	now func() time.Time
}

func New(cfg Config) *Service {
	if cfg.DefaultPauseReason == "" {
		cfg.DefaultPauseReason = "central control skeleton"
	}
	return &Service{
		cfg: cfg,
		st: State{
			PausedInstruments: make(map[string]string),
		},
		now: time.Now,
	}
}

func (s *Service) State() State {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneState(s.st)
}

func (s *Service) RestoreState(state State) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.st = cloneState(state)
	if s.st.PausedInstruments == nil {
		s.st.PausedInstruments = make(map[string]string)
	}
}

func (s *Service) PauseTrading(reason string) Result {
	return s.apply("pause_trading", reason, func(st *State) {
		st.TradingPaused = true
		st.PauseReason = firstNonEmpty(reason, s.cfg.DefaultPauseReason)
	})
}

func (s *Service) ResumeTrading(reason string) Result {
	return s.apply("resume_trading", reason, func(st *State) {
		st.TradingPaused = false
		st.PauseReason = firstNonEmpty(reason, "")
	})
}

func (s *Service) PauseInstrument(symbol, reason string) Result {
	symbol = strings.TrimSpace(symbol)
	if symbol == "" {
		return Result{Action: "pause_instrument", OK: false, Reason: "symbol is required", State: s.State()}
	}
	return s.apply("pause_instrument", reason, func(st *State) {
		st.PausedInstruments[symbol] = firstNonEmpty(reason, s.cfg.DefaultPauseReason)
	})
}

func (s *Service) ResumeInstrument(symbol, reason string) Result {
	symbol = strings.TrimSpace(symbol)
	if symbol == "" {
		return Result{Action: "resume_instrument", OK: false, Reason: "symbol is required", State: s.State()}
	}
	return s.apply("resume_instrument", reason, func(st *State) {
		delete(st.PausedInstruments, symbol)
	})
}

func (s *Service) ApplyDataAlert(ctx context.Context, alert quantcontracts.DataAlert) (Result, error) {
	_ = ctx
	reason := firstNonEmpty(alert.Reason, alert.Type)
	if shouldAutoPause(alert.Level, s.cfg.AutoPauseOnCriticalAlert) {
		if alert.Symbol != "" {
			return s.PauseInstrument(alert.Symbol, reason), nil
		}
		return s.PauseTrading(reason), nil
	}
	return s.record("data_alert", reason, func(st *State) {}), nil
}

func (s *Service) ApplyAccountError(ctx context.Context, err quantcontracts.AccountError) (Result, error) {
	_ = ctx
	reason := firstNonEmpty(err.Message, err.Code)
	if s.cfg.AutoPauseOnAccountError && !err.Recovering {
		return s.PauseTrading(reason), nil
	}
	return s.record("account_error", reason, func(st *State) {}), nil
}

func (s *Service) ApplyMacroEvent(ctx context.Context, event quantcontracts.MacroEvent) (Result, error) {
	_ = ctx
	return s.record("macro_event", firstNonEmpty(event.Summary, event.EventType), func(st *State) {}), nil
}

func (s *Service) ApplyEmergencyAction(ctx context.Context, req EmergencyActionRequest) (Result, error) {
	_ = ctx
	action := strings.ToLower(strings.TrimSpace(req.Action))
	reason := firstNonEmpty(req.Reason, action, s.cfg.DefaultPauseReason)
	symbol := strings.TrimSpace(req.Symbol)

	switch action {
	case "pause_trading":
		return s.record(action, reason, func(st *State) {
			st.TradingPaused = true
			st.PauseReason = reason
		}), nil
	case "resume_trading":
		return s.record(action, reason, func(st *State) {
			st.TradingPaused = false
			st.PauseReason = ""
		}), nil
	case "pause_instrument":
		if symbol == "" {
			return Result{Action: action, OK: false, Reason: "symbol is required", State: s.State()}, nil
		}
		return s.record(action, reason, func(st *State) {
			if st.PausedInstruments == nil {
				st.PausedInstruments = make(map[string]string)
			}
			st.PausedInstruments[symbol] = reason
		}), nil
	case "resume_instrument":
		if symbol == "" {
			return Result{Action: action, OK: false, Reason: "symbol is required", State: s.State()}, nil
		}
		return s.record(action, reason, func(st *State) {
			delete(st.PausedInstruments, symbol)
		}), nil
	default:
		return Result{Action: "emergency_action", OK: false, Reason: "unsupported action: " + req.Action, State: s.State()}, nil
	}
}

func (s *Service) ApplyConfigUpdate(ctx context.Context, req ConfigUpdateRequest) (Result, error) {
	_ = ctx
	scope := strings.TrimSpace(req.Scope)
	reason := firstNonEmpty(req.Reason, scope, "config update applied")
	patch := append(json.RawMessage(nil), req.Patch...)
	return s.record("update_config", reason, func(st *State) {
		st.LastConfigScope = scope
		st.LastConfigPatch = patch
	}), nil
}

func (s *Service) RecordReviewOutcome(approved bool, reason string, sizeFactor float64) Result {
	outcome := "rejected"
	if approved {
		outcome = "approved"
	}
	return s.record("review_decision", reason, func(st *State) {
		st.LastReviewOutcome = outcome
		st.LastReviewReason = reason
		st.LastReviewSizeFactor = sizeFactor
	})
}

func (s *Service) apply(action, reason string, mutate func(*State)) Result {
	return s.record(action, reason, mutate)
}

func (s *Service) record(action, reason string, mutate func(*State)) Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.st.PausedInstruments == nil {
		s.st.PausedInstruments = make(map[string]string)
	}
	mutate(&s.st)
	s.st.LastAction = action
	s.st.LastReason = reason
	s.st.UpdatedAtMS = s.now().UTC().UnixMilli()
	return Result{
		Action: action,
		OK:     true,
		Reason: reason,
		State:  cloneState(s.st),
	}
}

func cloneState(st State) State {
	clone := st
	if len(st.PausedInstruments) > 0 {
		clone.PausedInstruments = make(map[string]string, len(st.PausedInstruments))
		for k, v := range st.PausedInstruments {
			clone.PausedInstruments[k] = v
		}
	}
	if len(st.LastConfigPatch) > 0 {
		clone.LastConfigPatch = append(json.RawMessage(nil), st.LastConfigPatch...)
	}
	return clone
}

func shouldAutoPause(level string, enabled bool) bool {
	if !enabled {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "critical", "fatal", "panic":
		return true
	default:
		return false
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
