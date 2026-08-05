package httpapi

import (
	"net/http"
	"os/exec"
	"path/filepath"
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
	if !gitRepo(absRoot) {
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
	out, err := runGit(absRoot, args...)
	if err != nil {
		writeJSON(w, http.StatusOK, gitDiffResponse{Repo: true, Error: err.Error()})
		return
	}

	truncated := len(out) > maxGitDiffBytes
	if truncated {
		out = out[:maxGitDiffBytes] + "\n… (diff truncated)"
	}
	writeJSON(w, http.StatusOK, gitDiffResponse{Repo: true, Diff: out, Truncated: truncated})
}

// gitRepo reports whether dir is inside a git working tree.
func gitRepo(dir string) bool {
	_, err := runGit(dir, "rev-parse", "--is-inside-work-tree")
	return err == nil
}

// runGit executes git in dir and returns trimmed combined output.
func runGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
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
