package license

import "time"

// File is the on-disk JSON license structure for paid specialist brains.
type File struct {
	SchemaVersion  int               `json:"schema_version"`
	LicenseID      string            `json:"license_id"`
	Customer       string            `json:"customer"`
	Edition        string            `json:"edition"`
	AllowedBrains  []string          `json:"allowed_brains"`
	Features       map[string]bool   `json:"features,omitempty"`
	NotBefore      string            `json:"not_before"`
	ExpiresAt      string            `json:"expires_at"`
	MaxNodes       int               `json:"max_nodes,omitempty"`
	MaxConcurrency int               `json:"max_concurrency,omitempty"`
	IssuedAt       string            `json:"issued_at"`
	Issuer         string            `json:"issuer"`
	Metadata       map[string]string `json:"metadata,omitempty"`
	Signature      string            `json:"signature"`
}

// Result is the validated license payload returned after signature and policy
// checks succeed.
type Result struct {
	Path           string
	LicenseID      string
	Customer       string
	Edition        string
	AllowedBrains  []string
	Features       map[string]bool
	NotBefore      time.Time
	ExpiresAt      time.Time
	MaxNodes       int
	MaxConcurrency int
	IssuedAt       time.Time
	Issuer         string
	Metadata       map[string]string
}

// AllowsFeature reports whether a validated license explicitly enables the
// named feature flag.
func (r *Result) AllowsFeature(name string) bool {
	if r == nil || len(r.Features) == 0 {
		return false
	}
	return r.Features[name]
}

// ResolveOptions controls how a license file is discovered.
type ResolveOptions struct {
	LicensePath    string
	ExecutablePath string
	HomeDir        string
}

// VerifyOptions controls license verification.
type VerifyOptions struct {
	LicensePath    string
	ExecutablePath string
	HomeDir        string
	PublicKey      []byte
	PublicKeyPEM   []byte
	Now            func() time.Time
}
