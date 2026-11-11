package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestUnrelatedQueryDoesNotRecallAnyMemoryType(t *testing.T) {
	m := NewManager(t.TempDir())
	for _, kind := range []Type{TypeUser, TypeWorkflow, TypeProject, TypeWarning, TypeWorkMethod, TypeWorkFact} {
		if _, err := m.Remember(SaveInput{Title: string(kind), Summary: "unrelated topic", Content: "unrelated knowledge", Type: kind}); err != nil {
			t.Fatal(err)
		}
	}
	for _, search := range []struct {
		name string
		run  func(SearchOptions) ([]StoredMemory, error)
	}{{"sqlite", m.searchWithSidecar}, {"linear", m.searchLinear}} {
		t.Run(search.name, func(t *testing.T) {
			rows, err := search.run(SearchOptions{Query: "qzxvtrplmknb987654321qzxv"})
			if err != nil || len(rows) != 0 {
				t.Fatalf("unrelated query: rows=%+v err=%v", rows, err)
			}
		})
	}
	layers, err := m.PreviewContextLayers("qzxvtrplmknb987654321qzxv")
	if err != nil || len(layers.Core) != 1 || layers.Core[0].Type != TypeUser || len(layers.Relevant) != 0 {
		t.Fatalf("layers=%+v err=%v", layers, err)
	}
}

func TestPromptSectionNeverInjectsCatalog(t *testing.T) {
	m := NewManager(t.TempDir())
	for _, title := range []string{"Unrelated interview snapshot", "Archived obsolete command"} {
		if _, err := m.Remember(SaveInput{Title: title, Summary: title, Content: title, Type: TypeProject}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.Archive(ForgetInput{Title: "Archived obsolete command"}); err != nil {
		t.Fatal(err)
	}
	prompt, err := m.BuildPromptSection()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(prompt, "Unrelated interview snapshot") || strings.Contains(prompt, "Archived obsolete command") {
		t.Fatalf("catalog leaked: %s", prompt)
	}
}

func TestValiditySurvivesReopenAndIndexRebuild(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)
	old, err := m.Remember(SaveInput{Title: "Old OCR", Summary: "OCR with obsolete command", Content: "use tesseract for OCR", Type: TypeWorkflow, Tags: []string{"core"}})
	if err != nil {
		t.Fatal(err)
	}
	current, err := m.Remember(SaveInput{Title: "Current OCR", Summary: "OCR with Vision", Content: "use Vision for OCR", Type: TypeWorkflow})
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.Update(UpdateInput{Match: ForgetInput{File: old.File}, Title: old.Title, Summary: old.Summary, Content: "use tesseract for OCR", Type: old.Type, Tags: old.Tags, SupersededBy: &current.ID})
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.Remember(SaveInput{Title: "Expired OCR warning", Summary: "OCR old warning", Content: "obsolete OCR warning", Type: TypeWarning, Tags: []string{"core"}, ExpiresAt: time.Now().Add(-time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	for _, rebuild := range []bool{false, true} {
		if rebuild {
			if err := os.Remove(filepath.Join(dir, IndexFileName)); err != nil {
				t.Fatal(err)
			}
		}
		fresh := NewManager(dir)
		for _, search := range []struct {
			name string
			run  func(SearchOptions) ([]StoredMemory, error)
		}{{"sqlite", fresh.searchWithSidecar}, {"linear", fresh.searchLinear}} {
			rows, err := search.run(SearchOptions{Query: "OCR"})
			if err != nil || len(rows) != 1 || rows[0].ID != current.ID {
				t.Fatalf("%s rebuild=%v rows=%+v err=%v", search.name, rebuild, rows, err)
			}
		}
		layers, err := fresh.PreviewContextLayers("OCR")
		if err != nil || len(layers.Core) != 0 {
			t.Fatalf("invalid core: %+v %v", layers, err)
		}
		all, err := fresh.Search(SearchOptions{Status: StatusAll})
		if err != nil || len(all) != 3 {
			t.Fatalf("management history lost: %+v %v", all, err)
		}
	}
	// Ordinary edits and repeated remembers must not resurrect archived entries.
	if _, err = m.Archive(ForgetInput{File: current.File}); err != nil {
		t.Fatal(err)
	}
	if _, err = m.Remember(SaveInput{Title: current.Title, Summary: current.Summary, Content: "updated OCR", Type: current.Type}); err != nil {
		t.Fatal(err)
	}
	if _, err = m.Update(UpdateInput{Match: ForgetInput{File: current.File}, Title: current.Title, Summary: current.Summary, Content: "corrected OCR", Type: current.Type}); err != nil {
		t.Fatal(err)
	}
	rows, err := m.Search(SearchOptions{Query: "OCR"})
	if err != nil || len(rows) != 0 {
		t.Fatalf("archived memory resurrected: %+v %v", rows, err)
	}
}

func TestSupersessionRejectsInvalidReplacementAndCycles(t *testing.T) {
	m := NewManager(t.TempDir())
	a, err := m.Remember(SaveInput{Title: "a", Summary: "s", Content: "c", Type: TypeProject})
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.Remember(SaveInput{Title: "b", Summary: "s", Content: "c", Type: TypeProject})
	if err != nil {
		t.Fatal(err)
	}
	update := func(entry *Entry, replacement string) error {
		_, err := m.Update(UpdateInput{Match: ForgetInput{File: entry.File}, Title: entry.Title, Summary: entry.Summary, Content: "c", Type: entry.Type, SupersededBy: &replacement})
		return err
	}
	if update(a, a.ID) == nil || update(a, "missing") == nil {
		t.Fatal("accepted invalid replacement")
	}
	if err := update(a, b.ID); err != nil {
		t.Fatal(err)
	}
	if update(b, a.ID) == nil {
		t.Fatal("accepted supersession cycle")
	}
}
