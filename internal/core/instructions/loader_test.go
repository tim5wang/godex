package instructions

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCanonicalInstructionsOverrideLegacyWithoutDuplicating(t *testing.T) {
	workspace, state := t.TempDir(), t.TempDir()
	write := func(base, name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(base, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write(workspace, "AGENT.md", "legacy project")
	write(workspace, "AGENTS.md", "canonical project")
	write(state, "AGENT.local.md", "legacy local")
	write(state, "AGENTS.local.md", "canonical local")
	if err := os.MkdirAll(filepath.Join(state, "rules"), 0755); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(state, "rules"), "coding.md", "local rule")
	sources, err := NewLoader().Load(workspace, state)
	if err != nil || len(sources) != 3 {
		t.Fatalf("sources=%+v err=%v", sources, err)
	}
	for i, want := range []string{"canonical project", "local rule", "canonical local"} {
		if sources[i].Content != want {
			t.Fatalf("source order/content: %+v", sources)
		}
	}
	if err := os.Remove(filepath.Join(workspace, "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(state, "AGENTS.local.md")); err != nil {
		t.Fatal(err)
	}
	sources, err = NewLoader().Load(workspace, state)
	if err != nil || len(sources) != 3 || sources[0].Content != "legacy project" || sources[2].Content != "legacy local" {
		t.Fatalf("legacy compatibility: %+v %v", sources, err)
	}
}
