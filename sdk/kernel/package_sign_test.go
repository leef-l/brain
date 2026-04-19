package kernel

import (
	"crypto/ed25519"
	"os"
	"path/filepath"
	"testing"
)

func TestGenerateKeyPair(t *testing.T) {
	pub, priv, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair failed: %v", err)
	}
	if len(pub) != ed25519.PublicKeySize {
		t.Errorf("public key length = %d, want %d", len(pub), ed25519.PublicKeySize)
	}
	if len(priv) != ed25519.PrivateKeySize {
		t.Errorf("private key length = %d, want %d", len(priv), ed25519.PrivateKeySize)
	}
}

func TestSignAndVerifyRoundTrip(t *testing.T) {
	// 创建临时目录和假的 .brainpkg 文件
	tmpDir := t.TempDir()
	pkgPath := filepath.Join(tmpDir, "test-1.0.0.brainpkg")

	// 写入测试数据
	testData := []byte("this is a fake brain package for testing signatures")
	if err := os.WriteFile(pkgPath, testData, 0o644); err != nil {
		t.Fatalf("write test pkg: %v", err)
	}

	// 生成密钥对
	pub, priv, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}

	// 签名
	if err := SignPackage(pkgPath, priv); err != nil {
		t.Fatalf("SignPackage: %v", err)
	}

	// 验证签名文件是否存在
	sigPath := pkgPath + ".sig"
	if _, err := os.Stat(sigPath); err != nil {
		t.Fatalf("sig file not created: %v", err)
	}

	// 验证签名
	if err := VerifyPackageSignature(pkgPath, pub); err != nil {
		t.Fatalf("VerifyPackageSignature: %v", err)
	}
}

func TestVerifyTamperedPackage(t *testing.T) {
	tmpDir := t.TempDir()
	pkgPath := filepath.Join(tmpDir, "test-1.0.0.brainpkg")

	// 写入原始数据并签名
	if err := os.WriteFile(pkgPath, []byte("original content"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	pub, priv, _ := GenerateKeyPair()
	if err := SignPackage(pkgPath, priv); err != nil {
		t.Fatalf("sign: %v", err)
	}

	// 篡改包内容
	if err := os.WriteFile(pkgPath, []byte("tampered content!"), 0o644); err != nil {
		t.Fatalf("tamper: %v", err)
	}

	// 验证应失败
	err := VerifyPackageSignature(pkgPath, pub)
	if err == nil {
		t.Fatal("expected verification to fail on tampered package, but it succeeded")
	}
}

func TestVerifyWrongKey(t *testing.T) {
	tmpDir := t.TempDir()
	pkgPath := filepath.Join(tmpDir, "test-1.0.0.brainpkg")

	if err := os.WriteFile(pkgPath, []byte("test content"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// 用 key1 签名
	_, priv1, _ := GenerateKeyPair()
	if err := SignPackage(pkgPath, priv1); err != nil {
		t.Fatalf("sign: %v", err)
	}

	// 用 key2 的公钥验证
	pub2, _, _ := GenerateKeyPair()
	err := VerifyPackageSignature(pkgPath, pub2)
	if err == nil {
		t.Fatal("expected verification to fail with wrong key, but it succeeded")
	}
}

func TestVerifyNoSigFile(t *testing.T) {
	tmpDir := t.TempDir()
	pkgPath := filepath.Join(tmpDir, "test-1.0.0.brainpkg")

	if err := os.WriteFile(pkgPath, []byte("test content"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	pub, _, _ := GenerateKeyPair()
	err := VerifyPackageSignature(pkgPath, pub)
	if err == nil {
		t.Fatal("expected error when sig file missing, but got nil")
	}
}

func TestSignInvalidKeyLength(t *testing.T) {
	tmpDir := t.TempDir()
	pkgPath := filepath.Join(tmpDir, "test.brainpkg")
	if err := os.WriteFile(pkgPath, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	err := SignPackage(pkgPath, []byte("too-short"))
	if err == nil {
		t.Fatal("expected error for invalid key length")
	}
}

func TestVerifyInvalidPubKeyLength(t *testing.T) {
	tmpDir := t.TempDir()
	pkgPath := filepath.Join(tmpDir, "test.brainpkg")
	if err := os.WriteFile(pkgPath, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	err := VerifyPackageSignature(pkgPath, []byte("short"))
	if err == nil {
		t.Fatal("expected error for invalid pubkey length")
	}
}

func TestPackageInstallerVerifyWithPublicKey(t *testing.T) {
	// 测试 PackageInstaller.Verify 在有公钥配置时的签名校验行为
	// 由于 Verify 需要真实的 .brainpkg tar.gz 格式，这里只测试 PublicKey 字段设置
	installer := NewPackageInstaller()
	if installer.PublicKey != nil {
		t.Error("default installer should have nil PublicKey")
	}

	pub, _, _ := GenerateKeyPair()
	installer.PublicKey = pub
	if len(installer.PublicKey) != ed25519.PublicKeySize {
		t.Errorf("PublicKey length = %d, want %d", len(installer.PublicKey), ed25519.PublicKeySize)
	}
}
