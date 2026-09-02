package templates

import "testing"

func TestNormalizeEngineID(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", EngineDefault},
		{"godex", EngineDefault},
		{"GODEX", EngineDefault},
		{"  godex  ", EngineDefault},
		{"acp:codex", "acp:codex"},
		{"ACP:CODE X", "acp:code x"},
		{"acp:pi", "acp:pi"},
		{"some-engine", "some-engine"},
	}
	for _, c := range cases {
		if got := NormalizeEngineID(c.in); got != c.want {
			t.Errorf("NormalizeEngineID(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestEngineFieldRoundTripsThroughManager verifies the Engine field survives
// a save → get cycle so a template with a pinned kernel keeps it across
// restarts (the runtime chain reads it from the persisted template).
func TestEngineFieldRoundTripsThroughManager(t *testing.T) {
	m := newTestManager(t)
	tpl := AgentTemplate{
		ID:     "ext-codex",
		Name:   "External Codex",
		Engine: "acp:codex",
	}
	if err := m.Save(tpl); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := m.Get("ext-codex")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Engine != "acp:codex" {
		t.Fatalf("Engine after round-trip = %q, want acp:codex", got.Engine)
	}
}

// TestEnginePinnedTemplateIsNotZeroish verifies that a template whose only
// preset is a non-default engine is not treated as "no preset at all" (the
// runtime chain must still apply it so the kernel switches).
func TestEnginePinnedTemplateIsNotZeroish(t *testing.T) {
	tpl := AgentTemplate{ID: "only-engine", Engine: "acp:pi"}
	if tpl.IsZeroish() {
		t.Fatal("expected a template pinned to a non-default engine to NOT be zeroish")
	}
	// Default/empty engine keeps zeroish semantics.
	if (AgentTemplate{ID: "plain", Name: "Plain"}).IsZeroish() == false {
		t.Fatal("expected a plain template without presets to stay zeroish")
	}
}
