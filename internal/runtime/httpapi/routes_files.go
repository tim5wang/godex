package httpapi

import (
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
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
