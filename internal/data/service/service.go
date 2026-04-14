package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	brainerrors "github.com/leef-l/brain/errors"
	"github.com/leef-l/brain/internal/data/model"
	"github.com/leef-l/brain/internal/data/processor"
	"github.com/leef-l/brain/internal/data/provider"
	"github.com/leef-l/brain/internal/data/ring"
	"github.com/leef-l/brain/internal/data/validator"
	"github.com/leef-l/brain/persistence"
)

type Config struct {
	RingCapacity               int
	DefaultTimeframe           string
	MonotonicTopics            map[string]bool
	AllowSameTimestampRealtime bool
	StateStore                 persistence.DataStateStore
}

type Service struct {
	mu                    sync.RWMutex
	registry              *provider.Registry
	validator             *validator.Validator
	processor             *processor.Processor
	ring                  *ring.Buffer
	stateStore            persistence.DataStateStore
	restoredProviderState []model.ProviderHealth
}

func New(cfg Config) *Service {
	if cfg.RingCapacity <= 0 {
		cfg.RingCapacity = 1024
	}
	if strings.TrimSpace(cfg.DefaultTimeframe) == "" {
		cfg.DefaultTimeframe = "1m"
	}

	return &Service{
		registry: provider.NewRegistry(),
		validator: validator.New(validator.Config{
			MonotonicTopics:            cfg.MonotonicTopics,
			AllowSameTimestampRealtime: cfg.AllowSameTimestampRealtime,
		}),
		processor:  processor.New(cfg.DefaultTimeframe),
		ring:       ring.New(cfg.RingCapacity),
		stateStore: cfg.StateStore,
	}
}

func (s *Service) RegisterProvider(p provider.Provider) error {
	if err := s.registry.Register(p); err != nil {
		return err
	}
	return s.persistState(context.Background())
}

func (s *Service) Ingest(ctx context.Context, event model.MarketEvent) (model.MarketSnapshot, model.ValidationResult, error) {
	result := s.validator.Validate(event)
	if !result.Accepted {
		return model.MarketSnapshot{}, result, nil
	}
	snapshot := s.processor.Process(event, result)
	written := s.ring.Write(snapshot)
	if err := s.persistState(ctx); err != nil {
		return model.MarketSnapshot{}, result, err
	}
	return written, result, nil
}

func (s *Service) StoreSnapshot(snapshot model.MarketSnapshot) model.MarketSnapshot {
	written := s.ring.Write(snapshot)
	_ = s.persistState(context.Background())
	return written
}

func (s *Service) LatestSnapshot(symbol string) (model.MarketSnapshot, bool) {
	return s.ring.Latest(symbol)
}

func (s *Service) FeatureVector(symbol string) ([]float64, bool) {
	snapshot, ok := s.ring.Latest(symbol)
	if !ok {
		return nil, false
	}
	return append([]float64(nil), snapshot.FeatureVector...), true
}

func (s *Service) Candles(symbol, timeframe string) ([]model.Candle, bool) {
	snapshot, ok := s.ring.Latest(symbol)
	if !ok {
		return nil, false
	}
	if timeframe == "" {
		timeframe = "1m"
	}
	candles, ok := snapshot.Candles[timeframe]
	if !ok {
		return nil, false
	}
	return append([]model.Candle(nil), candles...), true
}

func (s *Service) ProviderHealth(ctx context.Context) []model.ProviderHealth {
	providers := s.registry.List()
	out := make([]model.ProviderHealth, 0, len(providers))
	for _, p := range providers {
		out = append(out, p.Health(ctx))
	}
	if len(out) == 0 {
		return s.restoredProviderHealthsSnapshot()
	}
	return out
}

func (s *Service) Health(ctx context.Context) model.Health {
	seq, _ := s.ring.Stats()
	providerHealths := s.ProviderHealth(ctx)
	liveProviders := s.registry.Count()
	knownProviders := liveProviders
	if knownProviders == 0 {
		knownProviders = len(providerHealths)
	}
	health := model.Health{
		State:           model.HealthStateHealthy,
		LatestSequence:  seq,
		ValidationStats: s.validator.Stats(),
		ProviderHealths: providerHealths,
		KnownProviders:  knownProviders,
	}
	if health.KnownProviders == 0 {
		health.State = model.HealthStateDegraded
		health.Message = "no providers registered"
	}
	for _, ph := range providerHealths {
		if strings.EqualFold(ph.State, model.ProviderStateActive) {
			health.ActiveProviders++
		}
	}
	if health.ActiveProviders == 0 && health.KnownProviders > 0 {
		health.State = model.HealthStateDegraded
		health.Message = "all providers degraded"
	}
	if liveProviders == 0 && len(providerHealths) > 0 {
		health.State = model.HealthStateDegraded
		health.Message = "providers restored from persisted state; live feeds not attached"
	}
	return health
}

func (s *Service) Ready(ctx context.Context) (bool, string, model.Health) {
	health := s.Health(ctx)
	if health.State == model.HealthStateHealthy && health.ActiveProviders > 0 {
		return true, "", health
	}
	reason := health.Message
	if strings.TrimSpace(reason) == "" {
		reason = "data brain is not ready"
	}
	return false, reason, health
}

func (s *Service) HandleTool(ctx context.Context, name string, payload []byte) ([]byte, error) {
	switch strings.TrimSpace(name) {
	case model.ToolGetSnapshot:
		var query model.SnapshotQuery
		if len(payload) > 0 {
			if err := json.Unmarshal(payload, &query); err != nil {
				return nil, fmt.Errorf("decode snapshot query: %w", err)
			}
		}
		snapshot, ok := s.LatestSnapshot(query.Symbol)
		if !ok {
			return json.Marshal(struct {
				OK     bool   `json:"ok"`
				Error  string `json:"error"`
				Tool   string `json:"tool"`
				Symbol string `json:"symbol,omitempty"`
			}{
				OK:     false,
				Error:  "snapshot not found",
				Tool:   model.ToolGetSnapshot,
				Symbol: query.Symbol,
			})
		}
		return json.Marshal(snapshot)

	case model.ToolGetFeatureVector:
		var query model.FeatureVectorQuery
		if len(payload) > 0 {
			if err := json.Unmarshal(payload, &query); err != nil {
				return nil, fmt.Errorf("decode feature vector query: %w", err)
			}
		}
		vector, ok := s.FeatureVector(query.Symbol)
		if !ok {
			return json.Marshal(struct {
				OK     bool   `json:"ok"`
				Error  string `json:"error"`
				Tool   string `json:"tool"`
				Symbol string `json:"symbol,omitempty"`
			}{
				OK:     false,
				Error:  "feature vector not found",
				Tool:   model.ToolGetFeatureVector,
				Symbol: query.Symbol,
			})
		}
		return json.Marshal(struct {
			Symbol string    `json:"symbol"`
			Vector []float64 `json:"vector"`
		}{
			Symbol: query.Symbol,
			Vector: vector,
		})

	case model.ToolGetCandles:
		var query model.CandleQuery
		if len(payload) > 0 {
			if err := json.Unmarshal(payload, &query); err != nil {
				return nil, fmt.Errorf("decode candle query: %w", err)
			}
		}
		candles, ok := s.Candles(query.Symbol, query.Timeframe)
		if !ok {
			return json.Marshal(struct {
				OK     bool   `json:"ok"`
				Error  string `json:"error"`
				Tool   string `json:"tool"`
				Symbol string `json:"symbol,omitempty"`
			}{
				OK:     false,
				Error:  "candles not found",
				Tool:   model.ToolGetCandles,
				Symbol: query.Symbol,
			})
		}
		return json.Marshal(struct {
			Symbol    string         `json:"symbol"`
			Timeframe string         `json:"timeframe"`
			Candles   []model.Candle `json:"candles"`
		}{
			Symbol:    query.Symbol,
			Timeframe: query.Timeframe,
			Candles:   candles,
		})

	case model.ToolProviderHealth:
		return json.Marshal(struct {
			Providers []model.ProviderHealth `json:"providers"`
		}{
			Providers: s.ProviderHealth(ctx),
		})

	case model.ToolValidationStats:
		return json.Marshal(struct {
			Stats model.ValidationStats `json:"stats"`
		}{
			Stats: s.validator.Stats(),
		})

	case model.ToolReplayStart, model.ToolReplayStop, model.ToolGetSimilar:
		return json.Marshal(struct {
			OK    bool   `json:"ok"`
			Tool  string `json:"tool"`
			Error string `json:"error"`
		}{
			OK:    false,
			Tool:  strings.TrimSpace(name),
			Error: "unsupported in minimal skeleton",
		})

	default:
		return nil, fmt.Errorf("unsupported data tool %q", name)
	}
}

func (s *Service) Ring() *ring.Buffer {
	return s.ring
}

func (s *Service) Registry() *provider.Registry {
	return s.registry
}

func (s *Service) DrainProviders(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		providers := s.registry.List()
		if len(providers) == 0 {
			return nil
		}

		progressed := false
		for _, p := range providers {
			event, ok, err := p.Next(ctx)
			if err != nil {
				return fmt.Errorf("provider %s next: %w", p.Name(), err)
			}
			if !ok {
				continue
			}
			progressed = true
			if _, _, err := s.Ingest(ctx, event); err != nil {
				return fmt.Errorf("provider %s ingest: %w", p.Name(), err)
			}
		}
		if !progressed {
			return nil
		}
	}
}

func (s *Service) RestoreState(ctx context.Context) error {
	if s == nil || s.stateStore == nil {
		return nil
	}

	stored, err := s.stateStore.Get(ctx)
	if err != nil {
		if be, ok := err.(*brainerrors.BrainError); ok && be.ErrorCode == brainerrors.CodeRecordNotFound {
			return nil
		}
		return fmt.Errorf("load data state: %w", err)
	}

	var snapshots []model.MarketSnapshot
	if len(stored.Snapshots) > 0 {
		if err := json.Unmarshal(stored.Snapshots, &snapshots); err != nil {
			return fmt.Errorf("decode data snapshots: %w", err)
		}
	}
	s.ring.RestoreSnapshots(snapshots)
	s.validator.RestoreState(validator.State{
		LastTS:     cloneInt64Map(stored.Validator.LastTS),
		LastDigest: cloneUint64Map(stored.Validator.LastDigest),
		Stats: model.ValidationStats{
			Accepted: stored.Validator.Accepted,
			Rejected: stored.Validator.Rejected,
			Skipped:  stored.Validator.Skipped,
		},
	})

	var providerHealths []model.ProviderHealth
	if len(stored.ProviderHealths) > 0 {
		if err := json.Unmarshal(stored.ProviderHealths, &providerHealths); err != nil {
			return fmt.Errorf("decode provider healths: %w", err)
		}
	}
	s.mu.Lock()
	s.restoredProviderState = cloneProviderHealths(providerHealths)
	s.mu.Unlock()
	return nil
}

func (s *Service) persistState(ctx context.Context) error {
	if s == nil || s.stateStore == nil {
		return nil
	}

	snapshotsRaw, err := json.Marshal(s.ring.Snapshots())
	if err != nil {
		return fmt.Errorf("encode data snapshots: %w", err)
	}
	providerHealthsRaw, err := json.Marshal(s.currentProviderHealthSnapshot(ctx))
	if err != nil {
		return fmt.Errorf("encode provider healths: %w", err)
	}
	validatorState := s.validator.ExportState()
	if err := s.stateStore.Save(ctx, &persistence.DataState{
		Snapshots:       snapshotsRaw,
		ProviderHealths: providerHealthsRaw,
		Validator: persistence.DataValidatorState{
			LastTS:     cloneInt64Map(validatorState.LastTS),
			LastDigest: cloneUint64Map(validatorState.LastDigest),
			Accepted:   validatorState.Stats.Accepted,
			Rejected:   validatorState.Stats.Rejected,
			Skipped:    validatorState.Stats.Skipped,
		},
	}); err != nil {
		return fmt.Errorf("save data state: %w", err)
	}
	return nil
}

func (s *Service) currentProviderHealthSnapshot(ctx context.Context) []model.ProviderHealth {
	providers := s.registry.List()
	if len(providers) == 0 {
		return s.restoredProviderHealthsSnapshot()
	}
	out := make([]model.ProviderHealth, 0, len(providers))
	for _, p := range providers {
		out = append(out, p.Health(ctx))
	}
	return out
}

func (s *Service) restoredProviderHealthsSnapshot() []model.ProviderHealth {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneProviderHealths(s.restoredProviderState)
}

func cloneProviderHealths(src []model.ProviderHealth) []model.ProviderHealth {
	if len(src) == 0 {
		return nil
	}
	out := make([]model.ProviderHealth, len(src))
	copy(out, src)
	return out
}

func cloneInt64Map(src map[string]int64) map[string]int64 {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]int64, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}

func cloneUint64Map(src map[string]uint64) map[string]uint64 {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]uint64, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}
