package kernel

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeTestIndex(t *testing.T, dir string, idx MarketplaceIndex) string {
	t.Helper()
	mpDir := filepath.Join(dir, "marketplace")
	if err := os.MkdirAll(mpDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(mpDir, "index.json")
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func sampleIndex() MarketplaceIndex {
	return MarketplaceIndex{
		Version:   1,
		UpdatedAt: time.Now(),
		Entries: []MarketplaceEntry{
			{
				PackageID:    "leef-l/browser",
				Name:         "Browser Brain",
				Description:  "Web browsing and testing automation",
				Kind:         "browser",
				Version:      "1.0.0",
				Publisher:    "leef-l",
				Capabilities: []string{"web.browse", "web.assert"},
				RuntimeType:  "native",
				Downloads:    100,
				Rating:       4.5,
				Compatible:   true,
			},
			{
				PackageID:    "leef-l/data",
				Name:         "Data Brain",
				Description:  "Database and data pipeline management",
				Kind:         "data",
				Version:      "2.1.0",
				Publisher:    "leef-l",
				Capabilities: []string{"db.query", "db.migrate"},
				RuntimeType:  "mcp-backed",
				Downloads:    50,
				Rating:       4.0,
				Compatible:   true,
			},
			{
				PackageID:    "acme/security",
				Name:         "Security Suite",
				Description:  "Security auditing and vulnerability scanning",
				Kind:         "security",
				Version:      "0.9.0",
				Publisher:    "acme",
				Capabilities: []string{"security.scan", "security.audit"},
				RuntimeType:  "hybrid",
				Downloads:    30,
				Rating:       3.8,
				Compatible:   false,
			},
		},
	}
}

func TestLocalMarketplace_Search(t *testing.T) {
	dir := t.TempDir()
	path := writeTestIndex(t, dir, sampleIndex())
	mp := NewLocalMarketplace(path)
	ctx := context.Background()

	tests := []struct {
		query string
		want  int
	}{
		{"browser", 1},
		{"data", 1},
		{"brain", 2}, // "Browser Brain" and "Data Brain"
		{"security", 1},
		{"leef", 2},
		{"nonexistent", 0},
		{"", 3}, // empty query matches all
	}

	for _, tt := range tests {
		results, err := mp.Search(ctx, tt.query)
		if err != nil {
			t.Fatalf("Search(%q): %v", tt.query, err)
		}
		if len(results) != tt.want {
			t.Errorf("Search(%q) got %d results, want %d", tt.query, len(results), tt.want)
		}
	}
}

func TestLocalMarketplace_Get(t *testing.T) {
	dir := t.TempDir()
	path := writeTestIndex(t, dir, sampleIndex())
	mp := NewLocalMarketplace(path)
	ctx := context.Background()

	entry, err := mp.Get(ctx, "leef-l/browser")
	if err != nil {
		t.Fatal(err)
	}
	if entry.Name != "Browser Brain" {
		t.Errorf("got name %q, want %q", entry.Name, "Browser Brain")
	}

	_, err = mp.Get(ctx, "nonexistent/pkg")
	if err == nil {
		t.Error("expected error for nonexistent package, got nil")
	}
}

func TestLocalMarketplace_List(t *testing.T) {
	dir := t.TempDir()
	path := writeTestIndex(t, dir, sampleIndex())
	mp := NewLocalMarketplace(path)
	ctx := context.Background()

	// filter by kind
	results, err := mp.List(ctx, MarketplaceFilter{Kind: "browser"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Errorf("List(kind=browser) got %d, want 1", len(results))
	}

	// filter by publisher
	results, err = mp.List(ctx, MarketplaceFilter{Publisher: "leef-l"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Errorf("List(publisher=leef-l) got %d, want 2", len(results))
	}

	// filter by capability
	results, err = mp.List(ctx, MarketplaceFilter{Capability: "db.query"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Errorf("List(capability=db.query) got %d, want 1", len(results))
	}

	// filter by runtime type
	results, err = mp.List(ctx, MarketplaceFilter{RuntimeType: "mcp-backed"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Errorf("List(runtime=mcp-backed) got %d, want 1", len(results))
	}

	// empty filter returns all
	results, err = mp.List(ctx, MarketplaceFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Errorf("List(empty) got %d, want 3", len(results))
	}
}

func TestLocalMarketplace_MissingIndex(t *testing.T) {
	mp := NewLocalMarketplace("/tmp/nonexistent/path/index.json")
	ctx := context.Background()

	_, err := mp.Search(ctx, "test")
	if err == nil {
		t.Error("expected error for missing index, got nil")
	}
}
