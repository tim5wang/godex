package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeMCPHelperPath returns the path to the re-exec'd test binary acting as a
// fake stdio MCP server (see TestFakeMCPHelperServer).
func fakeMCPHelperPath(t *testing.T) string {
	t.Helper()
	helper := os.Getenv("GODEX_MCP_TEST_HELPER")
	if helper != "" {
		return helper
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("executable: %v", err)
	}
	return exe
}

// TestFakeMCPHelperServer is not a real test: when the test binary is
// re-executed with GODEX_MCP_HELPER=1 it acts as a JSON-RPC MCP server over
// stdio so the client tests exercise the real wire protocol.
func TestFakeMCPHelperServer(t *testing.T) {
	if os.Getenv("GODEX_MCP_HELPER") != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			continue
		}
		if len(req.ID) == 0 {
			continue // notification
		}
		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "fake", "version": "1.0.0"},
			}
		case "tools/list":
			result = map[string]any{
				"tools": []map[string]any{
					{
						"name":        "echo",
						"description": "echo the message back",
						"inputSchema": map[string]any{
							"type":       "object",
							"properties": map[string]any{"message": map[string]any{"type": "string"}},
							"required":   []string{"message"},
						},
					},
					{
						"name":        "boom",
						"description": "always errors",
						"inputSchema": map[string]any{"type": "object"},
					},
				},
			}
		case "prompts/list":
			result = map[string]any{
				"prompts": []map[string]any{
					{
						"name":        "review",
						"description": "review the code",
						"arguments":   []map[string]any{{"name": "file", "required": true}},
					},
				},
			}
		case "prompts/get":
			result = map[string]any{
				"description": "review the code",
				"messages": []map[string]any{
					{"role": "user", "content": map[string]any{"type": "text", "text": "Please review the code."}},
					{"role": "assistant", "content": map[string]any{"type": "text", "text": "Here is my review."}},
				},
			}
		case "tools/call":
			var params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			_ = json.Unmarshal(req.Params, &params)
			if params.Name == "boom" {
				result = map[string]any{
					"content": []map[string]any{{"type": "text", "text": "boom failed"}},
					"isError": true,
				}
			} else {
				message, _ := params.Arguments["message"].(string)
				result = map[string]any{
					"content": []map[string]any{{"type": "text", "text": "echo: " + message}},
				}
			}
		default:
			result = map[string]any{}
		}
		resp := map[string]any{
			"jsonrpc": "2.0",
			"id":      json.RawMessage(req.ID),
			"result":  result,
		}
		line, _ := json.Marshal(resp)
		fmt.Fprintln(os.Stdout, string(line))
	}
}

// writeStdioConfig writes an mcp.json with one stdio server backed by the test
// binary itself.
func writeStdioConfig(t *testing.T, workspace string) string {
	t.Helper()
	helper := fakeMCPHelperPath(t)
	cfg := Config{
		Servers: []ServerConfig{{
			Name:    "fake",
			Type:    ServerTypeStdio,
			Command: helper,
			Args:    []string{"-test.run", "TestFakeMCPHelperServer", "-test.v"},
			Env:     map[string]string{"GODEX_MCP_HELPER": "1"},
		}},
	}
	data, err := MarshalConfig(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	configPath := filepath.Join(workspace, ".godex", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(configPath, data, 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return configPath
}

func TestManagerListsAndCallsStdioTools(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping stdio MCP integration in short mode")
	}
	workspace := t.TempDir()
	configPath := writeStdioConfig(t, workspace)
	manager := NewManager(configPath, workspace, filepath.Join(workspace, ".godex", ".tmp"))

	tools, err := manager.ListTools(context.Background())
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %+v", tools)
	}
	if tools[0].Server != "fake" || tools[0].Name != "boom" || tools[1].Name != "echo" {
		t.Fatalf("unexpected tool order: %+v", tools)
	}
	if len(tools[1].InputSchema) == 0 {
		t.Fatal("expected input schema preserved")
	}

	call, err := manager.CallTool(context.Background(), "fake", "echo", map[string]any{"message": "hi"})
	if err != nil {
		t.Fatalf("call tool: %v", err)
	}
	if call.Text != "echo: hi" || call.IsError {
		t.Fatalf("unexpected call result: %+v", call)
	}

	failed, err := manager.CallTool(context.Background(), "fake", "boom", map[string]any{})
	if err != nil {
		t.Fatalf("call boom: %v", err)
	}
	if !failed.IsError {
		t.Fatalf("expected isError for boom, got %+v", failed)
	}
}

func TestManagerStdioMissingCommand(t *testing.T) {
	workspace := t.TempDir()
	configPath := filepath.Join(workspace, ".godex", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cfg := Config{Servers: []ServerConfig{{Name: "bad", Type: ServerTypeStdio}}}
	data, _ := MarshalConfig(cfg)
	if err := os.WriteFile(configPath, data, 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	manager := NewManager(configPath, workspace, workspace)
	if _, err := manager.ListTools(context.Background()); err == nil {
		t.Fatal("expected error for stdio server without command")
	} else if !strings.Contains(err.Error(), "missing command") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestStdioClientFailsOnUnknownServer(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping stdio MCP integration in short mode")
	}
	workspace := t.TempDir()
	configPath := writeStdioConfig(t, workspace)
	manager := NewManager(configPath, workspace, workspace)
	if _, err := manager.CallTool(context.Background(), "ghost", "echo", nil); err == nil {
		t.Fatal("expected error for unknown server")
	}
	if _, err := exec.LookPath("definitely-not-a-real-mcp-server-binary"); err == nil {
		t.Skip("unexpectedly found sentinel binary")
	}
}

func TestManagerListsAndGetsPrompts(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping stdio MCP integration in short mode")
	}
	workspace := t.TempDir()
	configPath := writeStdioConfig(t, workspace)
	manager := NewManager(configPath, workspace, filepath.Join(workspace, ".godex", ".tmp"))

	prompts, err := manager.ListPrompts(context.Background())
	if err != nil {
		t.Fatalf("list prompts: %v", err)
	}
	if len(prompts) != 1 || prompts[0].Name != "review" || prompts[0].Server != "fake" {
		t.Fatalf("unexpected prompts: %+v", prompts)
	}
	if len(prompts[0].Arguments) == 0 {
		t.Fatal("expected prompt arguments preserved")
	}

	got, err := manager.GetPrompt(context.Background(), "fake", "review", map[string]any{"file": "main.go"})
	if err != nil {
		t.Fatalf("get prompt: %v", err)
	}
	if len(got.Messages) != 2 {
		t.Fatalf("expected 2 rendered messages, got %+v", got)
	}
	if got.Messages[0].Role != "user" || !strings.Contains(got.Messages[0].Content, "review the code") {
		t.Fatalf("unexpected first message: %+v", got.Messages[0])
	}
}
