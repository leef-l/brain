// Package backfill implements historical K-line data retrieval from OKX REST API
// and stores the results via the store.Store interface.
package backfill

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"golang.org/x/time/rate"

	"github.com/leef-l/brain/brains/data/store"
)

// Config holds the backfiller configuration.
type Config struct {
	RESTURL    string        // e.g. "https://www.okx.com"
	GoBack     time.Duration // how far back to fill, e.g. 90 * 24h
	Timeframes []string      // e.g. ["1m","5m","15m","1H","4H"]
	MaxBars    int           // OKX returns at most 100 bars per call
	RateLimit  float64       // requests per second (default: 5, OKX limit)
}

// Backfiller fetches historical candles from OKX and writes them to the store.
type Backfiller struct {
	httpClient *http.Client
	store      store.Store
	limiter    *rate.Limiter
	config     Config
}

// New creates a Backfiller with the given HTTP client, store, and config.
func New(httpClient *http.Client, st store.Store, cfg Config) *Backfiller {
	if cfg.MaxBars <= 0 {
		cfg.MaxBars = 100
	}
	if cfg.RateLimit <= 0 {
		cfg.RateLimit = 5
	}
	return &Backfiller{
		httpClient: httpClient,
		store:      st,
		limiter:    rate.NewLimiter(rate.Limit(cfg.RateLimit), 1),
		config:     cfg,
	}
}

// BackfillAll backfills all instruments across all configured timeframes.
// 单个品种失败时记录日志并继续，最终聚合所有错误返回。
func (b *Backfiller) BackfillAll(ctx context.Context, instruments []string) error {
	var errs []error
	for _, inst := range instruments {
		for _, tf := range b.config.Timeframes {
			if err := b.backfillOne(ctx, inst, tf); err != nil {
				slog.Warn("backfill instrument failed, skipping",
					"inst", inst,
					"tf", tf,
					"err", err,
				)
				errs = append(errs, fmt.Errorf("backfill %s/%s: %w", inst, tf, err))
			}
		}
	}
	return errors.Join(errs...)
}

// backfillOne fills a single instrument+timeframe, ensuring contiguous coverage
// from (now - GoBack) up to now.
//
// Two-phase approach:
//  1. Forward-fill: from now backwards to the previous newest edge, filling
//     the gap between the last run and the current time.
//  2. Backward-fill: from the previous oldest edge backwards to GoBack,
//     continuing to extend historical depth.
//
// This ensures that restarting the process (even days later) always fills
// the gap up to the present, not just extending into the past.
func (b *Backfiller) backfillOne(ctx context.Context, instID, tf string) error {
	progress, _ := b.store.GetProgress(ctx, instID, tf)
	nowMS := time.Now().UnixMilli()
	goBackTS := time.Now().Add(-b.config.GoBack).UnixMilli()

	var oldestEdge, newestEdge int64
	var barCount int

	if progress != nil && progress.LatestTS > 0 {
		oldestEdge = progress.LatestTS
		newestEdge = progress.NewestTS
		barCount = progress.BarCount
		// Legacy: NewestTS==0 means old-format progress without newest tracking.
		if newestEdge == 0 {
			newestEdge = oldestEdge
		}
	} else {
		// First run — both edges start at now.
		oldestEdge = nowMS
		newestEdge = nowMS
		barCount = 0
	}

	// Phase 1: forward-fill — fill from now back to newestEdge.
	// This covers the time gap since the last backfill run.
	if nowMS > newestEdge+1 {
		cursor := nowMS
		for cursor > newestEdge {
			candles, err := b.fetchCandles(ctx, instID, tf, cursor, b.config.MaxBars)
			if err != nil {
				return err
			}
			if len(candles) == 0 {
				break
			}
			if err := b.store.BatchInsert(ctx, candles); err != nil {
				return fmt.Errorf("forward-fill batch insert: %w", err)
			}
			barCount += len(candles)
			cursor = candles[0].Timestamp - 1

			if err := b.saveProgress(ctx, instID, tf, oldestEdge, nowMS, barCount); err != nil {
				return fmt.Errorf("save progress: %w", err)
			}
			if len(candles) < b.config.MaxBars {
				break
			}
		}
		newestEdge = nowMS
	}

	// Phase 2: backward-fill — extend historical depth from oldestEdge to goBackTS.
	cursor := oldestEdge
	for cursor > goBackTS {
		candles, err := b.fetchCandles(ctx, instID, tf, cursor, b.config.MaxBars)
		if err != nil {
			return err
		}
		if len(candles) == 0 {
			break
		}
		if err := b.store.BatchInsert(ctx, candles); err != nil {
			return fmt.Errorf("backward-fill batch insert: %w", err)
		}
		barCount += len(candles)

		earliest := candles[0].Timestamp
		cursor = earliest - 1

		if err := b.saveProgress(ctx, instID, tf, cursor, newestEdge, barCount); err != nil {
			return fmt.Errorf("save progress: %w", err)
		}
		if len(candles) < b.config.MaxBars {
			break
		}
	}

	return nil
}

// FillGap fills missing candles for a specific time range.
func (b *Backfiller) FillGap(ctx context.Context, instID, tf string, from, to int64) error {
	cursor := from
	for cursor < to {
		candles, err := b.fetchCandles(ctx, instID, tf, cursor, b.config.MaxBars)
		if err != nil {
			return err
		}
		if len(candles) == 0 {
			break
		}
		if err := b.store.BatchInsert(ctx, candles); err != nil {
			return fmt.Errorf("batch insert: %w", err)
		}
		cursor = candles[len(candles)-1].Timestamp + 1
		if len(candles) < b.config.MaxBars {
			break
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// OKX REST API interaction
// ---------------------------------------------------------------------------

// okxResponse is the envelope returned by the OKX REST API.
type okxResponse struct {
	Code string     `json:"code"`
	Data [][]string `json:"data"`
}

// fetchCandles calls the OKX history-candles endpoint and returns parsed candles
// in ascending chronological order.
//
// OKX API: GET /api/v5/market/history-candles
//
//	?instId=BTC-USDT&bar=1m&limit=100&after=<ts>
//
// The API returns data in descending order; this function reverses it.
func (b *Backfiller) fetchCandles(ctx context.Context, instID, bar string, after int64, limit int) ([]store.Candle, error) {
	// Respect rate limit.
	if err := b.limiter.Wait(ctx); err != nil {
		return nil, err
	}

	url := fmt.Sprintf("%s/api/v5/market/history-candles?instId=%s&bar=%s&limit=%d&after=%d",
		b.config.RESTURL, instID, bar, limit, after)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}

	resp, err := b.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http do: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	var okxResp okxResponse
	if err := json.Unmarshal(body, &okxResp); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	if okxResp.Code != "0" {
		return nil, fmt.Errorf("okx api error code=%s body=%s", okxResp.Code, string(body))
	}

	candles, err := parseOKXCandles(instID, bar, okxResp.Data)
	if err != nil {
		return nil, err
	}

	// OKX returns descending order — reverse to ascending.
	reverseCandles(candles)
	return candles, nil
}

// parseOKXCandles converts raw OKX response rows into store.Candle values.
// Each row: [ts, o, h, l, c, vol, volCcy, volCcyQuote, confirm]
func parseOKXCandles(instID, bar string, rows [][]string) ([]store.Candle, error) {
	candles := make([]store.Candle, 0, len(rows))
	for i, row := range rows {
		if len(row) < 7 {
			return nil, fmt.Errorf("row %d: expected >= 7 fields, got %d", i, len(row))
		}
		ts, err := strconv.ParseInt(row[0], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("row %d ts: %w", i, err)
		}
		o, err := strconv.ParseFloat(row[1], 64)
		if err != nil {
			return nil, fmt.Errorf("row %d open: %w", i, err)
		}
		h, err := strconv.ParseFloat(row[2], 64)
		if err != nil {
			return nil, fmt.Errorf("row %d high: %w", i, err)
		}
		l, err := strconv.ParseFloat(row[3], 64)
		if err != nil {
			return nil, fmt.Errorf("row %d low: %w", i, err)
		}
		c, err := strconv.ParseFloat(row[4], 64)
		if err != nil {
			return nil, fmt.Errorf("row %d close: %w", i, err)
		}
		vol, err := strconv.ParseFloat(row[5], 64)
		if err != nil {
			return nil, fmt.Errorf("row %d vol: %w", i, err)
		}
		var volCcy float64
		if len(row) > 6 && row[6] != "" {
			volCcy, err = strconv.ParseFloat(row[6], 64)
			if err != nil {
				return nil, fmt.Errorf("row %d volCcy: %w", i, err)
			}
		}
		candles = append(candles, store.Candle{
			InstID:    instID,
			Bar:       bar,
			Timestamp: ts,
			Open:      o,
			High:      h,
			Low:       l,
			Close:     c,
			Volume:    vol,
			VolumeCcy: volCcy,
		})
	}
	return candles, nil
}

// reverseCandles reverses a slice of candles in-place.
func reverseCandles(s []store.Candle) {
	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
}
