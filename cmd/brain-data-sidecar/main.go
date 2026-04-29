package main

import (
	"log/slog"
	"os"

	datasidecar "github.com/leef-l/brain/brains/data/sidecar"
	"github.com/leef-l/brain/sdk/license"
	"github.com/leef-l/brain/sdk/sidecar"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	verifyOpts, err := license.VerifyOptionsFromEnv(license.VerifyOptions{})
	if err != nil {
		logger.Error("license config failed", "err", err)
		os.Exit(1)
	}
	if _, err := license.CheckSidecar("brain-data", verifyOpts); err != nil {
		logger.Error("license check failed", "err", err)
		os.Exit(1)
	}

	cfg, st := datasidecar.Load(logger)
	handler := datasidecar.Build(cfg, st, logger)

	if err := sidecar.Run(handler); err != nil {
		logger.Error("sidecar run failed", "err", err)
		os.Exit(1)
	}
}
