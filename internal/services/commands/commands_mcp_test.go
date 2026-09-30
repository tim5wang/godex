package commands

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/core/mcp"
)

func TestExecuteMCPListToolsAndLoad(t *testing.T) {
	cfg := newTestConfig(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("get test executable: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(cfg.MCPConfigPath), 0755); err != nil {
		t.Fatalf("create MCP config directory: %v", err)
	}
	configData, err := mcp.MarshalConfig(mcp.Config{Servers: []mcp.ServerConfig{{
		Name:    "fake",
		Type:    mcp.ServerTypeStdio,
		Command: exe,
		Args:    []string{"-test.run", "TestCommandsMCPServerHelper"},
		Env:     map[string]string{"GODEX_COMMAND_MCP_HELPER": "1"},
	}}})
	if err != nil {
		t.Fatalf("marshal MCP config: %v", err)
	}
	if err := os.WriteFile(cfg.MCPConfigPath, configData, 0644); err != nil {
		t.Fatalf("write MCP config: %v", err)
	}

	a := newTestAgent(t, cfg)
	service := NewService(cfg)
	ctx := context.Background()

	list, err := service.Execute(ctx, a, Command{Name: "mcp"})
	if err != nil {
		t.Fatalf("list MCP servers: %v", err)
	}
	if !strings.Contains(list.Output, "fake (stdio)") || strings.Contains(list.Output, exe) {
		t.Fatalf("unexpected MCP server list (must not expose config details): %q", list.Output)
	}

	toolsResult, err := service.Execute(ctx, a, Command{Name: "mcp", Args: []string{"tools", "fake"}})
	if err != nil {
		t.Fatalf("list MCP server tools: %v", err)
	}
	if !strings.Contains(toolsResult.Output, "echo: echo a message") || !strings.Contains(toolsResult.Output, "status: report status") {
		t.Fatalf("unexpected MCP tool listing: %q", toolsResult.Output)
	}

	load, err := service.Execute(ctx, a, Command{Name: "mcp", Args: []string{"load", "fake"}})
	if err != nil {
		t.Fatalf("load MCP server: %v", err)
	}
	if !load.RefreshSnapshot || !strings.Contains(load.Output, "Loaded 2 tools") {
		t.Fatalf("unexpected MCP load result: %+v", load)
	}
	activeTools := a.ExportState().ActiveTools
	for _, name := range []string{"fake__echo", "fake__status"} {
		if !containsCommandString(activeTools, name) {
			t.Fatalf("expected loaded MCP tool %q in persisted active tools: %v", name, activeTools)
		}
	}
}

func TestExecuteMCPRejectsInvalidUsage(t *testing.T) {
	cfg := newTestConfig(t)
	service := NewService(cfg)
	a := newTestAgent(t, cfg)
	for _, cmd := range []Command{
		{Name: "mcp", Args: []string{"tools"}},
		{Name: "mcp", Args: []string{"load"}},
		{Name: "mcp", Args: []string{"unknown"}},
	} {
		if _, err := service.Execute(context.Background(), a, cmd); err == nil {
			t.Fatalf("expected usage or unknown-subcommand error for %+v", cmd)
		}
	}
}

func containsCommandString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

// TestCommandsMCPServerHelper is re-executed as a stdio MCP server.
func TestCommandsMCPServerHelper(t *testing.T) {
	if os.Getenv("GODEX_COMMAND_MCP_HELPER") != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil || len(request.ID) == 0 || string(request.ID) == "null" {
			continue
		}
		var result any
		switch request.Method {
		case "initialize":
			result = map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities":    map[string]any{"tools": map[string]any{}},
			}
		case "tools/list":
			result = map[string]any{"tools": []map[string]any{
				{"name": "echo", "description": "echo a message", "inputSchema": map[string]any{"type": "object"}},
				{"name": "status", "description": "report status", "inputSchema": map[string]any{"type": "object"}},
			}}
		default:
			result = map[string]any{}
		}
		response, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
		fmt.Fprintln(os.Stdout, string(response))
	}
}
