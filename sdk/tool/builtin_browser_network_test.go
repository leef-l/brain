package tool

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	brainerrors "github.com/leef-l/brain/sdk/errors"
	"github.com/leef-l/brain/sdk/tool/cdp"
)

func TestNetBufLifecycleAndRingBuffer(t *testing.T) {
	buf := newNetBuf(2)

	req1 := &netEntry{
		ID:           "req-1",
		URL:          "https://api.example.com/one",
		Method:       "GET",
		ResourceType: "xhr",
		StartedAt:    time.Now().Add(-50 * time.Millisecond).UnixMilli(),
		InFlight:     true,
	}
	buf.add(req1)
	if got := buf.inflightCount(); got != 1 {
		t.Fatalf("inflight after add=%d, want 1", got)
	}

	buf.update("req-1", func(e *netEntry) {
		e.Status = 200
		e.StatusText = "OK"
		e.MimeType = "application/json"
		e.ResponseHdrs = map[string]string{"content-type": "application/json"}
	})
	buf.complete("req-1", 128, false, "")

	snap := buf.snapshot()
	if len(snap) != 1 {
		t.Fatalf("snapshot len=%d, want 1", len(snap))
	}
	if snap[0].Status != 200 || snap[0].StatusText != "OK" {
		t.Fatalf("entry after response=%+v, want 200 OK", snap[0])
	}
	if snap[0].InFlight || snap[0].Failed {
		t.Fatalf("entry after finish=%+v, want completed non-failed", snap[0])
	}
	if snap[0].Encoded != 128 {
		t.Fatalf("encoded=%d, want 128", snap[0].Encoded)
	}
	if snap[0].FinishedAt == 0 || snap[0].Duration < 0 {
		t.Fatalf("timing fields=%+v, want finished/duration set", snap[0])
	}
	if got := buf.inflightCount(); got != 0 {
		t.Fatalf("inflight after complete=%d, want 0", got)
	}

	req2 := &netEntry{
		ID:           "req-2",
		URL:          "https://api.example.com/two",
		Method:       "POST",
		ResourceType: "fetch",
		StartedAt:    time.Now().Add(-25 * time.Millisecond).UnixMilli(),
		InFlight:     true,
	}
	buf.add(req2)
	buf.complete("req-2", 0, true, "net::ERR_ABORTED")

	snap = buf.snapshot()
	if len(snap) != 2 {
		t.Fatalf("snapshot len after second request=%d, want 2", len(snap))
	}
	if snap[0].ID != "req-2" || !snap[0].Failed || snap[0].ErrorText != "net::ERR_ABORTED" {
		t.Fatalf("failed entry=%+v, want req-2 failed with error text", snap[0])
	}
	if got := buf.inflightCount(); got != 0 {
		t.Fatalf("inflight after failed completion=%d, want 0", got)
	}

	req3 := &netEntry{
		ID:           "req-3",
		URL:          "https://api.example.com/three",
		Method:       "GET",
		ResourceType: "document",
		StartedAt:    time.Now().UnixMilli(),
		InFlight:     true,
	}
	buf.add(req3)

	snap = buf.snapshot()
	if len(snap) != 2 {
		t.Fatalf("snapshot len after ring wrap=%d, want 2", len(snap))
	}
	if snap[0].ID != "req-3" || snap[1].ID != "req-2" {
		t.Fatalf("snapshot order after ring wrap=%v, want [req-3 req-2]", []string{snap[0].ID, snap[1].ID})
	}
	if _, ok := buf.byID["req-1"]; ok {
		t.Fatalf("oldest request req-1 should be evicted from byID")
	}
	if got := buf.inflightCount(); got != 1 {
		t.Fatalf("inflight after third add=%d, want 1", got)
	}
}

func TestNetBufWaitForMatch(t *testing.T) {
	buf := newNetBuf(4)

	go func() {
		time.Sleep(30 * time.Millisecond)
		buf.add(&netEntry{
			ID:           "req-wait",
			URL:          "https://api.example.com/orders",
			Method:       "POST",
			ResourceType: "fetch",
			StartedAt:    time.Now().UnixMilli(),
			InFlight:     true,
		})
		buf.update("req-wait", func(e *netEntry) {
			e.Status = 202
		})
		buf.complete("req-wait", 64, false, "")
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	got := buf.waitForMatch(ctx, func(e *netEntry) bool {
		return e.URL == "https://api.example.com/orders" && e.Status == 202
	})
	if got == nil {
		t.Fatal("waitForMatch returned nil, want matching entry")
	}
	if got.ID != "req-wait" || got.InFlight {
		t.Fatalf("waitForMatch entry=%+v, want completed req-wait", got)
	}
}

func TestBrowserNetworkToolActions(t *testing.T) {
	holder := newBrowserSessionHolder()
	holder.session = &cdp.BrowserSession{}
	tool := &browserNetworkTool{holder: holder}

	holder.netbuf.add(&netEntry{
		ID:           "req-old",
		URL:          "https://api.example.com/orders/old",
		Method:       "GET",
		Status:       200,
		ResourceType: "xhr",
		StartedAt:    time.Now().Add(-2 * time.Minute).UnixMilli(),
	})
	holder.netbuf.add(&netEntry{
		ID:           "req-new",
		URL:          "https://api.example.com/orders/42",
		Method:       "POST",
		Status:       201,
		ResourceType: "fetch",
		StartedAt:    time.Now().UnixMilli(),
	})

	listArgs := mustJSON(t, map[string]interface{}{
		"action":        "list",
		"url_pattern":   `orders`,
		"method":        "post",
		"status":        201,
		"resource_type": "FETCH",
		"limit":         5,
	})
	res, err := tool.Execute(context.Background(), listArgs)
	if err != nil {
		t.Fatalf("list Execute error: %v", err)
	}
	if res.IsError {
		t.Fatalf("list returned error result: %s", string(res.Output))
	}
	var listOut struct {
		Action   string     `json:"action"`
		Count    int        `json:"count"`
		Inflight int        `json:"inflight"`
		Entries  []netEntry `json:"entries"`
	}
	mustUnmarshal(t, res.Output, &listOut)
	if listOut.Action != "list" || listOut.Count != 1 {
		t.Fatalf("list output=%+v, want action=list count=1", listOut)
	}
	if len(listOut.Entries) != 1 || listOut.Entries[0].ID != "req-new" {
		t.Fatalf("list entries=%+v, want req-new only", listOut.Entries)
	}

	getArgs := mustJSON(t, map[string]interface{}{
		"action": "get",
		"id":     "req-new",
	})
	res, err = tool.Execute(context.Background(), getArgs)
	if err != nil {
		t.Fatalf("get Execute error: %v", err)
	}
	if res.IsError {
		t.Fatalf("get returned error result: %s", string(res.Output))
	}
	var getOut struct {
		Action string   `json:"action"`
		Entry  netEntry `json:"entry"`
	}
	mustUnmarshal(t, res.Output, &getOut)
	if getOut.Action != "get" || getOut.Entry.ID != "req-new" {
		t.Fatalf("get output=%+v, want req-new", getOut)
	}

	waitDone := make(chan struct{})
	go func() {
		defer close(waitDone)
		time.Sleep(30 * time.Millisecond)
		holder.netbuf.add(&netEntry{
			ID:           "req-wait",
			URL:          "https://api.example.com/payments/1",
			Method:       "POST",
			ResourceType: "fetch",
			StartedAt:    time.Now().UnixMilli(),
			InFlight:     true,
		})
		holder.netbuf.update("req-wait", func(e *netEntry) {
			e.Status = 202
		})
		holder.netbuf.complete("req-wait", 32, false, "")
	}()

	waitArgs := mustJSON(t, map[string]interface{}{
		"action":      "wait_for",
		"url_pattern": `payments`,
		"method":      "POST",
		"status":      202,
		"timeout_ms":  500,
	})
	res, err = tool.Execute(context.Background(), waitArgs)
	if err != nil {
		t.Fatalf("wait_for Execute error: %v", err)
	}
	<-waitDone
	if res.IsError {
		t.Fatalf("wait_for returned error result: %s", string(res.Output))
	}
	var waitOut struct {
		Action string   `json:"action"`
		Entry  netEntry `json:"entry"`
	}
	mustUnmarshal(t, res.Output, &waitOut)
	if waitOut.Action != "wait_for" || waitOut.Entry.ID != "req-wait" {
		t.Fatalf("wait_for output=%+v, want req-wait", waitOut)
	}

	holder.netbuf.add(&netEntry{
		ID:        "req-inflight",
		URL:       "https://api.example.com/inflight",
		Method:    "GET",
		StartedAt: time.Now().UnixMilli(),
		InFlight:  true,
	})
	clearArgs := mustJSON(t, map[string]interface{}{"action": "clear"})
	res, err = tool.Execute(context.Background(), clearArgs)
	if err != nil {
		t.Fatalf("clear Execute error: %v", err)
	}
	if res.IsError {
		t.Fatalf("clear returned error result: %s", string(res.Output))
	}
	var clearOut struct {
		Action   string `json:"action"`
		Count    int    `json:"count"`
		Inflight int    `json:"inflight"`
	}
	mustUnmarshal(t, res.Output, &clearOut)
	if clearOut.Action != "clear" || clearOut.Count != 0 || clearOut.Inflight != 0 {
		t.Fatalf("clear output=%+v, want zeroed clear result", clearOut)
	}
	if got := len(holder.netbuf.snapshot()); got != 0 {
		t.Fatalf("buffer not cleared, snapshot len=%d", got)
	}
}

func TestBrowserNetworkToolWaitForTimeout(t *testing.T) {
	holder := newBrowserSessionHolder()
	holder.session = &cdp.BrowserSession{}
	tool := &browserNetworkTool{holder: holder}

	args := mustJSON(t, map[string]interface{}{
		"action":      "wait_for",
		"url_pattern": `never-matches`,
		"timeout_ms":  20,
	})
	res, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("wait_for timeout Execute error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("wait_for timeout should be error result, got %s", string(res.Output))
	}
	var payload struct {
		Code string `json:"error_code"`
	}
	mustUnmarshal(t, res.Output, &payload)
	if payload.Code != brainerrors.CodeToolTimeout {
		t.Fatalf("error_code=%q, want %q", payload.Code, brainerrors.CodeToolTimeout)
	}
}

func mustJSON(t *testing.T, v interface{}) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return raw
}

func mustUnmarshal(t *testing.T, raw []byte, v interface{}) {
	t.Helper()
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("json.Unmarshal(%s): %v", string(raw), err)
	}
}
