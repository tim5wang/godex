package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRetrievedBodyPreservesUTF8Boundary(t *testing.T) {
	for _, prefix := range []string{"", "a", "ab", "ASCII 中文🙂 "} {
		input := prefix + strings.Repeat("中文🙂", 200)
		got := truncateBody(input)
		if !utf8.ValidString(got) || strings.ContainsRune(got, utf8.RuneError) || len(got) > maxRetrievedBody+3 || !strings.HasSuffix(got, "...") {
			t.Fatalf("invalid truncation: %q", got)
		}
		if !strings.HasPrefix(input, strings.TrimSuffix(got, "...")) {
			t.Fatal("truncation rewrote content")
		}
	}
}

func TestManualPolicyKeepsSnapshotsSearchableWithoutAutomaticRecall(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)
	entry, err := m.Remember(SaveInput{Title: "P3 全部完成", Summary: "OCR milestone", Content: "historical OCR implementation", Type: TypeProject})
	if err != nil {
		t.Fatal(err)
	}
	if entry.RecallPolicy != "manual" {
		t.Fatalf("delivery snapshot not manual: %+v", entry)
	}
	for _, rebuild := range []bool{false, true} {
		if rebuild {
			if err := os.Remove(filepath.Join(dir, IndexFileName)); err != nil {
				t.Fatal(err)
			}
		}
		fresh := NewManager(dir)
		rows, err := fresh.Search(SearchOptions{Query: "OCR"})
		if err != nil || len(rows) != 1 {
			t.Fatalf("explicit search lost history: %+v %v", rows, err)
		}
		recalled, err := fresh.FindRelevant("OCR", 5)
		if err != nil || len(recalled) != 0 {
			t.Fatalf("snapshot automatically recalled: %+v %v", recalled, err)
		}
		layers, err := fresh.PreviewContextLayers("OCR")
		if err != nil || len(layers.Core)+len(layers.Relevant) != 0 || layers.Excluded["manual_only"] != 1 {
			t.Fatalf("selection: %+v %v", layers, err)
		}
	}
	policy := "auto"
	_, err = m.Update(UpdateInput{Match: ForgetInput{File: entry.File}, Title: entry.Title, Summary: entry.Summary, Content: "OCR implementation", Type: entry.Type, RecallPolicy: &policy})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := m.FindRelevant("OCR", 5)
	if err != nil || len(rows) != 1 {
		t.Fatalf("explicit auto policy ignored: %+v %v", rows, err)
	}
}
