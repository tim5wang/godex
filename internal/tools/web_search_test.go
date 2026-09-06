package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/core/config"
)

func TestWebSearchServiceFallsBackToDuckDuckGo(t *testing.T) {
	ddg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`
<html><body>
  <a class="result__a" href="/l/?uddg=https%3A%2F%2Fexample.com%2Fweather">Weather today</a>
  <div class="result__snippet">Current weather information</div>
</body></html>`))
	}))
	defer ddg.Close()

	service := NewWebSearchService(config.WebSearchConfig{
		Enabled:         true,
		ProviderOrder:   []string{"duckduckgo"},
		CacheTTLSeconds: 60,
	})
	service.endpoints.DuckDuckGo = ddg.URL

	result, err := service.Search(context.Background(), "weather", 5, "")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if result.Provider != "duckduckgo" {
		t.Fatalf("expected duckduckgo provider, got %q", result.Provider)
	}
	if len(result.Results) != 1 || result.Results[0].URL != "https://example.com/weather" {
		t.Fatalf("unexpected results: %#v", result.Results)
	}
}

func TestWebSearchToolUsesConfiguredProviderOrder(t *testing.T) {
	brave := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Subscription-Token"); got != "brave-key" {
			t.Fatalf("unexpected brave token %q", got)
		}
		_, _ = w.Write([]byte(`{"web":{"results":[{"title":"Brave result","url":"https://brave.example","description":"snippet"}]}}`))
	}))
	defer brave.Close()

	service := NewWebSearchService(config.WebSearchConfig{
		Enabled:         true,
		ProviderOrder:   []string{"brave", "duckduckgo"},
		CacheTTLSeconds: 60,
		BraveAPIKey:     "brave-key",
	})
	service.endpoints.Brave = brave.URL

	tool := NewWebSearchTool(service)
	output, err := tool.Execute(context.Background(), map[string]interface{}{
		"query":       "latest godex",
		"max_results": float64(3),
	})
	if err != nil {
		t.Fatalf("execute tool: %v", err)
	}

	var parsed WebSearchResponse
	if err := json.Unmarshal([]byte(output), &parsed); err != nil {
		t.Fatalf("parse output: %v", err)
	}
	if parsed.Provider != "brave" || len(parsed.Results) != 1 || !strings.Contains(parsed.Results[0].Title, "Brave") {
		t.Fatalf("unexpected parsed response: %#v", parsed)
	}
	if !strings.Contains(parsed.NextAction, "Do not repeat the same web_search query") {
		t.Fatalf("expected anti-repeat next action, got %q", parsed.NextAction)
	}
}

func TestWebSearchServiceCanonicalizesDedupesRanksAndPreviews(t *testing.T) {
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><head>
<title>Official GoDex Docs</title>
<meta name="description" content="Official release documentation">
<meta property="article:published_time" content="2026-04-30T12:00:00Z">
<link rel="canonical" href="https://docs.example.com/godex">
</head><body><article><h1>GoDex Release Notes</h1><p>Official documentation for the latest release.</p></article></body></html>`))
	}))
	defer page.Close()

	brave := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("q")
		switch {
		case strings.Contains(query, "official"):
			_, _ = w.Write([]byte(`{"web":{"results":[
				{"title":"Official GoDex Docs","url":"` + page.URL + `/godex?utm_source=news#intro","description":"official docs release"},
				{"title":"Official GoDex Docs duplicate","url":"` + page.URL + `/godex?utm_medium=email","description":"duplicate"}
			]}}`))
		default:
			_, _ = w.Write([]byte(`{"web":{"results":[
				{"title":"Low quality mirror","url":"https://mirror.example.com/godex?utm_source=x","description":"random mirror"},
				{"title":"Official GoDex Docs","url":"` + page.URL + `/godex?utm_campaign=y#section","description":"official docs"}
			]}}`))
		}
	}))
	defer brave.Close()

	fetch := NewWebFetchService(config.WebFetchConfig{
		Enabled:           true,
		MaxChars:          300,
		TimeoutSeconds:    10,
		Policy:            "allow_all",
		AllowPrivateHosts: true,
	}, t.TempDir())
	service := NewWebSearchService(config.WebSearchConfig{
		Enabled:         true,
		ProviderOrder:   []string{"brave"},
		CacheTTLSeconds: 60,
		BraveAPIKey:     "brave-key",
	})
	service.endpoints.Brave = brave.URL
	service.SetPreviewFetcher(fetch)

	result, err := service.Search(context.Background(), "godex latest", 5, "")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(result.Results) != 2 {
		t.Fatalf("expected duplicate official URLs to collapse, got %#v", result.Results)
	}
	first := result.Results[0]
	if !strings.Contains(first.Title, "Official") || first.CanonicalURL == "" {
		t.Fatalf("expected official docs to rank first with canonical URL, got %#v", first)
	}
	if strings.Contains(first.CanonicalURL, "utm_") || strings.Contains(first.CanonicalURL, "#") {
		t.Fatalf("expected canonical URL without tracking/fragment, got %q", first.CanonicalURL)
	}
	if first.FetchedPreview == "" || first.PublishedAt != "2026-04-30T12:00:00Z" {
		t.Fatalf("expected fetched preview and additive fields, got %#v", first)
	}
	if first.SourceQuality == "" || first.RankScore == 0 || first.RankReason == "" {
		t.Fatalf("expected rank metadata, got %#v", first)
	}
}

func TestWebSearchPreviewPolicyFailureIsSilent(t *testing.T) {
	brave := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"web":{"results":[{"title":"External","url":"https://example.com/private","description":"external"}]}}`))
	}))
	defer brave.Close()

	fetch := NewWebFetchService(config.WebFetchConfig{
		Enabled:           true,
		MaxChars:          100,
		TimeoutSeconds:    1,
		Policy:            "allowlist",
		AllowedDomains:    []string{"docs.example.com"},
		AllowPrivateHosts: true,
	}, t.TempDir())
	service := NewWebSearchService(config.WebSearchConfig{
		Enabled:         true,
		ProviderOrder:   []string{"brave"},
		CacheTTLSeconds: 60,
		BraveAPIKey:     "brave-key",
	})
	service.endpoints.Brave = brave.URL
	service.SetPreviewFetcher(fetch)

	result, err := service.Search(context.Background(), "local private", 1, "")
	if err != nil {
		t.Fatalf("search should soft-fail preview errors: %v", err)
	}
	if len(result.Results) != 1 {
		t.Fatalf("expected one result, got %#v", result.Results)
	}
	if result.Results[0].FetchError != "" || result.Results[0].FetchedPreview != "" {
		t.Fatalf("expected policy-denied automatic preview to stay silent, got %#v", result.Results[0])
	}
}

type fakeBrowserSearchProvider struct {
	calls   []fakeBrowserSearchCall
	results []SearchResult
	err     error
}

type fakeBrowserSearchCall struct {
	sessionID  string
	query      string
	maxResults int
}

func (f *fakeBrowserSearchProvider) BrowserSearch(ctx context.Context, sessionID, query string, maxResults int) ([]SearchResult, error) {
	f.calls = append(f.calls, fakeBrowserSearchCall{
		sessionID:  sessionID,
		query:      query,
		maxResults: maxResults,
	})
	return append([]SearchResult{}, f.results...), f.err
}

func TestWebSearchServiceUsesBrowserProviderWithSessionContext(t *testing.T) {
	browser := &fakeBrowserSearchProvider{
		results: []SearchResult{{
			Title:   "Browser result",
			URL:     "https://example.com/browser?q=1&utm_source=test#top",
			Snippet: "rendered search result",
		}},
	}
	service := NewWebSearchService(config.WebSearchConfig{
		Enabled:         true,
		ProviderOrder:   []string{"browser"},
		CacheTTLSeconds: 60,
	})
	service.SetBrowserSearcher(browser)

	ctx := WithSessionID(context.Background(), "session-browser")
	result, err := service.Search(ctx, "rendered search", 3, "")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if result.Provider != "browser" {
		t.Fatalf("expected browser provider, got %q", result.Provider)
	}
	if len(browser.calls) != 1 || browser.calls[0].sessionID != "session-browser" || browser.calls[0].query != "rendered search" || browser.calls[0].maxResults != 3 {
		t.Fatalf("unexpected browser call: %#v", browser.calls)
	}
	if len(result.Results) != 1 || result.Results[0].CanonicalURL != "https://example.com/browser?q=1" {
		t.Fatalf("expected canonicalized browser result, got %#v", result.Results)
	}
}

func TestWebSearchServiceUsesInternalSessionForBrowserProviderWithoutSessionContext(t *testing.T) {
	browser := &fakeBrowserSearchProvider{
		results: []SearchResult{{
			Title: "Browser result",
			URL:   "https://example.com/browser",
		}},
	}
	service := NewWebSearchService(config.WebSearchConfig{
		Enabled:         true,
		ProviderOrder:   []string{"browser"},
		CacheTTLSeconds: 60,
	})
	service.SetBrowserSearcher(browser)

	if _, err := service.Search(context.Background(), "rendered search", 1, ""); err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(browser.calls) == 0 || browser.calls[0].sessionID != "web-search-browser" {
		t.Fatalf("expected internal browser session id, got %#v", browser.calls)
	}
}

func TestNormalizeBrowserSearchConfigSupportsEnginesAndTemplates(t *testing.T) {
	cfg := normalizeBrowserSearchConfig(config.WebSearchBrowserConfig{})
	if cfg.Engine != "duckduckgo" || !strings.Contains(cfg.Engines["duckduckgo"].SearchURLTemplate, "{{query}}") {
		t.Fatalf("expected duckduckgo defaults, got %#v", cfg)
	}

	bing := normalizeBrowserSearchConfig(config.WebSearchBrowserConfig{Engine: "bing"})
	if bing.Engine != "bing" || !strings.Contains(bing.Engines["bing"].SearchURLTemplate, "bing.com") {
		t.Fatalf("expected bing template, got %#v", bing)
	}

	brave := normalizeBrowserSearchConfig(config.WebSearchBrowserConfig{Engine: "brave"})
	if brave.Engine != "brave" || !strings.Contains(brave.Engines["brave"].SearchURLTemplate, "search.brave.com") {
		t.Fatalf("expected brave template, got %#v", brave)
	}

	custom := normalizeBrowserSearchConfig(config.WebSearchBrowserConfig{
		Engine:         "custom",
		PreferredHosts: []string{" docs.example "},
		EngineFallback: []string{" bing ", "duckduckgo", "bing"},
		Engines: map[string]config.WebSearchBrowserEngineConfig{
			"custom": {
				SearchURLTemplate: "https://search.example/?q={{query}}",
				BlockedHosts:      []string{" search.example ", ""},
			},
		},
	})
	customEngine := custom.Engines["custom"]
	if custom.Engine != "custom" || customEngine.SearchURLTemplate != "https://search.example/?q={{query}}" {
		t.Fatalf("expected custom template to survive, got %#v", custom)
	}
	if len(customEngine.BlockedHosts) != 1 || customEngine.BlockedHosts[0] != "search.example" || len(custom.PreferredHosts) != 1 || custom.PreferredHosts[0] != "docs.example" {
		t.Fatalf("expected cleaned hosts, got blocked=%#v preferred=%#v", customEngine.BlockedHosts, custom.PreferredHosts)
	}
	if len(custom.EngineFallback) != 2 || custom.EngineFallback[0] != "bing" || custom.EngineFallback[1] != "duckduckgo" {
		t.Fatalf("expected cleaned fallback engines, got %#v", custom.EngineFallback)
	}
}

func TestBrowserSearchURLForEngine(t *testing.T) {
	for _, tc := range []struct {
		name     string
		engine   string
		contains string
	}{
		{name: "duckduckgo", engine: "duckduckgo", contains: "duckduckgo.com"},
		{name: "bing", engine: "bing", contains: "bing.com"},
		{name: "brave", engine: "brave", contains: "search.brave.com"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := normalizeBrowserSearchConfig(config.WebSearchBrowserConfig{Engine: tc.engine})
			got, err := browserSearchURLForQuery(browserSearchConfigForEngine(cfg, tc.engine), "godex latest")
			if err != nil {
				t.Fatalf("build url: %v", err)
			}
			if !strings.Contains(got, tc.contains) || !strings.Contains(got, "godex") {
				t.Fatalf("unexpected search URL %q", got)
			}
		})
	}
}

func TestBrowserSearchEngineAttemptsDedupesPrimaryAndFallback(t *testing.T) {
	cfg := normalizeBrowserSearchConfig(config.WebSearchBrowserConfig{
		Engine:         "bing",
		EngineFallback: []string{"brave", "bing", "duckduckgo", "unknown"},
	})
	attempts := browserSearchEngineAttempts(cfg)
	if strings.Join(attempts, ",") != "bing,brave,duckduckgo" {
		t.Fatalf("unexpected browser search attempts: %#v", attempts)
	}
}

func TestProviderOrderIncludesLightpanda(t *testing.T) {
	order := providerOrder([]string{"lightpanda", "brave", "duckduckgo"})
	found := false
	for _, p := range order {
		if p == "lightpanda" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("providerOrder should include lightpanda, got %v", order)
	}
}

func TestProviderOrderLightpandaFirst(t *testing.T) {
	order := providerOrder([]string{"lightpanda", "brave", "exa", "tavily", "duckduckgo"})
	if order[0] != "lightpanda" {
		t.Fatalf("expected lightpanda first, got %v", order)
	}
}

type mockSearchProvider struct {
	results []SearchResult
	err     error
	calls   int
}

func (m *mockSearchProvider) BrowserSearch(ctx context.Context, sessionID, query string, maxResults int) ([]SearchResult, error) {
	m.calls++
	if m.err != nil {
		return nil, m.err
	}
	if len(m.results) > maxResults {
		return m.results[:maxResults], nil
	}
	return m.results, nil
}

func TestSearchWithProvider_Lightpanda(t *testing.T) {
	mock := &mockSearchProvider{
		results: []SearchResult{
			{Title: "Go Docs", URL: "https://go.dev/doc/", Snippet: "Go documentation"},
		},
	}
	svc := NewWebSearchService(config.WebSearchConfig{
		Enabled:       true,
		ProviderOrder: []string{"lightpanda"},
	})
	svc.SetLightpandaSearcher(mock)

	ctx := context.Background()
	resp, err := svc.Search(ctx, "golang docs", 5, "")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if resp.Provider != "lightpanda" {
		t.Fatalf("expected provider lightpanda, got %q", resp.Provider)
	}
	if len(resp.Results) == 0 {
		t.Fatal("expected results, got 0")
	}
}

func TestSearchFallbackFromLightpanda(t *testing.T) {
	lightpandaErr := &mockSearchProvider{err: fmt.Errorf("lightpanda binary not found")}
	ddg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body>
<a class="result__a" href="https://example.com/result1">Example Result</a>
<div class="result__snippet">This is a snippet.</div>
</body></html>`))
	}))
	defer ddg.Close()

	svc := NewWebSearchService(config.WebSearchConfig{
		Enabled:       true,
		ProviderOrder: []string{"lightpanda", "duckduckgo"},
	})
	svc.SetLightpandaSearcher(lightpandaErr)
	svc.endpoints.DuckDuckGo = ddg.URL

	ctx := context.Background()
	resp, err := svc.Search(ctx, "test query", 5, "")
	if err != nil {
		t.Fatalf("search should fallback to duckduckgo: %v", err)
	}
	if resp.Provider != "duckduckgo" {
		t.Fatalf("expected provider duckduckgo after fallback, got %q", resp.Provider)
	}
	if lightpandaErr.calls != 1 {
		t.Fatalf("provider failure should not be retried with query variants, got %d calls", lightpandaErr.calls)
	}
	if _, err := svc.Search(ctx, "different query", 5, ""); err != nil {
		t.Fatalf("search should skip cooling-down provider: %v", err)
	}
	if lightpandaErr.calls != 1 {
		t.Fatalf("cooling-down provider should be skipped, got %d calls", lightpandaErr.calls)
	}
}

func TestWebSearchFailureReturnsBoundedFallbackGuidance(t *testing.T) {
	failing := &mockSearchProvider{err: fmt.Errorf("signal: killed")}
	svc := NewWebSearchService(config.WebSearchConfig{
		Enabled:       true,
		ProviderOrder: []string{"lightpanda"},
	})
	svc.SetLightpandaSearcher(failing)
	svc.endpoints.DuckDuckGo = "http://127.0.0.1:1"

	_, err := svc.Search(context.Background(), "current browser docs", 5, "")
	if err == nil {
		t.Fatal("expected provider failure")
	}
	if failing.calls != 1 {
		t.Fatalf("expected one provider attempt, got %d", failing.calls)
	}
	if got := err.Error(); !strings.Contains(got, "do not repeat") || !strings.Contains(got, "web_fetch") || !strings.Contains(got, "local/offline") {
		t.Fatalf("expected actionable fallback guidance, got %q", got)
	}
}

func TestWebSearchCancellationDoesNotTripProviderCooldown(t *testing.T) {
	failing := &mockSearchProvider{err: context.Canceled}
	svc := NewWebSearchService(config.WebSearchConfig{
		Enabled:       true,
		ProviderOrder: []string{"lightpanda"},
	})
	svc.SetLightpandaSearcher(failing)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := svc.Search(ctx, "canceled query", 5, "")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
	if svc.providerIsCoolingDown("lightpanda", time.Now()) {
		t.Fatal("request cancellation must not mark the provider unavailable")
	}
}

func TestSearchLightpandaUnavailableFallsBack(t *testing.T) {
	ddg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body>
<a class="result__a" href="https://example.com/fallback">Fallback Result</a>
<div class="result__snippet">A fallback snippet.</div>
</body></html>`))
	}))
	defer ddg.Close()

	svc := NewWebSearchService(config.WebSearchConfig{
		Enabled:       true,
		ProviderOrder: []string{"lightpanda", "duckduckgo"},
	})
	// Don't call SetLightpandaSearcher — lightpanda remains nil
	svc.endpoints.DuckDuckGo = ddg.URL

	ctx := context.Background()
	resp, err := svc.Search(ctx, "test query", 5, "")
	if err != nil {
		t.Fatalf("search should fallback when lightpanda nil: %v", err)
	}
	if resp.Provider != "duckduckgo" {
		t.Fatalf("expected provider duckduckgo, got %q", resp.Provider)
	}
}
