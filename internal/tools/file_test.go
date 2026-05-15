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
	if err := os.WriteFile(target, []byte("package skill"), 0644); err != nil {
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
		if got != "package skill" {
			t.Fatalf("unexpected content for %q: %q", path, got)
		}
	}
}

func TestReadFileToolSupportsOffsetsAndStartLines(t *testing.T) {
	workspace := t.TempDir()
	target := filepath.Join(workspace, "notes.txt")
	content := "line1\nline2\nline3\n"
	if err := os.WriteFile(target, []byte(content), 0644); err != nil {
		t.Fatalf("write target file: %v", err)
	}

	tool := NewReadFileTool(workspace)

	got, err := tool.Execute(context.Background(), map[string]interface{}{
		"path":       "notes.txt",
		"start_line": 2,
		"limit":      100,
	})
	if err != nil {
		t.Fatalf("read with start_line: %v", err)
	}
	if got != "line2\nline3\n" {
		t.Fatalf("unexpected start_line content: %q", got)
	}

	got, err = tool.Execute(context.Background(), map[string]interface{}{
		"path":   "notes.txt",
		"offset": 6,
		"limit":  5,
	})
	if err != nil {
		t.Fatalf("read with offset: %v", err)
	}
	if got != "line2" {
		t.Fatalf("unexpected offset content: %q", got)
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
