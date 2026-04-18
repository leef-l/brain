package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// WriteFileTool writes content to a file on the local filesystem.
type WriteFileTool struct {
	brainKind string
}

// NewWriteFileTool constructs a WriteFileTool for the given brain kind.
func NewWriteFileTool(brainKind string) *WriteFileTool {
	return &WriteFileTool{brainKind: brainKind}
}

func (t *WriteFileTool) Name() string { return t.brainKind + ".write_file" }

func (t *WriteFileTool) Schema() Schema {
	return Schema{
		Name:        t.Name(),
		Description: "Write content to a file. Creates parent directories if needed. Overwrites existing content.",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "path": {
      "type": "string",
      "description": "Absolute or relative path to the file to write"
    },
    "content": {
      "type": "string",
      "description": "The text content to write to the file"
    }
  },
  "required": ["path", "content"]
}`),
		OutputSchema: writeFileOutputSchema,
		Brain:        t.brainKind,
		Concurrency: &ToolConcurrencySpec{
			Capability:          "file.write",
			ResourceKeyTemplate: "file:{{path}}",
			AccessMode:          "exclusive-write",
			Scope:               "turn",
			ApprovalClass:       "workspace-write",
		},
	}
}

func (t *WriteFileTool) Risk() Risk { return RiskMedium }

type writeFileInput struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type writeFileOutput struct {
	BytesWritten int    `json:"bytes_written"`
	Path         string `json:"path"`
}

func (t *WriteFileTool) Execute(ctx context.Context, args json.RawMessage) (*Result, error) {
	var input writeFileInput
	if err := json.Unmarshal(args, &input); err != nil {
		return &Result{Output: jsonStr(fmt.Sprintf("invalid arguments: %v", err)), IsError: true}, nil
	}
	if input.Path == "" {
		return &Result{Output: jsonStr("path is required"), IsError: true}, nil
	}

	// Security: reject sensitive paths.
	if isSensitivePath(input.Path) {
		return &Result{Output: jsonStr("access denied: sensitive path"), IsError: true}, nil
	}

	// Resolve to absolute path.
	absPath, err := filepath.Abs(input.Path)
	if err != nil {
		return &Result{Output: jsonStr(fmt.Sprintf("invalid path: %v", err)), IsError: true}, nil
	}

	// Reject writing to system directories.
	if isSystemDir(absPath) {
		return &Result{Output: jsonStr("access denied: system directory"), IsError: true}, nil
	}

	// Create parent directories.
	dir := filepath.Dir(absPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return &Result{Output: jsonStr(fmt.Sprintf("mkdir failed: %v", err)), IsError: true}, nil
	}

	// Write the file.
	data := []byte(input.Content)
	if err := os.WriteFile(absPath, data, 0644); err != nil {
		return &Result{Output: jsonStr(fmt.Sprintf("write failed: %v", err)), IsError: true}, nil
	}

	out := writeFileOutput{
		BytesWritten: len(data),
		Path:         absPath,
	}
	raw, _ := json.Marshal(out)
	return &Result{Output: raw}, nil
}

// systemDirPrefixes are directories that tools must never write to.
var systemDirPrefixes = []string{
	"/etc",
	"/usr",
	"/bin",
	"/sbin",
	"/boot",
	"/proc",
	"/sys",
	"/dev",
}

func isSystemDir(absPath string) bool {
	for _, prefix := range systemDirPrefixes {
		if absPath == prefix || len(absPath) > len(prefix) && absPath[:len(prefix)+1] == prefix+"/" {
			return true
		}
	}
	return false
}
