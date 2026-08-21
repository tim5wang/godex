package config

import (
	"strings"
	"testing"
)

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

func TestConfigTemplateRendersMemorySection(t *testing.T) {
	rendered, err := renderConfigTemplate(defaultConfigFile())
	if err != nil {
		t.Fatalf("render config template: %v", err)
	}
	tpl := string(rendered)
	for _, want := range []string{"memory:", "strategy: per-turn", "consolidate_after: 10", "session_scope: false"} {
		if !strings.Contains(tpl, want) {
			t.Errorf("expected template to contain %q", want)
		}
	}
}

func TestConfigTemplateRendersControlCredentialAndForwardAllow(t *testing.T) {
	rendered, err := renderConfigTemplate(defaultConfigFile())
	if err != nil {
		t.Fatalf("render config template: %v", err)
	}
	tpl := string(rendered)
	for _, want := range []string{"credential:", "forward_allow:", "GODEX_CONTROL_CREDENTIAL", "GODEX_CONTROL_FORWARD_ALLOW"} {
		if !strings.Contains(tpl, want) {
			t.Errorf("expected template to contain %q", want)
		}
	}
}

func TestConfigTemplateRendersSerpapiKey(t *testing.T) {
	rendered, err := renderConfigTemplate(defaultConfigFile())
	if err != nil {
		t.Fatalf("render config template: %v", err)
	}
	if !strings.Contains(string(rendered), "serpapi:") {
		t.Errorf("expected template to contain serpapi key block")
	}
}

func TestApplyStoredValuesMemoryAndForwardAllow(t *testing.T) {
	file := defaultConfigFile()
	req := UpdateRequest{
		Values: map[string]any{
			"memory.strategy":       "consolidated",
			"memory.session_scope":  true,
			"control.forward_allow": []string{"10.0.0.5:3306", "127.0.0.1:*"},
		},
	}
	if err := applyStoredValues(&file, req); err != nil {
		t.Fatalf("apply stored values: %v", err)
	}
	if file.Memory.Strategy != "consolidated" {
		t.Errorf("expected strategy consolidated, got %q", file.Memory.Strategy)
	}
	if !file.Memory.SessionScope {
		t.Errorf("expected session_scope true")
	}
	if len(file.Control.ForwardAllow) != 2 || file.Control.ForwardAllow[0] != "10.0.0.5:3306" || file.Control.ForwardAllow[1] != "127.0.0.1:*" {
		t.Errorf("expected forward_allow preserved, got %v", file.Control.ForwardAllow)
	}

	// Round-trip through the view maps: stored view must expose both fields.
	view := storedValues(file)
	if got := view["memory.session_scope"]; got != true {
		t.Errorf("expected stored view session_scope true, got %v", got)
	}
	if got := view["control.forward_allow"]; got == nil {
		t.Errorf("expected stored view forward_allow present")
	}
}

// TestControlForwardsRoundTrip verifies control.forwards survives the stored →
// view → resolved config → rendered template pipeline end to end.
func TestControlForwardsRoundTrip(t *testing.T) {
	file := defaultConfigFile()
	req := UpdateRequest{
		Values: map[string]any{
			"control.forwards": []map[string]any{
				{"id": "fw-1", "name": "node-b gateway", "node_id": "node-b", "local_port": 3921, "target": "127.0.0.1:3921"},
			},
		},
	}
	if err := applyStoredValues(&file, req); err != nil {
		t.Fatalf("apply stored values: %v", err)
	}
	if len(file.Control.Forwards) != 1 {
		t.Fatalf("expected 1 forward, got %d: %+v", len(file.Control.Forwards), file.Control.Forwards)
	}
	f := file.Control.Forwards[0]
	if f.ID != "fw-1" || f.NodeID != "node-b" || f.LocalPort != 3921 || f.Target != "127.0.0.1:3921" {
		t.Fatalf("forward mismatch: %+v", f)
	}

	view := storedValues(file)
	if got := view["control.forwards"]; got == nil {
		t.Errorf("expected stored view control.forwards present")
	}

	resolved := resolveConfigFile(file, "home", "proj", "", "", "", "", "", "")
	if len(resolved.Control.Forwards) != 1 {
		t.Fatalf("expected 1 resolved forward, got %d", len(resolved.Control.Forwards))
	}
	if resolved.Control.Forwards[0].LocalPort != 3921 || resolved.Control.Forwards[0].NodeID != "node-b" {
		t.Fatalf("resolved forward mismatch: %+v", resolved.Control.Forwards[0])
	}

	rendered, err := renderConfigTemplate(file)
	if err != nil {
		t.Fatalf("render config template: %v", err)
	}
	if !strings.Contains(string(rendered), "forwards:") {
		t.Errorf("expected template to contain forwards section")
	}
}
