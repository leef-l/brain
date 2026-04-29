package kernel

import (
	"testing"

	"github.com/leef-l/brain/sdk/agent"
)

// ---------------------------------------------------------------------------
// ParseCapabilityTag
// ---------------------------------------------------------------------------

func TestParseCapabilityTag_Function(t *testing.T) {
	tag := ParseCapabilityTag("trading.execute")
	if tag.Category != "function" {
		t.Fatalf("expected category function, got %s", tag.Category)
	}
	if tag.Primary != "trading" {
		t.Fatalf("expected primary trading, got %s", tag.Primary)
	}
	if tag.Sub != "execute" {
		t.Fatalf("expected sub execute, got %s", tag.Sub)
	}
	if tag.Raw != "trading.execute" {
		t.Fatalf("expected raw trading.execute, got %s", tag.Raw)
	}
}

func TestParseCapabilityTag_Domain(t *testing.T) {
	tag := ParseCapabilityTag("domain.crypto")
	if tag.Category != "domain" {
		t.Fatalf("expected category domain, got %s", tag.Category)
	}
	if tag.Primary != "crypto" {
		t.Fatalf("expected primary crypto, got %s", tag.Primary)
	}
	if tag.Sub != "" {
		t.Fatalf("expected empty sub, got %s", tag.Sub)
	}
}

func TestParseCapabilityTag_Resource(t *testing.T) {
	tag := ParseCapabilityTag("resource.exchange_api")
	if tag.Category != "resource" {
		t.Fatalf("expected category resource, got %s", tag.Category)
	}
	if tag.Primary != "exchange_api" {
		t.Fatalf("expected primary exchange_api, got %s", tag.Primary)
	}
}

func TestParseCapabilityTag_Mode(t *testing.T) {
	tag := ParseCapabilityTag("mode.background")
	if tag.Category != "mode" {
		t.Fatalf("expected category mode, got %s", tag.Category)
	}
	if tag.Primary != "background" {
		t.Fatalf("expected primary background, got %s", tag.Primary)
	}
}

// ---------------------------------------------------------------------------
// CapabilityIndex
// ---------------------------------------------------------------------------

func setupTestIndex() *CapabilityIndex {
	idx := NewCapabilityIndex()
	idx.AddBrain(agent.KindCode, []string{
		"coding.write",
		"coding.review",
		"domain.devops",
		"resource.filesystem",
		"mode.streaming",
	})
	idx.AddBrain(agent.KindBrowser, []string{
		"browsing.navigate",
		"browsing.extract",
		"domain.web",
		"mode.streaming",
	})
	idx.AddBrain(agent.KindVerifier, []string{
		"testing.run",
		"coding.review",
		"domain.devops",
		"mode.background",
	})
	return idx
}

func TestCapabilityIndex_AddAndFindByTag(t *testing.T) {
	idx := setupTestIndex()

	// 精确匹配
	results := idx.FindByTag("coding.review")
	if len(results) != 2 {
		t.Fatalf("expected 2 brains for coding.review, got %d", len(results))
	}

	results = idx.FindByTag("browsing.navigate")
	if len(results) != 1 || results[0] != agent.KindBrowser {
		t.Fatalf("expected [browser], got %v", results)
	}

	// 不存在的标签
	results = idx.FindByTag("nonexistent.tag")
	if len(results) != 0 {
		t.Fatalf("expected 0, got %d", len(results))
	}
}

func TestCapabilityIndex_RemoveBrain(t *testing.T) {
	idx := setupTestIndex()

	idx.RemoveBrain(agent.KindCode)

	results := idx.FindByTag("coding.write")
	if len(results) != 0 {
		t.Fatalf("expected 0 after remove, got %d", len(results))
	}

	// coding.review 应该只剩 verifier
	results = idx.FindByTag("coding.review")
	if len(results) != 1 || results[0] != agent.KindVerifier {
		t.Fatalf("expected [verifier], got %v", results)
	}

	all := idx.AllBrains()
	if len(all) != 2 {
		t.Fatalf("expected 2 brains remaining, got %d", len(all))
	}
}

func TestCapabilityIndex_FindByPrefix(t *testing.T) {
	idx := setupTestIndex()

	results := idx.FindByPrefix("coding.")
	if len(results) != 2 {
		t.Fatalf("expected 2 brains for prefix coding., got %d: %v", len(results), results)
	}

	results = idx.FindByPrefix("mode.")
	if len(results) != 3 {
		t.Fatalf("expected 3 brains for prefix mode., got %d: %v", len(results), results)
	}

	results = idx.FindByPrefix("nonexistent.")
	if len(results) != 0 {
		t.Fatalf("expected 0 for nonexistent prefix, got %d", len(results))
	}
}

func TestCapabilityIndex_FindByCategory(t *testing.T) {
	idx := setupTestIndex()

	results := idx.FindByCategory("domain")
	if len(results) != 3 {
		t.Fatalf("expected 3 brains with domain tags, got %d: %v", len(results), results)
	}

	results = idx.FindByCategory("mode")
	if len(results) != 3 {
		t.Fatalf("expected 3 brains with mode tags, got %d: %v", len(results), results)
	}

	results = idx.FindByCategory("resource")
	if len(results) != 1 {
		t.Fatalf("expected 1 brain with resource tags, got %d: %v", len(results), results)
	}

	results = idx.FindByCategory("function")
	if len(results) != 3 {
		t.Fatalf("expected 3 brains with function tags, got %d: %v", len(results), results)
	}
}

func TestCapabilityIndex_AllBrains(t *testing.T) {
	idx := setupTestIndex()
	all := idx.AllBrains()
	if len(all) != 3 {
		t.Fatalf("expected 3 brains, got %d", len(all))
	}
}

// ---------------------------------------------------------------------------
// CapabilityMatcher
// ---------------------------------------------------------------------------

func TestMatcher_HardFilterExcludes(t *testing.T) {
	idx := setupTestIndex()
	matcher := NewCapabilityMatcher(idx)

	// 要求 coding.write + testing.run，没有 brain 同时具备
	results := matcher.Match(MatchRequest{
		Required: []string{"coding.write", "testing.run"},
	})
	if len(results) != 0 {
		t.Fatalf("expected 0 candidates, got %d: %v", len(results), results)
	}
}

func TestMatcher_HardFilterPasses(t *testing.T) {
	idx := setupTestIndex()
	matcher := NewCapabilityMatcher(idx)

	results := matcher.Match(MatchRequest{
		Required: []string{"coding.write", "domain.devops"},
	})
	if len(results) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(results))
	}
	if results[0].BrainKind != agent.KindCode {
		t.Fatalf("expected code brain, got %s", results[0].BrainKind)
	}
	if results[0].HardScore != 1.0 {
		t.Fatalf("expected HardScore 1.0, got %f", results[0].HardScore)
	}
}

func TestMatcher_SoftScoring(t *testing.T) {
	idx := setupTestIndex()
	matcher := NewCapabilityMatcher(idx)

	results := matcher.Match(MatchRequest{
		Required:  []string{"coding.review"},
		Preferred: []string{"domain.devops", "mode.streaming"},
	})

	// code 和 verifier 都有 coding.review
	if len(results) != 2 {
		t.Fatalf("expected 2 candidates, got %d", len(results))
	}

	// code 有 domain.devops + mode.streaming (2/2 = 1.0)
	// verifier 有 domain.devops 但无 mode.streaming (1/2 = 0.5)
	var codeResult, verifierResult MatchResult
	for _, r := range results {
		switch r.BrainKind {
		case agent.KindCode:
			codeResult = r
		case agent.KindVerifier:
			verifierResult = r
		}
	}

	if codeResult.SoftScore != 1.0 {
		t.Fatalf("code SoftScore: expected 1.0, got %f", codeResult.SoftScore)
	}
	if verifierResult.SoftScore != 0.5 {
		t.Fatalf("verifier SoftScore: expected 0.5, got %f", verifierResult.SoftScore)
	}
}

func TestMatcher_CombinedSortOrder(t *testing.T) {
	idx := setupTestIndex()
	matcher := NewCapabilityMatcher(idx)

	results := matcher.Match(MatchRequest{
		Required:  []string{"coding.review"},
		Preferred: []string{"domain.devops", "mode.streaming"},
	})

	if len(results) < 2 {
		t.Fatalf("expected >= 2 results, got %d", len(results))
	}

	// code: 1.0*0.6 + 1.0*0.4 = 1.0
	// verifier: 1.0*0.6 + 0.5*0.4 = 0.8
	if results[0].BrainKind != agent.KindCode {
		t.Fatalf("expected code first, got %s", results[0].BrainKind)
	}
	if results[1].BrainKind != agent.KindVerifier {
		t.Fatalf("expected verifier second, got %s", results[1].BrainKind)
	}

	// 验证具体分数
	const eps = 1e-9
	if diff := results[0].CombinedScore - 1.0; diff > eps || diff < -eps {
		t.Fatalf("code combined: expected 1.0, got %f", results[0].CombinedScore)
	}
	if diff := results[1].CombinedScore - 0.8; diff > eps || diff < -eps {
		t.Fatalf("verifier combined: expected 0.8, got %f", results[1].CombinedScore)
	}
}

func TestMatcher_EmptyRequiredReturnsAll(t *testing.T) {
	idx := setupTestIndex()
	matcher := NewCapabilityMatcher(idx)

	results := matcher.Match(MatchRequest{})
	if len(results) != 3 {
		t.Fatalf("expected all 3 brains, got %d", len(results))
	}
}

func TestMatcher_NoCandidatesReturnsEmpty(t *testing.T) {
	idx := NewCapabilityIndex()
	matcher := NewCapabilityMatcher(idx)

	results := matcher.Match(MatchRequest{
		Required: []string{"anything"},
	})
	if len(results) != 0 {
		t.Fatalf("expected 0, got %d", len(results))
	}
}

func TestMatcher_EmptyIndexReturnsEmpty(t *testing.T) {
	idx := NewCapabilityIndex()
	matcher := NewCapabilityMatcher(idx)

	results := matcher.Match(MatchRequest{})
	if len(results) != 0 {
		t.Fatalf("expected 0 from empty index, got %d", len(results))
	}
}

func TestMatcher_BestMatch(t *testing.T) {
	idx := setupTestIndex()
	matcher := NewCapabilityMatcher(idx)

	best, ok := matcher.BestMatch(MatchRequest{
		Required:  []string{"coding.review"},
		Preferred: []string{"domain.devops", "mode.streaming"},
	})
	if !ok {
		t.Fatal("expected BestMatch to succeed")
	}
	if best.BrainKind != agent.KindCode {
		t.Fatalf("expected code brain, got %s", best.BrainKind)
	}
	if best.CombinedScore != 1.0 {
		t.Fatalf("expected CombinedScore=1.0, got %f", best.CombinedScore)
	}
}

func TestMatcher_BestMatchNoCandidates(t *testing.T) {
	idx := NewCapabilityIndex()
	matcher := NewCapabilityMatcher(idx)

	_, ok := matcher.BestMatch(MatchRequest{Required: []string{"nonexistent"}})
	if ok {
		t.Fatal("expected BestMatch to fail on empty index")
	}
}

// ---------------------------------------------------------------------------
// Version parsing & matching
// ---------------------------------------------------------------------------

func TestParseCapabilityTag_WithVersion(t *testing.T) {
	cases := []struct {
		raw      string
		category string
		primary  string
		sub      string
		version  string
	}{
		{"function.write_file.v2", "function", "function", "write_file", "v2"},
		{"domain.code.v2", "domain", "code", "", "v2"},
		{"resource.api.v3", "resource", "api", "", "v3"},
		{"function.review.code.v3", "function", "function", "review", "v3"},
		{"mode.background", "mode", "background", "", ""},
		{"trading.execute", "function", "trading", "execute", ""},
	}
	for _, c := range cases {
		tag := ParseCapabilityTag(c.raw)
		if tag.Category != c.category {
			t.Fatalf("ParseCapabilityTag(%q) category=%s want %s", c.raw, tag.Category, c.category)
		}
		if tag.Primary != c.primary {
			t.Fatalf("ParseCapabilityTag(%q) primary=%s want %s", c.raw, tag.Primary, c.primary)
		}
		if tag.Sub != c.sub {
			t.Fatalf("ParseCapabilityTag(%q) sub=%s want %s", c.raw, tag.Sub, c.sub)
		}
		if tag.Version != c.version {
			t.Fatalf("ParseCapabilityTag(%q) version=%s want %s", c.raw, tag.Version, c.version)
		}
	}
}

func TestMatcher_VersionedHardFilter(t *testing.T) {
	idx := NewCapabilityIndex()
	idx.AddBrain(agent.KindCode, []string{
		"function.write_file.v2",
		"function.read_file.v1",
	})
	idx.AddBrain(agent.KindBrowser, []string{
		"function.write_file.v1",
		"function.screenshot.v3",
	})

	matcher := NewCapabilityMatcher(idx)

	// Request v2 write_file — only code brain (v2) should match.
	results := matcher.Match(MatchRequest{Required: []string{"function.write_file.v2"}})
	if len(results) != 1 {
		t.Fatalf("expected 1 match for v2, got %d", len(results))
	}
	if results[0].BrainKind != agent.KindCode {
		t.Fatalf("expected code brain for v2, got %s", results[0].BrainKind)
	}

	// Request v1 write_file — both code (v1) and browser (v1) should match,
	// but code actually has v2. Code should still match because v2 >= v1.
	results = matcher.Match(MatchRequest{Required: []string{"function.write_file.v1"}})
	if len(results) != 2 {
		t.Fatalf("expected 2 matches for v1 (code v2 >= v1, browser v1), got %d", len(results))
	}

	// Request v3 screenshot — only browser brain (v3) should match.
	results = matcher.Match(MatchRequest{Required: []string{"function.screenshot.v3"}})
	if len(results) != 1 {
		t.Fatalf("expected 1 match for v3 screenshot, got %d", len(results))
	}
	if results[0].BrainKind != agent.KindBrowser {
		t.Fatalf("expected browser brain for v3, got %s", results[0].BrainKind)
	}
}

func TestMatcher_NoVersionMatchesAnyVersion(t *testing.T) {
	idx := NewCapabilityIndex()
	idx.AddBrain(agent.KindCode, []string{
		"function.write_file.v2",
	})
	matcher := NewCapabilityMatcher(idx)

	// Request without version should match brain that has versioned capability.
	results := matcher.Match(MatchRequest{Required: []string{"function.write_file"}})
	if len(results) != 1 {
		t.Fatalf("expected 1 match for unversioned request, got %d", len(results))
	}
}

func TestParseVersion(t *testing.T) {
	cases := []struct {
		s    string
		want versionTuple
	}{
		{"v2", versionTuple{2, 0, 0}},
		{"v1.3", versionTuple{1, 3, 0}},
		{"v2.1.0", versionTuple{2, 1, 0}},
		{"V10.5.3", versionTuple{10, 5, 3}},
	}
	for _, c := range cases {
		got := parseVersion(c.s)
		if got != c.want {
			t.Fatalf("parseVersion(%q) = %+v, want %+v", c.s, got, c.want)
		}
	}
}

func TestVersionGTE(t *testing.T) {
	cases := []struct {
		a, b versionTuple
		want bool
	}{
		{versionTuple{2, 0, 0}, versionTuple{1, 0, 0}, true},
		{versionTuple{1, 3, 0}, versionTuple{1, 2, 0}, true},
		{versionTuple{1, 2, 1}, versionTuple{1, 2, 0}, true},
		{versionTuple{1, 2, 0}, versionTuple{1, 2, 0}, true},
		{versionTuple{1, 1, 0}, versionTuple{1, 2, 0}, false},
		{versionTuple{0, 0, 0}, versionTuple{1, 0, 0}, false},
	}
	for _, c := range cases {
		got := versionGTE(c.a, c.b)
		if got != c.want {
			t.Fatalf("versionGTE(%+v, %+v) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}
