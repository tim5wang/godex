package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// initTestGitRepoWithStates builds a repo that exercises every status column:
// an unstaged-only modification, a staged modification, an untracked file and
// a staged rename. Each needs a different git plumbing path, and the unstaged
// one is the regression guard for the leading-space column.
func initTestGitRepoWithStates(t *testing.T, dir string) {
	t.Helper()
	runTestGit(t, dir, "init", "-q", "-b", "main")
	runTestGit(t, dir, "config", "user.email", "test@godex.local")
	runTestGit(t, dir, "config", "user.name", "GoDex Test")
	os.WriteFile(filepath.Join(dir, "unstaged.go"), []byte("package main\n"), 0644)
	os.WriteFile(filepath.Join(dir, "staged.go"), []byte("package main\n"), 0644)
	os.WriteFile(filepath.Join(dir, "old_name.go"), []byte("package main\n"), 0644)
	runTestGit(t, dir, "add", "-A")
	runTestGit(t, dir, "commit", "-q", "-m", "init")

	// Unstaged-only: index and HEAD agree, working tree differs.
	os.WriteFile(filepath.Join(dir, "unstaged.go"), []byte("package main\n\nfunc a() {}\n"), 0644)
	// Staged: index differs from HEAD, working tree matches the index.
	os.WriteFile(filepath.Join(dir, "staged.go"), []byte("package main\n\nfunc b() {}\n"), 0644)
	runTestGit(t, dir, "add", "staged.go")
	// Rename, staged.
	runTestGit(t, dir, "mv", "old_name.go", "new_name.go")
	// Untracked.
	os.WriteFile(filepath.Join(dir, "untracked.go"), []byte("package main\n"), 0644)
}

func gitTestHandler(t *testing.T, workspace string) http.Handler {
	t.Helper()
	cfg := newTestConfig(t)
	cfg.WorkspaceDir = workspace
	manager := newTestManager(t, cfg)
	return NewHandlerWithRuntime(manager, nil, nil, nil, nil, nil, nil, nil)
}

func TestGitStatusDistinguishesStagedAndUnstaged(t *testing.T) {
	workspace := t.TempDir()
	initTestGitRepoWithStates(t, workspace)
	handler := gitTestHandler(t, workspace)

	r := httptest.NewRequest("GET", "/git/status", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp gitStatusResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Repo {
		t.Fatalf("expected repo=true, got %+v", resp)
	}
	if resp.Branch != "main" {
		t.Errorf("expected branch main, got %q", resp.Branch)
	}

	byPath := map[string]gitStatusFile{}
	for _, file := range resp.Files {
		byPath[file.Path] = file
	}

	// The unstaged-only entry must keep its empty index column. If the porcelain
	// output were trimmed, this would read "M " instead and look staged.
	unstaged, ok := byPath["unstaged.go"]
	if !ok {
		t.Fatalf("missing unstaged.go in %+v", resp.Files)
	}
	if unstaged.Index != " " || unstaged.Worktree != "M" {
		t.Errorf("expected unstaged.go index=%q worktree=%q, got index=%q worktree=%q",
			" ", "M", unstaged.Index, unstaged.Worktree)
	}

	staged, ok := byPath["staged.go"]
	if !ok {
		t.Fatalf("missing staged.go in %+v", resp.Files)
	}
	if staged.Index != "M" || staged.Worktree != " " {
		t.Errorf("expected staged.go index=%q worktree=%q, got index=%q worktree=%q",
			"M", " ", staged.Index, staged.Worktree)
	}

	untracked, ok := byPath["untracked.go"]
	if !ok {
		t.Fatalf("missing untracked.go in %+v", resp.Files)
	}
	if !untracked.Untracked {
		t.Errorf("expected untracked.go to be flagged untracked, got %+v", untracked)
	}

	renamed, ok := byPath["new_name.go"]
	if !ok {
		t.Fatalf("missing new_name.go in %+v", resp.Files)
	}
	if renamed.Index != "R" || renamed.OrigPath != "old_name.go" {
		t.Errorf("expected rename new_name.go <- old_name.go, got %+v", renamed)
	}
}

func TestGitStatusNonGitDir(t *testing.T) {
	handler := gitTestHandler(t, t.TempDir())

	r := httptest.NewRequest("GET", "/git/status", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp gitStatusResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Repo {
		t.Fatalf("expected repo=false for non-git dir, got %+v", resp)
	}
}

func TestGitStatusRejectsTraversalViaDiffPaths(t *testing.T) {
	workspace := t.TempDir()
	initTestGitRepoWithStates(t, workspace)
	handler := gitTestHandler(t, workspace)

	// The status route takes no path argument, but its sibling diff routes do;
	// keep the traversal guarantee covered for the shared root resolution.
	r := httptest.NewRequest("GET", "/git/diff?base=head&path=../outside.go", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for traversal with base=head, got %d", w.Code)
	}
}

func TestGitDiffBaseModes(t *testing.T) {
	workspace := t.TempDir()
	initTestGitRepoWithStates(t, workspace)
	handler := gitTestHandler(t, workspace)

	fetch := func(query string) gitDiffResponse {
		t.Helper()
		r := httptest.NewRequest("GET", "/git/diff?"+query, nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 for %q, got %d: %s", query, w.Code, w.Body.String())
		}
		var resp gitDiffResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return resp
	}

	// worktree (default): only the unstaged file appears.
	worktree := fetch("path=unstaged.go")
	if worktree.Base != "worktree" {
		t.Errorf("expected base worktree, got %q", worktree.Base)
	}
	if !contains(worktree.Diff, "func a()") {
		t.Errorf("expected unstaged content in worktree diff, got %q", worktree.Diff)
	}

	// A staged-only change is invisible in the worktree baseline but present in
	// the staged baseline — this is the distinction the UI has to label.
	if got := fetch("path=staged.go"); got.Diff != "" {
		t.Errorf("expected empty worktree diff for staged-only file, got %q", got.Diff)
	}
	staged := fetch("base=staged&path=staged.go")
	if staged.Base != "staged" {
		t.Errorf("expected base staged, got %q", staged.Base)
	}
	if !contains(staged.Diff, "func b()") {
		t.Errorf("expected staged diff for staged.go, got %q", staged.Diff)
	}

	// head: staged and unstaged together.
	head := fetch("base=head&path=staged.go")
	if head.Base != "head" || !contains(head.Diff, "func b()") {
		t.Errorf("expected head baseline to include the staged change, got base=%q diff=%q", head.Base, head.Diff)
	}
}

func TestGitDiffUntrackedFile(t *testing.T) {
	workspace := t.TempDir()
	initTestGitRepoWithStates(t, workspace)
	handler := gitTestHandler(t, workspace)

	r := httptest.NewRequest("GET", "/git/diff?path=untracked.go", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp gitDiffResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Untracked {
		t.Errorf("expected untracked=true, got %+v", resp)
	}
	// Without the --no-index path this would be empty: git diff ignores
	// untracked files entirely.
	if !contains(resp.Diff, "package main") {
		t.Errorf("expected untracked file content in diff, got %q", resp.Diff)
	}
}

func TestGitDiffRejectsUnknownBase(t *testing.T) {
	workspace := t.TempDir()
	initTestGitRepo(t, workspace)
	handler := gitTestHandler(t, workspace)

	r := httptest.NewRequest("GET", "/git/diff?base=index", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unknown base, got %d: %s", w.Code, w.Body.String())
	}
}

func TestGitReadEndpointsLeaveRepositoryUntouched(t *testing.T) {
	workspace := t.TempDir()
	initTestGitRepoWithStates(t, workspace)
	handler := gitTestHandler(t, workspace)

	indexBefore, err := os.ReadFile(filepath.Join(workspace, ".git", "index"))
	if err != nil {
		t.Fatalf("read index: %v", err)
	}

	for _, target := range []string{
		"/git/status",
		"/git/diff",
		"/git/diff?path=untracked.go",
		"/git/diff?base=head",
		"/git/diff-stats?path=unstaged.go",
	} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", target, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 for %s, got %d: %s", target, w.Code, w.Body.String())
		}
	}

	indexAfter, err := os.ReadFile(filepath.Join(workspace, ".git", "index"))
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	// A read-only surface must not stage anything as a side effect.
	if string(indexBefore) != string(indexAfter) {
		t.Error("git index changed: a read-only endpoint wrote to the repository")
	}
}
