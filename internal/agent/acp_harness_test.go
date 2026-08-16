package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/protocol"
	"github.com/tim5wang/godex/internal/domain/events"
)

// TestACPFakeServer is not a real test: when the test binary is re-executed
// with GODEX_ACP_HELPER=1 it acts as a minimal ACP stdio server (initialize,
// session/new, session/prompt) so the harness tests exercise the real wire
// protocol.
func TestACPFakeServer(t *testing.T) {
	if os.Getenv("GODEX_ACP_HELPER") != "1" {
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
		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{}}
		case "session/new":
			result = map[string]any{"sessionId": "acp-fake-1"}
		case "session/prompt":
			result = map[string]any{"stopReason": "end_turn"}
			// Send a streaming update the client collects into text.
			update := map[string]any{
				"jsonrpc": "2.0",
				"method":  "session/update",
				"params": map[string]any{
					"sessionId": "acp-fake-1",
					"update": map[string]any{
						"sessionUpdate": "agent_message_chunk",
						"content":       map[string]any{"type": "text", "text": "hello from acp"},
					},
				},
			}
			line, _ := json.Marshal(update)
			fmt.Fprintln(os.Stdout, string(line))
		default:
			result = map[string]any{}
		}
		resp := map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result":  result,
		}
		line, _ := json.Marshal(resp)
		fmt.Fprintln(os.Stdout, string(line))
	}
}

func acpHarnessConfig(t *testing.T) config.ACPAgentConfig {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("executable: %v", err)
	}
	return config.ACPAgentConfig{
		ID:      "fake-acp",
		Command: exe,
		Args:    []string{"-test.run", "TestACPFakeServer"},
		Env:     map[string]string{"GODEX_ACP_HELPER": "1"},
	}
}

func TestACPHarnessProfile(t *testing.T) {
	h := NewACPHarness("fake-acp", acpHarnessConfig(t))
	profile := h.Profile()
	if profile.ID != "acp:fake-acp" {
		t.Fatalf("unexpected profile id: %q", profile.ID)
	}
	if h.Models() != nil || h.Tools() != nil {
		t.Fatal("external ACP engine must not claim GoDex models/tools")
	}
	if err := h.ResetSession(context.Background(), "s1"); err != nil {
		t.Fatalf("reset session: %v", err)
	}
	if err := h.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestACPHarnessRunTurn(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping ACP integration in short mode")
	}
	workspace := t.TempDir()
	messages := []protocol.Message{
		protocol.NewTextMessage(protocol.RoleUser, "do the thing"),
	}
	h := NewACPHarness("fake-acp", acpHarnessConfig(t))
	result, err := h.RunTurn(context.Background(), HarnessTurnInput{
		SessionID:    "s1",
		TurnID:       "t1",
		WorkspaceDir: workspace,
		Messages: func() []protocol.Message {
			return messages
		},
	})
	if err != nil {
		t.Fatalf("run turn: %v", err)
	}
	if !result.Completed {
		t.Fatal("expected completed")
	}
	if !strings.Contains(result.Reply, "hello from acp") {
		t.Fatalf("unexpected reply: %q", result.Reply)
	}
}

func TestACPHarnessMissingPrompt(t *testing.T) {
	h := NewACPHarness("fake-acp", acpHarnessConfig(t))
	_, err := h.RunTurn(context.Background(), HarnessTurnInput{
		Messages: func() []protocol.Message { return nil },
	})
	if err == nil {
		t.Fatal("expected error for empty user prompt")
	}
}

func TestLastUserPrompt(t *testing.T) {
	messages := []protocol.Message{
		protocol.NewTextMessage(protocol.RoleAssistant, "previous reply"),
		protocol.NewTextMessage(protocol.RoleUser, "  current ask  "),
		protocol.NewTextMessage(protocol.RoleAssistant, "trailing"),
	}
	got := lastUserPrompt(func() []protocol.Message { return messages })
	if got != "current ask" {
		t.Fatalf("unexpected prompt: %q", got)
	}
	if lastUserPrompt(nil) != "" {
		t.Fatal("nil messages provider must yield empty prompt")
	}
}

// TestRunWithOptionsRoutesToACPHarnessAndConsumesReply verifies the P2 #2 host
// behavior end to end: a turn routed to an ACP harness appends the engine
// reply to the transcript and checkpoints.
func TestRunWithOptionsRoutesToACPHarnessAndConsumesReply(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping ACP integration in short mode")
	}
	a := newTestAgent(t, 4096)
	a.RegisterHarness("acp:fake-acp", NewACPHarness("fake-acp", acpHarnessConfig(t)))
	a.appendMessage(protocol.NewTextMessage(protocol.RoleUser, "please run the analysis"))
	a.AppendAssistantText("user ask", "")

	checkpoints := 0
	sink := &recordingSink{}
	err := a.RunWithOptions(context.Background(), RunOptions{
		SessionID:  "s1",
		TurnID:     "t1",
		Harness:    "acp:fake-acp",
		Sink:       sink,
		Checkpoint: func() { checkpoints++ },
	})
	if err != nil {
		t.Fatalf("routed turn: %v", err)
	}
	if checkpoints == 0 {
		t.Fatal("expected at least one checkpoint after host consumes reply")
	}
	messages := a.GetMessages()
	last := messages[len(messages)-1]
	if last.Role != protocol.RoleAssistant {
		t.Fatalf("expected assistant reply appended, got %+v", last)
	}
	found := false
	for _, block := range last.Content {
		if block.Type == protocol.BlockText && strings.Contains(block.Text, "hello from acp") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected acp reply in transcript, got %+v", last.Content)
	}
}

type recordingSink struct{}

func (s *recordingSink) Emit(events.Event) {}
