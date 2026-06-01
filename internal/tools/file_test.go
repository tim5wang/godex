package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadFileToolResolvesWorkspaceRelativeAndDuplicatedBasenamePaths(t *testing.T) {
	workspace := t.TempDir()
	target := filepath.Join(workspace, "skill", "skill.go")
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		t.Fatalf("mkdir target dir: %v", err)
	}
	if err := os.WriteFile(target, []byte("package skill\n"), 0644); err != nil {
		t.Fatalf("write target file: %v", err)
	}

	tool := NewReadFileTool(workspace)

	for _, path := range []string{
		"skill/skill.go",
		filepath.Join(filepath.Base(workspace), "skill", "skill.go"),
		filepath.Join(workspace, filepath.Base(workspace), "skill", "skill.go"),
	} {
		got, err := tool.Execute(context.Background(), map[string]interface{}{"path": path})
		if err != nil {
			t.Fatalf("read %q: %v", path, err)
		}
		// New format: line-numbered output
		if !strings.Contains(got, "package skill") {
			t.Fatalf("expected content for %q, got: %q", path, got)
		}
	}
}

func TestReadFileToolLineNumberedOutput(t *testing.T) {
	workspace := t.TempDir()
	content := "line1\nline2\nline3\n"
	if err := os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte(content), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	tool := NewReadFileTool(workspace)

	// Default: line numbers included
	got, err := tool.Execute(context.Background(), map[string]interface{}{"path": "notes.txt"})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(got, "     1\tline1") {
		t.Fatalf("expected line-numbered output, got: %q", got)
	}
	if !strings.Contains(got, "     3\tline3") {
		t.Fatalf("expected line 3, got: %q", got)
	}

	// Without line numbers
	got, err = tool.Execute(context.Background(), map[string]interface{}{
		"path":                 "notes.txt",
		"include_line_numbers": false,
	})
	if err != nil {
		t.Fatalf("read without line numbers: %v", err)
	}
	if strings.Contains(got, "\tline1") {
		t.Fatalf("expected no line numbers, got: %q", got)
	}
	if !strings.Contains(got, "line1") {
		t.Fatalf("expected content, got: %q", got)
	}
}

func TestReadFileToolSupportsLineOffsetAndLimit(t *testing.T) {
	workspace := t.TempDir()
	content := "line1\nline2\nline3\nline4\nline5\n"
	if err := os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte(content), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	tool := NewReadFileTool(workspace)

	// offset=3 (start from line 3), limit=2 (show 2 lines)
	got, err := tool.Execute(context.Background(), map[string]interface{}{
		"path":   "notes.txt",
		"offset": 3,
		"limit":  2,
	})
	if err != nil {
		t.Fatalf("read with offset: %v", err)
	}
	if !strings.Contains(got, "     3\tline3") || !strings.Contains(got, "     4\tline4") {
		t.Fatalf("expected lines 3-4, got: %q", got)
	}
	if strings.Contains(got, "line5") {
		t.Fatalf("should not contain line5, got: %q", got)
	}

	// Backward compat: start_line
	got, err = tool.Execute(context.Background(), map[string]interface{}{
		"path":       "notes.txt",
		"start_line": 4,
	})
	if err != nil {
		t.Fatalf("read with start_line: %v", err)
	}
	if !strings.Contains(got, "     4\tline4") || !strings.Contains(got, "     5\tline5") {
		t.Fatalf("expected lines 4-5, got: %q", got)
	}
}

func TestWriteFileToolKeepsPathsInsideWorkspace(t *testing.T) {
	workspace := t.TempDir()
	tool := NewWriteFileTool(workspace)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"path":    filepath.Join(filepath.Base(workspace), "notes", "todo.txt"),
		"content": "hello",
	})
	if err != nil {
		t.Fatalf("write duplicated basename path: %v", err)
	}
	if result != "OK" {
		t.Fatalf("expected OK result, got %q", result)
	}

	data, err := os.ReadFile(filepath.Join(workspace, "notes", "todo.txt"))
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("unexpected file content: %q", string(data))
	}

	_, err = tool.Execute(context.Background(), map[string]interface{}{
		"path":    "../outside.txt",
		"content": "nope",
	})
	if err == nil || !strings.Contains(err.Error(), "outside workspace") {
		t.Fatalf("expected outside workspace error, got %v", err)
	}
}

func TestWriteFileToolMissingPathExplainsRequiredArguments(t *testing.T) {
	tool := NewWriteFileTool(t.TempDir())

	_, err := tool.Execute(context.Background(), map[string]interface{}{})
	if err == nil {
		t.Fatal("expected missing path error")
	}
	for _, want := range []string{"missing path argument", "write_file requires", "path", "content"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("expected error to contain %q, got %q", want, err.Error())
		}
	}
}

func TestAttachFileToolReturnsExplicitArtifactPath(t *testing.T) {
	workspace := t.TempDir()
	target := filepath.Join(workspace, ".godex", ".tmp", "report.pdf")
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		t.Fatalf("mkdir target dir: %v", err)
	}
	if err := os.WriteFile(target, []byte("%PDF"), 0644); err != nil {
		t.Fatalf("write target file: %v", err)
	}

	tool := NewAttachFileTool(workspace)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"path": ".godex/.tmp/report.pdf",
	})
	if err != nil {
		t.Fatalf("attach file: %v", err)
	}
	if !strings.Contains(result, `"status":"attached"`) {
		t.Fatalf("unexpected attach result: %s", result)
	}

	handler := NewToolHandler()
	handler.RegisterWithMeta(NewAttachFileTool(workspace), ToolMeta{AlwaysActive: true})
	typedResult, err := handler.HandleResult(context.Background(), "attach_file", map[string]interface{}{
		"path": ".godex/.tmp/report.pdf",
	})
	if err != nil {
		t.Fatalf("handle result attach file: %v", err)
	}
	if len(typedResult.ArtifactPaths) != 1 {
		t.Fatalf("expected one artifact path, got %+v", typedResult.ArtifactPaths)
	}
	if typedResult.ArtifactPaths[0] != filepath.ToSlash(target) {
		t.Fatalf("unexpected artifact path %q", typedResult.ArtifactPaths[0])
	}
}
