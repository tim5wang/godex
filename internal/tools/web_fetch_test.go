package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/core/config"
)

func TestWebFetchServiceExtractsAndSpillsLargeHTML(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><body><h1>Hello</h1><p>` + strings.Repeat("world ", 80) + `</p></body></html>`))
	}))
	defer server.Close()

	tempDir := t.TempDir()
	service := NewWebFetchService(config.WebFetchConfig{
		Enabled:           true,
		MaxChars:          120,
		TimeoutSeconds:    10,
		Policy:            "allow_all",
		AllowPrivateHosts: true,
	}, tempDir)

	result, err := service.Fetch(context.Background(), server.URL, "markdown", 120)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !result.Truncated || result.FilePath == "" {
		t.Fatalf("expected truncation spill, got %#v", result)
	}
	if _, err := os.Stat(result.FilePath); err != nil {
		t.Fatalf("expected spill file to exist: %v", err)
	}
	if !strings.Contains(result.Content, "Hello") {
		t.Fatalf("expected markdown content, got %q", result.Content)
	}
}

func TestWebFetchSpillUsesContentAddressedPath(t *testing.T) {
	tempDir := t.TempDir()
	result := WebFetchResponse{URL: "https://example.com/docs", Mode: "markdown"}

	first, err := spillFetchedContent(tempDir, result, "same content")
	if err != nil {
		t.Fatalf("first spill: %v", err)
	}
	second, err := spillFetchedContent(tempDir, result, "same content")
	if err != nil {
		t.Fatalf("second spill: %v", err)
	}
	if first != second {
		t.Fatalf("expected repeated content to reuse spill path, got %q and %q", first, second)
	}
	if _, err := os.Stat(first); err != nil {
		t.Fatalf("expected spill file: %v", err)
	}
}

func TestWebFetchServiceExtractsMetadataReadableContentAndWarnings(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html>
<html>
<head>
  <title>Docs Page</title>
  <meta name="description" content="Readable documentation">
  <meta property="og:site_name" content="Example Docs">
  <meta property="article:published_time" content="2026-04-30T10:00:00Z">
  <link rel="canonical" href="https://docs.example.com/product?utm_source=news#intro">
</head>
<body>
  <nav>navigation should disappear</nav>
  <main>
    <h1>Product Guide</h1>
    <p>This official documentation explains installation and release behavior.</p>
    <pre><code>godex serve</code></pre>
  </main>
  <footer>footer should disappear</footer>
</body>
</html>`))
	}))
	defer server.Close()

	service := NewWebFetchService(config.WebFetchConfig{
		Enabled:           true,
		MaxChars:          500,
		TimeoutSeconds:    10,
		Policy:            "allow_all",
		AllowPrivateHosts: true,
	}, t.TempDir())

	result, err := service.Fetch(context.Background(), server.URL, "markdown", 500, "installation")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if result.Title != "Docs Page" || result.Description != "Readable documentation" || result.SiteName != "Example Docs" {
		t.Fatalf("expected metadata, got %#v", result)
	}
	if result.CanonicalURL != "https://docs.example.com/product" || result.PublishedAt != "2026-04-30T10:00:00Z" {
		t.Fatalf("unexpected canonical/date metadata: %#v", result)
	}
	if strings.Contains(result.Content, "navigation should disappear") || strings.Contains(result.Content, "footer should disappear") {
		t.Fatalf("expected boilerplate to be removed, got %q", result.Content)
	}
	if !strings.Contains(result.Content, "Product Guide") || !strings.Contains(result.Content, "godex serve") {
		t.Fatalf("expected readable content and code to remain, got %q", result.Content)
	}
	if result.NeedsBrowser {
		t.Fatalf("did not expect browser warning for readable document: %#v", result.ExtractionWarnings)
	}
	if len(result.Chunks) == 0 || !strings.Contains(result.Chunks[0].Content, "installation") {
		t.Fatalf("expected query-relevant chunks, got %#v", result.Chunks)
	}
}

func TestWebFetchServiceFlagsJavaScriptHeavyPagesAndChunksLongContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><body>
<div id="app"></div><noscript>Please enable JavaScript to use this app.</noscript>
<script>` + strings.Repeat("console.log('dynamic');", 120) + `</script>
</body></html>`))
	}))
	defer server.Close()

	service := NewWebFetchService(config.WebFetchConfig{
		Enabled:           true,
		MaxChars:          80,
		TimeoutSeconds:    10,
		Policy:            "allow_all",
		AllowPrivateHosts: true,
	}, t.TempDir())

	result, err := service.Fetch(context.Background(), server.URL, "text", 80, "dynamic app")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !result.NeedsBrowser || len(result.ExtractionWarnings) == 0 {
		t.Fatalf("expected JS-heavy page to request browser, got %#v", result)
	}
}

func TestWebFetchServiceRejectsOversizedBody(t *testing.T) {
	previousLimit := maxWebFetchBodyBytes
	maxWebFetchBodyBytes = 32
	t.Cleanup(func() {
		maxWebFetchBodyBytes = previousLimit
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(strings.Repeat("x", 33)))
	}))
	defer server.Close()

	service := NewWebFetchService(config.WebFetchConfig{
		Enabled:           true,
		MaxChars:          100,
		TimeoutSeconds:    10,
		Policy:            "allow_all",
		AllowPrivateHosts: true,
	}, t.TempDir())

	_, err := service.Fetch(context.Background(), server.URL, "text", 0)
	if err == nil || !strings.Contains(err.Error(), "exceeds max size") {
		t.Fatalf("expected oversized body error, got %v", err)
	}
}

func TestWebFetchServiceHonorsPolicyAndPrivateHostGuard(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()

	service := NewWebFetchService(config.WebFetchConfig{
		Enabled:           true,
		MaxChars:          100,
		TimeoutSeconds:    10,
		Policy:            "allow_all",
		AllowPrivateHosts: false,
	}, t.TempDir())

	if _, err := service.Fetch(context.Background(), server.URL, "text", 0); err == nil {
		t.Fatalf("expected private host protection error")
	}

	service.ApplyConfig(config.WebFetchConfig{
		Enabled:           true,
		MaxChars:          100,
		TimeoutSeconds:    10,
		Policy:            "allowlist",
		AllowedDomains:    []string{"not-the-host.example"},
		AllowPrivateHosts: true,
	}, t.TempDir())
	if _, err := service.Fetch(context.Background(), server.URL, "text", 0); err == nil {
		t.Fatalf("expected allowlist enforcement error")
	}
}

func TestFetchLightpandaFallback_NeedsBrowser(t *testing.T) {
	// Server returns minimal HTML that triggers NeedsBrowser detection.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!DOCTYPE html>
<html><head><title>SPA Page</title></head>
<body>
<div id="app"></div>
<script>document.getElementById("app").innerHTML = "rendered";</script>
<noscript>This page requires JavaScript.</noscript>
</body></html>`))
	}))
	defer server.Close()

	// Mock lightpanda binary that returns markdown content.
	dir := t.TempDir()
	mock := filepath.Join(dir, "lightpanda")
	if err := os.WriteFile(mock, []byte("#!/bin/sh\necho '# SPA Page Content'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	lightpanda := &LightpandaBinary{path: mock}

	service := NewWebFetchService(config.WebFetchConfig{
		Enabled:          true,
		AllowPrivateHosts: true,
	}, t.TempDir())
	service.SetLightpandaFetcher(lightpanda)

	resp, err := service.Fetch(context.Background(), server.URL, "markdown", 0)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if resp.NeedsBrowser {
		t.Error("expected NeedsBrowser=false after lightpanda fallback")
	}
	if !strings.Contains(resp.Content, "SPA Page Content") {
		t.Errorf("expected lightpanda content in response, got %q", resp.Content[:100])
	}
}

func TestFetchLightpandaFallback_Unavailable(t *testing.T) {
	// Server returns minimal HTML that triggers NeedsBrowser detection.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!DOCTYPE html>
<html><head><title>SPA Page</title></head>
<body>
<div id="app"></div>
<script>document.getElementById("app").innerHTML = "rendered";</script>
<noscript>This page requires JavaScript.</noscript>
</body></html>`))
	}))
	defer server.Close()

	service := NewWebFetchService(config.WebFetchConfig{
		Enabled:          true,
		AllowPrivateHosts: true,
	}, t.TempDir())
	// Don't call SetLightpandaFetcher — lightpanda remains nil

	resp, err := service.Fetch(context.Background(), server.URL, "markdown", 0)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	// Without lightpanda, NeedsBrowser should remain true.
	if !resp.NeedsBrowser {
		t.Error("expected NeedsBrowser=true when lightpanda unavailable")
	}
}

func TestSetLightpandaFetcher(t *testing.T) {
	service := NewWebFetchService(config.WebFetchConfig{}, t.TempDir())
	binary := &LightpandaBinary{}
	service.SetLightpandaFetcher(binary)
	service.mu.RLock()
	if service.lightpanda != binary {
		t.Error("expected lightpanda to be set")
	}
	service.mu.RUnlock()
}

func TestFallbackHintForURL(t *testing.T) {
	cases := []struct {
		name         string
		rawURL       string
		needsBrowser bool
		body         string
		contentLen   int
		wantContains string
	}{
		{
			name:         "wechat degraded",
			rawURL:       "https://mp.weixin.qq.com/s/GhngoUnIS7CYjnGeFA7XZA",
			needsBrowser: true,
			wantContains: "curl",
		},
		{
			name:         "github page degraded",
			rawURL:       "https://github.com/foo/bar",
			contentLen:   50,
			wantContains: "git/trees",
		},
		{
			name:         "raw github degraded",
			rawURL:       "https://raw.githubusercontent.com/foo/bar/main/README.md",
			contentLen:   50,
			wantContains: "default_branch",
		},
		{
			name:         "npm page degraded",
			rawURL:       "https://www.npmjs.com/package/dsh-taskboard",
			needsBrowser: true,
			wantContains: "registry.npmjs.org",
		},
		{
			name:         "cloudflare challenge degraded",
			rawURL:       "https://example.com/page",
			needsBrowser: true,
			body:         "Just a moment...",
			wantContains: "Cloudflare",
		},
		{
			name:         "successful fetch no hint",
			rawURL:       "https://example.com/page",
			contentLen:   5000,
			wantContains: "",
		},
		{
			name:         "unknown host no hint",
			rawURL:       "https://docs.example.com/page",
			contentLen:   50,
			wantContains: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hint := fallbackHintForURL(tc.rawURL, tc.needsBrowser, []byte(tc.body), tc.contentLen)
			if tc.wantContains == "" {
				if hint != "" {
					t.Fatalf("expected empty hint, got %q", hint)
				}
				return
			}
			if !strings.Contains(hint, tc.wantContains) {
				t.Fatalf("expected hint to contain %q, got %q", tc.wantContains, hint)
			}
		})
	}
}
