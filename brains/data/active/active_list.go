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

// InstrumentInfo holds an instrument identifier with its 24h USDT volume.
type InstrumentInfo struct {
	InstID     string
	VolUsdt24h float64
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

	// Calculate USDT volume for each instrument.
	var instruments []InstrumentInfo
	for _, tk := range tickerResp.Data {
		volCcy, err1 := strconv.ParseFloat(tk.VolCcy24h, 64)
		last, err2 := strconv.ParseFloat(tk.Last, 64)
		if err1 != nil || err2 != nil || last <= 0 {
			continue
		}
		volUsdt := volCcy * last
		instruments = append(instruments, InstrumentInfo{
			InstID:     tk.InstID,
			VolUsdt24h: volUsdt,
		})
	}

	// Sort descending by volume.
	sort.Slice(instruments, func(i, j int) bool {
		return instruments[i].VolUsdt24h > instruments[j].VolUsdt24h
	})

	// Build the active set: top N with minimum volume.
	active := make(map[string]bool)
	var result []InstrumentInfo

	for _, inst := range instruments {
		if len(result) >= l.config.MaxInstruments {
			break
		}
		if inst.VolUsdt24h < l.config.MinVolume24h {
			break
		}
		active[inst.InstID] = true
		result = append(result, inst)
	}

	// Always include specified instruments.
	alwaysSet := make(map[string]bool)
	for _, id := range l.config.AlwaysInclude {
		alwaysSet[id] = true
	}
	// Add always-include instruments that aren't already in the result.
	for _, id := range l.config.AlwaysInclude {
		if !active[id] {
			active[id] = true
			// Find its volume info if available.
			vol := 0.0
			for _, inst := range instruments {
				if inst.InstID == id {
					vol = inst.VolUsdt24h
					break
				}
			}
			result = append(result, InstrumentInfo{InstID: id, VolUsdt24h: vol})
		}
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
