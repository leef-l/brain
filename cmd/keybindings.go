package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// keybindings defines configurable key bindings for chat mode.
// Keys are represented as strings like "ctrl+w", "ctrl+c", "ctrl+k", etc.
type keybindings struct {
	// Action keys (produce inputAction).
	CycleMode string `json:"cycle_mode"` // cycle plan → accept-edits → auto
	Cancel    string `json:"cancel"`     // cancel current AI response
	Quit      string `json:"quit"`       // exit chat entirely

	// Line editing keys.
	ClearScreen   string `json:"clear_screen"`    // clear terminal
	LineStart     string `json:"line_start"`      // cursor to beginning of line
	LineEnd       string `json:"line_end"`        // cursor to end of line
	DeleteToStart string `json:"delete_to_start"` // delete from cursor to line start
	DeleteToEnd   string `json:"delete_to_end"`   // delete from cursor to line end
	DeleteWord    string `json:"delete_word"`     // delete word before cursor
}

// defaultKeybindings returns the built-in defaults.
func defaultKeybindings() *keybindings {
	return &keybindings{
		CycleMode:     "ctrl+w",
		Cancel:        "ctrl+c", // Ctrl+C is ignored in REPL; Escape handles cancellation
		Quit:          "ctrl+d",
		ClearScreen:   "ctrl+l",
		LineStart:     "ctrl+a",
		LineEnd:       "ctrl+e",
		DeleteToStart: "ctrl+u",
		DeleteToEnd:   "ctrl+k",
		DeleteWord:    "ctrl+b",
	}
}

// keybindingsPath returns ~/.brain/keybindings.json.
func keybindingsPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".brain", "keybindings.json")
}

// loadKeybindings reads keybindings from disk, falling back to defaults.
func loadKeybindings() *keybindings {
	kb := defaultKeybindings()
	data, err := os.ReadFile(keybindingsPath())
	if err != nil {
		return kb
	}
	// Merge: only override fields that are present in the file.
	json.Unmarshal(data, kb)
	return kb
}

// keyToChar converts a key binding string like "ctrl+w" to its byte value.
// Supported: ctrl+a through ctrl+z (0x01-0x1A).
func keyToChar(binding string) (byte, error) {
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
	// ctrl+a = 0x01, ctrl+b = 0x02, ... ctrl+z = 0x1a
	return letter[0] - 'a' + 1, nil
}

// keybindingsHelp returns a formatted string showing all current key bindings.
func keybindingsHelp(kb *keybindings) string {
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
