package agent

import (
	"reflect"
	"sort"
	"testing"

	"github.com/tim5wang/godex/internal/tools"
)

func TestAgentRefactorKeepsDefaultToolCatalogShape(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()

	catalog := a.ToolCatalog()
	if !facadeContainsString(catalog.ActiveBundles, bundleCoreCode) {
		t.Fatalf("expected default active bundle %q, got %+v", bundleCoreCode, catalog.ActiveBundles)
	}
	if !facadeContainsString(catalog.ActiveBundles, bundlePlanning) {
		t.Fatalf("expected default active bundle %q, got %+v", bundlePlanning, catalog.ActiveBundles)
	}
	if !facadeContainsString(catalog.AlwaysActiveTools, "tool_exchange") {
		t.Fatalf("expected tool_exchange to stay always active, got %+v", catalog.AlwaysActiveTools)
	}
	if !catalogContainsTool(catalog, bundleCoreCode, "bash") {
		t.Fatalf("expected bash in bundle %q, got %+v", bundleCoreCode, catalog.Bundles)
	}
	if !catalogContainsTool(catalog, bundleSubagent, "task") {
		t.Fatalf("expected task in bundle %q, got %+v", bundleSubagent, catalog.Bundles)
	}
}

func TestAgentRefactorKeepsSessionFacadeCopies(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.AddMessage("first")

	messages := a.GetMessages()
	if len(messages) != 1 {
		t.Fatalf("expected one message, got %d", len(messages))
	}
	messages = nil
	if got := len(a.GetMessages()); got != 1 {
		t.Fatalf("mutating returned slice must not mutate agent messages, got %d", got)
	}
}

func TestAgentRefactorKeepsActiveSkillNamesSorted(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.mu.Lock()
	a.activeSkills["zeta"] = &activeSkillState{}
	a.activeSkills["alpha"] = &activeSkillState{}
	a.mu.Unlock()

	got := a.ActiveSkillNames()
	want := []string{"alpha", "zeta"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expected sorted active skills %+v, got %+v", want, got)
	}
}

func facadeContainsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func catalogContainsTool(catalog tools.ToolCatalog, bundleName, toolName string) bool {
	for _, item := range catalog.Bundles {
		if item.Name != bundleName {
			continue
		}
		toolNames := append([]string{}, item.Tools...)
		sort.Strings(toolNames)
		i := sort.SearchStrings(toolNames, toolName)
		return i < len(toolNames) && toolNames[i] == toolName
	}
	return false
}
