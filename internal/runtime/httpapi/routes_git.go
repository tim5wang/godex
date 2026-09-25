package httpapi

import (
	"context"
	"net/http"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/tim5wang/godex/internal/core/config"
)

// maxGitDiffBytes caps the unified diff returned by /git/diff so a huge
// working tree change cannot blow up the response.
const maxGitDiffBytes = 256 * 1024

type gitDiffResponse struct {
	Repo      bool   `json:"repo"`
	Diff      string `json:"diff,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	Error     string `json:"error,omitempty"`
}

type gitDiffFileStats struct {
	Path    string `json:"path"`
	Added   int    `json:"added"`
	Deleted int    `json:"deleted"`
}

type gitDiffStatsResponse struct {
	Repo  bool               `json:"repo"`
	Files []gitDiffFileStats `json:"files,omitempty"`
	Error string             `json:"error,omitempty"`
}

// registerGitRoutes adds Git-backed endpoints for the chat "Changes" card:
//
//   - GET /git/diff?root=<workspace>&path=<rel> — returns the working-tree
//     unified diff for one file (or the whole tree when path is omitted).
//     Only local git repositories are supported; SSH execution mode and
//     non-git directories respond with {repo:false} so the UI can degrade
//     gracefully to a plain file list.
//
// The route is protected like the files routes (Bearer or ?token= auth).
func registerGitRoutes(mux *http.ServeMux, protected func(http.Handler) http.Handler, manager *config.Manager) {
	mux.Handle("GET /git/diff", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handleGitDiff(w, r, manager)
	})))
	mux.Handle("GET /git/diff-stats", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handleGitDiffStats(w, r, manager)
	})))
}

func handleGitDiff(w http.ResponseWriter, r *http.Request, manager *config.Manager) {
	cfg := manager.Current()
	exec := cfg.Tools.Execution

	// SSH mode: git runs on the remote host; we do not shell out over SSH here.
	mode := strings.ToLower(strings.TrimSpace(exec.Mode))
	if mode == "ssh" && strings.TrimSpace(exec.SSHTarget) != "" {
		writeJSON(w, http.StatusOK, gitDiffResponse{Repo: false})
		return
	}

	root := strings.TrimSpace(r.URL.Query().Get("root"))
	if root == "" {
		root = cfg.WorkspaceDir
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !gitRepo(r.Context(), absRoot) {
		if r.Context().Err() != nil {
			return
		}
		writeJSON(w, http.StatusOK, gitDiffResponse{Repo: false})
		return
	}

	path := strings.TrimSpace(r.URL.Query().Get("path"))
	if path != "" {
		// Reject traversal: the path must stay inside the workspace root.
		if strings.Contains(path, "..") || filepath.IsAbs(path) {
			writeError(w, http.StatusBadRequest, errPathOutsideWorkspace(path))
			return
		}
	}

	args := []string{"diff", "--no-color", "--"}
	if path != "" {
		args = append(args, path)
	}
	out, err := runGit(r.Context(), absRoot, args...)
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		writeJSON(w, http.StatusOK, gitDiffResponse{Repo: true, Error: err.Error()})
		return
	}

	truncated := len(out) > maxGitDiffBytes
	if truncated {
		out = out[:maxGitDiffBytes] + "\n… (diff truncated)"
	}
	writeJSON(w, http.StatusOK, gitDiffResponse{Repo: true, Diff: out, Truncated: truncated})
}

func handleGitDiffStats(w http.ResponseWriter, r *http.Request, manager *config.Manager) {
	cfg := manager.Current()
	execConfig := cfg.Tools.Execution
	if mode := strings.ToLower(strings.TrimSpace(execConfig.Mode)); mode == "ssh" && strings.TrimSpace(execConfig.SSHTarget) != "" {
		writeJSON(w, http.StatusOK, gitDiffStatsResponse{Repo: false})
		return
	}

	root := strings.TrimSpace(r.URL.Query().Get("root"))
	if root == "" {
		root = cfg.WorkspaceDir
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !gitRepo(r.Context(), absRoot) {
		if r.Context().Err() != nil {
			return
		}
		writeJSON(w, http.StatusOK, gitDiffStatsResponse{Repo: false})
		return
	}

	paths := r.URL.Query()["path"]
	args := []string{"diff", "--numstat", "-z", "--"}
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		if strings.Contains(path, "..") || filepath.IsAbs(path) {
			writeError(w, http.StatusBadRequest, errPathOutsideWorkspace(path))
			return
		}
		args = append(args, path)
	}
	if len(args) == 4 {
		writeJSON(w, http.StatusOK, gitDiffStatsResponse{Repo: true, Files: []gitDiffFileStats{}})
		return
	}

	out, err := runGit(r.Context(), absRoot, args...)
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		writeJSON(w, http.StatusOK, gitDiffStatsResponse{Repo: true, Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, gitDiffStatsResponse{Repo: true, Files: parseGitDiffNumstat(out)})
}

func parseGitDiffNumstat(output string) []gitDiffFileStats {
	records := strings.Split(output, "\x00")
	files := make([]gitDiffFileStats, 0, len(records))
	for i := 0; i < len(records); i++ {
		if records[i] == "" {
			continue
		}
		fields := strings.SplitN(records[i], "\t", 3)
		if len(fields) != 3 {
			continue
		}
		path := fields[2]
		if path == "" && i+2 < len(records) {
			// With -z, git emits an empty path field followed by old and new
			// path records for a rename.
			path = records[i+2]
			i += 2
		}
		added, _ := strconv.Atoi(fields[0])
		deleted, _ := strconv.Atoi(fields[1])
		if path == "" {
			continue
		}
		files = append(files, gitDiffFileStats{Path: path, Added: added, Deleted: deleted})
	}
	return files
}

// gitRepo reports whether dir is inside a git working tree.
func gitRepo(ctx context.Context, dir string) bool {
	_, err := runGit(ctx, dir, "rev-parse", "--is-inside-work-tree")
	return err == nil
}

// runGit executes git in dir and returns trimmed combined output.
func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func errPathOutsideWorkspace(path string) error {
	return &httpError{Status: http.StatusBadRequest, Message: "path must be workspace-relative: " + path}
}

// httpError is a small typed error the writeError helper understands.
type httpError struct {
	Status  int
	Message string
}

func (e *httpError) Error() string { return e.Message }
