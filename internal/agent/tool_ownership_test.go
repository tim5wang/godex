package agent

import (
	"testing"
)

func TestMCPToolsRegisteredWithBuiltinOwner(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleMCP)

	names := []string{
		"list_mcp_resources",
		"read_mcp_resource",
		"list_mcp_tools",
		"call_mcp_tool",
		"list_mcp_prompts",
		"get_mcp_prompt",
	}
	for _, name := range names {
		if a.toolHandler.Get(name) == nil {
			t.Fatalf("expected %s registered", name)
		}
		if got := a.toolHandler.OwnerFor(name); got != builtinPluginOwnerMCP {
			t.Fatalf("expected %s owner %q, got %q", name, builtinPluginOwnerMCP, got)
		}
	}

	// UnregisterOwner removes the whole builtin MCP group cleanly.
	removed := a.toolHandler.UnregisterOwner(builtinPluginOwnerMCP)
	if len(removed) != len(names) {
		t.Fatalf("expected %d MCP tools removed, got %v", len(names), removed)
	}
	for _, name := range names {
		if a.toolHandler.Get(name) != nil {
			t.Fatalf("expected %s removed after UnregisterOwner", name)
		}
	}
}
