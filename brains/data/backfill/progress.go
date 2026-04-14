package backfill

import (
	"context"
	"time"

	"github.com/leef-l/brain/brains/data/store"
)

// getStartTS returns the timestamp from which backfill should begin.
// If a checkpoint exists, it resumes from the saved LatestTS;
// otherwise it falls back to now - config.GoBack.
func (b *Backfiller) getStartTS(ctx context.Context, instID, tf string) int64 {
	progress, err := b.store.GetProgress(ctx, instID, tf)
	if err != nil || progress == nil {
		return time.Now().Add(-b.config.GoBack).UnixMilli()
	}
	return progress.LatestTS
}

// saveProgress persists the backfill checkpoint so it can be resumed later.
func (b *Backfiller) saveProgress(ctx context.Context, instID, tf string, latestTS int64, count int) error {
	return b.store.SaveProgress(ctx, store.BackfillProgress{
		InstID:    instID,
		Timeframe: tf,
		LatestTS:  latestTS,
		BarCount:  count,
	})
}
