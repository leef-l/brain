package provider

import (
	"fmt"
	"time"
)

// RegisterBuiltin registers the built-in provider factories (okx-swap, replay).
// Call this once at startup before creating providers from config.
func RegisterBuiltin(r *ProviderRegistry) {
	r.Register("okx-swap", okxSwapFactory)
	// Note: replay factory requires a store.CandleStore, which cannot be
	// passed through map[string]any cleanly. Use NewReplayProvider directly
	// for backtest scenarios. The registry entry is a placeholder.
	r.Register("replay", replayFactory)
}

func okxSwapFactory(cfg map[string]any) (DataProvider, error) {
	name, _ := cfg["name"].(string)
	if name == "" {
		name = "okx-swap"
	}

	var instruments []string
	if raw, ok := cfg["instruments"].([]any); ok {
		for _, v := range raw {
			if s, ok := v.(string); ok {
				instruments = append(instruments, s)
			}
		}
	}

	wsURL, _ := cfg["ws_url"].(string)
	bizWSURL, _ := cfg["business_ws_url"].(string)
	restURL, _ := cfg["rest_url"].(string)

	oc := OKXSwapConfig{
		Instruments: instruments,
	}
	if wsURL != "" {
		oc.WSURL = wsURL
	}
	if bizWSURL != "" {
		oc.BusinessWSURL = bizWSURL
	}
	if restURL != "" {
		oc.RESTURL = restURL
	}

	// Reconnect delays
	if delays, ok := cfg["reconnect_delays"].([]any); ok {
		for _, d := range delays {
			switch v := d.(type) {
			case float64:
				oc.ReconnectDelay = append(oc.ReconnectDelay, time.Duration(v)*time.Millisecond)
			case int:
				oc.ReconnectDelay = append(oc.ReconnectDelay, time.Duration(v)*time.Millisecond)
			}
		}
	}

	return NewOKXSwapProvider(name, oc), nil
}

func replayFactory(cfg map[string]any) (DataProvider, error) {
	// Replay provider requires a store.CandleStore which cannot be cleanly
	// passed through map[string]any. This factory exists for registry
	// completeness; use NewReplayProvider() directly for backtest workflows.
	return nil, fmt.Errorf("replay provider must be created directly via NewReplayProvider (requires CandleStore)")
}
