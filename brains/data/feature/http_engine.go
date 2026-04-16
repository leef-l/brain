package feature

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// HTTPMLOption configures an HTTPMLEngine.
type HTTPMLOption func(*HTTPMLEngine)

// WithTimeout sets the HTTP request timeout for Predict calls.
func WithTimeout(d time.Duration) HTTPMLOption {
	return func(e *HTTPMLEngine) { e.timeout = d }
}

// WithRetries sets the number of retry attempts on transient failures.
func WithRetries(n int) HTTPMLOption {
	return func(e *HTTPMLEngine) { e.retries = n }
}

// HTTPMLEngine calls an external Python inference service via HTTP POST.
// It implements the MLEngine interface.
type HTTPMLEngine struct {
	baseURL string
	timeout time.Duration
	retries int
	client  *http.Client

	// health check
	ready  atomic.Bool
	stopCh chan struct{}
	wg     sync.WaitGroup

	// circuit breaker
	consecFails atomic.Int64
	openUntil   atomic.Int64 // unix-nano timestamp; 0 = closed
}

const (
	cbFailThreshold = 5
	cbOpenDuration  = 30 * time.Second
	healthInterval  = 30 * time.Second
	defaultTimeout  = 500 * time.Millisecond
)

// NewHTTPMLEngine creates a new HTTPMLEngine that calls baseURL for inference.
// The first health ping is performed synchronously during construction.
func NewHTTPMLEngine(baseURL string, opts ...HTTPMLOption) *HTTPMLEngine {
	e := &HTTPMLEngine{
		baseURL: baseURL,
		timeout: defaultTimeout,
		retries: 0,
		stopCh:  make(chan struct{}),
	}
	for _, o := range opts {
		o(e)
	}
	e.client = &http.Client{Timeout: e.timeout}

	// Initial health check (synchronous).
	e.pingHealth()

	// Background health check goroutine.
	e.wg.Add(1)
	go e.healthLoop()

	return e
}

// predictRequest is the JSON body sent to the inference service.
type predictRequest struct {
	Features []float64 `json:"features"`
}

// predictResponse is the JSON body returned by the inference service.
type predictResponse struct {
	MarketRegime [4]float64 `json:"market_regime"`
	VolPredict   [4]float64 `json:"vol_predict"`
	AnomalyScore [4]float64 `json:"anomaly_score"`
	Reserved     [4]float64 `json:"reserved"`
}

// Predict sends the rule features to the remote service and returns MLFeatures.
func (e *HTTPMLEngine) Predict(ctx context.Context, ruleFeatures []float64) (MLFeatures, error) {
	// Circuit breaker: if open, fail fast.
	if until := e.openUntil.Load(); until != 0 {
		if time.Now().UnixNano() < until {
			return MLFeatures{}, fmt.Errorf("http_ml: circuit breaker open")
		}
		// Half-open: allow one attempt, reset state below on success.
		e.openUntil.Store(0)
		e.consecFails.Store(0)
	}

	var lastErr error
	attempts := 1 + e.retries
	for i := 0; i < attempts; i++ {
		feat, err := e.doPredict(ctx, ruleFeatures)
		if err == nil {
			e.consecFails.Store(0)
			return feat, nil
		}
		lastErr = err
	}

	// Record failure for circuit breaker.
	if n := e.consecFails.Add(1); n >= cbFailThreshold {
		e.openUntil.Store(time.Now().Add(cbOpenDuration).UnixNano())
	}
	return MLFeatures{}, lastErr
}

func (e *HTTPMLEngine) doPredict(ctx context.Context, ruleFeatures []float64) (MLFeatures, error) {
	body, err := json.Marshal(predictRequest{Features: ruleFeatures})
	if err != nil {
		return MLFeatures{}, fmt.Errorf("http_ml: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+"/predict", bytes.NewReader(body))
	if err != nil {
		return MLFeatures{}, fmt.Errorf("http_ml: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.client.Do(req)
	if err != nil {
		return MLFeatures{}, fmt.Errorf("http_ml: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return MLFeatures{}, fmt.Errorf("http_ml: unexpected status %d", resp.StatusCode)
	}

	var pr predictResponse
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
		return MLFeatures{}, fmt.Errorf("http_ml: decode response: %w", err)
	}

	return MLFeatures{
		MarketRegime: pr.MarketRegime,
		VolPredict:   pr.VolPredict,
		AnomalyScore: pr.AnomalyScore,
		Reserved:     pr.Reserved,
	}, nil
}

// Ready reports whether the last health check succeeded.
func (e *HTTPMLEngine) Ready() bool {
	return e.ready.Load()
}

// Name returns the engine identifier.
func (e *HTTPMLEngine) Name() string {
	return "http_ml"
}

// Close stops the background health check goroutine and waits for it to exit.
func (e *HTTPMLEngine) Close() {
	close(e.stopCh)
	e.wg.Wait()
}

func (e *HTTPMLEngine) healthLoop() {
	defer e.wg.Done()
	ticker := time.NewTicker(healthInterval)
	defer ticker.Stop()
	for {
		select {
		case <-e.stopCh:
			return
		case <-ticker.C:
			e.pingHealth()
		}
	}
}

func (e *HTTPMLEngine) pingHealth() {
	ctx, cancel := context.WithTimeout(context.Background(), e.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.baseURL+"/health", nil)
	if err != nil {
		e.ready.Store(false)
		return
	}

	resp, err := e.client.Do(req)
	if err != nil {
		e.ready.Store(false)
		return
	}
	resp.Body.Close()
	e.ready.Store(resp.StatusCode == http.StatusOK)
}

// Compile-time interface check.
var _ MLEngine = (*HTTPMLEngine)(nil)
