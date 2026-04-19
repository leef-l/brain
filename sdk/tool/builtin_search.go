package tool

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// SearchTool searches for patterns in files recursively.
type SearchTool struct {
	brainKind string
}

// NewSearchTool constructs a SearchTool for the given brain kind.
func NewSearchTool(brainKind string) *SearchTool {
	return &SearchTool{brainKind: brainKind}
}

func (t *SearchTool) Name() string { return t.brainKind + ".search" }

func (t *SearchTool) Schema() Schema {
	return Schema{
		Name:        t.Name(),
		Description: "Search for a pattern in files recursively. Returns matching lines with file paths and line numbers. Supports context lines (-B/-A) and multiline matching.",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "pattern": {
      "type": "string",
      "description": "Search pattern (substring match or regex if is_regex is true)"
    },
    "path": {
      "type": "string",
      "description": "Directory to search in. Default: current directory"
    },
    "glob": {
      "type": "string",
      "description": "File glob pattern to filter files (e.g. '*.go', '*.py'). Default: all files"
    },
    "is_regex": {
      "type": "boolean",
      "description": "Treat pattern as a regular expression. Default: false"
    },
    "max_results": {
      "type": "integer",
      "description": "Maximum number of matches to return. Default: 50"
    },
    "context_before": {
      "type": "integer",
      "description": "Lines of context before each match (ripgrep -B). Default: 0"
    },
    "context_after": {
      "type": "integer",
      "description": "Lines of context after each match (ripgrep -A). Default: 0"
    },
    "multiline": {
      "type": "boolean",
      "description": "Enable multiline regex (pattern may span newlines, requires is_regex=true). Default: false"
    }
  },
  "required": ["pattern"]
}`),
		OutputSchema: searchOutputSchema,
		Brain:        t.brainKind,
		Concurrency: &ToolConcurrencySpec{
			Capability:          "code.search",
			ResourceKeyTemplate: "fs:*",
			AccessMode:          "shared-read",
			Scope:               "turn",
			ApprovalClass:       "readonly",
		},
	}
}

func (t *SearchTool) Risk() Risk { return RiskSafe }

type searchInput struct {
	Pattern       string `json:"pattern"`
	Path          string `json:"path"`
	Glob          string `json:"glob"`
	IsRegex       bool   `json:"is_regex"`
	MaxResults    int    `json:"max_results"`
	ContextBefore int    `json:"context_before"`
	ContextAfter  int    `json:"context_after"`
	Multiline     bool   `json:"multiline"`
}

type searchMatch struct {
	File    string   `json:"file"`
	Line    int      `json:"line"`
	Text    string   `json:"text"`
	Before  []string `json:"before,omitempty"`
	After   []string `json:"after,omitempty"`
}

type searchOutput struct {
	Matches   []searchMatch `json:"matches"`
	Total     int           `json:"total"`
	Truncated bool          `json:"truncated"`
	// Backend 标注实际使用的搜索后端（ripgrep / walk）。两后端对 glob 语义
	// 和 skip 规则有差异，LLM 需要这个信号来判断结果可比性。
	Backend string `json:"backend,omitempty"`
	// Notes 列出非致命但影响结果的副作用（大文件跳过、glob 语义差异等），
	// 便于 LLM 做失败恢复或二次查询。
	Notes []string `json:"notes,omitempty"`
}

// skipDirs are directories to always skip during search.
var skipDirs = map[string]bool{
	".git":         true,
	"vendor":       true,
	"node_modules": true,
	".idea":        true,
	".vscode":      true,
	"__pycache__":  true,
	".mypy_cache":  true,
	".tox":         true,
}

var (
	rgLookPath = exec.LookPath
)

var rgSkipGlobs = []string{
	"!.git/**", "!**/.git/**",
	"!vendor/**", "!**/vendor/**",
	"!node_modules/**", "!**/node_modules/**",
	"!.idea/**", "!**/.idea/**",
	"!.vscode/**", "!**/.vscode/**",
	"!__pycache__/**", "!**/__pycache__/**",
	"!.mypy_cache/**", "!**/.mypy_cache/**",
	"!.tox/**", "!**/.tox/**",
}

type ripgrepEvent struct {
	Type string `json:"type"`
	Data struct {
		Path struct {
			Text string `json:"text"`
		} `json:"path"`
		Lines struct {
			Text string `json:"text"`
		} `json:"lines"`
		LineNumber int `json:"line_number"`
	} `json:"data"`
}

func (t *SearchTool) Execute(ctx context.Context, args json.RawMessage) (*Result, error) {
	var input searchInput
	if err := json.Unmarshal(args, &input); err != nil {
		return &Result{Output: jsonStr(fmt.Sprintf("invalid arguments: %v", err)), IsError: true}, nil
	}
	if input.Pattern == "" {
		return &Result{Output: jsonStr("pattern is required"), IsError: true}, nil
	}
	if input.Path == "" {
		input.Path = "."
	}
	if input.MaxResults <= 0 {
		input.MaxResults = 50
	}

	// Compile regex if needed.
	var re *regexp.Regexp
	if input.IsRegex {
		var err error
		pattern := input.Pattern
		if input.Multiline {
			// (?s) — DOTALL 让 . 匹配换行；walk 路径按整个文件全文匹配
			pattern = "(?s)" + pattern
		}
		re, err = regexp.Compile(pattern)
		if err != nil {
			return &Result{Output: jsonStr(fmt.Sprintf("invalid regex: %v", err)), IsError: true}, nil
		}
	}

	absPath, err := filepath.Abs(input.Path)
	if err != nil {
		return &Result{Output: jsonStr(fmt.Sprintf("invalid path: %v", err)), IsError: true}, nil
	}

	if isSensitivePath(absPath) {
		return &Result{Output: jsonStr("access denied: sensitive path"), IsError: true}, nil
	}

	out, usedRG, err := t.searchWithRipgrep(ctx, absPath, input)
	if !usedRG {
		out, err = t.searchWithWalk(ctx, absPath, input, re)
		out.Backend = "walk"
		out.Notes = append(out.Notes,
			"ripgrep not found; using slower walk backend. Install ripgrep for faster search and richer glob semantics.",
			"walk backend: glob matches basename only (not path); files >1MB are skipped.")
	} else {
		out.Backend = "ripgrep"
		// ripgrep 也会跳过 >1MB 文件（--max-filesize 1M），给个提示
		out.Notes = append(out.Notes, "ripgrep backend: files >1MB are skipped; binary files auto-detected.")
	}

	if err != nil && ctx.Err() == nil {
		return &Result{Output: jsonStr(fmt.Sprintf("search error: %v", err)), IsError: true}, nil
	}
	if out.Matches == nil {
		out.Matches = []searchMatch{}
	}
	raw, _ := json.Marshal(out)
	return &Result{Output: raw}, nil
}

func (t *SearchTool) searchWithRipgrep(ctx context.Context, absPath string, input searchInput) (searchOutput, bool, error) {
	rgPath, err := rgLookPath("rg")
	if err != nil {
		return searchOutput{}, false, nil
	}

	info, err := os.Stat(absPath)
	if err != nil {
		return searchOutput{}, true, err
	}

	var (
		searchDir  = absPath
		searchPath = "."
	)
	if !info.IsDir() {
		searchDir = filepath.Dir(absPath)
		searchPath = filepath.Base(absPath)
	}

	cmd := exec.CommandContext(ctx, rgPath, buildRipgrepArgs(input, searchPath)...)
	cmd.Dir = searchDir

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return searchOutput{}, true, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return searchOutput{}, true, err
	}

	var stderrBuf bytes.Buffer
	if err := cmd.Start(); err != nil {
		return searchOutput{}, true, err
	}

	stderrDone := make(chan error, 1)
	go func() {
		_, copyErr := io.Copy(&stderrBuf, stderr)
		stderrDone <- copyErr
	}()

	out, parseErr := parseRipgrepOutput(stdout, searchDir, input.MaxResults)
	waitErr := cmd.Wait()
	copyErr := <-stderrDone

	if parseErr != nil {
		return out, true, parseErr
	}
	if copyErr != nil && !errors.Is(copyErr, os.ErrClosed) {
		return out, true, copyErr
	}
	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) && exitErr.ExitCode() == 1 {
			return out, true, nil
		}
		if ctx.Err() != nil {
			return out, true, ctx.Err()
		}
		msg := strings.TrimSpace(stderrBuf.String())
		if msg == "" {
			msg = waitErr.Error()
		}
		return out, true, fmt.Errorf("ripgrep failed: %s", msg)
	}
	return out, true, nil
}

func buildRipgrepArgs(input searchInput, searchPath string) []string {
	args := []string{
		"--json",
		"--line-number",
		"--hidden",
		"--no-ignore",
		"--no-messages",
		"--max-filesize", "1M",
	}
	for _, glob := range rgSkipGlobs {
		args = append(args, "-g", glob)
	}
	if input.Glob != "" {
		args = append(args, "-g", input.Glob)
	}
	if input.ContextBefore > 0 {
		args = append(args, "-B", fmt.Sprintf("%d", input.ContextBefore))
	}
	if input.ContextAfter > 0 {
		args = append(args, "-A", fmt.Sprintf("%d", input.ContextAfter))
	}
	if input.Multiline && input.IsRegex {
		args = append(args, "-U", "--multiline-dotall")
	}
	if input.IsRegex {
		args = append(args, "-e", input.Pattern)
	} else {
		args = append(args, "-F", "-e", input.Pattern)
	}
	args = append(args, searchPath)
	return args
}

func parseRipgrepOutput(r io.Reader, searchDir string, maxResults int) (searchOutput, error) {
	var out searchOutput
	out.Matches = []searchMatch{}

	// ripgrep 输出顺序：对每个文件，按行号交织 context / match / context 事件。
	// 我们累积 "上一次 match 之前缓冲的 context 行"作为该 match 的 before；
	// 一旦遇到新 match，把上一次 match 的 after 填完。这样也不用重开窗口。
	pendingBefore := []string{} // 在下一个 match 出现前累积的 context 行
	var lastMatchIdx = -1        // 指向 out.Matches 中最近一次 match 的下标（已提交）
	var lastMatchFile string

	flushAfter := func() {
		pendingBefore = pendingBefore[:0]
		lastMatchIdx = -1
		lastMatchFile = ""
	}

	dec := json.NewDecoder(bufio.NewReader(r))
	for {
		var ev ripgrepEvent
		if err := dec.Decode(&ev); err != nil {
			if errors.Is(err, io.EOF) {
				return out, nil
			}
			return out, err
		}
		switch ev.Type {
		case "begin":
			// 新文件开始：重置 pending/last 状态
			flushAfter()
		case "end":
			flushAfter()
		case "context":
			file := normalizeRipgrepPath(searchDir, ev.Data.Path.Text)
			text := truncateSearchLine(ev.Data.Lines.Text)
			if lastMatchIdx >= 0 && file == lastMatchFile {
				// 属于最近一次 match 的 after
				out.Matches[lastMatchIdx].After = append(out.Matches[lastMatchIdx].After, text)
			} else {
				// 作为下一个 match 的 before
				pendingBefore = append(pendingBefore, text)
			}
		case "match":
			out.Total++
			if len(out.Matches) >= maxResults {
				out.Truncated = true
				continue
			}
			file := normalizeRipgrepPath(searchDir, ev.Data.Path.Text)
			text := truncateSearchLine(ev.Data.Lines.Text)
			m := searchMatch{
				File: file,
				Line: ev.Data.LineNumber,
				Text: text,
			}
			if len(pendingBefore) > 0 {
				m.Before = append([]string(nil), pendingBefore...)
				pendingBefore = pendingBefore[:0]
			}
			out.Matches = append(out.Matches, m)
			lastMatchIdx = len(out.Matches) - 1
			lastMatchFile = file
		}
	}
}

func normalizeRipgrepPath(searchDir, path string) string {
	if path == "" {
		return path
	}
	if filepath.IsAbs(path) {
		if rel, err := filepath.Rel(searchDir, path); err == nil && rel != "" {
			path = rel
		}
	}
	path = filepath.Clean(path)
	return strings.TrimPrefix(path, "."+string(filepath.Separator))
}

func truncateSearchLine(line string) string {
	line = strings.TrimRight(line, "\r\n")
	if len(line) > 200 {
		return line[:200] + "..."
	}
	return strings.TrimRight(line, "\r")
}

func (t *SearchTool) searchWithWalk(ctx context.Context, absPath string, input searchInput, re *regexp.Regexp) (searchOutput, error) {
	var (
		out     searchOutput
		rootDir = absPath
	)
	if info, err := os.Stat(absPath); err == nil && !info.IsDir() {
		rootDir = filepath.Dir(absPath)
	}

	err := filepath.Walk(absPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}

		if info.IsDir() {
			if skipDirs[info.Name()] {
				return filepath.SkipDir
			}
			return nil
		}

		if input.Glob != "" {
			matched, _ := filepath.Match(input.Glob, info.Name())
			if !matched {
				return nil
			}
		}

		if info.Size() > 1<<20 {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}

		if len(data) > 0 {
			sample := data
			if len(sample) > 512 {
				sample = sample[:512]
			}
			for _, b := range sample {
				if b == 0 {
					return nil
				}
			}
		}

		relPath, _ := filepath.Rel(rootDir, path)
		if relPath == "" {
			relPath = path
		}

		lines := strings.Split(string(data), "\n")
		for i, line := range lines {
			var found bool
			if re != nil {
				found = re.MatchString(line)
			} else {
				found = strings.Contains(line, input.Pattern)
			}
			if !found {
				continue
			}

			out.Total++
			if len(out.Matches) >= input.MaxResults {
				out.Truncated = true
				continue
			}
			m := searchMatch{
				File: relPath,
				Line: i + 1,
				Text: truncateSearchLine(line),
			}
			// 收集上下文
			if input.ContextBefore > 0 {
				start := i - input.ContextBefore
				if start < 0 {
					start = 0
				}
				for j := start; j < i; j++ {
					m.Before = append(m.Before, truncateSearchLine(lines[j]))
				}
			}
			if input.ContextAfter > 0 {
				end := i + 1 + input.ContextAfter
				if end > len(lines) {
					end = len(lines)
				}
				for j := i + 1; j < end; j++ {
					m.After = append(m.After, truncateSearchLine(lines[j]))
				}
			}
			out.Matches = append(out.Matches, m)
		}
		return nil
	})

	return out, err
}
