package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestDesktopOpenPathRejectsEmpty(t *testing.T) {
	tl := NewDesktopOpenPathTool()
	res, err := tl.Execute(context.Background(), json.RawMessage(`{"target":""}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.IsError {
		t.Error("empty target must be rejected")
	}
}

func TestDesktopSendHotkeyRejectsEmpty(t *testing.T) {
	tl := NewDesktopSendHotkeyTool()
	res, _ := tl.Execute(context.Background(), json.RawMessage(`{"keys":""}`))
	if !res.IsError {
		t.Error("empty keys must be rejected")
	}
}

func TestParseWmctrlSample(t *testing.T) {
	sample := `0x03200001  0 1234 host Terminal — bash
0x04a00002  0 2345 host Firefox — Mozilla`
	got := parseWmctrl(sample)
	if len(got) != 2 {
		t.Fatalf("expected 2 windows, got %d", len(got))
	}
	if got[0].PID != 1234 || !strings.Contains(got[0].Title, "Terminal") {
		t.Errorf("first window wrong: %+v", got[0])
	}
	if got[1].PID != 2345 || !strings.Contains(got[1].Title, "Firefox") {
		t.Errorf("second window wrong: %+v", got[1])
	}
}

func TestBuildAppleScriptKeyDownBasic(t *testing.T) {
	script := buildAppleScriptKeyDown("cmd+shift+t")
	if !strings.Contains(script, "command down") || !strings.Contains(script, "shift down") {
		t.Errorf("missing modifiers in script: %q", script)
	}
	if !strings.Contains(script, `keystroke "t"`) {
		t.Errorf("missing keystroke: %q", script)
	}
}

func TestBuildAppleScriptKeyDownNoFinalReturnsEmpty(t *testing.T) {
	if got := buildAppleScriptKeyDown("ctrl+shift"); got != "" {
		t.Errorf("expected empty for modifier-only combo, got %q", got)
	}
}

func TestDesktopOpenPathSchema(t *testing.T) {
	tl := NewDesktopOpenPathTool()
	s := tl.Schema()
	if s.Name != "desktop.open_path" {
		t.Errorf("name = %q", s.Name)
	}
	if s.Brain != "desktop" {
		t.Errorf("brain = %q, want desktop", s.Brain)
	}
}
