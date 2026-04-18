package term

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Keybindings struct {
	CycleMode     string `json:"cycle_mode"`
	Cancel        string `json:"cancel"`
	Quit          string `json:"quit"`
	ClearScreen   string `json:"clear_screen"`
	LineStart     string `json:"line_start"`
	LineEnd       string `json:"line_end"`
	DeleteToStart string `json:"delete_to_start"`
	DeleteToEnd   string `json:"delete_to_end"`
	DeleteWord    string `json:"delete_word"`
}

func DefaultKeybindings() *Keybindings {
	return &Keybindings{
		CycleMode:     "ctrl+w",
		Cancel:        "ctrl+c",
		Quit:          "ctrl+d",
		ClearScreen:   "ctrl+l",
		LineStart:     "ctrl+a",
		LineEnd:       "ctrl+e",
		DeleteToStart: "ctrl+u",
		DeleteToEnd:   "ctrl+k",
		DeleteWord:    "ctrl+b",
	}
}

func KeybindingsPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".brain", "keybindings.json")
}

func LoadKeybindings() *Keybindings {
	kb := DefaultKeybindings()
	data, err := os.ReadFile(KeybindingsPath())
	if err != nil {
		return kb
	}
	json.Unmarshal(data, kb)
	return kb
}

func KeyToChar(binding string) (byte, error) {
	binding = strings.ToLower(strings.TrimSpace(binding))
	if binding == "" {
		return 0, fmt.Errorf("empty key binding")
	}
	if !strings.HasPrefix(binding, "ctrl+") {
		return 0, fmt.Errorf("unsupported key binding %q (only ctrl+<letter> is supported)", binding)
	}
	letter := binding[5:]
	if len(letter) != 1 || letter[0] < 'a' || letter[0] > 'z' {
		return 0, fmt.Errorf("unsupported key binding %q (must be ctrl+a through ctrl+z)", binding)
	}
	return letter[0] - 'a' + 1, nil
}

func KeybindingsHelp(kb *Keybindings) string {
	return fmt.Sprintf(`  %-18s %s
  %-18s %s
  %-18s %s
  %-18s %s
  %-18s %s
  %-18s %s
  %-18s %s
  %-18s %s
  %-18s %s
  %-18s %s
  %-18s %s
  %-18s %s`,
		strings.ToUpper(kb.CycleMode), "Cycle mode (plan → accept-edits → auto)",
		strings.ToUpper(kb.Cancel), "Cancel current AI response",
		strings.ToUpper(kb.Quit), "Quit chat",
		strings.ToUpper(kb.ClearScreen), "Clear screen",
		strings.ToUpper(kb.LineStart), "Move cursor to line start",
		strings.ToUpper(kb.LineEnd), "Move cursor to line end",
		strings.ToUpper(kb.DeleteToStart), "Delete to line start",
		strings.ToUpper(kb.DeleteToEnd), "Delete to line end",
		strings.ToUpper(kb.DeleteWord), "Delete previous word",
		"←/→", "Move cursor left/right",
		"HOME/END", "Move cursor to line start/end",
		"DELETE", "Delete character at cursor",
	)
}
