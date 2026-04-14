package llm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// cassette is a recorded HTTP interaction for replay in tests.
// Each cassette file contains one or more exchanges in order.
type cassette struct {
	Name       string              `json:"name"`
	Exchanges  []cassetteExchange  `json:"exchanges"`
}

type cassetteExchange struct {
	Request  cassetteRequest  `json:"request"`
	Response cassetteResponse `json:"response"`
}

type cassetteRequest struct {
	Method string            `json:"method"`
	URL    string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
	Body   json.RawMessage   `json:"body"`
}

type cassetteResponse struct {
	StatusCode int               `json:"status_code"`
	Headers    map[string]string `json:"headers,omitempty"`
	Body       json.RawMessage   `json:"body"`
}

// cassetteTransport is an http.RoundTripper that replays recorded cassettes.
// It matches exchanges in FIFO order.
type cassetteTransport struct {
	mu        sync.Mutex
	exchanges []cassetteExchange
	cursor    int
	record    bool          // if true, record mode — pass through to real transport
	real      http.RoundTripper
	recorded  []cassetteExchange
}

func newReplayTransport(c *cassette) *cassetteTransport {
	return &cassetteTransport{exchanges: c.Exchanges}
}

func newRecordTransport(real http.RoundTripper) *cassetteTransport {
	if real == nil {
		real = http.DefaultTransport
	}
	return &cassetteTransport{record: true, real: real}
}

func (t *cassetteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.record {
		return t.roundTripRecord(req)
	}
	return t.roundTripReplay(req)
}

func (t *cassetteTransport) roundTripReplay(req *http.Request) (*http.Response, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.cursor >= len(t.exchanges) {
		return nil, fmt.Errorf("cassette: no more exchanges (cursor=%d, total=%d)", t.cursor, len(t.exchanges))
	}

	ex := t.exchanges[t.cursor]
	t.cursor++

	resp := &http.Response{
		StatusCode: ex.Response.StatusCode,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(ex.Response.Body)),
	}
	for k, v := range ex.Response.Headers {
		resp.Header.Set(k, v)
	}
	if resp.Header.Get("Content-Type") == "" {
		resp.Header.Set("Content-Type", "application/json")
	}
	return resp, nil
}

func (t *cassetteTransport) roundTripRecord(req *http.Request) (*http.Response, error) {
	var reqBody []byte
	if req.Body != nil {
		var err error
		reqBody, err = io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		req.Body = io.NopCloser(bytes.NewReader(reqBody))
	}

	resp, err := t.real.RoundTrip(req)
	if err != nil {
		return nil, err
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	resp.Body = io.NopCloser(bytes.NewReader(respBody))

	headers := make(map[string]string)
	for k := range resp.Header {
		headers[k] = resp.Header.Get(k)
	}

	ex := cassetteExchange{
		Request: cassetteRequest{
			Method: req.Method,
			URL:    req.URL.String(),
			Body:   json.RawMessage(reqBody),
		},
		Response: cassetteResponse{
			StatusCode: resp.StatusCode,
			Headers:    headers,
			Body:       json.RawMessage(respBody),
		},
	}

	t.mu.Lock()
	t.recorded = append(t.recorded, ex)
	t.mu.Unlock()

	return resp, nil
}

func (t *cassetteTransport) saveCassette(name, path string) error {
	t.mu.Lock()
	c := cassette{Name: name, Exchanges: t.recorded}
	t.mu.Unlock()

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func loadCassette(path string) (*cassette, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c cassette
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

func cassettePath(name string) string {
	return filepath.Join("testdata", name+".cassette.json")
}

// cassetteClient creates an http.Client backed by a cassette for replay.
func cassetteClient(name string) (*http.Client, error) {
	c, err := loadCassette(cassettePath(name))
	if err != nil {
		return nil, err
	}
	return &http.Client{Transport: newReplayTransport(c)}, nil
}

// --- SSE cassette helpers ---

// buildSSEBody builds a raw SSE response body from a slice of event strings.
func buildSSEBody(events []string) []byte {
	var buf bytes.Buffer
	for _, e := range events {
		buf.WriteString(e)
		buf.WriteString("\n\n")
	}
	return buf.Bytes()
}

// sseEvent formats a single SSE event line pair.
func sseEvent(eventType, data string) string {
	return fmt.Sprintf("event: %s\ndata: %s", eventType, data)
}

// --- Inline cassette builder for tests that don't need files ---

type inlineCassette struct {
	exchanges []cassetteExchange
}

func newInlineCassette() *inlineCassette {
	return &inlineCassette{}
}

func (ic *inlineCassette) addComplete(statusCode int, body interface{}) *inlineCassette {
	raw, _ := json.Marshal(body)
	ic.exchanges = append(ic.exchanges, cassetteExchange{
		Request: cassetteRequest{Method: "POST", URL: "/v1/messages"},
		Response: cassetteResponse{
			StatusCode: statusCode,
			Headers:    map[string]string{"Content-Type": "application/json"},
			Body:       raw,
		},
	})
	return ic
}

func (ic *inlineCassette) addSSE(statusCode int, events []string) *inlineCassette {
	body := buildSSEBody(events)
	ic.exchanges = append(ic.exchanges, cassetteExchange{
		Request: cassetteRequest{Method: "POST", URL: "/v1/messages"},
		Response: cassetteResponse{
			StatusCode: statusCode,
			Headers: map[string]string{
				"Content-Type": "text/event-stream",
			},
			Body: json.RawMessage(body),
		},
	})
	return ic
}

func (ic *inlineCassette) transport() *cassetteTransport {
	return &cassetteTransport{exchanges: ic.exchanges}
}

func (ic *inlineCassette) client() *http.Client {
	return &http.Client{Transport: ic.transport()}
}

// sseStreamBody returns SSE bytes that only use "data: {json with type}" format
// (compatible with the Anthropic direct data-line path).
func sseStreamBody(events []struct{ Type, Data string }) []byte {
	var buf bytes.Buffer
	for _, e := range events {
		buf.WriteString("event: " + e.Type + "\n")
		buf.WriteString("data: " + e.Data + "\n\n")
	}
	return buf.Bytes()
}

// ignoreCloser wraps a reader into a ReadCloser that ignores Close.
// (already handled by io.NopCloser, this is for clarity in SSE tests
// where the body must remain readable across multiple Next calls.)
func mustMarshal(v interface{}) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func containsSubstring(s, substr string) bool {
	return strings.Contains(s, substr)
}
