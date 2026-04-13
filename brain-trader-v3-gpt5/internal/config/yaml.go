package config

import (
	"bufio"
	"fmt"
	"strings"
)

type yamlLine struct {
	indent  int
	content string
}

func parseYAML(input string) (map[string]any, error) {
	lines, err := tokenizeYAML(input)
	if err != nil {
		return nil, err
	}
	node, next, err := parseBlock(lines, 0, 0)
	if err != nil {
		return nil, err
	}
	if next != len(lines) {
		return nil, fmt.Errorf("config: unexpected trailing yaml content at line %d", next+1)
	}
	if node == nil {
		return map[string]any{}, nil
	}
	m, ok := node.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("config: top-level YAML must be a mapping")
	}
	return m, nil
}

func tokenizeYAML(input string) ([]yamlLine, error) {
	var lines []yamlLine
	scanner := bufio.NewScanner(strings.NewReader(input))
	for scanner.Scan() {
		raw := scanner.Text()
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		cleaned := stripYAMLComment(raw)
		if strings.TrimSpace(cleaned) == "" {
			continue
		}
		indent := 0
		for indent < len(cleaned) && cleaned[indent] == ' ' {
			indent++
		}
		if indent < len(cleaned) && cleaned[indent] == '\t' {
			return nil, fmt.Errorf("config: tabs are not allowed in YAML indentation")
		}
		content := strings.TrimSpace(cleaned)
		lines = append(lines, yamlLine{
			indent:  indent,
			content: content,
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return lines, nil
}

func parseBlock(lines []yamlLine, start int, indent int) (any, int, error) {
	if start >= len(lines) || lines[start].indent < indent {
		return nil, start, nil
	}
	if strings.HasPrefix(lines[start].content, "- ") {
		return parseList(lines, start, indent)
	}
	return parseMap(lines, start, indent)
}

func parseMap(lines []yamlLine, start int, indent int) (map[string]any, int, error) {
	out := make(map[string]any)
	i := start
	for i < len(lines) {
		line := lines[i]
		if line.indent < indent {
			break
		}
		if line.indent > indent {
			return nil, i, fmt.Errorf("config: invalid indentation at line %d", i+1)
		}
		if strings.HasPrefix(line.content, "- ") {
			break
		}

		key, value, found := strings.Cut(line.content, ":")
		if !found {
			return nil, i, fmt.Errorf("config: expected key:value at line %d", i+1)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		i++
		if value != "" {
			parsed, err := parseScalar(value)
			if err != nil {
				return nil, i, fmt.Errorf("config: line %d: %w", i, err)
			}
			out[key] = parsed
			continue
		}

		if i >= len(lines) || lines[i].indent <= indent {
			out[key] = map[string]any{}
			continue
		}

		child, next, err := parseBlock(lines, i, lines[i].indent)
		if err != nil {
			return nil, i, err
		}
		out[key] = child
		i = next
	}
	return out, i, nil
}

func parseList(lines []yamlLine, start int, indent int) ([]any, int, error) {
	var out []any
	i := start
	for i < len(lines) {
		line := lines[i]
		if line.indent < indent {
			break
		}
		if line.indent > indent {
			return nil, i, fmt.Errorf("config: invalid list indentation at line %d", i+1)
		}
		trimmed := strings.TrimSpace(line.content)
		if !strings.HasPrefix(trimmed, "- ") {
			break
		}
		item := strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))
		i++
		if item == "" {
			if i < len(lines) && lines[i].indent > indent {
				child, next, err := parseBlock(lines, i, lines[i].indent)
				if err != nil {
					return nil, i, err
				}
				out = append(out, child)
				i = next
				continue
			}
			out = append(out, "")
			continue
		}
		parsed, err := parseScalar(item)
		if err != nil {
			return nil, i, fmt.Errorf("config: line %d: %w", i, err)
		}
		out = append(out, parsed)
	}
	return out, i, nil
}

func parseScalar(value string) (any, error) {
	value = strings.TrimSpace(expandEnv(value))
	switch {
	case value == "":
		return "", nil
	case strings.HasPrefix(value, "\"") && strings.HasSuffix(value, "\""):
		return strings.Trim(value, "\""), nil
	case strings.HasPrefix(value, "'") && strings.HasSuffix(value, "'"):
		return strings.Trim(value, "'"), nil
	case strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]"):
		return parseInlineList(value)
	case strings.EqualFold(value, "true"):
		return true, nil
	case strings.EqualFold(value, "false"):
		return false, nil
	}

	if parsed, err := parseInt64(value); err == nil {
		return int(parsed), nil
	}
	if parsed, err := parseFloat(value); err == nil {
		return parsed, nil
	}
	return value, nil
}

func parseInlineList(value string) ([]any, error) {
	inner := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(value, "["), "]"))
	if inner == "" {
		return []any{}, nil
	}
	parts, err := splitListItems(inner)
	if err != nil {
		return nil, err
	}
	out := make([]any, 0, len(parts))
	for _, part := range parts {
		parsed, err := parseScalar(part)
		if err != nil {
			return nil, err
		}
		out = append(out, parsed)
	}
	return out, nil
}

func splitListItems(input string) ([]string, error) {
	var (
		items []string
		buf   strings.Builder
		quote rune
	)
	for _, r := range input {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			}
			buf.WriteRune(r)
		case r == '\'' || r == '"':
			quote = r
			buf.WriteRune(r)
		case r == ',':
			items = append(items, strings.TrimSpace(buf.String()))
			buf.Reset()
		default:
			buf.WriteRune(r)
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("config: unterminated quote in list: %q", input)
	}
	if trimmed := strings.TrimSpace(buf.String()); trimmed != "" {
		items = append(items, trimmed)
	}
	return items, nil
}

func stripYAMLComment(line string) string {
	var (
		quote rune
		buf   strings.Builder
	)
	for _, r := range line {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			}
			buf.WriteRune(r)
		case r == '\'' || r == '"':
			quote = r
			buf.WriteRune(r)
		case r == '#':
			return strings.TrimRight(buf.String(), " ")
		default:
			buf.WriteRune(r)
		}
	}
	return buf.String()
}
