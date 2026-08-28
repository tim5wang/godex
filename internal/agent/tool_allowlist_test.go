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

func TestApplyToolOverlayAddRemoveReplace(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	registerStepTestTools(a)

	// Baseline: template pinned crm server + read_file only (simulates the
	// template's exact tool set).
	a.ApplyToolAllowlist([]string{"crm"}, []string{"read_file"})

	// Overlay: append kb server + bash, remove crm__create_order (tool-level
	// exclusion), keep the rest. Semantics: 可增可删可替换.
	a.ApplyToolOverlay([]string{"kb"}, []string{"bash", "!crm__create_order"})

	for _, name := range []string{"crm__get_order", "kb__search", "read_file", "bash"} {
		if !a.toolHandler.IsActive(name) {
			t.Fatalf("expected %q active after overlay", name)
		}
	}
	if a.toolHandler.IsActive("crm__create_order") {
		t.Fatalf("expected crm__create_order removed by !tool overlay")
	}
	if a.toolHandler.IsActive("grep") {
		t.Fatalf("expected grep still inactive (never baseline nor overlay)")
	}
	// Always-active tools stay.
	if !a.toolHandler.IsActive("memory") {
		t.Fatal("expected always-active memory preserved after overlay")
	}
}

func TestApplyToolOverlayServerExclusion(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	registerStepTestTools(a)

	// Baseline: crm + kb servers.
	a.ApplyToolOverlay([]string{"crm", "kb"}, []string{"*"})

	// Server-level exclusion: drop the whole kb server.
	a.ApplyToolOverlay([]string{"!kb"}, nil)

	if !a.toolHandler.IsActive("crm__get_order") {
		t.Fatal("expected crm__get_order active after !kb")
	}
	if a.toolHandler.IsActive("kb__search") {
		t.Fatal("expected kb__search removed by !kb")
	}
}

func TestApplyToolOverlayWildcard(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	registerStepTestTools(a)

	a.ApplyToolAllowlist([]string{"crm"}, []string{})
	a.ApplyToolOverlay([]string{"*"}, []string{"*"})
	for _, name := range []string{"crm__get_order", "crm__create_order", "kb__search", "read_file", "bash", "grep"} {
		if !a.toolHandler.IsActive(name) {
			t.Fatalf("expected %q active with wildcard overlay", name)
		}
	}
}

func TestApplyStepListNarrowOnlyRemoves(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	registerStepTestTools(a)

	// Baseline + overlay: crm + kb servers, read_file + bash + grep.
	a.ApplyToolOverlay([]string{"crm", "kb"}, []string{"read_file", "bash", "grep"})

	// Request narrows: only crm server, sandbox "!grep".
	a.ApplyStepListNarrow([]string{"crm"}, []string{"*", "!grep"})

	for _, name := range []string{"crm__get_order", "crm__create_order", "read_file", "bash"} {
		if !a.toolHandler.IsActive(name) {
			t.Fatalf("expected %q active after narrow", name)
		}
	}
	for _, name := range []string{"kb__search", "grep"} {
		if a.toolHandler.IsActive(name) {
			t.Fatalf("expected %q removed by narrow", name)
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
