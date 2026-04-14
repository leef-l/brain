package license

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	brainerrors "github.com/leef-l/brain/sdk/errors"
)

const envLicenseFile = "BRAIN_LICENSE_FILE"

// ResolvePath finds the active license file path using the configured path,
// environment override, ~/.brain/license.json, then <executable-dir>/license.json.
func ResolvePath(opts ResolveOptions) (string, error) {
	if path := strings.TrimSpace(opts.LicensePath); path != "" {
		return ensureExists(path)
	}

	if path := strings.TrimSpace(os.Getenv(envLicenseFile)); path != "" {
		return ensureExists(path)
	}

	if home := resolveHomeDir(opts.HomeDir); home != "" {
		if path, ok := firstExisting(filepath.Join(home, ".brain", "license.json")); ok {
			return path, nil
		}
	}

	if execPath := resolveExecutablePath(opts.ExecutablePath); execPath != "" {
		dir := execPath
		if st, err := os.Stat(execPath); err == nil {
			if !st.IsDir() {
				dir = filepath.Dir(execPath)
			}
		} else {
			dir = filepath.Dir(execPath)
		}
		if path, ok := firstExisting(filepath.Join(dir, "license.json")); ok {
			return path, nil
		}
	}

	return "", brainerrors.New(brainerrors.CodeLicenseNotFound,
		brainerrors.WithMessage("license file not found"),
		brainerrors.WithHint("set BRAIN_LICENSE_FILE or place license.json in ~/.brain or next to the paid brain binary"))
}

// VerifyForBrain verifies the configured license file and ensures it allows
// the requested paid brain.
func VerifyForBrain(brain string, opts VerifyOptions) (*Result, error) {
	path, err := ResolvePath(ResolveOptions{
		LicensePath:    opts.LicensePath,
		ExecutablePath: opts.ExecutablePath,
		HomeDir:        opts.HomeDir,
	})
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, brainerrors.Wrap(err, brainerrors.CodeLicenseNotFound,
			brainerrors.WithMessage("read license file"),
			brainerrors.WithHint("verify the license file path and permissions"))
	}

	var file File
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, brainerrors.Wrap(err, brainerrors.CodeInvalidParams,
			brainerrors.WithMessage("parse license file"))
	}

	if file.SchemaVersion != 1 {
		return nil, brainerrors.New(brainerrors.CodeLicenseSchemaUnsupported,
			brainerrors.WithMessage(fmt.Sprintf("unsupported license schema version %d", file.SchemaVersion)))
	}

	pub, err := resolvePublicKey(opts)
	if err != nil {
		return nil, err
	}

	payload, err := canonicalPayload(file)
	if err != nil {
		return nil, brainerrors.Wrap(err, brainerrors.CodeFrameEncodingError,
			brainerrors.WithMessage("canonicalize license payload"))
	}

	sig, err := base64.StdEncoding.DecodeString(file.Signature)
	if err != nil {
		return nil, brainerrors.Wrap(err, brainerrors.CodeLicenseInvalidSignature,
			brainerrors.WithMessage("decode license signature"))
	}
	if !ed25519.Verify(pub, payload, sig) {
		return nil, brainerrors.New(brainerrors.CodeLicenseInvalidSignature,
			brainerrors.WithMessage("license signature verification failed"))
	}

	nowFn := opts.Now
	if nowFn == nil {
		nowFn = func() time.Time { return time.Now().UTC() }
	}
	now := nowFn().UTC()

	notBefore, err := parseRFC3339("not_before", file.NotBefore)
	if err != nil {
		return nil, err
	}
	expiresAt, err := parseRFC3339("expires_at", file.ExpiresAt)
	if err != nil {
		return nil, err
	}
	issuedAt, err := parseRFC3339("issued_at", file.IssuedAt)
	if err != nil {
		return nil, err
	}

	if now.Before(notBefore) {
		return nil, brainerrors.New(brainerrors.CodeLicenseNotYetValid,
			brainerrors.WithMessage(fmt.Sprintf("license is not valid before %s", notBefore.Format(time.RFC3339))))
	}
	if now.After(expiresAt) {
		return nil, brainerrors.New(brainerrors.CodeLicenseExpired,
			brainerrors.WithMessage(fmt.Sprintf("license expired at %s", expiresAt.Format(time.RFC3339))))
	}
	if !brainAllowed(brain, file.AllowedBrains) {
		return nil, brainerrors.New(brainerrors.CodeLicenseBrainNotAllowed,
			brainerrors.WithMessage(fmt.Sprintf("license does not allow paid brain %q", brain)))
	}

	return &Result{
		Path:           path,
		LicenseID:      file.LicenseID,
		Customer:       file.Customer,
		Edition:        file.Edition,
		AllowedBrains:  append([]string(nil), file.AllowedBrains...),
		Features:       cloneBoolMap(file.Features),
		NotBefore:      notBefore,
		ExpiresAt:      expiresAt,
		MaxNodes:       file.MaxNodes,
		MaxConcurrency: file.MaxConcurrency,
		IssuedAt:       issuedAt,
		Issuer:         file.Issuer,
		Metadata:       cloneStringMap(file.Metadata),
	}, nil
}

func parseRFC3339(field, value string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(value))
	if err != nil {
		return time.Time{}, brainerrors.Wrap(err, brainerrors.CodeInvalidParams,
			brainerrors.WithMessage(fmt.Sprintf("invalid %s in license file", field)))
	}
	return t.UTC(), nil
}

func canonicalPayload(file File) ([]byte, error) {
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

func resolvePublicKey(opts VerifyOptions) (ed25519.PublicKey, error) {
	if len(opts.PublicKey) == ed25519.PublicKeySize {
		return ed25519.PublicKey(opts.PublicKey), nil
	}
	if len(opts.PublicKeyPEM) == 0 {
		return nil, brainerrors.New(brainerrors.CodeLicenseInvalidSignature,
			brainerrors.WithMessage("no public key configured for paid brain license verification"))
	}
	block, _ := pem.Decode(opts.PublicKeyPEM)
	if block == nil {
		return nil, brainerrors.New(brainerrors.CodeLicenseInvalidSignature,
			brainerrors.WithMessage("invalid public key PEM"))
	}
	pubAny, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, brainerrors.Wrap(err, brainerrors.CodeLicenseInvalidSignature,
			brainerrors.WithMessage("parse public key PEM"))
	}
	pub, ok := pubAny.(ed25519.PublicKey)
	if !ok {
		return nil, brainerrors.New(brainerrors.CodeLicenseInvalidSignature,
			brainerrors.WithMessage("public key is not an Ed25519 key"))
	}
	return pub, nil
}

func brainAllowed(brain string, allowed []string) bool {
	for _, item := range allowed {
		item = strings.TrimSpace(item)
		if item == "*" || item == brain {
			return true
		}
	}
	return false
}

func ensureExists(path string) (string, error) {
	if resolved, ok := firstExisting(path); ok {
		return resolved, nil
	}
	return "", brainerrors.New(brainerrors.CodeLicenseNotFound,
		brainerrors.WithMessage(fmt.Sprintf("license file not found: %s", path)))
}

func firstExisting(path string) (string, bool) {
	if strings.TrimSpace(path) == "" {
		return "", false
	}
	if _, err := os.Stat(path); err == nil {
		return path, true
	}
	return "", false
}

func resolveHomeDir(home string) string {
	if strings.TrimSpace(home) != "" {
		return home
	}
	home, _ = os.UserHomeDir()
	return home
}

func resolveExecutablePath(execPath string) string {
	if strings.TrimSpace(execPath) != "" {
		return execPath
	}
	execPath, _ = os.Executable()
	return execPath
}

func cloneBoolMap(src map[string]bool) map[string]bool {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]bool, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func cloneStringMap(src map[string]string) map[string]string {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]string, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}
