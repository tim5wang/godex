package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func runTestGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v: %s", args, err, string(out))
	}
}

// initTestGitRepo creates a git repo in dir with one committed file, then
// modifies it (uncommitted) so `git diff` has output.
func initTestGitRepo(t *testing.T, dir string) {
	t.Helper()
	runTestGit(t, dir, "init", "-q", "-b", "main")
	runTestGit(t, dir, "config", "user.email", "test@godex.local")
	runTestGit(t, dir, "config", "user.name", "GoDex Test")
	os.WriteFile(filepath.Join(dir, "app.go"), []byte("package main\n\nfunc main() {}\n"), 0644)
	runTestGit(t, dir, "add", "app.go")
	runTestGit(t, dir, "commit", "-q", "-m", "init")

	// Uncommitted modification → appears in git diff.
	os.WriteFile(filepath.Join(dir, "app.go"), []byte("package main\n\nfunc main() { println(\"hi\") }\n"), 0644)
}

func TestGitDiffNonGitDir(t *testing.T) {
	cfg := newTestConfig(t) // temp dir without git
	manager := newTestManager(t, cfg)
	handler := NewHandlerWithRuntime(manager, nil, nil, nil, nil, nil, nil, nil)

	r := httptest.NewRequest("GET", "/git/diff", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp gitDiffResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Repo {
		t.Fatalf("expected repo=false for non-git dir, got %+v", resp)
	}
}

func TestGitDiffRepoWithChanges(t *testing.T) {
	workspace := t.TempDir()
	initTestGitRepo(t, workspace)

	cfg := newTestConfig(t)
	cfg.WorkspaceDir = workspace
	manager := newTestManager(t, cfg)
	handler := NewHandlerWithRuntime(manager, nil, nil, nil, nil, nil, nil, nil)

	r := httptest.NewRequest("GET", "/git/diff", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp gitDiffResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Repo {
		t.Fatalf("expected repo=true, got %+v", resp)
	}
	if resp.Diff == "" {
		t.Fatal("expected non-empty diff for modified repo")
	}
	// Unified diff markers present.
	if !contains(resp.Diff, "diff --git") || !contains(resp.Diff, "+") {
		t.Errorf("unexpected diff content: %q", resp.Diff)
	}
}

func TestGitDiffRepoCustomRoot(t *testing.T) {
	workspace := t.TempDir()
	initTestGitRepo(t, workspace)

	cfg := newTestConfig(t) // default workspace ≠ repo dir
	manager := newTestManager(t, cfg)
	handler := NewHandlerWithRuntime(manager, nil, nil, nil, nil, nil, nil, nil)

	r := httptest.NewRequest("GET", "/git/diff?root="+workspace, nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp gitDiffResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Repo {
		t.Fatalf("expected repo=true for custom root, got %+v", resp)
	}
}

func TestGitDiffSingleFile(t *testing.T) {
	workspace := t.TempDir()
	initTestGitRepo(t, workspace)

	cfg := newTestConfig(t)
	cfg.WorkspaceDir = workspace
	manager := newTestManager(t, cfg)
	handler := NewHandlerWithRuntime(manager, nil, nil, nil, nil, nil, nil, nil)

	r := httptest.NewRequest("GET", "/git/diff?path=app.go", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp gitDiffResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Repo {
		t.Fatalf("expected repo=true, got %+v", resp)
	}
	if !contains(resp.Diff, "app.go") {
		t.Errorf("expected diff to reference app.go, got %q", resp.Diff)
	}
}

func TestGitDiffRejectsTraversal(t *testing.T) {
	workspace := t.TempDir()
	initTestGitRepo(t, workspace)

	cfg := newTestConfig(t)
	cfg.WorkspaceDir = workspace
	manager := newTestManager(t, cfg)
	handler := NewHandlerWithRuntime(manager, nil, nil, nil, nil, nil, nil, nil)

	for _, bad := range []string{"../outside.go", "/etc/passwd", "a/../../b"} {
		r := httptest.NewRequest("GET", "/git/diff?path="+bad, nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400 for path %q, got %d: %s", bad, w.Code, w.Body.String())
		}
	}
}

func contains(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}
