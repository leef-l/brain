package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// ReadFileTool tests
// ---------------------------------------------------------------------------

func TestReadFile_BasicRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hello.txt")
	os.WriteFile(path, []byte("line1\nline2\nline3\n"), 0644)

	tool := NewReadFileTool("code")
	args, _ := json.Marshal(map[string]string{"path": path})
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Output)
	}

	var out readFileOutput
	json.Unmarshal(result.Output, &out)
	if !strings.Contains(out.Content, "line1") {
		t.Errorf("content missing 'line1': %q", out.Content)
	}
	if out.TotalLines != 4 { // 3 lines + trailing newline splits to 4
		t.Errorf("total_lines=%d", out.TotalLines)
	}
}

func TestReadFile_WithOffsetLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.txt")
	var lines []string
	for i := 0; i < 100; i++ {
		lines = append(lines, "line")
	}
	os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0644)

	tool := NewReadFileTool("code")
	args, _ := json.Marshal(map[string]interface{}{"path": path, "offset": 10, "limit": 5})
	result, _ := tool.Execute(context.Background(), args)

	var out readFileOutput
	json.Unmarshal(result.Output, &out)
	if out.Lines != 5 {
		t.Errorf("lines=%d, want 5", out.Lines)
	}
	if !out.Truncated {
		t.Error("should be truncated")
	}
}

func TestReadFile_FileNotFound(t *testing.T) {
	tool := NewReadFileTool("code")
	args, _ := json.Marshal(map[string]string{"path": "/nonexistent/file.txt"})
	result, _ := tool.Execute(context.Background(), args)
	if !result.IsError {
		t.Error("expected error for missing file")
	}
}

func TestReadFile_Directory(t *testing.T) {
	tool := NewReadFileTool("code")
	args, _ := json.Marshal(map[string]string{"path": t.TempDir()})
	result, _ := tool.Execute(context.Background(), args)
	if !result.IsError {
		t.Error("expected error for directory")
	}
}

func TestReadFile_BinaryDetection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "binary.bin")
	data := []byte{0x89, 0x50, 0x4E, 0x47, 0x00, 0x00, 0x00} // PNG header with NUL
	os.WriteFile(path, data, 0644)

	tool := NewReadFileTool("code")
	args, _ := json.Marshal(map[string]string{"path": path})
	result, _ := tool.Execute(context.Background(), args)
	if result.IsError {
		t.Fatal("binary should not be an error")
	}

	var out readFileOutput
	json.Unmarshal(result.Output, &out)
	if !strings.Contains(out.Content, "binary file") {
		t.Errorf("content=%q, want 'binary file'", out.Content)
	}
}

func TestReadFile_EmptyPath(t *testing.T) {
	tool := NewReadFileTool("code")
	args, _ := json.Marshal(map[string]string{"path": ""})
	result, _ := tool.Execute(context.Background(), args)
	if !result.IsError {
		t.Error("expected error for empty path")
	}
}

func TestReadFile_Name(t *testing.T) {
	tool := NewReadFileTool("code")
	if tool.Name() != "code.read_file" {
		t.Errorf("Name()=%q", tool.Name())
	}
	if tool.Risk() != RiskSafe {
		t.Errorf("Risk()=%v", tool.Risk())
	}
}

// ---------------------------------------------------------------------------
// WriteFileTool tests
// ---------------------------------------------------------------------------

func TestWriteFile_BasicWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "output.txt")

	tool := NewWriteFileTool("code")
	args, _ := json.Marshal(map[string]string{"path": path, "content": "hello world"})
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Output)
	}

	var out writeFileOutput
	json.Unmarshal(result.Output, &out)
	if out.BytesWritten != 11 {
		t.Errorf("bytes_written=%d, want 11", out.BytesWritten)
	}

	// Verify file content.
	data, _ := os.ReadFile(path)
	if string(data) != "hello world" {
		t.Errorf("file content=%q", string(data))
	}
}

func TestWriteFile_CreateDirs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a", "b", "c", "deep.txt")

	tool := NewWriteFileTool("code")
	args, _ := json.Marshal(map[string]string{"path": path, "content": "deep"})
	result, _ := tool.Execute(context.Background(), args)
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Output)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "deep" {
		t.Errorf("content=%q", string(data))
	}
}

func TestWriteFile_Overwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "exists.txt")
	os.WriteFile(path, []byte("old"), 0644)

	tool := NewWriteFileTool("code")
	args, _ := json.Marshal(map[string]string{"path": path, "content": "new content"})
	result, _ := tool.Execute(context.Background(), args)
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Output)
	}

	data, _ := os.ReadFile(path)
	if string(data) != "new content" {
		t.Errorf("content=%q, want 'new content'", string(data))
	}
}

func TestWriteFile_EmptyPath(t *testing.T) {
	tool := NewWriteFileTool("code")
	args, _ := json.Marshal(map[string]string{"path": "", "content": "x"})
	result, _ := tool.Execute(context.Background(), args)
	if !result.IsError {
		t.Error("expected error for empty path")
	}
}

func TestWriteFile_SystemDirRejected(t *testing.T) {
	tool := NewWriteFileTool("code")
	args, _ := json.Marshal(map[string]string{"path": "/etc/evil.conf", "content": "x"})
	result, _ := tool.Execute(context.Background(), args)
	if !result.IsError {
		t.Error("expected error for /etc/ write")
	}
}

func TestWriteFile_Name(t *testing.T) {
	tool := NewWriteFileTool("code")
	if tool.Name() != "code.write_file" {
		t.Errorf("Name()=%q", tool.Name())
	}
	if tool.Risk() != RiskMedium {
		t.Errorf("Risk()=%v", tool.Risk())
	}
}

// ---------------------------------------------------------------------------
// SearchTool tests
// ---------------------------------------------------------------------------

func TestSearch_BasicMatch(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("package main\nfunc main() {}\n"), 0644)
	os.WriteFile(filepath.Join(dir, "b.go"), []byte("package util\nfunc helper() {}\n"), 0644)

	tool := NewSearchTool("code")
	args, _ := json.Marshal(map[string]interface{}{"pattern": "func main", "path": dir})
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Output)
	}

	var out searchOutput
	json.Unmarshal(result.Output, &out)
	if out.Total != 1 {
		t.Errorf("total=%d, want 1", out.Total)
	}
	if len(out.Matches) != 1 {
		t.Fatalf("matches=%d, want 1", len(out.Matches))
	}
	if out.Matches[0].File != "a.go" {
		t.Fatalf("file=%q, want a.go", out.Matches[0].File)
	}
	if out.Matches[0].Line != 2 {
		t.Errorf("line=%d, want 2", out.Matches[0].Line)
	}
}

func TestSearch_WithGlob(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("hello go\n"), 0644)
	os.WriteFile(filepath.Join(dir, "b.py"), []byte("hello python\n"), 0644)

	tool := NewSearchTool("code")
	args, _ := json.Marshal(map[string]interface{}{"pattern": "hello", "path": dir, "glob": "*.go"})
	result, _ := tool.Execute(context.Background(), args)

	var out searchOutput
	json.Unmarshal(result.Output, &out)
	if out.Total != 1 {
		t.Errorf("total=%d, want 1 (only .go)", out.Total)
	}
}

func TestSearch_Regex(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "test.go"), []byte("func TestFoo(t *testing.T) {}\nfunc TestBar(t *testing.T) {}\n"), 0644)

	tool := NewSearchTool("code")
	args, _ := json.Marshal(map[string]interface{}{
		"pattern":  "func Test\\w+",
		"path":     dir,
		"is_regex": true,
	})
	result, _ := tool.Execute(context.Background(), args)

	var out searchOutput
	json.Unmarshal(result.Output, &out)
	if out.Total != 2 {
		t.Errorf("total=%d, want 2", out.Total)
	}
}

func TestSearch_NoResults(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("nothing here\n"), 0644)

	tool := NewSearchTool("code")
	args, _ := json.Marshal(map[string]interface{}{"pattern": "nonexistent", "path": dir})
	result, _ := tool.Execute(context.Background(), args)

	var out searchOutput
	json.Unmarshal(result.Output, &out)
	if out.Total != 0 {
		t.Errorf("total=%d, want 0", out.Total)
	}
}

func TestSearch_MaxResults(t *testing.T) {
	dir := t.TempDir()
	var lines []string
	for i := 0; i < 100; i++ {
		lines = append(lines, "match line")
	}
	os.WriteFile(filepath.Join(dir, "big.txt"), []byte(strings.Join(lines, "\n")), 0644)

	tool := NewSearchTool("code")
	args, _ := json.Marshal(map[string]interface{}{"pattern": "match", "path": dir, "max_results": 5})
	result, _ := tool.Execute(context.Background(), args)

	var out searchOutput
	json.Unmarshal(result.Output, &out)
	if len(out.Matches) != 5 {
		t.Errorf("matches=%d, want 5", len(out.Matches))
	}
	if out.Total != 100 {
		t.Errorf("total=%d, want 100", out.Total)
	}
	if !out.Truncated {
		t.Error("should be truncated")
	}
}

func TestSearch_IncludesHiddenFiles(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".hidden.go"), []byte("secret match\n"), 0644)

	tool := NewSearchTool("code")
	args, _ := json.Marshal(map[string]interface{}{"pattern": "secret", "path": dir})
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Output)
	}

	var out searchOutput
	json.Unmarshal(result.Output, &out)
	if out.Total != 1 {
		t.Fatalf("total=%d, want 1", out.Total)
	}
	if len(out.Matches) != 1 || out.Matches[0].File != ".hidden.go" {
		t.Fatalf("matches=%+v, want .hidden.go", out.Matches)
	}
}

func TestSearch_SkipsConfiguredDirs(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "vendor"), 0755)
	os.MkdirAll(filepath.Join(dir, "node_modules"), 0755)
	os.WriteFile(filepath.Join(dir, "vendor", "skip.go"), []byte("blocked match\n"), 0644)
	os.WriteFile(filepath.Join(dir, "node_modules", "skip.js"), []byte("blocked match\n"), 0644)
	os.WriteFile(filepath.Join(dir, "keep.go"), []byte("allowed match\n"), 0644)

	tool := NewSearchTool("code")
	args, _ := json.Marshal(map[string]interface{}{"pattern": "match", "path": dir})
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Output)
	}

	var out searchOutput
	json.Unmarshal(result.Output, &out)
	if out.Total != 1 {
		t.Fatalf("total=%d, want 1", out.Total)
	}
	if len(out.Matches) != 1 || out.Matches[0].File != "keep.go" {
		t.Fatalf("matches=%+v, want keep.go only", out.Matches)
	}
}

func TestSearch_FallbackWithoutRipgrep(t *testing.T) {
	orig := rgLookPath
	rgLookPath = func(string) (string, error) {
		return "", os.ErrNotExist
	}
	t.Cleanup(func() { rgLookPath = orig })

	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("package main\nfunc main() {}\n"), 0644)

	tool := NewSearchTool("code")
	args, _ := json.Marshal(map[string]interface{}{"pattern": "func main", "path": dir})
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Output)
	}

	var out searchOutput
	json.Unmarshal(result.Output, &out)
	if out.Total != 1 || len(out.Matches) != 1 {
		t.Fatalf("out=%+v, want one fallback match", out)
	}
}

func TestSearch_EmptyPattern(t *testing.T) {
	tool := NewSearchTool("code")
	args, _ := json.Marshal(map[string]interface{}{"pattern": ""})
	result, _ := tool.Execute(context.Background(), args)
	if !result.IsError {
		t.Error("expected error for empty pattern")
	}
}

func TestSearch_Name(t *testing.T) {
	tool := NewSearchTool("code")
	if tool.Name() != "code.search" {
		t.Errorf("Name()=%q", tool.Name())
	}
}

// ---------------------------------------------------------------------------
// ShellExecTool tests
// ---------------------------------------------------------------------------

func TestShellExec_BasicCommand(t *testing.T) {
	tool := NewShellExecTool("code", nil)
	args, _ := json.Marshal(map[string]string{"command": "echo hello"})
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}

	var out shellExecOutput
	json.Unmarshal(result.Output, &out)
	if out.Stdout != "hello" {
		t.Errorf("stdout=%q, want 'hello'", out.Stdout)
	}
	if out.ExitCode != 0 {
		t.Errorf("exit_code=%d, want 0", out.ExitCode)
	}
	if result.IsError {
		t.Error("exit 0 should not be IsError")
	}
}

func TestShellExec_NonZeroExit(t *testing.T) {
	tool := NewShellExecTool("code", nil)
	args, _ := json.Marshal(map[string]string{"command": "exit 42"})
	result, _ := tool.Execute(context.Background(), args)

	var out shellExecOutput
	json.Unmarshal(result.Output, &out)
	if out.ExitCode != 42 {
		t.Errorf("exit_code=%d, want 42", out.ExitCode)
	}
	if !result.IsError {
		t.Error("non-zero exit should be IsError")
	}
}

func TestShellExec_Stderr(t *testing.T) {
	tool := NewShellExecTool("code", nil)
	args, _ := json.Marshal(map[string]string{"command": "echo err >&2"})
	result, _ := tool.Execute(context.Background(), args)

	var out shellExecOutput
	json.Unmarshal(result.Output, &out)
	if out.Stderr != "err" {
		t.Errorf("stderr=%q, want 'err'", out.Stderr)
	}
}

func TestShellExec_Timeout(t *testing.T) {
	tool := NewShellExecTool("code", nil)
	args, _ := json.Marshal(map[string]interface{}{"command": "sleep 10", "timeout_seconds": 1})
	result, _ := tool.Execute(context.Background(), args)

	var out shellExecOutput
	json.Unmarshal(result.Output, &out)
	if !out.TimedOut {
		t.Error("expected timed_out=true")
	}
}

func TestShellExec_WorkingDir(t *testing.T) {
	dir := t.TempDir()
	tool := NewShellExecTool("code", nil)
	args, _ := json.Marshal(map[string]interface{}{"command": "pwd", "working_dir": dir})
	result, _ := tool.Execute(context.Background(), args)

	var out shellExecOutput
	json.Unmarshal(result.Output, &out)
	if !strings.Contains(out.Stdout, filepath.Base(dir)) {
		t.Errorf("stdout=%q, want contains %q", out.Stdout, filepath.Base(dir))
	}
}

func TestShellExec_EmptyCommand(t *testing.T) {
	tool := NewShellExecTool("code", nil)
	args, _ := json.Marshal(map[string]string{"command": ""})
	result, _ := tool.Execute(context.Background(), args)
	if !result.IsError {
		t.Error("expected error for empty command")
	}
}

func TestShellExec_Name(t *testing.T) {
	tool := NewShellExecTool("code", nil)
	if tool.Name() != "code.shell_exec" {
		t.Errorf("Name()=%q", tool.Name())
	}
	if tool.Risk() != RiskHigh {
		t.Errorf("Risk()=%v", tool.Risk())
	}
}

func TestShellExec_CommandSandbox(t *testing.T) {
	dir := t.TempDir()
	sb := NewSandbox(dir)
	cfg := &SandboxConfig{Enabled: true}
	cmdSandbox := NewCommandSandbox(sb, cfg)

	if cmdSandbox == nil || !cmdSandbox.Available() {
		t.Skip("OS-level command sandbox not available on this platform")
	}

	// Write a file inside sandbox.
	os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("sandboxed"), 0644)

	st := NewShellExecTool("code", sb)
	st.SetCommandSandbox(cmdSandbox)

	// Can read a file inside sandbox.
	args, _ := json.Marshal(map[string]interface{}{
		"command":     "cat hello.txt",
		"working_dir": dir,
	})
	result, err := st.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	var out shellExecOutput
	json.Unmarshal(result.Output, &out)
	if out.Stdout != "sandboxed" {
		t.Errorf("expected 'sandboxed', got %q", out.Stdout)
	}
	if !out.Sandboxed {
		t.Error("expected Sandboxed=true")
	}

	// Cannot access host /tmp (sandbox gives a private tmpfs on Linux).
	os.WriteFile("/tmp/cmd_sandbox_test_sentinel", []byte("leaked"), 0644)
	defer os.Remove("/tmp/cmd_sandbox_test_sentinel")

	args2, _ := json.Marshal(map[string]interface{}{
		"command": "cat /tmp/cmd_sandbox_test_sentinel",
	})
	result2, _ := st.Execute(context.Background(), args2)
	var out2 shellExecOutput
	json.Unmarshal(result2.Output, &out2)
	if strings.Contains(out2.Stdout, "leaked") {
		t.Error("sandbox leaked: command could read host /tmp file")
	}

	// Network is isolated.
	args3, _ := json.Marshal(map[string]interface{}{
		"command":         "curl -s --connect-timeout 1 http://1.1.1.1 2>&1 || echo network_blocked",
		"timeout_seconds": 3,
	})
	result3, _ := st.Execute(context.Background(), args3)
	var out3 shellExecOutput
	json.Unmarshal(result3.Output, &out3)
	combined := out3.Stdout + out3.Stderr
	if !strings.Contains(combined, "network_blocked") && !strings.Contains(combined, "Could not resolve") && !strings.Contains(combined, "Network is unreachable") && out3.ExitCode == 0 {
		t.Errorf("expected network to be blocked, got stdout=%q stderr=%q exit=%d", out3.Stdout, out3.Stderr, out3.ExitCode)
	}
}
