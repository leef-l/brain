package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	brainerrors "github.com/leef-l/brain/sdk/errors"
	"github.com/leef-l/brain/sdk/tool/cdp"
)

// Network observation layer for Browser Brain.
//
// See sdk/docs/39-Browser-Brain感知与嗅探增强设计.md §3.2 / §3.3.
//
// NetBuf is a bounded ring of observed HTTP(S) requests. A single buffer is
// shared across all browser tools via browserSessionHolder. CDP Network.*
// events drive the state machine:
//
//   Network.requestWillBeSent  → new record, inflight++
//   Network.responseReceived   → fill status/headers/mime
//   Network.loadingFinished    → inflight--, mark complete
//   Network.loadingFailed      → inflight--, mark failed
//
// The companion browser.network tool exposes list/get/wait_for/clear
// operations so an LLM can consume this state directly instead of reading
// page screenshots.

// netEntry is a single observed network request + response.
type netEntry struct {
	ID           string            `json:"id"`
	URL          string            `json:"url"`
	Method       string            `json:"method"`
	Status       int               `json:"status,omitempty"`
	StatusText   string            `json:"statusText,omitempty"`
	MimeType     string            `json:"mimeType,omitempty"`
	ResourceType string            `json:"resourceType,omitempty"`
	StartedAt    int64             `json:"startedAt"` // unix millis
	FinishedAt   int64             `json:"finishedAt,omitempty"`
	Duration     int64             `json:"durationMs,omitempty"`
	RequestHdrs  map[string]string `json:"requestHeaders,omitempty"`
	ResponseHdrs map[string]string `json:"responseHeaders,omitempty"`
	Failed       bool              `json:"failed,omitempty"`
	ErrorText    string            `json:"errorText,omitempty"`
	PostData     string            `json:"postData,omitempty"`
	Encoded      int64             `json:"encodedBytes,omitempty"`
	InFlight     bool              `json:"inFlight,omitempty"`
}

// netBuf is the session-wide ring buffer + inflight counter.
type netBuf struct {
	mu       sync.RWMutex
	capacity int
	entries  []*netEntry        // ring, capacity fixed
	index    int                // next write slot
	byID     map[string]*netEntry
	inflight int
	cond     *sync.Cond
}

func newNetBuf(capacity int) *netBuf {
	if capacity <= 0 {
		capacity = 200
	}
	b := &netBuf{
		capacity: capacity,
		entries:  make([]*netEntry, 0, capacity),
		byID:     make(map[string]*netEntry),
	}
	b.cond = sync.NewCond(&b.mu)
	return b
}

// attach subscribes CDP event handlers that populate the buffer.
func (b *netBuf) attach(client *cdp.Client) {
	client.On("Network.requestWillBeSent", func(raw json.RawMessage) {
		var p struct {
			RequestID string `json:"requestId"`
			Request   struct {
				URL      string            `json:"url"`
				Method   string            `json:"method"`
				Headers  map[string]string `json:"headers"`
				PostData string            `json:"postData"`
			} `json:"request"`
			Type        string  `json:"type"`
			Timestamp   float64 `json:"timestamp"`
			WallTime    float64 `json:"wallTime"`
		}
		if json.Unmarshal(raw, &p) != nil {
			return
		}
		e := &netEntry{
			ID:           p.RequestID,
			URL:          p.Request.URL,
			Method:       p.Request.Method,
			ResourceType: p.Type,
			StartedAt:    time.Now().UnixMilli(),
			RequestHdrs:  p.Request.Headers,
			PostData:     truncateNetField(p.Request.PostData, 4000),
			InFlight:     true,
		}
		b.add(e)
	})

	client.On("Network.responseReceived", func(raw json.RawMessage) {
		var p struct {
			RequestID string `json:"requestId"`
			Response  struct {
				Status     int               `json:"status"`
				StatusText string            `json:"statusText"`
				MimeType   string            `json:"mimeType"`
				Headers    map[string]string `json:"headers"`
				URL        string            `json:"url"`
			} `json:"response"`
			Type string `json:"type"`
		}
		if json.Unmarshal(raw, &p) != nil {
			return
		}
		b.update(p.RequestID, func(e *netEntry) {
			e.Status = p.Response.Status
			e.StatusText = p.Response.StatusText
			e.MimeType = p.Response.MimeType
			e.ResponseHdrs = p.Response.Headers
			if e.URL == "" {
				e.URL = p.Response.URL
			}
			if p.Type != "" {
				e.ResourceType = p.Type
			}
		})
	})

	client.On("Network.loadingFinished", func(raw json.RawMessage) {
		var p struct {
			RequestID        string  `json:"requestId"`
			Timestamp        float64 `json:"timestamp"`
			EncodedDataLen   int64   `json:"encodedDataLength"`
		}
		if json.Unmarshal(raw, &p) != nil {
			return
		}
		b.complete(p.RequestID, p.EncodedDataLen, false, "")
	})

	client.On("Network.loadingFailed", func(raw json.RawMessage) {
		var p struct {
			RequestID string `json:"requestId"`
			ErrorText string `json:"errorText"`
			Canceled  bool   `json:"canceled"`
		}
		if json.Unmarshal(raw, &p) != nil {
			return
		}
		msg := p.ErrorText
		if p.Canceled {
			msg = "canceled"
		}
		b.complete(p.RequestID, 0, true, msg)
	})
}

func (b *netBuf) add(e *netEntry) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, exists := b.byID[e.ID]; exists {
		return // dup event; ignore
	}
	if len(b.entries) < b.capacity {
		b.entries = append(b.entries, e)
	} else {
		old := b.entries[b.index]
		delete(b.byID, old.ID)
		b.entries[b.index] = e
		b.index = (b.index + 1) % b.capacity
	}
	b.byID[e.ID] = e
	b.inflight++
	b.cond.Broadcast()
}

func (b *netBuf) update(id string, fn func(*netEntry)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if e, ok := b.byID[id]; ok {
		fn(e)
		b.cond.Broadcast()
	}
}

func (b *netBuf) complete(id string, encoded int64, failed bool, errMsg string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	e, ok := b.byID[id]
	if !ok {
		return
	}
	if e.InFlight {
		b.inflight--
		if b.inflight < 0 {
			b.inflight = 0
		}
	}
	e.InFlight = false
	e.Failed = failed
	e.ErrorText = errMsg
	e.FinishedAt = time.Now().UnixMilli()
	e.Encoded = encoded
	if e.StartedAt > 0 {
		e.Duration = e.FinishedAt - e.StartedAt
	}
	b.cond.Broadcast()
}

// snapshot returns a copy of current entries (newest first).
func (b *netBuf) snapshot() []*netEntry {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]*netEntry, 0, len(b.entries))
	for i := len(b.entries) - 1; i >= 0; i-- {
		e := *b.entries[i]
		out = append(out, &e)
	}
	return out
}

func (b *netBuf) clear() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.entries = b.entries[:0]
	b.index = 0
	b.byID = make(map[string]*netEntry)
	b.inflight = 0
}

func (b *netBuf) inflightCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.inflight
}

// waitForMatch blocks until an entry matches pred, returns nil on timeout.
// pred is called with the read lock held; it must not mutate.
func (b *netBuf) waitForMatch(ctx context.Context, pred func(*netEntry) bool) *netEntry {
	deadline, hasDeadline := ctx.Deadline()
	b.mu.Lock()
	defer b.mu.Unlock()
	for {
		for _, e := range b.entries {
			if pred(e) {
				copy := *e
				return &copy
			}
		}
		if hasDeadline && time.Now().After(deadline) {
			return nil
		}
		// Use a helper goroutine + cond wait with context cancellation
		done := make(chan struct{})
		go func() {
			select {
			case <-ctx.Done():
				b.mu.Lock()
				b.cond.Broadcast()
				b.mu.Unlock()
			case <-done:
			}
		}()
		b.cond.Wait()
		close(done)
		if ctx.Err() != nil {
			return nil
		}
	}
}

func truncateNetField(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...[truncated]"
}

// ---------------------------------------------------------------------------
// browser.network tool
// ---------------------------------------------------------------------------

type browserNetworkTool struct{ holder *browserSessionHolder }

func (t *browserNetworkTool) Name() string { return "browser.network" }
func (t *browserNetworkTool) Risk() Risk   { return RiskSafe }

func (t *browserNetworkTool) Schema() Schema {
	return Schema{
		Name: t.Name(),
		Description: `Query observed HTTP(S) requests on the current browser session.

actions:
  - list: return recent requests matching filters (default)
  - get: fetch one entry by id including response body (when available)
  - wait_for: block until a matching response arrives or timeout
  - clear: drop all buffered entries

Use this instead of screenshotting error messages: an LLM can read API
status codes and JSON responses directly to decide what happened.`,
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "action":      { "type": "string", "enum": ["list","get","wait_for","clear"], "description": "default: list" },
    "url_pattern": { "type": "string", "description": "Regex to match request URL" },
    "status":      { "type": "integer", "description": "Exact HTTP status to match" },
    "method":      { "type": "string", "description": "HTTP method (GET/POST/…)" },
    "resource_type": { "type": "string", "description": "fetch / xhr / document / stylesheet / image / …" },
    "since_ts":    { "type": "integer", "description": "Unix millis; entries started after this" },
    "id":          { "type": "string", "description": "Request ID (for action=get)" },
    "timeout_ms":  { "type": "integer", "description": "For action=wait_for (default 10000)" },
    "limit":       { "type": "integer", "description": "Max entries to return (default 50)" },
    "with_body":   { "type": "boolean", "description": "Include response body when available (action=get/wait_for)" }
  }
}`),
		OutputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "action":   { "type": "string" },
    "count":    { "type": "integer" },
    "inflight": { "type": "integer" },
    "entries":  { "type": "array" },
    "entry":    { "type": "object" }
  }
}`),
		Brain: "browser",
		Concurrency: &ToolConcurrencySpec{
			Capability:          "web.read",
			ResourceKeyTemplate: "browser:session",
			AccessMode:          "shared-session",
			Scope:               "turn",
			ApprovalClass:       "safe",
		},
	}
}

type networkInput struct {
	Action       string `json:"action"`
	URLPattern   string `json:"url_pattern"`
	Status       int    `json:"status"`
	Method       string `json:"method"`
	ResourceType string `json:"resource_type"`
	SinceTS      int64  `json:"since_ts"`
	ID           string `json:"id"`
	TimeoutMS    int    `json:"timeout_ms"`
	Limit        int    `json:"limit"`
	WithBody     bool   `json:"with_body"`
}

func (t *browserNetworkTool) Execute(ctx context.Context, args json.RawMessage) (*Result, error) {
	var input networkInput
	if len(args) > 0 && string(args) != "null" {
		if err := json.Unmarshal(args, &input); err != nil {
			return errResult("invalid arguments: %v", err), nil
		}
	}
	if input.Action == "" {
		input.Action = "list"
	}
	if input.Limit <= 0 {
		input.Limit = 50
	}

	// NetBuf is always attached; session may still be nil if not opened.
	sess, err := t.holder.get(ctx)
	if err != nil && input.Action != "list" && input.Action != "clear" {
		return errResult("no browser session: %v", err), nil
	}
	_ = sess // used in get/wait_for for response body

	buf := t.holder.netbuf

	switch input.Action {
	case "clear":
		buf.clear()
		return okResult(map[string]interface{}{"action": "clear", "count": 0, "inflight": 0}), nil

	case "list":
		filter, ferr := buildFilter(&input)
		if ferr != nil {
			return errResult("%v", ferr), nil
		}
		all := buf.snapshot()
		out := make([]*netEntry, 0, input.Limit)
		for _, e := range all {
			if filter(e) {
				out = append(out, e)
				if len(out) >= input.Limit {
					break
				}
			}
		}
		return okResult(map[string]interface{}{
			"action":   "list",
			"count":    len(out),
			"inflight": buf.inflightCount(),
			"entries":  out,
		}), nil

	case "get":
		if input.ID == "" {
			return errResult("id required for action=get"), nil
		}
		var entry *netEntry
		for _, e := range buf.snapshot() {
			if e.ID == input.ID {
				entry = e
				break
			}
		}
		if entry == nil {
			return errResult("request id %q not found", input.ID), nil
		}
		payload := map[string]interface{}{"action": "get", "entry": entry}
		if input.WithBody && sess != nil && !entry.InFlight {
			if body, mime, berr := getResponseBody(ctx, sess, input.ID); berr == nil {
				payload["body"] = body
				payload["body_mime"] = mime
			}
		}
		return okResult(payload), nil

	case "wait_for":
		timeout := time.Duration(input.TimeoutMS) * time.Millisecond
		if timeout <= 0 {
			timeout = 10 * time.Second
		}
		if timeout > 60*time.Second {
			timeout = 60 * time.Second
		}
		waitCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		filter, ferr := buildFilter(&input)
		if ferr != nil {
			return errResult("%v", ferr), nil
		}
		entry := buf.waitForMatch(waitCtx, func(e *netEntry) bool {
			// Only react to entries that have received a response.
			if e.Status == 0 && !e.Failed {
				return false
			}
			return filter(e)
		})
		if entry == nil {
			return ErrorResult(brainerrors.CodeToolTimeout, "wait_for timeout after %v (inflight=%d)", timeout, buf.inflightCount()), nil
		}
		payload := map[string]interface{}{"action": "wait_for", "entry": entry}
		if input.WithBody && sess != nil && !entry.InFlight {
			if body, mime, berr := getResponseBody(ctx, sess, entry.ID); berr == nil {
				payload["body"] = body
				payload["body_mime"] = mime
			}
		}
		return okResult(payload), nil

	default:
		return errResult("unknown action %q", input.Action), nil
	}
}

func buildFilter(in *networkInput) (func(*netEntry) bool, error) {
	var urlRe *regexp.Regexp
	if in.URLPattern != "" {
		re, err := regexp.Compile(in.URLPattern)
		if err != nil {
			return nil, fmt.Errorf("invalid url_pattern regex: %w", err)
		}
		urlRe = re
	}
	method := strings.ToUpper(in.Method)
	resType := in.ResourceType
	sinceTS := in.SinceTS
	status := in.Status
	return func(e *netEntry) bool {
		if urlRe != nil && !urlRe.MatchString(e.URL) {
			return false
		}
		if status != 0 && e.Status != status {
			return false
		}
		if method != "" && strings.ToUpper(e.Method) != method {
			return false
		}
		if resType != "" && !strings.EqualFold(e.ResourceType, resType) {
			return false
		}
		if sinceTS > 0 && e.StartedAt < sinceTS {
			return false
		}
		return true
	}, nil
}

func getResponseBody(ctx context.Context, sess *cdp.BrowserSession, requestID string) (string, string, error) {
	var out struct {
		Body          string `json:"body"`
		Base64Encoded bool   `json:"base64Encoded"`
	}
	if err := sess.Exec(ctx, "Network.getResponseBody", map[string]interface{}{
		"requestId": requestID,
	}, &out); err != nil {
		return "", "", err
	}
	if out.Base64Encoded {
		return out.Body, "application/octet-stream;base64", nil
	}
	return out.Body, "text/plain", nil
}

// ---------------------------------------------------------------------------
// wait.network_idle — New independent tool (does not alter browser.wait)
// ---------------------------------------------------------------------------

type browserWaitNetworkIdleTool struct{ holder *browserSessionHolder }

func (t *browserWaitNetworkIdleTool) Name() string { return "wait.network_idle" }
func (t *browserWaitNetworkIdleTool) Risk() Risk   { return RiskSafe }

func (t *browserWaitNetworkIdleTool) Schema() Schema {
	return Schema{
		Name: t.Name(),
		Description: `Wait until network has been idle — no in-flight requests for the given
quiet period (default 500ms). Unlike the old browser.wait condition=idle, this
observes actual CDP Network events (not a fixed sleep).

Use after navigations, form submits, or SPA route changes to know the UI
has settled before reading a snapshot.`,
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "quiet_ms":   { "type": "integer", "description": "Idle duration threshold (default 500)" },
    "timeout_ms": { "type": "integer", "description": "Hard timeout (default 10000, max 60000)" }
  }
}`),
		OutputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "status":   { "type": "string" },
    "idle_ms":  { "type": "integer" },
    "forced":   { "type": "boolean" }
  }
}`),
		Brain: "browser",
		Concurrency: &ToolConcurrencySpec{
			Capability:          "web.read",
			ResourceKeyTemplate: "browser:session",
			AccessMode:          "shared-session",
			Scope:               "turn",
			ApprovalClass:       "safe",
		},
	}
}

func (t *browserWaitNetworkIdleTool) Execute(ctx context.Context, args json.RawMessage) (*Result, error) {
	var input struct {
		QuietMS   int `json:"quiet_ms"`
		TimeoutMS int `json:"timeout_ms"`
	}
	if len(args) > 0 && string(args) != "null" {
		if err := json.Unmarshal(args, &input); err != nil {
			return errResult("invalid arguments: %v", err), nil
		}
	}
	if input.QuietMS <= 0 {
		input.QuietMS = 500
	}
	if input.TimeoutMS <= 0 {
		input.TimeoutMS = 10_000
	}
	if input.TimeoutMS > 60_000 {
		input.TimeoutMS = 60_000
	}

	if _, err := t.holder.get(ctx); err != nil {
		return errResult("no browser session: %v", err), nil
	}
	buf := t.holder.netbuf

	waitCtx, cancel := context.WithTimeout(ctx, time.Duration(input.TimeoutMS)*time.Millisecond)
	defer cancel()

	err := waitNetworkIdle(waitCtx, buf, time.Duration(input.QuietMS)*time.Millisecond)
	if err != nil {
		return okResult(map[string]interface{}{
			"status": "timeout",
			"forced": true,
		}), nil
	}
	return okResult(map[string]interface{}{
		"status": "idle",
		"forced": false,
	}), nil
}

// waitNetworkIdle blocks until the buffer reports 0 in-flight for the given
// quiet duration, or the context is canceled. Returns nil on idle, error on timeout/cancel.
func waitNetworkIdle(ctx context.Context, buf *netBuf, quiet time.Duration) error {
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	var idleSince time.Time
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
		if buf.inflightCount() == 0 {
			if idleSince.IsZero() {
				idleSince = time.Now()
			} else if time.Since(idleSince) >= quiet {
				return nil
			}
		} else {
			idleSince = time.Time{}
		}
	}
}
