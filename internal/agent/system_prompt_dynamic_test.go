package agent

import (
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/core/templates"
)

func TestPluginPromptSectionsInjectedIntoRuntimePrompt(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.SetPluginPromptProvider(func() []runtimePromptSection {
		return []runtimePromptSection{{
			Key:    "plugin:demo:note",
			Kind:   protocol.KindBackground,
			Text:   "A WASM plugin is active.",
			Tokens: 5,
		}}
	})
	sections, err := a.buildDynamicRuntimePromptSections(config.AgentProfileGeneral)
	if err != nil {
		t.Fatalf("build sections: %v", err)
	}
	found := false
	for _, section := range sections {
		if section.Key == "plugin:demo:note" && strings.Contains(section.Text, "WASM plugin") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected plugin section in runtime prompt, got %+v", sections)
	}
}

// TestLeanTemplatePromptOmitsInactiveToolMentions reproduces the reported
// defect: a lean template session (only bash + edit_file, coding profile, no
// tool_exchange / web / lsp) was taught to advertise web/browser/package/lsp
// as "unavailable capabilities" because the prompt unconditionally named
// those tools. The prompt must only mention capabilities that are actually
// active.
func TestLeanTemplatePromptOmitsInactiveToolMentions(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.ApplyTemplate(templates.AgentTemplate{
		ID:      "geek",
		Tools:   []string{"bash", "edit_file"},
		Profile: config.AgentProfileCoding,
	})

	sections, err := a.buildDynamicRuntimePromptSections("")
	if err != nil {
		t.Fatalf("buildDynamicRuntimePromptSections: %v", err)
	}
	var availability string
	for _, s := range sections {
		if s.Key == "tool_availability" {
			availability = s.Text
		}
	}
	if availability == "" {
		t.Fatal("expected tool_availability section")
	}
	// The availability section must not name capabilities that are inactive:
	// naming them is what taught the model to answer "❌ 网络搜索 ❌ 浏览器自动化"
	// when asked to self-introduce.
	for _, banned := range []string{
		"web_search", "web_fetch", "browser", "list_packages", "acp_agent",
		"prefer the lsp tool", "replace web_search", "substitute for",
	} {
		if strings.Contains(availability, banned) {
			t.Fatalf("tool_availability must not mention inactive capability %q: %s", banned, availability)
		}
	}
	// It should still state the fixed tool set and list only active tools.
	if !strings.Contains(availability, "not available on demand") {
		t.Fatalf("tool_availability should state the tool set is fixed: %s", availability)
	}
	if !strings.Contains(availability, "- Active tools: bash, edit_file") {
		t.Fatalf("tool_availability should list the exact active tools: %s", availability)
	}

	dynamic, err := a.buildDynamicSystemPrompt("")
	if err != nil {
		t.Fatalf("buildDynamicSystemPrompt: %v", err)
	}
	// The dynamic system prompt (capability check + coding profile) must also
	// not name inactive web/browser/lsp capabilities.
	for _, banned := range []string{
		"web_search", "web_fetch", "browser", "prefer the lsp tool", "curl/wget",
		"not available in this session",
	} {
		if strings.Contains(dynamic, banned) {
			t.Fatalf("dynamic system prompt must not mention inactive capability %q: %s", banned, dynamic)
		}
	}
}
