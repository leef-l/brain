package validator

import (
	"fmt"
	"hash/fnv"
	"strings"
	"sync"

	"github.com/leef-l/brain/internal/data/model"
)

type Config struct {
	MonotonicTopics            map[string]bool
	AllowSameTimestampRealtime bool
}

type Validator struct {
	mu         sync.Mutex
	cfg        Config
	lastTS     map[string]int64
	lastDigest map[string]uint64
	stats      model.ValidationStats
}

type State struct {
	LastTS     map[string]int64
	LastDigest map[string]uint64
	Stats      model.ValidationStats
}

func New(cfg Config) *Validator {
	return &Validator{
		cfg:        cfg,
		lastTS:     make(map[string]int64),
		lastDigest: make(map[string]uint64),
	}
}

func (v *Validator) Validate(event model.MarketEvent) model.ValidationResult {
	key := streamKey(event.Provider, event.Topic, event.Symbol)
	if strings.TrimSpace(event.Provider) == "" || strings.TrimSpace(event.Topic) == "" || strings.TrimSpace(event.Symbol) == "" {
		v.mu.Lock()
		v.stats.Rejected++
		v.mu.Unlock()
		return model.ValidationResult{
			Accepted: false,
			Action:   model.ValidationReject,
			Reason:   "provider, topic and symbol are required",
			Stage:    "shape",
			Key:      key,
			Sequence: event.Sequence,
		}
	}

	if event.Digest == 0 && len(event.Payload) > 0 {
		event.Digest = digest(event.Payload)
	}

	v.mu.Lock()
	defer v.mu.Unlock()

	lastTS := v.lastTS[key]
	lastDigest := v.lastDigest[key]
	monotonic := v.cfg.MonotonicTopics[event.Topic] || strings.HasPrefix(event.Topic, "candle.")
	sameTsAllowed := v.cfg.AllowSameTimestampRealtime && !monotonic

	switch {
	case monotonic && event.Timestamp < lastTS:
		v.stats.Rejected++
		return model.ValidationResult{
			Accepted: false,
			Action:   model.ValidationReject,
			Reason:   fmt.Sprintf("stale timestamp: %d < %d", event.Timestamp, lastTS),
			Stage:    "order",
			Key:      key,
			Sequence: event.Sequence,
		}
	case monotonic && event.Timestamp == lastTS && event.Digest == lastDigest && event.Digest != 0:
		v.stats.Skipped++
		return model.ValidationResult{
			Accepted: false,
			Action:   model.ValidationSkip,
			Reason:   "duplicate payload",
			Stage:    "dedupe",
			Key:      key,
			Sequence: event.Sequence,
		}
	case !sameTsAllowed && event.Timestamp <= lastTS:
		v.stats.Rejected++
		return model.ValidationResult{
			Accepted: false,
			Action:   model.ValidationReject,
			Reason:   fmt.Sprintf("non-monotonic timestamp: %d <= %d", event.Timestamp, lastTS),
			Stage:    "order",
			Key:      key,
			Sequence: event.Sequence,
		}
	}

	v.lastTS[key] = event.Timestamp
	v.lastDigest[key] = event.Digest
	v.stats.Accepted++
	return model.ValidationResult{
		Accepted: true,
		Action:   model.ValidationAccept,
		Key:      key,
		Sequence: event.Sequence,
	}
}

func (v *Validator) Stats() model.ValidationStats {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.stats
}

func (v *Validator) ExportState() State {
	v.mu.Lock()
	defer v.mu.Unlock()

	state := State{
		LastTS:     make(map[string]int64, len(v.lastTS)),
		LastDigest: make(map[string]uint64, len(v.lastDigest)),
		Stats:      v.stats,
	}
	for key, value := range v.lastTS {
		state.LastTS[key] = value
	}
	for key, value := range v.lastDigest {
		state.LastDigest[key] = value
	}
	return state
}

func (v *Validator) RestoreState(state State) {
	v.mu.Lock()
	defer v.mu.Unlock()

	v.lastTS = make(map[string]int64, len(state.LastTS))
	for key, value := range state.LastTS {
		v.lastTS[key] = value
	}
	v.lastDigest = make(map[string]uint64, len(state.LastDigest))
	for key, value := range state.LastDigest {
		v.lastDigest[key] = value
	}
	v.stats = state.Stats
}

func streamKey(provider, topic, symbol string) string {
	return strings.ToLower(strings.TrimSpace(provider)) + "|" + strings.ToLower(strings.TrimSpace(topic)) + "|" + strings.ToLower(strings.TrimSpace(symbol))
}

func digest(payload []byte) uint64 {
	h := fnv.New64a()
	_, _ = h.Write(payload)
	return h.Sum64()
}
