package braintesting

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	brainerrors "github.com/leef-l/brain/errors"
)

// FileCassettePlayer implements CassettePlayer by reading a JSONL cassette
// file produced by FileCassetteRecorder. Load reads the entire file into
// memory and positions the cursor at the first non-header event so that Next
// walks the recorded stream deterministically without any further I/O.
//
// See 25-测试策略.md §11.
type FileCassettePlayer struct {
	mu      sync.Mutex
	baseDir string
	events  []CassetteEvent
	cursor  int
	loaded  bool
}

// NewFileCassettePlayer returns a player rooted at baseDir. No I/O is
// performed until Load is called.
func NewFileCassettePlayer(baseDir string) *FileCassettePlayer {
	return &FileCassettePlayer{baseDir: baseDir}
}

// rawLine is used internally to unmarshal one JSONL line before deciding
// whether it is a header or a CassetteEvent.
type rawLine struct {
	Type string `json:"type"`
	// Embed the full raw map so we can re-marshal into CassetteEvent.
	json.RawMessage
}

// Load opens the cassette identified by name (<baseDir>/<name>.jsonl), reads
// all lines and appends non-header events to the internal slice. name undergoes
// the same validation as FileCassetteRecorder.Start. After a successful Load
// the cursor is positioned at the first event.
func (p *FileCassettePlayer) Load(ctx context.Context, name string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if err := validateCassetteName(name); err != nil {
		return err
	}

	path := filepath.Join(p.baseDir, name+".jsonl")
	f, err := os.Open(path)
	if err != nil {
		return brainerrors.New(brainerrors.CodeRecordNotFound,
			brainerrors.WithMessage(fmt.Sprintf(
				"FileCassettePlayer.Load: cannot open cassette %q: %v", path, err)))
	}
	defer f.Close()

	var events []CassetteEvent
	scanner := bufio.NewScanner(f)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		raw := scanner.Bytes()
		if len(raw) == 0 {
			continue
		}

		// First, extract the "type" discriminator.
		var peek struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(raw, &peek); err != nil {
			return brainerrors.New(brainerrors.CodeRecordNotFound,
				brainerrors.WithMessage(fmt.Sprintf(
					"FileCassettePlayer.Load: JSON parse error at line %d: %v", lineNo, err)))
		}

		// Skip the header line.
		if peek.Type == "header" {
			continue
		}

		var ev CassetteEvent
		if err := json.Unmarshal(raw, &ev); err != nil {
			return brainerrors.New(brainerrors.CodeRecordNotFound,
				brainerrors.WithMessage(fmt.Sprintf(
					"FileCassettePlayer.Load: cannot unmarshal CassetteEvent at line %d: %v",
					lineNo, err)))
		}
		events = append(events, ev)
	}
	if err := scanner.Err(); err != nil {
		return brainerrors.New(brainerrors.CodeRecordNotFound,
			brainerrors.WithMessage(fmt.Sprintf(
				"FileCassettePlayer.Load: scanner error reading %q: %v", path, err)))
	}

	p.events = events
	p.cursor = 0
	p.loaded = true
	return nil
}

// Next returns the next recorded CassetteEvent in cassette order and advances
// the cursor. Returns CodeInvariantViolated if Load has not been called yet.
// Returns CodeRecordNotFound with detail "cassette.eof" when the cassette is
// exhausted per 25-测试策略.md §11.2.
func (p *FileCassettePlayer) Next(ctx context.Context) (CassetteEvent, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.loaded {
		return CassetteEvent{}, brainerrors.New(brainerrors.CodeInvariantViolated,
			brainerrors.WithMessage("FileCassettePlayer.Next: cassette not loaded; call Load first"))
	}
	if p.cursor >= len(p.events) {
		return CassetteEvent{}, brainerrors.New(brainerrors.CodeRecordNotFound,
			brainerrors.WithMessage("cassette.eof"))
	}
	ev := p.events[p.cursor]
	p.cursor++
	return ev, nil
}

// Rewind resets the cursor to the first event. It does NOT reload from disk
// per the CassettePlayer interface contract.
func (p *FileCassettePlayer) Rewind(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.cursor = 0
	return nil
}
