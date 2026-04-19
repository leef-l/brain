package license

import (
	"fmt"
	"os"
	"strings"
)

const (
	envLicensePublicKeyPEM     = "BRAIN_LICENSE_PUBLIC_KEY_PEM"
	envLicensePublicKeyPEMFile = "BRAIN_LICENSE_PUBLIC_KEY_PEM_FILE"
)

// VerifyOptionsFromEnv fills optional verification inputs from environment
// variables when the caller did not provide them explicitly.
func VerifyOptionsFromEnv(opts VerifyOptions) (VerifyOptions, error) {
	if len(opts.PublicKey) != 0 || len(opts.PublicKeyPEM) != 0 {
		return opts, nil
	}
	if raw := strings.TrimSpace(os.Getenv(envLicensePublicKeyPEM)); raw != "" {
		opts.PublicKeyPEM = []byte(raw)
		return opts, nil
	}
	if path := strings.TrimSpace(os.Getenv(envLicensePublicKeyPEMFile)); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return opts, fmt.Errorf("read %s: %w", envLicensePublicKeyPEMFile, err)
		}
		opts.PublicKeyPEM = data
	}
	return opts, nil
}
