package agent

import (
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/core/templates"
)

func researcherLikeTemplate() templates.AgentTemplate {
	return templates.AgentTemplate{
		ID:                "test-researcher",
		Name:              "Test Researcher",
		Bundles:           []string{"web", "browser"},
		Persona:           "You are a thorough test researcher.",
		TrimHeavySections: true,
		Skills:            []string{"alpha", "beta"},
	}
}

func TestApplyTemplateActivatesBundleToolsOnly(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()

	a.ApplyTemplate(researcherLikeTemplate())

	if got := a.TemplateID(); got != "test-researcher" {
		t.Fatalf("TemplateID = %q, want test-researcher", got)
	}
	// Bundle tools are active.
	for _, name := range []string{"web_search", "web_fetch"} {
		if !a.toolHandler.IsActive(name) {
			t.Fatalf("expected %s active from web bundle", name)
		}
	}
	// Default-active core tools are NOT part of the template's bundle set.
	if a.toolHandler.IsActive("bash") {
		t.Fatal("expected bash inactive for a web/browser-only template")
	}
	// Always-active tools survive SetActiveTools.
	if !a.toolHandler.IsActive("memory") {
		t.Fatal("expected always-active tools to survive template tool preset")
	}
}

func TestApplyTemplateToolsAllowlistWins(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()

	tpl := templates.AgentTemplate{
		ID:    "allowlist",
		Tools: []string{"read_file"},
	}
	a.ApplyTemplate(tpl)

	if !a.toolHandler.IsActive("read_file") {
		t.Fatal("expected read_file active via tools allowlist")
	}
	if a.toolHandler.IsActive("web_search") {
		t.Fatal("expected web_search inactive: tools allowlist wins over nothing else")
	}
	if !a.toolHandler.IsActive("memory") {
		t.Fatal("expected always-active tools to survive")
	}
}

func TestApplyTemplateDefaultKeepsStockBehavior(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()

	def, err := templates.NewManager(t.TempDir(), "").Get(templates.BuiltinDefault)
	if err != nil {
		t.Fatalf("Get default template: %v", err)
	}
	a.ApplyTemplate(def)

	if !a.toolHandler.IsActive("grep") {
		t.Fatal("expected default template to keep default-active tools")
	}
	if a.promptTrimHeavySections() {
		t.Fatal("expected default template to keep heavy prompt sections")
	}
}

func TestTemplatePersonaAndBasePromptInSystemPrompt(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.ApplyTemplate(researcherLikeTemplate())

	prompt, err := a.buildDynamicSystemPrompt("")
	if err != nil {
		t.Fatalf("buildDynamicSystemPrompt: %v", err)
	}
	if !strings.Contains(prompt, "thorough test researcher") {
		t.Fatal("expected persona text in dynamic system prompt")
	}

	// Base prompt section is appended when set.
	a.mu.Lock()
	a.templateBasePrompt = "Stay strictly within task scope."
	a.mu.Unlock()
	prompt, err = a.buildDynamicSystemPrompt("")
	if err != nil {
		t.Fatalf("buildDynamicSystemPrompt: %v", err)
	}
	if !strings.Contains(prompt, "Stay strictly within task scope.") {
		t.Fatal("expected template base prompt in dynamic system prompt")
	}
}

func TestTemplateProfileOverridesContextProfile(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()

	tpl := templates.AgentTemplate{ID: "coding-forced", Profile: "coding"}
	a.ApplyTemplate(tpl)

	if got := a.effectiveTemplateProfile("general"); got != "coding" {
		t.Fatalf("effectiveTemplateProfile = %q, want coding", got)
	}
	if got := a.effectiveTemplateProfile("coding"); got != "coding" {
		t.Fatalf("effectiveTemplateProfile = %q, want coding", got)
	}
}

func TestTemplateTrimHeavySections(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.ApplyTemplate(researcherLikeTemplate())

	if !a.promptTrimHeavySections() {
		t.Fatal("expected trim_heavy_sections template to trim heavy sections")
	}
	sections, err := a.buildDynamicRuntimePromptSections("")
	if err != nil {
		t.Fatalf("buildDynamicRuntimePromptSections: %v", err)
	}
	for _, s := range sections {
		switch s.Key {
		case "skill_catalog", "repo_map", "active_skills":
			t.Fatalf("section %q should be trimmed for lean templates", s.Key)
		}
	}
	// Environment and tool availability must survive trimming.
	keys := map[string]bool{}
	for _, s := range sections {
		keys[s.Key] = true
	}
	if !keys["environment"] || !keys["tool_availability"] {
		t.Fatalf("expected environment and tool_availability to survive, got %v", keys)
	}
}

func TestTemplateSkillsAccessorReturnsCopy(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.ApplyTemplate(researcherLikeTemplate())

	skills := a.TemplateSkills()
	if len(skills) != 2 {
		t.Fatalf("TemplateSkills = %v, want 2 entries", skills)
	}
	skills[0] = "mutated"
	if again := a.TemplateSkills(); again[0] == "mutated" {
		t.Fatal("TemplateSkills must return a defensive copy")
	}
}
