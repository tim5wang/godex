package templates

import (
	"path/filepath"
	"strings"
	"testing"
)

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	return NewManager(filepath.Join(t.TempDir(), "state"), filepath.Join(t.TempDir(), "skills"))
}

func TestListIncludesAllBuiltins(t *testing.T) {
	m := newTestManager(t)
	list, err := m.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	got := map[string]AgentTemplate{}
	for _, tpl := range list {
		got[tpl.ID] = tpl
	}
	for _, id := range []string{BuiltinDefault, BuiltinMinimal, BuiltinGeneralAssistant, BuiltinCoder, BuiltinResearcher, BuiltinReviewer, BuiltinPlanner, BuiltinPJM} {
		tpl, ok := got[id]
		if !ok {
			t.Fatalf("expected builtin template %q in list", id)
		}
		if tpl.Source != SourceBuiltin {
			t.Fatalf("template %q source = %q, want builtin", id, tpl.Source)
		}
		if tpl.Name == "" {
			t.Fatalf("template %q has empty name", id)
		}
	}
}

func TestBuiltinTemplateSemantics(t *testing.T) {
	m := newTestManager(t)

	def, err := m.Get(BuiltinDefault)
	if err != nil {
		t.Fatalf("Get default: %v", err)
	}
	if len(def.Bundles) != 0 || len(def.Tools) != 0 || def.TrimHeavySections {
		t.Fatal("default template must keep stock behavior (no tool preset, no trims)")
	}

	minimal, err := m.Get(BuiltinMinimal)
	if err != nil {
		t.Fatalf("Get minimal: %v", err)
	}
	if len(minimal.Tools) != 5 || !minimal.TrimHeavySections {
		t.Fatal("minimal template must pin the four core tools + tool_exchange and trim heavy sections")
	}

	researcher, err := m.Get(BuiltinResearcher)
	if err != nil {
		t.Fatalf("Get researcher: %v", err)
	}
	if researcher.WriteEnabled {
		t.Fatal("researcher template must be read-only")
	}
	if len(researcher.Bundles) == 0 || researcher.Persona == "" {
		t.Fatal("researcher template must pin read-only bundles and a persona")
	}
}

func TestSaveGetDeleteUserTemplate(t *testing.T) {
	m := newTestManager(t)
	tpl := AgentTemplate{
		ID:      "My Dev",
		Name:    "My Dev",
		Bundles: []string{"core_code", "lsp"},
		Skills:  []string{"missing-skill"},
	}
	if err := m.Save(tpl); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// ID is sanitized on save.
	got, err := m.Get("my-dev")
	if err != nil {
		t.Fatalf("Get sanitized id: %v", err)
	}
	if got.Source != SourceUser {
		t.Fatalf("source = %q, want user", got.Source)
	}

	// Resolve drops uninstalled skill references with warnings.
	resolved, warnings, err := m.Resolve("my-dev")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(resolved.Skills) != 0 {
		t.Fatalf("expected missing skill to be dropped, got %v", resolved.Skills)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "missing-skill") {
		t.Fatalf("expected one warning about missing-skill, got %v", warnings)
	}

	if err := m.Delete("my-dev"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := m.Get("my-dev"); err == nil {
		t.Fatal("expected deleted template to be gone")
	}
}

func TestSaveRejectsBuiltinCollisionAndDeleteRejectsReadonly(t *testing.T) {
	m := newTestManager(t)
	if err := m.Save(AgentTemplate{ID: BuiltinDefault, Name: "shadow"}); err == nil {
		t.Fatal("expected builtin ID collision to be rejected")
	}
	if err := m.Delete(BuiltinDefault); err == nil {
		t.Fatal("expected builtin delete to be rejected")
	}
}

func TestGetUnknownTemplate(t *testing.T) {
	m := newTestManager(t)
	if _, err := m.Get("no-such-template"); err == nil {
		t.Fatal("expected error for unknown template")
	}
}
