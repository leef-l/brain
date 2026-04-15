package quant

import "time"

// AccountConfig describes one trading account in the config file.
type AccountConfig struct {
	ID         string `json:"id" yaml:"id"`
	Exchange   string `json:"exchange" yaml:"exchange"`     // "okx", "paper"
	APIKey     string `json:"api_key" yaml:"api_key"`
	SecretKey  string `json:"secret_key" yaml:"secret_key"`
	Passphrase string `json:"passphrase" yaml:"passphrase"`
	BaseURL    string `json:"base_url" yaml:"base_url"`
	Simulated  bool   `json:"simulated" yaml:"simulated"` // OKX demo mode

	// Paper exchange config
	InitialEquity float64 `json:"initial_equity" yaml:"initial_equity"`

	// Tags for grouping
	Tags []string `json:"tags" yaml:"tags"`

	// AccountRouter config
	Route *RouteConfig `json:"route,omitempty" yaml:"route,omitempty"`
}

// UnitConfig describes one trading unit in the config file.
type UnitConfig struct {
	ID          string   `json:"id" yaml:"id"`
	AccountID   string   `json:"account_id" yaml:"account_id"`
	Symbols     []string `json:"symbols" yaml:"symbols"`
	Timeframe   string   `json:"timeframe" yaml:"timeframe"`
	MaxLeverage int      `json:"max_leverage" yaml:"max_leverage"`
	Enabled     bool     `json:"enabled" yaml:"enabled"`
}

// FullConfig is the complete quant brain configuration.
type FullConfig struct {
	Brain    Config          `json:"brain" yaml:"brain"`
	Accounts []AccountConfig `json:"accounts" yaml:"accounts"`
	Units    []UnitConfig    `json:"units" yaml:"units"`
}

// DefaultFullConfig returns a minimal working configuration with a paper account.
func DefaultFullConfig() FullConfig {
	return FullConfig{
		Brain: Config{
			CycleInterval:    5 * time.Second,
			DefaultTimeframe: "1H",
		},
		Accounts: []AccountConfig{
			{
				ID:            "paper-default",
				Exchange:      "paper",
				InitialEquity: 10000,
				Tags:          []string{"test"},
			},
		},
		Units: []UnitConfig{
			{
				ID:          "default-unit",
				AccountID:   "paper-default",
				Timeframe:   "1H",
				MaxLeverage: 10,
				Enabled:     true,
			},
		},
	}
}
