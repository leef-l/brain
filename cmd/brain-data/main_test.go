package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/leef-l/brain/agent"
	"github.com/leef-l/brain/internal/data"
	"github.com/leef-l/brain/internal/quantcontracts"
	"github.com/leef-l/brain/license"
	"github.com/leef-l/brain/persistence"
	"github.com/leef-l/brain/protocol"
	"github.com/leef-l/brain/sidecar"
)

func TestDefaultConfigIncludesQuantSafeDefaults(t *testing.T) {
	cfg := defaultConfig()

	if cfg.RingCapacity != 1024 {
		t.Fatalf("RingCapacity=%d, want 1024", cfg.RingCapacity)
	}
	if cfg.DefaultTimeframe != "1m" {
		t.Fatalf("DefaultTimeframe=%q, want 1m", cfg.DefaultTimeframe)
	}
	if !cfg.AllowSameTimestampRealtime {
		t.Fatal("AllowSameTimestampRealtime should default to true")
	}
	for _, topic := range []string{"trade", "books5", "funding", "candle.1m", "candle.5m", "candle.15m"} {
		if !cfg.MonotonicTopics[topic] {
			t.Fatalf("MonotonicTopics[%q]=false, want true", topic)
		}
	}
}

func TestRunChecksLicenseBeforeStartingSidecar(t *testing.T) {
	started := false

	err := run(
		context.Background(),
		func(sidecar.BrainHandler) error {
			started = true
			return nil
		},
		func(name string, opts license.VerifyOptions) (*license.Result, error) {
			if name != "brain-data" {
				t.Fatalf("unexpected sidecar name: %s", name)
			}
			return nil, errors.New("license denied")
		},
	)
	if err == nil || err.Error() != "license: license denied" {
		t.Fatalf("unexpected error: %v", err)
	}
	if started {
		t.Fatal("sidecar should not start when license check fails")
	}
}

func TestRunStartsDataBrainAfterLicenseCheck(t *testing.T) {
	wantErr := errors.New("boom")

	err := runWithFactory(
		context.Background(),
		func(handler sidecar.BrainHandler) error {
			if handler.Kind() != agent.KindData {
				t.Fatalf("handler kind=%s, want %s", handler.Kind(), agent.KindData)
			}
			return wantErr
		},
		func(name string, opts license.VerifyOptions) (*license.Result, error) {
			if name != "brain-data" {
				t.Fatalf("unexpected sidecar name: %s", name)
			}
			return nil, nil
		},
		func(context.Context) (*data.Brain, func() error, error) {
			return data.NewBrain(defaultConfig()), nil, nil
		},
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNewRuntimeBrainFromEnvRestoresPersistedDataState(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "brain-data.json")
	stores, err := persistence.Open("file", dsn)
	if err != nil {
		t.Fatalf("Open(file): %v", err)
	}
	defer stores.Close()

	if err := stores.DataStateStore.Save(ctx, &persistence.DataState{
		Snapshots:       json.RawMessage(`[{"provider":"okx","topic":"trade","symbol":"BTC-USDT-SWAP","write_seq":7,"timestamp":1700000000000,"price":101.5}]`),
		ProviderHealths: json.RawMessage(`[{"name":"okx","state":"active"}]`),
		Validator: persistence.DataValidatorState{
			LastTS:     map[string]int64{"okx|trade|btc-usdt-swap": 1700000000000},
			LastDigest: map[string]uint64{"okx|trade|btc-usdt-swap": 123},
			Accepted:   1,
		},
	}); err != nil {
		t.Fatalf("DataStateStore.Save: %v", err)
	}

	t.Setenv("BRAIN_DATA_PERSIST_DRIVER", "file")
	t.Setenv("BRAIN_DATA_PERSIST_DSN", dsn)

	brain, closeFn, err := newRuntimeBrainFromEnv(ctx)
	if err != nil {
		t.Fatalf("newRuntimeBrainFromEnv: %v", err)
	}
	defer closeFn()

	resp, err := brain.HandleMethod(ctx, "tools/call", mustRawJSON(protocol.ToolCallRequest{
		Name:      quantcontracts.ToolDataGetSnapshot,
		Arguments: json.RawMessage(`{"symbol":"BTC-USDT-SWAP"}`),
	}))
	if err != nil {
		t.Fatalf("HandleMethod tools/call: %v", err)
	}
	var toolResult protocol.ToolCallResult
	if err := copyJSON(resp, &toolResult); err != nil {
		t.Fatalf("copyJSON: %v", err)
	}
	var output quantcontracts.SnapshotQueryResult
	if err := toolResult.DecodeOutput(&output); err != nil {
		t.Fatalf("DecodeOutput: %v", err)
	}
	if output.Snapshot == nil || output.Snapshot.Sequence != 7 {
		t.Fatalf("unexpected snapshot output: %+v", output)
	}
}

func TestNewRuntimeBrainFromEnvBootstrapsStaticFixtureProvider(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "brain-data.json")
	fixture := filepath.Join("..", "..", "test", "fixtures", "data", "market_events.okx.json")
	if _, err := os.Stat(fixture); err != nil {
		t.Fatalf("Stat fixture: %v", err)
	}

	t.Setenv("BRAIN_DATA_PERSIST_DRIVER", "file")
	t.Setenv("BRAIN_DATA_PERSIST_DSN", dsn)
	t.Setenv("BRAIN_DATA_STATIC_FIXTURE", fixture)
	t.Setenv("BRAIN_DATA_STATIC_PROVIDER_NAME", "okx")

	brain, closeFn, err := newRuntimeBrainFromEnv(ctx)
	if err != nil {
		t.Fatalf("newRuntimeBrainFromEnv: %v", err)
	}
	defer closeFn()

	resp, err := brain.HandleMethod(ctx, "tools/call", mustRawJSON(protocol.ToolCallRequest{
		Name:      quantcontracts.ToolDataGetSnapshot,
		Arguments: json.RawMessage(`{"symbol":"ETH-USDT-SWAP"}`),
	}))
	if err != nil {
		t.Fatalf("HandleMethod tools/call: %v", err)
	}
	var toolResult protocol.ToolCallResult
	if err := copyJSON(resp, &toolResult); err != nil {
		t.Fatalf("copyJSON: %v", err)
	}
	var output quantcontracts.SnapshotQueryResult
	if err := toolResult.DecodeOutput(&output); err != nil {
		t.Fatalf("DecodeOutput: %v", err)
	}
	if output.Snapshot == nil || output.Snapshot.Symbol != "ETH-USDT-SWAP" {
		t.Fatalf("unexpected snapshot output: %+v", output)
	}
}

func mustRawJSON(v any) json.RawMessage {
	raw, _ := json.Marshal(v)
	return raw
}

func copyJSON(src any, dst any) error {
	raw, err := json.Marshal(src)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, dst)
}
