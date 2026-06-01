package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEditFileMultiEditAppliesAllEdits(t *testing.T) {
	workspace := t.TempDir()
	target := filepath.Join(workspace, "main.go")
	content := `package main

func main() {
	println("hello")
	println("world")
}
`
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(target, []byte(content), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	tool := NewEditFileTool(workspace)

	// Multi-edit test
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"path": "main.go",
		"edits": []interface{}{
			map[string]interface{}{"old_text": "hello", "new_text": "hi"},
			map[string]interface{}{"old_text": "world", "new_text": "earth"},
		},
	})
	if err != nil {
		t.Fatalf("multi-edit failed: %v", err)
	}
	if !strings.Contains(result, "Applied 2 edit(s)") {
		t.Fatalf("unexpected result: %s", result)
	}

	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(data), "println(\"hi\")") {
		t.Fatalf("first edit not applied: %s", string(data))
	}
	if !strings.Contains(string(data), "println(\"earth\")") {
		t.Fatalf("second edit not applied: %s", string(data))
	}
}

func TestEditFileRejectsNonUniqueOldText(t *testing.T) {
	workspace := t.TempDir()
	target := filepath.Join(workspace, "dup.txt")
	content := "a\nb\na\nb\n"
	if err := os.WriteFile(target, []byte(content), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	tool := NewEditFileTool(workspace)

	_, err := tool.Execute(context.Background(), map[string]interface{}{
		"path":     "dup.txt",
		"old_text": "a",
		"new_text": "x",
	})
	if err == nil {
		t.Fatal("expected error for non-unique old_text")
	}
	if !strings.Contains(err.Error(), "found 2 times") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEditFileRejectsOverlappingEdits(t *testing.T) {
	workspace := t.TempDir()
	target := filepath.Join(workspace, "overlap.txt")
	content := "abcdefghij"
	if err := os.WriteFile(target, []byte(content), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	tool := NewEditFileTool(workspace)

	_, err := tool.Execute(context.Background(), map[string]interface{}{
		"path": "overlap.txt",
		"edits": []interface{}{
			map[string]interface{}{"old_text": "abcde", "new_text": "12345"},
			map[string]interface{}{"old_text": "defgh", "new_text": "67890"},
		},
	})
	if err == nil {
		t.Fatal("expected error for overlapping edits")
	}
	if !strings.Contains(err.Error(), "overlap") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEditFileOldTextNotFoundSuggestsSimilar(t *testing.T) {
	workspace := t.TempDir()
	target := filepath.Join(workspace, "main.go")
	content := "func helloWorld() {\n\tprintln(\"hello\")\n}\n"
	if err := os.WriteFile(target, []byte(content), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	tool := NewEditFileTool(workspace)

	_, err := tool.Execute(context.Background(), map[string]interface{}{
		"path":     "main.go",
		"old_text": "func helloWorldX()",
		"new_text": "func hi()",
	})
	if err == nil {
		t.Fatal("expected error for not-found text")
	}
	if !strings.Contains(err.Error(), "old_text not found") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEditFileBackwardCompatibleSingleEdit(t *testing.T) {
	workspace := t.TempDir()
	target := filepath.Join(workspace, "file.txt")
	content := "hello world"
	if err := os.WriteFile(target, []byte(content), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	tool := NewEditFileTool(workspace)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"path":     "file.txt",
		"old_text": "hello",
		"new_text": "hi",
	})
	if err != nil {
		t.Fatalf("legacy single edit failed: %v", err)
	}
	if result != "OK" {
		t.Fatalf("unexpected result: %s", result)
	}

	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "hi world" {
		t.Fatalf("unexpected content: %s", string(data))
	}
}

func TestLsToolListsDirectory(t *testing.T) {
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, "src", "pkg"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "README.md"), []byte("readme"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	tool := NewLsTool(workspace)

	resultStr, err := tool.Execute(context.Background(), map[string]interface{}{"path": "."})
	if err != nil {
		t.Fatalf("ls failed: %v", err)
	}
	if !strings.Contains(resultStr, "README.md") {
		t.Fatalf("expected README.md in ls result: %s", resultStr)
	}
	if !strings.Contains(resultStr, "src") {
		t.Fatalf("expected src dir in ls result: %s", resultStr)
	}
	if !strings.Contains(resultStr, `"is_dir":true`) {
		t.Fatalf("expected is_dir in result: %s", resultStr)
	}
}

func TestFindToolFindsFiles(t *testing.T) {
	workspace := t.TempDir()
	dirs := []string{"src/pkg", "src/cmd", "internal"}
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(workspace, d), 0755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	files := []string{"src/pkg/main.go", "src/cmd/main.go", "internal/helper.go", "README.md"}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(workspace, f), []byte("content"), 0644); err != nil {
			t.Fatalf("write %s: %v", f, err)
		}
	}

	tool := NewFindTool(workspace)

	resultStr, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern": "*.go",
	})
	if err != nil {
		t.Fatalf("find failed: %v", err)
	}
	if !strings.Contains(resultStr, "src/pkg/main.go") || !strings.Contains(resultStr, "src/cmd/main.go") || !strings.Contains(resultStr, "internal/helper.go") {
		t.Fatalf("expected .go files, got: %s", resultStr)
	}
	if strings.Contains(resultStr, "README.md") {
		t.Fatalf("should not contain README.md: %s", resultStr)
	}
}

func TestGrepToolSearchesContent(t *testing.T) {
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, "src"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	content1 := "func hello() {\n\tprintln(\"hello\")\n}\n"
	content2 := "func world() {\n\tprintln(\"world\")\n}\n"
	if err := os.WriteFile(filepath.Join(workspace, "src", "a.go"), []byte(content1), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "src", "b.go"), []byte(content2), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	tool := NewGrepTool(workspace)

	resultStr, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern": "func hello",
	})
	if err != nil {
		t.Fatalf("grep failed: %v", err)
	}
	if !strings.Contains(resultStr, "func hello") {
		t.Fatalf("expected to find 'func hello' in: %s", resultStr)
	}
	if !strings.Contains(resultStr, `"total_matches":1`) {
		t.Fatalf("expected 1 match: %s", resultStr)
	}
}
