package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Config is the top-level trading configuration.
type Config struct {
	Mode        string         `json:"mode"`
	Instruments []string       `json:"instruments"`
	Data        DataConfig     `json:"data"`
	Strategy    StrategyConfig `json:"strategy"`
	Vector      VectorConfig   `json:"vector"`
	Risk        RiskConfig     `json:"risk"`
	LLM         LLMConfig      `json:"llm"`
	Database    DatabaseConfig `json:"database"`
	Executor    ExecutorConfig `json:"executor"`
}

// DataConfig controls market data ingestion.
type DataConfig struct {
	OKXWSURL         string          `json:"okx_ws_url"`
	WSReconnectDelay []time.Duration `json:"ws_reconnect_delay"`
	RestPollInterval time.Duration   `json:"rest_poll_interval"`
	HistoryCandles   int             `json:"history_candles"`
}

// StrategyConfig controls strategy pool behavior.
type StrategyConfig struct {
	InitialWeights       []float64     `json:"initial_weights"`
	WeightUpdateInterval time.Duration `json:"weight_update_interval"`
	WeightMin            float64       `json:"weight_min"`
	WeightMax            float64       `json:"weight_max"`
	WeightTemperature    float64       `json:"weight_temperature"`
	SignalThreshold      float64       `json:"signal_threshold"`
}

// VectorConfig controls vector persistence and HNSW tuning.
type VectorConfig struct {
	Dimensions         int           `json:"dimensions"`
	HNSWM              int           `json:"hnsw_m"`
	HNSWEfConstruction int           `json:"hnsw_ef_construction"`
	HNSWEfSearch       int           `json:"hnsw_ef_search"`
	PatternRetention1m time.Duration `json:"pattern_retention_1m"`
	PatternRetention5m time.Duration `json:"pattern_retention_5m"`
}

// RiskConfig controls pre-trade and portfolio risk limits.
type RiskConfig struct {
	MaxPositionPct           float64 `json:"max_position_pct"`
	MaxLeverage              int     `json:"max_leverage"`
	MaxConcurrent            int     `json:"max_concurrent"`
	MaxTotalExposure         float64 `json:"max_total_exposure"`
	MaxSameDirection         float64 `json:"max_same_direction"`
	DailyLossPause           float64 `json:"daily_loss_pause"`
	DailyLossClose           float64 `json:"daily_loss_close"`
	CircuitBreakerVolatility int     `json:"circuit_breaker_volatility"`
	CircuitBreakerBTCMove    float64 `json:"circuit_breaker_btc_move"`
}

// LLMConfig controls review calls to the model.
type LLMConfig struct {
	Enabled            bool          `json:"enabled"`
	Model              string        `json:"model"`
	TriggerConcurrent  int           `json:"trigger_concurrent"`
	TriggerPositionPct float64       `json:"trigger_position_pct"`
	TriggerDailyLoss   float64       `json:"trigger_daily_loss"`
	Timeout            time.Duration `json:"timeout"`
}

// DatabaseConfig controls PostgreSQL connectivity.
type DatabaseConfig struct {
	Host            string        `json:"host"`
	Port            int           `json:"port"`
	DBName          string        `json:"dbname"`
	User            string        `json:"user"`
	Password        string        `json:"password"`
	SSLMode         string        `json:"sslmode"`
	Schema          string        `json:"schema"`
	MaxConns        int           `json:"max_conns"`
	MinConns        int           `json:"min_conns"`
	ConnMaxIdleTime time.Duration `json:"conn_max_idle_time"`
	ConnMaxLifetime time.Duration `json:"conn_max_lifetime"`
}

// ExecutorConfig controls execution backend selection.
type ExecutorConfig struct {
	Backend       string  `json:"backend"`
	OKXAPIKey     string  `json:"okx_api_key"`
	OKXSecret     string  `json:"okx_secret"`
	OKXPassphrase string  `json:"okx_passphrase"`
	PaperSlippage float64 `json:"paper_slippage"`
	PaperMakerFee float64 `json:"paper_maker_fee"`
	PaperTakerFee float64 `json:"paper_taker_fee"`
}

// Default returns the documented baseline configuration.
func Default() Config {
	return Config{
		Mode: "paper",
		Instruments: []string{
			"BTC-USDT-SWAP",
			"ETH-USDT-SWAP",
			"SOL-USDT-SWAP",
		},
		Data: DataConfig{
			OKXWSURL:         "wss://ws.okx.com:8443/ws/v5/public",
			WSReconnectDelay: []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second},
			RestPollInterval: 30 * time.Second,
			HistoryCandles:   500,
		},
		Strategy: StrategyConfig{
			InitialWeights:       []float64{0.30, 0.25, 0.25, 0.20},
			WeightUpdateInterval: 24 * time.Hour,
			WeightMin:            0.10,
			WeightMax:            0.40,
			WeightTemperature:    2.0,
			SignalThreshold:      0.45,
		},
		Vector: VectorConfig{
			Dimensions:         192,
			HNSWM:              16,
			HNSWEfConstruction: 200,
			HNSWEfSearch:       100,
			PatternRetention1m: 60 * 24 * time.Hour,
			PatternRetention5m: 180 * 24 * time.Hour,
		},
		Risk: RiskConfig{
			MaxPositionPct:           5.0,
			MaxLeverage:              20,
			MaxConcurrent:            5,
			MaxTotalExposure:         30.0,
			MaxSameDirection:         20.0,
			DailyLossPause:           3.0,
			DailyLossClose:           5.0,
			CircuitBreakerVolatility: 99,
			CircuitBreakerBTCMove:    5.0,
		},
		LLM: LLMConfig{
			Enabled:            true,
			Model:              "haiku",
			TriggerConcurrent:  3,
			TriggerPositionPct: 5.0,
			TriggerDailyLoss:   3.0,
			Timeout:            10 * time.Second,
		},
		Database: DatabaseConfig{
			Host:            "localhost",
			Port:            5432,
			DBName:          "brain_trader",
			User:            "trader",
			SSLMode:         "disable",
			Schema:          "trader",
			MaxConns:        10,
			MinConns:        1,
			ConnMaxIdleTime: 30 * time.Minute,
			ConnMaxLifetime: 2 * time.Hour,
		},
		Executor: ExecutorConfig{
			Backend:       "paper",
			PaperSlippage: 0.0003,
			PaperMakerFee: 0.0002,
			PaperTakerFee: 0.0005,
		},
	}
}

// Load reads a trading config from path.
func Load(path string) (*Config, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("config: resolve path: %w", err)
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", abs, err)
	}
	cfg, err := LoadBytes(data)
	if err != nil {
		return nil, err
	}
	return cfg, nil
}

// LoadBytes parses a YAML document and applies defaults first.
func LoadBytes(data []byte) (*Config, error) {
	raw, err := parseYAML(string(data))
	if err != nil {
		return nil, err
	}

	cfg := Default()
	if err := decodeConfig(raw, &cfg); err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Validate checks for the minimum safe configuration requirements.
func (c Config) Validate() error {
	if c.Mode != "paper" && c.Mode != "live" {
		return fmt.Errorf("config: mode must be paper or live, got %q", c.Mode)
	}
	if len(c.Instruments) == 0 {
		return errors.New("config: instruments must not be empty")
	}
	if len(c.Instruments) > 30 {
		return fmt.Errorf("config: instruments exceeds max 30, got %d", len(c.Instruments))
	}
	if err := validateUniqueStrings(c.Instruments, "instrument"); err != nil {
		return err
	}
	if c.Data.OKXWSURL == "" {
		return errors.New("config: data.okx_ws_url is required")
	}
	if c.Data.HistoryCandles <= 0 {
		return errors.New("config: data.history_candles must be positive")
	}
	if len(c.Strategy.InitialWeights) != 4 {
		return fmt.Errorf("config: strategy.initial_weights must have 4 entries, got %d", len(c.Strategy.InitialWeights))
	}
	if c.Vector.Dimensions <= 0 {
		return errors.New("config: vector.dimensions must be positive")
	}
	if c.Database.Host == "" || c.Database.DBName == "" || c.Database.User == "" {
		return errors.New("config: database host, dbname and user are required")
	}
	if c.Database.Port <= 0 {
		return errors.New("config: database.port must be positive")
	}
	if c.Database.Schema == "" {
		return errors.New("config: database.schema must not be empty")
	}
	if c.Database.MaxConns < 0 || c.Database.MinConns < 0 {
		return errors.New("config: database connection limits must not be negative")
	}
	if c.Executor.Backend != "paper" && c.Executor.Backend != "okx_ws" && c.Executor.Backend != "okx_rest" {
		return fmt.Errorf("config: executor.backend must be paper, okx_ws or okx_rest, got %q", c.Executor.Backend)
	}
	if c.LLM.Enabled {
		switch c.LLM.Model {
		case "haiku", "sonnet":
		default:
			return fmt.Errorf("config: llm.model must be haiku or sonnet, got %q", c.LLM.Model)
		}
	}
	return nil
}

func validateUniqueStrings(values []string, label string) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("config: %s must not contain empty values", label)
		}
		if _, ok := seen[value]; ok {
			return fmt.Errorf("config: duplicate %s %q", label, value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func decodeConfig(raw map[string]any, cfg *Config) error {
	if cfg == nil {
		return errors.New("config: nil target")
	}

	if v, ok := raw["mode"]; ok {
		cfg.Mode = stringValue(v)
	}
	if v, ok := raw["instruments"]; ok {
		values, err := stringSlice(v)
		if err != nil {
			return fmt.Errorf("config: instruments: %w", err)
		}
		cfg.Instruments = values
	}
	if v, ok := raw["data"]; ok {
		m, err := mapValue(v)
		if err != nil {
			return fmt.Errorf("config: data: %w", err)
		}
		if value, ok := m["okx_ws_url"]; ok {
			cfg.Data.OKXWSURL = stringValue(value)
		}
		if value, ok := m["ws_reconnect_delay"]; ok {
			values, err := durationSecondsSlice(value)
			if err != nil {
				return fmt.Errorf("config: data.ws_reconnect_delay: %w", err)
			}
			cfg.Data.WSReconnectDelay = values
		}
		if value, ok := m["rest_poll_interval"]; ok {
			duration, err := durationValue(value)
			if err != nil {
				return fmt.Errorf("config: data.rest_poll_interval: %w", err)
			}
			cfg.Data.RestPollInterval = duration
		}
		if value, ok := m["history_candles"]; ok {
			n, err := intValue(value)
			if err != nil {
				return fmt.Errorf("config: data.history_candles: %w", err)
			}
			cfg.Data.HistoryCandles = n
		}
	}
	if v, ok := raw["strategy"]; ok {
		m, err := mapValue(v)
		if err != nil {
			return fmt.Errorf("config: strategy: %w", err)
		}
		if value, ok := m["initial_weights"]; ok {
			values, err := floatSlice(value)
			if err != nil {
				return fmt.Errorf("config: strategy.initial_weights: %w", err)
			}
			cfg.Strategy.InitialWeights = values
		}
		if value, ok := m["weight_update_interval"]; ok {
			duration, err := durationValue(value)
			if err != nil {
				return fmt.Errorf("config: strategy.weight_update_interval: %w", err)
			}
			cfg.Strategy.WeightUpdateInterval = duration
		}
		if value, ok := m["weight_min"]; ok {
			n, err := floatValue(value)
			if err != nil {
				return fmt.Errorf("config: strategy.weight_min: %w", err)
			}
			cfg.Strategy.WeightMin = n
		}
		if value, ok := m["weight_max"]; ok {
			n, err := floatValue(value)
			if err != nil {
				return fmt.Errorf("config: strategy.weight_max: %w", err)
			}
			cfg.Strategy.WeightMax = n
		}
		if value, ok := m["weight_temperature"]; ok {
			n, err := floatValue(value)
			if err != nil {
				return fmt.Errorf("config: strategy.weight_temperature: %w", err)
			}
			cfg.Strategy.WeightTemperature = n
		}
		if value, ok := m["signal_threshold"]; ok {
			n, err := floatValue(value)
			if err != nil {
				return fmt.Errorf("config: strategy.signal_threshold: %w", err)
			}
			cfg.Strategy.SignalThreshold = n
		}
	}
	if v, ok := raw["vector"]; ok {
		m, err := mapValue(v)
		if err != nil {
			return fmt.Errorf("config: vector: %w", err)
		}
		if value, ok := m["dimensions"]; ok {
			n, err := intValue(value)
			if err != nil {
				return fmt.Errorf("config: vector.dimensions: %w", err)
			}
			cfg.Vector.Dimensions = n
		}
		if value, ok := m["hnsw_m"]; ok {
			n, err := intValue(value)
			if err != nil {
				return fmt.Errorf("config: vector.hnsw_m: %w", err)
			}
			cfg.Vector.HNSWM = n
		}
		if value, ok := m["hnsw_ef_construction"]; ok {
			n, err := intValue(value)
			if err != nil {
				return fmt.Errorf("config: vector.hnsw_ef_construction: %w", err)
			}
			cfg.Vector.HNSWEfConstruction = n
		}
		if value, ok := m["hnsw_ef_search"]; ok {
			n, err := intValue(value)
			if err != nil {
				return fmt.Errorf("config: vector.hnsw_ef_search: %w", err)
			}
			cfg.Vector.HNSWEfSearch = n
		}
		if value, ok := m["pattern_retention_1m"]; ok {
			d, err := durationValue(value)
			if err != nil {
				return fmt.Errorf("config: vector.pattern_retention_1m: %w", err)
			}
			cfg.Vector.PatternRetention1m = d
		}
		if value, ok := m["pattern_retention_5m"]; ok {
			d, err := durationValue(value)
			if err != nil {
				return fmt.Errorf("config: vector.pattern_retention_5m: %w", err)
			}
			cfg.Vector.PatternRetention5m = d
		}
	}
	if v, ok := raw["risk"]; ok {
		m, err := mapValue(v)
		if err != nil {
			return fmt.Errorf("config: risk: %w", err)
		}
		if value, ok := m["max_position_pct"]; ok {
			n, err := floatValue(value)
			if err != nil {
				return fmt.Errorf("config: risk.max_position_pct: %w", err)
			}
			cfg.Risk.MaxPositionPct = n
		}
		if value, ok := m["max_leverage"]; ok {
			n, err := intValue(value)
			if err != nil {
				return fmt.Errorf("config: risk.max_leverage: %w", err)
			}
			cfg.Risk.MaxLeverage = n
		}
		if value, ok := m["max_concurrent"]; ok {
			n, err := intValue(value)
			if err != nil {
				return fmt.Errorf("config: risk.max_concurrent: %w", err)
			}
			cfg.Risk.MaxConcurrent = n
		}
		if value, ok := m["max_total_exposure"]; ok {
			n, err := floatValue(value)
			if err != nil {
				return fmt.Errorf("config: risk.max_total_exposure: %w", err)
			}
			cfg.Risk.MaxTotalExposure = n
		}
		if value, ok := m["max_same_direction"]; ok {
			n, err := floatValue(value)
			if err != nil {
				return fmt.Errorf("config: risk.max_same_direction: %w", err)
			}
			cfg.Risk.MaxSameDirection = n
		}
		if value, ok := m["daily_loss_pause"]; ok {
			n, err := floatValue(value)
			if err != nil {
				return fmt.Errorf("config: risk.daily_loss_pause: %w", err)
			}
			cfg.Risk.DailyLossPause = n
		}
		if value, ok := m["daily_loss_close"]; ok {
			n, err := floatValue(value)
			if err != nil {
				return fmt.Errorf("config: risk.daily_loss_close: %w", err)
			}
			cfg.Risk.DailyLossClose = n
		}
		if value, ok := m["circuit_breaker_volatility"]; ok {
			n, err := intValue(value)
			if err != nil {
				return fmt.Errorf("config: risk.circuit_breaker_volatility: %w", err)
			}
			cfg.Risk.CircuitBreakerVolatility = n
		}
		if value, ok := m["circuit_breaker_btc_move"]; ok {
			n, err := floatValue(value)
			if err != nil {
				return fmt.Errorf("config: risk.circuit_breaker_btc_move: %w", err)
			}
			cfg.Risk.CircuitBreakerBTCMove = n
		}
	}
	if v, ok := raw["llm"]; ok {
		m, err := mapValue(v)
		if err != nil {
			return fmt.Errorf("config: llm: %w", err)
		}
		if value, ok := m["enabled"]; ok {
			b, err := boolValue(value)
			if err != nil {
				return fmt.Errorf("config: llm.enabled: %w", err)
			}
			cfg.LLM.Enabled = b
		}
		if value, ok := m["model"]; ok {
			cfg.LLM.Model = stringValue(value)
		}
		if value, ok := m["trigger_concurrent"]; ok {
			n, err := intValue(value)
			if err != nil {
				return fmt.Errorf("config: llm.trigger_concurrent: %w", err)
			}
			cfg.LLM.TriggerConcurrent = n
		}
		if value, ok := m["trigger_position_pct"]; ok {
			n, err := floatValue(value)
			if err != nil {
				return fmt.Errorf("config: llm.trigger_position_pct: %w", err)
			}
			cfg.LLM.TriggerPositionPct = n
		}
		if value, ok := m["trigger_daily_loss"]; ok {
			n, err := floatValue(value)
			if err != nil {
				return fmt.Errorf("config: llm.trigger_daily_loss: %w", err)
			}
			cfg.LLM.TriggerDailyLoss = n
		}
		if value, ok := m["timeout"]; ok {
			d, err := durationValue(value)
			if err != nil {
				return fmt.Errorf("config: llm.timeout: %w", err)
			}
			cfg.LLM.Timeout = d
		}
	}
	if v, ok := raw["database"]; ok {
		m, err := mapValue(v)
		if err != nil {
			return fmt.Errorf("config: database: %w", err)
		}
		if value, ok := m["host"]; ok {
			cfg.Database.Host = stringValue(value)
		}
		if value, ok := m["port"]; ok {
			n, err := intValue(value)
			if err != nil {
				return fmt.Errorf("config: database.port: %w", err)
			}
			cfg.Database.Port = n
		}
		if value, ok := m["dbname"]; ok {
			cfg.Database.DBName = stringValue(value)
		}
		if value, ok := m["user"]; ok {
			cfg.Database.User = stringValue(value)
		}
		if value, ok := m["password"]; ok {
			cfg.Database.Password = stringValue(value)
		}
		if value, ok := m["sslmode"]; ok {
			cfg.Database.SSLMode = stringValue(value)
		}
		if value, ok := m["schema"]; ok {
			cfg.Database.Schema = stringValue(value)
		}
		if value, ok := m["max_conns"]; ok {
			n, err := intValue(value)
			if err != nil {
				return fmt.Errorf("config: database.max_conns: %w", err)
			}
			cfg.Database.MaxConns = n
		}
		if value, ok := m["min_conns"]; ok {
			n, err := intValue(value)
			if err != nil {
				return fmt.Errorf("config: database.min_conns: %w", err)
			}
			cfg.Database.MinConns = n
		}
		if value, ok := m["conn_max_idle_time"]; ok {
			d, err := durationValue(value)
			if err != nil {
				return fmt.Errorf("config: database.conn_max_idle_time: %w", err)
			}
			cfg.Database.ConnMaxIdleTime = d
		}
		if value, ok := m["conn_max_lifetime"]; ok {
			d, err := durationValue(value)
			if err != nil {
				return fmt.Errorf("config: database.conn_max_lifetime: %w", err)
			}
			cfg.Database.ConnMaxLifetime = d
		}
	}
	if v, ok := raw["executor"]; ok {
		m, err := mapValue(v)
		if err != nil {
			return fmt.Errorf("config: executor: %w", err)
		}
		if value, ok := m["backend"]; ok {
			cfg.Executor.Backend = stringValue(value)
		}
		if value, ok := m["okx_api_key"]; ok {
			cfg.Executor.OKXAPIKey = stringValue(value)
		}
		if value, ok := m["okx_secret"]; ok {
			cfg.Executor.OKXSecret = stringValue(value)
		}
		if value, ok := m["okx_passphrase"]; ok {
			cfg.Executor.OKXPassphrase = stringValue(value)
		}
		if value, ok := m["paper_slippage"]; ok {
			n, err := floatValue(value)
			if err != nil {
				return fmt.Errorf("config: executor.paper_slippage: %w", err)
			}
			cfg.Executor.PaperSlippage = n
		}
		if value, ok := m["paper_maker_fee"]; ok {
			n, err := floatValue(value)
			if err != nil {
				return fmt.Errorf("config: executor.paper_maker_fee: %w", err)
			}
			cfg.Executor.PaperMakerFee = n
		}
		if value, ok := m["paper_taker_fee"]; ok {
			n, err := floatValue(value)
			if err != nil {
				return fmt.Errorf("config: executor.paper_taker_fee: %w", err)
			}
			cfg.Executor.PaperTakerFee = n
		}
	}
	return nil
}

func stringValue(v any) string {
	switch value := v.(type) {
	case string:
		return strings.TrimSpace(expandEnv(value))
	case fmt.Stringer:
		return strings.TrimSpace(expandEnv(value.String()))
	default:
		return strings.TrimSpace(expandEnv(fmt.Sprint(v)))
	}
}

func boolValue(v any) (bool, error) {
	switch value := v.(type) {
	case bool:
		return value, nil
	case string:
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "true", "yes", "on":
			return true, nil
		case "false", "no", "off":
			return false, nil
		}
	}
	return false, fmt.Errorf("invalid bool %T", v)
}

func intValue(v any) (int, error) {
	switch value := v.(type) {
	case int:
		return value, nil
	case int64:
		return int(value), nil
	case float64:
		return int(value), nil
	case string:
		n, err := parseInt(value)
		if err != nil {
			return 0, err
		}
		return n, nil
	default:
		return 0, fmt.Errorf("invalid integer %T", v)
	}
}

func floatValue(v any) (float64, error) {
	switch value := v.(type) {
	case float64:
		return value, nil
	case float32:
		return float64(value), nil
	case int:
		return float64(value), nil
	case int64:
		return float64(value), nil
	case string:
		return parseFloat(value)
	default:
		return 0, fmt.Errorf("invalid number %T", v)
	}
}

func durationValue(v any) (time.Duration, error) {
	switch value := v.(type) {
	case time.Duration:
		return value, nil
	case int:
		return time.Duration(value) * time.Second, nil
	case int64:
		return time.Duration(value) * time.Second, nil
	case float64:
		return time.Duration(value * float64(time.Second)), nil
	case string:
		value = strings.TrimSpace(expandEnv(value))
		if value == "" {
			return 0, nil
		}
		if strings.HasSuffix(value, "d") {
			n, err := parseInt(strings.TrimSuffix(value, "d"))
			if err != nil {
				return 0, err
			}
			return time.Duration(n) * 24 * time.Hour, nil
		}
		if d, err := time.ParseDuration(value); err == nil {
			return d, nil
		}
		n, err := parseInt(value)
		if err != nil {
			return 0, err
		}
		return time.Duration(n) * time.Second, nil
	default:
		return 0, fmt.Errorf("invalid duration %T", v)
	}
}

func durationSecondsSlice(v any) ([]time.Duration, error) {
	items, err := sliceValue(v)
	if err != nil {
		return nil, err
	}
	out := make([]time.Duration, 0, len(items))
	for _, item := range items {
		d, err := durationValue(item)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}

func floatSlice(v any) ([]float64, error) {
	items, err := sliceValue(v)
	if err != nil {
		return nil, err
	}
	out := make([]float64, 0, len(items))
	for _, item := range items {
		n, err := floatValue(item)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, nil
}

func stringSlice(v any) ([]string, error) {
	items, err := sliceValue(v)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, stringValue(item))
	}
	return out, nil
}

func sliceValue(v any) ([]any, error) {
	switch value := v.(type) {
	case []any:
		return value, nil
	case []string:
		out := make([]any, 0, len(value))
		for _, item := range value {
			out = append(out, item)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("expected list, got %T", v)
	}
}

func mapValue(v any) (map[string]any, error) {
	switch value := v.(type) {
	case map[string]any:
		return value, nil
	default:
		return nil, fmt.Errorf("expected map, got %T", v)
	}
}

func parseInt(s string) (int, error) {
	n, err := parseInt64(s)
	if err != nil {
		return 0, err
	}
	return int(n), nil
}

func parseInt64(s string) (int64, error) {
	s = strings.TrimSpace(expandEnv(s))
	if s == "" {
		return 0, errors.New("empty integer")
	}
	var sign int64 = 1
	if strings.HasPrefix(s, "+") {
		s = s[1:]
	}
	if strings.HasPrefix(s, "-") {
		sign = -1
		s = s[1:]
	}
	var n int64
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("invalid integer %q", s)
		}
		n = n*10 + int64(r-'0')
	}
	return n * sign, nil
}

func parseFloat(s string) (float64, error) {
	s = strings.TrimSpace(expandEnv(s))
	if s == "" {
		return 0, errors.New("empty float")
	}
	var sign float64 = 1
	if strings.HasPrefix(s, "+") {
		s = s[1:]
	}
	if strings.HasPrefix(s, "-") {
		sign = -1
		s = s[1:]
	}
	var whole, frac float64
	var fracDiv float64 = 1
	seenDot := false
	for _, r := range s {
		switch {
		case r == '.':
			if seenDot {
				return 0, fmt.Errorf("invalid float %q", s)
			}
			seenDot = true
		case r >= '0' && r <= '9':
			if !seenDot {
				whole = whole*10 + float64(r-'0')
			} else {
				fracDiv *= 10
				frac = frac*10 + float64(r-'0')
			}
		default:
			return 0, fmt.Errorf("invalid float %q", s)
		}
	}
	return sign * (whole + frac/fracDiv), nil
}

func expandEnv(s string) string {
	return os.Expand(s, func(key string) string {
		return os.Getenv(key)
	})
}

// SortedInstruments returns a copy of the instruments slice in lexical order.
func (c Config) SortedInstruments() []string {
	out := append([]string(nil), c.Instruments...)
	sort.Strings(out)
	return out
}
