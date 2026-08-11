package config

import "testing"

func TestNormalizeMemoryStrategyKind(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", "per-turn"},
		{"per-turn", "per-turn"},
		{"per_turn", "per-turn"},
		{"PER-TURN", "per-turn"},
		{"agent-only", "agent-only"},
		{"agent_only", "agent-only"},
		{"AgentOnly", "agent-only"},
		{"consolidated", "consolidated"},
		{"consolidation", "consolidated"},
		{"bogus", "per-turn"},
		{"  ", "per-turn"},
	}
	for _, tc := range cases {
		if got := normalizeMemoryStrategyKind(tc.in); got != tc.want {
			t.Errorf("normalizeMemoryStrategyKind(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestDefaultConfigFileHasPerTurnMemoryStrategy(t *testing.T) {
	file := defaultConfigFile()
	if file.Memory.Strategy != "per-turn" {
		t.Errorf("expected default memory strategy per-turn, got %q", file.Memory.Strategy)
	}
	if file.Memory.ConsolidateAfter != 10 {
		t.Errorf("expected default consolidate_after 10, got %d", file.Memory.ConsolidateAfter)
	}
}

func TestResolveConfigFileMapsMemorySection(t *testing.T) {
	cfg := resolveConfigFile(
		ConfigFile{Memory: MemorySection{Strategy: "consolidated", ConsolidateAfter: 25}},
		"", "", "", "", "", "", "", "",
	)
	if cfg.Memory.Strategy != "consolidated" {
		t.Errorf("expected resolved strategy consolidated, got %q", cfg.Memory.Strategy)
	}
	if cfg.Memory.ConsolidateAfter != 25 {
		t.Errorf("expected resolved consolidate_after 25, got %d", cfg.Memory.ConsolidateAfter)
	}
}

func TestResolveConfigFileDefaultsMemoryStrategy(t *testing.T) {
	cfg := resolveConfigFile(ConfigFile{}, "", "", "", "", "", "", "", "")
	if cfg.Memory.Strategy != "per-turn" {
		t.Errorf("expected empty memory section to resolve per-turn, got %q", cfg.Memory.Strategy)
	}
	if cfg.Memory.ConsolidateAfter != 0 {
		t.Errorf("expected empty consolidate_after to stay 0 (default applied at wiring), got %d", cfg.Memory.ConsolidateAfter)
	}
}
