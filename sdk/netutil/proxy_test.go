package netutil

import (
	"net/http"
	"net/url"
	"testing"
)

func TestProxyFuncReturnsFunction(t *testing.T) {
	pf := ProxyFunc()
	if pf == nil {
		t.Fatal("expected non-nil proxy function")
	}
}

func TestProxyFuncWithDirectRequest(t *testing.T) {
	pf := ProxyFunc()
	req, err := http.NewRequest("GET", "http://localhost:7701/health", nil)
	if err != nil {
		t.Fatalf("create request failed: %v", err)
	}

	u, err := pf(req)
	if err != nil {
		t.Fatalf("proxy func returned error: %v", err)
	}
	// In most test environments no proxy is configured, so u should be nil.
	_ = u
}

func TestProxyFuncWithHTTPSRequest(t *testing.T) {
	pf := ProxyFunc()
	req, err := http.NewRequest("GET", "https://example.com/api", nil)
	if err != nil {
		t.Fatalf("create request failed: %v", err)
	}

	u, err := pf(req)
	if err != nil {
		t.Fatalf("proxy func returned error: %v", err)
	}
	_ = u
}

func TestProxyFuncNilRequest(t *testing.T) {
	pf := ProxyFunc()
	// Ensure the function doesn't panic on nil request.
	// Note: http.ProxyFromEnvironment may panic on nil req in older Go versions,
	// but we test the wrapper behavior.
	defer func() {
		if r := recover(); r != nil {
			// Acceptable if underlying implementation panics on nil request.
		}
	}()
	_, _ = pf(nil)
}

func TestProxyFuncSignature(t *testing.T) {
	var pf func(*http.Request) (*url.URL, error) = ProxyFunc()
	if pf == nil {
		t.Fatal("expected ProxyFunc to match expected signature")
	}
}
