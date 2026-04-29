package diff

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

type Op struct {
	Kind byte
	Text string
}

type WritePreviewInput struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type FileSnapshot struct {
	Path       string
	OldContent string
	OldExists  bool
}

func BuildPreExecPreview(workdir, toolName string, args json.RawMessage, maxLines int) []string {
	if !strings.HasSuffix(toolName, ".write_file") || maxLines <= 0 {
		return nil
	}
	var input WritePreviewInput
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

	ops := DiffLines(SplitDiffLines(oldContent), SplitDiffLines(input.Content))
	added, removed := CountDiffOps(ops)
	if added == 0 && removed == 0 {
		return []string{
			fmt.Sprintf("diff -- %s", input.Path),
			"(no textual changes)",
		}
	}

	exc := ExcerptDiffOps(ops, maxLines-4)
	lines := []string{
		fmt.Sprintf("diff -- %s (+%d -%d)", input.Path, added, removed),
		fmt.Sprintf("--- %s", DiffOldLabel(input.Path, oldExists)),
		fmt.Sprintf("+++ %s", DiffNewLabel(input.Path)),
		fmt.Sprintf("@@ -%d,%d +%d,%d @@", exc.OldStart, exc.OldCount, exc.NewStart, exc.NewCount),
	}
	lines = append(lines, exc.Lines...)
	if exc.Truncated {
		lines = append(lines, "...")
	}
	return lines
}

func SnapshotForTool(workdir, toolName string, args json.RawMessage) *FileSnapshot {
	var filePath string

	switch {
	case strings.HasSuffix(toolName, ".write_file"):
		var input WritePreviewInput
		if json.Unmarshal(args, &input) != nil || input.Path == "" {
			return nil
		}
		filePath = input.Path

	case strings.HasSuffix(toolName, ".shell_exec"):
		var shellInput struct {
			Command string `json:"command"`
		}
		if json.Unmarshal(args, &shellInput) != nil {
			return nil
		}
		filePath = ExtractFilePathFromCommand(shellInput.Command)
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

	snap := &FileSnapshot{Path: absPath}
	if data, err := os.ReadFile(absPath); err == nil {
		snap.OldExists = true
		snap.OldContent = string(data)
	}
	return snap
}

func ExtractFilePathFromCommand(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return ""
	}

	base := filepath.Base(fields[0])

	switch base {
	case "sed":
		for i := 1; i < len(fields); i++ {
			f := fields[i]
			if f == "-i" {
				continue
			}
			if strings.HasPrefix(f, "-i") {
				continue
			}
			if strings.HasPrefix(f, "-") {
				continue
			}
			if strings.HasPrefix(f, "'") || strings.HasPrefix(f, "\"") || strings.HasPrefix(f, "s/") || strings.HasPrefix(f, "/") {
				continue
			}
			return f
		}
		if len(fields) >= 3 {
			last := fields[len(fields)-1]
			if !strings.HasPrefix(last, "-") && !strings.HasPrefix(last, "'") && !strings.HasPrefix(last, "\"") {
				return last
			}
		}

	case "rm":
		for i := 1; i < len(fields); i++ {
			if !strings.HasPrefix(fields[i], "-") {
				return fields[i]
			}
		}

	case "tee":
		for i := 1; i < len(fields); i++ {
			if !strings.HasPrefix(fields[i], "-") {
				return fields[i]
			}
		}

	case "cp", "mv":
		if len(fields) >= 3 {
			return fields[len(fields)-1]
		}
	}

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

func BuildPostExecDiff(snap *FileSnapshot, maxLines int) []string {
	if snap == nil || maxLines <= 0 {
		return nil
	}

	newContent := ""
	newExists := false
	if data, err := os.ReadFile(snap.Path); err == nil {
		newExists = true
		newContent = string(data)
	}

	if snap.OldContent == newContent && snap.OldExists == newExists {
		return nil
	}

	if snap.OldExists && !newExists {
		ops := DiffLines(SplitDiffLines(snap.OldContent), nil)
		_, removed := CountDiffOps(ops)
		exc := ExcerptDiffOps(ops, maxLines-4)
		lines := []string{
			fmt.Sprintf("diff -- %s (+0 -%d)", snap.Path, removed),
			fmt.Sprintf("--- %s", DiffOldLabel(snap.Path, true)),
			"+++ /dev/null",
			fmt.Sprintf("@@ -%d,%d +0,0 @@", exc.OldStart, exc.OldCount),
		}
		lines = append(lines, exc.Lines...)
		if exc.Truncated {
			lines = append(lines, "...")
		}
		return lines
	}

	ops := DiffLines(SplitDiffLines(snap.OldContent), SplitDiffLines(newContent))
	added, removed := CountDiffOps(ops)
	if added == 0 && removed == 0 {
		return nil
	}

	exc := ExcerptDiffOps(ops, maxLines-4)
	lines := []string{
		fmt.Sprintf("diff -- %s (+%d -%d)", snap.Path, added, removed),
		fmt.Sprintf("--- %s", DiffOldLabel(snap.Path, snap.OldExists)),
		fmt.Sprintf("+++ %s", DiffNewLabel(snap.Path)),
		fmt.Sprintf("@@ -%d,%d +%d,%d @@", exc.OldStart, exc.OldCount, exc.NewStart, exc.NewCount),
	}
	lines = append(lines, exc.Lines...)
	if exc.Truncated {
		lines = append(lines, "...")
	}
	return lines
}

func SplitDiffLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func DiffLines(oldLines, newLines []string) []Op {
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

	var ops []Op
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case oldLines[i] == newLines[j]:
			ops = append(ops, Op{Kind: ' ', Text: oldLines[i]})
			i++
			j++
		case dp[i+1][j] >= dp[i][j+1]:
			ops = append(ops, Op{Kind: '-', Text: oldLines[i]})
			i++
		default:
			ops = append(ops, Op{Kind: '+', Text: newLines[j]})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, Op{Kind: '-', Text: oldLines[i]})
	}
	for ; j < m; j++ {
		ops = append(ops, Op{Kind: '+', Text: newLines[j]})
	}
	return ops
}

func fallbackDiffLines(oldLines, newLines []string) []Op {
	limit := len(oldLines)
	if len(newLines) > limit {
		limit = len(newLines)
	}

	var ops []Op
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
			ops = append(ops, Op{Kind: ' ', Text: oldLine})
		default:
			if hasOld {
				ops = append(ops, Op{Kind: '-', Text: oldLine})
			}
			if hasNew {
				ops = append(ops, Op{Kind: '+', Text: newLine})
			}
		}
	}
	return ops
}

func CountDiffOps(ops []Op) (added, removed int) {
	for _, op := range ops {
		switch op.Kind {
		case '+':
			added++
		case '-':
			removed++
		}
	}
	return added, removed
}

type DiffExcerpt struct {
	Lines     []string
	Truncated bool
	OldStart  int
	OldCount  int
	NewStart  int
	NewCount  int
}

func ExcerptDiffOps(ops []Op, maxLines int) DiffExcerpt {
	if maxLines <= 0 || len(ops) == 0 {
		return DiffExcerpt{Truncated: len(ops) > 0}
	}

	firstDiff := -1
	for i, op := range ops {
		if op.Kind != ' ' {
			firstDiff = i
			break
		}
	}
	if firstDiff < 0 {
		return DiffExcerpt{}
	}

	start := firstDiff
	if start > 0 {
		start--
	}
	end := start + maxLines
	if end > len(ops) {
		end = len(ops)
	}
	if end < len(ops) && ops[end-1].Kind != ' ' {
		end++
		if end > len(ops) {
			end = len(ops)
		}
	}

	oldLine := 1
	newLine := 1
	for _, op := range ops[:start] {
		switch op.Kind {
		case ' ':
			oldLine++
			newLine++
		case '-':
			oldLine++
		case '+':
			newLine++
		}
	}

	exc := DiffExcerpt{
		OldStart: oldLine,
		NewStart: newLine,
	}

	lines := make([]string, 0, end-start)
	for _, op := range ops[start:end] {
		switch op.Kind {
		case ' ':
			lines = append(lines, fmt.Sprintf("  %4d    %s", newLine, op.Text))
			oldLine++
			newLine++
			exc.OldCount++
			exc.NewCount++
		case '-':
			lines = append(lines, fmt.Sprintf("  %4d -  %s", oldLine, op.Text))
			oldLine++
			exc.OldCount++
		case '+':
			lines = append(lines, fmt.Sprintf("  %4d +  %s", newLine, op.Text))
			newLine++
			exc.NewCount++
		}
	}
	exc.Lines = lines

	for _, op := range ops[end:] {
		if op.Kind != ' ' {
			exc.Truncated = true
			break
		}
	}
	return exc
}

func DiffOldLabel(path string, exists bool) string {
	if !exists {
		return "/dev/null"
	}
	if strings.HasPrefix(path, "/") {
		return "a" + path
	}
	return "a/" + path
}

func DiffNewLabel(path string) string {
	if strings.HasPrefix(path, "/") {
		return "b" + path
	}
	return "b/" + path
}

func ColorizeDiffLines(lines []string, filePath string) []string {
	if len(lines) == 0 {
		return nil
	}

	type lineInfo struct {
		prefix string
		code   string
		kind   byte
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

	highlighted := highlightCode(codeBuilder.String(), filePath)
	hlLines := strings.Split(highlighted, "\n")

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

func splitDiffLineContent(line string) (string, string) {
	if len(line) < 2 || line[0] != ' ' || line[1] != ' ' {
		return "", line
	}
	i := 2
	for i < len(line) && line[i] >= '0' && line[i] <= '9' {
		i++
	}
	if i < len(line) && line[i] == ' ' {
		i++
	}
	if i < len(line) && (line[i] == '+' || line[i] == '-' || line[i] == ' ') {
		i++
	}
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

func highlightCode(code, filePath string) string {
	if strings.TrimSpace(code) == "" {
		return code
	}

	lexer := lexers.Match(filePath)
	if lexer == nil {
		lexer = lexers.Analyse(code)
	}
	if lexer == nil {
		lexer = lexers.Fallback
	}
	lexer = chroma.Coalesce(lexer)

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

	result := buf.String()
	result = strings.TrimSuffix(result, "\n")
	return result
}

func injectBgAfterReset(s, bg string) string {
	return strings.ReplaceAll(s, "\033[0m", "\033[0m"+bg)
}
