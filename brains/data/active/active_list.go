// Package active manages the list of actively traded instruments.
package active

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"
)

// Config holds settings for the ActiveList.
type Config struct {
	RESTURL        string        // e.g. "https://www.okx.com"
	MinVolume24h   float64       // minimum 24h USDT volume, default 10_000_000
	MaxInstruments int           // max instruments to track, default 100
	UpdateInterval time.Duration // how often to refresh, default 7 days
	AlwaysInclude  []string      // instruments always included regardless of volume
	RankByVolatility bool        // true = rank by 24h amplitude instead of volume
	MinAmplitudePct  float64     // minimum 24h amplitude %, e.g. 3.0 = filter < 3% swing
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() Config {
	return Config{
		RESTURL:        "https://www.okx.com",
		MinVolume24h:   10_000_000,
		MaxInstruments: 100,
		UpdateInterval: 7 * 24 * time.Hour,
		AlwaysInclude:  []string{"BTC-USDT-SWAP", "ETH-USDT-SWAP", "SOL-USDT-SWAP"},
	}
}

// InstrumentInfo holds an instrument identifier with its 24h USDT volume and price change.
type InstrumentInfo struct {
	InstID       string
	VolUsdt24h   float64
	Change24hPct float64 // absolute 24h price change percentage
}

// ActiveList tracks the set of actively traded instruments.
type ActiveList struct {
	config     Config
	httpClient *http.Client
	active     map[string]bool
	lastUpdate time.Time
	mu         sync.RWMutex
}

// New creates an ActiveList.
func New(config Config, httpClient *http.Client) *ActiveList {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &ActiveList{
		config:     config,
		httpClient: httpClient,
		active:     make(map[string]bool),
	}
}

// okxTickerResp is the OKX API response structure for tickers.
type okxTickerResp struct {
	Code string       `json:"code"`
	Data []okxTicker  `json:"data"`
}

type okxTicker struct {
	InstID     string `json:"instId"`
	Last       string `json:"last"`
	Open24h    string `json:"open24h"`
	High24h    string `json:"high24h"`
	Low24h     string `json:"low24h"`
	VolCcy24h  string `json:"volCcy24h"`
}

// Refresh fetches ticker data from OKX, ranks by USDT volume, and updates the active list.
func (l *ActiveList) Refresh(ctx context.Context) ([]InstrumentInfo, error) {
	url := l.config.RESTURL + "/api/v5/market/tickers?instType=SWAP"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	resp, err := l.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch tickers: %w", err)
	}
	defer resp.Body.Close()

	var tickerResp okxTickerResp
	if err := json.NewDecoder(resp.Body).Decode(&tickerResp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if tickerResp.Code != "0" {
		return nil, fmt.Errorf("OKX API error code: %s", tickerResp.Code)
	}

	// Calculate USDT volume and 24h amplitude for each instrument.
	var instruments []InstrumentInfo
	for _, tk := range tickerResp.Data {
		volCcy, err1 := strconv.ParseFloat(tk.VolCcy24h, 64)
		last, err2 := strconv.ParseFloat(tk.Last, 64)
		if err1 != nil || err2 != nil || last <= 0 {
			continue
		}
		volUsdt := volCcy * last

		// 24h amplitude = (high - low) / low * 100
		var amplitude float64
		high, errH := strconv.ParseFloat(tk.High24h, 64)
		low, errL := strconv.ParseFloat(tk.Low24h, 64)
		if errH == nil && errL == nil && low > 0 {
			amplitude = (high - low) / low * 100
		}

		instruments = append(instruments, InstrumentInfo{
			InstID:       tk.InstID,
			VolUsdt24h:   volUsdt,
			Change24hPct: amplitude,
		})
	}

	// First pass: filter by minimum volume (need enough liquidity to trade).
	var qualified []InstrumentInfo
	for _, inst := range instruments {
		if inst.VolUsdt24h < l.config.MinVolume24h {
			continue
		}
		if l.config.MinAmplitudePct > 0 && inst.Change24hPct < l.config.MinAmplitudePct {
			continue
		}
		qualified = append(qualified, inst)
	}

	// Sort: by amplitude descending (volatility mode) or by volume descending.
	if l.config.RankByVolatility {
		sort.Slice(qualified, func(i, j int) bool {
			return qualified[i].Change24hPct > qualified[j].Change24hPct
		})
	} else {
		sort.Slice(qualified, func(i, j int) bool {
			return qualified[i].VolUsdt24h > qualified[j].VolUsdt24h
		})
	}

	// Take top N.
	active := make(map[string]bool)
	var result []InstrumentInfo
	for _, inst := range qualified {
		if len(result) >= l.config.MaxInstruments {
			break
		}
		active[inst.InstID] = true
		result = append(result, inst)
	}

	// Always include specified instruments (pinned).
	for _, id := range l.config.AlwaysInclude {
		if active[id] {
			continue
		}
		active[id] = true
		// Find its info from the full list.
		info := InstrumentInfo{InstID: id}
		for _, inst := range instruments {
			if inst.InstID == id {
				info.VolUsdt24h = inst.VolUsdt24h
				info.Change24hPct = inst.Change24hPct
				break
			}
		}
		result = append(result, info)
	}

	l.mu.Lock()
	l.active = active
	l.lastUpdate = time.Now()
	l.mu.Unlock()

	return result, nil
}

// IsActive returns true if the instrument is in the active list.
func (l *ActiveList) IsActive(instID string) bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.active[instID]
}

// List returns a copy of the current active instrument IDs.
func (l *ActiveList) List() []string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	list := make([]string, 0, len(l.active))
	for id := range l.active {
		list = append(list, id)
	}
	sort.Strings(list)
	return list
}

// Count returns the number of active instruments.
func (l *ActiveList) Count() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return len(l.active)
}

// LastUpdate returns the time of the last successful refresh.
func (l *ActiveList) LastUpdate() time.Time {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.lastUpdate
}
