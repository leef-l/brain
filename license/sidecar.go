package license

import (
	"errors"
	"os"

	brainerrors "github.com/leef-l/brain/errors"
)

// paidBrains lists brains that require a valid license to run.
// Built-in free brains are NOT in this list.
var paidBrains = map[string]struct{}{
	// Currently all 5 built-in brains are free.
	// Paid brains will be added here as they ship:
	// "brain-browser-pro": {},
	// "brain-code-pro":    {},
}

// IsPaidBrain returns true if the named brain requires a license.
func IsPaidBrain(name string) bool {
	_, ok := paidBrains[name]
	return ok
}

// envLicenseRequired is the environment variable that forces license
// verification even for free brains (for enterprise deployments).
const envLicenseRequired = "BRAIN_LICENSE_REQUIRED"

// CheckSidecar verifies the license for a sidecar brain at startup.
//
// Behavior:
//   - If the brain is in the paid list → license is mandatory.
//   - If BRAIN_LICENSE_REQUIRED=1 → license is mandatory for all brains.
//   - Otherwise → returns nil (free brain, no license needed).
//
// When a license is required but verification fails, the returned error
// should be printed and the sidecar should exit(1).
//
// When a license is found and valid, the Result is returned so the sidecar
// can check feature flags.
func CheckSidecar(brainName string, opts VerifyOptions) (*Result, error) {
	required := IsPaidBrain(brainName) || os.Getenv(envLicenseRequired) == "1"

	if !required {
		// Try to verify if a license file exists (optional), but don't
		// fail if it's not found.
		result, err := VerifyForBrain(brainName, opts)
		if err != nil {
			// License not found or invalid — acceptable for free brains.
			var be *brainerrors.BrainError
			if errors.As(err, &be) && be.ErrorCode == brainerrors.CodeLicenseNotFound {
				return nil, nil
			}
			if errors.As(err, &be) && be.ErrorCode == brainerrors.CodeLicenseBrainNotAllowed {
				// License exists but doesn't list this brain — fine for free brains.
				return nil, nil
			}
			// Other errors (expired, invalid sig) — warn but don't block.
			return nil, nil
		}
		return result, nil
	}

	// License is mandatory.
	return VerifyForBrain(brainName, opts)
}
