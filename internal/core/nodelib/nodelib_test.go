package nodelib

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/core/flow"
)

// TestListIncludesBuiltinSeeds verifies the audio-transcription seed set ships
// builtin and is listed with source=builtin.
func TestListIncludesBuiltinSeeds(t *testing.T) {
	m := NewManager(filepath.Join(t.TempDir(), "state"))
	items, err := m.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) < 5 {
		t.Fatalf("expected at least 5 builtin seeds, got %d", len(items))
	}
	ids := map[string]bool{}
	for _, it := range items {
		ids[it.ID] = true
		if it.Source != SourceBuiltin {
			t.Fatalf("expected builtin source, got %q", it.Source)
		}
		if it.Function.Runtime != flow.FunctionRuntimeJS {
			t.Fatalf("expected js runtime, got %q", it.Function.Runtime)
		}
		if strings.TrimSpace(it.Function.Source) == "" {
			t.Fatalf("expected handler source for %q", it.ID)
		}
	}
	for _, want := range []string{"vad_split", "asr_transcribe", "quality_check", "classify", "llm_rewrite"} {
		if !ids[want] {
			t.Fatalf("expected builtin seed %q in list", want)
		}
	}
}

// TestSaveGetDeleteRoundTrip verifies user entries CRUD: save, list (user
// overrides builtin), get, delete.
func TestSaveGetDeleteRoundTrip(t *testing.T) {
	m := NewManager(filepath.Join(t.TempDir(), "state"))
	e := Entry{
		ID:          "my_dup",
		Name:        "去重",
		Description: "去重文本行",
		Tags:        []string{"dedup", "text"},
		Function: flow.FunctionSpec{
			Runtime: "js",
			Handler: "handle",
			Source:  "function handle(ctx, event) { return { lines: 1 }; }",
		},
	}
	if err := m.Save(e); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := m.Get("my_dup")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Source != SourceUser || got.Function.Source != e.Function.Source {
		t.Fatalf("unexpected entry: %+v", got)
	}
	if got.CreatedAt == "" || got.UpdatedAt == "" {
		t.Fatalf("expected timestamps, got %+v", got)
	}

	items, err := m.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	found := false
	for _, it := range items {
		if it.ID == "my_dup" {
			found = true
			if it.Source != SourceUser {
				t.Fatalf("expected user source, got %q", it.Source)
			}
		}
	}
	if !found {
		t.Fatalf("expected saved entry in list")
	}

	if err := m.Delete("my_dup"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := m.Get("my_dup"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not found after delete, got %v", err)
	}
}

// TestSaveRejectsInvalidEntry verifies id/runtime requirements.
func TestSaveRejectsInvalidEntry(t *testing.T) {
	m := NewManager(filepath.Join(t.TempDir(), "state"))
	if err := m.Save(Entry{}); err == nil || !strings.Contains(err.Error(), "requires id") {
		t.Fatalf("expected id error, got %v", err)
	}
	if err := m.Save(Entry{ID: "x"}); err == nil || !strings.Contains(err.Error(), "function.runtime") {
		t.Fatalf("expected runtime error, got %v", err)
	}
}

// TestUserOverridesBuiltin verifies a user entry with a builtin id overrides
// the read-only builtin (same contract as agent templates).
func TestUserOverridesBuiltin(t *testing.T) {
	m := NewManager(filepath.Join(t.TempDir(), "state"))
	e := Entry{
		ID:       "vad_split",
		Name:     "VAD 切分（自定义）",
		Function: flow.FunctionSpec{Runtime: "js", Handler: "handle", Source: "function handle(ctx,e){return {custom:true};}"},
	}
	if err := m.Save(e); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := m.Get("vad_split")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Source != SourceUser || got.Name != "VAD 切分（自定义）" {
		t.Fatalf("expected user override, got %+v", got)
	}
	// Delete restores the builtin.
	if err := m.Delete("vad_split"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	got, err = m.Get("vad_split")
	if err != nil {
		t.Fatalf("get builtin after delete: %v", err)
	}
	if got.Source != SourceBuiltin {
		t.Fatalf("expected builtin restored, got %+v", got)
	}
}
