package chat

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

const MaxHistoryLines = 500

func HistoryPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".brain", "history")
}

func LoadHistory() []string {
	f, err := os.Open(HistoryPath())
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

	if len(lines) > MaxHistoryLines {
		lines = lines[len(lines)-MaxHistoryLines:]
	}
	return lines
}

func SaveHistory(history []string) {
	if len(history) > MaxHistoryLines {
		history = history[len(history)-MaxHistoryLines:]
	}

	path := HistoryPath()
	dir := filepath.Dir(path)
	os.MkdirAll(dir, 0700)

	f, err := os.Create(path)
	if err != nil {
		return
	}
	defer f.Close()

	w := bufio.NewWriter(f)
	for _, line := range history {
		line = strings.Join(strings.Fields(line), " ")
		if line != "" {
			w.WriteString(line)
			w.WriteByte('\n')
		}
	}
	w.Flush()
}

func AppendHistory(line string) {
	line = strings.Join(strings.Fields(line), " ")
	if line == "" {
		return
	}

	path := HistoryPath()
	dir := filepath.Dir(path)
	os.MkdirAll(dir, 0700)

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	defer f.Close()
	f.WriteString(line + "\n")
}
