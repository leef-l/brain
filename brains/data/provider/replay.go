package provider

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/leef-l/brain/brains/data/store"
)

// ReplayConfig configures the replay provider.
type ReplayConfig struct {
	Store      store.CandleStore
	InstIDs    []string // instruments to replay
	Timeframes []string // bar intervals to replay (e.g. "1m", "5m")
	From       int64    // start timestamp (ms)
	To         int64    // end timestamp (ms); 0 = now
	Speed      float64  // playback speed: 0 = as-fast-as-possible, 1.0 = realtime, 10.0 = 10x
}

// ReplayProvider reads historical candles from the store and replays them
// as DataEvents, allowing the rest of the pipeline to run in backtest mode.
type ReplayProvider struct {
	name   string
	config ReplayConfig
	health atomic.Value // *ProviderHealth

	mu   sync.Mutex
	sink DataSink
	done chan struct{}
}

// NewReplayProvider creates a replay provider (not started).
func NewReplayProvider(name string, cfg ReplayConfig) *ReplayProvider {
	p := &ReplayProvider{
		name:   name,
		config: cfg,
		done:   make(chan struct{}),
	}
	p.health.Store(&ProviderHealth{Status: "idle"})
	return p
}

func (p *ReplayProvider) Name() string { return p.name }

func (p *ReplayProvider) Subscribe(sink DataSink) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sink = sink
	return nil
}

func (p *ReplayProvider) Health() ProviderHealth {
	if h, ok := p.health.Load().(*ProviderHealth); ok {
		return *h
	}
	return ProviderHealth{Status: "unknown"}
}

// Start begins replaying candles in a goroutine.
func (p *ReplayProvider) Start(ctx context.Context) error {
	p.mu.Lock()
	sink := p.sink
	p.mu.Unlock()
	if sink == nil {
		return fmt.Errorf("no sink subscribed")
	}

	p.health.Store(&ProviderHealth{Status: "connected", LastEvent: time.Now()})

	go p.run(ctx, sink)
	return nil
}

// Stop signals the replay goroutine to exit.
func (p *ReplayProvider) Stop(_ context.Context) error {
	select {
	case <-p.done:
	default:
		close(p.done)
	}
	p.health.Store(&ProviderHealth{Status: "stopped"})
	return nil
}

func (p *ReplayProvider) run(ctx context.Context, sink DataSink) {
	cfg := p.config
	to := cfg.To
	if to == 0 {
		to = time.Now().UnixMilli()
	}

	tfs := cfg.Timeframes
	if len(tfs) == 0 {
		tfs = []string{"1m"}
	}

	var prevTS int64

	for _, instID := range cfg.InstIDs {
		for _, tf := range tfs {
			if err := p.replayOne(ctx, sink, instID, tf, cfg.From, to, cfg.Speed, &prevTS); err != nil {
				p.health.Store(&ProviderHealth{
					Status:     "error",
					ErrorCount: 1,
				})
				return
			}
		}
	}

	p.health.Store(&ProviderHealth{Status: "completed", LastEvent: time.Now()})
}

func (p *ReplayProvider) replayOne(
	ctx context.Context, sink DataSink,
	instID, tf string, from, to int64, speed float64,
	prevTS *int64,
) error {
	candles, err := p.config.Store.QueryRange(ctx, instID, tf, from, to)
	if err != nil {
		return fmt.Errorf("query %s/%s: %w", instID, tf, err)
	}

	for _, c := range candles {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-p.done:
			return nil
		default:
		}

		// Throttle if speed > 0
		if speed > 0 && *prevTS > 0 {
			gap := time.Duration(c.Timestamp-*prevTS) * time.Millisecond
			if gap > 0 {
				sleepDur := time.Duration(float64(gap) / speed)
				if sleepDur > 0 {
					timer := time.NewTimer(sleepDur)
					select {
					case <-timer.C:
					case <-ctx.Done():
						timer.Stop()
						return ctx.Err()
					case <-p.done:
						timer.Stop()
						return nil
					}
				}
			}
		}
		*prevTS = c.Timestamp

		event := DataEvent{
			Provider:  p.name,
			Symbol:    instID,
			Topic:     fmt.Sprintf("candle.%s.%s", tf, instID),
			Timestamp: c.Timestamp,
			LocalTS:   time.Now().UnixMilli(),
			Priority:  PriorityNearRT,
			Payload: []Candle{{
				InstID:    c.InstID,
				Bar:       c.Bar,
				Timestamp: c.Timestamp,
				Open:      c.Open,
				High:      c.High,
				Low:       c.Low,
				Close:     c.Close,
				Volume:    c.Volume,
				VolumeCcy: c.VolumeCcy,
			}},
		}
		sink.OnEvent(event)

		p.health.Store(&ProviderHealth{
			Status:    "connected",
			LastEvent: time.Now(),
		})
	}
	return nil
}
