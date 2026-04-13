package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
)

type diffOp struct {
	kind byte
	text string
}

type writePreviewInput struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// buildPreExecPreview generates a diff preview before a tool executes,
// used in the approval dialog so the user can see what will change.
func buildPreExecPreview(workdir, toolName string, args json.RawMessage, maxLines int) []string {
	if !strings.HasSuffix(toolName, ".write_file") || maxLines <= 0 {
		return nil
	}
	var input writePreviewInput
	if json.Unmarshal(args, &input) != nil || input.Path == "" {
		return nil
	}

	absPath := input.Path
	if !filepath.IsAbs(absPath) {
		absPath = filepath.Join(workdir, input.Path)
	}
	absPath = filepath.Clean(absPath)

	oldContent := ""
	oldExists := false
	if data, err := os.ReadFile(absPath); err == nil {
		oldExists = true
		oldContent = string(data)
	} else if !os.IsNotExist(err) {
		return []string{
			fmt.Sprintf("diff -- %s", input.Path),
			fmt.Sprintf("! unable to read existing file: %v", err),
		}
	}

	ops := diffLines(splitDiffLines(oldContent), splitDiffLines(input.Content))
	added, removed := countDiffOps(ops)
	if added == 0 && removed == 0 {
		return []string{
			fmt.Sprintf("diff -- %s", input.Path),
			"(no textual changes)",
		}
	}

	exc := excerptDiffOps(ops, maxLines-4)
	lines := []string{
		fmt.Sprintf("diff -- %s (+%d -%d)", input.Path, added, removed),
		fmt.Sprintf("--- %s", diffOldLabel(input.Path, oldExists)),
		fmt.Sprintf("+++ %s", diffNewLabel(input.Path)),
		fmt.Sprintf("@@ -%d,%d +%d,%d @@", exc.oldStart, exc.oldCount, exc.newStart, exc.newCount),
	}
	lines = append(lines, exc.lines...)
	if exc.truncated {
		lines = append(lines, "...")
	}
	return lines
}

// snapshotForTool captures the pre-execution state of a file that a tool
// is about to modify. Returns nil if the tool is not a file-modifying tool
// or the target file path cannot be determined.
func snapshotForTool(workdir, toolName string, args json.RawMessage) *fileSnapshot {
	var filePath string

	switch {
	case strings.HasSuffix(toolName, ".write_file"):
		var input writePreviewInput
		if json.Unmarshal(args, &input) != nil || input.Path == "" {
			return nil
		}
		filePath = input.Path

	case strings.HasSuffix(toolName, ".shell_exec"):
		// Try to extract target file from common file-modifying commands.
		var shellInput struct {
			Command string `json:"command"`
		}
		if json.Unmarshal(args, &shellInput) != nil {
			return nil
		}
		filePath = extractFilePathFromCommand(shellInput.Command)
		if filePath == "" {
			return nil
		}

	default:
		return nil
	}

	absPath := filePath
	if !filepath.IsAbs(absPath) {
		absPath = filepath.Join(workdir, filePath)
	}
	absPath = filepath.Clean(absPath)

	snap := &fileSnapshot{path: absPath}
	if data, err := os.ReadFile(absPath); err == nil {
		snap.oldExists = true
		snap.oldContent = string(data)
	}
	return snap
}

// extractFilePathFromCommand tries to extract the target file path from
// common file-modifying shell commands (sed -i, tee, cp, mv, rm, etc.).
func extractFilePathFromCommand(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return ""
	}

	base := filepath.Base(fields[0])

	switch base {
	case "sed":
		// sed -i 'expr' file  or  sed -i'' 'expr' file
		for i := 1; i < len(fields); i++ {
			f := fields[i]
			if f == "-i" {
				// Next might be suffix or expression. Skip to find the file.
				continue
			}
			if strings.HasPrefix(f, "-i") {
				// -i'' or -i.bak — skip
				continue
			}
			if strings.HasPrefix(f, "-") {
				continue
			}
			if strings.HasPrefix(f, "'") || strings.HasPrefix(f, "\"") || strings.HasPrefix(f, "s/") || strings.HasPrefix(f, "/") {
				continue
			}
			// This is likely the file path.
			return f
		}
		// Fallback: last field is often the file.
		if len(fields) >= 3 {
			last := fields[len(fields)-1]
			if !strings.HasPrefix(last, "-") && !strings.HasPrefix(last, "'") && !strings.HasPrefix(last, "\"") {
				return last
			}
		}

	case "rm":
		// rm [-rf] file
		for i := 1; i < len(fields); i++ {
			if !strings.HasPrefix(fields[i], "-") {
				return fields[i]
			}
		}

	case "tee":
		// tee file
		for i := 1; i < len(fields); i++ {
			if !strings.HasPrefix(fields[i], "-") {
				return fields[i]
			}
		}

	case "cp", "mv":
		// cp/mv src dest — last arg is the destination
		if len(fields) >= 3 {
			return fields[len(fields)-1]
		}
	}

	// Check for output redirection: ... > file or ... >> file
	for i, f := range fields {
		if (f == ">" || f == ">>") && i+1 < len(fields) {
			return fields[i+1]
		}
		if strings.HasPrefix(f, ">") && len(f) > 1 && f != ">>" {
			return strings.TrimLeft(f, ">")
		}
	}

	return ""
}

// buildPostExecDiff reads the current file state and compares it against
// the pre-execution snapshot to produce a unified diff.
func buildPostExecDiff(snap *fileSnapshot, maxLines int) []string {
	if snap == nil || maxLines <= 0 {
		return nil
	}

	newContent := ""
	newExists := false
	if data, err := os.ReadFile(snap.path); err == nil {
		newExists = true
		newContent = string(data)
	}

	// No change.
	if snap.oldContent == newContent && snap.oldExists == newExists {
		return nil
	}

	// File was deleted.
	if snap.oldExists && !newExists {
		ops := diffLines(splitDiffLines(snap.oldContent), nil)
		_, removed := countDiffOps(ops)
		exc := excerptDiffOps(ops, maxLines-4)
		lines := []string{
			fmt.Sprintf("diff -- %s (+0 -%d)", snap.path, removed),
			fmt.Sprintf("--- %s", diffOldLabel(snap.path, true)),
			"+++ /dev/null",
			fmt.Sprintf("@@ -%d,%d +0,0 @@", exc.oldStart, exc.oldCount),
		}
		lines = append(lines, exc.lines...)
		if exc.truncated {
			lines = append(lines, "...")
		}
		return lines
	}

	ops := diffLines(splitDiffLines(snap.oldContent), splitDiffLines(newContent))
	added, removed := countDiffOps(ops)
	if added == 0 && removed == 0 {
		return nil
	}

	exc := excerptDiffOps(ops, maxLines-4)
	lines := []string{
		fmt.Sprintf("diff -- %s (+%d -%d)", snap.path, added, removed),
		fmt.Sprintf("--- %s", diffOldLabel(snap.path, snap.oldExists)),
		fmt.Sprintf("+++ %s", diffNewLabel(snap.path)),
		fmt.Sprintf("@@ -%d,%d +%d,%d @@", exc.oldStart, exc.oldCount, exc.newStart, exc.newCount),
	}
	lines = append(lines, exc.lines...)
	if exc.truncated {
		lines = append(lines, "...")
	}
	return lines
}

func splitDiffLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func diffLines(oldLines, newLines []string) []diffOp {
	if len(oldLines) == 0 && len(newLines) == 0 {
		return nil
	}
	if len(oldLines)*len(newLines) > 40000 {
		return fallbackDiffLines(oldLines, newLines)
	}

	n := len(oldLines)
	m := len(newLines)
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}

	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if oldLines[i] == newLines[j] {
				dp[i][j] = dp[i+1][j+1] + 1
				continue
			}
			if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}

	var ops []diffOp
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case oldLines[i] == newLines[j]:
			ops = append(ops, diffOp{kind: ' ', text: oldLines[i]})
			i++
			j++
		case dp[i+1][j] >= dp[i][j+1]:
			ops = append(ops, diffOp{kind: '-', text: oldLines[i]})
			i++
		default:
			ops = append(ops, diffOp{kind: '+', text: newLines[j]})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, diffOp{kind: '-', text: oldLines[i]})
	}
	for ; j < m; j++ {
		ops = append(ops, diffOp{kind: '+', text: newLines[j]})
	}
	return ops
}

func fallbackDiffLines(oldLines, newLines []string) []diffOp {
	limit := len(oldLines)
	if len(newLines) > limit {
		limit = len(newLines)
	}

	var ops []diffOp
	for i := 0; i < limit; i++ {
		var (
			oldLine string
			newLine string
			hasOld  = i < len(oldLines)
			hasNew  = i < len(newLines)
		)
		if hasOld {
			oldLine = oldLines[i]
		}
		if hasNew {
			newLine = newLines[i]
		}
		switch {
		case hasOld && hasNew && oldLine == newLine:
			ops = append(ops, diffOp{kind: ' ', text: oldLine})
		default:
			if hasOld {
				ops = append(ops, diffOp{kind: '-', text: oldLine})
			}
			if hasNew {
				ops = append(ops, diffOp{kind: '+', text: newLine})
			}
		}
	}
	return ops
}

func countDiffOps(ops []diffOp) (added, removed int) {
	for _, op := range ops {
		switch op.kind {
		case '+':
			added++
		case '-':
			removed++
		}
	}
	return added, removed
}

// diffExcerpt holds the result of excerpting diff ops, including the
// hunk header line numbers for git-style @@ display.
type diffExcerpt struct {
	lines     []string
	truncated bool
	oldStart  int
	oldCount  int
	newStart  int
	newCount  int
}

func excerptDiffOps(ops []diffOp, maxLines int) diffExcerpt {
	if maxLines <= 0 || len(ops) == 0 {
		return diffExcerpt{truncated: len(ops) > 0}
	}

	firstDiff := -1
	for i, op := range ops {
		if op.kind != ' ' {
			firstDiff = i
			break
		}
	}
	if firstDiff < 0 {
		return diffExcerpt{}
	}

	start := firstDiff
	if start > 0 {
		start--
	}
	end := start + maxLines
	if end > len(ops) {
		end = len(ops)
	}
	if end < len(ops) && ops[end-1].kind != ' ' {
		end++
		if end > len(ops) {
			end = len(ops)
		}
	}

	// Compute starting line numbers for old and new files.
	oldLine := 1
	newLine := 1
	for _, op := range ops[:start] {
		switch op.kind {
		case ' ':
			oldLine++
			newLine++
		case '-':
			oldLine++
		case '+':
			newLine++
		}
	}

	exc := diffExcerpt{
		oldStart: oldLine,
		newStart: newLine,
	}

	lines := make([]string, 0, end-start)
	for _, op := range ops[start:end] {
		switch op.kind {
		case ' ':
			lines = append(lines, fmt.Sprintf("  %4d    %s", newLine, op.text))
			oldLine++
			newLine++
			exc.oldCount++
			exc.newCount++
		case '-':
			lines = append(lines, fmt.Sprintf("  %4d -  %s", oldLine, op.text))
			oldLine++
			exc.oldCount++
		case '+':
			lines = append(lines, fmt.Sprintf("  %4d +  %s", newLine, op.text))
			newLine++
			exc.newCount++
		}
	}
	exc.lines = lines

	for _, op := range ops[end:] {
		if op.kind != ' ' {
			exc.truncated = true
			break
		}
	}
	return exc
}

func diffOldLabel(path string, exists bool) string {
	if !exists {
		return "/dev/null"
	}
	if filepath.IsAbs(path) {
		return "a" + path
	}
	return "a/" + path
}

func diffNewLabel(path string) string {
	if filepath.IsAbs(path) {
		return "b" + path
	}
	return "b/" + path
}

// colorizeDiffLines applies syntax highlighting + diff coloring to a block
// of diff lines. filePath is used to detect the language for syntax highlighting.
func colorizeDiffLines(lines []string, filePath string) []string {
	if len(lines) == 0 {
		return nil
	}

	// Build a map of code content lines for syntax highlighting.
	// Extract just the code part from diff lines (after the "  1234 +  " prefix).
	type lineInfo struct {
		prefix string // e.g. "  1234 +  " or "  1234    "
		code   string // the actual code content
		kind   byte   // '+', '-', ' ', or 'h' for header
	}
	parsed := make([]lineInfo, len(lines))
	var codeBuilder strings.Builder

	for i, line := range lines {
		switch {
		case strings.HasPrefix(line, "diff --"),
			strings.HasPrefix(line, "---"),
			strings.HasPrefix(line, "+++"),
			strings.HasPrefix(line, "@@"),
			strings.HasPrefix(line, "..."),
			strings.HasPrefix(line, "(no "),
			strings.HasPrefix(line, "! "):
			parsed[i] = lineInfo{prefix: line, kind: 'h'}
		default:
			prefix, code := splitDiffLineContent(line)
			parsed[i] = lineInfo{prefix: prefix, code: code, kind: diffLineKind(line)}
			codeBuilder.WriteString(code)
			codeBuilder.WriteByte('\n')
		}
	}

	// Syntax-highlight all code lines at once.
	highlighted := highlightCode(codeBuilder.String(), filePath)
	hlLines := strings.Split(highlighted, "\n")

	// Reassemble: prefix (with diff coloring) + highlighted code.
	// Chroma's ANSI output contains \033[0m resets that kill our background.
	// We inject our background color after every reset so it persists.
	const bgGreen = "\033[48;5;22m"
	const bgRed = "\033[48;5;52m"

	result := make([]string, len(lines))
	hlIdx := 0
	for i, info := range parsed {
		switch info.kind {
		case 'h':
			result[i] = "\033[2m" + info.prefix + "\033[0m"
		case '+':
			code := info.code
			if hlIdx < len(hlLines) {
				code = injectBgAfterReset(hlLines[hlIdx], bgGreen)
				hlIdx++
			}
			result[i] = "\033[38;5;156;48;5;22m" + info.prefix + "\033[0m" +
				bgGreen + code + "\033[0m"
		case '-':
			code := info.code
			if hlIdx < len(hlLines) {
				code = injectBgAfterReset(hlLines[hlIdx], bgRed)
				hlIdx++
			}
			result[i] = "\033[38;5;210;48;5;52m" + info.prefix + "\033[0m" +
				bgRed + code + "\033[0m"
		default:
			code := info.code
			if hlIdx < len(hlLines) {
				code = hlLines[hlIdx]
				hlIdx++
			}
			result[i] = "\033[2m" + info.prefix + "\033[0m" + code
		}
	}
	return result
}

// splitDiffLineContent splits a diff line into its prefix (line number + marker)
// and the actual code content.
// Input format: "  1234 +  code here" → prefix="  1234 +  ", code="code here"
func splitDiffLineContent(line string) (string, string) {
	if len(line) < 2 || line[0] != ' ' || line[1] != ' ' {
		return "", line
	}
	// Scan past spaces and digits to find +/-/space marker, then "  " separator.
	i := 2
	// Skip digits
	for i < len(line) && line[i] >= '0' && line[i] <= '9' {
		i++
	}
	// Skip space before marker
	if i < len(line) && line[i] == ' ' {
		i++
	}
	// Skip marker (+, -, or space)
	if i < len(line) && (line[i] == '+' || line[i] == '-' || line[i] == ' ') {
		i++
	}
	// Skip "  " after marker
	if i < len(line) && line[i] == ' ' {
		i++
		if i < len(line) && line[i] == ' ' {
			i++
		}
	}
	if i >= len(line) {
		return line, ""
	}
	return line[:i], line[i:]
}

// diffLineKind returns '+', '-', or ' ' based on the diff line format.
func diffLineKind(line string) byte {
	if len(line) < 8 || line[0] != ' ' || line[1] != ' ' {
		return ' '
	}
	for i := 2; i < len(line); i++ {
		if line[i] == '+' {
			return '+'
		}
		if line[i] == '-' {
			return '-'
		}
		if line[i] != ' ' && (line[i] < '0' || line[i] > '9') {
			return ' '
		}
	}
	return ' '
}

// ---------------------------------------------------------------------------
// Syntax highlighting via chroma
// ---------------------------------------------------------------------------

// highlightCode uses chroma to syntax-highlight a block of code, returning
// ANSI-colored text. Falls back to plain text if highlighting fails.
func highlightCode(code, filePath string) string {
	if strings.TrimSpace(code) == "" {
		return code
	}

	// Detect language from file extension.
	lexer := lexers.Match(filePath)
	if lexer == nil {
		lexer = lexers.Analyse(code)
	}
	if lexer == nil {
		lexer = lexers.Fallback
	}
	lexer = chroma.Coalesce(lexer)

	// Use a 256-color terminal formatter and a dark theme.
	formatter := formatters.Get("terminal256")
	if formatter == nil {
		formatter = formatters.Fallback
	}
	style := styles.Get("monokai")
	if style == nil {
		style = styles.Fallback
	}

	iterator, err := lexer.Tokenise(nil, code)
	if err != nil {
		return code
	}

	var buf bytes.Buffer
	if err := formatter.Format(&buf, style, iterator); err != nil {
		return code
	}

	// Remove trailing newline that chroma adds.
	result := buf.String()
	result = strings.TrimSuffix(result, "\n")
	return result
}

// injectBgAfterReset replaces every ANSI reset (\033[0m) in s with
// reset + bg color, so the diff background persists through chroma's
// syntax highlighting tokens.
func injectBgAfterReset(s, bg string) string {
	return strings.ReplaceAll(s, "\033[0m", "\033[0m"+bg)
}
