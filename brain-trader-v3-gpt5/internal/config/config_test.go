package config

import (
	"os"
	"testing"
	"time"
)

func TestLoadBytes(t *testing.T) {
	t.Setenv("PG_PASSWORD", "secret-pass")
	t.Setenv("OKX_API_KEY", "api-key")
	t.Setenv("OKX_SECRET", "api-secret")
	t.Setenv("OKX_PASSPHRASE", "phrase")

	cfg, err := LoadBytes([]byte(`
mode: live
instruments:
  - BTC-USDT-SWAP
  - ETH-USDT-SWAP
data:
  okx_ws_url: wss://ws.okx.com:8443/ws/v5/public
  ws_reconnect_delay: [1, 2, 4]
  rest_poll_interval: 30s
  history_candles: 500
strategy:
  initial_weights: [0.30, 0.25, 0.25, 0.20]
  weight_update_interval: 24h
  weight_min: 0.10
  weight_max: 0.40
  weight_temperature: 2.0
  signal_threshold: 0.45
vector:
  dimensions: 192
  hnsw_m: 16
  hnsw_ef_construction: 200
  hnsw_ef_search: 100
  pattern_retention_1m: 60d
  pattern_retention_5m: 180d
risk:
  max_position_pct: 5.0
  max_leverage: 20
  max_concurrent: 5
  max_total_exposure: 30.0
  max_same_direction: 20.0
  daily_loss_pause: 3.0
  daily_loss_close: 5.0
  circuit_breaker_volatility: 99
  circuit_breaker_btc_move: 5.0
llm:
  enabled: true
  model: sonnet
  trigger_concurrent: 3
  trigger_position_pct: 5.0
  trigger_daily_loss: 3.0
  timeout: 10s
database:
  host: localhost
  port: 5432
  dbname: brain_trader
  user: trader
  password: ${PG_PASSWORD}
  sslmode: disable
  schema: trader
  max_conns: 10
executor:
  backend: paper
  okx_api_key: ${OKX_API_KEY}
  okx_secret: ${OKX_SECRET}
  okx_passphrase: ${OKX_PASSPHRASE}
  paper_slippage: 0.0003
  paper_maker_fee: 0.0002
  paper_taker_fee: 0.0005
`))
	if err != nil {
		t.Fatalf("LoadBytes() error = %v", err)
	}

	if cfg.Mode != "live" {
		t.Fatalf("Mode = %q", cfg.Mode)
	}
	if got, want := len(cfg.Instruments), 2; got != want {
		t.Fatalf("len(Instruments) = %d, want %d", got, want)
	}
	if cfg.Database.Password != "secret-pass" {
		t.Fatalf("Database.Password = %q", cfg.Database.Password)
	}
	if cfg.Executor.OKXAPIKey != "api-key" {
		t.Fatalf("Executor.OKXAPIKey = %q", cfg.Executor.OKXAPIKey)
	}
	if cfg.Vector.PatternRetention1m != 60*24*time.Hour {
		t.Fatalf("Vector.PatternRetention1m = %s", cfg.Vector.PatternRetention1m)
	}
}

func TestLoadBytesMissingRequired(t *testing.T) {
	_, err := LoadBytes([]byte(`mode: sideways`))
	if err == nil {
		t.Fatal("expected validation error")
	}
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
