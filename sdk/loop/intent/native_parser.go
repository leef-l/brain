package intent

// NativeToolUseParser is the trivial passthrough for ContentBlocks the
// provider already emitted as `tool_use`. It exists in the chain so the
// runner can have a single uniform entry point — instead of "if native
// then dispatch else IntentParser", the runner always asks the chain
// and gets back a unified Intent slice.
//
// Confidence is fixed at 1.0 because by the time we reach this parser
// the provider has already done its own structured-output validation in
// llm.ValidateToolUseResponse (see sdk/llm/response_validation.go).
type NativeToolUseParser struct{}

// Name implements Parser.
func (NativeToolUseParser) Name() string { return "native_tool_use" }

// Parse extracts every Type=="tool_use" ContentBlock and emits a 1.0
// Intent for each. Order is preserved, which means downstream Chain
// dedup sees native intents first and they win all ties.
func (NativeToolUseParser) Parse(pc ParseContext) ([]Intent, error) {
	var out []Intent
	for _, b := range pc.Content {
		if b.Type != "tool_use" {
			continue
		}
		args := b.Input
		if len(args) == 0 {
			args = []byte("{}")
		}
		intent := Intent{
			ToolName:   b.ToolName,
			Args:       append([]byte(nil), args...),
			Confidence: 1.0,
			Source:     SourceNative,
		}
		out = append(out, intent)
	}
	return out, nil
}
