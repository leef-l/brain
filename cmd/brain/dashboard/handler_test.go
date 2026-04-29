package dashboard

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestExtractBearerToken(t *testing.T) {
	// Empty token always passes.
	req := httptest.NewRequest("GET", "/test", nil)
	if !extractBearerToken(req, "") {
		t.Fatal("expected empty token to pass")
	}

	// Valid Bearer header.
	req = httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer secret123")
	if !extractBearerToken(req, "secret123") {
		t.Fatal("expected valid bearer token to pass")
	}

	// Invalid Bearer header.
	req = httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	if extractBearerToken(req, "secret123") {
		t.Fatal("expected invalid bearer token to fail")
	}

	// Valid query param fallback.
	req = httptest.NewRequest("GET", "/test?token=secret123", nil)
	if !extractBearerToken(req, "secret123") {
		t.Fatal("expected valid query token to pass")
	}

	// Invalid query param.
	req = httptest.NewRequest("GET", "/test?token=wrong", nil)
	if extractBearerToken(req, "secret123") {
		t.Fatal("expected invalid query token to fail")
	}

	// No auth provided.
	req = httptest.NewRequest("GET", "/test", nil)
	if extractBearerToken(req, "secret123") {
		t.Fatal("expected missing token to fail")
	}
}

func TestAuthMiddleware(t *testing.T) {
	called := false
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	// Empty token passes through.
	mw := AuthMiddleware("", handler)
	req := httptest.NewRequest("GET", "/test", nil)
	rr := httptest.NewRecorder()
	mw.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if !called {
		t.Fatal("expected handler to be called")
	}

	// Valid token.
	called = false
	mw = AuthMiddleware("secret", handler)
	req = httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rr = httptest.NewRecorder()
	mw.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if !called {
		t.Fatal("expected handler to be called with valid token")
	}

	// Invalid token.
	called = false
	req = httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	rr = httptest.NewRecorder()
	mw.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
	if called {
		t.Fatal("expected handler to NOT be called with invalid token")
	}
}

func TestAuthWrap(t *testing.T) {
	called := false
	handler := func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}

	// Empty token.
	wrapped := authWrap("", handler)
	req := httptest.NewRequest("GET", "/test", nil)
	rr := httptest.NewRecorder()
	wrapped(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	// Valid token.
	called = false
	wrapped = authWrap("secret", handler)
	req = httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rr = httptest.NewRecorder()
	wrapped(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if !called {
		t.Fatal("expected handler to be called")
	}
}
