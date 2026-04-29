// Command quant-brain runs the Quant Brain as a sidecar process.
//
// It reads configuration from the path in QUANT_CONFIG env var,
// from -config flag, or falls back to paper-trading defaults.
// The Kernel launches this as a child process through BrainRegistration.
//
// Usage:
//
//	quant-brain -paper -equity 50000
//	quant-brain -config /path/to/quant-brain.yaml
//	PG_URL=postgres://... quant-brain -paper
package main

import (
	"flag"
	"log"
	"log/slog"
	"os"

	quant "github.com/leef-l/brain/brains/quant"
	quantsidecar "github.com/leef-l/brain/brains/quant/sidecar"
	"github.com/leef-l/brain/sdk/license"
	"github.com/leef-l/brain/sdk/sidecar"
)

func main() {
	configFile := flag.String("config", "", "path to config file (YAML or JSON)")
	paperMode := flag.Bool("paper", false, "quick start with paper trading")
	equity := flag.Float64("equity", 10000, "initial equity for paper mode")
	pgURL := flag.String("pg", os.Getenv("PG_URL"), "PostgreSQL URL for persistent trade history")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	verifyOpts, err := license.VerifyOptionsFromEnv(license.VerifyOptions{})
	if err != nil {
		logger.Error("license config failed", "err", err)
		os.Exit(1)
	}
	if _, err := license.CheckSidecar("brain-quant", verifyOpts); err != nil {
		logger.Error("license check failed", "err", err)
		os.Exit(1)
	}

	var cfg quant.FullConfig
	if *paperMode {
		cfg = quant.DefaultFullConfig()
		if len(cfg.Accounts) > 0 {
			cfg.Accounts[0].InitialEquity = *equity
		}
		logger.Info("starting in paper mode", "equity", *equity)
	} else {
		if *configFile != "" {
			os.Setenv("QUANT_CONFIG", *configFile)
		}
		cfg = quantsidecar.Load(logger)
	}

	if *pgURL != "" {
		os.Setenv("PG_URL", *pgURL)
	}

	handler, stop := quantsidecar.Build(cfg, logger)
	defer stop()

	if err := sidecar.Run(handler); err != nil {
		log.Fatal(err)
	}
}
