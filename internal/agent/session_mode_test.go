package agent

import (
	"testing"

	"github.com/tim5wang/godex/internal/core/config"
)

func TestApplySessionModeMinimalRestrictsToolsAndTrimsPrompt(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	if !a.toolHandler.IsActive("grep") {
		t.Fatal("expected default-active grep before minimal mode")
	}

	a.ApplySessionMode(SessionModeMinimal)

	for _, want := range []string{"read_file", "write_file", "edit_file", "bash"} {
		if !a.toolHandler.IsActive(want) {
			t.Fatalf("expected %q active in minimal mode", want)
		}
	}
	for _, gone := range []string{"grep", "find", "ls", "glob"} {
		if a.toolHandler.IsActive(gone) {
			t.Fatalf("expected %q inactive in minimal mode", gone)
		}
	}
	if !a.toolHandler.IsActive("tool_exchange") {
		t.Fatal("expected always-active tool_exchange to stay active in minimal mode")
	}

	// Minimal mode trims the heavyweight background sections from the prompt.
	sections, err := a.buildDynamicRuntimePromptSections(config.AgentProfileGeneral)
	if err != nil {
		t.Fatalf("build sections: %v", err)
	}
	keys := make(map[string]bool, len(sections))
	for _, section := range sections {
		keys[section.Key] = true
	}
	for _, skipped := range []string{"skill_catalog", "repo_map", "active_skills"} {
		if keys[skipped] {
			t.Fatalf("expected minimal mode to skip %q section, got keys %v", skipped, keys)
		}
	}
	for _, kept := range []string{"environment", "tool_availability"} {
		if !keys[kept] {
			t.Fatalf("expected minimal mode to keep %q section, got keys %v", kept, keys)
		}
	}
}

func TestApplySessionModeDefaultKeepsFullToolSet(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.ApplySessionMode(SessionModeDefault)
	if !a.toolHandler.IsActive("grep") {
		t.Fatal("expected default mode to keep default-active grep")
	}
	if !a.toolHandler.IsActive("bash") {
		t.Fatal("expected default mode to keep bash")
	}
	if a.sessionModeIsMinimal() {
		t.Fatal("expected default mode to not be minimal")
	}
}

func TestApplySessionModeUnknownModeKeepsDefaults(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.ApplySessionMode("bogus-mode")
	if !a.toolHandler.IsActive("grep") {
		t.Fatal("expected unknown mode to keep default-active tools")
	}
	if a.sessionModeIsMinimal() {
		t.Fatal("expected unknown mode to not be minimal")
	}
}
