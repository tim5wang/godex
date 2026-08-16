package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// A recursive glob must skip derived/generated directories (node_modules, .git,
// build caches) so it cannot hang for minutes walking tens of millions of
// files — but an explicit root inside one of them is still honored.
func TestGlobToolSkipsDerivedDirs(t *testing.T) {
	workspace := t.TempDir()
	for _, path := range []string{
		"internal/agent.go",
		"node_modules/pkg/index.js",
		".git/config",
		"dist/bundle.js",
		".godex-build-cache/x/term.go",
	} {
		absolute := filepath.Join(workspace, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(absolute), 0755); err != nil {
			t.Fatalf("mkdir %s: %v", absolute, err)
		}
		if err := os.WriteFile(absolute, []byte(path), 0644); err != nil {
			t.Fatalf("write %s: %v", absolute, err)
		}
	}

	tool := NewGlobTool(workspace, 100)
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
	if len(parsed.Matches) != 1 || parsed.Matches[0] != "internal/agent.go" {
		t.Fatalf("expected only the source file, got %#v", parsed.Matches)
	}

	// An explicit root inside a skipped dir is still scanned.
	output, err = tool.Execute(context.Background(), map[string]interface{}{
		"pattern": "**/*.go",
		"root":    ".godex-build-cache",
	})
	if err != nil {
		t.Fatalf("execute glob under explicit root: %v", err)
	}
	if err := json.Unmarshal([]byte(output), &parsed); err != nil {
		t.Fatalf("parse result: %v", err)
	}
	if len(parsed.Matches) != 1 || !strings.HasSuffix(parsed.Matches[0], "x/term.go") {
		t.Fatalf("expected explicit root to be scanned, got %#v", parsed.Matches)
	}
}

// The walk honors the context so the runner timeout actually stops the tool
// instead of leaving an uncancellable goroutine scanning the tree.
func TestGlobToolCancellable(t *testing.T) {
	workspace := t.TempDir()
	for i := 0; i < 200; i++ {
		absolute := filepath.Join(workspace, "dir", fmt.Sprintf("f%d.go", i))
		if err := os.MkdirAll(filepath.Dir(absolute), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(absolute, []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	tool := NewGlobTool(workspace, 100)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled
	if _, err := tool.Execute(ctx, map[string]interface{}{
		"pattern": "**/*.go",
	}); err == nil {
		t.Fatal("expected cancelled context to abort glob")
	}
}

// max_results stops the walk early instead of scanning the whole tree first.
func TestGlobToolStopsAtMaxResults(t *testing.T) {
	workspace := t.TempDir()
	for i := 0; i < 50; i++ {
		absolute := filepath.Join(workspace, fmt.Sprintf("f%d.go", i))
		if err := os.WriteFile(absolute, []byte("x"), 0644); err != nil {
			t.Fatal(err)
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
	if len(parsed.Matches) != 10 || !parsed.Truncated {
		t.Fatalf("expected 10 matches truncated, got %d truncated=%v", len(parsed.Matches), parsed.Truncated)
	}
}
