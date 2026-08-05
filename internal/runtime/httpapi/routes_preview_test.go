package httpapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustPreviewHandler(t *testing.T, workspaceDir string) http.Handler {
	t.Helper()
	cfg := newTestConfig(t)
	cfg.WorkspaceDir = workspaceDir
	manager := newTestManager(t, cfg)
	return NewHandlerWithRuntime(manager, nil, nil, nil, nil, nil, nil, nil)
}

func TestPreviewStaticServesIndex(t *testing.T) {
	cfg := newTestConfig(t)
	workspace := cfg.WorkspaceDir
	os.WriteFile(filepath.Join(workspace, "index.html"), []byte("<h1>hello</h1>"), 0644)

	manager := newTestManager(t, cfg)
	handler := NewHandlerWithRuntime(manager, nil, nil, nil, nil, nil, nil, nil)

	r := httptest.NewRequest("GET", "/preview/static/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "hello") {
		t.Errorf("expected index.html body, got %q", w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("expected text/html content type, got %q", ct)
	}
}

func TestPreviewStaticServesSubpathFile(t *testing.T) {
	cfg := newTestConfig(t)
	workspace := cfg.WorkspaceDir
	os.MkdirAll(filepath.Join(workspace, "dist"), 0755)
	os.WriteFile(filepath.Join(workspace, "dist", "app.js"), []byte("console.log(1)"), 0644)

	manager := newTestManager(t, cfg)
	handler := NewHandlerWithRuntime(manager, nil, nil, nil, nil, nil, nil, nil)

	r := httptest.NewRequest("GET", "/preview/static/dist/app.js", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "console.log(1)") {
		t.Errorf("expected app.js body, got %q", w.Body.String())
	}
}

func TestPreviewStaticSpaFallbackToIndex(t *testing.T) {
	cfg := newTestConfig(t)
	workspace := cfg.WorkspaceDir
	os.WriteFile(filepath.Join(workspace, "index.html"), []byte("<div id=app></div>"), 0644)

	manager := newTestManager(t, cfg)
	handler := NewHandlerWithRuntime(manager, nil, nil, nil, nil, nil, nil, nil)

	// A client-side route like /preview/static/dashboard/settings should
	// fall back to index.html so the SPA can render the route client-side.
	r := httptest.NewRequest("GET", "/preview/static/dashboard/settings", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for SPA fallback, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "<div id=app>") {
		t.Errorf("expected index.html fallback body, got %q", w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("expected text/html for SPA fallback, got %q", ct)
	}
}

func TestPreviewStaticDirectoryServesItsIndex(t *testing.T) {
	cfg := newTestConfig(t)
	os.MkdirAll(filepath.Join(cfg.WorkspaceDir, "docs"), 0755)
	os.WriteFile(filepath.Join(cfg.WorkspaceDir, "docs", "index.html"), []byte("<h1>docs</h1>"), 0644)

	manager := newTestManager(t, cfg)
	handler := NewHandlerWithRuntime(manager, nil, nil, nil, nil, nil, nil, nil)

	r := httptest.NewRequest("GET", "/preview/static/docs", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "<h1>docs</h1>") {
		t.Errorf("expected docs index body, got %q", w.Body.String())
	}
}

func TestPreviewStaticMissingFile(t *testing.T) {
	cfg := newTestConfig(t)
	// No index.html at all: everything 404s.
	manager := newTestManager(t, cfg)
	handler := NewHandlerWithRuntime(manager, nil, nil, nil, nil, nil, nil, nil)

	r := httptest.NewRequest("GET", "/preview/static/nope.txt", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestPreviewStaticRejectsTraversal(t *testing.T) {
	cfg := newTestConfig(t)
	workspace := cfg.WorkspaceDir
	os.WriteFile(filepath.Join(workspace, "index.html"), []byte("<h1>hello</h1>"), 0644)
	secret := filepath.Join(filepath.Dir(workspace), "secret.txt")
	os.WriteFile(secret, []byte("topsecret"), 0644)

	manager := newTestManager(t, cfg)
	handler := NewHandlerWithRuntime(manager, nil, nil, nil, nil, nil, nil, nil)

	r := httptest.NewRequest("GET", "/preview/static/../secret.txt", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code == http.StatusOK && strings.Contains(w.Body.String(), "topsecret") {
		t.Fatalf("path traversal leaked secret: %s", w.Body.String())
	}
}

func TestPreviewRequiresQueryTokenWhenAuthEnabled(t *testing.T) {
	cfg := newTestConfig(t)
	workspace := cfg.WorkspaceDir
	os.WriteFile(filepath.Join(workspace, "index.html"), []byte("<h1>hello</h1>"), 0644)
	cfg.WebToken = "secret-token"

	manager := newTestManager(t, cfg)
	handler := NewHandlerWithRuntime(manager, nil, nil, nil, nil, nil, nil, nil)

	// Without token: 401.
	r := httptest.NewRequest("GET", "/preview/static/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d", w.Code)
	}

	// With query token (iframe style): 200.
	r2 := httptest.NewRequest("GET", "/preview/static/?token=secret-token", nil)
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, r2)
	if w2.Code != http.StatusOK {
		t.Fatalf("expected 200 with query token, got %d: %s", w2.Code, w2.Body.String())
	}
}

func TestPreviewProxyStripsFrameBustingHeaders(t *testing.T) {
	// Fake dev server that sets X-Frame-Options + CSP frame-ancestors.
	dev := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'; script-src 'self'")
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<h1>dev app</h1>"))
	}))
	defer dev.Close()

	port := strings.TrimPrefix(dev.URL, "http://127.0.0.1:")

	cfg := newTestConfig(t)
	manager := newTestManager(t, cfg)
	handler := NewHandlerWithRuntime(manager, nil, nil, nil, nil, nil, nil, nil)

	r := httptest.NewRequest("GET", "/preview/proxy/"+port+"/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("X-Frame-Options"); got != "" {
		t.Errorf("expected X-Frame-Options stripped, got %q", got)
	}
	csp := w.Header().Get("Content-Security-Policy")
	if strings.Contains(csp, "frame-ancestors") {
		t.Errorf("expected frame-ancestors stripped from CSP, got %q", csp)
	}
	if !strings.Contains(csp, "default-src 'self'") || !strings.Contains(csp, "script-src 'self'") {
		t.Errorf("expected other CSP directives preserved, got %q", csp)
	}
	if !strings.Contains(w.Body.String(), "dev app") {
		t.Errorf("expected proxied body, got %q", w.Body.String())
	}
}

func TestPreviewProxyInvalidPort(t *testing.T) {
	cfg := newTestConfig(t)
	manager := newTestManager(t, cfg)
	handler := NewHandlerWithRuntime(manager, nil, nil, nil, nil, nil, nil, nil)

	for _, path := range []string{"/preview/proxy/notaport/", "/preview/proxy/0/", "/preview/proxy/99999/"} {
		r := httptest.NewRequest("GET", path, nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400 for %s, got %d", path, w.Code)
		}
	}
}

func TestStripFrameAncestors(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"default-src 'self'; frame-ancestors 'none'; script-src 'self'", "default-src 'self'; script-src 'self'"},
		{"frame-ancestors https://example.com", ""},
		{"default-src 'self'", "default-src 'self'"},
		{"", ""},
	}
	for _, c := range cases {
		got := stripFrameAncestors(c.in)
		if got != c.want {
			t.Errorf("stripFrameAncestors(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
