package httpapi

import (
	"fmt"
	"mime"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/tim5wang/godex/internal/core/config"
)

// registerPreviewRoutes adds the Web App Preview endpoints:
//
//   - GET /preview/static/{path...}  — static file serving rooted at the
//     session workspace (local or SSH, following the same execution-mode
//     resolution as the Files panel). Missing paths fall back to index.html
//     so single-page apps work out of the box.
//   - GET /preview/proxy/{port}/{path...} — reverse proxy to a dev server
//     running on 127.0.0.1:{port} (e.g. vite dev on 5173). X-Frame-Options
//     and CSP frame-ancestors are stripped so the app can render inside the
//     preview iframe; WebSocket upgrades pass through for HMR.
//
// Both routes accept authentication via Authorization header or a ?token=
// query parameter (iframes cannot set headers).
func registerPreviewRoutes(mux *http.ServeMux, manager *config.Manager) {
	previewAuth := withPreviewAuthProvider(func() string {
		return manager.Current().WebToken
	})

	staticHandler := func(w http.ResponseWriter, r *http.Request) {
		servePreviewStatic(w, r, manager)
	}
	proxyHandler := func(w http.ResponseWriter, r *http.Request) {
		servePreviewProxy(w, r)
	}

	// Static: /preview/static and /preview/static/{path...}
	mux.Handle("GET /preview/static", previewAuth(http.HandlerFunc(staticHandler)))
	mux.Handle("GET /preview/static/{path...}", previewAuth(http.HandlerFunc(staticHandler)))

	// Proxy: /preview/proxy/{port} and /preview/proxy/{port}/{path...}
	mux.Handle("GET /preview/proxy/{port}", previewAuth(http.HandlerFunc(proxyHandler)))
	mux.Handle("GET /preview/proxy/{port}/{path...}", previewAuth(http.HandlerFunc(proxyHandler)))
}

// previewTokenCookie lets iframe subresources (images, css, js, nested
// iframes) authenticate without repeating the token in every URL: once the
// top-level preview URL passes with ?token=, the cookie is set on the
// response and the browser sends it for same-origin subresource requests.
const previewTokenCookie = "godex_preview_token"

func withPreviewAuthProvider(token func() string) func(http.Handler) http.Handler {
	return func(handler http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tok := strings.TrimSpace(token())
			if tok == "" {
				handler.ServeHTTP(w, r)
				return
			}
			if bearerAuthorized(r, tok) || previewCookieAuthorized(r, tok) {
				handler.ServeHTTP(w, r)
				return
			}
			if strings.TrimSpace(r.URL.Query().Get("token")) == tok {
				http.SetCookie(w, &http.Cookie{
					Name:     previewTokenCookie,
					Value:    tok,
					Path:     "/api/preview",
					SameSite: http.SameSiteLaxMode,
					Secure:   r.TLS != nil,
				})
				handler.ServeHTTP(w, r)
				return
			}
			writeError(w, http.StatusUnauthorized, fmt.Errorf("missing or invalid bearer token"))
		})
	}
}

func previewCookieAuthorized(r *http.Request, token string) bool {
	c, err := r.Cookie(previewTokenCookie)
	if err != nil {
		return false
	}
	return c.Value != "" && c.Value == token
}

// withPreviewAuthProvider is like withBearerAuthProvider but also accepts the
// token via a ?token= query parameter, which is required for iframe-based
// consumers (a preview iframe cannot attach an Authorization header).


// servePreviewStatic serves files from the resolved workspace FS. If the
// requested path does not exist, it falls back to index.html (SPA support).
func servePreviewStatic(w http.ResponseWriter, r *http.Request, manager *config.Manager) {
	fsys, err := resolveFileRoot(r, manager)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	defer fsys.Close()

	rel := strings.TrimPrefix(r.PathValue("path"), "/")
	if rel == "" || rel == "." {
		rel = "index.html"
	}
	// Reject obvious path traversal before hitting the FS.
	if strings.Contains(rel, "..") {
		writeError(w, http.StatusNotFound, fmt.Errorf("not found: %s", rel))
		return
	}

	spaFallback := false
	data, err := fsys.ReadFile(rel)
	if err != nil {
		// Try <dir>/index.html (directory request), then root index.html (SPA).
		if dirData, dirErr := fsys.ReadFile(strings.TrimSuffix(rel, "/") + "/index.html"); dirErr == nil {
			data = dirData
		} else if rootIndex, rootErr := fsys.ReadFile("index.html"); rootErr == nil {
			data = rootIndex
			spaFallback = true
		} else {
			writeFileError(w, err)
			return
		}
	}

	contentType := mime.TypeByExtension(filepath.Ext(rel))
	if spaFallback || contentType == "" {
		contentType = "text/html; charset=utf-8"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(data)
}

// servePreviewProxy reverse-proxies to a dev server on 127.0.0.1:{port}.
// Only loopback targets are allowed (SSRF guard). X-Frame-Options and CSP
// frame-ancestors are removed so the target renders inside the iframe;
// WebSocket upgrades are passed through for HMR support.
func servePreviewProxy(w http.ResponseWriter, r *http.Request) {
	portStr := r.PathValue("port")
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid port %q", portStr))
		return
	}

	target := &url.URL{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%d", port)}
	proxy := httputil.NewSingleHostReverseProxy(target)

	prefix := "/preview/proxy/" + portStr
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		rest := strings.TrimPrefix(req.URL.Path, prefix)
		if rest == "" {
			rest = "/"
		}
		req.URL.Path = rest
		req.URL.RawPath = ""
	}

	proxy.ModifyResponse = func(resp *http.Response) error {
		// Allow embedding inside the preview iframe.
		resp.Header.Del("X-Frame-Options")
		if csp := resp.Header.Get("Content-Security-Policy"); csp != "" {
			resp.Header.Set("Content-Security-Policy", stripFrameAncestors(csp))
		}
		return nil
	}

	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, proxyErr error) {
		writeError(w, http.StatusBadGateway, fmt.Errorf("preview proxy to 127.0.0.1:%d: %w", port, proxyErr))
	}

	proxy.ServeHTTP(w, r)
}

// stripFrameAncestors removes frame-ancestors directives from a CSP header
// while preserving every other directive.
func stripFrameAncestors(csp string) string {
	directives := strings.Split(csp, ";")
	out := make([]string, 0, len(directives))
	for _, d := range directives {
		trimmed := strings.TrimSpace(d)
		if trimmed == "" || strings.HasPrefix(strings.ToLower(trimmed), "frame-ancestors") {
			continue
		}
		out = append(out, trimmed)
	}
	return strings.Join(out, "; ")
}
