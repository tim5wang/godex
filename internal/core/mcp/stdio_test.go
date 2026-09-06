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
	// When GODEX_MCP_HELPER_LOG is set, the helper appends one line per event
	// (start / list / call / prompt) so tests can assert how many processes
	// were spawned and how many requests reached the wire.
	mark := func(kind string) {
		if logPath := os.Getenv("GODEX_MCP_HELPER_LOG"); logPath != "" {
			if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644); err == nil {
				fmt.Fprintln(f, kind)
				_ = f.Close()
			}
		}
	}
	mark("start")
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
			mark("list")
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
			mark("call")
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

func TestManagerTransientServerListsAndCallsWithoutPersistence(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping stdio MCP integration in short mode")
	}
	workspace := t.TempDir()
	configPath := filepath.Join(workspace, ".godex", "mcp.json")
	manager := NewManager(configPath, workspace, filepath.Join(workspace, ".godex", ".tmp"))
	server := ServerConfig{
		Name:    "acp-mcp:session:fake",
		Type:    ServerTypeStdio,
		Command: fakeMCPHelperPath(t),
		Args:    []string{"-test.run", "TestFakeMCPHelperServer", "-test.v"},
		Env:     map[string]string{"GODEX_MCP_HELPER": "1"},
	}
	if err := manager.UpsertTransientServer(server); err != nil {
		t.Fatalf("upsert transient server: %v", err)
	}
	tools, err := manager.ListServerTools(context.Background(), server.Name)
	if err != nil || len(tools) != 2 {
		t.Fatalf("list transient tools: tools=%+v err=%v", tools, err)
	}
	result, err := manager.CallTool(context.Background(), server.Name, "echo", map[string]any{"message": "hi"})
	if err != nil || result.Text != "echo: hi" {
		t.Fatalf("call transient tool: result=%+v err=%v", result, err)
	}
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("transient server must not persist config, stat err=%v", err)
	}
	manager.DeleteTransientServer(server.Name)
	if _, err := manager.ListServerTools(context.Background(), server.Name); err == nil {
		t.Fatal("expected deleted transient server to be unavailable")
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

// TestManagerCachesAndReusesStdioClient verifies the two performance fixes:
// tools/list is cached so repeated session opens do not cold-start a stdio
// daemon, and the stdio client is reused across calls (including tools/call)
// so a daemon stays warm instead of being spawned per request.
func TestManagerCachesAndReusesStdioClient(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping stdio MCP integration in short mode")
	}
	workspace := t.TempDir()
	logPath := filepath.Join(workspace, "helper.log")

	helper := fakeMCPHelperPath(t)
	serverEnv := func(extra map[string]string) map[string]string {
		env := map[string]string{
			"GODEX_MCP_HELPER":     "1",
			"GODEX_MCP_HELPER_LOG": logPath,
		}
		for k, v := range extra {
			env[k] = v
		}
		return env
	}
	writeConfig := func(env map[string]string) string {
		cfg := Config{Servers: []ServerConfig{{
			Name:    "fake",
			Type:    ServerTypeStdio,
			Command: helper,
			Args:    []string{"-test.run", "TestFakeMCPHelperServer", "-test.v"},
			Env:     env,
		}}}
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

	manager := NewManager(writeConfig(serverEnv(nil)), workspace, filepath.Join(workspace, ".godex", ".tmp"))
	defer manager.Close()

	readLog := func() string {
		data, _ := os.ReadFile(logPath)
		return string(data)
	}

	if _, err := manager.ListTools(context.Background()); err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if got := readLog(); got != "start\nlist\n" {
		t.Fatalf("after first ListTools, want one spawn + one tools/list, got %q", got)
	}

	// Second ListTools within the cache TTL must be served from cache: no new
	// spawn and no second tools/list round-trip.
	if _, err := manager.ListTools(context.Background()); err != nil {
		t.Fatalf("list tools (cached): %v", err)
	}
	if got := readLog(); got != "start\nlist\n" {
		t.Fatalf("cached ListTools must not re-spawn or re-list, got %q", got)
	}

	// CallTool must reuse the persistent client instead of spawning again.
	if _, err := manager.CallTool(context.Background(), "fake", "echo", map[string]any{"message": "hi"}); err != nil {
		t.Fatalf("call tool: %v", err)
	}
	if got := readLog(); got != "start\nlist\ncall\n" {
		t.Fatalf("CallTool must reuse the persistent client, got %q", got)
	}

	// A config change invalidates the cache and the client: the next
	// ListTools re-spawns the daemon with the new settings.
	if err := manager.UpsertServer(ServerConfig{
		Name:    "fake",
		Type:    ServerTypeStdio,
		Command: helper,
		Args:    []string{"-test.run", "TestFakeMCPHelperServer", "-test.v"},
		Env:     serverEnv(map[string]string{"EXTRA": "changed"}),
	}); err != nil {
		t.Fatalf("upsert server: %v", err)
	}
	if _, err := manager.ListTools(context.Background()); err != nil {
		t.Fatalf("list tools after config change: %v", err)
	}
	if got := readLog(); strings.Count(got, "start") != 2 {
		t.Fatalf("config change must re-spawn the stdio process, got %q", got)
	}
}
