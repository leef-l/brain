package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/leef-l/brain/sdk/netutil"
)

// OKXSwapConfig holds the configuration for OKXSwapProvider.
type OKXSwapConfig struct {
	WSURL          string          // wss://ws.okx.com:8443/ws/v5/public
	BusinessWSURL  string          // wss://ws.okx.com:8443/ws/v5/business (for candle channels)
	RESTURL        string          // https://www.okx.com
	Instruments    []string        // empty = fetch all SWAP instruments
	ReconnectDelay []time.Duration // e.g. [1s, 2s, 4s, 8s, 16s, 30s]
	PingInterval   time.Duration   // e.g. 25s
}

// defaultReconnectDelay is the exponential back-off schedule.
var defaultReconnectDelay = []time.Duration{
	1 * time.Second,
	2 * time.Second,
	4 * time.Second,
	8 * time.Second,
	16 * time.Second,
	30 * time.Second,
}

// OKXSwapProvider connects to OKX WebSocket and streams market data.
// It maintains two connections:
//   - public  (wss://.../ws/v5/public)   → trades, books5, funding-rate
//   - business (wss://.../ws/v5/business) → candle channels
//
// Both share the same reconnect, ping, and readLoop logic.
type OKXSwapProvider struct {
	name        string
	config      OKXSwapConfig
	instruments []string
	sink        DataSink
	health      atomic.Value // stores *ProviderHealth
	done        chan struct{}
	mu          sync.Mutex // protects sink
	httpClient  *http.Client
}

// NewOKXSwapProvider creates a new provider. Call Start to begin streaming.
func NewOKXSwapProvider(name string, config OKXSwapConfig) *OKXSwapProvider {
	if len(config.ReconnectDelay) == 0 {
		config.ReconnectDelay = defaultReconnectDelay
	}
	if config.PingInterval == 0 {
		config.PingInterval = 25 * time.Second
	}
	if config.WSURL == "" {
		config.WSURL = "wss://ws.okx.com:8443/ws/v5/public"
	}
	if config.BusinessWSURL == "" {
		config.BusinessWSURL = "wss://ws.okx.com:8443/ws/v5/business"
	}
	if config.RESTURL == "" {
		config.RESTURL = "https://www.okx.com"
	}

	p := &OKXSwapProvider{
		name:       name,
		config:     config,
		done:       make(chan struct{}),
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
	p.health.Store(&ProviderHealth{Status: "init"})
	return p
}

// Name implements DataProvider.
func (p *OKXSwapProvider) Name() string { return p.name }

// Health implements DataProvider.
func (p *OKXSwapProvider) Health() ProviderHealth {
	h := p.health.Load().(*ProviderHealth)
	return *h
}

// Subscribe sets the DataSink that receives all events.
func (p *OKXSwapProvider) Subscribe(sink DataSink) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sink = sink
	return nil
}

// publicChannels are subscribed on wss://.../ws/v5/public.
var publicChannels = []string{"books5", "trades", "funding-rate"}

// businessChannels are subscribed on wss://.../ws/v5/business.
var businessChannels = []string{"candle1m", "candle5m", "candle15m", "candle1H", "candle4H"}

// Start implements DataProvider. It fetches instruments, then starts two
// WebSocket goroutines (public + business) that share the same DataSink.
func (p *OKXSwapProvider) Start(ctx context.Context) error {
	if len(p.config.Instruments) > 0 {
		p.instruments = p.config.Instruments
	} else {
		insts, err := p.fetchInstruments(ctx)
		if err != nil {
			return fmt.Errorf("fetch instruments: %w", err)
		}
		p.instruments = insts
	}
	if len(p.instruments) == 0 {
		return fmt.Errorf("no instruments to subscribe")
	}

	go p.wsLoop(ctx, "public", p.config.WSURL, publicChannels)
	go p.wsLoop(ctx, "business", p.config.BusinessWSURL, businessChannels)
	return nil
}

// Stop implements DataProvider.
func (p *OKXSwapProvider) Stop(_ context.Context) error {
	select {
	case <-p.done:
	default:
		close(p.done)
	}
	p.health.Store(&ProviderHealth{Status: "stopped"})
	return nil
}

// wsLoop is a reconnecting loop for one WebSocket endpoint.
// tag is "public" or "business" for log clarity.
func (p *OKXSwapProvider) wsLoop(ctx context.Context, tag, wsURL string, channels []string) {
	attempt := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.done:
			return
		default:
		}

		conn, err := p.dialURL(ctx, wsURL)
		if err != nil {
			log.Printf("[%s/%s] connect error: %v", p.name, tag, err)
			p.backoff(ctx, attempt)
			attempt++
			continue
		}

		if err := p.subscribeOn(conn, channels); err != nil {
			log.Printf("[%s/%s] subscribe error: %v", p.name, tag, err)
			_ = conn.Close()
			p.backoff(ctx, attempt)
			attempt++
			continue
		}

		attempt = 0
		log.Printf("[%s/%s] connected", p.name, tag)
		p.health.Store(&ProviderHealth{Status: "connected", LastEvent: time.Now()})

		p.readLoop(ctx, conn)
		_ = conn.Close()

		log.Printf("[%s/%s] disconnected, will reconnect", p.name, tag)
	}
}

// dialURL dials a WebSocket URL with automatic proxy fallback.
func (p *OKXSwapProvider) dialURL(ctx context.Context, wsURL string) (*websocket.Conn, error) {
	proxyFn := netutil.ProxyFunc()
	testReq, _ := http.NewRequest("GET", wsURL, nil)
	proxyURL, _ := proxyFn(testReq)

	dial := func(proxy func(*http.Request) (*url.URL, error)) (*websocket.Conn, error) {
		dialer := websocket.Dialer{
			HandshakeTimeout: 10 * time.Second,
			Proxy:            proxy,
		}
		conn, _, err := dialer.DialContext(ctx, wsURL, nil)
		return conn, err
	}

	if proxyURL != nil {
		conn, err := dial(proxyFn)
		if err == nil {
			return conn, nil
		}
		log.Printf("[%s] proxy failed (%v), falling back to direct", p.name, err)
	}
	return dial(nil)
}

// subscribeOn sends subscription messages on a specific connection.
func (p *OKXSwapProvider) subscribeOn(conn *websocket.Conn, channels []string) error {
	for _, ch := range channels {
		for start := 0; start < len(p.instruments); start += 100 {
			end := start + 100
			if end > len(p.instruments) {
				end = len(p.instruments)
			}
			args := buildSubscribeArgs(ch, p.instruments[start:end])
			msg := map[string]any{
				"op":   "subscribe",
				"args": args,
			}
			data, err := json.Marshal(msg)
			if err != nil {
				return fmt.Errorf("marshal subscribe: %w", err)
			}
			if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
				return fmt.Errorf("write subscribe: %w", err)
			}
		}
	}
	return nil
}

// buildSubscribeArgs creates the args slice for one channel and a batch of instruments.
func buildSubscribeArgs(channel string, instruments []string) []map[string]string {
	args := make([]map[string]string, 0, len(instruments))
	for _, inst := range instruments {
		args = append(args, map[string]string{
			"channel": channel,
			"instId":  inst,
		})
	}
	return args
}

// readLoop reads messages from a WebSocket connection until error or cancel.
func (p *OKXSwapProvider) readLoop(ctx context.Context, conn *websocket.Conn) {
	pingTicker := time.NewTicker(p.config.PingInterval)
	defer pingTicker.Stop()

	// Ping goroutine for this connection.
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-p.done:
				return
			case <-pingTicker.C:
				_ = conn.WriteMessage(websocket.TextMessage, []byte("ping"))
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case <-p.done:
			return
		default:
		}

		_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		_, raw, err := conn.ReadMessage()
		if err != nil {
			h := p.health.Load().(*ProviderHealth)
			p.health.Store(&ProviderHealth{
				Status:     "error",
				Latency:    h.Latency,
				LastEvent:  h.LastEvent,
				ErrorCount: h.ErrorCount + 1,
			})
			return
		}

		if string(raw) == "pong" {
			continue
		}

		events := ParseWSMessage(raw, p.name)
		if len(events) == 0 {
			continue
		}

		p.mu.Lock()
		sink := p.sink
		p.mu.Unlock()
		if sink == nil {
			continue
		}

		for _, ev := range events {
			sink.OnEvent(ev)
		}

		p.health.Store(&ProviderHealth{
			Status:    "connected",
			LastEvent: time.Now(),
		})
	}
}

// backoff sleeps for the appropriate reconnect delay.
func (p *OKXSwapProvider) backoff(ctx context.Context, attempt int) {
	delays := p.config.ReconnectDelay
	idx := attempt
	if idx >= len(delays) {
		idx = len(delays) - 1
	}
	select {
	case <-time.After(delays[idx]):
	case <-ctx.Done():
	case <-p.done:
	}
}

// --- REST: fetch instruments ---

type okxInstrumentsResp struct {
	Code string          `json:"code"`
	Data []okxInstrument `json:"data"`
}

type okxInstrument struct {
	InstID string `json:"instId"`
}

// fetchInstruments calls the OKX REST API to get all SWAP instrument IDs.
func (p *OKXSwapProvider) fetchInstruments(ctx context.Context) ([]string, error) {
	url := p.config.RESTURL + "/api/v5/public/instruments?instType=SWAP"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return parseInstrumentsResponse(body)
}

// parseInstrumentsResponse extracts instId values from the REST JSON response.
func parseInstrumentsResponse(body []byte) ([]string, error) {
	var r okxInstrumentsResp
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("unmarshal instruments: %w", err)
	}
	if r.Code != "0" {
		return nil, fmt.Errorf("OKX instruments API error code: %s", r.Code)
	}
	ids := make([]string, 0, len(r.Data))
	for _, d := range r.Data {
		if d.InstID != "" {
			ids = append(ids, d.InstID)
		}
	}
	return ids, nil
}
