package agent

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/tim5wang/godex/internal/core/mcp"
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

func TestMCPServerToolsRegisteredPerServer(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping stdio MCP integration in short mode")
	}
	a := newTestAgent(t, 4096)
	// Point the agent's MCP config at a fake stdio server (re-exec'd helper
	// from the mcp package tests is not available here; use the tools-level
	// fake via a config pointing at this test binary's MCP helper).
	workspace := t.TempDir()
	configPath := filepath.Join(workspace, ".godex", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("executable: %v", err)
	}
	cfg := map[string]any{
		"servers": []map[string]any{{
			"name":    "fake",
			"type":    "stdio",
			"command": exe,
			"args":    []string{"-test.run", "TestMCPFakeServerHelper"},
			"env":     map[string]string{"GODEX_MCP_HELPER": "1"},
		}},
	}
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(configPath, data, 0644); err != nil {
		t.Fatalf("write mcp config: %v", err)
	}
	a.cfg.MCPConfigPath = configPath
	a.mcpMgr = mcp.NewManager(configPath, workspace, filepath.Join(workspace, ".godex", ".tmp"))

	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleMCP)

	// The fake server exposes echo + boom tools; they must appear namespaced
	// and owned by mcp:fake.
	for _, name := range []string{"fake__echo", "fake__boom"} {
		if a.toolHandler.Get(name) == nil {
			t.Fatalf("expected per-server tool %s registered", name)
		}
		if got := a.toolHandler.OwnerFor(name); got != "mcp:fake" {
			t.Fatalf("expected owner mcp:fake for %s, got %q", name, got)
		}
	}
	// Unregistering one server's group leaves the builtin bridge intact.
	removed := a.toolHandler.UnregisterOwner("mcp:fake")
	if len(removed) != 2 {
		t.Fatalf("expected 2 per-server tools removed, got %v", removed)
	}
	if a.toolHandler.Get("list_mcp_tools") == nil {
		t.Fatal("expected generic bridge tools to remain")
	}
}

// TestMCPFakeServerHelper is not a real test: re-exec'd with
// GODEX_MCP_HELPER=1 it serves the MCP tools/list + tools/call protocol.
func TestMCPFakeServerHelper(t *testing.T) {
	if os.Getenv("GODEX_MCP_HELPER") != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		var req struct {
			ID     any             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			continue
		}
		if req.ID == nil {
			continue
		}
		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{"tools": map[string]any{}}}
		case "tools/list":
			result = map[string]any{
				"tools": []map[string]any{
					{"name": "echo", "description": "echo", "inputSchema": map[string]any{"type": "object"}},
					{"name": "boom", "description": "boom", "inputSchema": map[string]any{"type": "object"}},
				},
			}
		case "tools/call":
			result = map[string]any{"content": []map[string]any{{"type": "text", "text": "ok"}}}
		default:
			result = map[string]any{}
		}
		resp := map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result}
		line, _ := json.Marshal(resp)
		fmt.Fprintln(os.Stdout, string(line))
	}
}
