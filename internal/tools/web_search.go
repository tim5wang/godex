package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tim5wang/godex/internal/core/config"
	"golang.org/x/net/html"
)

const (
	defaultSearchResults = 5
	maxSearchResults     = 10
	defaultSearchTTL     = 300 * time.Second
	searchUserAgent      = "GoDex/1.0 (+https://github.com/tim5wang/godex)"
)

type SearchResult struct {
	Title          string `json:"title"`
	URL            string `json:"url"`
	Snippet        string `json:"snippet,omitempty"`
	CanonicalURL   string `json:"canonical_url,omitempty"`
	SourceQuality  string `json:"source_quality,omitempty"`
	RankScore      int    `json:"rank_score,omitempty"`
	RankReason     string `json:"rank_reason,omitempty"`
	FetchedPreview string `json:"fetched_preview,omitempty"`
	PublishedAt    string `json:"published_at,omitempty"`
	FetchError     string `json:"fetch_error,omitempty"`
}

type WebSearchResponse struct {
	Provider   string         `json:"provider"`
	Query      string         `json:"query"`
	Results    []SearchResult `json:"results"`
	NextAction string         `json:"next_action,omitempty"`
}

type webSearchCacheEntry struct {
	value   WebSearchResponse
	expires time.Time
}

type webSearchEndpoints struct {
	Brave      string
	Exa        string
	Tavily     string
	DuckDuckGo string
}

// WebSearchService executes provider-chain current-information search.
type WebSearchService struct {
	mu        sync.RWMutex
	cfg       config.WebSearchConfig
	client    *http.Client
	cache     map[string]webSearchCacheEntry
	now       func() time.Time
	endpoints webSearchEndpoints
	preview   *WebFetchService
	browser   BrowserSearchProvider
}

type BrowserSearchProvider interface {
	BrowserSearch(ctx context.Context, sessionID, query string, maxResults int) ([]SearchResult, error)
}

// NewWebSearchService creates a search service with the provided config.
func NewWebSearchService(cfg config.WebSearchConfig) *WebSearchService {
	service := &WebSearchService{
		client: &http.Client{Timeout: 30 * time.Second},
		cache:  make(map[string]webSearchCacheEntry),
		now:    time.Now,
		endpoints: webSearchEndpoints{
			Brave:      "https://api.search.brave.com/res/v1/web/search",
			Exa:        "https://api.exa.ai/search",
			Tavily:     "https://api.tavily.com/search",
			DuckDuckGo: "https://html.duckduckgo.com/html/",
		},
	}
	service.ApplyConfig(cfg)
	return service
}

func (s *WebSearchService) SetPreviewFetcher(fetcher *WebFetchService) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.preview = fetcher
	s.cache = make(map[string]webSearchCacheEntry)
}

func (s *WebSearchService) SetBrowserSearcher(searcher BrowserSearchProvider) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.browser = searcher
	s.cache = make(map[string]webSearchCacheEntry)
}

// ApplyConfig swaps runtime configuration and clears search cache.
func (s *WebSearchService) ApplyConfig(cfg config.WebSearchConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg = normalizeWebSearchConfig(cfg)
	s.cache = make(map[string]webSearchCacheEntry)
}

// Search runs one provider-chain search and returns structured results.
func (s *WebSearchService) Search(ctx context.Context, query string, maxResults int, freshness string) (WebSearchResponse, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return WebSearchResponse{}, fmt.Errorf("missing query argument")
	}

	s.mu.RLock()
	cfg := s.cfg
	now := s.now
	client := s.client
	endpoints := s.endpoints
	preview := s.preview
	browser := s.browser
	s.mu.RUnlock()

	if !cfg.Enabled {
		return WebSearchResponse{}, fmt.Errorf("web_search is disabled in tools.web_search.enabled")
	}

	maxResults = clampSearchResults(maxResults)
	cacheKey := fmt.Sprintf("%s|%d|%s", query, maxResults, strings.TrimSpace(freshness))
	if entry, ok := s.lookupCache(cacheKey); ok && now().Before(entry.expires) {
		return entry.value, nil
	}

	providers := providerOrder(cfg.ProviderOrder)
	var providerName string
	var lastErr error
	for _, name := range providers {
		var merged []SearchResult
		for _, variant := range searchProviderQueryVariants(name, query) {
			results, err := s.searchWithProvider(ctx, client, endpoints, cfg, browser, name, variant, maxResults, freshness)
			if err != nil {
				lastErr = err
				continue
			}
			merged = append(merged, results...)
			if len(merged) >= maxResults*3 {
				break
			}
		}
		if len(merged) == 0 {
			continue
		}
		providerName = name
		results := enhanceSearchResults(ctx, preview, query, merged, maxResults, searchPreferredHosts(name, cfg))
		response := WebSearchResponse{
			Provider:   providerName,
			Query:      query,
			Results:    results,
			NextAction: webSearchNextAction(len(results)),
		}
		s.storeCache(cacheKey, webSearchCacheEntry{
			value:   response,
			expires: now().Add(time.Duration(cfg.CacheTTLSeconds) * time.Second),
		})
		return response, nil
	}

	if lastErr != nil {
		return WebSearchResponse{}, lastErr
	}
	return WebSearchResponse{}, fmt.Errorf("no web search providers are configured")
}

func webSearchNextAction(resultCount int) string {
	if resultCount > 0 {
		return "Use these ranked results and fetched previews as evidence; fetch a specific result URL only when more detail is needed. Do not repeat the same web_search query unless the user asks for a refreshed search."
	}
	return "No results were returned. Try one different, more specific query or report that current web results were unavailable; do not repeat the same web_search query."
}

func searchQueryVariants(query string) []string {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil
	}
	variants := []string{query}
	lower := strings.ToLower(query)
	if !strings.Contains(lower, "official") {
		variants = append(variants, query+" official")
	}
	if !strings.Contains(lower, "docs") && !strings.Contains(lower, "documentation") {
		variants = append(variants, query+" documentation release notes")
	}
	if len(variants) > 3 {
		variants = variants[:3]
	}
	return variants
}

func searchProviderQueryVariants(provider, query string) []string {
	if strings.EqualFold(strings.TrimSpace(provider), "browser") {
		query = strings.TrimSpace(query)
		if query == "" {
			return nil
		}
		return []string{query}
	}
	return searchQueryVariants(query)
}

func enhanceSearchResults(ctx context.Context, preview *WebFetchService, query string, raw []SearchResult, maxResults int, preferredHosts []string) []SearchResult {
	results := dedupeSearchResults(raw)
	for i := range results {
		score, quality, reason := scoreSearchResult(results[i], query, preferredHosts)
		results[i].RankScore = score
		results[i].SourceQuality = quality
		results[i].RankReason = reason
	}
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].RankScore != results[j].RankScore {
			return results[i].RankScore > results[j].RankScore
		}
		return results[i].Title < results[j].Title
	})
	limit := maxResults
	if len(results) < limit {
		limit = len(results)
	}
	results = results[:limit]
	addSearchPreviews(ctx, preview, query, results)
	return results
}

func dedupeSearchResults(raw []SearchResult) []SearchResult {
	seen := make(map[string]int, len(raw))
	out := make([]SearchResult, 0, len(raw))
	for _, item := range raw {
		item.Title = strings.TrimSpace(item.Title)
		item.URL = strings.TrimSpace(item.URL)
		item.Snippet = strings.TrimSpace(item.Snippet)
		if item.URL == "" {
			continue
		}
		item.CanonicalURL = canonicalizeURL(item.URL)
		key := item.CanonicalURL
		if key == "" {
			key = item.URL
		}
		if idx, ok := seen[key]; ok {
			out[idx] = mergeSearchResult(out[idx], item)
			continue
		}
		seen[key] = len(out)
		out = append(out, item)
	}
	return out
}

func mergeSearchResult(left, right SearchResult) SearchResult {
	if len(right.Title) > len(left.Title) {
		left.Title = right.Title
	}
	if len(right.Snippet) > len(left.Snippet) {
		left.Snippet = right.Snippet
	}
	if left.CanonicalURL == "" {
		left.CanonicalURL = right.CanonicalURL
	}
	return left
}

func searchPreferredHosts(provider string, cfg config.WebSearchConfig) []string {
	if strings.EqualFold(strings.TrimSpace(provider), "browser") {
		return cfg.Browser.PreferredHosts
	}
	return nil
}

func scoreSearchResult(item SearchResult, query string, preferredHosts []string) (int, string, string) {
	text := strings.ToLower(item.Title + " " + item.Snippet + " " + item.CanonicalURL)
	score := 0
	reasons := make([]string, 0, 4)
	for _, keyword := range queryKeywords(query) {
		if strings.Contains(text, keyword) {
			score += 5
			reasons = append(reasons, "query match")
		}
	}
	u, _ := url.Parse(item.CanonicalURL)
	host := strings.ToLower(u.Hostname())
	path := strings.ToLower(u.Path)
	quality := "standard"
	if matchDomainPattern(host, preferredHosts) {
		score += 6
		reasons = append(reasons, "preferred host")
	}
	if strings.Contains(text, "official") || strings.Contains(host, "docs.") || strings.Contains(path, "docs") || strings.Contains(path, "documentation") {
		score += 12
		quality = "official_or_docs"
		reasons = append(reasons, "official/docs signal")
	}
	if strings.Contains(path, "release") || strings.Contains(path, "changelog") || strings.Contains(path, "blog") {
		score += 4
		reasons = append(reasons, "freshness/source signal")
	}
	for _, marker := range []string{"mirror", "scrape", "coupon", "best-", "top-10", "alternatives"} {
		if strings.Contains(text, marker) {
			score -= 8
			quality = "low"
			reasons = append(reasons, "low-quality signal")
			break
		}
	}
	if strings.TrimSpace(item.Snippet) == "" {
		score -= 2
	}
	if len(reasons) == 0 {
		reasons = append(reasons, "provider rank")
	}
	return score, quality, strings.Join(uniqueStrings(reasons), ", ")
}

func addSearchPreviews(ctx context.Context, preview *WebFetchService, query string, results []SearchResult) {
	if preview == nil {
		return
	}
	limit := 3
	if len(results) < limit {
		limit = len(results)
	}
	var wg sync.WaitGroup
	for i := 0; i < limit; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			fetched, err := preview.Fetch(ctx, results[idx].URL, "markdown", 1200, query)
			if err != nil {
				if isSearchPreviewPolicyError(err) {
					return
				}
				results[idx].FetchError = err.Error()
				return
			}
			if fetched.CanonicalURL != "" {
				results[idx].CanonicalURL = fetched.CanonicalURL
			}
			if fetched.Title != "" && results[idx].Title == "" {
				results[idx].Title = fetched.Title
			}
			results[idx].FetchedPreview = strings.TrimSpace(fetched.Content)
			if len(results[idx].FetchedPreview) > 600 {
				results[idx].FetchedPreview = strings.TrimSpace(results[idx].FetchedPreview[:600]) + "..."
			}
			results[idx].PublishedAt = fetched.PublishedAt
			if fetched.Description != "" && results[idx].Snippet == "" {
				results[idx].Snippet = fetched.Description
			}
		}(i)
	}
	wg.Wait()
}

func isSearchPreviewPolicyError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, marker := range []string{
		"is not in the allowed domains list",
		"blocked by policy",
		"private or local address",
		"localhost is not allowed",
	} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func (s *WebSearchService) lookupCache(key string) (webSearchCacheEntry, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entry, ok := s.cache[key]
	return entry, ok
}

func (s *WebSearchService) storeCache(key string, entry webSearchCacheEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cache[key] = entry
}

func (s *WebSearchService) searchWithProvider(ctx context.Context, client *http.Client, endpoints webSearchEndpoints, cfg config.WebSearchConfig, browser BrowserSearchProvider, provider, query string, maxResults int, freshness string) ([]SearchResult, error) {
	switch provider {
	case "brave":
		if strings.TrimSpace(cfg.BraveAPIKey) == "" {
			return nil, fmt.Errorf("brave api key not configured")
		}
		return braveSearch(ctx, client, endpoints.Brave, cfg.BraveAPIKey, query, maxResults, freshness)
	case "exa":
		if strings.TrimSpace(cfg.ExaAPIKey) == "" {
			return nil, fmt.Errorf("exa api key not configured")
		}
		return exaSearch(ctx, client, endpoints.Exa, cfg.ExaAPIKey, query, maxResults)
	case "tavily":
		if strings.TrimSpace(cfg.TavilyAPIKey) == "" {
			return nil, fmt.Errorf("tavily api key not configured")
		}
		return tavilySearch(ctx, client, endpoints.Tavily, cfg.TavilyAPIKey, query, maxResults)
	case "browser":
		if browser == nil {
			return nil, fmt.Errorf("browser search provider is unavailable")
		}
		return browser.BrowserSearch(ctx, webSearchBrowserSessionID(ctx), query, maxResults)
	case "duckduckgo":
		return duckDuckGoSearch(ctx, client, endpoints.DuckDuckGo, query, maxResults)
	default:
		return nil, fmt.Errorf("unknown search provider %q", provider)
	}
}

// WebSearchTool exposes the shared search service as a built-in tool.
type WebSearchTool struct {
	service *WebSearchService
}

type webSearchArgs struct {
	Query      string `json:"query"`
	MaxResults int    `json:"max_results,omitempty"`
	Freshness  string `json:"freshness,omitempty"`
}

func NewWebSearchTool(service *WebSearchService) Tool {
	return NewTypedTool(NewToolSpec("web_search", "Search the web for current information, returning ranked results with canonical URLs and lightweight fetched previews when available.", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"query": map[string]interface{}{
				"type":        "string",
				"description": "Search query",
			},
			"max_results": map[string]interface{}{
				"type":        "integer",
				"description": "Maximum number of results to return, capped at 10",
			},
			"freshness": map[string]interface{}{
				"type":        "string",
				"description": "Optional provider-specific freshness hint such as pd, pw, pm, py, or a date range",
			},
		},
		"required": []string{"query"},
	}, map[string]string{"q": "query"}), func(ctx context.Context, args webSearchArgs) (ToolResult, error) {
		if service == nil {
			return ToolResult{}, fmt.Errorf("web_search service is unavailable")
		}
		maxResults := args.MaxResults
		if maxResults <= 0 {
			maxResults = defaultSearchResults
		}
		response, err := service.Search(ctx, args.Query, maxResults, args.Freshness)
		if err != nil {
			return ToolResult{}, err
		}
		return ToolResult{Structured: response}, nil
	})
}

func normalizeWebSearchConfig(cfg config.WebSearchConfig) config.WebSearchConfig {
	if cfg.CacheTTLSeconds <= 0 {
		cfg.CacheTTLSeconds = int(defaultSearchTTL / time.Second)
	}
	if len(cfg.ProviderOrder) == 0 {
		cfg.ProviderOrder = []string{"brave", "exa", "tavily", "duckduckgo"}
	}
	cfg.ProviderOrder = providerOrder(cfg.ProviderOrder)
	return cfg
}

func providerOrder(order []string) []string {
	seen := make(map[string]struct{}, len(order))
	out := make([]string, 0, len(order)+1)
	for _, item := range order {
		item = strings.ToLower(strings.TrimSpace(item))
		switch item {
		case "brave", "exa", "tavily", "browser", "duckduckgo":
			if _, ok := seen[item]; ok {
				continue
			}
			seen[item] = struct{}{}
			out = append(out, item)
		}
	}
	if _, ok := seen["duckduckgo"]; !ok {
		out = append(out, "duckduckgo")
	}
	return out
}

func webSearchBrowserSessionID(ctx context.Context) string {
	if sessionID := strings.TrimSpace(SessionIDFromContext(ctx)); sessionID != "" {
		return sessionID
	}
	if sessionID := strings.TrimSpace(SessionContextFromContext(ctx).SessionID); sessionID != "" {
		return sessionID
	}
	return "web-search-browser"
}

func clampSearchResults(v int) int {
	if v <= 0 {
		return defaultSearchResults
	}
	if v > maxSearchResults {
		return maxSearchResults
	}
	return v
}

func braveSearch(ctx context.Context, client *http.Client, endpoint, apiKey, query string, maxResults int, freshness string) ([]SearchResult, error) {
	values := url.Values{}
	values.Set("q", query)
	values.Set("count", fmt.Sprintf("%d", maxResults))
	if strings.TrimSpace(freshness) != "" {
		values.Set("freshness", strings.TrimSpace(freshness))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+values.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", searchUserAgent)
	req.Header.Set("X-Subscription-Token", apiKey)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("brave search failed: %s", strings.TrimSpace(string(body)))
	}
	var parsed struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, err
	}
	results := make([]SearchResult, 0, len(parsed.Web.Results))
	for _, item := range parsed.Web.Results {
		results = append(results, SearchResult{
			Title:   strings.TrimSpace(item.Title),
			URL:     strings.TrimSpace(item.URL),
			Snippet: strings.TrimSpace(item.Description),
		})
	}
	return trimSearchResults(results, maxResults), nil
}

func exaSearch(ctx context.Context, client *http.Client, endpoint, apiKey, query string, maxResults int) ([]SearchResult, error) {
	body, err := json.Marshal(map[string]interface{}{
		"query":      query,
		"numResults": maxResults,
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", searchUserAgent)
	req.Header.Set("x-api-key", apiKey)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("exa search failed: %s", strings.TrimSpace(string(data)))
	}
	var parsed struct {
		Results []struct {
			Title string `json:"title"`
			URL   string `json:"url"`
			Text  string `json:"text"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, err
	}
	results := make([]SearchResult, 0, len(parsed.Results))
	for _, item := range parsed.Results {
		results = append(results, SearchResult{
			Title:   strings.TrimSpace(item.Title),
			URL:     strings.TrimSpace(item.URL),
			Snippet: strings.TrimSpace(item.Text),
		})
	}
	return trimSearchResults(results, maxResults), nil
}

func tavilySearch(ctx context.Context, client *http.Client, endpoint, apiKey, query string, maxResults int) ([]SearchResult, error) {
	body, err := json.Marshal(map[string]interface{}{
		"api_key":     apiKey,
		"query":       query,
		"max_results": maxResults,
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", searchUserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("tavily search failed: %s", strings.TrimSpace(string(data)))
	}
	var parsed struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, err
	}
	results := make([]SearchResult, 0, len(parsed.Results))
	for _, item := range parsed.Results {
		results = append(results, SearchResult{
			Title:   strings.TrimSpace(item.Title),
			URL:     strings.TrimSpace(item.URL),
			Snippet: strings.TrimSpace(item.Content),
		})
	}
	return trimSearchResults(results, maxResults), nil
}

func duckDuckGoSearch(ctx context.Context, client *http.Client, endpoint, query string, maxResults int) ([]SearchResult, error) {
	values := url.Values{}
	values.Set("q", query)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+values.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/html")
	req.Header.Set("User-Agent", searchUserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("duckduckgo search failed: %s", strings.TrimSpace(string(data)))
	}
	return parseDuckDuckGoResults(resp.Body, maxResults)
}

func parseDuckDuckGoResults(reader io.Reader, maxResults int) ([]SearchResult, error) {
	tokenizer := html.NewTokenizer(reader)
	results := make([]SearchResult, 0, maxResults)
	var current *SearchResult
	var captureTitle, captureSnippet bool
	for {
		switch tokenizer.Next() {
		case html.ErrorToken:
			if tokenizer.Err() == io.EOF {
				return trimSearchResults(results, maxResults), nil
			}
			return nil, tokenizer.Err()
		case html.StartTagToken:
			token := tokenizer.Token()
			className := attrValue(token, "class")
			switch token.Data {
			case "a":
				if strings.Contains(className, "result__a") {
					href := decodeDuckDuckGoURL(attrValue(token, "href"))
					result := SearchResult{URL: href}
					results = append(results, result)
					current = &results[len(results)-1]
					captureTitle = true
				}
			case "div", "span":
				if strings.Contains(className, "result__snippet") && current != nil {
					captureSnippet = true
				}
			}
		case html.TextToken:
			text := strings.TrimSpace(string(tokenizer.Text()))
			if text == "" || current == nil {
				continue
			}
			switch {
			case captureTitle:
				if current.Title == "" {
					current.Title = text
				} else {
					current.Title += " " + text
				}
			case captureSnippet:
				if current.Snippet == "" {
					current.Snippet = text
				} else {
					current.Snippet += " " + text
				}
			}
		case html.EndTagToken:
			token := tokenizer.Token()
			if token.Data == "a" {
				captureTitle = false
			}
			if token.Data == "div" || token.Data == "span" || token.Data == "a" {
				captureSnippet = false
			}
		}
		if len(results) >= maxResults {
			return trimSearchResults(results, maxResults), nil
		}
	}
}

func attrValue(token html.Token, name string) string {
	for _, attr := range token.Attr {
		if attr.Key == name {
			return attr.Val
		}
	}
	return ""
}

func decodeDuckDuckGoURL(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return raw
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if target := parsed.Query().Get("uddg"); target != "" {
		if decoded, err := url.QueryUnescape(target); err == nil {
			return decoded
		}
		return target
	}
	return raw
}

func trimSearchResults(results []SearchResult, maxResults int) []SearchResult {
	trimmed := make([]SearchResult, 0, minInt(len(results), maxResults))
	for _, item := range results {
		item.Title = strings.Join(strings.Fields(item.Title), " ")
		item.URL = strings.TrimSpace(item.URL)
		item.Snippet = strings.Join(strings.Fields(item.Snippet), " ")
		if item.Title == "" || item.URL == "" {
			continue
		}
		if len(item.Snippet) > 400 {
			item.Snippet = item.Snippet[:400]
		}
		trimmed = append(trimmed, item)
		if len(trimmed) >= maxResults {
			break
		}
	}
	return trimmed
}
