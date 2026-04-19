package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// EditFileTool 精确替换文件内容。old_string 必须在文件中唯一（除非 replace_all=true）。
// 相比 write_file，避免整文件重写，节省 token 并保留未改动区域的历史真实性。
type EditFileTool struct {
	brainKind string
}

func NewEditFileTool(brainKind string) *EditFileTool {
	return &EditFileTool{brainKind: brainKind}
}

func (t *EditFileTool) Name() string { return t.brainKind + ".edit_file" }
func (t *EditFileTool) Risk() Risk   { return RiskMedium }

func (t *EditFileTool) Schema() Schema {
	return Schema{
		Name: t.Name(),
		Description: "Edit a file by replacing exact text matches. " +
			"old_string must match exactly (including whitespace) and must be unique in the file " +
			"unless replace_all=true. Use this instead of write_file for small edits — it's safer " +
			"and much cheaper in tokens.",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "path":        { "type": "string", "description": "File path to edit" },
    "old_string":  { "type": "string", "description": "Exact text to replace (must be unique unless replace_all=true)" },
    "new_string":  { "type": "string", "description": "Replacement text" },
    "replace_all": { "type": "boolean", "description": "Replace every occurrence (default false)" }
  },
  "required": ["path", "old_string", "new_string"]
}`),
		OutputSchema: editFileOutputSchema,
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

type editFileInput struct {
	Path       string `json:"path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all"`
}

type editFileOutput struct {
	Path         string `json:"path"`
	Replacements int    `json:"replacements"`
	BytesWritten int    `json:"bytes_written"`
}

func (t *EditFileTool) Execute(ctx context.Context, args json.RawMessage) (*Result, error) {
	var input editFileInput
	if err := json.Unmarshal(args, &input); err != nil {
		return &Result{Output: jsonStr(fmt.Sprintf("invalid arguments: %v", err)), IsError: true}, nil
	}
	if input.Path == "" {
		return &Result{Output: jsonStr("path is required"), IsError: true}, nil
	}
	if input.OldString == "" {
		return &Result{Output: jsonStr("old_string is required (cannot be empty)"), IsError: true}, nil
	}
	if input.OldString == input.NewString {
		return &Result{Output: jsonStr("new_string must differ from old_string"), IsError: true}, nil
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

	raw, err := os.ReadFile(absPath)
	if err != nil {
		return &Result{Output: jsonStr(fmt.Sprintf("read failed: %v", err)), IsError: true}, nil
	}
	content := string(raw)

	count := strings.Count(content, input.OldString)
	if count == 0 {
		// 给 LLM 可操作的失败信息：找不到时说明最可能原因 + 最相近行提示
		diag := diagnoseEditMiss(content, input.OldString, absPath)
		raw, _ := json.Marshal(diag)
		return &Result{Output: raw, IsError: true}, nil
	}
	if count > 1 && !input.ReplaceAll {
		return &Result{Output: jsonStr(fmt.Sprintf(
			"old_string matches %d occurrences in %s; provide more context to make it unique, or set replace_all=true",
			count, absPath)), IsError: true}, nil
	}

	var updated string
	if input.ReplaceAll {
		updated = strings.ReplaceAll(content, input.OldString, input.NewString)
	} else {
		updated = strings.Replace(content, input.OldString, input.NewString, 1)
	}

	data := []byte(updated)
	if err := os.WriteFile(absPath, data, 0644); err != nil {
		return &Result{Output: jsonStr(fmt.Sprintf("write failed: %v", err)), IsError: true}, nil
	}

	replacements := 1
	if input.ReplaceAll {
		replacements = count
	}

	out := editFileOutput{
		Path:         absPath,
		Replacements: replacements,
		BytesWritten: len(data),
	}
	rawOut, _ := json.Marshal(out)
	return &Result{Output: rawOut}, nil
}

// diagnoseEditMiss 在 old_string 找不到时给 LLM 可操作的诊断。
// 返回 JSON 对象，包含：
//   - error: 主要错误信息
//   - path
//   - hints: 可能的原因列表（空格/CRLF/BOM/大小写）
//   - similar_lines: 与 old_string 首行最相近的 3 行（带行号）
func diagnoseEditMiss(content, oldString, path string) map[string]interface{} {
	firstLine := oldString
	if idx := strings.Index(firstLine, "\n"); idx >= 0 {
		firstLine = firstLine[:idx]
	}
	firstLine = strings.TrimSpace(firstLine)

	hints := []string{}
	if strings.ContainsRune(oldString, '\r') {
		hints = append(hints, "old_string contains \\r (CRLF). File may use LF-only; try removing \\r.")
	}
	if strings.Contains(content, "\r\n") && !strings.Contains(oldString, "\r\n") {
		hints = append(hints, "file uses CRLF line endings but old_string uses LF. Try adding \\r\\n.")
	}
	if strings.HasPrefix(content, "\uFEFF") {
		hints = append(hints, "file starts with UTF-8 BOM; if matching beginning of file, include BOM or skip it.")
	}
	if strings.Contains(content, strings.ToLower(oldString)) || strings.Contains(strings.ToLower(content), strings.ToLower(oldString)) {
		hints = append(hints, "case-insensitive match exists; your old_string is case-sensitive.")
	}
	// 去掉前后空白后能找到？
	trimmed := strings.TrimSpace(oldString)
	if trimmed != oldString && trimmed != "" && strings.Contains(content, trimmed) {
		hints = append(hints, "match found if leading/trailing whitespace is trimmed; check your indentation.")
	}

	// 相似行：按最长公共子串长度排序
	similar := findSimilarLines(content, firstLine, 3)

	return map[string]interface{}{
		"error":         fmt.Sprintf("old_string not found in %s", path),
		"path":          path,
		"hints":         hints,
		"similar_lines": similar,
	}
}

// findSimilarLines 返回文件中与 target 最相近的 maxN 行，包含行号和文本。
// 评分标准：共享单词数 + 字符前缀匹配长度。
func findSimilarLines(content, target string, maxN int) []map[string]interface{} {
	if target == "" || len(content) == 0 {
		return nil
	}
	targetLower := strings.ToLower(target)
	targetWords := strings.Fields(targetLower)
	type scored struct {
		lineNum int
		text    string
		score   int
	}
	var candidates []scored
	for i, line := range strings.Split(content, "\n") {
		lower := strings.ToLower(strings.TrimSpace(line))
		if lower == "" {
			continue
		}
		score := 0
		for _, w := range targetWords {
			if len(w) >= 3 && strings.Contains(lower, w) {
				score++
			}
		}
		// 前缀匹配加权
		prefix := commonPrefixLen(lower, targetLower)
		score += prefix / 4
		if score > 0 {
			candidates = append(candidates, scored{lineNum: i + 1, text: line, score: score})
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].score > candidates[j].score })
	if maxN > len(candidates) {
		maxN = len(candidates)
	}
	out := make([]map[string]interface{}, 0, maxN)
	for i := 0; i < maxN; i++ {
		out = append(out, map[string]interface{}{
			"line": candidates[i].lineNum,
			"text": truncateForDiag(candidates[i].text, 200),
		})
	}
	return out
}

func commonPrefixLen(a, b string) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

func truncateForDiag(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
