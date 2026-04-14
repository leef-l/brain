package persistence

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestMemDataStateStore_SaveGet(t *testing.T) {
	store := NewMemDataStateStore(nil)
	state := &DataState{
		Snapshots:       json.RawMessage(`[{"symbol":"BTC-USDT-SWAP","write_seq":7}]`),
		ProviderHealths: json.RawMessage(`[{"name":"okx","state":"active"}]`),
		Validator: DataValidatorState{
			LastTS:     map[string]int64{"okx|trade|btc-usdt-swap": 123},
			LastDigest: map[string]uint64{"okx|trade|btc-usdt-swap": 456},
			Accepted:   3,
		},
	}

	if err := store.Save(context.Background(), state); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := store.Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got.Snapshots) != `[{"symbol":"BTC-USDT-SWAP","write_seq":7}]` {
		t.Fatalf("snapshots=%s", string(got.Snapshots))
	}
	if got.Validator.LastTS["okx|trade|btc-usdt-swap"] != 123 {
		t.Fatalf("last ts=%d", got.Validator.LastTS["okx|trade|btc-usdt-swap"])
	}
}

func TestFileDataStateStore_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "brain.json")
	fs, err := OpenFileStore(path)
	if err != nil {
		t.Fatalf("OpenFileStore: %v", err)
	}

	state := &DataState{
		Snapshots: json.RawMessage(`[{"symbol":"ETH-USDT-SWAP","write_seq":9}]`),
		Validator: DataValidatorState{
			Rejected: 2,
		},
	}
	if err := fs.DataStateStore().Save(context.Background(), state); err != nil {
		t.Fatalf("Save: %v", err)
	}

	reopened, err := OpenFileStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, err := reopened.DataStateStore().Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	var snapshots []struct {
		Symbol   string `json:"symbol"`
		WriteSeq uint64 `json:"write_seq"`
	}
	if err := json.Unmarshal(got.Snapshots, &snapshots); err != nil {
		t.Fatalf("Unmarshal snapshots: %v", err)
	}
	if len(snapshots) != 1 || snapshots[0].Symbol != "ETH-USDT-SWAP" || snapshots[0].WriteSeq != 9 {
		t.Fatalf("snapshots=%+v", snapshots)
	}
	if got.Validator.Rejected != 2 {
		t.Fatalf("rejected=%d", got.Validator.Rejected)
	}
}
