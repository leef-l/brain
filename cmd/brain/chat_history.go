package main

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

const maxHistoryLines = 500

// historyPath returns ~/.brain/history.
func historyPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".brain", "history")
}

// loadHistory reads persistent input history from disk.
func loadHistory() []string {
	f, err := os.Open(historyPath())
	if err != nil {
		return nil
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 64*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if line != "" {
			lines = append(lines, line)
		}
	}

	// Keep only recent entries.
	if len(lines) > maxHistoryLines {
		lines = lines[len(lines)-maxHistoryLines:]
	}
	return lines
}

// saveHistory writes the full history list to disk.
func saveHistory(history []string) {
	// Keep only recent entries.
	if len(history) > maxHistoryLines {
		history = history[len(history)-maxHistoryLines:]
	}

	path := historyPath()
	dir := filepath.Dir(path)
	os.MkdirAll(dir, 0700)

	f, err := os.Create(path)
	if err != nil {
		return
	}
	defer f.Close()

	w := bufio.NewWriter(f)
	for _, line := range history {
		// Replace newlines with spaces for single-line storage.
		line = strings.Join(strings.Fields(line), " ")
		if line != "" {
			w.WriteString(line)
			w.WriteByte('\n')
		}
	}
	w.Flush()
}

// appendHistory adds a single line to the history file (fast path).
func appendHistory(line string) {
	line = strings.Join(strings.Fields(line), " ")
	if line == "" {
		return
	}

	path := historyPath()
	dir := filepath.Dir(path)
	os.MkdirAll(dir, 0700)

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	defer f.Close()
	f.WriteString(line + "\n")
}
