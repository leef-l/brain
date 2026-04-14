package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/leef-l/brain/internal/data"
	"github.com/leef-l/brain/internal/data/model"
	"github.com/leef-l/brain/internal/data/provider"
	"github.com/leef-l/brain/internal/data/service"
	"github.com/leef-l/brain/license"
	"github.com/leef-l/brain/persistence"
	"github.com/leef-l/brain/sidecar"
)

type sidecarRunner func(sidecar.BrainHandler) error
type sidecarLicenseChecker func(string, license.VerifyOptions) (*license.Result, error)
type dataBrainFactory func(context.Context) (*data.Brain, func() error, error)

func main() {
	if err := run(context.Background(), sidecar.Run, license.CheckSidecar); err != nil {
		fmt.Fprintf(os.Stderr, "brain-data: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, runSidecar sidecarRunner, checkLicense sidecarLicenseChecker) error {
	return runWithFactory(ctx, runSidecar, checkLicense, newRuntimeBrainFromEnv)
}

func runWithFactory(ctx context.Context, runSidecar sidecarRunner, checkLicense sidecarLicenseChecker, factory dataBrainFactory) error {
	if _, err := checkLicense("brain-data", license.VerifyOptions{}); err != nil {
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

func newRuntimeBrainFromEnv(ctx context.Context) (*data.Brain, func() error, error) {
	driver, dsn, err := dataPersistenceConfigFromEnv()
	if err != nil {
		return nil, nil, err
	}

	stores, err := persistence.Open(driver, dsn)
	if err != nil {
		return nil, nil, err
	}
	if stores.DataStateStore == nil {
		_ = stores.Close()
		return nil, nil, fmt.Errorf("persistence driver %q does not provide a data state store", driver)
	}

	cfg := defaultConfig()
	cfg.StateStore = stores.DataStateStore
	brain := data.NewBrain(cfg)
	if err := brain.RestoreState(ctx); err != nil {
		_ = stores.Close()
		return nil, nil, err
	}
	staticProvider, err := loadStaticProviderFromEnv()
	if err != nil {
		_ = stores.Close()
		return nil, nil, err
	}
	if staticProvider != nil {
		if err := brain.Service().RegisterProvider(staticProvider); err != nil {
			_ = stores.Close()
			return nil, nil, err
		}
		if err := brain.Service().DrainProviders(ctx); err != nil {
			_ = stores.Close()
			return nil, nil, err
		}
	}
	return brain, stores.Close, nil
}

func dataPersistenceConfigFromEnv() (string, string, error) {
	driver := strings.TrimSpace(os.Getenv("BRAIN_DATA_PERSIST_DRIVER"))
	if driver == "" {
		driver = "file"
	}

	dsn := strings.TrimSpace(os.Getenv("BRAIN_DATA_PERSIST_DSN"))
	if dsn != "" || driver != "file" {
		return driver, dsn, nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", fmt.Errorf("resolve data persistence home: %w", err)
	}
	return driver, filepath.Join(home, ".brain", "sidecars", "brain-data.json"), nil
}

func loadStaticProviderFromEnv() (provider.Provider, error) {
	path := strings.TrimSpace(os.Getenv("BRAIN_DATA_STATIC_FIXTURE"))
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read static data fixture: %w", err)
	}

	var events []model.MarketEvent
	if err := json.Unmarshal(data, &events); err != nil {
		return nil, fmt.Errorf("decode static data fixture: %w", err)
	}
	name := strings.TrimSpace(os.Getenv("BRAIN_DATA_STATIC_PROVIDER_NAME"))
	if name == "" {
		name = "fixture"
	}
	return provider.NewStaticProvider(name, events...), nil
}

func defaultConfig() service.Config {
	return service.Config{
		RingCapacity:     1024,
		DefaultTimeframe: "1m",
		MonotonicTopics: map[string]bool{
			"trade":      true,
			"books5":     true,
			"funding":    true,
			"candle.1m":  true,
			"candle.5m":  true,
			"candle.15m": true,
		},
		AllowSameTimestampRealtime: true,
	}
}
