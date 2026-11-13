package memory

import (
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestSearchProjectionTracksOtherManagerAndMarkdownEdits(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)
	e, err := m.Remember(SaveInput{Title: "gopls references", Summary: "gopls references", Content: "gopls references original", Type: TypeWorkFact})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := m.FindRelevant("gopls references", 5); err != nil || len(got) != 1 {
		t.Fatal(got, err)
	}
	other := NewManager(dir)
	if _, err = other.Update(UpdateInput{Match: ForgetInput{File: e.File}, Type: e.Type, Title: e.Title, Summary: e.Summary, Content: "gopls references revised by another conversation"}); err != nil {
		t.Fatal(err)
	}
	got, err := m.FindRelevant("gopls references", 5)
	if err != nil || len(got) != 1 || !strings.Contains(got[0].Content, "revised by another conversation") {
		t.Fatal("stale cross-manager projection", got, err)
	}
	path := filepath.Join(dir, e.File)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, []byte(strings.Replace(string(raw), "revised by another conversation", "externally edited Markdown body with extra text", 1)), 0644); err != nil {
		t.Fatal(err)
	}
	got, err = m.FindRelevant("gopls references", 5)
	if err != nil || len(got) != 1 || !strings.Contains(got[0].Content, "externally edited") {
		t.Fatal("stale external body", got, err)
	}
	if _, err = other.Archive(ForgetInput{File: e.File}); err != nil {
		t.Fatal(err)
	}
	if got, err = m.FindRelevant("gopls references", 5); err != nil || len(got) != 0 {
		t.Fatal("archived memory survived cache", got, err)
	}
}

func TestSearchProjectionRechecksOriginAndDoesNotExposeMutableMetadata(t *testing.T) {
	m := NewManager(t.TempDir())
	var valid atomic.Bool
	valid.Store(true)
	m.SetOriginValidation(func(Origin) bool { return valid.Load() }, nil)
	_, err := m.Remember(SaveInput{Title: "Vision OCR", Summary: "Vision OCR", Content: "Vision OCR", Type: TypeWorkFact, Tags: []string{"ocr"}, Origin: &Origin{ContextID: "desktop", FactID: "source", FactVersion: 1}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := m.FindRelevant("Vision OCR", 5)
	if err != nil || len(got) != 1 {
		t.Fatal(got, err)
	}
	got[0].Tags[0] = "corrupted"
	got[0].Origin.FactVersion = 999
	got, err = m.FindRelevant("Vision OCR", 5)
	if err != nil || len(got) != 1 || got[0].Tags[0] != "ocr" || got[0].Origin.FactVersion != 1 {
		t.Fatal("cached metadata mutated by caller", got, err)
	}
	if filtered, err := m.Search(SearchOptions{Query: "Vision OCR", Tag: " ", Source: " "}); err != nil || len(filtered) != 1 {
		t.Fatal("blank metadata filters changed search semantics", filtered, err)
	}
	valid.Store(false)
	if got, err = m.FindRelevant("Vision OCR", 5); err != nil || len(got) != 0 {
		t.Fatal("invalid source passed warm projection", got, err)
	}
}
