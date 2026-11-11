package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/core/modelcontext"
	"github.com/tim5wang/godex/internal/platform/tooling"
)

func TestGrepPaginationKeepsSymbolsAndValidJSON(t *testing.T) {
	workspace := t.TempDir()
	var source strings.Builder
	for i := 0; i < 700; i++ {
		fmt.Fprintf(&source, "func Handler%d() {}\n", i)
	}
	if err := os.WriteFile(filepath.Join(workspace, "source.go"), []byte(source.String()), 0644); err != nil {
		t.Fatal(err)
	}
	tool := NewGrepToolWithBackend(NewGoGrepBackend(workspace))
	offset, seen := 0, 0
	for page := 0; page < 10; page++ {
		output, err := tool.Execute(context.Background(), map[string]interface{}{"pattern": "^func ", "path": "source.go", "max_results": 200, "offset": offset})
		if err != nil {
			t.Fatal(err)
		}
		output = modelcontext.CompressToolOutput("grep", "", output)
		var result grepResult
		if err := json.Unmarshal([]byte(output), &result); err != nil {
			t.Fatalf("model received corrupt search JSON: %v", err)
		}
		for _, match := range result.Matches {
			if match.Line != seen+1 || !strings.Contains(match.Content, fmt.Sprintf("Handler%d()", seen)) {
				t.Fatalf("lost/repeated match at %d: %+v", seen, match)
			}
			seen++
		}
		if result.NextOffset == nil {
			break
		}
		if *result.NextOffset <= offset || !result.Truncated {
			t.Fatalf("invalid cursor: %+v", result)
		}
		offset = *result.NextOffset
	}
	if seen != 700 {
		t.Fatalf("expected all 700 symbols across pages, saw %d", seen)
	}
}

func TestGrepLargePageKeepsCursorVisible(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "source.txt"), []byte(strings.Repeat(strings.Repeat("x", 500)+"\n", 500)), 0644); err != nil {
		t.Fatal(err)
	}
	output, err := NewGrepToolWithBackend(NewGoGrepBackend(workspace)).Execute(context.Background(), map[string]interface{}{"pattern": "x", "path": "source.txt", "max_results": 500})
	if err != nil {
		t.Fatal(err)
	}
	var page grepResult
	if json.Unmarshal([]byte(output), &page) != nil || len(output) > 24*1024 || page.NextOffset == nil || !page.Truncated {
		t.Fatalf("oversized or unpageable result: bytes=%d cursor=%v", len(output), page.NextOffset)
	}
}

func TestReadFileRangeReportsContinuationAndSurvivesCompression(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "source.go"), []byte(strings.Repeat("// source line\n", 10)), 0644); err != nil {
		t.Fatal(err)
	}
	result, err := tooling.NewWorkspaceExecutor(workspace).ReadFileLines(tooling.ReadFileLinesOptions{Path: "source.go", Offset: 2, Limit: 3, IncludeLineNumbers: true})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Truncated || result.StartLine != 2 || result.EndLine != 4 || result.NextOffset != 5 || !strings.Contains(result.Content, "offset=5") {
		t.Fatalf("missing continuation metadata: %+v", result)
	}
	if got := modelcontext.CompressToolOutput("read_file", "", result.Content); got != result.Content {
		t.Fatal("source range was compressed a second time")
	}
}

func TestGrepGeneratedDirectoriesAreExplicitlySearchable(t *testing.T) {
	workspace := t.TempDir()
	for _, name := range []string{"src/a.go", "temp/cache/a.go", "desktop/target/b.go"} {
		path := filepath.Join(workspace, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("func Marker() {}\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	backends := map[string]GrepBackend{"go": NewGoGrepBackend(workspace)}
	if rg, err := exec.LookPath("rg"); err == nil {
		backends["rg"] = NewRipGrepBackend(workspace, rg)
	}
	for name, backend := range backends {
		t.Run(name, func(t *testing.T) {
			for _, tc := range []struct {
				path    string
				include bool
				want    int
			}{{".", false, 1}, {".", true, 3}, {"temp", false, 1}} {
				result, err := backend.Search(context.Background(), GrepOptions{Pattern: "Marker", Path: tc.path, Glob: "*.go", MaxResults: 10, IncludeGenerated: tc.include})
				if err != nil || len(result.Matches) != tc.want {
					t.Fatalf("path=%s include=%v: got=%+v err=%v", tc.path, tc.include, result, err)
				}
			}
		})
	}
	properties := tooling.GrepDefinition().ToolSchema().InputSchema["properties"].(map[string]interface{})
	if properties["offset"] == nil || properties["include_generated"] == nil {
		t.Fatal("pagination and scope controls must be declared in the model-visible schema properties")
	}
}

func TestBroadGrepIgnoresBinaryAndDirectoryLinks(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "source.go"), []byte("func Marker() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "binary"), []byte("\x00Marker\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(".", filepath.Join(workspace, "loop.go")); err != nil {
		t.Fatal(err)
	}
	result, err := NewGoGrepBackend(workspace).Search(context.Background(), GrepOptions{Pattern: "Marker", MaxResults: 100})
	if err != nil || len(result.Matches) != 1 || result.Matches[0].File != "source.go" {
		t.Fatalf("broad search was corrupted by non-source entries: %+v, %v", result, err)
	}
}

func TestDirectoryGrepPagesUseStableFileOrder(t *testing.T) {
	workspace := t.TempDir()
	for _, name := range []string{"z.go", "a.go"} {
		if err := os.WriteFile(filepath.Join(workspace, name), []byte("Marker\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	tool := NewGrepToolWithBackend(NewGoGrepBackend(workspace))
	for offset, want := range []string{"a.go", "z.go"} {
		output, err := tool.Execute(context.Background(), map[string]interface{}{"pattern": "Marker", "max_results": 1, "offset": offset})
		if err != nil {
			t.Fatal(err)
		}
		var page grepResult
		if json.Unmarshal([]byte(output), &page) != nil || len(page.Matches) != 1 || page.Matches[0].File != want {
			t.Fatalf("unstable page %d: %s", offset, output)
		}
	}
}

// Optional local benchmark for a real workspace; no repository paths or
// private source data are embedded in the test suite.
func BenchmarkWorkspaceGrep(b *testing.B) {
	workspace := os.Getenv("GODEX_BENCH_WORKSPACE")
	if workspace == "" {
		b.Skip("set GODEX_BENCH_WORKSPACE to benchmark a real checkout")
	}
	for _, include := range []bool{false, true} {
		b.Run(fmt.Sprintf("include_generated_%v", include), func(b *testing.B) {
			backend := NewGoGrepBackend(workspace)
			options := GrepOptions{Pattern: "prepareParallelReads|preparedToolOutcome|CanParallel|MaxParallel", Glob: "*.go", MaxResults: 61, IncludeGenerated: include}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := backend.Search(context.Background(), options); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
