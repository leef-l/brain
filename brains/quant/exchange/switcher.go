package exchange

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// ModeConfig configures the trading mode switcher.
type ModeConfig struct {
	Mode         string // "paper" | "live"
	PaperBackend ExchangeBackend
	LiveBackend  ExchangeBackend
}

// AuditRecord records a mode switch event.
type AuditRecord struct {
	From      string
	To        string
	Time      time.Time
	Confirmed bool
	Error     string
}

// TradingModeSwitcher routes trading operations between paper and live backends.
type TradingModeSwitcher struct {
	mu            sync.RWMutex
	mode          string
	paper         ExchangeBackend
	live          ExchangeBackend
	audit         []AuditRecord
	liveArmed     bool
	liveArmExpiry time.Time
	confirmToken  string
	maxAudit      int
}

// NewTradingModeSwitcher creates a new switcher.
func NewTradingModeSwitcher(cfg ModeConfig) *TradingModeSwitcher {
	mode := cfg.Mode
	if mode == "" {
		mode = "paper"
	}
	return &TradingModeSwitcher{
		mode:     mode,
		paper:    cfg.PaperBackend,
		live:     cfg.LiveBackend,
		audit:    make([]AuditRecord, 0, 100),
		maxAudit: 1000,
	}
}

// SetLiveConfirmToken sets the token required to switch to live mode.
func (s *TradingModeSwitcher) SetLiveConfirmToken(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.confirmToken = token
}

// ArmLive arms the switcher for live mode transition (valid for 5 minutes).
func (s *TradingModeSwitcher) ArmLive(token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.confirmToken != "" && token != s.confirmToken {
		return fmt.Errorf("invalid live confirmation token")
	}
	s.liveArmed = true
	s.liveArmExpiry = time.Now().Add(5 * time.Minute)
	return nil
}

// Switch changes the trading mode.
// Switching to "live" requires prior confirmation via ArmLive.
func (s *TradingModeSwitcher) Switch(mode string) error {
	if mode != "paper" && mode != "live" {
		return fmt.Errorf("invalid mode %q, must be 'paper' or 'live'", mode)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.mode == mode {
		return nil
	}

	if mode == "live" {
		if s.live == nil {
			s.recordAudit(s.mode, mode, false, "live backend not configured")
			return fmt.Errorf("live backend not configured")
		}
		if !s.liveArmed || time.Now().After(s.liveArmExpiry) {
			s.recordAudit(s.mode, mode, false, "live mode requires confirmation")
			return fmt.Errorf("live mode requires confirmation: call ArmLive(token) first")
		}
		s.liveArmed = false
	}

	from := s.mode
	s.mode = mode
	s.recordAudit(from, mode, true, "")
	slog.Info("trading mode switched", "from", from, "to", mode)
	return nil
}

func (s *TradingModeSwitcher) recordAudit(from, to string, confirmed bool, err string) {
	rec := AuditRecord{
		From:      from,
		To:        to,
		Time:      time.Now(),
		Confirmed: confirmed,
		Error:     err,
	}
	s.audit = append(s.audit, rec)
	if len(s.audit) > s.maxAudit {
		s.audit = s.audit[len(s.audit)-s.maxAudit:]
	}
}

// Current returns the current trading mode.
func (s *TradingModeSwitcher) Current() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.mode
}

// IsLive returns true if currently in live mode.
func (s *TradingModeSwitcher) IsLive() bool {
	return s.Current() == "live"
}

// Execute routes an order to the current backend.
func (s *TradingModeSwitcher) Execute(ctx context.Context, req OrderRequest) (OrderResponse, error) {
	s.mu.RLock()
	backend := s.paper
	if s.mode == "live" {
		backend = s.live
	}
	s.mu.RUnlock()

	if backend == nil {
		return OrderResponse{}, fmt.Errorf("backend not available for mode %q", s.Current())
	}
	return backend.PlaceOrder(ctx, req)
}

// AuditLog returns a copy of the audit records.
func (s *TradingModeSwitcher) AuditLog() []AuditRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]AuditRecord, len(s.audit))
	copy(out, s.audit)
	return out
}

// LiveIndicator returns the dashboard indicator text.
func (s *TradingModeSwitcher) LiveIndicator() string {
	if s.IsLive() {
		return "LIVE TRADING"
	}
	return ""
}
