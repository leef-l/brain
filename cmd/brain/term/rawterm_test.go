package term

import (
	"io"
	"os"
	"testing"
)

func TestLineReadSessionConsumeMuteEcho(t *testing.T) {
	session := NewLineReadSession(&Keybindings{}, 0)
	session.MuteEcho = true

	stdout := captureStdout(t, func() {
		line, action, done, err := session.Consume([]byte("abc"))
		if err != nil {
			t.Fatalf("Consume returned error: %v", err)
		}
		if done {
			t.Fatalf("done = true, want false")
		}
		if action != ActionEnter {
			t.Fatalf("action = %v, want %v", action, ActionEnter)
		}
		if line != "" {
			t.Fatalf("line = %q, want empty", line)
		}
	})

	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	if got := session.Ed.String(); got != "abc" {
		t.Fatalf("editor content = %q, want abc", got)
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	defer func() {
		os.Stdout = origStdout
	}()

	fn()

	if err := w.Close(); err != nil {
		t.Fatalf("close write pipe: %v", err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("io.ReadAll: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("close read pipe: %v", err)
	}
	return string(out)
}
