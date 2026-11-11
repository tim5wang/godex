package memory

import (
	"strings"
	"testing"
)

func TestSourceDocumentSurvivesBodyTruncationAndCoreProjection(t *testing.T) {
	m := NewManager(t.TempDir())
	_, err := m.Remember(SaveInput{Title: "Reference index", Summary: "A generic index summary", Content: "Source document: docs/README.md\n" + strings.Repeat("reference details ", 200), Type: TypeProject, Tags: []string{"core"}})
	if err != nil {
		t.Fatal(err)
	}
	layers, err := m.PreviewContextLayers("reference")
	if err != nil || len(layers.Core) != 1 || layers.Core[0].SourceDocument != "docs/README.md" {
		t.Fatalf("source pointer lost: %+v %v", layers, err)
	}
	rows, err := m.FindRelevant("reference", 5)
	if err != nil || len(rows) != 1 || rows[0].SourceDocument != "docs/README.md" {
		t.Fatalf("retrieved source pointer lost: %+v %v", rows, err)
	}
}
