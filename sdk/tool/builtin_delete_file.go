package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type DeleteFileTool struct {
	brainKind string
}

func NewDeleteFileTool(brainKind string) *DeleteFileTool {
	return &DeleteFileTool{brainKind: brainKind}
}

func (t *DeleteFileTool) Name() string { return t.brainKind + ".delete_file" }

func (t *DeleteFileTool) Schema() Schema {
	return Schema{
		Name:        t.Name(),
		Description: "Delete a file from the local filesystem. Directories are not supported.",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "path": {
      "type": "string",
      "description": "Absolute or relative path to the file to delete"
    }
  },
  "required": ["path"]
}`),
		OutputSchema: deleteFileOutputSchema,
		Brain:        t.brainKind,
		Concurrency: &ToolConcurrencySpec{
			Capability:          "file.delete",
			ResourceKeyTemplate: "file:{{path}}",
			AccessMode:          "exclusive-write",
			Scope:               "turn",
			ApprovalClass:       "workspace-write",
		},
	}
}

func (t *DeleteFileTool) Risk() Risk { return RiskHigh }

func (t *DeleteFileTool) Execute(ctx context.Context, args json.RawMessage) (*Result, error) {
	var input struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return &Result{Output: jsonStr(fmt.Sprintf("invalid arguments: %v", err)), IsError: true}, nil
	}
	if input.Path == "" {
		return &Result{Output: jsonStr("path is required"), IsError: true}, nil
	}
	if isSensitivePath(input.Path) {
		return &Result{Output: jsonStr("access denied: sensitive path"), IsError: true}, nil
	}

	absPath, err := filepath.Abs(input.Path)
	if err != nil {
		return &Result{Output: jsonStr(fmt.Sprintf("invalid path: %v", err)), IsError: true}, nil
	}
	if isSystemDir(absPath) {
		return &Result{Output: jsonStr("access denied: system directory"), IsError: true}, nil
	}

	info, err := os.Lstat(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &Result{Output: jsonStr("path does not exist"), IsError: true}, nil
		}
		return &Result{Output: jsonStr(fmt.Sprintf("stat failed: %v", err)), IsError: true}, nil
	}
	if info.IsDir() {
		return &Result{Output: jsonStr("delete_file only supports files"), IsError: true}, nil
	}
	if err := os.Remove(absPath); err != nil {
		return &Result{Output: jsonStr(fmt.Sprintf("delete failed: %v", err)), IsError: true}, nil
	}

	raw, _ := json.Marshal(map[string]interface{}{
		"deleted": true,
		"path":    absPath,
	})
	return &Result{Output: raw}, nil
}
