package agent

import (
	"context"
	"testing"

	"github.com/tim5wang/godex/internal/tools"
)

func registerStepTestTools(a *Agent) {
	// Two MCP tools from server "crm" and one from "kb".
	for _, name := range []string{"crm__get_order", "crm__create_order", "kb__search"} {
		tool := tools.NewTypedTool(tools.NewToolSpec(name, name+" desc", map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		}, nil), func(ctx context.Context, args map[string]interface{}) (tools.ToolResult, error) {
			return tools.ToolResult{}, nil
		})
		a.toolHandler.RegisterWithMeta(tool, tools.ToolMeta{Bundle: "mcp"})
	}
	// Sandbox tools.
	for _, name := range []string{"read_file", "bash", "grep"} {
		tool := tools.NewTypedTool(tools.NewToolSpec(name, name+" desc", map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		}, nil), func(ctx context.Context, args map[string]interface{}) (tools.ToolResult, error) {
			return tools.ToolResult{}, nil
		})
		a.toolHandler.RegisterWithMeta(tool, tools.ToolMeta{Bundle: "core_code"})
	}
}

func TestApplyToolAllowlistFiltersByServerAndSandbox(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	registerStepTestTools(a)

	// Allow only the crm MCP server and read_file + bash from sandbox.
	a.ApplyToolAllowlist([]string{"crm"}, []string{"read_file", "bash"})

	// crm tools stay active.
	for _, name := range []string{"crm__get_order", "crm__create_order"} {
		if !a.toolHandler.IsActive(name) {
			t.Fatalf("expected %q active", name)
		}
	}
	// kb server excluded, and sandbox tools not on the list are excluded.
	for _, name := range []string{"kb__search", "grep"} {
		if a.toolHandler.IsActive(name) {
			t.Fatalf("expected %q inactive", name)
		}
	}
	// Allowed sandbox stays.
	if !a.toolHandler.IsActive("read_file") {
		t.Fatal("expected read_file active")
	}
	// Always-active tools are preserved.
	if !a.toolHandler.IsActive("memory") {
		t.Fatal("expected always-active memory preserved")
	}
}

func TestApplyToolAllowlistWildcard(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	registerStepTestTools(a)

	a.ApplyToolAllowlist([]string{"*"}, []string{"*"})
	for _, name := range []string{"crm__get_order", "kb__search", "read_file", "bash"} {
		if !a.toolHandler.IsActive(name) {
			t.Fatalf("expected %q active with wildcard", name)
		}
	}
}

func TestSplitMCPToolName(t *testing.T) {
	cases := []struct {
		name      string
		server    string
		tool      string
		isMCP     bool
	}{
		{"crm__get_order", "crm", "get_order", true},
		{"read_file", "", "", false},
		{"__", "", "", false},
		{"a__", "", "", false},
		{"__b", "", "", false},
	}
	for _, c := range cases {
		server, tool, ok := splitMCPToolName(c.name)
		if ok != c.isMCP {
			t.Fatalf("%q: expected mcp=%v, got %v", c.name, c.isMCP, ok)
		}
		if ok && (server != c.server || tool != c.tool) {
			t.Fatalf("%q: expected %s/%s, got %s/%s", c.name, c.server, c.tool, server, tool)
		}
	}
}
