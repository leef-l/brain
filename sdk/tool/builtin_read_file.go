package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ReadFileTool reads the content of a file from the local filesystem.
// It supports offset/limit for large files and detects binary content.
type ReadFileTool struct {
	brainKind string
}

// NewReadFileTool constructs a ReadFileTool for the given brain kind.
func NewReadFileTool(brainKind string) *ReadFileTool {
	return &ReadFileTool{brainKind: brainKind}
}

func (t *ReadFileTool) Name() string { return t.brainKind + ".read_file" }

func (t *ReadFileTool) Schema() Schema {
	return Schema{
		Name:        t.Name(),
		Description: "Read the content of a file. Returns the text content with line count. Supports offset and limit for large files.",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "path": {
      "type": "string",
      "description": "Absolute or relative path to the file to read"
    },
    "offset": {
      "type": "integer",
      "description": "Line number to start reading from (0-based). Default: 0"
    },
    "limit": {
      "type": "integer",
      "description": "Maximum number of lines to read. Default: 2000"
    }
  },
  "required": ["path"]
}`),
		OutputSchema: readFileOutputSchema,
		Brain:        t.brainKind,
		Concurrency: &ToolConcurrencySpec{
			Capability:          "file.read",
			ResourceKeyTemplate: "file:{{path}}",
			AccessMode:          "shared-read",
			Scope:               "turn",
			ApprovalClass:       "readonly",
		},
	}
}

func (t *ReadFileTool) Risk() Risk { return RiskSafe }

type readFileInput struct {
	Path   string `json:"path"`
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"`
}

type readFileOutput struct {
	Content    string `json:"content"`
	Lines      int    `json:"lines"`
	TotalLines int    `json:"total_lines"`
	Truncated  bool   `json:"truncated"`
	Path       string `json:"path"`
}

func (t *ReadFileTool) Execute(ctx context.Context, args json.RawMessage) (*Result, error) {
	var input readFileInput
	if err := json.Unmarshal(args, &input); err != nil {
		return &Result{Output: jsonStr(fmt.Sprintf("invalid arguments: %v", err)), IsError: true}, nil
	}
	if input.Path == "" {
		return &Result{Output: jsonStr("path is required"), IsError: true}, nil
	}
	if input.Limit <= 0 {
		input.Limit = 2000
	}

	return ReadFileCore(ctx, input.Path, input.Offset, input.Limit)
}

// ReadFileCore is the shared implementation for reading files. It is used by
// both code.read_file and verifier.read_file.
func ReadFileCore(_ context.Context, path string, offset, limit int) (*Result, error) {
	// Security: reject sensitive paths.
	if isSensitivePath(path) {
		return &Result{Output: jsonStr("access denied: sensitive path"), IsError: true}, nil
	}

	// Resolve to absolute path.
	absPath, err := filepath.Abs(path)
	if err != nil {
		return &Result{Output: jsonStr(fmt.Sprintf("invalid path: %v", err)), IsError: true}, nil
	}

	// Check file exists.
	info, err := os.Stat(absPath)
	if err != nil {
		return &Result{Output: jsonStr(fmt.Sprintf("file not found: %v", err)), IsError: true}, nil
	}
	if info.IsDir() {
		return &Result{Output: jsonStr("path is a directory, not a file"), IsError: true}, nil
	}

	// Read the file.
	data, err := os.ReadFile(absPath)
	if err != nil {
		return &Result{Output: jsonStr(fmt.Sprintf("read error: %v", err)), IsError: true}, nil
	}

	// Binary detection: check first 512 bytes for NUL.
	sample := data
	if len(sample) > 512 {
		sample = sample[:512]
	}
	if bytes.Contains(sample, []byte{0}) {
		out := readFileOutput{
			Content: fmt.Sprintf("(binary file, %d bytes)", len(data)),
			Lines:   0,
			Path:    absPath,
		}
		raw, _ := json.Marshal(out)
		return &Result{Output: raw}, nil
	}

	// Split into lines.
	allLines := strings.Split(string(data), "\n")
	totalLines := len(allLines)

	// Apply offset and limit.
	if offset < 0 {
		offset = 0
	}
	if offset > len(allLines) {
		offset = len(allLines)
	}
	end := offset + limit
	if end > len(allLines) {
		end = len(allLines)
	}
	selectedLines := allLines[offset:end]
	truncated := end < totalLines

	content := strings.Join(selectedLines, "\n")

	out := readFileOutput{
		Content:    content,
		Lines:      len(selectedLines),
		TotalLines: totalLines,
		Truncated:  truncated,
		Path:       absPath,
	}
	raw, _ := json.Marshal(out)
	return &Result{Output: raw}, nil
}

// sensitivePathPrefixes are paths that tools must never read or write.
var sensitivePathPrefixes = []string{
	"/.ssh",
	"/.gnupg",
	"/.aws/credentials",
	"/etc/shadow",
	"/etc/passwd",
	"/etc/sudoers",
}

func isSensitivePath(path string) bool {
	abs, err := filepath.Abs(path)
	if err != nil {
		return true // reject if can't resolve
	}
	home, _ := os.UserHomeDir()

	for _, prefix := range sensitivePathPrefixes {
		// Check both absolute and relative to home.
		if strings.HasPrefix(abs, prefix) {
			return true
		}
		if home != "" && strings.HasPrefix(abs, filepath.Join(home, prefix)) {
			return true
		}
	}
	return false
}

// jsonStr creates a JSON-encoded string value.
func jsonStr(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return b
}
