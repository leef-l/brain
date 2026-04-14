package tool

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
)

// Sandbox enforces that tool operations stay within allowed directories.
// The primary directory is the working directory where brain was launched.
// Additional directories can be authorized at runtime by the user.
type Sandbox struct {
	mu      sync.RWMutex
	primary string   // normalized absolute path of the launch directory
	allowed []string // additional authorized directories (absolute paths)
}

// NewSandbox creates a sandbox rooted at the given working directory.
// workDir must be an absolute path.
func NewSandbox(workDir string) *Sandbox {
	abs, _ := filepath.Abs(workDir)
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	return &Sandbox{
		primary: filepath.Clean(abs),
	}
}

// Primary returns the primary working directory.
func (s *Sandbox) Primary() string {
	return s.primary
}

// Authorize adds an additional directory to the allow list.
// Returns the cleaned absolute path that was added.
func (s *Sandbox) Authorize(dir string) string {
	abs, _ := filepath.Abs(dir)
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	abs = filepath.Clean(abs)

	s.mu.Lock()
	defer s.mu.Unlock()

	// Don't add duplicates.
	for _, a := range s.allowed {
		if a == abs {
			return abs
		}
	}
	s.allowed = append(s.allowed, abs)
	return abs
}

// Revoke removes a directory from the allow list.
func (s *Sandbox) Revoke(dir string) {
	abs, _ := filepath.Abs(dir)
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	abs = filepath.Clean(abs)

	s.mu.Lock()
	defer s.mu.Unlock()

	for i, a := range s.allowed {
		if a == abs {
			s.allowed = append(s.allowed[:i], s.allowed[i+1:]...)
			return
		}
	}
}

// Allowed returns all currently authorized directories (primary + extras).
func (s *Sandbox) Allowed() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	dirs := make([]string, 0, 1+len(s.allowed))
	dirs = append(dirs, s.primary)
	dirs = append(dirs, s.allowed...)
	return dirs
}

// Check verifies that the given path is within an allowed directory.
// The path can be absolute or relative (resolved against the primary dir).
// Returns the resolved absolute path and nil on success, or an error if
// the path escapes the sandbox.
func (s *Sandbox) Check(path string) (string, error) {
	var abs string
	if filepath.IsAbs(path) {
		abs = filepath.Clean(path)
	} else {
		abs = filepath.Clean(filepath.Join(s.primary, path))
	}

	// Resolve symlinks to prevent escaping via symlink.
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		// File might not exist yet (write_file creates it). Check parent dir.
		parentReal, err2 := filepath.EvalSymlinks(filepath.Dir(abs))
		if err2 != nil {
			// Parent doesn't exist either — check the raw path.
			real = abs
		} else {
			real = filepath.Join(parentReal, filepath.Base(abs))
		}
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	// Check against primary directory.
	if isUnder(real, s.primary) {
		return abs, nil
	}

	// Check against additional authorized directories.
	for _, dir := range s.allowed {
		if isUnder(real, dir) {
			return abs, nil
		}
	}

	return abs, &SandboxError{
		Path:    abs,
		Allowed: append([]string{s.primary}, s.allowed...),
	}
}

// isUnder returns true if path is equal to or nested under dir.
func isUnder(path, dir string) bool {
	if path == dir {
		return true
	}
	prefix := dir + "/"
	return strings.HasPrefix(path, prefix)
}

// SandboxError is returned when a path escapes the sandbox.
type SandboxError struct {
	Path    string
	Allowed []string
}

func (e *SandboxError) Error() string {
	return fmt.Sprintf("path %q is outside the sandbox (allowed: %s)",
		e.Path, strings.Join(e.Allowed, ", "))
}
