package executionpolicy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func tempRoot(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	// Create some test files.
	for _, name := range []string{"readme.md", "src/main.go", "src/lib.go", "build/output.bin"} {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("test"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func boolPtr(v bool) *bool { return &v }

// --- NewFilePolicy ---

func TestNewFilePolicy_NilSpec(t *testing.T) {
	p, err := NewFilePolicy("/tmp", nil)
	if err != nil {
		t.Fatal(err)
	}
	if p != nil {
		t.Error("nil spec should return nil policy")
	}
}

func TestNewFilePolicy_EmptySpec(t *testing.T) {
	p, err := NewFilePolicy("/tmp", &FilePolicySpec{})
	if err != nil {
		t.Fatal(err)
	}
	if p != nil {
		t.Error("empty spec should return nil policy (not enabled)")
	}
}

func TestNewFilePolicy_InvalidPattern(t *testing.T) {
	_, err := NewFilePolicy("/tmp", &FilePolicySpec{
		AllowRead: []string{"[invalid"},
	})
	if err == nil {
		t.Error("expected error for invalid pattern")
	}
}

func TestNewFilePolicy_Valid(t *testing.T) {
	root := tempRoot(t)
	p, err := NewFilePolicy(root, &FilePolicySpec{
		AllowRead:   []string{"**/*.go"},
		AllowCreate: []string{"**/*.go"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if p == nil {
		t.Fatal("expected non-nil policy")
	}
	if !p.Enabled() {
		t.Error("policy should be enabled")
	}
	if p.Root() != filepath.Clean(root) {
		t.Errorf("Root() = %q", p.Root())
	}
}

// --- Enabled ---

func TestEnabled_Nil(t *testing.T) {
	var p *FilePolicy
	if p.Enabled() {
		t.Error("nil policy should not be enabled")
	}
}

// --- AllowsCommands ---

func TestAllowsCommands(t *testing.T) {
	root := tempRoot(t)

	// nil policy = commands allowed
	var nilP *FilePolicy
	if !nilP.AllowsCommands() {
		t.Error("nil policy should allow commands")
	}

	// explicit false
	p, _ := NewFilePolicy(root, &FilePolicySpec{
		AllowRead:     []string{"**"},
		AllowCommands: boolPtr(false),
	})
	if p.AllowsCommands() {
		t.Error("should deny commands when AllowCommands=false")
	}

	// explicit true
	p2, _ := NewFilePolicy(root, &FilePolicySpec{
		AllowRead:     []string{"**"},
		AllowCommands: boolPtr(true),
	})
	if !p2.AllowsCommands() {
		t.Error("should allow commands when AllowCommands=true")
	}
}

// --- AllowsDelegation ---

func TestAllowsDelegation(t *testing.T) {
	root := tempRoot(t)
	p, _ := NewFilePolicy(root, &FilePolicySpec{
		AllowRead:     []string{"**"},
		AllowDelegate: boolPtr(false),
	})
	if p.AllowsDelegation() {
		t.Error("should deny delegation when AllowDelegate=false")
	}
}

// --- CheckRead ---

func TestCheckRead_Allowed(t *testing.T) {
	root := tempRoot(t)
	p, _ := NewFilePolicy(root, &FilePolicySpec{
		AllowRead: []string{"**/*.go"},
	})
	if err := p.CheckRead(filepath.Join(root, "src/main.go")); err != nil {
		t.Errorf("CheckRead allowed file: %v", err)
	}
}

func TestCheckRead_Denied(t *testing.T) {
	root := tempRoot(t)
	p, _ := NewFilePolicy(root, &FilePolicySpec{
		AllowRead: []string{"**/*.go"},
	})
	if err := p.CheckRead(filepath.Join(root, "readme.md")); err == nil {
		t.Error("expected error reading non-matching file")
	}
}

func TestCheckRead_DenyOverridesAllow(t *testing.T) {
	root := tempRoot(t)
	p, _ := NewFilePolicy(root, &FilePolicySpec{
		AllowRead: []string{"**"},
		Deny:      []string{"build/**"},
	})
	if err := p.CheckRead(filepath.Join(root, "build/output.bin")); err == nil {
		t.Error("expected deny to override allow")
	}
}

func TestCheckRead_FileNotFound(t *testing.T) {
	root := tempRoot(t)
	p, _ := NewFilePolicy(root, &FilePolicySpec{
		AllowRead: []string{"**"},
	})
	if err := p.CheckRead(filepath.Join(root, "nonexistent.go")); err == nil {
		t.Error("expected error for nonexistent file")
	}
}

func TestCheckRead_DisabledPolicy(t *testing.T) {
	var p *FilePolicy
	if err := p.CheckRead("/any/path"); err != nil {
		t.Errorf("disabled policy should allow all reads: %v", err)
	}
}

// --- CheckWrite ---

func TestCheckWrite_NewFile(t *testing.T) {
	root := tempRoot(t)
	p, _ := NewFilePolicy(root, &FilePolicySpec{
		AllowCreate: []string{"**/*.go"},
	})
	if err := p.CheckWrite(filepath.Join(root, "src/new.go")); err != nil {
		t.Errorf("CheckWrite new file: %v", err)
	}
}

func TestCheckWrite_ExistingFile(t *testing.T) {
	root := tempRoot(t)
	p, _ := NewFilePolicy(root, &FilePolicySpec{
		AllowEdit: []string{"**/*.go"},
	})
	if err := p.CheckWrite(filepath.Join(root, "src/main.go")); err != nil {
		t.Errorf("CheckWrite existing file: %v", err)
	}
}

func TestCheckWrite_DeniedNew(t *testing.T) {
	root := tempRoot(t)
	p, _ := NewFilePolicy(root, &FilePolicySpec{
		AllowCreate: []string{"src/**"},
	})
	if err := p.CheckWrite(filepath.Join(root, "build/new.bin")); err == nil {
		t.Error("expected error creating file outside allow_create")
	}
}

func TestCheckWrite_DeniedEdit(t *testing.T) {
	root := tempRoot(t)
	p, _ := NewFilePolicy(root, &FilePolicySpec{
		AllowEdit: []string{"src/**"},
	})
	if err := p.CheckWrite(filepath.Join(root, "readme.md")); err == nil {
		t.Error("expected error editing file outside allow_edit")
	}
}

// --- CheckDelete ---

func TestCheckDelete_Allowed(t *testing.T) {
	root := tempRoot(t)
	p, _ := NewFilePolicy(root, &FilePolicySpec{
		AllowDelete: []string{"build/**"},
	})
	if err := p.CheckDelete(filepath.Join(root, "build/output.bin")); err != nil {
		t.Errorf("CheckDelete: %v", err)
	}
}

func TestCheckDelete_Denied(t *testing.T) {
	root := tempRoot(t)
	p, _ := NewFilePolicy(root, &FilePolicySpec{
		AllowDelete: []string{"build/**"},
	})
	if err := p.CheckDelete(filepath.Join(root, "src/main.go")); err == nil {
		t.Error("expected error deleting file outside allow_delete")
	}
}

// --- Path resolution ---

func TestResolve_OutsideWorkdir(t *testing.T) {
	root := tempRoot(t)
	p, _ := NewFilePolicy(root, &FilePolicySpec{
		AllowRead: []string{"**"},
	})
	if err := p.CheckRead("/etc/passwd"); err == nil {
		t.Error("expected error for path outside workdir")
	}
}

func TestResolve_RelativePath(t *testing.T) {
	root := tempRoot(t)
	p, _ := NewFilePolicy(root, &FilePolicySpec{
		AllowRead: []string{"**/*.go"},
	})
	// Relative path should be resolved against root
	if err := p.CheckRead("src/main.go"); err != nil {
		t.Errorf("relative path read: %v", err)
	}
}

func TestResolve_DotDotEscape(t *testing.T) {
	root := tempRoot(t)
	p, _ := NewFilePolicy(root, &FilePolicySpec{
		AllowRead: []string{"**"},
	})
	if err := p.CheckRead(filepath.Join(root, "src/../../etc/passwd")); err == nil {
		t.Error("expected error for ../ escape")
	}
}

// --- Spec ---

func TestSpec_DefensiveCopy(t *testing.T) {
	root := tempRoot(t)
	p, _ := NewFilePolicy(root, &FilePolicySpec{
		AllowRead: []string{"**"},
	})
	spec := p.Spec()
	spec.AllowRead = append(spec.AllowRead, "extra")
	if len(p.Spec().AllowRead) != 1 {
		t.Error("Spec() should return a defensive copy")
	}
}

func TestSpec_Nil(t *testing.T) {
	var p *FilePolicy
	if p.Spec() != nil {
		t.Error("nil policy Spec() should return nil")
	}
}

// --- Root ---

func TestRoot_Nil(t *testing.T) {
	var p *FilePolicy
	if p.Root() != "" {
		t.Error("nil policy Root() should return empty string")
	}
}

// --- MarshalJSON ---

func TestMarshalJSON(t *testing.T) {
	root := tempRoot(t)
	p, _ := NewFilePolicy(root, &FilePolicySpec{
		AllowRead: []string{"**/*.go"},
		Deny:      []string{"secret/**"},
	})
	data, err := p.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	var spec FilePolicySpec
	if err := json.Unmarshal(data, &spec); err != nil {
		t.Fatal(err)
	}
	if len(spec.AllowRead) != 1 || spec.AllowRead[0] != "**/*.go" {
		t.Errorf("AllowRead = %v", spec.AllowRead)
	}
}

// --- ExecutionSpec ---

func TestExecutionSpec_JSON(t *testing.T) {
	spec := ExecutionSpec{
		Workdir: "/tmp/work",
		FilePolicy: &FilePolicySpec{
			AllowRead: []string{"**"},
		},
	}
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	var got ExecutionSpec
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Workdir != "/tmp/work" {
		t.Errorf("Workdir = %q", got.Workdir)
	}
	if got.FilePolicy == nil || len(got.FilePolicy.AllowRead) != 1 {
		t.Error("FilePolicy not preserved")
	}
}

// --- normalizePatterns ---

func TestNormalizePatterns(t *testing.T) {
	got := normalizePatterns([]string{" src/main.go ", "  ", "c/d"})
	if len(got) != 2 {
		t.Fatalf("got %d patterns, want 2", len(got))
	}
	if got[0] != "src/main.go" {
		t.Errorf("got[0] = %q", got[0])
	}
	if got[1] != "c/d" {
		t.Errorf("got[1] = %q", got[1])
	}
}

func TestNormalizePatterns_Empty(t *testing.T) {
	got := normalizePatterns(nil)
	if got != nil {
		t.Error("nil input should return nil")
	}
}

// --- CheckSearchPath ---

func TestCheckSearchPath_AllowedDir(t *testing.T) {
	root := tempRoot(t)
	p, _ := NewFilePolicy(root, &FilePolicySpec{
		AllowRead: []string{"src/**"},
	})
	if err := p.CheckSearchPath("src"); err != nil {
		t.Errorf("CheckSearchPath: %v", err)
	}
}

func TestCheckSearchPath_DeniedDir(t *testing.T) {
	root := tempRoot(t)
	p, _ := NewFilePolicy(root, &FilePolicySpec{
		AllowRead: []string{"src/**"},
	})
	if err := p.CheckSearchPath("build"); err == nil {
		t.Error("expected error for search in non-allowed dir")
	}
}

func TestCheckSearchPath_Disabled(t *testing.T) {
	var p *FilePolicy
	if err := p.CheckSearchPath("/any"); err != nil {
		t.Errorf("disabled policy: %v", err)
	}
}
