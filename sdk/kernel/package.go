// Package kernel 提供 Brain Package 打包、校验、安装、卸载能力。
// 对应规范：34-Brain-Package与Marketplace规范.md
package kernel

import (
	"archive/tar"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/leef-l/brain/sdk/kernel/manifest"
)

// ---------------------------------------------------------------------------
// 核心类型
// ---------------------------------------------------------------------------

// BrainPackage 代表一个可分发的 Brain 包。
type BrainPackage struct {
	PackageID      string        `json:"package_id"`      // publisher/name 格式
	PackageVersion string        `json:"package_version"` // semver
	Manifest       *manifest.Manifest `json:"manifest"`
	Checksum       string        `json:"checksum"`  // 整包 SHA256
	Signature      string        `json:"signature"` // 可选签名
	Files          []PackageFile `json:"files"`     // 包含的文件列表
}

// PackageFile 描述包内的单个文件。
type PackageFile struct {
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	Checksum string `json:"checksum"` // 单文件 SHA256
}

// PackageMetadata 是嵌入 .brainpkg 的元数据文件内容。
type PackageMetadata struct {
	PackageID      string        `json:"package_id"`
	PackageVersion string        `json:"package_version"`
	Checksum       string        `json:"checksum"`
	Signature      string        `json:"signature,omitempty"`
	Files          []PackageFile `json:"files"`
}

// PackageInstaller 负责 Brain Package 的打包、校验、安装与卸载。
type PackageInstaller struct {
	// PublicKey 可选的 Ed25519 公钥（32 字节）。
	// 如果设置，Verify 时会自动检查 .brainpkg.sig 签名。
	PublicKey []byte
}

// NewPackageInstaller 创建一个 PackageInstaller 实例。
func NewPackageInstaller() *PackageInstaller {
	return &PackageInstaller{}
}

// ---------------------------------------------------------------------------
// Pack — 从源目录打包为 .brainpkg (tar.gz)
// ---------------------------------------------------------------------------

// Pack 将 srcDir 打包为 .brainpkg 文件，输出到 srcDir 的父目录。
// 返回 BrainPackage 元数据。
func (pi *PackageInstaller) Pack(srcDir string) (*BrainPackage, error) {
	srcDir, err := filepath.Abs(srcDir)
	if err != nil {
		return nil, fmt.Errorf("package pack: 解析绝对路径失败: %w", err)
	}

	// 加载并验证 manifest
	m, err := manifest.LoadFromDir(srcDir)
	if err != nil {
		return nil, fmt.Errorf("package pack: %w", err)
	}
	errs := manifest.Validate(m)
	if len(errs) > 0 {
		return nil, fmt.Errorf("package pack: manifest 校验失败: %v", errs[0])
	}

	// 收集文件列表
	var files []PackageFile
	err = filepath.Walk(srcDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		relPath, _ := filepath.Rel(srcDir, path)
		relPath = filepath.ToSlash(relPath)

		// 跳过已有的 .brainpkg 文件
		if strings.HasSuffix(relPath, ".brainpkg") {
			return nil
		}

		checksum, csErr := fileSHA256(path)
		if csErr != nil {
			return csErr
		}
		files = append(files, PackageFile{
			Path:     relPath,
			Size:     info.Size(),
			Checksum: checksum,
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("package pack: 遍历文件失败: %w", err)
	}

	// 构建 packageID
	publisher := "local"
	if md, ok := m.Metadata["publisher"]; ok {
		if s, ok2 := md.(string); ok2 && s != "" {
			publisher = s
		}
	}
	packageID := publisher + "/" + m.Kind

	pkg := &BrainPackage{
		PackageID:      packageID,
		PackageVersion: m.BrainVersion,
		Manifest:       m,
		Files:          files,
	}

	// 写入 tar.gz
	outName := fmt.Sprintf("%s-%s.brainpkg", m.Kind, m.BrainVersion)
	outPath := filepath.Join(filepath.Dir(srcDir), outName)

	if err := pi.writeTarGz(outPath, srcDir, pkg); err != nil {
		return nil, fmt.Errorf("package pack: 写入归档失败: %w", err)
	}

	// 计算整包 checksum
	checksum, err := fileSHA256(outPath)
	if err != nil {
		return nil, fmt.Errorf("package pack: 计算校验和失败: %w", err)
	}
	pkg.Checksum = checksum

	fmt.Printf("Packed %s -> %s (checksum: %s)\n", srcDir, outPath, checksum[:16]+"...")
	return pkg, nil
}

// writeTarGz 将目录内容写入 tar.gz 归档，并在头部嵌入 .brainpkg-meta.json。
func (pi *PackageInstaller) writeTarGz(outPath, srcDir string, pkg *BrainPackage) error {
	f, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer f.Close()

	gw := gzip.NewWriter(f)
	defer gw.Close()

	tw := tar.NewWriter(gw)
	defer tw.Close()

	// 先写入元数据文件
	meta := PackageMetadata{
		PackageID:      pkg.PackageID,
		PackageVersion: pkg.PackageVersion,
		Checksum:       pkg.Checksum,
		Signature:      pkg.Signature,
		Files:          pkg.Files,
	}
	metaData, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{
		Name: ".brainpkg-meta.json",
		Size: int64(len(metaData)),
		Mode: 0o644,
	}); err != nil {
		return err
	}
	if _, err := tw.Write(metaData); err != nil {
		return err
	}

	// 写入所有文件
	baseName := filepath.Base(srcDir)
	return filepath.Walk(srcDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		relPath, _ := filepath.Rel(srcDir, path)
		relPath = filepath.ToSlash(relPath)

		// 跳过 .brainpkg 文件
		if strings.HasSuffix(relPath, ".brainpkg") {
			return nil
		}

		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}

		if relPath == "." {
			header.Name = baseName + "/"
		} else {
			header.Name = baseName + "/" + relPath
			if info.IsDir() {
				header.Name += "/"
			}
		}

		if err := tw.WriteHeader(header); err != nil {
			return err
		}

		if info.IsDir() {
			return nil
		}

		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()
		_, err = io.Copy(tw, file)
		return err
	})
}

// ---------------------------------------------------------------------------
// Verify — 验证 .brainpkg 包的完整性
// ---------------------------------------------------------------------------

// Verify 解析 .brainpkg 文件，校验 checksum 和 manifest 格式。
func (pi *PackageInstaller) Verify(pkgPath string) (*BrainPackage, error) {
	pkgPath, err := filepath.Abs(pkgPath)
	if err != nil {
		return nil, fmt.Errorf("package verify: %w", err)
	}

	f, err := os.Open(pkgPath)
	if err != nil {
		return nil, fmt.Errorf("package verify: 打开文件失败: %w", err)
	}
	defer f.Close()

	gr, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("package verify: 非有效 gzip 文件: %w", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)

	var meta *PackageMetadata
	var m *manifest.Manifest
	fileChecksums := make(map[string]string) // relPath -> actual checksum

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("package verify: 读取归档失败: %w", err)
		}

		if header.Typeflag == tar.TypeDir {
			continue
		}

		name := header.Name

		// 元数据（限制 10MB）
		if filepath.Base(name) == ".brainpkg-meta.json" {
			const maxMetaSize = 10 << 20
			data, err := io.ReadAll(io.LimitReader(tr, maxMetaSize))
			if err != nil {
				return nil, fmt.Errorf("package verify: 读取元数据失败: %w", err)
			}
			meta = &PackageMetadata{}
			if err := json.Unmarshal(data, meta); err != nil {
				return nil, fmt.Errorf("package verify: 元数据解析失败: %w", err)
			}
			continue
		}

		// 限制单文件最大 256MB 防止恶意归档耗尽内存
		const maxFileSize = 256 << 20
		if header.Size > maxFileSize {
			return nil, fmt.Errorf("package verify: 文件 %s 大小 %d 超过限制 %d", name, header.Size, maxFileSize)
		}

		// 计算文件校验和
		h := sha256.New()
		data, err := io.ReadAll(io.TeeReader(io.LimitReader(tr, maxFileSize+1), h))
		if err != nil {
			return nil, fmt.Errorf("package verify: 读取文件 %s 失败: %w", name, err)
		}
		if int64(len(data)) > maxFileSize {
			return nil, fmt.Errorf("package verify: 文件 %s 实际大小超过限制 %d", name, maxFileSize)
		}
		checksum := hex.EncodeToString(h.Sum(nil))

		// 去掉顶层目录前缀
		parts := strings.SplitN(filepath.ToSlash(name), "/", 2)
		var relPath string
		if len(parts) == 2 {
			relPath = parts[1]
		} else {
			relPath = parts[0]
		}
		fileChecksums[relPath] = checksum

		// 解析 manifest
		base := filepath.Base(name)
		if base == "brain.json" || base == "brain.yaml" || base == "brain.yml" {
			m = &manifest.Manifest{}
			if base == "brain.json" {
				if err := json.Unmarshal(data, m); err != nil {
					return nil, fmt.Errorf("package verify: manifest 解析失败: %w", err)
				}
			}
		}
	}

	if meta == nil {
		return nil, fmt.Errorf("package verify: 归档中缺少 .brainpkg-meta.json")
	}
	if m == nil {
		return nil, fmt.Errorf("package verify: 归档中缺少 brain.json")
	}

	// 校验 manifest
	errs := manifest.Validate(m)
	if len(errs) > 0 {
		return nil, fmt.Errorf("package verify: manifest 校验失败: %v", errs[0])
	}

	// 校验每个文件的 checksum
	for _, pf := range meta.Files {
		actual, ok := fileChecksums[pf.Path]
		if !ok {
			return nil, fmt.Errorf("package verify: 元数据中声明的文件 %q 在归档中不存在", pf.Path)
		}
		if actual != pf.Checksum {
			return nil, fmt.Errorf("package verify: 文件 %q checksum 不匹配 (期望 %s, 实际 %s)", pf.Path, pf.Checksum[:16], actual[:16])
		}
	}

	// 签名校验：如果配置了公钥且签名文件存在，自动验证签名
	if len(pi.PublicKey) > 0 {
		sigPath := pkgPath + ".sig"
		if _, sigErr := os.Stat(sigPath); sigErr == nil {
			// 签名文件存在，执行验证
			if verifyErr := VerifyPackageSignature(pkgPath, pi.PublicKey); verifyErr != nil {
				return nil, fmt.Errorf("package verify: %w", verifyErr)
			}
		}
		// 签名文件不存在时跳过（向后兼容）
	}

	return &BrainPackage{
		PackageID:      meta.PackageID,
		PackageVersion: meta.PackageVersion,
		Manifest:       m,
		Checksum:       meta.Checksum,
		Signature:      meta.Signature,
		Files:          meta.Files,
	}, nil
}

// ---------------------------------------------------------------------------
// Install — 从 .brainpkg 安装到目标目录
// ---------------------------------------------------------------------------

// Install 将 .brainpkg 解包到 targetDir/<kind>，并验证完整性。
func (pi *PackageInstaller) Install(pkgPath, targetDir string) error {
	// 先验证
	pkg, err := pi.Verify(pkgPath)
	if err != nil {
		return fmt.Errorf("package install: 验证失败: %w", err)
	}

	destDir := filepath.Join(targetDir, pkg.Manifest.Kind)
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return fmt.Errorf("package install: 创建目录失败: %w", err)
	}

	// 重新打开归档并解压
	f, err := os.Open(pkgPath)
	if err != nil {
		return fmt.Errorf("package install: %w", err)
	}
	defer f.Close()

	gr, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gr.Close()

	tw := tar.NewReader(gr)
	for {
		header, err := tw.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("package install: 读取归档失败: %w", err)
		}

		// 跳过元数据
		if filepath.Base(header.Name) == ".brainpkg-meta.json" {
			continue
		}

		// 去掉顶层目录前缀
		parts := strings.SplitN(filepath.ToSlash(header.Name), "/", 2)
		var relPath string
		if len(parts) == 2 {
			relPath = parts[1]
		} else {
			relPath = parts[0]
		}
		if relPath == "" {
			continue
		}

		target := filepath.Join(destDir, relPath)

		// Zip Slip 防护：确保解压路径不会逃逸出 destDir
		if !strings.HasPrefix(filepath.Clean(target)+string(os.PathSeparator), filepath.Clean(destDir)+string(os.PathSeparator)) &&
			filepath.Clean(target) != filepath.Clean(destDir) {
			return fmt.Errorf("package install: 非法路径 %q 逃逸出目标目录", relPath)
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(header.Mode))
			if err != nil {
				return err
			}
			// 限制单文件最大 256MB 防止恶意归档耗尽磁盘
			const maxInstallFileSize = 256 << 20
			if _, err := io.Copy(out, io.LimitReader(tw, maxInstallFileSize)); err != nil {
				out.Close()
				return err
			}
			out.Close()
		}
	}

	// 写入 .active 标记
	if err := os.WriteFile(filepath.Join(destDir, ".active"), []byte(""), 0o644); err != nil {
		return err
	}

	fmt.Printf("Installed brain %q (v%s) from package to %s\n", pkg.Manifest.Kind, pkg.PackageVersion, destDir)
	return nil
}

// ---------------------------------------------------------------------------
// Uninstall — 移除已安装的 Brain
// ---------------------------------------------------------------------------

// Uninstall 移除 targetDir/<kind> 目录。
func (pi *PackageInstaller) Uninstall(kind, targetDir string) error {
	// 路径遍历防护：禁止 kind 包含路径分隔符或 ".."
	if err := validateKind(kind); err != nil {
		return fmt.Errorf("package uninstall: %w", err)
	}
	dir := filepath.Join(targetDir, kind)
	info, err := os.Stat(dir)
	if os.IsNotExist(err) {
		return fmt.Errorf("package uninstall: brain %q 未安装", kind)
	}
	if err != nil {
		return fmt.Errorf("package uninstall: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("package uninstall: %s 不是目录", dir)
	}

	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("package uninstall: 删除失败: %w", err)
	}

	fmt.Printf("Uninstalled brain %q from %s\n", kind, dir)
	return nil
}

// ---------------------------------------------------------------------------
// 辅助函数
// ---------------------------------------------------------------------------

// validateKind 校验 brain kind 是否安全，防止路径遍历攻击。
// kind 不能为空、不能包含路径分隔符、不能是 ".." 或 "."。
func validateKind(kind string) error {
	if kind == "" {
		return fmt.Errorf("kind 不能为空")
	}
	if strings.ContainsAny(kind, "/\\") {
		return fmt.Errorf("kind %q 包含非法路径分隔符", kind)
	}
	if kind == ".." || kind == "." {
		return fmt.Errorf("kind %q 是非法路径", kind)
	}
	return nil
}

// fileSHA256 计算文件的 SHA256 校验和。
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ---------------------------------------------------------------------------
// Ed25519 签名校验
// ---------------------------------------------------------------------------

// GenerateKeyPair 生成 Ed25519 密钥对，返回 (publicKey, privateKey)。
// publicKey 长度 32 字节，privateKey 长度 64 字节。
func GenerateKeyPair() (publicKey, privateKey []byte, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate key pair: %w", err)
	}
	return []byte(pub), []byte(priv), nil
}

// SignPackage 对 .brainpkg 文件进行 Ed25519 签名，签名写入 <pkgPath>.sig 文件。
// privateKey 必须是 64 字节的 Ed25519 私钥。
func SignPackage(pkgPath string, privateKey []byte) error {
	if len(privateKey) != ed25519.PrivateKeySize {
		return fmt.Errorf("sign: 私钥长度无效，期望 %d 字节，实际 %d 字节", ed25519.PrivateKeySize, len(privateKey))
	}

	pkgPath, err := filepath.Abs(pkgPath)
	if err != nil {
		return fmt.Errorf("sign: %w", err)
	}

	data, err := os.ReadFile(pkgPath)
	if err != nil {
		return fmt.Errorf("sign: 读取包文件失败: %w", err)
	}

	sig := ed25519.Sign(ed25519.PrivateKey(privateKey), data)

	sigPath := pkgPath + ".sig"
	if err := os.WriteFile(sigPath, sig, 0o644); err != nil {
		return fmt.Errorf("sign: 写入签名文件失败: %w", err)
	}

	return nil
}

// VerifyPackageSignature 验证 .brainpkg 文件的 Ed25519 签名。
// 签名文件应位于 <pkgPath>.sig。publicKey 必须是 32 字节的 Ed25519 公钥。
func VerifyPackageSignature(pkgPath string, publicKey []byte) error {
	if len(publicKey) != ed25519.PublicKeySize {
		return fmt.Errorf("verify signature: 公钥长度无效，期望 %d 字节，实际 %d 字节", ed25519.PublicKeySize, len(publicKey))
	}

	pkgPath, err := filepath.Abs(pkgPath)
	if err != nil {
		return fmt.Errorf("verify signature: %w", err)
	}

	sigPath := pkgPath + ".sig"
	sig, err := os.ReadFile(sigPath)
	if err != nil {
		return fmt.Errorf("verify signature: 读取签名文件失败: %w", err)
	}

	if len(sig) != ed25519.SignatureSize {
		return fmt.Errorf("verify signature: 签名长度无效，期望 %d 字节，实际 %d 字节", ed25519.SignatureSize, len(sig))
	}

	data, err := os.ReadFile(pkgPath)
	if err != nil {
		return fmt.Errorf("verify signature: 读取包文件失败: %w", err)
	}

	if !ed25519.Verify(ed25519.PublicKey(publicKey), data, sig) {
		return fmt.Errorf("verify signature: 签名验证失败，包可能已被篡改")
	}

	return nil
}
