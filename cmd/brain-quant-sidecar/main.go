package main

import (
	"log/slog"
	"os"

	quantsidecar "github.com/leef-l/brain/brains/quant/sidecar"
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
	if _, err := license.CheckSidecar("brain-quant", verifyOpts); err != nil {
		logger.Error("license check failed", "err", err)
		os.Exit(1)
	}

	cfg := quantsidecar.Load(logger)
	handler, stop := quantsidecar.Build(cfg, logger)
	defer stop()

	if err := sidecar.Run(handler); err != nil {
		logger.Error("sidecar run failed", "err", err)
		os.Exit(1)
	}
}
