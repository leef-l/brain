// Command brain-quant is the Quant Brain sidecar entry point.
//
// It intentionally stays thin: boot the handler, hand control to the shared
// sidecar runtime, and exit with a non-zero status on startup failure.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/leef-l/brain/internal/quant/audit"
	"github.com/leef-l/brain/internal/quant/service"
	"github.com/leef-l/brain/license"
	"github.com/leef-l/brain/persistence"
	"github.com/leef-l/brain/sidecar"
)

func main() {
	if err := runWithFactory(context.Background(), sidecar.Run, license.CheckSidecar, newRuntimeBrainFromEnv); err != nil {
		fmt.Fprintf(os.Stderr, "brain-quant: %v\n", err)
		os.Exit(1)
	}
}

type sidecarRunner func(sidecar.BrainHandler) error
type sidecarLicenseChecker func(string, license.VerifyOptions) (*license.Result, error)
type quantBrainFactory func(context.Context) (*service.Brain, func() error, error)

func run(runSidecar sidecarRunner, checkLicense sidecarLicenseChecker) error {
	return runWithFactory(context.Background(), runSidecar, checkLicense, func(ctx context.Context) (*service.Brain, func() error, error) {
		return service.NewBrain(), nil, nil
	})
}

func runWithFactory(ctx context.Context, runSidecar sidecarRunner, checkLicense sidecarLicenseChecker, factory quantBrainFactory) error {
	if _, err := checkLicense("brain-quant", license.VerifyOptions{}); err != nil {
		return fmt.Errorf("license: %w", err)
	}
	brain, closeFn, err := factory(ctx)
	if err != nil {
		return fmt.Errorf("init runtime: %w", err)
	}
	if closeFn != nil {
		defer closeFn()
	}
	return runSidecar(brain)
}

func newRuntimeBrainFromEnv(ctx context.Context) (*service.Brain, func() error, error) {
	driver, dsn, err := quantPersistenceConfigFromEnv()
	if err != nil {
		return nil, nil, err
	}

	stores, err := persistence.Open(driver, dsn)
	if err != nil {
		return nil, nil, err
	}
	auditStore := audit.NewPersistentStore(stores.SignalTraceStore)
	if auditStore == nil {
		_ = stores.Close()
		return nil, nil, fmt.Errorf("persistence driver %q does not provide a signal trace store", driver)
	}

	brain := service.NewBrain(service.WithAuditStore(auditStore))
	if err := brain.RestoreRuntime(ctx); err != nil {
		_ = stores.Close()
		return nil, nil, err
	}
	return brain, stores.Close, nil
}

func quantPersistenceConfigFromEnv() (string, string, error) {
	driver := strings.TrimSpace(os.Getenv("BRAIN_QUANT_PERSIST_DRIVER"))
	if driver == "" {
		driver = "file"
	}

	dsn := strings.TrimSpace(os.Getenv("BRAIN_QUANT_PERSIST_DSN"))
	if dsn != "" || driver != "file" {
		return driver, dsn, nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", fmt.Errorf("resolve quant persistence home: %w", err)
	}
	return driver, filepath.Join(home, ".brain", "sidecars", "brain-quant.json"), nil
}
