package braintesting

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestHealthCheckScriptAcceptsRepoStyleFallbackLayout(t *testing.T) {
	root := t.TempDir()
	scriptPath := filepath.Join(root, "scripts", "ops", "health-check.sh")
	if err := os.MkdirAll(filepath.Dir(scriptPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(script): %v", err)
	}

	srcScript := filepath.Join("..", "scripts", "ops", "health-check.sh")
	data, err := os.ReadFile(srcScript)
	if err != nil {
		t.Fatalf("ReadFile(script): %v", err)
	}
	if err := os.WriteFile(scriptPath, data, 0o755); err != nil {
		t.Fatalf("WriteFile(script): %v", err)
	}

	for _, rel := range []string{
		"bin/brain",
		"bin/brain-central",
		"bin/brain-data",
		"bin/brain-quant",
		"bin/brain-code",
		"bin/brain-verifier",
		"bin/brain-fault",
		"bin/brain-browser",
		"bin/exchange-executor",
	} {
		writeExecutable(t, filepath.Join(root, rel))
	}
	for _, rel := range []string{
		"bin/config.example.json",
		"bin/keybindings.example.json",
		"bin/quant.example.yaml",
		"VERSION.json",
		"LICENSE",
		"README.md",
		"CHANGELOG.md",
		"SECURITY.md",
		"docs/quant/43-生产交付清单.md",
		"scripts/ops/apply-migrations.sh",
		"scripts/ops/start-kernel.sh",
		"persistence/migrations/0001_signal_traces.sql",
		"persistence/migrations/0002_account_snapshots_daily_reviews.sql",
	} {
		writeFile(t, filepath.Join(root, rel), "fixture\n", 0o644)
	}

	cmd := exec.Command("bash", scriptPath)
	cmd.Dir = root
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("health-check failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "ok:") {
		t.Fatalf("expected success output, got:\n%s", output)
	}
}

func TestHealthCheckScriptVerifiesManifestWhenPresent(t *testing.T) {
	bundle := t.TempDir()
	for _, rel := range []string{
		"brain",
		"brain-central",
		"brain-data",
		"brain-quant",
		"brain-code",
		"brain-verifier",
		"brain-fault",
		"brain-browser",
		"exchange-executor",
	} {
		writeExecutable(t, filepath.Join(bundle, rel))
	}
	for _, rel := range []string{
		"config.example.json",
		"keybindings.example.json",
		"quant.example.yaml",
		"VERSION.json",
		"LICENSE",
		"README.md",
		"CHANGELOG.md",
		"SECURITY.md",
		"docs/quant/43-生产交付清单.md",
		"scripts/ops/apply-migrations.sh",
		"scripts/ops/health-check.sh",
		"scripts/ops/start-kernel.sh",
		"persistence/migrations/0001_signal_traces.sql",
		"persistence/migrations/0002_account_snapshots_daily_reviews.sql",
	} {
		writeFile(t, filepath.Join(bundle, rel), "fixture\n", 0o644)
	}
	writeManifest(t, bundle)

	script := filepath.Join("..", "scripts", "ops", "health-check.sh")
	cmd := exec.Command("bash", script, bundle)
	cmd.Dir = filepath.Join("..", "testing")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("health-check failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "MANIFEST.SHA256SUMS") {
		t.Fatalf("expected manifest verification output, got:\n%s", output)
	}
}

func TestHealthCheckScriptBundleModeDoesNotFallbackToRepoFiles(t *testing.T) {
	bundle := t.TempDir()
	script := filepath.Join("..", "scripts", "ops", "health-check.sh")

	cmd := exec.Command("bash", script, bundle)
	cmd.Dir = filepath.Join("..", "testing")
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected bundle mode to fail for empty bundle, got success:\n%s", output)
	}
	if strings.Contains(string(output), "/www/wwwroot/project/exchange/codex/brain") {
		t.Fatalf("expected bundle mode to avoid repo fallback, got:\n%s", output)
	}
	if !strings.Contains(string(output), "missing: brain") {
		t.Fatalf("expected missing bundle artifacts, got:\n%s", output)
	}
}

func TestHealthCheckScriptRunsDoctorInIsolatedHomeByDefault(t *testing.T) {
	bundle := t.TempDir()
	hostHome := t.TempDir()
	script := filepath.Join("..", "scripts", "ops", "health-check.sh")

	for _, rel := range []string{
		"brain-central",
		"brain-data",
		"brain-quant",
		"brain-code",
		"brain-verifier",
		"brain-fault",
		"brain-browser",
		"exchange-executor",
	} {
		writeExecutable(t, filepath.Join(bundle, rel))
	}
	writeFile(t, filepath.Join(bundle, "brain"), "#!/usr/bin/env bash\nset -euo pipefail\nif [[ \"${1:-}\" == \"doctor\" ]]; then\n  if [[ -f \"${HOME}/.brain/config.json\" ]]; then\n    echo \"host config leaked into doctor home\" >&2\n    exit 23\n  fi\nfi\nexit 0\n", 0o755)
	for _, rel := range []string{
		"config.example.json",
		"keybindings.example.json",
		"quant.example.yaml",
		"VERSION.json",
		"LICENSE",
		"README.md",
		"CHANGELOG.md",
		"SECURITY.md",
		"docs/quant/43-生产交付清单.md",
		"scripts/ops/apply-migrations.sh",
		"scripts/ops/health-check.sh",
		"scripts/ops/start-kernel.sh",
		"persistence/migrations/0001_signal_traces.sql",
		"persistence/migrations/0002_account_snapshots_daily_reviews.sql",
	} {
		writeFile(t, filepath.Join(bundle, rel), "fixture\n", 0o644)
	}
	writeFile(t, filepath.Join(hostHome, ".brain", "config.json"), "{\n  \"dummy\": true\n}\n", 0o644)

	cmd := exec.Command("bash", script, bundle)
	cmd.Dir = filepath.Join("..", "testing")
	cmd.Env = append(os.Environ(),
		"BRAIN_RUN_DOCTOR=1",
		"HOME="+hostHome,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("health-check with isolated doctor home failed: %v\n%s", err, output)
	}
}

func TestHealthCheckScriptFailsWhenManifestCannotBeVerified(t *testing.T) {
	bundle := t.TempDir()
	script := filepath.Join("..", "scripts", "ops", "health-check.sh")
	tempBin := t.TempDir()
	bashPath, err := exec.LookPath("bash")
	if err != nil {
		t.Fatalf("LookPath(bash): %v", err)
	}

	for _, rel := range []string{
		"brain",
		"brain-central",
		"brain-data",
		"brain-quant",
		"brain-code",
		"brain-verifier",
		"brain-fault",
		"brain-browser",
		"exchange-executor",
	} {
		writeExecutable(t, filepath.Join(bundle, rel))
	}
	for _, rel := range []string{
		"config.example.json",
		"keybindings.example.json",
		"quant.example.yaml",
		"VERSION.json",
		"LICENSE",
		"README.md",
		"CHANGELOG.md",
		"SECURITY.md",
		"docs/quant/43-生产交付清单.md",
		"scripts/ops/apply-migrations.sh",
		"scripts/ops/health-check.sh",
		"scripts/ops/start-kernel.sh",
		"persistence/migrations/0001_signal_traces.sql",
		"persistence/migrations/0002_account_snapshots_daily_reviews.sql",
	} {
		writeFile(t, filepath.Join(bundle, rel), "fixture\n", 0o644)
	}
	writeManifest(t, bundle)
	linkCommand(t, tempBin, "basename")
	linkCommand(t, tempBin, "dirname")

	cmd := exec.Command(bashPath, script, bundle)
	cmd.Dir = filepath.Join("..", "testing")
	cmd.Env = append(os.Environ(), "PATH="+tempBin)
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected manifest verification failure without checksum tools, got success:\n%s", output)
	}
	if !strings.Contains(string(output), "checksum tool missing") {
		t.Fatalf("expected checksum tool error, got:\n%s", output)
	}
}

func writeManifest(t *testing.T, dir string) {
	t.Helper()

	checker, args := checksumCommand()
	manifest := filepath.Join(dir, "MANIFEST.SHA256SUMS")
	cmd := exec.Command(checker, args...)
	cmd.Dir = dir
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("generate manifest: %v", err)
	}
	if err := os.WriteFile(manifest, output, 0o644); err != nil {
		t.Fatalf("WriteFile(manifest): %v", err)
	}
}

func checksumCommand() (string, []string) {
	if _, err := exec.LookPath("sha256sum"); err == nil {
		return "bash", []string{"-lc", "find . -type f ! -name MANIFEST.SHA256SUMS | LC_ALL=C sort | xargs sha256sum"}
	}
	if _, err := exec.LookPath("shasum"); err == nil {
		return "bash", []string{"-lc", "find . -type f ! -name MANIFEST.SHA256SUMS | LC_ALL=C sort | xargs shasum -a 256"}
	}
	return "false", nil
}

func writeExecutable(t *testing.T, path string) {
	t.Helper()
	writeFile(t, path, "#!/usr/bin/env bash\nexit 0\n", 0o755)
}

func linkCommand(t *testing.T, dir, name string) {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("required command %s not available: %v", name, err)
	}
	target := filepath.Join(dir, name)
	if err := os.Symlink(path, target); err != nil {
		t.Fatalf("Symlink(%s): %v", name, err)
	}
}

func writeFile(t *testing.T, path, data string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", path, err)
	}
	if err := os.WriteFile(path, []byte(data), mode); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
}
