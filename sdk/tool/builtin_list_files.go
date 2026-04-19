package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// ListFilesTool 按 glob 模式列出文件路径。
// 专注于"找文件"（返回路径列表），不读取内容；用于让 LLM 快速感知目录结构。
type ListFilesTool struct {
	brainKind string
}

func NewListFilesTool(brainKind string) *ListFilesTool {
	return &ListFilesTool{brainKind: brainKind}
}

func (t *ListFilesTool) Name() string { return t.brainKind + ".list_files" }
func (t *ListFilesTool) Risk() Risk   { return RiskLow }

func (t *ListFilesTool) Schema() Schema {
	return Schema{
		Name: t.Name(),
		Description: "List files matching a glob pattern. Supports ** for recursive. " +
			"Examples: '*.go' (top level), '**/*.go' (all Go files), 'src/**/*.ts'. " +
			"Returns sorted paths without reading contents.",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "pattern":     { "type": "string", "description": "Glob pattern, e.g. '**/*.go'" },
    "root":        { "type": "string", "description": "Directory to search in (default: current working directory)" },
    "max_results": { "type": "integer", "description": "Max paths returned (default 500, max 5000)" },
    "include_dirs": { "type": "boolean", "description": "Include directories in results (default false)" }
  },
  "required": ["pattern"]
}`),
		OutputSchema: listFilesOutputSchema,
		Brain:        t.brainKind,
	}
}

type listFilesInput struct {
	Pattern     string `json:"pattern"`
	Root        string `json:"root"`
	MaxResults  int    `json:"max_results"`
	IncludeDirs bool   `json:"include_dirs"`
}

type listFilesOutput struct {
	Paths     []string `json:"paths"`
	Count     int      `json:"count"`
	Truncated bool     `json:"truncated"`
}

func (t *ListFilesTool) Execute(ctx context.Context, args json.RawMessage) (*Result, error) {
	var input listFilesInput
	if err := json.Unmarshal(args, &input); err != nil {
		return &Result{Output: jsonStr(fmt.Sprintf("invalid arguments: %v", err)), IsError: true}, nil
	}
	if input.Pattern == "" {
		return &Result{Output: jsonStr("pattern is required"), IsError: true}, nil
	}

	maxResults := input.MaxResults
	if maxResults <= 0 {
		maxResults = 500
	}
	if maxResults > 5000 {
		maxResults = 5000
	}

	root := input.Root
	if root == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return &Result{Output: jsonStr(fmt.Sprintf("cwd: %v", err)), IsError: true}, nil
		}
		root = cwd
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return &Result{Output: jsonStr(fmt.Sprintf("abs root: %v", err)), IsError: true}, nil
	}

	pattern := normalizeGlobPattern(input.Pattern)
	if err := validateGlobPattern(pattern); err != nil {
		return &Result{Output: jsonStr(fmt.Sprintf("invalid pattern: %v", err)), IsError: true}, nil
	}

	var matches []string
	truncated := false
	walkErr := filepath.WalkDir(absRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// 跳过敏感路径
		if isSensitivePath(p) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		// 跳过 .git 等常见噪声
		name := d.Name()
		if d.IsDir() && (name == ".git" || name == "node_modules" || name == "vendor" || name == "dist" || name == ".venv") {
			return fs.SkipDir
		}
		if !input.IncludeDirs && d.IsDir() {
			return nil
		}

		rel, relErr := filepath.Rel(absRoot, p)
		if relErr != nil {
			return nil
		}
		if rel == "." {
			return nil
		}
		relSlash := filepath.ToSlash(rel)
		ok, matchErr := globMatch(pattern, relSlash)
		if matchErr != nil {
			return matchErr
		}
		if !ok {
			return nil
		}
		if len(matches) >= maxResults {
			truncated = true
			return filepath.SkipAll
		}
		matches = append(matches, relSlash)
		return nil
	})
	if walkErr != nil && !errorsIs(walkErr, filepath.SkipAll) && !errorsIs(walkErr, context.Canceled) {
		return &Result{Output: jsonStr(fmt.Sprintf("walk: %v", walkErr)), IsError: true}, nil
	}

	sort.Strings(matches)

	out := listFilesOutput{
		Paths:     matches,
		Count:     len(matches),
		Truncated: truncated,
	}
	raw, _ := json.Marshal(out)
	return &Result{Output: raw}, nil
}

// normalizeGlobPattern 统一分隔符为 /，清除多余斜杠。
func normalizeGlobPattern(p string) string {
	p = filepath.ToSlash(p)
	p = strings.TrimSpace(p)
	return p
}

// validateGlobPattern 检查每段 pattern 是否是 path.Match 可接受的语法。
// 支持 ** 跨段匹配。
func validateGlobPattern(pattern string) error {
	if pattern == "" {
		return fmt.Errorf("empty pattern")
	}
	for _, seg := range strings.Split(pattern, "/") {
		if seg == "" || seg == "**" {
			continue
		}
		if _, err := path.Match(seg, "probe"); err != nil {
			return err
		}
	}
	return nil
}

// globMatch 对 target（slash 路径）应用 pattern 匹配，支持 ** 跨段。
func globMatch(pattern, target string) (bool, error) {
	patSegs := strings.Split(pattern, "/")
	tgtSegs := strings.Split(target, "/")
	return matchSegments(patSegs, tgtSegs)
}

func matchSegments(pat, tgt []string) (bool, error) {
	for i := 0; i < len(pat); i++ {
		if pat[i] == "**" {
			// ** 消耗任意多个 target 段（包括 0 个）
			if i == len(pat)-1 {
				return true, nil
			}
			rest := pat[i+1:]
			for j := 0; j <= len(tgt); j++ {
				if ok, err := matchSegments(rest, tgt[j:]); err != nil {
					return false, err
				} else if ok {
					return true, nil
				}
			}
			return false, nil
		}
		if len(tgt) == 0 {
			return false, nil
		}
		ok, err := path.Match(pat[i], tgt[0])
		if err != nil {
			return false, err
		}
		if !ok {
			return false, nil
		}
		tgt = tgt[1:]
	}
	return len(tgt) == 0, nil
}

// errorsIs 本地辅助以避免在此文件里额外 import "errors"。
func errorsIs(err, target error) bool {
	if err == nil || target == nil {
		return err == target
	}
	for err != nil {
		if err == target {
			return true
		}
		unwrap, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrap.Unwrap()
	}
	return false
}
