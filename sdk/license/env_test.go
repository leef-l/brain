package license

import (
	"os"
	"path/filepath"
	"testing"
)

func TestVerifyOptionsFromEnv_UsesInlinePEM(t *testing.T) {
	t.Setenv(envLicensePublicKeyPEM, "pem-data")
	t.Setenv(envLicensePublicKeyPEMFile, "")

	got, err := VerifyOptionsFromEnv(VerifyOptions{})
	if err != nil {
		t.Fatalf("VerifyOptionsFromEnv() err = %v", err)
	}
	if string(got.PublicKeyPEM) != "pem-data" {
		t.Fatalf("PublicKeyPEM = %q, want %q", string(got.PublicKeyPEM), "pem-data")
	}
}

func TestVerifyOptionsFromEnv_UsesPEMFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "license.pub.pem")
	if err := os.WriteFile(path, []byte("file-pem"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envLicensePublicKeyPEM, "")
	t.Setenv(envLicensePublicKeyPEMFile, path)

	got, err := VerifyOptionsFromEnv(VerifyOptions{})
	if err != nil {
		t.Fatalf("VerifyOptionsFromEnv() err = %v", err)
	}
	if string(got.PublicKeyPEM) != "file-pem" {
		t.Fatalf("PublicKeyPEM = %q, want %q", string(got.PublicKeyPEM), "file-pem")
	}
}

func TestVerifyOptionsFromEnv_PreservesExplicitPEM(t *testing.T) {
	t.Setenv(envLicensePublicKeyPEM, "pem-data")

	got, err := VerifyOptionsFromEnv(VerifyOptions{PublicKeyPEM: []byte("explicit")})
	if err != nil {
		t.Fatalf("VerifyOptionsFromEnv() err = %v", err)
	}
	if string(got.PublicKeyPEM) != "explicit" {
		t.Fatalf("PublicKeyPEM = %q, want %q", string(got.PublicKeyPEM), "explicit")
	}
}
