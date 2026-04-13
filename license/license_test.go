package license

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"

	brainerrors "github.com/leef-l/brain/errors"
)

func TestVerifyForBrain_Valid(t *testing.T) {
	pubPEM, filePath := writeSignedLicense(t, signedLicenseConfig{
		allowedBrains: []string{"browser-pro"},
		notBefore:     "2026-01-01T00:00:00Z",
		expiresAt:     "2027-01-01T00:00:00Z",
	})

	res, err := VerifyForBrain("browser-pro", VerifyOptions{
		LicensePath:  filePath,
		PublicKeyPEM: pubPEM,
		Now:          func() time.Time { return time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("VerifyForBrain(valid): %v", err)
	}
	if res.Customer != "Acme Inc." {
		t.Fatalf("Customer = %q, want Acme Inc.", res.Customer)
	}
	if !res.AllowsFeature("browser-pro.evidence") {
		t.Fatalf("AllowsFeature(browser-pro.evidence) = false, want true")
	}
}

func TestVerifyForBrain_Expired(t *testing.T) {
	pubPEM, filePath := writeSignedLicense(t, signedLicenseConfig{
		allowedBrains: []string{"browser-pro"},
		notBefore:     "2026-01-01T00:00:00Z",
		expiresAt:     "2026-02-01T00:00:00Z",
	})

	_, err := VerifyForBrain("browser-pro", VerifyOptions{
		LicensePath:  filePath,
		PublicKeyPEM: pubPEM,
		Now:          func() time.Time { return time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC) },
	})
	assertBrainErrorCode(t, err, brainerrors.CodeLicenseExpired)
}

func TestVerifyForBrain_BrainNotAllowed(t *testing.T) {
	pubPEM, filePath := writeSignedLicense(t, signedLicenseConfig{
		allowedBrains: []string{"security-pro"},
		notBefore:     "2026-01-01T00:00:00Z",
		expiresAt:     "2027-01-01T00:00:00Z",
	})

	_, err := VerifyForBrain("browser-pro", VerifyOptions{
		LicensePath:  filePath,
		PublicKeyPEM: pubPEM,
		Now:          func() time.Time { return time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC) },
	})
	assertBrainErrorCode(t, err, brainerrors.CodeLicenseBrainNotAllowed)
}

func TestVerifyForBrain_InvalidSignature(t *testing.T) {
	pubPEM, filePath := writeSignedLicense(t, signedLicenseConfig{
		allowedBrains: []string{"browser-pro"},
		notBefore:     "2026-01-01T00:00:00Z",
		expiresAt:     "2027-01-01T00:00:00Z",
	})
	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	var file File
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	file.Signature = file.Signature[:len(file.Signature)-1] + "A"
	data, err = jsonMarshal(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = VerifyForBrain("browser-pro", VerifyOptions{
		LicensePath:  filePath,
		PublicKeyPEM: pubPEM,
		Now:          func() time.Time { return time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC) },
	})
	assertBrainErrorCode(t, err, brainerrors.CodeLicenseInvalidSignature)
}

func TestResolvePath_PrefersEnvThenExecutableDir(t *testing.T) {
	t.Setenv(envLicenseFile, filepath.Join(t.TempDir(), "missing.json"))

	_, err := ResolvePath(ResolveOptions{})
	assertBrainErrorCode(t, err, brainerrors.CodeLicenseNotFound)

	execDir := t.TempDir()
	candidate := filepath.Join(execDir, "license.json")
	if err := os.WriteFile(candidate, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envLicenseFile, "")

	got, err := ResolvePath(ResolveOptions{ExecutablePath: filepath.Join(execDir, "brain-browser-pro")})
	if err != nil {
		t.Fatalf("ResolvePath(executable dir): %v", err)
	}
	if got != candidate {
		t.Fatalf("ResolvePath() = %q, want %q", got, candidate)
	}
}

type signedLicenseConfig struct {
	allowedBrains []string
	notBefore     string
	expiresAt     string
}

func writeSignedLicense(t *testing.T, cfg signedLicenseConfig) ([]byte, string) {
	t.Helper()

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	file := File{
		SchemaVersion: 1,
		LicenseID:     "lic_test_001",
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

	payload, err := canonicalPayload(file)
	if err != nil {
		t.Fatal(err)
	}
	file.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, payload))

	dir := t.TempDir()
	path := filepath.Join(dir, "license.json")
	data, err := jsonMarshal(file)
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

func jsonMarshal(file File) ([]byte, error) {
	return []byte(`{
  "schema_version": 1,
  "license_id": "` + file.LicenseID + `",
  "customer": "` + file.Customer + `",
  "edition": "` + file.Edition + `",
  "allowed_brains": ["` + file.AllowedBrains[0] + `"],
  "features": {"browser-pro.evidence": true},
  "not_before": "` + file.NotBefore + `",
  "expires_at": "` + file.ExpiresAt + `",
  "max_nodes": 5,
  "max_concurrency": 3,
  "issued_at": "` + file.IssuedAt + `",
  "issuer": "` + file.Issuer + `",
  "signature": "` + file.Signature + `"
}`), nil
}

func assertBrainErrorCode(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error %q, got nil", want)
	}
	be, ok := err.(*brainerrors.BrainError)
	if !ok {
		t.Fatalf("error type = %T, want *BrainError", err)
	}
	if be.ErrorCode != want {
		t.Fatalf("ErrorCode = %q, want %q", be.ErrorCode, want)
	}
}
