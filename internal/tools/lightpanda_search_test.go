package tools

import (
	"fmt"
	"strings"
	"testing"
)

func TestParseSearchResults_DDGMarkdown(t *testing.T) {
	// Simulate DuckDuckGo markdown output from lightpanda fetch --dump markdown
	md := `# Search Results

[Example Domain](https://example.com/)
Example Domain — This domain is for use in illustrative examples in documents.

[Go Programming Language](https://go.dev/)
Go is an open source programming language supported by Google.

[DuckDuckGo - Privacy](https://spreadprivacy.com/)
DuckDuckGo protects your privacy online.
`
	results := parseSearchMarkdown(md, "duckduckgo", 5)
	if len(results) < 2 {
		t.Fatalf("expected at least 2 results, got %d", len(results))
	}
	if results[0].URL != "https://example.com/" {
		t.Errorf("first result URL = %q, want https://example.com/", results[0].URL)
	}
	if results[0].Title != "Example Domain" {
		t.Errorf("first result title = %q, want 'Example Domain'", results[0].Title)
	}
	if !strings.Contains(results[0].Snippet, "illustrative examples") {
		t.Errorf("first result snippet missing expected text: %q", results[0].Snippet)
	}
}

func TestParseSearchResults_BingMarkdown(t *testing.T) {
	md := `Search results

1. [Go Language](https://go.dev/) — Build fast, reliable software at scale.
2. [Wikipedia: Go](https://en.wikipedia.org/wiki/Go_(programming_language)) — Go is a statically typed compiled programming language.
3. [GitHub - golang/go](https://github.com/golang/go) — The Go programming language repository.
`
	results := parseSearchMarkdown(md, "bing", 5)
	if len(results) < 2 {
		t.Fatalf("expected at least 2 results, got %d", len(results))
	}
	if results[0].URL != "https://go.dev/" {
		t.Errorf("first result URL = %q, want https://go.dev/", results[0].URL)
	}
	if results[0].Title != "Go Language" {
		t.Errorf("first result title = %q, want 'Go Language'", results[0].Title)
	}
}

func TestParseSearchResults_BraveMarkdown(t *testing.T) {
	md := `## Web Results

[Go Programming Language](https://go.dev/)
Go is an open source programming language supported by Google with built-in concurrency.

[The Go Blog](https://go.dev/blog/)
Official blog for the Go programming language.
`
	results := parseSearchMarkdown(md, "brave", 5)
	if len(results) < 1 {
		t.Fatalf("expected at least 1 result, got %d", len(results))
	}
	if results[0].URL != "https://go.dev/" {
		t.Errorf("first result URL = %q", results[0].URL)
	}
}

func TestParseSearchResults_LooseURLs(t *testing.T) {
	// When markdown links don't work, fall back to bare URL parsing.
	md := `Search Results

https://example.com/page1
Some text about this page.

https://example.com/page2
More text about second page.
`
	results := parseSearchMarkdown(md, "duckduckgo", 5)
	if len(results) < 2 {
		t.Fatalf("expected at least 2 results, got %d", len(results))
	}
	if results[0].URL != "https://example.com/page1" {
		t.Errorf("first result URL = %q", results[0].URL)
	}
}

func TestParseSearchResults_Empty(t *testing.T) {
	tests := []struct {
		name string
		md   string
	}{
		{"empty string", ""},
		{"no links", "Just some plain text with no URLs or links at all."},
		{"only headers", "# Header\n## Subheader\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results := parseSearchMarkdown(tt.md, "duckduckgo", 5)
			if len(results) != 0 {
				t.Errorf("expected 0 results, got %d", len(results))
			}
		})
	}
}

func TestFilterSearchEngineSelfLinks(t *testing.T) {
	results := []SearchResult{
		{Title: "Example", URL: "https://example.com/"},
		{Title: "DDG Help", URL: "https://duckduckgo.com/settings"},
		{Title: "DDG Subdomain", URL: "https://help.duckduckgo.com/"},
		{Title: "Bing Settings", URL: "https://www.bing.com/account"},
		{Title: "Brave About", URL: "https://brave.com/about"},
		{Title: "Search Result", URL: "https://example.org/"},
	}

	filtered := filterSearchEngineSelfLinks(results, "duckduckgo", defaultBlockedHosts("duckduckgo"))
	// duckduckgo blocked hosts only filter duckduckgo.com and *.duckduckgo.com
	// bing.com and brave.com are NOT blocked by duckduckgo's list
	if len(filtered) != 4 {
		t.Fatalf("expected 4 results after filtering (only DDG links removed), got %d", len(filtered))
	}
	if filtered[0].URL != "https://example.com/" {
		t.Errorf("expected first result to be example.com, got %q", filtered[0].URL)
	}
	if filtered[1].URL != "https://www.bing.com/account" {
		t.Errorf("expected second result to be bing.com, got %q", filtered[1].URL)
	}
	if filtered[2].URL != "https://brave.com/about" {
		t.Errorf("expected third result to be brave.com, got %q", filtered[2].URL)
	}
	if filtered[3].URL != "https://example.org/" {
		t.Errorf("expected fourth result to be example.org, got %q", filtered[3].URL)
	}
}

func TestFilterSearchEngineSelfLinks_Bing(t *testing.T) {
	results := []SearchResult{
		{Title: "Example", URL: "https://example.com/"},
		{Title: "Bing Settings", URL: "https://www.bing.com/settings"},
		{Title: "Bing Sub", URL: "https://login.bing.com/"},
	}
	filtered := filterSearchEngineSelfLinks(results, "bing", defaultBlockedHosts("bing"))
	if len(filtered) != 1 {
		t.Fatalf("expected 1 result, got %d", len(filtered))
	}
	if filtered[0].URL != "https://example.com/" {
		t.Errorf("expected example.com, got %q", filtered[0].URL)
	}
}

func TestBuildSearchURL_DDG(t *testing.T) {
	u, err := buildSearchURL("duckduckgo", "golang tutorial")
	if err != nil {
		t.Fatalf("buildSearchURL: %v", err)
	}
	if !strings.Contains(u, "duckduckgo.com") {
		t.Errorf("expected duckduckgo.com in URL, got %q", u)
	}
	if !strings.Contains(u, "golang+tutorial") && !strings.Contains(u, "golang%20tutorial") && !strings.Contains(u, "golang+tutorial") {
		t.Errorf("expected query in URL, got %q", u)
	}
}

func TestBuildSearchURL_Bing(t *testing.T) {
	u, err := buildSearchURL("bing", "golang")
	if err != nil {
		t.Fatalf("buildSearchURL: %v", err)
	}
	if !strings.Contains(u, "bing.com") {
		t.Errorf("expected bing.com in URL, got %q", u)
	}
}

func TestBuildSearchURL_Brave(t *testing.T) {
	u, err := buildSearchURL("brave", "golang")
	if err != nil {
		t.Fatalf("buildSearchURL: %v", err)
	}
	if !strings.Contains(u, "search.brave.com") {
		t.Errorf("expected search.brave.com in URL, got %q", u)
	}
}

func TestBuildSearchURL_CustomTemplate(t *testing.T) {
	template := "https://custom.search.com/search?q={{query}}"
	u, err := buildSearchURLWithTemplate(template, "test query")
	if err != nil {
		t.Fatalf("buildSearchURLWithTemplate: %v", err)
	}
	if !strings.Contains(u, "custom.search.com") {
		t.Errorf("expected custom.search.com in URL, got %q", u)
	}
	if !strings.Contains(u, "test") {
		t.Errorf("expected 'test' in URL, got %q", u)
	}
}

func TestBuildSearchURL_EmptyQuery(t *testing.T) {
	_, err := buildSearchURL("duckduckgo", "")
	if err == nil {
		t.Fatal("expected error for empty query")
	}
}

func TestDefaultBlockedHosts(t *testing.T) {
	tests := []struct {
		engine string
		host   string
		want   bool
	}{
		{"duckduckgo", "duckduckgo.com", true},
		{"duckduckgo", "help.duckduckgo.com", true},
		{"duckduckgo", "example.com", false},
		{"bing", "bing.com", true},
		{"bing", "login.bing.com", true},
		{"bing", "example.com", false},
		{"brave", "search.brave.com", true},
		{"brave", "example.com", false},
	}
	for _, tt := range tests {
		t.Run(tt.engine+"/"+tt.host, func(t *testing.T) {
			blocked := defaultBlockedHosts(tt.engine)
			got := isHostBlocked(tt.host, blocked)
			if got != tt.want {
				t.Errorf("isHostBlocked(%q, %q) = %v, want %v", tt.host, tt.engine, got, tt.want)
			}
		})
	}
}

func TestParseMarkdown_MaxResults(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 20; i++ {
		fmt.Fprintf(&b, "[Result %d](https://example.com/page%d)\nSnippet for page %d.\n\n", i, i, i)
	}
	results := parseSearchMarkdown(b.String(), "duckduckgo", 5)
	if len(results) > 5 {
		t.Errorf("expected max 5 results, got %d", len(results))
	}
}

func TestTrimSearchResultSnippet(t *testing.T) {
	longSnippet := strings.Repeat("a", 500)
	results := []SearchResult{
		{Title: "Test", URL: "https://example.com/", Snippet: longSnippet},
	}
	trimmed := trimSearchResults(results, 10)
	if len(trimmed) != 1 {
		t.Fatalf("expected 1 result, got %d", len(trimmed))
	}
	if len(trimmed[0].Snippet) > 400 {
		t.Errorf("expected snippet <= 400 chars, got %d", len(trimmed[0].Snippet))
	}
}
