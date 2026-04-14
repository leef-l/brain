// Command data-brain runs the Data Brain as a standalone process.
//
// Usage:
//
//	data-brain [-pg URL] [-instruments BTC-USDT-SWAP,ETH-USDT-SWAP] [-backfill]
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	data "github.com/leef-l/brain/brains/data"
	"github.com/leef-l/brain/brains/data/store"
)

func main() {
	var (
		pgURL       = flag.String("pg", os.Getenv("PG_URL"), "PostgreSQL connection URL")
		instruments = flag.String("instruments", "", "comma-separated instrument IDs (empty = auto-discover)")
		backfill    = flag.Bool("backfill", false, "enable historical backfill on startup")
		backfillDays = flag.Int("backfill-days", 90, "number of days to backfill")
	)
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// Build config
	cfg := data.Config{
		ActiveList: data.ActiveListConfig{
			MinVolume24h:   10_000_000,
			MaxInstruments: 100,
			UpdateInterval: 7 * 24 * time.Hour,
			AlwaysInclude:  []string{"BTC-USDT-SWAP", "ETH-USDT-SWAP", "SOL-USDT-SWAP"},
		},
		Backfill: data.BackfillConfig{
			Enabled:   *backfill,
			MaxDays:   *backfillDays,
			BatchSize: 100,
		},
		Validation: data.ValidationConfig{
			MaxPriceJump: 0.10,
		},
	}

	// Override instruments if provided
	if *instruments != "" {
		cfg.ActiveList.AlwaysInclude = strings.Split(*instruments, ",")
	}

	// Connect to PostgreSQL (optional — runs without PG for debugging)
	var st store.Store
	if *pgURL != "" {
		pgStore, err := store.NewPGStore(context.Background(), *pgURL)
		if err != nil {
			logger.Error("failed to connect to PostgreSQL", "err", err)
			os.Exit(1)
		}
		if err := pgStore.Migrate(context.Background()); err != nil {
			logger.Error("failed to run migrations", "err", err)
			os.Exit(1)
		}
		st = pgStore
		logger.Info("connected to PostgreSQL")
	} else {
		logger.Warn("running without PostgreSQL — data will not be persisted")
	}

	// Create and start DataBrain
	brain := data.New(cfg, st, logger)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := brain.Start(ctx); err != nil {
		logger.Error("failed to start data brain", "err", err)
		os.Exit(1)
	}

	logger.Info("data brain running, press Ctrl+C to stop")

	// Block until signal
	<-ctx.Done()
	logger.Info("shutting down...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := brain.Stop(shutdownCtx); err != nil {
		logger.Error("shutdown error", "err", err)
		os.Exit(1)
	}

	logger.Info("data brain stopped gracefully")
}
