package intent

import (
	"encoding/json"
	"regexp"
	"strings"
)

// XMLToolParser handles XML / pseudo-XML tool wrappers commonly produced
// by Qwen, GLM, and Claude post-tuned models when their training data
// emphasized that style:
//
//   <tool name="code.write_file">
//     <input>{"path":"game.html","content":"..."}</input>
//   </tool>
//
//   <function_call>
//     <name>code.write_file</name>
//     <arguments>{"path":"game.html"}</arguments>
//   </function_call>
//
//   <invoke name="code.write_file">
//     <parameter name="path">game.html</parameter>
//     <parameter name="content">...</parameter>
//   </invoke>
//
// We use stdlib regexp rather than encoding/xml because the LLM output
// is rarely valid XML (unescaped angle brackets in code, missing root
// element, etc.) — regex is forgiving in the right ways.
//
// Confidence is 0.85 — the wrapping is unambiguous, but multi-parameter
// XML forms require us to assemble JSON ourselves which is more error-prone
// than the JSON-block parsers above.
type XMLToolParser struct{}

// Name implements Parser.
func (XMLToolParser) Name() string { return "xml_tool" }

var (
	xmlToolRe          = regexp.MustCompile(`(?is)<tool\s+name=["']([^"']+)["']\s*>(.*?)</tool>`)
	xmlFunctionCallRe  = regexp.MustCompile(`(?is)<function_call\s*>(.*?)</function_call>`)
	xmlFunctionNameRe  = regexp.MustCompile(`(?is)<name\s*>(.*?)</name>`)
	xmlFunctionArgsRe  = regexp.MustCompile(`(?is)<(?:arguments|args|input)\s*>(.*?)</(?:arguments|args|input)>`)
	xmlInvokeRe        = regexp.MustCompile(`(?is)<invoke\s+name=["']([^"']+)["']\s*>(.*?)</invoke>`)
	xmlParameterRe     = regexp.MustCompile(`(?is)<parameter\s+name=["']([^"']+)["']\s*>(.*?)</parameter>`)
	xmlInputRe         = regexp.MustCompile(`(?is)<input\s*>(.*?)</input>`)
)

// Parse extracts every recognized XML envelope and emits one Intent per
// match.
func (XMLToolParser) Parse(pc ParseContext) ([]Intent, error) {
	var out []Intent
	for _, b := range pc.Content {
		if b.Type != "text" || b.Text == "" {
			continue
		}
		out = append(out, parseXMLToolForm(b.Text)...)
		out = append(out, parseXMLFunctionCallForm(b.Text)...)
		out = append(out, parseXMLInvokeForm(b.Text)...)
	}
	return out, nil
}

// parseXMLToolForm handles `<tool name="..."><input>{json}</input></tool>`.
func parseXMLToolForm(text string) []Intent {
	matches := xmlToolRe.FindAllStringSubmatchIndex(text, -1)
	var out []Intent
	for _, m := range matches {
		// m: [outerStart, outerEnd, nameStart, nameEnd, bodyStart, bodyEnd]
		name := text[m[2]:m[3]]
		body := text[m[4]:m[5]]
		args := extractXMLArgs(body)
		if !json.Valid(args) {
			args = json.RawMessage("{}")
		}
		out = append(out, Intent{
			ToolName:   strings.TrimSpace(name),
			Args:       args,
			Confidence: 0.85,
			Source:     SourceXMLTool,
			SpanStart:  m[0],
			SpanEnd:    m[1],
			SourceText: shortPreview(body, 80),
		})
	}
	return out
}

// parseXMLFunctionCallForm handles
// `<function_call><name>..</name><arguments>{json}</arguments></function_call>`.
func parseXMLFunctionCallForm(text string) []Intent {
	matches := xmlFunctionCallRe.FindAllStringSubmatchIndex(text, -1)
	var out []Intent
	for _, m := range matches {
		body := text[m[2]:m[3]]
		nameMatch := xmlFunctionNameRe.FindStringSubmatch(body)
		argsMatch := xmlFunctionArgsRe.FindStringSubmatch(body)
		if len(nameMatch) < 2 {
			continue
		}
		name := strings.TrimSpace(nameMatch[1])
		var args json.RawMessage = json.RawMessage("{}")
		if len(argsMatch) >= 2 {
			candidate := strings.TrimSpace(argsMatch[1])
			if json.Valid([]byte(candidate)) {
				args = json.RawMessage(candidate)
			}
		}
		out = append(out, Intent{
			ToolName:   name,
			Args:       args,
			Confidence: 0.85,
			Source:     SourceXMLTool,
			SpanStart:  m[0],
			SpanEnd:    m[1],
			SourceText: shortPreview(body, 80),
		})
	}
	return out
}

// parseXMLInvokeForm handles
// `<invoke name="..."><parameter name="x">v</parameter>...</invoke>` —
// the Anthropic post-tuned variant.
func parseXMLInvokeForm(text string) []Intent {
	matches := xmlInvokeRe.FindAllStringSubmatchIndex(text, -1)
	var out []Intent
	for _, m := range matches {
		name := text[m[2]:m[3]]
		body := text[m[4]:m[5]]
		params := xmlParameterRe.FindAllStringSubmatch(body, -1)
		if len(params) == 0 {
			continue
		}
		argMap := make(map[string]string, len(params))
		for _, p := range params {
			if len(p) >= 3 {
				argMap[strings.TrimSpace(p[1])] = strings.TrimSpace(p[2])
			}
		}
		args, err := json.Marshal(argMap)
		if err != nil {
			args = json.RawMessage("{}")
		}
		out = append(out, Intent{
			ToolName:   strings.TrimSpace(name),
			Args:       args,
			Confidence: 0.85,
			Source:     SourceXMLTool,
			SpanStart:  m[0],
			SpanEnd:    m[1],
			SourceText: shortPreview(body, 80),
		})
	}
	return out
}

// extractXMLArgs pulls the first `<input>...</input>` body out, falling
// back to the entire enclosing body when absent. The result is a raw
// JSON candidate — caller validates.
func extractXMLArgs(body string) json.RawMessage {
	if m := xmlInputRe.FindStringSubmatch(body); len(m) >= 2 {
		return json.RawMessage(strings.TrimSpace(m[1]))
	}
	return json.RawMessage(strings.TrimSpace(body))
}
