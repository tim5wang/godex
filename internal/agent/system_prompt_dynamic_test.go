package agent

import (
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/protocol"
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
