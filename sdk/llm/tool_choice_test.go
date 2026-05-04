package llm

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestBuildOpenAIToolChoice(t *testing.T) {
	cases := []struct {
		name    string
		value   string
		support ToolChoiceMode
		want    interface{}
	}{
		{"none-support drops everything", "required", ToolChoiceNone, nil},
		{"empty value omits", "", ToolChoiceRequired, nil},
		{"auto on auto", "auto", ToolChoiceAuto, "auto"},
		{"auto on required", "auto", ToolChoiceRequired, "auto"},
		{"none on auto", "none", ToolChoiceAuto, "none"},
		{"required on auto drops", "required", ToolChoiceAuto, nil},
		{"required on required emits", "required", ToolChoiceRequired, "required"},
		{"required on specific emits", "required", ToolChoiceSpecific, "required"},
		{"specific name on specific", "code.write_file", ToolChoiceSpecific, map[string]interface{}{
			"type": "function",
			"function": map[string]string{
				"name": "code__write_file",
			},
		}},
		{"specific name on required degrades", "code.write_file", ToolChoiceRequired, "required"},
		{"specific name on auto drops", "code.write_file", ToolChoiceAuto, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := buildOpenAIToolChoice(c.value, c.support)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("buildOpenAIToolChoice(%q,%v) = %#v, want %#v", c.value, c.support, got, c.want)
			}
		})
	}
}

func TestBuildAnthropicToolChoice(t *testing.T) {
	mustJSON := func(v interface{}) json.RawMessage {
		b, _ := json.Marshal(v)
		return b
	}
	cases := []struct {
		name    string
		value   string
		support ToolChoiceMode
		want    json.RawMessage
	}{
		{"none-support drops", "required", ToolChoiceNone, nil},
		{"empty value omits", "", ToolChoiceRequired, nil},
		{"auto on auto", "auto", ToolChoiceAuto, mustJSON(map[string]string{"type": "auto"})},
		{"required on required maps to any", "required", ToolChoiceRequired, mustJSON(map[string]string{"type": "any"})},
		{"required on auto drops", "required", ToolChoiceAuto, nil},
		{"specific on specific", "code.write_file", ToolChoiceSpecific, mustJSON(map[string]string{
			"type": "tool",
			"name": "code__write_file",
		})},
		{"specific on required degrades to any", "code.write_file", ToolChoiceRequired, mustJSON(map[string]string{"type": "any"})},
		{"none on auto", "none", ToolChoiceAuto, mustJSON(map[string]string{"type": "none"})},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := buildAnthropicToolChoice(c.value, c.support)
			if !bytesEq(got, c.want) {
				t.Errorf("buildAnthropicToolChoice(%q,%v) = %s, want %s", c.value, c.support, string(got), string(c.want))
			}
		})
	}
}

func bytesEq(a, b json.RawMessage) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return string(a) == string(b)
}
