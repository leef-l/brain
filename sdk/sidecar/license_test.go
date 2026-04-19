package sidecar

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leef-l/brain/sdk/agent"
	brainlicense "github.com/leef-l/brain/sdk/license"
	"github.com/leef-l/brain/sdk/tool"
)

type testLicensedHandler struct{}

func (testLicensedHandler) Kind() agent.Kind { return agent.Kind("browser-pro") }
func (testLicensedHandler) Version() string  { return "test" }
func (testLicensedHandler) Tools() []string  { return nil }
func (testLicensedHandler) HandleMethod(context.Context, string, json.RawMessage) (interface{}, error) {
	return nil, ErrMethodNotFound
}

func TestRunLicensed_RejectsMissingHandlerFactory(t *testing.T) {
	err := RunLicensed(LicensedConfig{Brain: "browser-pro"})
	if err == nil {
		t.Fatal("expected missing handler factory error")
	}
	if !strings.Contains(err.Error(), `missing handler factory`) {
		t.Fatalf("error=%q, want missing handler factory", err)
	}
}

func TestRunLicensed_ReturnsVerifyError(t *testing.T) {
	err := RunLicensed(LicensedConfig{
		Brain:       "browser-pro",
		LicensePath: filepath.Join(t.TempDir(), "missing-license.json"),
		NewHandler: func(*brainlicense.Result) BrainHandler {
			t.Fatal("NewHandler should not be called on verify error")
			return nil
		},
	})
	if err == nil {
		t.Fatal("expected verify error")
	}
	if !strings.Contains(err.Error(), "license") {
		t.Fatalf("error=%q, want license-related error", err)
	}
}

func TestRunLicensed_SuccessPath(t *testing.T) {
	pubPEM, licensePath := writeSignedSidecarLicense(t, sidecarSignedLicenseConfig{
		allowedBrains: []string{"browser-pro"},
		notBefore:     "2020-01-01T00:00:00Z",
		expiresAt:     "2035-01-01T00:00:00Z",
	})

	restoreIO := swapSidecarStdioWithEOF(t)
	defer restoreIO()
	prevGate := tool.CurrentBrowserFeatureGateConfig()
	t.Cleanup(func() { tool.SetBrowserFeatureGate(&prevGate) })

	called := false
	err := RunLicensed(LicensedConfig{
		Brain:        "browser-pro",
		LicensePath:  licensePath,
		PublicKeyPEM: pubPEM,
		NewHandler: func(res *brainlicense.Result) BrainHandler {
			called = true
			if res == nil {
				t.Fatal("license result is nil")
			}
			if res.Customer != "Acme Inc." {
				t.Fatalf("customer=%q, want Acme Inc.", res.Customer)
			}
			if !res.AllowsFeature("browser-pro.evidence") {
				t.Fatal("expected browser-pro.evidence feature enabled")
			}
			return testLicensedHandler{}
		},
	})
	if err != nil {
		t.Fatalf("RunLicensed(success): %v", err)
	}
	if !called {
		t.Fatal("NewHandler was not called")
	}
	cfg := tool.CurrentBrowserFeatureGateConfig()
	if !cfg.Enabled {
		t.Fatalf("feature gate enabled = false, want true when browser-pro features exist in license: %+v", cfg)
	}
	if !cfg.Features["browser-pro.evidence"] {
		t.Fatalf("feature gate missing browser-pro.evidence: %+v", cfg.Features)
	}
}

func TestRunLicensed_AppliesBrowserFeatureGateFromLicense(t *testing.T) {
	prevVerify := verifyForBrain
	prevRun := runSidecar
	prevGate := tool.CurrentBrowserFeatureGateConfig()
	t.Cleanup(func() {
		verifyForBrain = prevVerify
		runSidecar = prevRun
		tool.SetBrowserFeatureGate(&prevGate)
	})

	verifyForBrain = func(string, brainlicense.VerifyOptions) (*brainlicense.Result, error) {
		return &brainlicense.Result{
			Features: map[string]bool{
				"browser-pro.intelligence": true,
				"browser-pro.evidence":     true,
			},
		}, nil
	}
	runSidecar = func(BrainHandler) error { return nil }

	err := RunLicensed(LicensedConfig{
		Brain:      "browser-pro",
		NewHandler: func(*brainlicense.Result) BrainHandler { return testLicensedHandler{} },
	})
	if err != nil {
		t.Fatalf("RunLicensed() err = %v, want nil", err)
	}

	cfg := tool.CurrentBrowserFeatureGateConfig()
	if !cfg.Enabled {
		t.Fatal("feature gate should be enabled from verified license")
	}
	if !cfg.Features["browser-pro.intelligence"] {
		t.Fatalf("missing browser-pro.intelligence in %+v", cfg.Features)
	}
	if !cfg.Features["browser-pro.evidence"] {
		t.Fatalf("missing browser-pro.evidence in %+v", cfg.Features)
	}
}

type sidecarSignedLicenseConfig struct {
	allowedBrains []string
	notBefore     string
	expiresAt     string
}

func writeSignedSidecarLicense(t *testing.T, cfg sidecarSignedLicenseConfig) ([]byte, string) {
	t.Helper()

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	file := brainlicense.File{
		SchemaVersion: 1,
		LicenseID:     "lic_test_sidecar_001",
		Customer:      "Acme Inc.",
		Edition:       "enterprise",
		AllowedBrains: cfg.allowedBrains,
		Features: map[string]bool{
			"browser-pro.evidence": true,
		},
		NotBefore:      cfg.notBefore,
		ExpiresAt:      cfg.expiresAt,
		MaxNodes:       5,
		MaxConcurrency: 3,
		IssuedAt:       "2026-01-01T00:00:00Z",
		Issuer:         "leef-l",
	}

	payload, err := canonicalLicensePayload(file)
	if err != nil {
		t.Fatal(err)
	}
	file.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, payload))

	dir := t.TempDir()
	path := filepath.Join(dir, "license.json")
	data, err := json.Marshal(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	pubDER, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})
	return pubPEM, path
}

func canonicalLicensePayload(file brainlicense.File) ([]byte, error) {
	m := map[string]interface{}{
		"schema_version":  file.SchemaVersion,
		"license_id":      file.LicenseID,
		"customer":        file.Customer,
		"edition":         file.Edition,
		"allowed_brains":  file.AllowedBrains,
		"features":        file.Features,
		"not_before":      file.NotBefore,
		"expires_at":      file.ExpiresAt,
		"max_nodes":       file.MaxNodes,
		"max_concurrency": file.MaxConcurrency,
		"issued_at":       file.IssuedAt,
		"issuer":          file.Issuer,
		"metadata":        file.Metadata,
	}
	return json.Marshal(m)
}

func swapSidecarStdioWithEOF(t *testing.T) func() {
	t.Helper()

	origStdin := os.Stdin
	origStdout := os.Stdout

	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := stdinW.Close(); err != nil {
		t.Fatal(err)
	}

	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	os.Stdin = stdinR
	os.Stdout = stdoutW

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = io.Copy(io.Discard, stdoutR)
	}()

	return func() {
		_ = stdoutW.Close()
		_ = stdinR.Close()
		_ = stdoutR.Close()
		os.Stdin = origStdin
		os.Stdout = origStdout
		<-done
	}
}
