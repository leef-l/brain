package validator

import (
	"strings"
	"sync"

	"github.com/leef-l/brain/brains/data/provider"
)

// barIntervalMs maps bar sizes to their expected interval in milliseconds.
var barIntervalMs = map[string]int64{
	"1m":  60_000,
	"5m":  300_000,
	"15m": 900_000,
	"1H":  3_600_000,
	"4H":  14_400_000,
}

// GapDetector tracks candle continuity and detects missing bars.
type GapDetector struct {
	lastCandleTS map[string]int64 // "instID:bar" -> last candle timestamp
	consecutive  map[string]int   // "instID:bar" -> consecutive missing count
	threshold    int
	onGap        func(instID, bar string, from, to int64)
	mu           sync.Mutex
}

// NewGapDetector creates a GapDetector that fires onGap when consecutive
// missing candles exceed threshold.
func NewGapDetector(threshold int, onGap func(instID, bar string, from, to int64)) *GapDetector {
	return &GapDetector{
		lastCandleTS: make(map[string]int64),
		consecutive:  make(map[string]int),
		threshold:    threshold,
		onGap:        onGap,
	}
}

// Observe inspects a DataEvent for candle gaps.
func (g *GapDetector) Observe(event *provider.DataEvent) {
	if !strings.Contains(event.Topic, "candle") {
		return
	}

	candles := extractCandles(event)
	for _, c := range candles {
		g.observeCandle(c)
	}
}

func (g *GapDetector) observeCandle(c provider.Candle) {
	interval, ok := barIntervalMs[c.Bar]
	if !ok {
		return
	}

	key := c.InstID + ":" + c.Bar

	g.mu.Lock()
	defer g.mu.Unlock()

	last, exists := g.lastCandleTS[key]
	if exists {
		diff := c.Timestamp - last
		// threshold = 1.5x the normal interval
		if diff > interval*3/2 {
			missed := int(diff/interval) - 1
			g.consecutive[key] += missed
			if g.consecutive[key] >= g.threshold && g.onGap != nil {
				g.onGap(c.InstID, c.Bar, last, c.Timestamp)
			}
		} else {
			g.consecutive[key] = 0
		}
	}
	g.lastCandleTS[key] = c.Timestamp
}

// extractCandles pulls Candle values from the event payload.
func extractCandles(event *provider.DataEvent) []provider.Candle {
	switch p := event.Payload.(type) {
	case provider.Candle:
		return []provider.Candle{p}
	case *provider.Candle:
		return []provider.Candle{*p}
	case []provider.Candle:
		return p
	}
	return nil
}
