package pathglob

import "testing"

func TestValidate_ValidPatterns(t *testing.T) {
	valid := []string{
		"*.go",
		"src/**/*.ts",
		"**",
		"a/b/c",
		"dir/file.txt",
		"[abc].go",
		"dir/**/file.txt",
		"a/*/b/**/c",
	}
	for _, p := range valid {
		if err := Validate(p); err != nil {
			t.Errorf("Validate(%q) = %v, want nil", p, err)
		}
	}
}

func TestValidate_InvalidPatterns(t *testing.T) {
	invalid := []string{
		"",
		"   ",
		"[invalid",
	}
	for _, p := range invalid {
		if err := Validate(p); err == nil {
			t.Errorf("Validate(%q) = nil, want error", p)
		}
	}
}

func TestMatch_ExactPath(t *testing.T) {
	ok, err := Match("src/main.go", "src/main.go")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("expected match for exact path")
	}
}

func TestMatch_SingleStar(t *testing.T) {
	cases := []struct {
		pattern, target string
		want            bool
	}{
		{"*.go", "main.go", true},
		{"*.go", "src/main.go", false}, // single star doesn't cross /
		{"src/*.go", "src/main.go", true},
		{"src/*.go", "src/sub/main.go", false},
	}
	for _, c := range cases {
		got, err := Match(c.pattern, c.target)
		if err != nil {
			t.Fatalf("Match(%q, %q) error: %v", c.pattern, c.target, err)
		}
		if got != c.want {
			t.Errorf("Match(%q, %q) = %v, want %v", c.pattern, c.target, got, c.want)
		}
	}
}

func TestMatch_DoubleStar(t *testing.T) {
	cases := []struct {
		pattern, target string
		want            bool
	}{
		{"**/*.go", "main.go", true},
		{"**/*.go", "src/main.go", true},
		{"**/*.go", "a/b/c/d.go", true},
		{"src/**", "src/a", true},
		{"src/**", "src/a/b/c", true},
		{"src/**/test.go", "src/test.go", true},
		{"src/**/test.go", "src/a/test.go", true},
		{"src/**/test.go", "src/a/b/test.go", true},
		{"**", "anything", true},
		{"**", "a/b/c", true},
	}
	for _, c := range cases {
		got, err := Match(c.pattern, c.target)
		if err != nil {
			t.Fatalf("Match(%q, %q) error: %v", c.pattern, c.target, err)
		}
		if got != c.want {
			t.Errorf("Match(%q, %q) = %v, want %v", c.pattern, c.target, got, c.want)
		}
	}
}

func TestMatch_NoMatch(t *testing.T) {
	cases := []struct {
		pattern, target string
	}{
		{"*.go", "main.rs"},
		{"src/*.go", "lib/main.go"},
		{"a/b", "a/c"},
	}
	for _, c := range cases {
		got, err := Match(c.pattern, c.target)
		if err != nil {
			t.Fatalf("Match(%q, %q) error: %v", c.pattern, c.target, err)
		}
		if got {
			t.Errorf("Match(%q, %q) = true, want false", c.pattern, c.target)
		}
	}
}

func TestMatch_InvalidPattern(t *testing.T) {
	_, err := Match("[bad", "anything")
	if err == nil {
		t.Error("expected error for invalid pattern")
	}
}

func TestMatch_CharacterClass(t *testing.T) {
	ok, err := Match("[abc].go", "a.go")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("expected match for character class")
	}

	ok, err = Match("[abc].go", "d.go")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("expected no match for character class")
	}
}

func TestMatch_QuestionMark(t *testing.T) {
	ok, err := Match("?.go", "a.go")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("expected match for ? wildcard")
	}
}

func TestNormalize(t *testing.T) {
	cases := []struct {
		input, want string
	}{
		{"a\\b\\c", "a/b/c"},
		{"./src/main.go", "src/main.go"},
		{"  src/main.go  ", "src/main.go"},
		{".", ""},
		{"a/./b/../c", "a/c"},
	}
	for _, c := range cases {
		got := normalize(c.input)
		if got != c.want {
			t.Errorf("normalize(%q) = %q, want %q", c.input, got, c.want)
		}
	}
}

func TestSplit(t *testing.T) {
	got := split("")
	if got != nil {
		t.Errorf("split(\"\") = %v, want nil", got)
	}
	got = split("a/b/c")
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Errorf("split(\"a/b/c\") = %v", got)
	}
}
