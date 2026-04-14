package persistence

import "testing"

// TestSha256HexKnownVector verifies the canonical empty-input digest so
// that regressions on the CAS key algorithm defined in
// 26-持久化与恢复.md §6.2 are caught immediately.
func TestSha256HexKnownVector(t *testing.T) {
	// SHA-256 of "" is the standard test vector from FIPS 180-4.
	const empty = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	got := Sha256Hex(nil)
	if got != empty {
		t.Fatalf("Sha256Hex(nil) = %q, want %q", got, empty)
	}
	if gotBytes := Sha256Hex([]byte{}); gotBytes != empty {
		t.Fatalf("Sha256Hex([]byte{}) = %q, want %q", gotBytes, empty)
	}

	// "abc" vector from FIPS 180-4 Appendix B.
	const abc = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	if got := Sha256Hex([]byte("abc")); got != abc {
		t.Fatalf("Sha256Hex(\"abc\") = %q, want %q", got, abc)
	}
}

// TestComputeKeyFormat pins the wire format defined in
// 26-持久化与恢复.md §6.2 so byte-identical payloads always produce the
// same Ref regardless of backend.
func TestComputeKeyFormat(t *testing.T) {
	ref := ComputeKey([]byte("abc"))
	const want = Ref("sha256/ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad")
	if ref != want {
		t.Fatalf("ComputeKey(\"abc\") = %q, want %q", ref, want)
	}

	// Dedup guarantee: same bytes MUST produce the same ref.
	if ComputeKey([]byte("abc")) != ComputeKey([]byte("abc")) {
		t.Fatal("ComputeKey is not deterministic for identical input")
	}

	// Different bytes MUST produce different refs.
	if ComputeKey([]byte("abc")) == ComputeKey([]byte("abd")) {
		t.Fatal("ComputeKey collided on distinct single-byte inputs")
	}
}
