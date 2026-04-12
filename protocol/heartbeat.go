package protocol

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"
)

// PingScheduler implements the protocol-layer $/ping / $/pong exchange
// defined in 20-协议规格.md §7.2. Every PingInterval the scheduler emits a
// $/ping notification carrying a monotonically increasing seq field; the
// peer is required to echo the same seq back via $/pong. Three consecutive
// missed pongs mark the sidecar as hung and trigger the lifecycle FSM
// transition to Draining per §7.3.
//
// PingScheduler is deliberately decoupled from the lifecycle FSM — it
// reports failures via the OnHung callback and lets the caller choose the
// response. Tests drive it with a fake BidirRPC so the heartbeat logic
// can be verified without spawning a real sidecar.
type PingScheduler struct {
	rpc BidirRPC

	interval      time.Duration
	missThreshold int

	seq      atomic.Int64
	lastPong atomic.Int64 // unix nanos

	pendingMu sync.Mutex
	pending   map[int64]time.Time

	onHung func()

	stop chan struct{}
	wg   sync.WaitGroup
	once sync.Once
}

// PingSchedulerOptions tunes PingScheduler timings. All zero-valued fields
// fall back to the 20 §7.1 defaults: 10s interval, 3 miss threshold.
type PingSchedulerOptions struct {
	// Interval between outbound $/ping notifications. Default 10s per
	// §7.1. Tests override this to sub-second for fast assertions.
	Interval time.Duration

	// MissThreshold is the number of consecutive pings that may go
	// without a matching pong before OnHung fires. Default 3 per §7.1.
	MissThreshold int

	// OnHung is invoked once (via sync.Once) when the threshold is
	// reached. Implementations typically flip the sidecar FSM to
	// Draining. A nil callback is a no-op — callers that only want to
	// observe the staleness via PendingCount() / LastPong() can leave
	// it unset.
	OnHung func()
}

// NewPingScheduler creates a scheduler over the given BidirRPC. The
// scheduler installs a $/pong handler on rpc so inbound pongs clear the
// pending map; duplicate registration of $/pong on the same rpc is a
// programmer bug and panics.
func NewPingScheduler(rpc BidirRPC, opts PingSchedulerOptions) *PingScheduler {
	interval := opts.Interval
	if interval <= 0 {
		interval = 10 * time.Second
	}
	miss := opts.MissThreshold
	if miss <= 0 {
		miss = 3
	}
	p := &PingScheduler{
		rpc:           rpc,
		interval:      interval,
		missThreshold: miss,
		pending:       make(map[int64]time.Time),
		onHung:        opts.OnHung,
		stop:          make(chan struct{}),
	}
	// Register the $/pong handler on construction so pongs for a ping
	// sent before Start races do not get dropped.
	rpc.Handle("$/pong", p.handlePong)
	return p
}

// Start begins the ping goroutine. Idempotent via sync.Once so tests
// that call Start and then Stop multiple times do not spawn extra
// goroutines.
func (p *PingScheduler) Start(ctx context.Context) {
	p.once.Do(func() {
		p.wg.Add(1)
		go p.loop(ctx)
	})
}

// Stop halts the ping loop and waits for the goroutine to exit. Safe to
// call multiple times.
func (p *PingScheduler) Stop() {
	select {
	case <-p.stop:
		return
	default:
		close(p.stop)
	}
	p.wg.Wait()
}

// PendingCount returns the number of pings that have been sent but have
// not yet received a matching pong. Used by tests and the watchdog to
// observe liveness without waiting for the threshold.
func (p *PingScheduler) PendingCount() int {
	p.pendingMu.Lock()
	defer p.pendingMu.Unlock()
	return len(p.pending)
}

// LastPongAt returns the wall-clock time of the most recent successful
// pong. Zero time means no pong has been received yet.
func (p *PingScheduler) LastPongAt() time.Time {
	ns := p.lastPong.Load()
	if ns == 0 {
		return time.Time{}
	}
	return time.Unix(0, ns)
}

// loop is the ticker goroutine. On each tick it:
//  1. Allocates a new seq and sends $/ping.
//  2. Records the pending seq → send-time mapping.
//  3. Scans the pending map to count how many pings are older than the
//     interval (i.e. have missed their pong). If the count ≥ threshold,
//     fire OnHung exactly once and exit the loop.
//
// Sending a ping MUST NOT block the loop — Notify is fire-and-forget and
// only fails if the writer is saturated, in which case we treat the
// failure as an implicit miss (the pending entry stays, the next scan
// picks it up).
func (p *PingScheduler) loop(ctx context.Context) {
	defer p.wg.Done()
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	hungFired := false

	for {
		select {
		case <-p.stop:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.sendPing(ctx)

			misses := p.countMisses()
			if misses >= p.missThreshold && !hungFired {
				hungFired = true
				if p.onHung != nil {
					p.onHung()
				}
				// Keep the loop running — the watchdog may want to see
				// additional telemetry. Stop() is the only way to fully
				// shut down the scheduler.
			}
		}
	}
}

// sendPing allocates a new seq, records it as pending, and fires a
// $/ping notification. The notification carries only {"seq": N} per
// §7.2; no other fields are defined in v1.
func (p *PingScheduler) sendPing(ctx context.Context) {
	seq := p.seq.Add(1)
	now := time.Now()

	p.pendingMu.Lock()
	p.pending[seq] = now
	p.pendingMu.Unlock()

	sendCtx, cancel := context.WithTimeout(ctx, p.interval)
	defer cancel()

	// Ignore the Notify error — a writer saturation counts as an
	// implicit miss and the next scan will detect it via the pending
	// map. Surfacing the error here would double-count failures.
	_ = p.rpc.Notify(sendCtx, "$/ping", map[string]int64{"seq": seq})
}

// countMisses walks the pending map and returns how many entries are
// older than the configured interval. Also prunes entries that are so
// old they exceed threshold×interval — otherwise the map would grow
// unbounded on a truly dead peer.
func (p *PingScheduler) countMisses() int {
	cutoff := time.Now().Add(-p.interval)
	pruneCutoff := time.Now().Add(-time.Duration(p.missThreshold*2) * p.interval)

	p.pendingMu.Lock()
	defer p.pendingMu.Unlock()

	misses := 0
	for seq, sentAt := range p.pending {
		if sentAt.Before(cutoff) {
			misses++
		}
		if sentAt.Before(pruneCutoff) {
			delete(p.pending, seq)
		}
	}
	return misses
}

// handlePong is the inbound $/pong notification handler. It clears the
// matching pending entry and updates the last-pong timestamp so
// LastPongAt reflects the most recent heartbeat.
func (p *PingScheduler) handlePong(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var payload struct {
		Seq int64 `json:"seq"`
	}
	if err := json.Unmarshal(params, &payload); err != nil {
		// Malformed $/pong is not worth crashing over — log-and-drop
		// semantics match §7.2 which only requires that the peer echo
		// the seq. Protocol violations are counted by the reader layer.
		return nil, nil
	}
	p.pendingMu.Lock()
	delete(p.pending, payload.Seq)
	p.pendingMu.Unlock()
	p.lastPong.Store(time.Now().UnixNano())
	return nil, nil
}

// StaleResponseTracker implements the §4.5 degrade escalation: every
// stale response increments a windowed counter, and the watchdog fires
// OnDegraded when the counter crosses the threshold within the window.
// The counter is reset at the start of each window so a bursty peer
// does not accumulate history across calm periods.
type StaleResponseTracker struct {
	mu        sync.Mutex
	window    time.Duration
	threshold int
	events    []time.Time
	onDegrade func()
	fired     bool
}

// NewStaleResponseTracker builds a tracker with the given window and
// threshold. v1 defaults are 1 minute and 10 events per §4.5.
func NewStaleResponseTracker(window time.Duration, threshold int, onDegrade func()) *StaleResponseTracker {
	if window <= 0 {
		window = time.Minute
	}
	if threshold <= 0 {
		threshold = 10
	}
	return &StaleResponseTracker{
		window:    window,
		threshold: threshold,
		onDegrade: onDegrade,
	}
}

// Observe records a stale-response event at wall-clock now. Prunes any
// events older than the window before checking the threshold so the
// check is always window-local. Fires OnDegraded at most once — after
// the first fire, the tracker stays armed and must be Reset to fire
// again.
func (t *StaleResponseTracker) Observe() {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-t.window)

	// Prune old events.
	fresh := t.events[:0]
	for _, e := range t.events {
		if !e.Before(cutoff) {
			fresh = append(fresh, e)
		}
	}
	t.events = append(fresh, now)

	if !t.fired && len(t.events) >= t.threshold {
		t.fired = true
		if t.onDegrade != nil {
			t.onDegrade()
		}
	}
}

// Count returns the current number of events in the window. Useful for
// tests that want to inspect tracker state without driving the clock.
func (t *StaleResponseTracker) Count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.events)
}

// Reset clears the event buffer and rearms the tracker. Called by the
// kernel watchdog after an operator manually returns a degraded sidecar
// to the pool, or after a successful quarantine → healthy transition.
func (t *StaleResponseTracker) Reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.events = t.events[:0]
	t.fired = false
}

