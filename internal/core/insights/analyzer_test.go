package insights

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAnalyzerBuildsStructuredReport(t *testing.T) {
	base := t.TempDir()
	transcriptsDir := filepath.Join(base, "transcripts")
	tempDir := filepath.Join(base, "tmp")
	memoryDir := filepath.Join(base, "memory")
	if err := os.MkdirAll(transcriptsDir, 0755); err != nil {
		t.Fatalf("mkdir transcripts: %v", err)
	}
	if err := os.MkdirAll(tempDir, 0755); err != nil {
		t.Fatalf("mkdir temp: %v", err)
	}
	if err := os.MkdirAll(memoryDir, 0755); err != nil {
		t.Fatalf("mkdir memory: %v", err)
	}

	candidates := []candidate{{
		Fingerprint: "abc",
		Title:       "Workflow: Validate Go changes",
		Summary:     "Run go test ./... and go test -race ./... after runtime changes.",
		Content:     "Use both Go validation commands after runtime changes.",
		Type:        "workflow",
		Source:      "turn-end-extractor",
	}}
	candidateData, err := json.Marshal(candidates)
	if err != nil {
		t.Fatalf("marshal candidates: %v", err)
	}
	if err := os.WriteFile(filepath.Join(memoryDir, candidatesFileName), candidateData, 0644); err != nil {
		t.Fatalf("write candidates: %v", err)
	}

	catalog := ToolCatalog{
		ActiveBundles: []string{"core_code", "planning"},
	}

	analyzer := NewAnalyzer(transcriptsDir, tempDir, memoryDir)
	report, err := analyzer.Analyze(Input{
		CurrentMessages: []Message{
			{Text: "请帮我 review 这个 runtime 改动。"},
			{Text: "Run go test ./... and go test -race ./... after runtime changes. Also watch for context deadline exceeded errors.", ToolNames: []string{"background"}},
		},
		ActiveSkills: []string{"stock-fetcher"},
		ToolCatalog:  catalog,
		Todos:        []WorkItem{{Status: "pending"}, {Status: "pending"}, {Status: "pending"}, {Status: "pending"}, {Status: "pending"}},
		Tasks:        []WorkItem{{Status: "pending"}},
	})
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}

	markdown := report.Markdown()
	for _, want := range []string{
		"## AGENT.md Additions",
		"## Skill Candidates",
		"## Bundle Recommendations",
		"## Frictions",
		"go-validation",
		"Background bundle",
		"Model/API timeouts",
	} {
		if !strings.Contains(strings.ToLower(markdown), strings.ToLower(want)) {
			t.Fatalf("expected markdown to contain %q, got %q", want, markdown)
		}
	}
}
