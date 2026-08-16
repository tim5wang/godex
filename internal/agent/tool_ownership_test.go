package agent

import (
	"testing"
)

func TestMCPToolsRegisteredWithBuiltinOwner(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleMCP)

	for _, name := range []string{"list_mcp_resources", "read_mcp_resource"} {
		if a.toolHandler.Get(name) == nil {
			t.Fatalf("expected %s registered", name)
		}
		if got := a.toolHandler.OwnerFor(name); got != builtinPluginOwnerMCP {
			t.Fatalf("expected %s owner %q, got %q", name, builtinPluginOwnerMCP, got)
		}
	}

	// UnregisterOwner removes the whole builtin MCP group cleanly.
	removed := a.toolHandler.UnregisterOwner(builtinPluginOwnerMCP)
	if len(removed) != 2 {
		t.Fatalf("expected 2 MCP tools removed, got %v", removed)
	}
	for _, name := range []string{"list_mcp_resources", "read_mcp_resource"} {
		if a.toolHandler.Get(name) != nil {
			t.Fatalf("expected %s removed after UnregisterOwner", name)
		}
	}
}
