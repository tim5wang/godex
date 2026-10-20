package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/core/config"
)

func TestProviderOrderIncludesDeepSeek(t *testing.T) {
	order := providerOrder([]string{"deepseek", "duckduckgo"})
	if len(order) == 0 || order[0] != "deepseek" {
		t.Fatalf("expected deepseek first, got %v", order)
	}
	if got := providerOrder([]string{"bogus", "duckduckgo"}); len(got) != 1 || got[0] != "duckduckgo" {
		t.Fatalf("unknown provider should be dropped, got %v", got)
	}
}

func TestDeepSeekSearchProviderMapsFields(t *testing.T) {
	var gotBody map[string]interface{}
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"results":[
			{"rerank_score":2.4,"title":"DeepSeek V4","url":"https://example.com/v4","snippet":"short","summary":"long summary","date":"2026-06-01T00:00:00+00:00","authority":3},
			{"rerank_score":0.4,"title":"Junk","url":"https://example.com/junk","snippet":"meh","summary":"weak","date":"","authority":-1}
		]}`))
	}))
	defer server.Close()

	client := NewDeepSeekToolsClient("sk-test")
	client.search = server.URL
	// The paid provider must not fan out query variants: one request per search.
	client.fetch = "http://127.0.0.1:1"

	svc := NewWebSearchService(config.WebSearchConfig{
		Enabled:         true,
		ProviderOrder:   []string{"deepseek"},
		CacheTTLSeconds: 60,
	})
	svc.SetDeepSeekTools(client)

	resp, err := svc.Search(context.Background(), "deepseek v4 release", 5, "")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if gotAuth != "Bearer sk-test" {
		t.Fatalf("expected bearer auth, got %q", gotAuth)
	}
	if got := gotBody["count"]; got != float64(5) {
		t.Fatalf("expected count=5 in request body, got %#v", gotBody)
	}
	if got := gotBody["query"]; got != "deepseek v4 release" {
		t.Fatalf("expected unmodified query, got %#v", got)
	}
	if resp.Provider != "deepseek" || len(resp.Results) != 2 {
		t.Fatalf("unexpected response: %#v", resp)
	}
	// rerank_score + authority>=3 must rank the authoritative result first.
	first := resp.Results[0]
	if first.URL != "https://example.com/v4" {
		t.Fatalf("expected authoritative result first, got %#v", resp.Results)
	}
	if first.FetchedPreview != "long summary" {
		t.Fatalf("expected summary in fetched_preview, got %q", first.FetchedPreview)
	}
	if first.PublishedAt != "2026-06-01T00:00:00+00:00" {
		t.Fatalf("expected date in published_at, got %q", first.PublishedAt)
	}
	if first.RankScore <= 0 {
		t.Fatalf("expected rank score from provider relevance, got %#v", first)
	}
}

func TestDeepSeekSearchWithoutKeyIsNotConfigured(t *testing.T) {
	svc := NewWebSearchService(config.WebSearchConfig{
		Enabled:         true,
		ProviderOrder:   []string{"deepseek", "duckduckgo"},
		CacheTTLSeconds: 60,
	})
	// No SetDeepSeekTools: the provider must report a configuration error rather
	// than panicking or silently returning nothing.
	_, err := svc.searchWithProvider(context.Background(), svc.client, svc.endpoints, svc.cfg, nil, nil, nil, "deepseek", "q", 3, "")
	if err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("expected 'not configured' error, got %v", err)
	}

	// In the chain the unconfigured provider must fail over to the next one
	// instead of failing the whole search.
	ddg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html><body><a class="result__a" href="/l/?uddg=https%3A%2F%2Fexample.com%2Fok">OK</a></body></html>`))
	}))
	defer ddg.Close()
	svc.endpoints.DuckDuckGo = ddg.URL
	resp, err := svc.Search(context.Background(), "anything", 3, "")
	if err != nil {
		t.Fatalf("expected fallback to duckduckgo, got %v", err)
	}
	if resp.Provider != "duckduckgo" {
		t.Fatalf("expected duckduckgo provider, got %q", resp.Provider)
	}
}

func TestDeepSeekSearchSurfacesBalanceAndQPSActionably(t *testing.T) {
	cases := []struct {
		status int
		want   string
	}{
		{http.StatusPaymentRequired, "HTTP 402"},
		{http.StatusTooManyRequests, "HTTP 429"},
	}
	for _, tc := range cases {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(`{"error":{"message":"nope","code":"invalid_request_error"}}`))
		}))
		client := NewDeepSeekToolsClient("sk-test")
		client.search = server.URL
		_, err := client.searchResults(context.Background(), "q", 3)
		server.Close()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("status %d: expected actionable %q error, got %v", tc.status, tc.want, err)
		}
	}
}

// degradedFetchServer serves JS-heavy HTML that the local extractor cannot read.
func degradedFetchServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><head><title>SPA</title></head><body>
<div id="app"></div><noscript>Please enable JavaScript.</noscript>
<script>document.getElementById("app").innerHTML = "x";</script>
</body></html>`))
	}))
	t.Cleanup(server.Close)
	return server
}

func TestWebFetchFallsBackToDeepSeekOnDegradedPage(t *testing.T) {
	page := degradedFetchServer(t)
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["url"] == "" {
			t.Errorf("expected url in deepseek fetch request, got %#v", body)
		}
		_, _ = w.Write([]byte(`{"title":"Remote title","content":"# Rendered\n\nbody text here."}`))
	}))
	defer remote.Close()

	client := NewDeepSeekToolsClient("sk-test")
	client.fetch = remote.URL
	client.allowPrivateURLs = true // the local test page is loopback-only

	svc := NewWebFetchService(config.WebFetchConfig{
		Enabled:           true,
		TimeoutSeconds:    10,
		Policy:            "allow_all",
		AllowPrivateHosts: true,
	}, t.TempDir())
	svc.SetDeepSeekTools(client)

	resp, err := svc.Fetch(context.Background(), page.URL, "markdown", 0)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if resp.NeedsBrowser {
		t.Error("expected NeedsBrowser cleared after remote fallback")
	}
	if !strings.Contains(resp.Content, "Rendered") {
		t.Fatalf("expected deepseek content, got %q", resp.Content)
	}
	if resp.Title != "SPA" {
		t.Fatalf("expected the page title to be preserved, got %q", resp.Title)
	}
	if !strings.Contains(strings.Join(resp.ExtractionWarnings, " "), "deepseek fetch") {
		t.Fatalf("expected a fallback warning, got %#v", resp.ExtractionWarnings)
	}
}

func TestWebFetchDeepSeekFallbackFailureKeepsLocalResult(t *testing.T) {
	page := degradedFetchServer(t)
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"message":"no markdown body"}}`))
	}))
	defer remote.Close()

	client := NewDeepSeekToolsClient("sk-test")
	client.fetch = remote.URL
	client.allowPrivateURLs = true

	svc := NewWebFetchService(config.WebFetchConfig{
		Enabled:           true,
		TimeoutSeconds:    10,
		Policy:            "allow_all",
		AllowPrivateHosts: true,
	}, t.TempDir())
	svc.SetDeepSeekTools(client)

	// A failed paid fallback must not turn a usable (if thin) local fetch into an error.
	resp, err := svc.Fetch(context.Background(), page.URL, "markdown", 0)
	if err != nil {
		t.Fatalf("fetch should still succeed locally: %v", err)
	}
	if !resp.NeedsBrowser {
		t.Error("expected NeedsBrowser to remain set when the fallback fails")
	}
	if !strings.Contains(strings.Join(resp.ExtractionWarnings, " "), "fallback failed") {
		t.Fatalf("expected a recorded fallback failure, got %#v", resp.ExtractionWarnings)
	}
}

func TestWebFetchDeepSeekFallbackRefusesPrivateURLs(t *testing.T) {
	// allowPrivateURLs stays false (production default): a loopback URL means the
	// remote endpoint must never be contacted, even though local fetching is allowed.
	if (&DeepSeekToolsClient{apiKey: "sk-test"}).remoteFetchAllowed("http://127.0.0.1:9/x") {
		t.Error("loopback URL must not be sent to the remote endpoint")
	}
	client := &DeepSeekToolsClient{apiKey: "sk-test"}
	if client.remoteFetchAllowed("http://localhost/x") {
		t.Error("localhost URL must not be sent to the remote endpoint")
	}
	if !client.remoteFetchAllowed("https://example.com/x") {
		t.Error("public URL should be allowed")
	}
	if client.remoteFetchAllowed("not a url") {
		t.Error("malformed URL should not be allowed")
	}
}

func TestWebFetchSkipsDeepSeekWhenPageIsHealthy(t *testing.T) {
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><head><title>Fine</title></head><body><article><p>` +
			strings.Repeat("healthy content ", 40) + `</p></article></body></html>`))
	}))
	defer page.Close()

	called := false
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		_, _ = w.Write([]byte(`{"title":"x","content":"y"}`))
	}))
	defer remote.Close()

	client := NewDeepSeekToolsClient("sk-test")
	client.fetch = remote.URL
	client.allowPrivateURLs = true

	svc := NewWebFetchService(config.WebFetchConfig{
		Enabled:           true,
		TimeoutSeconds:    10,
		Policy:            "allow_all",
		AllowPrivateHosts: true,
	}, t.TempDir())
	svc.SetDeepSeekTools(client)

	if _, err := svc.Fetch(context.Background(), page.URL, "markdown", 0); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if called {
		t.Error("a healthy static page must not be re-fetched through the billed endpoint")
	}
}
