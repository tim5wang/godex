package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestGlobToolMatchesWorkspaceFiles(t *testing.T) {
	workspace := t.TempDir()
	for _, path := range []string{
		"a.go",
		"nested/b.txt",
		"nested/deeper/c.go",
	} {
		absolute := filepath.Join(workspace, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(absolute), 0755); err != nil {
			t.Fatalf("mkdir %s: %v", absolute, err)
		}
		if err := os.WriteFile(absolute, []byte(path), 0644); err != nil {
			t.Fatalf("write %s: %v", absolute, err)
		}
	}

	tool := NewGlobTool(workspace, 10)
	output, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern": "**/*.go",
	})
	if err != nil {
		t.Fatalf("execute glob: %v", err)
	}

	var parsed globResult
	if err := json.Unmarshal([]byte(output), &parsed); err != nil {
		t.Fatalf("parse result: %v", err)
	}
	if len(parsed.Matches) != 2 || parsed.Matches[0] != "a.go" || parsed.Matches[1] != "nested/deeper/c.go" {
		t.Fatalf("unexpected matches: %#v", parsed.Matches)
	}
	if parsed.Truncated {
		t.Fatalf("did not expect truncation")
	}
}

func TestGlobToolRejectsWorkspaceEscape(t *testing.T) {
	workspace := t.TempDir()
	tool := NewGlobTool(workspace, 10)
	if _, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern": "*.go",
		"root":    "../",
	}); err == nil {
		t.Fatalf("expected workspace escape to fail")
	}
}
