package ring

import (
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/leef-l/brain/internal/data/model"
)

var ErrEmpty = errors.New("ring buffer is empty")

type Buffer struct {
	mu             sync.RWMutex
	capacity       int
	entries        []model.MarketSnapshot
	head           int
	size           int
	nextSeq        uint64
	latestBySymbol map[string]model.MarketSnapshot
}

func New(capacity int) *Buffer {
	if capacity <= 0 {
		capacity = 1024
	}
	return &Buffer{
		capacity:       capacity,
		entries:        make([]model.MarketSnapshot, capacity),
		latestBySymbol: make(map[string]model.MarketSnapshot),
	}
}

func (b *Buffer) Write(snapshot model.MarketSnapshot) model.MarketSnapshot {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.nextSeq++
	if snapshot.Timestamp == 0 {
		snapshot.Timestamp = time.Now().UnixMilli()
	}
	snapshot = cloneSnapshot(snapshot)
	snapshot.WriteSeq = b.nextSeq

	b.entries[b.head] = snapshot
	b.head = (b.head + 1) % b.capacity
	if b.size < b.capacity {
		b.size++
	}
	if snapshot.Symbol != "" {
		b.latestBySymbol[snapshot.Symbol] = snapshot
	}
	return cloneSnapshot(snapshot)
}

func (b *Buffer) Latest(symbol string) (model.MarketSnapshot, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if symbol != "" {
		snapshot, ok := b.latestBySymbol[symbol]
		return cloneSnapshot(snapshot), ok
	}
	if b.size == 0 {
		return model.MarketSnapshot{}, false
	}
	idx := b.head - 1
	if idx < 0 {
		idx = b.capacity - 1
	}
	snapshot := b.entries[idx]
	if snapshot.WriteSeq == 0 {
		return model.MarketSnapshot{}, false
	}
	return cloneSnapshot(snapshot), true
}

func (b *Buffer) LatestSequence() uint64 {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.nextSeq
}

func (b *Buffer) ReadSince(seq uint64) []model.MarketSnapshot {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if b.size == 0 {
		return nil
	}
	out := make([]model.MarketSnapshot, 0, b.size)
	for i := 0; i < b.size; i++ {
		idx := (b.head - b.size + i + b.capacity) % b.capacity
		snapshot := b.entries[idx]
		if snapshot.WriteSeq > seq {
			out = append(out, cloneSnapshot(snapshot))
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].WriteSeq < out[j].WriteSeq
	})
	return out
}

func (b *Buffer) Snapshot(symbol string) (model.MarketSnapshot, bool) {
	return b.Latest(symbol)
}

func (b *Buffer) Snapshots() []model.MarketSnapshot {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if b.size == 0 {
		return nil
	}
	out := make([]model.MarketSnapshot, 0, b.size)
	for i := 0; i < b.size; i++ {
		idx := (b.head - b.size + i + b.capacity) % b.capacity
		out = append(out, cloneSnapshot(b.entries[idx]))
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].WriteSeq < out[j].WriteSeq
	})
	return out
}

func (b *Buffer) RestoreSnapshots(snapshots []model.MarketSnapshot) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.entries = make([]model.MarketSnapshot, b.capacity)
	b.head = 0
	b.size = 0
	b.nextSeq = 0
	b.latestBySymbol = make(map[string]model.MarketSnapshot)

	for _, snapshot := range snapshots {
		cloned := cloneSnapshot(snapshot)
		if cloned.WriteSeq > b.nextSeq {
			b.nextSeq = cloned.WriteSeq
		}
		b.entries[b.head] = cloned
		b.head = (b.head + 1) % b.capacity
		if b.size < b.capacity {
			b.size++
		}
		if cloned.Symbol != "" {
			b.latestBySymbol[cloned.Symbol] = cloned
		}
	}
}

func (b *Buffer) Stats() (uint64, int) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.nextSeq, b.size
}

func cloneSnapshot(snapshot model.MarketSnapshot) model.MarketSnapshot {
	out := snapshot
	if len(snapshot.FeatureVector) > 0 {
		out.FeatureVector = append([]float64(nil), snapshot.FeatureVector...)
	}
	if len(snapshot.Candles) > 0 {
		out.Candles = make(map[string][]model.Candle, len(snapshot.Candles))
		for timeframe, candles := range snapshot.Candles {
			out.Candles[timeframe] = append([]model.Candle(nil), candles...)
		}
	}
	return out
}
