// Package security provides the PermissionGate — a unified security checkpoint
// for sidecar brain startup.
//
// It bundles:
//   - License verification (paid / BRAIN_LICENSE_REQUIRED)
//   - Optional future: capability attestation, policy enforcement
//
// Usage in thin brain main():
//
//	security.NewGate("brain-code").MustCheck()
package security

import (
	"fmt"
	"os"

	"github.com/leef-l/brain/sdk/license"
)

// Gate is a per-brain security checkpoint.
type Gate struct {
	brainName string
}

// NewGate creates a security gate for the named brain binary.
func NewGate(brainName string) *Gate {
	return &Gate{brainName: brainName}
}

// Check runs license verification. Returns an error if the gate should block
// startup (e.g. missing mandatory license).
func (g *Gate) Check() error {
	verifyOpts, err := license.VerifyOptionsFromEnv(license.VerifyOptions{})
	if err != nil {
		return fmt.Errorf("license config: %w", err)
	}
	if _, err := license.CheckSidecar(g.brainName, verifyOpts); err != nil {
		return fmt.Errorf("license: %w", err)
	}
	return nil
}

// MustCheck runs the gate and exits the process on failure.
func (g *Gate) MustCheck() {
	if err := g.Check(); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", g.brainName, err)
		os.Exit(1)
	}
}
