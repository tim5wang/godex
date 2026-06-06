package webui

import (
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path"
	"strings"

	uiassets "github.com/tim5wang/godex/internal/uiassets"
)

// NewHandler serves the built web UI and falls back to the API handler for
// backend routes. It prefers a built dist on disk for development, then
// falls back to embedded assets for single-binary deployments.
func NewHandler(api http.Handler, distDir string) (http.Handler, error) {
	if api == nil {
		return nil, fmt.Errorf("missing api handler")
	}
	fsys, err := loadDistFS(distDir)
	if err != nil {
		return nil, err
	}

	return &handler{
		api:        api,
		fs:         fsys,
		fileServer: http.FileServer(http.FS(fsys)),
	}, nil
}

func loadDistFS(distDir string) (fs.FS, error) {
	if distDir != "" {
		info, err := os.Stat(distDir)
		switch {
		case err == nil && info.IsDir():
			fsys := os.DirFS(distDir)
			if _, statErr := fs.Stat(fsys, "index.html"); statErr == nil {
				return fsys, nil
			}
		case err == nil && !info.IsDir():
			// Fall through to embedded assets.
		case os.IsNotExist(err):
			// Fall through to embedded assets.
		case err != nil:
			return nil, err
		}
	}

	fsys, err := uiassets.DistFS()
	if err != nil {
		if distDir != "" {
			return nil, fmt.Errorf("web build not found at %s and embedded web ui unavailable: %w", distDir, err)
		}
		return nil, err
	}
	if _, err := fs.Stat(fsys, "index.html"); err != nil {
		if distDir != "" {
			return nil, fmt.Errorf("web build not found at %s and embedded web ui is incomplete: %w", distDir, err)
		}
		return nil, fmt.Errorf("embedded web ui is incomplete: %w", err)
	}
	return fsys, nil
}

type handler struct {
	api        http.Handler
	fs         fs.FS
	fileServer http.Handler
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/api" {
		r2 := r.Clone(r.Context())
		r2.URL.Path = strings.TrimPrefix(r.URL.Path, "/api")
		if r2.URL.Path == "" {
			r2.URL.Path = "/"
		}
		h.api.ServeHTTP(w, r2)
		return
	}
	// /v1/* is the OpenAI-compatible surface (chat completions, models, etc.).
	// Forward verbatim to the API handler so curl/OpenAI clients are not
	// intercepted by the SPA fallthrough below. Do NOT strip a prefix.
	if strings.HasPrefix(r.URL.Path, "/v1/") || r.URL.Path == "/v1" {
		h.api.ServeHTTP(w, r)
		return
	}

	urlPath := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if urlPath == "." || urlPath == "" {
		urlPath = "index.html"
	}

	if info, err := fs.Stat(h.fs, urlPath); err == nil && !info.IsDir() {
		if strings.HasPrefix(urlPath, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		h.fileServer.ServeHTTP(w, r)
		return
	}

	r2 := r.Clone(r.Context())
	r2.URL.Path = "/"
	h.fileServer.ServeHTTP(w, r2)
}
