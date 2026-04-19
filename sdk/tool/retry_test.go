package tool

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	brainerrors "github.com/leef-l/brain/sdk/errors"
)

type scriptedTool struct {
	name     string
	calls    int32
	results  []*Result
	errs     []error
}

func (s *scriptedTool) Name() string   { return s.name }
func (s *scriptedTool) Risk() Risk     { return RiskLow }
func (s *scriptedTool) Schema() Schema { return Schema{Name: s.name} }

func (s *scriptedTool) Execute(_ context.Context, _ json.RawMessage) (*Result, error) {
	idx := atomic.AddInt32(&s.calls, 1) - 1
	if int(idx) < len(s.errs) && s.errs[idx] != nil {
		return nil, s.errs[idx]
	}
	if int(idx) >= len(s.results) {
		return s.results[len(s.results)-1], nil
	}
	return s.results[idx], nil
}

func TestErrorResultExposesCodeAndClass(t *testing.T) {
	res := ErrorResult(brainerrors.CodeToolTimeout, "took too long: %v", 30)
	if !res.IsError {
		t.Fatal("IsError must be true")
	}
	var out map[string]interface{}
	if err := json.Unmarshal(res.Output, &out); err != nil {
		t.Fatalf("output not JSON: %v", err)
	}
	if out["error_code"] != brainerrors.CodeToolTimeout {
		t.Errorf("error_code = %v", out["error_code"])
	}
	if out["error_class"] != "transient" {
		t.Errorf("error_class = %v, want transient", out["error_class"])
	}
	if out["retryable"] != true {
		t.Errorf("retryable = %v", out["retryable"])
	}
}

func TestRetryWrapperRetriesTransient(t *testing.T) {
	transient := ErrorResult(brainerrors.CodeToolTimeout, "timeout")
	success := &Result{Output: json.RawMessage(`{"ok":true}`)}

	inner := &scriptedTool{
		name:    "browser.click",
		results: []*Result{transient, transient, success},
	}
	wrapped := WithRetry(inner).(*retryWrapper)
	wrapped.sleep = func(_ context.Context, _ time.Duration) {} // skip real delay

	res, err := wrapped.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Errorf("final result should succeed, got %s", string(res.Output))
	}
	if inner.calls != 3 {
		t.Errorf("expected 3 calls, got %d", inner.calls)
	}
}

func TestRetryWrapperDoesNotRetryPermanent(t *testing.T) {
	perm := ErrorResult(brainerrors.CodeToolInputInvalid, "bad id")

	inner := &scriptedTool{
		name:    "browser.click",
		results: []*Result{perm},
	}
	wrapped := WithRetry(inner).(*retryWrapper)
	wrapped.sleep = func(_ context.Context, _ time.Duration) {}

	res, err := wrapped.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.IsError {
		t.Error("expected IsError result")
	}
	if inner.calls != 1 {
		t.Errorf("permanent error must not retry; got %d calls", inner.calls)
	}
}

func TestRetryWrapperBailsOnHardError(t *testing.T) {
	inner := &scriptedTool{
		name:    "browser.click",
		results: []*Result{nil},
		errs:    []error{errors.New("boom")},
	}
	wrapped := WithRetry(inner)
	_, err := wrapped.Execute(context.Background(), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("hard error must propagate")
	}
}

func TestRetryWrapperHonorsContextCancel(t *testing.T) {
	transient := ErrorResult(brainerrors.CodeToolTimeout, "t")
	inner := &scriptedTool{name: "x", results: []*Result{transient, transient, transient}}
	wrapped := WithRetry(inner).(*retryWrapper)

	ctx, cancel := context.WithCancel(context.Background())
	wrapped.sleep = func(ctx context.Context, _ time.Duration) {
		cancel()
		<-ctx.Done()
	}
	res, err := wrapped.Execute(ctx, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// 被 cancel 后应该返回最后那次的错误结果
	if !res.IsError {
		t.Error("expected transient error surface after ctx cancel")
	}
}
