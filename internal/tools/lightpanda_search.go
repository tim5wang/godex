package tools

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

// LightpandaSearchProvider implements BrowserSearchProvider using the lightpanda CLI binary.
type LightpandaSearchProvider struct {
	binary        *LightpandaBinary
	engine        string // "duckduckgo" | "bing" | "brave"
	customTemplate string // overrides engine if set
	waitNetworkMS int
	obeyRobots    bool
	logLevel      string
}

// NewLightpandaSearchProvider creates a search provider backed by a lightpanda binary.
func NewLightpandaSearchProvider(binary *LightpandaBinary, engine string, customTemplate string, waitNetworkMS int, obeyRobots bool, logLevel string) *LightpandaSearchProvider {
	engine = strings.ToLower(strings.TrimSpace(engine))
	if engine == "" {
		engine = "duckduckgo"
	}
	if waitNetworkMS <= 0 {
		waitNetworkMS = 1500
	}
	if logLevel == "" {
		logLevel = "warn"
	}
	return &LightpandaSearchProvider{
		binary:         binary,
		engine:         engine,
		customTemplate: strings.TrimSpace(customTemplate),
		waitNetworkMS:  waitNetworkMS,
		obeyRobots:     obeyRobots,
		logLevel:       logLevel,
	}
}

// BrowserSearch implements BrowserSearchProvider. It fetches a search engine page via lightpanda CLI
// and parses the markdown output into SearchResult items.
func (p *LightpandaSearchProvider) BrowserSearch(ctx context.Context, sessionID, query string, maxResults int) ([]SearchResult, error) {
	if p == nil || p.binary == nil {
		return nil, fmt.Errorf("lightpanda search provider is unavailable")
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("lightpanda search requires query")
	}
	if maxResults <= 0 {
		maxResults = defaultSearchResults
	}

	searchURL, err := p.buildURL(query)
	if err != nil {
		return nil, fmt.Errorf("lightpanda search URL: %w", err)
	}

	opts := []FetchOption{
		WithFetchWaitUntil("networkidle"),
		WithFetchWaitMS(p.waitNetworkMS),
		WithFetchLogLevel(p.logLevel),
	}
	if p.obeyRobots {
		opts = append(opts, WithFetchObeyRobots())
	}

	md, err := p.binary.FetchDump(ctx, searchURL, "markdown", opts...)
	if err != nil {
		return nil, fmt.Errorf("lightpanda search fetch: %w", err)
	}

	results := parseSearchMarkdown(md, p.engine, maxResults*3)
	results = filterSearchEngineSelfLinks(results, p.engine, defaultBlockedHosts(p.engine))
	return trimSearchResults(results, maxResults), nil
}

func (p *LightpandaSearchProvider) buildURL(query string) (string, error) {
	if p.customTemplate != "" {
		return buildSearchURLWithTemplate(p.customTemplate, query)
	}
	return buildSearchURL(p.engine, query)
}

// buildSearchURL constructs a search engine URL for the given engine and query.
func buildSearchURL(engine, query string) (string, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return "", fmt.Errorf("search query is empty")
	}
	encoded := url.QueryEscape(query)
	switch strings.ToLower(strings.TrimSpace(engine)) {
	case "duckduckgo", "ddg":
		return "https://duckduckgo.com/?q=" + encoded + "&ia=web", nil
	case "bing":
		return "https://www.bing.com/search?q=" + encoded, nil
	case "brave":
		return "https://search.brave.com/search?q=" + encoded + "&source=web", nil
	default:
		return "https://duckduckgo.com/?q=" + encoded + "&ia=web", nil
	}
}

// buildSearchURLWithTemplate fills {{query}} in a custom URL template.
func buildSearchURLWithTemplate(template, query string) (string, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return "", fmt.Errorf("search query is empty")
	}
	template = strings.TrimSpace(template)
	if template == "" {
		return "", fmt.Errorf("search URL template is empty")
	}
	encoded := url.QueryEscape(query)
	result := strings.ReplaceAll(template, "{{query}}", encoded)
	result = strings.ReplaceAll(result, "{{query_raw}}", query)
	parsed, err := url.Parse(result)
	if err != nil {
		return "", fmt.Errorf("invalid search URL template result: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("search URL template must produce http or https URL")
	}
	if parsed.Hostname() == "" {
		return "", fmt.Errorf("search URL template produced URL without hostname")
	}
	return parsed.String(), nil
}

// defaultBlockedHosts returns hostnames that should be filtered from search results
// because they belong to the search engine itself.
func defaultBlockedHosts(engine string) []string {
	switch strings.ToLower(strings.TrimSpace(engine)) {
	case "duckduckgo", "ddg":
		return []string{"duckduckgo.com", "*.duckduckgo.com"}
	case "bing":
		return []string{"bing.com", "*.bing.com", "microsoft.com", "*.microsoft.com"}
	case "brave":
		return []string{"search.brave.com", "*.search.brave.com", "brave.com"}
	default:
		return nil
	}
}

// isHostBlocked checks if a hostname matches any of the blocked patterns.
func isHostBlocked(host string, blocked []string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	for _, pattern := range blocked {
		pattern = strings.ToLower(strings.TrimSpace(pattern))
		if pattern == "" {
			continue
		}
		if strings.HasPrefix(pattern, "*.") {
			// Wildcard subdomain: "*.example.com" matches "sub.example.com" but not "example.com"
			suffix := pattern[1:] // ".example.com"
			root := pattern[2:]   // "example.com"
			if strings.HasSuffix(host, suffix) && host != root {
				return true
			}
		} else if host == pattern {
			return true
		}
	}
	return false
}

// filterSearchEngineSelfLinks removes results whose URL host matches the search engine's own domains.
func filterSearchEngineSelfLinks(results []SearchResult, engine string, blockedHosts []string) []SearchResult {
	out := make([]SearchResult, 0, len(results))
	for _, r := range results {
		u, err := url.Parse(r.URL)
		if err != nil {
			out = append(out, r)
			continue
		}
		if isHostBlocked(u.Hostname(), blockedHosts) {
			continue
		}
		out = append(out, r)
	}
	return out
}

// parseSearchMarkdown extracts search results from markdown output of a search engine page.
// It supports two parsing strategies:
//  1. Markdown link format: [Title](URL) followed by snippet text
//  2. Loose URL fallback: bare URLs followed by text
func parseSearchMarkdown(md, engine string, maxResults int) []SearchResult {
	md = strings.TrimSpace(md)
	if md == "" {
		return nil
	}
	// Layer 1: Try markdown link parsing
	results := parseMarkdownLinks(md, maxResults)
	if len(results) > 0 {
		return results
	}
	// Layer 2: Fallback to bare URL parsing
	return parseLooseURLs(md, maxResults)
}

// parseMarkdownLinks extracts [Title](URL) patterns and collects trailing text as snippets.
func parseMarkdownLinks(md string, maxResults int) []SearchResult {
	lines := strings.Split(md, "\n")
	results := make([]SearchResult, 0, maxResults)
	var current *SearchResult

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Try to extract markdown links from this line.
		links := extractMarkdownLinks(line)
		for _, link := range links {
			if !isHTTPURL(link.URL) {
				continue
			}
			// Filter out very short or navigation-looking titles.
			if len(strings.TrimSpace(link.Title)) < 2 {
				continue
			}
			result := SearchResult{
				Title: strings.TrimSpace(link.Title),
				URL:   link.URL,
			}
			// If there's text on the same line after the link, use it as snippet.
			snippet := extractTextAfterLinks(line)
			snippet = strings.TrimSpace(snippet)
			if snippet != "" {
				// Strip common separators like " — " or " - "
				snippet = strings.TrimLeft(snippet, "—–-·| ")
				result.Snippet = strings.TrimSpace(snippet)
			}
			results = append(results, result)
			current = &results[len(results)-1]
			if len(results) >= maxResults {
				return results
			}
		}
		// If no links found on this line and we have a current result, check if
		// this line is a continuation snippet.
		if len(links) == 0 && current != nil && current.Snippet == "" && len(line) > 10 {
			// Check if it's not a heading or list item that looks like a new result.
			if !strings.HasPrefix(line, "#") && !strings.HasPrefix(line, "- [") && !strings.HasPrefix(line, "* [") {
				current.Snippet = line
			}
		}
	}
	return results
}

type markdownLink struct {
	Title string
	URL   string
}

// extractMarkdownLinks finds all [text](url) patterns in a line.
func extractMarkdownLinks(line string) []markdownLink {
	var links []markdownLink
	for {
		start := strings.Index(line, "[")
		if start < 0 {
			break
		}
		closeBracket := strings.Index(line[start:], "]")
		if closeBracket < 0 {
			break
		}
		closeBracket += start
		if closeBracket+1 >= len(line) || line[closeBracket+1] != '(' {
			line = line[closeBracket+1:]
			continue
		}
		closeParen := strings.Index(line[closeBracket+2:], ")")
		if closeParen < 0 {
			break
		}
		closeParen += closeBracket + 2
		title := line[start+1 : closeBracket]
		u := line[closeBracket+2 : closeParen]
		links = append(links, markdownLink{Title: title, URL: u})
		line = line[closeParen+1:]
	}
	return links
}

// extractTextAfterLinks returns text after all markdown links on a line.
func extractTextAfterLinks(line string) string {
	// Find the end of the last markdown link.
	lastCloseParen := -1
	depth := 0
	for i := len(line) - 1; i >= 0; i-- {
		if line[i] == ')' {
			if depth == 0 {
				lastCloseParen = i
			}
			depth++
		} else if line[i] == '(' {
			depth--
		}
	}
	if lastCloseParen < 0 {
		return ""
	}
	rest := line[lastCloseParen+1:]
	// Strip leading numbered list prefixes like " — " or common separators.
	rest = strings.TrimLeft(rest, " \t—–-·|")
	return strings.TrimSpace(rest)
}

// parseLooseURLs extracts bare URLs and collects nearby text as snippets.
func parseLooseURLs(md string, maxResults int) []SearchResult {
	lines := strings.Split(md, "\n")
	results := make([]SearchResult, 0, maxResults)
	var current *SearchResult

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Check if the line is a bare URL.
		if isHTTPURL(line) {
			result := SearchResult{URL: line}
			results = append(results, result)
			current = &results[len(results)-1]
			if len(results) >= maxResults {
				return results
			}
			continue
		}
		// Check for inline URL.
		if idx := strings.Index(line, "https://"); idx >= 0 {
			end := idx + 8
			for end < len(line) && line[end] != ' ' && line[end] != '\t' && line[end] != '\n' && line[end] != ')' && line[end] != ']' && line[end] != '>' {
				end++
			}
			u := line[idx:end]
			if isHTTPURL(u) {
				result := SearchResult{URL: u}
				// Use text before URL as potential title.
				before := strings.TrimSpace(line[:idx])
				if len(before) > 2 && len(before) < 200 {
					result.Title = strings.TrimRight(before, "—–-·|: ")
				}
				// Use text after URL as snippet.
				after := strings.TrimSpace(line[end:])
				if len(after) > 2 {
					after = strings.TrimLeft(after, "—–-·|: ")
					result.Snippet = after
				}
				results = append(results, result)
				current = &results[len(results)-1]
				if len(results) >= maxResults {
					return results
				}
				continue
			}
		}
		// If we have a current result without a snippet/title, accumulate text.
		if current != nil {
			if current.Title == "" && len(line) > 3 && len(line) < 200 {
				current.Title = line
			} else if current.Snippet == "" && len(line) > 10 {
				current.Snippet = line
			}
		}
	}
	return results
}

func isHTTPURL(s string) bool {
	s = strings.TrimSpace(s)
	return strings.HasPrefix(s, "https://") || strings.HasPrefix(s, "http://")
}
