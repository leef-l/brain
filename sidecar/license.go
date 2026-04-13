package sidecar

import (
	"fmt"

	brainlicense "github.com/leef-l/brain/license"
)

// LicensedConfig wires paid-brain license verification into the shared
// sidecar runtime without forcing the paid brain implementation to re-create
// the bootstrap flow.
type LicensedConfig struct {
	Brain        string
	LicensePath  string
	PublicKey    []byte
	PublicKeyPEM []byte
	NewHandler   func(*brainlicense.Result) BrainHandler
}

// RunLicensed verifies a paid-brain license, builds the brain handler from the
// validated license result, then starts the normal sidecar runtime.
func RunLicensed(cfg LicensedConfig) error {
	if cfg.NewHandler == nil {
		return fmt.Errorf("sidecar: licensed brain %q missing handler factory", cfg.Brain)
	}
	res, err := brainlicense.VerifyForBrain(cfg.Brain, brainlicense.VerifyOptions{
		LicensePath:  cfg.LicensePath,
		PublicKey:    cfg.PublicKey,
		PublicKeyPEM: cfg.PublicKeyPEM,
	})
	if err != nil {
		return err
	}
	return Run(cfg.NewHandler(res))
}
