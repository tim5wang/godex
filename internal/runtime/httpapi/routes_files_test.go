package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustFileHandler(t *testing.T, workspaceDir string) http.Handler {
	t.Helper()
	cfg := newTestConfig(t)
	cfg.WorkspaceDir = workspaceDir
	manager := newTestManager(t, cfg)
	return NewHandlerWithRuntime(manager, nil, nil, nil, nil, nil, nil, nil)
}

func TestFilesListRoot(t *testing.T) {
	cfg := newTestConfig(t)
	workspace := cfg.WorkspaceDir
	os.WriteFile(filepath.Join(workspace, "README.md"), []byte("hello"), 0644)
	os.MkdirAll(filepath.Join(workspace, "src"), 0755)
	os.WriteFile(filepath.Join(workspace, "src", "main.go"), []byte("package main"), 0644)

	manager := newTestManager(t, cfg)
	handler := NewHandlerWithRuntime(manager, nil, nil, nil, nil, nil, nil, nil)
	r := httptest.NewRequest("GET", "/files/list?dir=.", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Items []struct {
			Name  string `json:"name"`
			IsDir bool   `json:"isDir"`
		} `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	found := map[string]bool{}
	for _, item := range resp.Items {
		found[item.Name] = item.IsDir
	}
	if _, ok := found["README.md"]; !ok {
		t.Errorf("missing README.md, items: %+v", resp.Items)
	}
	if _, ok := found["src"]; !ok {
		t.Errorf("missing src directory, items: %+v", resp.Items)
	}
	if !found["src"] {
		t.Errorf("expected src to be a dir, items: %+v", resp.Items)
	}
}

func TestFilesListSubdir(t *testing.T) {
	cfg := newTestConfig(t)
	workspace := cfg.WorkspaceDir
	os.MkdirAll(filepath.Join(workspace, "sub"), 0755)
	os.WriteFile(filepath.Join(workspace, "sub", "a.txt"), []byte("a"), 0644)
	os.WriteFile(filepath.Join(workspace, "sub", "b.txt"), []byte("b"), 0644)

	manager := newTestManager(t, cfg)
	handler := NewHandlerWithRuntime(manager, nil, nil, nil, nil, nil, nil, nil)
	r := httptest.NewRequest("GET", "/files/list?dir=sub", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Items []struct{ Name string } `json:"items"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(resp.Items))
	}
}

func TestFilesListCustomRoot(t *testing.T) {
	cfg := newTestConfig(t)
	other := t.TempDir()
	os.WriteFile(filepath.Join(other, "custom.txt"), []byte("cs"), 0644)

	manager := newTestManager(t, cfg)
	handler := NewHandlerWithRuntime(manager, nil, nil, nil, nil, nil, nil, nil)
	r := httptest.NewRequest("GET", "/files/list?dir=.&root="+other, nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Items []struct{ Name string } `json:"items"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Items) < 1 {
		t.Fatalf("expected at least 1 item, got %d", len(resp.Items))
	}
	found := false
	for _, item := range resp.Items {
		if item.Name == "custom.txt" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected custom.txt in items: %+v", resp.Items)
	}
}

func TestFilesListNotFound(t *testing.T) {
	cfg := newTestConfig(t)
	manager := newTestManager(t, cfg)
	handler := NewHandlerWithRuntime(manager, nil, nil, nil, nil, nil, nil, nil)
	r := httptest.NewRequest("GET", "/files/list?dir=nosuch", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestFilesRead(t *testing.T) {
	cfg := newTestConfig(t)
	workspace := cfg.WorkspaceDir
	os.WriteFile(filepath.Join(workspace, "hello.txt"), []byte("hello world"), 0644)

	manager := newTestManager(t, cfg)
	handler := NewHandlerWithRuntime(manager, nil, nil, nil, nil, nil, nil, nil)
	r := httptest.NewRequest("GET", "/files/read?path=hello.txt", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["content"] != "hello world" {
		t.Fatalf("expected 'hello world', got %v", resp["content"])
	}
}

func TestFilesReadNotFound(t *testing.T) {
	cfg := newTestConfig(t)
	manager := newTestManager(t, cfg)
	handler := NewHandlerWithRuntime(manager, nil, nil, nil, nil, nil, nil, nil)
	r := httptest.NewRequest("GET", "/files/read?path=nosuch.txt", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestFilesReadEscape(t *testing.T) {
	cfg := newTestConfig(t)
	manager := newTestManager(t, cfg)
	handler := NewHandlerWithRuntime(manager, nil, nil, nil, nil, nil, nil, nil)
	r := httptest.NewRequest("GET", "/files/read?path=../../../etc/passwd", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusForbidden && w.Code != http.StatusNotFound {
		t.Fatalf("expected 403 or 404 for path escape, got %d: %s", w.Code, w.Body.String())
	}
}

func TestFilesWrite(t *testing.T) {
	cfg := newTestConfig(t)
	workspace := cfg.WorkspaceDir
	manager := newTestManager(t, cfg)
	handler := NewHandlerWithRuntime(manager, nil, nil, nil, nil, nil, nil, nil)

	body := `{"path":"newfile.txt","content":"new content"}`
	r := httptest.NewRequest("PUT", "/files/write", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	data, err := os.ReadFile(filepath.Join(workspace, "newfile.txt"))
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	if string(data) != "new content" {
		t.Fatalf("expected 'new content', got %q", string(data))
	}
}

func TestFilesWriteOverwrite(t *testing.T) {
	cfg := newTestConfig(t)
	workspace := cfg.WorkspaceDir
	os.WriteFile(filepath.Join(workspace, "existing.txt"), []byte("old"), 0644)
	manager := newTestManager(t, cfg)
	handler := NewHandlerWithRuntime(manager, nil, nil, nil, nil, nil, nil, nil)

	body := `{"path":"existing.txt","content":"replaced"}`
	r := httptest.NewRequest("PUT", "/files/write", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	data, _ := os.ReadFile(filepath.Join(workspace, "existing.txt"))
	if string(data) != "replaced" {
		t.Fatalf("expected 'replaced', got %q", string(data))
	}
}

func TestFilesDelete(t *testing.T) {
	cfg := newTestConfig(t)
	workspace := cfg.WorkspaceDir
	os.WriteFile(filepath.Join(workspace, "todelete.txt"), []byte("delete me"), 0644)
	manager := newTestManager(t, cfg)
	handler := NewHandlerWithRuntime(manager, nil, nil, nil, nil, nil, nil, nil)

	r := httptest.NewRequest("DELETE", "/files?path=todelete.txt", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", w.Code, w.Body.String())
	}

	if _, err := os.Stat(filepath.Join(workspace, "todelete.txt")); !os.IsNotExist(err) {
		t.Fatal("file should be deleted")
	}
}

func TestFilesDeleteDir(t *testing.T) {
	cfg := newTestConfig(t)
	workspace := cfg.WorkspaceDir
	os.MkdirAll(filepath.Join(workspace, "todelete"), 0755)
	os.WriteFile(filepath.Join(workspace, "todelete", "a.txt"), []byte("a"), 0644)
	manager := newTestManager(t, cfg)
	handler := NewHandlerWithRuntime(manager, nil, nil, nil, nil, nil, nil, nil)

	r := httptest.NewRequest("DELETE", "/files?path=todelete", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", w.Code, w.Body.String())
	}

	if _, err := os.Stat(filepath.Join(workspace, "todelete")); !os.IsNotExist(err) {
		t.Fatal("directory should be deleted")
	}
}

func TestFilesMkdir(t *testing.T) {
	cfg := newTestConfig(t)
	workspace := cfg.WorkspaceDir
	manager := newTestManager(t, cfg)
	handler := NewHandlerWithRuntime(manager, nil, nil, nil, nil, nil, nil, nil)

	body := `{"path":"newdir"}`
	r := httptest.NewRequest("POST", "/files/mkdir", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	info, err := os.Stat(filepath.Join(workspace, "newdir"))
	if err != nil {
		t.Fatalf("stat newdir: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("expected a directory")
	}
}

func TestFilesRename(t *testing.T) {
	cfg := newTestConfig(t)
	workspace := cfg.WorkspaceDir
	os.WriteFile(filepath.Join(workspace, "old.txt"), []byte("old"), 0644)
	manager := newTestManager(t, cfg)
	handler := NewHandlerWithRuntime(manager, nil, nil, nil, nil, nil, nil, nil)

	body := `{"from":"old.txt","to":"new.txt"}`
	r := httptest.NewRequest("POST", "/files/rename", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	if _, err := os.Stat(filepath.Join(workspace, "old.txt")); !os.IsNotExist(err) {
		t.Fatal("old file should be gone")
	}
	data, err := os.ReadFile(filepath.Join(workspace, "new.txt"))
	if err != nil {
		t.Fatalf("read new file: %v", err)
	}
	if string(data) != "old" {
		t.Fatalf("expected 'old', got %q", string(data))
	}
}
