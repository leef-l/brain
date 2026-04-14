package braintesting

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	brainerrors "github.com/leef-l/brain/sdk/errors"
)

// validNameRe restricts cassette names to safe path components. Absolute paths
// and directory traversal sequences are forbidden per 25-测试策略.md §11.3.
var validNameRe = regexp.MustCompile(`^[a-zA-Z0-9_\-/]+$`)

// validateCassetteName validates a cassette name and returns a BrainError on
// failure. Rules: non-empty, matches validNameRe, not absolute, no ".." component.
func validateCassetteName(name string) error {
	if name == "" {
		return brainerrors.New(brainerrors.CodeToolInputInvalid,
			brainerrors.WithMessage("cassette name must not be empty"))
	}
	if filepath.IsAbs(name) {
		return brainerrors.New(brainerrors.CodeToolInputInvalid,
			brainerrors.WithMessage(fmt.Sprintf(
				"cassette name %q must not be an absolute path", name)))
	}
	if !validNameRe.MatchString(name) {
		return brainerrors.New(brainerrors.CodeToolInputInvalid,
			brainerrors.WithMessage(fmt.Sprintf(
				"cassette name %q contains characters outside [a-zA-Z0-9_\\-/]", name)))
	}
	// Walk every cleaned component; reject ".." segments.
	clean := filepath.Clean(name)
	for p := clean; ; {
		dir, base := filepath.Split(p)
		if base == ".." {
			return brainerrors.New(brainerrors.CodeToolInputInvalid,
				brainerrors.WithMessage(fmt.Sprintf(
					"cassette name %q must not contain '..' path traversal", name)))
		}
		if dir == "" || dir == p {
			break
		}
		p = filepath.Clean(dir)
	}
	return nil
}

// cassetteHeader is the first JSONL line written to every cassette file.
// See 25-测试策略.md §11.1 for the canonical header schema.
type cassetteHeader struct {
	Type       string    `json:"type"`
	Name       string    `json:"name"`
	RecordedAt time.Time `json:"recorded_at"`
}

// FileCassetteRecorder implements CassetteRecorder by streaming JSONL events
// to <baseDir>/<name>.jsonl. Writes go to a .tmp sibling and the file is
// atomically renamed to the final path on Finish, so a crash mid-session
// never leaves a partial cassette visible to players.
//
// See 25-测试策略.md §11.3.
type FileCassetteRecorder struct {
	mu        sync.Mutex
	baseDir   string
	current   *os.File
	name      string
	tmpPath   string
	finalPath string
	buf       *bufio.Writer
	started   bool
	finished  bool
}

// NewFileCassetteRecorder creates a recorder rooted at baseDir. The directory
// is created with os.MkdirAll if it does not already exist. Returns a
// BrainError with CodeInvariantViolated if the directory cannot be created.
func NewFileCassetteRecorder(baseDir string) (*FileCassetteRecorder, error) {
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return nil, brainerrors.New(brainerrors.CodeInvariantViolated,
			brainerrors.WithMessage(fmt.Sprintf(
				"FileCassetteRecorder: cannot create base directory %q: %v", baseDir, err)))
	}
	return &FileCassetteRecorder{baseDir: baseDir}, nil
}

// Start begins a new recording session under name. name MUST match
// [a-zA-Z0-9_\-/], MUST NOT be absolute and MUST NOT contain "..". Calling
// Start on an already-started recorder returns CodeInvariantViolated.
func (r *FileCassetteRecorder) Start(ctx context.Context, name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.started {
		return brainerrors.New(brainerrors.CodeInvariantViolated,
			brainerrors.WithMessage("FileCassetteRecorder.Start: session already started"))
	}
	if err := validateCassetteName(name); err != nil {
		return err
	}

	finalPath := filepath.Join(r.baseDir, name+".jsonl")
	tmpPath := finalPath + ".tmp"

	// Ensure sub-directory exists (name may contain slash-separated components).
	if err := os.MkdirAll(filepath.Dir(finalPath), 0o755); err != nil {
		return brainerrors.New(brainerrors.CodeInvariantViolated,
			brainerrors.WithMessage(fmt.Sprintf(
				"FileCassetteRecorder.Start: cannot create cassette directory: %v", err)))
	}

	f, err := os.Create(tmpPath)
	if err != nil {
		return brainerrors.New(brainerrors.CodeInvariantViolated,
			brainerrors.WithMessage(fmt.Sprintf(
				"FileCassetteRecorder.Start: cannot create tmp file %q: %v", tmpPath, err)))
	}

	r.current = f
	r.name = name
	r.tmpPath = tmpPath
	r.finalPath = finalPath
	r.buf = bufio.NewWriter(f)
	r.started = true
	r.finished = false

	// Write header line: {"type":"header","name":...,"recorded_at":...}
	hdr := cassetteHeader{
		Type:       "header",
		Name:       name,
		RecordedAt: time.Now().UTC(),
	}
	line, merr := json.Marshal(hdr)
	if merr != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		r.started = false
		return brainerrors.New(brainerrors.CodeInvariantViolated,
			brainerrors.WithMessage(fmt.Sprintf(
				"FileCassetteRecorder.Start: cannot marshal header: %v", merr)))
	}
	if _, werr := fmt.Fprintf(r.buf, "%s\n", line); werr != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		r.started = false
		return brainerrors.New(brainerrors.CodeInvariantViolated,
			brainerrors.WithMessage(fmt.Sprintf(
				"FileCassetteRecorder.Start: cannot write header: %v", werr)))
	}
	return nil
}

// Record appends a single CassetteEvent to the active session in arrival
// order (JSONL). Returns CodeInvariantViolated if called before Start or
// after Finish.
func (r *FileCassetteRecorder) Record(ctx context.Context, event CassetteEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if !r.started {
		return brainerrors.New(brainerrors.CodeInvariantViolated,
			brainerrors.WithMessage("FileCassetteRecorder.Record: session not started"))
	}
	if r.finished {
		return brainerrors.New(brainerrors.CodeInvariantViolated,
			brainerrors.WithMessage("FileCassetteRecorder.Record: session already finished"))
	}

	line, err := json.Marshal(event)
	if err != nil {
		return brainerrors.New(brainerrors.CodeInvariantViolated,
			brainerrors.WithMessage(fmt.Sprintf(
				"FileCassetteRecorder.Record: cannot marshal event: %v", err)))
	}
	if _, werr := fmt.Fprintf(r.buf, "%s\n", line); werr != nil {
		return brainerrors.New(brainerrors.CodeInvariantViolated,
			brainerrors.WithMessage(fmt.Sprintf(
				"FileCassetteRecorder.Record: write error: %v", werr)))
	}
	return nil
}

// Finish flushes buffered bytes, closes the tmp file and atomically renames it
// to the final path. Idempotent: calling Finish on an already-finished recorder
// is a no-op. Calling Finish before Start returns CodeInvariantViolated.
func (r *FileCassetteRecorder) Finish(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if !r.started {
		return brainerrors.New(brainerrors.CodeInvariantViolated,
			brainerrors.WithMessage("FileCassetteRecorder.Finish: session not started"))
	}
	if r.finished {
		return nil // idempotent
	}

	if err := r.buf.Flush(); err != nil {
		return brainerrors.New(brainerrors.CodeInvariantViolated,
			brainerrors.WithMessage(fmt.Sprintf(
				"FileCassetteRecorder.Finish: flush error: %v", err)))
	}
	if err := r.current.Close(); err != nil {
		return brainerrors.New(brainerrors.CodeInvariantViolated,
			brainerrors.WithMessage(fmt.Sprintf(
				"FileCassetteRecorder.Finish: close error: %v", err)))
	}
	if err := os.Rename(r.tmpPath, r.finalPath); err != nil {
		return brainerrors.New(brainerrors.CodeInvariantViolated,
			brainerrors.WithMessage(fmt.Sprintf(
				"FileCassetteRecorder.Finish: rename %q → %q failed: %v",
				r.tmpPath, r.finalPath, err)))
	}
	r.finished = true
	return nil
}
