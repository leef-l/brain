package central

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	brainerrors "github.com/leef-l/brain/errors"
	"github.com/leef-l/brain/internal/central/control"
	"github.com/leef-l/brain/internal/central/review"
	"github.com/leef-l/brain/internal/central/reviewrun"
	"github.com/leef-l/brain/internal/quantcontracts"
	"github.com/leef-l/brain/persistence"
)

// Service wires review, control, and reviewrun together without transport code.
type Service struct {
	review  *review.Service
	control *control.Service
	runner  *reviewrun.Runner
	store   persistence.CentralStateStore
}

func New(cfg Config) *Service {
	reviewer := review.New(cfg.Review)
	controller := control.New(cfg.Control)
	return &Service{
		review:  reviewer,
		control: controller,
		runner:  reviewrun.New(reviewer, controller),
		store:   cfg.StateStore,
	}
}

func (s *Service) ReviewTrade(ctx context.Context, req quantcontracts.ReviewTradeRequest) (quantcontracts.ReviewDecision, error) {
	result, err := s.runner.Run(ctx, reviewrun.Request{Trade: req})
	if err != nil {
		return quantcontracts.ReviewDecision{}, err
	}
	if err := s.persistState(ctx); err != nil {
		return quantcontracts.ReviewDecision{}, err
	}
	return result.Decision, nil
}

func (s *Service) DataAlert(ctx context.Context, alert quantcontracts.DataAlert) (control.Result, error) {
	result, err := s.control.ApplyDataAlert(ctx, alert)
	if err != nil {
		return control.Result{}, err
	}
	if err := s.persistState(ctx); err != nil {
		return control.Result{}, err
	}
	return result, nil
}

func (s *Service) AccountError(ctx context.Context, err quantcontracts.AccountError) (control.Result, error) {
	result, applyErr := s.control.ApplyAccountError(ctx, err)
	if applyErr != nil {
		return control.Result{}, applyErr
	}
	if err := s.persistState(ctx); err != nil {
		return control.Result{}, err
	}
	return result, nil
}

func (s *Service) MacroEvent(ctx context.Context, event quantcontracts.MacroEvent) (control.Result, error) {
	result, err := s.control.ApplyMacroEvent(ctx, event)
	if err != nil {
		return control.Result{}, err
	}
	if err := s.persistState(ctx); err != nil {
		return control.Result{}, err
	}
	return result, nil
}

func (s *Service) EmergencyAction(ctx context.Context, req control.EmergencyActionRequest) (control.Result, error) {
	result, err := s.control.ApplyEmergencyAction(ctx, req)
	if err != nil {
		return control.Result{}, err
	}
	if err := s.persistState(ctx); err != nil {
		return control.Result{}, err
	}
	return result, nil
}

func (s *Service) UpdateConfig(ctx context.Context, req control.ConfigUpdateRequest) (control.Result, error) {
	result, err := s.control.ApplyConfigUpdate(ctx, req)
	if err != nil {
		return control.Result{}, err
	}
	if err := s.persistState(ctx); err != nil {
		return control.Result{}, err
	}
	return result, nil
}

func (s *Service) RunReview(ctx context.Context, req reviewrun.Request) (reviewrun.Result, error) {
	result, err := s.runner.Run(ctx, req)
	if err != nil {
		return reviewrun.Result{}, err
	}
	if err := s.persistState(ctx); err != nil {
		return reviewrun.Result{}, err
	}
	return result, nil
}

func (s *Service) State() State {
	return State{
		Review:  s.review.State(),
		Control: s.control.State(),
	}
}

func (s *Service) Health() Health {
	state := s.State()
	ready := true
	reason := ""
	if state.Control.TradingPaused {
		ready = false
		reason = "trading paused"
	}
	return Health{
		Ready:  ready,
		Reason: reason,
		State:  state,
	}
}

func (s *Service) RestoreState(ctx context.Context) error {
	if s == nil || s.store == nil {
		return nil
	}

	stored, err := s.store.Get(ctx)
	if err != nil {
		if be, ok := err.(*brainerrors.BrainError); ok && be.ErrorCode == brainerrors.CodeRecordNotFound {
			return nil
		}
		return fmt.Errorf("load central state: %w", err)
	}

	state, err := decodeStoredState(stored)
	if err != nil {
		return err
	}
	s.review.RestoreState(state.Review)
	s.control.RestoreState(state.Control)
	return nil
}

func (s *Service) persistState(ctx context.Context) error {
	if s == nil || s.store == nil {
		return nil
	}

	stored, err := encodeStoredState(s.State())
	if err != nil {
		return err
	}
	if err := s.store.Save(ctx, stored); err != nil {
		return fmt.Errorf("save central state: %w", err)
	}
	return nil
}

func encodeStoredState(state State) (*persistence.CentralState, error) {
	requestRaw, err := json.Marshal(state.Review.LastRequest)
	if err != nil {
		return nil, fmt.Errorf("encode central review request: %w", err)
	}
	decisionRaw, err := json.Marshal(state.Review.LastDecision)
	if err != nil {
		return nil, fmt.Errorf("encode central review decision: %w", err)
	}
	return &persistence.CentralState{
		Control: persistence.CentralControlState{
			TradingPaused:        state.Control.TradingPaused,
			PauseReason:          state.Control.PauseReason,
			PausedInstruments:    clonePausedInstruments(state.Control.PausedInstruments),
			LastAction:           state.Control.LastAction,
			LastReason:           state.Control.LastReason,
			LastConfigScope:      state.Control.LastConfigScope,
			LastConfigPatch:      append(json.RawMessage(nil), state.Control.LastConfigPatch...),
			LastReviewOutcome:    state.Control.LastReviewOutcome,
			LastReviewReason:     state.Control.LastReviewReason,
			LastReviewSizeFactor: state.Control.LastReviewSizeFactor,
			UpdatedAtMS:          state.Control.UpdatedAtMS,
		},
		Review: persistence.CentralReviewState{
			LastRequest:  requestRaw,
			LastDecision: decisionRaw,
			LastRunAtMS:  state.Review.LastRunAtMS,
		},
		UpdatedAt: time.Now().UTC(),
	}, nil
}

func decodeStoredState(stored *persistence.CentralState) (State, error) {
	if stored == nil {
		return State{}, nil
	}

	state := State{
		Control: control.State{
			TradingPaused:        stored.Control.TradingPaused,
			PauseReason:          stored.Control.PauseReason,
			PausedInstruments:    clonePausedInstruments(stored.Control.PausedInstruments),
			LastAction:           stored.Control.LastAction,
			LastReason:           stored.Control.LastReason,
			LastConfigScope:      stored.Control.LastConfigScope,
			LastConfigPatch:      append(json.RawMessage(nil), stored.Control.LastConfigPatch...),
			LastReviewOutcome:    stored.Control.LastReviewOutcome,
			LastReviewReason:     stored.Control.LastReviewReason,
			LastReviewSizeFactor: stored.Control.LastReviewSizeFactor,
			UpdatedAtMS:          stored.Control.UpdatedAtMS,
		},
		Review: review.State{
			LastRunAtMS: stored.Review.LastRunAtMS,
		},
	}
	if state.Control.PausedInstruments == nil {
		state.Control.PausedInstruments = make(map[string]string)
	}
	if len(stored.Review.LastRequest) > 0 {
		if err := json.Unmarshal(stored.Review.LastRequest, &state.Review.LastRequest); err != nil {
			return State{}, fmt.Errorf("decode central review request: %w", err)
		}
	}
	if len(stored.Review.LastDecision) > 0 {
		if err := json.Unmarshal(stored.Review.LastDecision, &state.Review.LastDecision); err != nil {
			return State{}, fmt.Errorf("decode central review decision: %w", err)
		}
	}
	return state, nil
}

func clonePausedInstruments(src map[string]string) map[string]string {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]string, len(src))
	for symbol, reason := range src {
		dst[symbol] = reason
	}
	return dst
}
