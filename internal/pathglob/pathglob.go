package pathglob

import (
	"fmt"
	"path"
	"strings"
)

// Validate reports whether pattern is a syntactically valid slash-based glob.
// It supports doublestar segments ("**") in addition to path.Match segment
// syntax. A doublestar segment must appear by itself between separators.
func Validate(pattern string) error {
	pattern = normalize(pattern)
	if pattern == "" {
		return fmt.Errorf("empty pattern")
	}
	for _, segment := range strings.Split(pattern, "/") {
		if segment == "" || segment == "**" {
			continue
		}
		if _, err := path.Match(segment, "probe"); err != nil {
			return err
		}
	}
	return nil
}

// Match reports whether target matches pattern. Both inputs use slash
// separators. Single-star segments do not cross path separators; doublestar
// segments ("**") match zero or more full path segments.
func Match(pattern, target string) (bool, error) {
	pattern = normalize(pattern)
	target = normalize(target)
	if err := Validate(pattern); err != nil {
		return false, err
	}

	patSegs := split(pattern)
	tgtSegs := split(target)
	memo := make(map[[2]int]bool)
	seen := make(map[[2]int]bool)
	return matchSegments(patSegs, tgtSegs, 0, 0, memo, seen), nil
}

func normalize(v string) string {
	v = strings.TrimSpace(strings.ReplaceAll(v, "\\", "/"))
	v = path.Clean(v)
	if v == "." {
		return ""
	}
	return strings.TrimPrefix(v, "./")
}

func split(v string) []string {
	if v == "" {
		return nil
	}
	return strings.Split(v, "/")
}

func matchSegments(pattern, target []string, pi, ti int, memo map[[2]int]bool, seen map[[2]int]bool) bool {
	key := [2]int{pi, ti}
	if seen[key] {
		return memo[key]
	}
	seen[key] = true

	switch {
	case pi == len(pattern):
		memo[key] = ti == len(target)
		return memo[key]
	case pattern[pi] == "**":
		if matchSegments(pattern, target, pi+1, ti, memo, seen) {
			memo[key] = true
			return true
		}
		if ti < len(target) && matchSegments(pattern, target, pi, ti+1, memo, seen) {
			memo[key] = true
			return true
		}
		memo[key] = false
		return false
	case ti >= len(target):
		memo[key] = false
		return false
	default:
		ok, err := path.Match(pattern[pi], target[ti])
		if err != nil || !ok {
			memo[key] = false
			return false
		}
		memo[key] = matchSegments(pattern, target, pi+1, ti+1, memo, seen)
		return memo[key]
	}
}
