package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/core/config"
)

// TestACPToolFakeServer is not a real test: re-exec'd with GODEX_ACP_TOOL_HELPER=1
// it acts as a minimal ACP stdio server for the exported RunACPAgent test.
func TestACPToolFakeServer(t *testing.T) {
	if os.Getenv("GODEX_ACP_TOOL_HELPER") != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var req struct {
			ID     any             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			continue
		}
		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": 1}
		case "session/new":
			result = map[string]any{"sessionId": "tool-fake-1"}
		case "session/prompt":
			result = map[string]any{"stopReason": "end_turn"}
			update := map[string]any{
				"jsonrpc": "2.0",
				"method":  "session/update",
				"params": map[string]any{
					"sessionId": "tool-fake-1",
					"update": map[string]any{
						"sessionUpdate": "agent_message_chunk",
						"content":       map[string]any{"type": "text", "text": "tool reply"},
					},
				},
			}
			line, _ := json.Marshal(update)
			fmt.Fprintln(os.Stdout, string(line))
		default:
			result = map[string]any{}
		}
		resp := map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result}
		line, _ := json.Marshal(resp)
		fmt.Fprintln(os.Stdout, string(line))
	}
}

func TestRunACPAgentExportedWrapper(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping ACP integration in short mode")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("executable: %v", err)
	}
	agent := config.ACPAgentConfig{
		ID:      "fake",
		Command: exe,
		Args:    []string{"-test.run", "TestACPToolFakeServer"},
		Env:     map[string]string{"GODEX_ACP_TOOL_HELPER": "1"},
	}
	result, err := RunACPAgent(context.Background(), agent, t.TempDir(), "hello", 30)
	if err != nil {
		t.Fatalf("run acp agent: %v", err)
	}
	if !strings.Contains(result.Text, "tool reply") {
		t.Fatalf("unexpected reply: %q", result.Text)
	}
	if result.Agent != "fake" || result.SessionID != "tool-fake-1" {
		t.Fatalf("unexpected result metadata: %+v", result)
	}
}
