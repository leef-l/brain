package diff

import (
	"testing"
)

func TestSplitDiffLines(t *testing.T) {
	cases := []struct {
		input string
		want  []string
	}{
		{"", nil},
		{"a\nb\nc", []string{"a", "b", "c"}},
		{"a\r\nb\r\nc", []string{"a", "b", "c"}},
		{"a\n", []string{"a"}},
		{"a\n\n", []string{"a", ""}},
	}
	for _, c := range cases {
		got := SplitDiffLines(c.input)
		if len(got) != len(c.want) {
			t.Fatalf("SplitDiffLines(%q) len=%d, want %d", c.input, len(got), len(c.want))
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("SplitDiffLines(%q)[%d]=%q, want %q", c.input, i, got[i], c.want[i])
			}
		}
	}
}

func TestDiffLines(t *testing.T) {
	old := []string{"a", "b", "c"}
	new := []string{"a", "x", "c"}
	ops := DiffLines(old, new)
	if len(ops) == 0 {
		t.Fatal("expected non-empty ops")
	}
	// Check that we have the expected operations.
	var added, removed int
	for _, op := range ops {
		switch op.Kind {
		case '+':
			added++
		case '-':
			removed++
		}
	}
	if added != 1 {
		t.Fatalf("expected 1 addition, got %d", added)
	}
	if removed != 1 {
		t.Fatalf("expected 1 removal, got %d", removed)
	}
}

func TestDiffLinesEmpty(t *testing.T) {
	ops := DiffLines(nil, nil)
	if ops != nil {
		t.Fatal("expected nil ops for empty inputs")
	}
}

func TestDiffLinesIdentical(t *testing.T) {
	lines := []string{"a", "b", "c"}
	ops := DiffLines(lines, lines)
	if len(ops) != 3 {
		t.Fatalf("expected 3 ops, got %d", len(ops))
	}
	for _, op := range ops {
		if op.Kind != ' ' {
			t.Fatalf("expected all unchanged, got %c", op.Kind)
		}
	}
}

func TestCountDiffOps(t *testing.T) {
	ops := []Op{
		{Kind: ' ', Text: "a"},
		{Kind: '-', Text: "b"},
		{Kind: '+', Text: "x"},
		{Kind: ' ', Text: "c"},
	}
	added, removed := CountDiffOps(ops)
	if added != 1 {
		t.Fatalf("expected added=1, got %d", added)
	}
	if removed != 1 {
		t.Fatalf("expected removed=1, got %d", removed)
	}
}

func TestDiffOldLabel(t *testing.T) {
	if DiffOldLabel("foo.txt", true) != "a/foo.txt" {
		t.Fatalf("expected a/foo.txt, got %s", DiffOldLabel("foo.txt", true))
	}
	if DiffOldLabel("foo.txt", false) != "/dev/null" {
		t.Fatalf("expected /dev/null, got %s", DiffOldLabel("foo.txt", false))
	}
	if DiffOldLabel("/abs/path.txt", true) != "a/abs/path.txt" {
		t.Fatalf("expected a/abs/path.txt, got %s", DiffOldLabel("/abs/path.txt", true))
	}
}

func TestDiffNewLabel(t *testing.T) {
	if DiffNewLabel("foo.txt") != "b/foo.txt" {
		t.Fatalf("expected b/foo.txt, got %s", DiffNewLabel("foo.txt"))
	}
	if DiffNewLabel("/abs/path.txt") != "b/abs/path.txt" {
		t.Fatalf("expected b/abs/path.txt, got %s", DiffNewLabel("/abs/path.txt"))
	}
}

func TestExtractFilePathFromCommand(t *testing.T) {
	cases := []struct {
		cmd  string
		want string
	}{
		{"", ""},
		{"ls -la", ""},
		{"sed -i 's/old/new/' file.txt", "file.txt"},
		{"rm file.txt", "file.txt"},
		{"tee file.txt", "file.txt"},
		{"cp src.txt dst.txt", "dst.txt"},
		{"mv src.txt dst.txt", "dst.txt"},
		{"echo hello > file.txt", "file.txt"},
		{"echo hello >> file.txt", "file.txt"},
	}
	for _, c := range cases {
		got := ExtractFilePathFromCommand(c.cmd)
		if got != c.want {
			t.Fatalf("ExtractFilePathFromCommand(%q)=%q, want %q", c.cmd, got, c.want)
		}
	}
}

func TestFallbackDiffLines(t *testing.T) {
	old := []string{"a", "b", "c"}
	new := []string{"a", "x", "c", "d"}
	ops := fallbackDiffLines(old, new)
	if len(ops) == 0 {
		t.Fatal("expected non-empty fallback ops")
	}
}
