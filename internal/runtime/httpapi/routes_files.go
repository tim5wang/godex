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
		if err := fsys.RemoveAll(path); err != nil {
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
		if err := fsys.MkdirAll(req.Path, 0755); err != nil {
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
		if err := fsys.Rename(req.From, req.To); err != nil {
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

func resolveFileRoot(r *http.Request, manager *config.Manager) (workspacefs.FS, error) {
	root := r.URL.Query().Get("root")
	return resolveFileRootFromParam(r, manager, root)
}

func resolveFileRootFromParam(r *http.Request, manager *config.Manager, root string) (workspacefs.FS, error) {
	cfg := manager.Current()
	exec := cfg.Tools.Execution
	mode := strings.ToLower(strings.TrimSpace(exec.Mode))

	// SSH mode: create SFTP-backed FS
	if mode == "ssh" && strings.TrimSpace(exec.SSHTarget) != "" {
		workspace := strings.TrimSpace(exec.SSHWorkspace)
		if workspace == "" {
			workspace = "/tmp/godex-workspace"
		}
		return workspacefs.NewSSHFS(workspacefs.SSHConfig{
			Target:     exec.SSHTarget,
			Workspace:  workspace,
			SSHOptions: exec.SSHOptions,
		})
	}

	// Local/Docker mode: local OS filesystem
	if strings.TrimSpace(root) == "" {
		root = cfg.WorkspaceDir
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

func searchFiles(fsys workspacefs.FS, query, mode string) ([]fileSearchResult, error) {
	queryLower := strings.ToLower(query)
	var results []fileSearchResult
	const maxResults = 200

	// walkDir recursively traverses the workspace via the FS interface,
	// so it works for both local (os.Root) and remote (SFTP) backends.
	var walkDir func(relDir string) error
	walkDir = func(relDir string) error {
		if len(results) >= maxResults {
			return filepath.SkipAll
		}
		entries, err := fsys.ReadDir(relDir)
		if err != nil {
			return nil // skip inaccessible
		}
		for _, entry := range entries {
			if len(results) >= maxResults {
				return filepath.SkipAll
			}
			name := entry.Name()
			rel := filepath.Join(relDir, name)
			if relDir == "." {
				rel = name
			}

			if entry.IsDir() {
				if skipDirs[name] {
					continue
				}
				// Recurse into subdirectory
				if err := walkDir(rel); err != nil {
					return err
				}
				// Directory name match
				if mode == "name" && strings.Contains(strings.ToLower(name), queryLower) {
					info, _ := entry.Info()
					size := int64(0)
					if info != nil {
						size = info.Size()
					}
					results = append(results, fileSearchResult{
						Path:  rel,
						IsDir: true,
						Size:  size,
					})
				}
				continue
			}

			// File matching
			if mode == "name" {
				if !strings.Contains(strings.ToLower(name), queryLower) {
					continue
				}
			} else {
				// content mode: skip large files
				info, infoErr := entry.Info()
				if infoErr != nil || info.Size() > 512*1024 {
					continue
				}
				data, readErr := fsys.ReadFile(rel)
				if readErr != nil {
					continue
				}
				if !strings.Contains(strings.ToLower(string(data)), queryLower) {
					continue
				}
			}

			info, _ := entry.Info()
			size := int64(0)
			if info != nil {
				size = info.Size()
			}
			results = append(results, fileSearchResult{
				Path:  rel,
				IsDir: false,
				Size:  size,
			})
		}
		return nil
	}

	err := walkDir(".")
	if err != nil && err != filepath.SkipAll {
		return nil, err
	}
	return results, nil
}
