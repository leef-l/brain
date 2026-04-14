package license

import (
	"testing"
)

func TestIsPaidBrain_BuiltinsAreFree(t *testing.T) {
	freeBrains := []string{
		"brain-central", "brain-code", "brain-verifier",
		"brain-fault", "brain-browser",
	}
	for _, name := range freeBrains {
		if IsPaidBrain(name) {
			t.Errorf("%q should be free, but IsPaidBrain returned true", name)
		}
	}
}

func TestCheckSidecar_FreeBrainNoLicense(t *testing.T) {
	// Free brains should return nil, nil when no license file exists.
	result, err := CheckSidecar("brain-central", VerifyOptions{
		HomeDir: t.TempDir(), // Empty dir — no license.json
	})
	if err != nil {
		t.Fatalf("CheckSidecar for free brain should not fail: %v", err)
	}
	if result != nil {
		t.Errorf("expected nil result for free brain without license, got %+v", result)
	}
}

func TestCheckSidecar_FreeBrainReturnsNilOnMissing(t *testing.T) {
	result, err := CheckSidecar("brain-code", VerifyOptions{
		LicensePath: "/nonexistent/path/license.json",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Error("expected nil result")
	}
}
