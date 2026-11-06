package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/tools"
)

func stubWorkContextTool() tools.Tool {
	return tools.NewTypedTool(tools.NewToolSpec("work_context", "work desktop ledger", map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
	}, nil), func(ctx context.Context, args map[string]interface{}) (tools.ToolResult, error) {
		return tools.ToolResult{}, nil
	})
}

// TestWorkContextToolIsBundleLoadable pins both halves of the contract:
// the ledger tool is active by default for a bound session, AND it lives in
// its own bundle so tool_exchange can unload/reload it. Registering it as
// AlwaysActive (the old behavior) silently pinned it to always_on, which
// tool_exchange refuses to touch — making it impossible to activate on demand.
func TestWorkContextToolIsBundleLoadable(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.SetWorkContextTool(stubWorkContextTool())

	if !a.toolHandler.IsActive("work_context") {
		t.Fatal("work_context should be active by default once mounted")
	}
	catalog := a.ToolCatalog()
	if facadeContainsString(catalog.AlwaysActiveTools, "work_context") {
		t.Fatalf("work_context must not be pinned to always_on, got %+v", catalog.AlwaysActiveTools)
	}
	if !catalogContainsTool(catalog, bundleWorkContext, "work_context") {
		t.Fatalf("expected work_context in bundle %q, got %+v", bundleWorkContext, catalog.Bundles)
	}

	exchange := tools.NewToolExchangeTool(a.toolHandler)

	// Unload on demand.
	if _, err := exchange.Execute(context.Background(), map[string]interface{}{
		"disable_bundles": []interface{}{bundleWorkContext},
	}); err != nil {
		t.Fatalf("tool_exchange disable: %v", err)
	}
	if a.toolHandler.IsActive("work_context") {
		t.Fatal("tool_exchange could not unload the work_context bundle")
	}

	// And load it back.
	if _, err := exchange.Execute(context.Background(), map[string]interface{}{
		"enable_bundles": []interface{}{bundleWorkContext},
	}); err != nil {
		t.Fatalf("tool_exchange enable: %v", err)
	}
	if !a.toolHandler.IsActive("work_context") {
		t.Fatal("tool_exchange could not reload the work_context bundle")
	}

	// A capability query in plain language must find it, or the model cannot
	// discover the bundle it needs to enable.
	result, err := exchange.Execute(context.Background(), map[string]interface{}{"query": "台账"})
	if err != nil {
		t.Fatalf("tool_exchange query: %v", err)
	}
	if !strings.Contains(result, "work_context") {
		t.Fatalf("query %q should recommend the work_context bundle, got %s", "台账", result)
	}
}
