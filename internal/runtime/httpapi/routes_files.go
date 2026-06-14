package httpapi

import (
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/platform/workspacefs"
)

func registerFileRoutes(mux *http.ServeMux, protected func(http.Handler) http.Handler, manager *config.Manager) {
	mux.Handle("GET /files/list", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fsys, err := resolveFileRoot(r, manager)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		defer fsys.Close()
		dir := r.URL.Query().Get("dir")
		if dir == "" {
			dir = "."
		}
		items, err := fsys.ReadDir(dir)
		if err != nil {
			writeFileError(w, err)
			return
		}
		type fileEntry struct {
			Name   string `json:"name"`
			IsDir  bool   `json:"isDir"`
			Size   int64  `json:"size"`
			ModTime string `json:"modTime"`
		}
		result := make([]fileEntry, 0, len(items))
		for _, item := range items {
			info, err := item.Info()
			if err != nil {
				continue
			}
			result = append(result, fileEntry{
				Name:   item.Name(),
				IsDir:  item.IsDir(),
				Size:   info.Size(),
				ModTime: info.ModTime().UTC().Format("2006-01-02T15:04:05Z"),
			})
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"items": result})
	})))

	mux.Handle("GET /files/read", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fsys, err := resolveFileRoot(r, manager)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		defer fsys.Close()
		path := r.URL.Query().Get("path")
		if path == "" {
			writeError(w, http.StatusBadRequest, fmt.Errorf("path is required"))
			return
		}
		data, err := fsys.ReadFile(path)
		if err != nil {
			writeFileError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"path":    path,
			"content": string(data),
			"size":    len(data),
		})
	})))

	mux.Handle("PUT /files/write", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Root    string `json:"root"`
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if req.Path == "" {
			writeError(w, http.StatusBadRequest, fmt.Errorf("path is required"))
			return
		}
		fsys, err := resolveFileRootFromParam(r, manager, req.Root)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		defer fsys.Close()
		data := []byte(req.Content)
		if err := fsys.WriteFile(req.Path, data, 0644); err != nil {
			writeFileError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"path": req.Path,
			"size": len(data),
		})
	})))

	mux.Handle("DELETE /files", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fsys, err := resolveFileRoot(r, manager)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		defer fsys.Close()
		path := r.URL.Query().Get("path")
		if path == "" {
			writeError(w, http.StatusBadRequest, fmt.Errorf("path is required"))
			return
		}
		abs, err := fsys.Abs(path)
		if err != nil {
			writeFileError(w, err)
			return
		}
		// Use os.RemoveAll which handles both files and directories
		if err := os.RemoveAll(abs); err != nil {
			writeFileError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})))

	mux.Handle("POST /files/mkdir", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Root string `json:"root"`
			Path string `json:"path"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if req.Path == "" {
			writeError(w, http.StatusBadRequest, fmt.Errorf("path is required"))
			return
		}
		fsys, err := resolveFileRootFromParam(r, manager, req.Root)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		defer fsys.Close()
		abs, err := fsys.Abs(req.Path)
		if err != nil {
			writeFileError(w, err)
			return
		}
		if err := os.MkdirAll(abs, 0755); err != nil {
			writeFileError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"path": req.Path})
	})))

	mux.Handle("POST /files/rename", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Root string `json:"root"`
			From string `json:"from"`
			To   string `json:"to"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if req.From == "" || req.To == "" {
			writeError(w, http.StatusBadRequest, fmt.Errorf("from and to are required"))
			return
		}
		fsys, err := resolveFileRootFromParam(r, manager, req.Root)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		defer fsys.Close()
		absFrom, err := fsys.Abs(req.From)
		if err != nil {
			writeFileError(w, err)
			return
		}
		absTo, err := fsys.Abs(req.To)
		if err != nil {
			writeFileError(w, err)
			return
		}
		if err := os.Rename(absFrom, absTo); err != nil {
			writeFileError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"from": req.From, "to": req.To})
	})))

	// GET /files/search?q=...&mode=name|content&root=...
	mux.Handle("GET /files/search", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fsys, err := resolveFileRoot(r, manager)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		defer fsys.Close()
		query := strings.TrimSpace(r.URL.Query().Get("q"))
		mode := strings.TrimSpace(r.URL.Query().Get("mode"))
		if query == "" {
			writeJSON(w, http.StatusOK, map[string]interface{}{"items": []interface{}{}})
			return
		}
		if mode == "" {
			mode = "name"
		}
		results, err := searchFiles(fsys, query, mode)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"items": results})
	})))
}

func resolveFileRoot(r *http.Request, manager *config.Manager) (*workspacefs.FS, error) {
	root := r.URL.Query().Get("root")
	return resolveFileRootFromParam(r, manager, root)
}

func resolveFileRootFromParam(r *http.Request, manager *config.Manager, root string) (*workspacefs.FS, error) {
	if strings.TrimSpace(root) == "" {
		root = manager.Current().WorkspaceDir
	}
	return workspacefs.New(root)
}

func writeFileError(w http.ResponseWriter, err error) {
	if os.IsNotExist(err) || errors.Is(err, fs.ErrNotExist) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if os.IsPermission(err) || errors.Is(err, fs.ErrPermission) {
		writeError(w, http.StatusForbidden, err)
		return
	}
	text := strings.ToLower(err.Error())
	if strings.Contains(text, "escape") || strings.Contains(text, "outside") {
		writeError(w, http.StatusForbidden, err)
		return
	}
	writeError(w, http.StatusInternalServerError, err)
}

type fileSearchResult struct {
	Path   string `json:"path"`
	IsDir  bool   `json:"isDir"`
	Size   int64  `json:"size"`
}

var skipDirs = map[string]bool{
	".git": true, "node_modules": true, ".godex": true,
	"__pycache__": true, ".venv": true, "vendor": true,
}

func searchFiles(fsys *workspacefs.FS, query, mode string) ([]fileSearchResult, error) {
	queryLower := strings.ToLower(query)
	var results []fileSearchResult
	const maxResults = 200

	err := filepath.WalkDir(fsys.Dir(), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip inaccessible
		}
		rel, relErr := filepath.Rel(fsys.Dir(), p)
		if relErr != nil {
			return nil
		}
		if rel == "." {
			return nil
		}
		if d.IsDir() && skipDirs[d.Name()] {
			return filepath.SkipDir
		}

		if mode == "name" {
			if !strings.Contains(strings.ToLower(d.Name()), queryLower) {
				return nil
			}
		} else {
			// content mode: only search files, not dirs
			if d.IsDir() {
				return nil
			}
			info, infoErr := d.Info()
			if infoErr != nil || info.Size() > 512*1024 {
				return nil // skip files > 512KB
			}
			data, readErr := fsys.ReadFile(rel)
			if readErr != nil {
				return nil
			}
			if !strings.Contains(strings.ToLower(string(data)), queryLower) {
				return nil
			}
		}

		info, _ := d.Info()
		size := int64(0)
		if info != nil {
			size = info.Size()
		}
		results = append(results, fileSearchResult{
			Path:  rel,
			IsDir: d.IsDir(),
			Size:  size,
		})
		if len(results) >= maxResults {
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return results, nil
}
