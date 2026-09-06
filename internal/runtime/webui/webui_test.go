package webui

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewHandlerFallsBackToEmbeddedWhenDistMissing(t *testing.T) {
	handler, err := NewHandler(http.NotFoundHandler(), filepath.Join(t.TempDir(), "missing"))
	if err != nil {
		t.Fatalf("expected embedded fallback, got %v", err)
	}

	server := httptest.NewServer(handler)
	defer server.Close()

	resp, err := http.Get(server.URL + "/")
	if err != nil {
		t.Fatalf("get root: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "<!doctype html") && !strings.Contains(strings.ToLower(string(body)), "<html") {
		t.Fatalf("expected embedded html page, got %q", string(body))
	}
}

func TestHandlerServesAssetsAndFallsBackToIndex(t *testing.T) {
	dist := t.TempDir()
	if err := os.WriteFile(filepath.Join(dist, "index.html"), []byte("<html>app</html>"), 0644); err != nil {
		t.Fatalf("write index.html: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dist, "assets"), 0755); err != nil {
		t.Fatalf("mkdir assets: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dist, "assets", "app.js"), []byte("console.log('ok')"), 0644); err != nil {
		t.Fatalf("write asset: %v", err)
	}

	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "api:"+r.URL.Path)
	})
	handler, err := NewHandler(api, dist)
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()

	resp, err := http.Get(server.URL + "/meta")
	if err != nil {
		t.Fatalf("get meta: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "app") {
		t.Fatalf("expected bare /meta to serve SPA index, got %q", string(body))
	}

	for _, apiPath := range []string{"/api/meta", "/api/memory", "/api/browser/frames"} {
		apiResp, err := http.Get(server.URL + apiPath)
		if err != nil {
			t.Fatalf("get %s: %v", apiPath, err)
		}
		apiBody, _ := io.ReadAll(apiResp.Body)
		apiResp.Body.Close()
		want := "api:" + strings.TrimPrefix(apiPath, "/api")
		if got := string(apiBody); got != want {
			t.Fatalf("expected /api prefix to strip before API handler, got %q", got)
		}
	}

	assetResp, err := http.Get(server.URL + "/assets/app.js")
	if err != nil {
		t.Fatalf("get asset: %v", err)
	}
	assetResp.Body.Close()
	if cacheControl := assetResp.Header.Get("Cache-Control"); !strings.Contains(cacheControl, "immutable") {
		t.Fatalf("expected immutable asset cache header, got %q", cacheControl)
	}

	pageResp, err := http.Get(server.URL + "/chat/demo")
	if err != nil {
		t.Fatalf("get spa route: %v", err)
	}
	pageBody, _ := io.ReadAll(pageResp.Body)
	pageResp.Body.Close()
	if !strings.Contains(string(pageBody), "app") {
		t.Fatalf("expected SPA fallback to index.html, got %q", string(pageBody))
	}

	for _, route := range []string{"/automation", "/memory"} {
		req, err := http.NewRequest(http.MethodGet, server.URL+route, nil)
		if err != nil {
			t.Fatalf("new page request %s: %v", route, err)
		}
		req.Header.Set("Accept", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("get page route %s: %v", route, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if !strings.Contains(string(body), "app") {
			t.Fatalf("expected %s to serve SPA index, got %q", route, string(body))
		}
	}
}

// TestHandlerRoutesV1PrefixToAPI covers the regression where POST /v1/chat/completions
// (the OpenAI-compatible usage gateway endpoint) was being intercepted by the webui SPA
// fallthrough and answered with index.html. Any path under /v1/ must be forwarded to
// the API handler verbatim, without stripping a prefix.
func TestHandlerRoutesV1PrefixToAPI(t *testing.T) {
	dist := t.TempDir()
	if err := os.WriteFile(filepath.Join(dist, "index.html"), []byte("<html>app</html>"), 0644); err != nil {
		t.Fatalf("write index.html: %v", err)
	}

	apiCalls := 0
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"path":"`+r.URL.Path+`","method":"`+r.Method+`"}`)
	})
	handler, err := NewHandler(api, dist)
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()

	for _, p := range []string{"/v1/chat/completions", "/v1/models"} {
		req, err := http.NewRequest(http.MethodPost, server.URL+p, strings.NewReader(`{"hello":"world"}`))
		if err != nil {
			t.Fatalf("new post %s: %v", p, err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("post %s: %v", p, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 for %s, got %d body=%q", p, resp.StatusCode, string(body))
		}
		if strings.Contains(strings.ToLower(string(body)), "<html") {
			t.Fatalf("expected JSON for %s, got SPA HTML: %q", p, string(body))
		}
		wantSub := `"path":"` + p + `"`
		if !strings.Contains(string(body), wantSub) {
			t.Fatalf("expected %q in body for %s, got %q", wantSub, p, string(body))
		}
	}
	if apiCalls != 2 {
		t.Fatalf("expected 2 api calls (one per path), got %d", apiCalls)
	}
}
