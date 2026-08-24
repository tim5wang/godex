package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeHTTPServer acts as a remote Streamable HTTP MCP server: it answers every
// POST with the terminal JSON-RPC result for initialize / tools/list /
// tools/call. When sse is true, responses are wrapped in a text/event-stream
// with a single `message` event (multi-line data), matching the Streamable
// HTTP wire format.
func fakeHTTPServer(t *testing.T, sse bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("MCP-Protocol-Version") == "" {
			http.Error(w, "missing protocol version", http.StatusBadRequest)
			return
		}
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{
				"protocolVersion": mcpProtocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "fake-http", "version": "1.0.0"},
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
		body, _ := json.Marshal(resp)
		if sse {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "event: message\n")
			// Multi-line data: the SSE parser joins all data: lines into one JSON.
			line := strings.ReplaceAll(string(body), "\n", "")
			fmt.Fprintf(w, "data: %s\n\n", line)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// writeHTTPConfig writes an mcp.json with one streamable-http server.
func writeHTTPConfig(t *testing.T, workspace, url string, sse bool) string {
	t.Helper()
	cfg := Config{
		Servers: []ServerConfig{{
			Name:            "remote",
			Type:            ServerTypeHTTP,
			URL:             url,
			Headers:         map[string]string{"Authorization": "Bearer test"},
			SessionRequired: sse,
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

func TestManagerListsAndCallsHTTPTools(t *testing.T) {
	for _, sse := range []bool{false, true} {
		name := "json"
		if sse {
			name = "sse"
		}
		t.Run(name, func(t *testing.T) {
			srv := fakeHTTPServer(t, sse)
			workspace := t.TempDir()
			configPath := writeHTTPConfig(t, workspace, srv.URL, sse)
			manager := NewManager(configPath, workspace, filepath.Join(workspace, ".godex", ".tmp"))

			// ListToolServers includes the remote server.
			servers := manager.ListToolServers()
			if len(servers) != 1 || servers[0] != "remote" {
				t.Fatalf("expected ListToolServers=[remote], got %+v", servers)
			}

			tools, err := manager.ListTools(context.Background())
			if err != nil {
				t.Fatalf("list tools: %v", err)
			}
			if len(tools) != 2 {
				t.Fatalf("expected 2 tools, got %+v", tools)
			}
			if tools[0].Server != "remote" || tools[0].Name != "boom" || tools[1].Name != "echo" {
				t.Fatalf("unexpected tool order: %+v", tools)
			}

			call, err := manager.CallTool(context.Background(), "remote", "echo", map[string]any{"message": "hi"})
			if err != nil {
				t.Fatalf("call tool: %v", err)
			}
			if call.Text != "echo: hi" || call.IsError {
				t.Fatalf("unexpected call result: %+v", call)
			}

			failed, err := manager.CallTool(context.Background(), "remote", "boom", map[string]any{})
			if err != nil {
				t.Fatalf("call boom: %v", err)
			}
			if !failed.IsError {
				t.Fatalf("expected isError for boom, got %+v", failed)
			}
		})
	}
}

func TestHTTPClientRejectsMissingURL(t *testing.T) {
	workspace := t.TempDir()
	configPath := filepath.Join(workspace, ".godex", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cfg := Config{Servers: []ServerConfig{{Name: "bad", Type: ServerTypeHTTP}}}
	data, _ := MarshalConfig(cfg)
	if err := os.WriteFile(configPath, data, 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	manager := NewManager(configPath, workspace, workspace)
	if _, err := manager.ListTools(context.Background()); err == nil {
		t.Fatal("expected error for http server without url")
	} else if !strings.Contains(err.Error(), "missing url") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestHTTPClientPropagatesUpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	workspace := t.TempDir()
	configPath := writeHTTPConfig(t, workspace, srv.URL, false)
	manager := NewManager(configPath, workspace, workspace)
	_, err := manager.ListTools(context.Background())
	if err == nil {
		t.Fatal("expected error from 500 upstream")
	}
}
