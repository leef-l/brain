package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// OKXSwapConfig holds the configuration for OKXSwapProvider.
type OKXSwapConfig struct {
	WSURL          string          // wss://ws.okx.com:8443/ws/v5/public
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

// OKXSwapProvider connects to OKX public WebSocket and streams market data.
type OKXSwapProvider struct {
	name        string
	config      OKXSwapConfig
	instruments []string
	conn        *websocket.Conn
	sink        DataSink
	health      atomic.Value // stores *ProviderHealth
	done        chan struct{}
	mu          sync.Mutex
	httpClient  *http.Client // injectable for testing
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

// Start implements DataProvider. It fetches instruments, connects, subscribes,
// and enters the read loop. It blocks until ctx is cancelled or Stop is called.
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

	go p.runLoop(ctx)
	return nil
}

// Stop implements DataProvider.
func (p *OKXSwapProvider) Stop(_ context.Context) error {
	select {
	case <-p.done:
		// already stopped
	default:
		close(p.done)
	}
	p.mu.Lock()
	if p.conn != nil {
		_ = p.conn.Close()
		p.conn = nil
	}
	p.mu.Unlock()
	p.health.Store(&ProviderHealth{Status: "stopped"})
	return nil
}

// runLoop is the main goroutine: connect → subscribe → readLoop → reconnect.
func (p *OKXSwapProvider) runLoop(ctx context.Context) {
	attempt := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.done:
			return
		default:
		}

		if err := p.connect(ctx); err != nil {
			log.Printf("[%s] connect error: %v", p.name, err)
			p.backoff(ctx, attempt)
			attempt++
			continue
		}
		if err := p.subscribe(); err != nil {
			log.Printf("[%s] subscribe error: %v", p.name, err)
			p.closeConn()
			p.backoff(ctx, attempt)
			attempt++
			continue
		}

		attempt = 0
		p.health.Store(&ProviderHealth{Status: "connected", LastEvent: time.Now()})

		// readLoop blocks until connection drops.
		p.readLoop(ctx)
		p.closeConn()

		log.Printf("[%s] disconnected, will reconnect", p.name)
	}
}

// connect dials the OKX WebSocket.
func (p *OKXSwapProvider) connect(ctx context.Context) error {
	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}
	conn, _, err := dialer.DialContext(ctx, p.config.WSURL, nil)
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.conn = conn
	p.mu.Unlock()
	return nil
}

// closeConn safely closes the current connection.
func (p *OKXSwapProvider) closeConn() {
	p.mu.Lock()
	if p.conn != nil {
		_ = p.conn.Close()
		p.conn = nil
	}
	p.mu.Unlock()
}

// subscribe sends subscription messages for all channels and instruments.
// OKX requires batches of args; we send up to 100 instruments per message.
func (p *OKXSwapProvider) subscribe() error {
	channels := []string{
		"candle1m", "candle5m", "candle15m", "candle1H", "candle4H",
		"books5", "trades", "funding-rate",
	}
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
			p.mu.Lock()
			err = p.conn.WriteMessage(websocket.TextMessage, data)
			p.mu.Unlock()
			if err != nil {
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

// readLoop reads messages from the WebSocket until an error or context cancel.
func (p *OKXSwapProvider) readLoop(ctx context.Context) {
	pingTicker := time.NewTicker(p.config.PingInterval)
	defer pingTicker.Stop()

	// Start ping goroutine.
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-p.done:
				return
			case <-pingTicker.C:
				p.mu.Lock()
				if p.conn != nil {
					_ = p.conn.WriteMessage(websocket.TextMessage, []byte("ping"))
				}
				p.mu.Unlock()
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

		p.mu.Lock()
		conn := p.conn
		p.mu.Unlock()
		if conn == nil {
			return
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

		// OKX sends "pong" as plain text.
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
