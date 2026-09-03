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
			// When GODEX_ACP_TOOL_HELPER_MODELS is set, advertise a model
			// select option so DiscoverACPAgentModelOptions can read it back.
			if os.Getenv("GODEX_ACP_TOOL_HELPER_MODELS") == "1" {
				result = map[string]any{
					"sessionId": "tool-fake-1",
					"configOptions": []map[string]any{
						{
							"type":         "select",
							"id":           "model",
							"name":         "Model",
							"currentValue": "model-a",
							"options": []map[string]any{
								{"name": "Model A", "value": "model-a"},
								{"name": "Model B", "value": "model-b"},
							},
						},
					},
				}
			} else {
				result = map[string]any{"sessionId": "tool-fake-1"}
			}
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

func TestStreamACPAgentInvokesOnUpdate(t *testing.T) {
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
	var streamed []ACPUpdate
	result, err := StreamACPAgent(context.Background(), agent, t.TempDir(), "hello", 30, func(update ACPUpdate) {
		streamed = append(streamed, update)
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	// The fake server streams one text chunk; it must be delivered live.
	foundChunk := false
	for _, update := range streamed {
		if update.Kind == "message_chunk" && strings.Contains(update.Text, "tool reply") {
			foundChunk = true
		}
	}
	if !foundChunk {
		t.Fatalf("expected streamed message chunk, got %+v", streamed)
	}
	// The aggregated result still contains the full reply.
	if !strings.Contains(result.Text, "tool reply") {
		t.Fatalf("expected reply text, got %q", result.Text)
	}
}

// TestDiscoverACPAgentModelOptions verifies model discovery reads the agent's
// session configOptions and that the throwaway process is torn down even when
// the workspace is a relative path (the process must not leak or hang).
func TestDiscoverACPAgentModelOptions(t *testing.T) {
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
		Env: map[string]string{
			"GODEX_ACP_TOOL_HELPER":        "1",
			"GODEX_ACP_TOOL_HELPER_MODELS": "1",
		},
	}
	// Relative workspace: discovery must resolve it to an absolute path
	// before handing it to the agent (dsh rejects relative cwd on
	// session/new).
	models, err := DiscoverACPAgentModelOptions(context.Background(), agent, ".")
	if err != nil {
		t.Fatalf("discover models: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("expected 2 model options, got %+v", models)
	}
	if models[0].Value != "model-a" || models[0].Name != "Model A" {
		t.Fatalf("unexpected first option: %+v", models[0])
	}
	if models[1].Value != "model-b" || models[1].Name != "Model B" {
		t.Fatalf("unexpected second option: %+v", models[1])
	}
}
