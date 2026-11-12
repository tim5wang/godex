package httpapi

import (
	"context"
	"errors"
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
	Repo bool `json:"repo"`
	// Base names the comparison the diff was taken against so the UI never
	// has to guess: "worktree" = index↔working tree, "staged" = HEAD↔index,
	// "head" = HEAD↔working tree (staged + unstaged combined).
	Base      string `json:"base,omitempty"`
	Diff      string `json:"diff,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	// Untracked marks a path git does not know yet: it has no index entry, so
	// the diff is produced with --no-index against /dev/null.
	Untracked bool   `json:"untracked,omitempty"`
	Error     string `json:"error,omitempty"`
}

// gitStatusFile is one entry of `git status --porcelain=v1 -z`. Index and
// Worktree are the raw porcelain columns (" " means "no change on that side"),
// so the UI can distinguish staged from unstaged without another request.
type gitStatusFile struct {
	Path      string `json:"path"`
	OrigPath  string `json:"origPath,omitempty"`
	Index     string `json:"index"`
	Worktree  string `json:"worktree"`
	Untracked bool   `json:"untracked,omitempty"`
}

type gitStatusResponse struct {
	Repo   bool            `json:"repo"`
	Branch string          `json:"branch,omitempty"`
	Files  []gitStatusFile `json:"files,omitempty"`
	Error  string          `json:"error,omitempty"`
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
	// Read-only status list: which files changed and on which side (index vs
	// working tree). This is what lets the Files surface show added / modified /
	// deleted / renamed / untracked badges. It never writes to the repository.
	mux.Handle("GET /git/status", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handleGitStatus(w, r, manager)
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

	// base selects the comparison the caller wants; the default keeps the
	// historical behaviour (unstaged changes only) so existing callers such as
	// the chat Changes card are unaffected.
	base := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("base")))
	var args []string
	switch base {
	case "", "worktree":
		base = "worktree"
		args = []string{"diff", "--no-color", "--"}
	case "staged":
		args = []string{"diff", "--cached", "--no-color", "--"}
	case "head":
		// HEAD↔working tree: staged and unstaged changes in one baseline.
		args = []string{"diff", "HEAD", "--no-color", "--"}
	default:
		writeError(w, http.StatusBadRequest, &httpError{Status: http.StatusBadRequest, Message: "base must be worktree, staged or head: " + base})
		return
	}
	if path != "" {
		args = append(args, path)
	}
	out, err := runGit(r.Context(), absRoot, args...)
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		writeJSON(w, http.StatusOK, gitDiffResponse{Repo: true, Base: base, Error: err.Error()})
		return
	}

	// An untracked file has no index entry, so `git diff` is empty for it even
	// though the file is new. Compare against /dev/null instead; --no-index exits
	// 1 whenever the inputs differ, which is the normal case here.
	untracked := false
	if path != "" && out == "" && base != "staged" && gitPathUntracked(r.Context(), absRoot, path) {
		untracked = true
		noIndexArgs := []string{"diff", "--no-color", "--no-index", "--", "/dev/null", path}
		if raw, rawErr := runGitRaw(r.Context(), absRoot, noIndexArgs...); raw != "" && (rawErr == nil || gitExitCodeOne(rawErr)) {
			out = strings.TrimSpace(raw)
		}
	}

	truncated := len(out) > maxGitDiffBytes
	if truncated {
		out = out[:maxGitDiffBytes] + "\n… (diff truncated)"
	}
	writeJSON(w, http.StatusOK, gitDiffResponse{Repo: true, Base: base, Diff: out, Truncated: truncated, Untracked: untracked})
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

func handleGitStatus(w http.ResponseWriter, r *http.Request, manager *config.Manager) {
	cfg := manager.Current()
	execConfig := cfg.Tools.Execution
	if mode := strings.ToLower(strings.TrimSpace(execConfig.Mode)); mode == "ssh" && strings.TrimSpace(execConfig.SSHTarget) != "" {
		writeJSON(w, http.StatusOK, gitStatusResponse{Repo: false})
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
		writeJSON(w, http.StatusOK, gitStatusResponse{Repo: false})
		return
	}

	// -z keeps paths unquoted and NUL-separated, which is the only form that
	// survives newlines and non-ASCII names. Untracked files are listed
	// individually (--untracked-files=all) so a new directory expands.
	out, err := runGitRaw(r.Context(), absRoot, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		writeJSON(w, http.StatusOK, gitStatusResponse{Repo: true, Error: err.Error()})
		return
	}
	// A repository without commits has no resolvable branch; the status list is
	// still meaningful, so an empty branch name is not an error.
	branch, _ := runGit(r.Context(), absRoot, "rev-parse", "--abbrev-ref", "HEAD")
	writeJSON(w, http.StatusOK, gitStatusResponse{Repo: true, Branch: branch, Files: parseGitStatus(out)})
}

// parseGitStatus decodes `git status --porcelain=v1 -z` output.
//
// Each record is "XY <path>", NUL-terminated. The first column is the index
// (staged) state and the second the working-tree state, and either may be a
// space — which is why this must not be run through a space-trimming helper.
// Rename/copy records are followed by a second record holding the original path.
func parseGitStatus(output string) []gitStatusFile {
	records := strings.Split(output, "\x00")
	files := make([]gitStatusFile, 0, len(records))
	for i := 0; i < len(records); i++ {
		record := records[i]
		if len(record) < 4 {
			continue
		}
		file := gitStatusFile{
			Path:     record[3:],
			Index:    string(record[0]),
			Worktree: string(record[1]),
		}
		if record[0] == '?' && record[1] == '?' {
			file.Untracked = true
		}
		// With -z the original path of a rename/copy arrives as its own record.
		if (record[0] == 'R' || record[0] == 'C' || record[1] == 'R' || record[1] == 'C') && i+1 < len(records) {
			file.OrigPath = records[i+1]
			i++
		}
		files = append(files, file)
	}
	return files
}

// gitPathUntracked reports whether path is untracked (known to git on disk but
// not in the index). ls-files --error-unmatch exits non-zero in that case.
func gitPathUntracked(ctx context.Context, dir, path string) bool {
	_, err := runGit(ctx, dir, "ls-files", "--error-unmatch", "--", path)
	return err != nil
}

// gitExitCodeOne reports whether err is git's "differences found" exit status,
// which --no-index uses as a normal result rather than a failure.
func gitExitCodeOne(err error) bool {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return false
	}
	return exitErr.ExitCode() == 1
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

// runGitRaw returns stdout untrimmed. Porcelain -z output starts with the two
// status columns and the first one is a space for an unstaged-only change, so
// trimming it would silently turn " M file" into "M file" (i.e. staged).
func runGitRaw(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	return string(out), err
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
