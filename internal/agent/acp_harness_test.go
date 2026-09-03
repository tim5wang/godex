package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/scope"
	"github.com/tim5wang/godex/internal/domain/events"
	"github.com/tim5wang/godex/internal/tools"
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
			if os.Getenv("GODEX_ACP_FAKE_IMAGE") == "1" {
				// Advertise prompt image support so the harness may forward
				// image content blocks (M2).
				result = map[string]any{
					"protocolVersion": 1,
					"agentCapabilities": map[string]any{
						"promptCapabilities": map[string]any{"image": true},
					},
				}
			}
		case "session/new":
			result = map[string]any{"sessionId": "acp-fake-1"}
		case "session/prompt":
			result = map[string]any{"stopReason": "end_turn"}
			if os.Getenv("GODEX_ACP_FAKE_PERMISSION") == "1" {
				// Send a session/request_permission request and read the
				// client's decision; echo the chosen option as a marker chunk
				// so tests can assert GoDex's permission bridge decision.
				permReq := map[string]any{
					"jsonrpc": "2.0",
					"id":      77,
					"method":  "session/request_permission",
					"params": map[string]any{
						"sessionId": "acp-fake-1",
						"toolCall":  map[string]any{"toolCallId": "call-1"},
						"options": []map[string]any{
							{"optionId": "allow", "name": "Allow", "kind": "allow_once"},
							{"optionId": "deny", "name": "Deny", "kind": "reject_once"},
						},
					},
				}
				permLine, _ := json.Marshal(permReq)
				fmt.Fprintln(os.Stdout, string(permLine))
				if scanner.Scan() {
					var permResp struct {
						Result struct {
							Outcome struct {
								Outcome  string `json:"outcome"`
								OptionId string `json:"optionId"`
							} `json:"outcome"`
						} `json:"result"`
					}
					if err := json.Unmarshal(scanner.Bytes(), &permResp); err == nil && permResp.Result.Outcome.OptionId != "" {
						markerChunk := map[string]any{
							"jsonrpc": "2.0",
							"method":  "session/update",
							"params": map[string]any{
								"sessionId": "acp-fake-1",
								"update": map[string]any{
									"sessionUpdate": "agent_message_chunk",
									"content": map[string]any{
										"type": "text",
										"text": "permission-" + permResp.Result.Outcome.OptionId,
									},
								},
							},
						}
						markerLine, _ := json.Marshal(markerChunk)
						fmt.Fprintln(os.Stdout, string(markerLine))
					}
				}
			}
			if os.Getenv("GODEX_ACP_FAKE_IMAGE") == "1" {
				// Echo a marker chunk when the prompt carried an image block so
				// tests can assert the harness actually forwarded it.
				var promptParams struct {
					Prompt []map[string]any `json:"prompt"`
				}
				if err := json.Unmarshal(req.Params, &promptParams); err == nil {
					for _, block := range promptParams.Prompt {
						if t, _ := block["type"].(string); t == "image" {
							imageChunk := map[string]any{
								"jsonrpc": "2.0",
								"method":  "session/update",
								"params": map[string]any{
									"sessionId": "acp-fake-1",
									"update": map[string]any{
										"sessionUpdate": "agent_message_chunk",
										"content":       map[string]any{"type": "text", "text": "image-received"},
									},
								},
							}
							line, _ := json.Marshal(imageChunk)
							fmt.Fprintln(os.Stdout, string(line))
							break
						}
					}
				}
			}
			if os.Getenv("GODEX_ACP_FAKE_USAGE") == "1" {
				// Advertise per-turn usage on the prompt result and stream a
				// usage_update context-window watermark (idea ① plumbing).
				result = map[string]any{
					"stopReason": "end_turn",
					"usage": map[string]any{
						"inputTokens":       1000,
						"outputTokens":      250,
						"cachedReadTokens":  700,
						"cachedWriteTokens": 50,
						"thoughtTokens":     20,
						"totalTokens":       1270,
					},
				}
				usageUpdate := map[string]any{
					"jsonrpc": "2.0",
					"method":  "session/update",
					"params": map[string]any{
						"sessionId": "acp-fake-1",
						"update": map[string]any{
							"sessionUpdate": "usage_update",
							"used":          12345,
							"size":          200000,
							"cost":          map[string]any{"amount": 0.5, "currency": "USD"},
						},
					},
				}
				usageLine, _ := json.Marshal(usageUpdate)
				fmt.Fprintln(os.Stdout, string(usageLine))
			}
			// Send a plan update (standard ACP entries form), a tool_call
			// update, then a streaming chunk.
			plan := map[string]any{
				"jsonrpc": "2.0",
				"method":  "session/update",
				"params": map[string]any{
					"sessionId": "acp-fake-1",
					"update": map[string]any{
						"sessionUpdate": "plan",
						"entries": []map[string]any{
							{"content": "analyze", "priority": "high", "status": "in_progress"},
							{"content": "report", "priority": "medium", "status": "pending"},
						},
					},
				},
			}
			planLine, _ := json.Marshal(plan)
			fmt.Fprintln(os.Stdout, string(planLine))
			toolCall := map[string]any{
				"jsonrpc": "2.0",
				"method":  "session/update",
				"params": map[string]any{
					"sessionId": "acp-fake-1",
					"update": map[string]any{
						"sessionUpdate": "tool_call",
						"title":         "acp_builtin_tool",
						"toolCallId":    "call-1",
						"rawInput":      map[string]any{"query": "x"},
					},
				},
			}
			line, _ := json.Marshal(toolCall)
			fmt.Fprintln(os.Stdout, string(line))
			// Tool call update (finish) for the same call id, so the harness
			// start/finish events can be paired by ID.
			toolCallUpdate := map[string]any{
				"jsonrpc": "2.0",
				"method":  "session/update",
				"params": map[string]any{
					"sessionId": "acp-fake-1",
					"update": map[string]any{
						"sessionUpdate": "tool_call_update",
						"title":         "acp_builtin_tool",
						"toolCallId":    "call-1",
						"rawOutput":     map[string]any{"result": "ok"},
					},
				},
			}
			line, _ = json.Marshal(toolCallUpdate)
			fmt.Fprintln(os.Stdout, string(line))
			// Streaming text chunk.
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
			line, _ = json.Marshal(update)
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

// capturingSink records emitted events for assertion.
type capturingSink struct {
	mu     sync.Mutex
	events []events.Event
}

func (s *capturingSink) Emit(event events.Event) {
	s.mu.Lock()
	s.events = append(s.events, event)
	s.mu.Unlock()
}

func (s *capturingSink) Snapshot() []events.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]events.Event{}, s.events...)
}

// TestACPHarnessMapsUpdatesToEvents verifies P2 #4: the external engine's
// session/update events (tool_call + message chunk) are replayed as GoDex
// events through the sink.
func TestACPHarnessMapsUpdatesToEvents(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping ACP integration in short mode")
	}
	sink := &capturingSink{}
	h := NewACPHarness("fake-acp", acpHarnessConfig(t))
	_, err := h.RunTurn(context.Background(), HarnessTurnInput{
		SessionID: "s1",
		TurnID:    "t1",
		Sink:      sink,
		Messages: func() []protocol.Message {
			return []protocol.Message{protocol.NewTextMessage(protocol.RoleUser, "run the analysis")}
		},
	})
	if err != nil {
		t.Fatalf("run turn: %v", err)
	}
	emitted := sink.Snapshot()
	if len(emitted) < 3 {
		t.Fatalf("expected at least 3 mapped events, got %d", len(emitted))
	}
	var sawTool, sawDelta, sawPlan bool
	var planTotal, planInProgress int
	var startedID, finishedID string
	for _, event := range emitted {
		switch event.Type {
		case events.EventToolCallStarted:
			if payload, ok := event.Payload.(events.ToolCallPayload); ok && payload.Name == "acp_builtin_tool" {
				sawTool = true
				startedID = payload.ID
			}
		case events.EventToolCallFinished:
			if payload, ok := event.Payload.(events.ToolCallPayload); ok && payload.Name == "acp_builtin_tool" {
				finishedID = payload.ID
			}
		case events.EventAssistantTextDelta:
			if payload, ok := event.Payload.(events.TextPayload); ok && strings.Contains(payload.Text, "hello from acp") {
				sawDelta = true
			}
		case events.EventTodoListUpdated:
			if payload, ok := event.Payload.(events.TodoListPayload); ok && payload.Total > 0 {
				sawPlan = true
				planTotal = payload.Total
				planInProgress = payload.InProgress
			}
		}
	}
	if !sawTool {
		t.Fatal("expected tool_call event mapped from ACP update")
	}
	if !sawDelta {
		t.Fatal("expected assistant_text_delta event mapped from ACP chunk")
	}
	if !sawPlan {
		t.Fatal("expected plan update mapped to todo list event")
	}
	if planTotal != 2 {
		t.Fatalf("expected 2 plan entries mapped, got %d", planTotal)
	}
	if planInProgress != 1 {
		t.Fatalf("expected 1 in_progress plan entry, got %d", planInProgress)
	}
	// P1 regression: start and finish must share the tool call id so
	// downstream collectors can pair them into one tool entry.
	if startedID == "" || finishedID == "" || startedID != finishedID {
		t.Fatalf("tool_call start/finish IDs must pair, got started=%q finished=%q", startedID, finishedID)
	}
	if startedID != "call-1" {
		t.Fatalf("unexpected tool call id: %q", startedID)
	}
}

// TestACPHarnessMapsUsageToEvent verifies the external agent's per-turn usage
// (session/prompt result "usage") is replayed as a model_request_completed
// event so the timeline/cache-hit surfaces render it like a native request.
func TestACPHarnessMapsUsageToEvent(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping ACP integration in short mode")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("executable: %v", err)
	}
	cfg := config.ACPAgentConfig{
		ID:      "fake-acp",
		Command: exe,
		Args:    []string{"-test.run", "TestACPFakeServer"},
		Env:     map[string]string{"GODEX_ACP_HELPER": "1", "GODEX_ACP_FAKE_USAGE": "1"},
	}
	sink := &capturingSink{}
	h := NewACPHarness("fake-acp", cfg)
	if _, err := h.RunTurn(context.Background(), HarnessTurnInput{
		SessionID: "s1",
		TurnID:    "t1",
		Sink:      sink,
		Messages: func() []protocol.Message {
			return []protocol.Message{protocol.NewTextMessage(protocol.RoleUser, "run the analysis")}
		},
	}); err != nil {
		t.Fatalf("run turn: %v", err)
	}
	var payload *events.ModelRequestPayload
	for _, event := range sink.Snapshot() {
		if event.Type == events.EventModelRequestCompleted {
			if p, ok := event.Payload.(events.ModelRequestPayload); ok {
				payload = &p
				break
			}
		}
	}
	if payload == nil {
		t.Fatal("expected model_request_completed event mapped from ACP usage")
	}
	if payload.Model != "acp:fake-acp" {
		t.Fatalf("unexpected usage event model: %q", payload.Model)
	}
	if payload.InputTokens != 1000 || payload.OutputTokens != 250 ||
		payload.CacheReadTokens != 700 || payload.CacheWriteTokens != 50 {
		t.Fatalf("unexpected usage event tokens: %+v", *payload)
	}
}

// TestACPHarnessForwardsImageBlock verifies M2: when the external engine
// advertises promptCapabilities.image, an image block in the user message is
// converted into an ACP image content block and forwarded on the wire; when
// the engine does not advertise image support the image is dropped gracefully
// (text still goes through).
func TestACPHarnessForwardsImageBlock(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping ACP integration in short mode")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("executable: %v", err)
	}
	imageMsg := func() []protocol.Message {
		return []protocol.Message{
			protocol.NewMessage(protocol.RoleUser,
				protocol.TextBlock("look at this"),
				protocol.ImageBlock("image/png", "aGVsbG8="),
			),
		}
	}
	// Engine advertises image support: image must reach the wire.
	withImage := NewACPHarness("fake-acp", config.ACPAgentConfig{
		ID:      "fake-acp",
		Command: exe,
		Args:    []string{"-test.run", "TestACPFakeServer"},
		Env:     map[string]string{"GODEX_ACP_HELPER": "1", "GODEX_ACP_FAKE_IMAGE": "1"},
	})
	result, err := withImage.RunTurn(context.Background(), HarnessTurnInput{
		SessionID: "s1",
		TurnID:    "t1",
		Messages:  imageMsg,
	})
	if err != nil {
		t.Fatalf("run turn with image support: %v", err)
	}
	if !strings.Contains(result.Reply, "image-received") {
		t.Fatalf("expected image block forwarded to the engine, reply=%q", result.Reply)
	}
	// Engine without image support: image dropped, text still delivered.
	withoutImage := NewACPHarness("fake-acp", acpHarnessConfig(t))
	result, err = withoutImage.RunTurn(context.Background(), HarnessTurnInput{
		SessionID: "s1",
		TurnID:    "t1",
		Messages:  imageMsg,
	})
	if err != nil {
		t.Fatalf("run turn without image support: %v", err)
	}
	if !strings.Contains(result.Reply, "hello from acp") {
		t.Fatalf("expected text reply from engine, reply=%q", result.Reply)
	}
	if strings.Contains(result.Reply, "image-received") {
		t.Fatalf("image must be dropped when the engine lacks image support, reply=%q", result.Reply)
	}
	// Image-only turn against a non-image engine is rejected, not silently
	// turned into an empty prompt.
	imageOnly := NewACPHarness("fake-acp", acpHarnessConfig(t))
	_, err = imageOnly.RunTurn(context.Background(), HarnessTurnInput{
		SessionID: "s1",
		TurnID:    "t1",
		Messages: func() []protocol.Message {
			return []protocol.Message{protocol.NewMessage(protocol.RoleUser, protocol.ImageBlock("image/png", "aGVsbG8="))}
		},
	})
	if err == nil || !strings.Contains(err.Error(), "does not support the message content") {
		t.Fatalf("expected content-support error for image-only turn, got: %v", err)
	}
}

// TestACPHarnessPermissionRequestDefaultsToDeny verifies M4: a
// session/request_permission from the external engine is answered by GoDex
// (default: deny) over the real wire, and surfaced as an audit warning event.
func TestACPHarnessPermissionRequestDefaultsToDeny(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping ACP integration in short mode")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("executable: %v", err)
	}
	cfg := config.ACPAgentConfig{
		ID:      "fake-acp",
		Command: exe,
		Args:    []string{"-test.run", "TestACPFakeServer"},
		Env:     map[string]string{"GODEX_ACP_HELPER": "1", "GODEX_ACP_FAKE_PERMISSION": "1"},
	}
	sink := &capturingSink{}
	h := NewACPHarness("fake-acp", cfg)
	result, err := h.RunTurn(context.Background(), HarnessTurnInput{
		SessionID: "s1",
		TurnID:    "t1",
		Sink:      sink,
		Messages: func() []protocol.Message {
			return []protocol.Message{protocol.NewTextMessage(protocol.RoleUser, "run the analysis")}
		},
	})
	if err != nil {
		t.Fatalf("run turn: %v", err)
	}
	if !strings.Contains(result.Reply, "permission-deny") {
		t.Fatalf("expected the engine's permission request answered with deny, reply=%q", result.Reply)
	}
	foundWarning := false
	for _, event := range sink.Snapshot() {
		if event.Type == events.EventWarningRaised {
			if payload, ok := event.Payload.(events.NoticePayload); ok && payload.Code == "acp_permission_request" {
				foundWarning = true
			}
		}
	}
	if !foundWarning {
		t.Fatal("expected an acp_permission_request warning event")
	}
}

// TestACPHarnessPermissionPolicyOverride verifies M4: a custom
// PermissionPolicy can answer permission requests (here: allow) instead of
// the default deny.
func TestACPHarnessPermissionPolicyOverride(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping ACP integration in short mode")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("executable: %v", err)
	}
	cfg := config.ACPAgentConfig{
		ID:      "fake-acp",
		Command: exe,
		Args:    []string{"-test.run", "TestACPFakeServer"},
		Env:     map[string]string{"GODEX_ACP_HELPER": "1", "GODEX_ACP_FAKE_PERMISSION": "1"},
	}
	h := NewACPHarness("fake-acp", cfg)
	h.PermissionPolicy = func(ctx context.Context, req tools.ACPPermissionRequest) (tools.ACPPermissionResponse, error) {
		return tools.SelectACPPermissionOption(req, "allow")
	}
	result, err := h.RunTurn(context.Background(), HarnessTurnInput{
		SessionID: "s1",
		TurnID:    "t1",
		Messages: func() []protocol.Message {
			return []protocol.Message{protocol.NewTextMessage(protocol.RoleUser, "run the analysis")}
		},
	})
	if err != nil {
		t.Fatalf("run turn: %v", err)
	}
	if !strings.Contains(result.Reply, "permission-allow") {
		t.Fatalf("expected the engine's permission request answered with allow, reply=%q", result.Reply)
	}
}

// TestACPHarnessScopeBinding verifies P2 #5: the harness binds to the first
// scope it serves and rejects cross-scope reuse until ResetSession.
func TestACPHarnessScopeBinding(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping ACP integration in short mode")
	}
	h := NewACPHarness("fake-acp", acpHarnessConfig(t))
	input := func(scopeID string) HarnessTurnInput {
		return HarnessTurnInput{
			SessionID: "s1",
			TurnID:    "t1",
			Scope:     scope.Id(scopeID),
			Messages: func() []protocol.Message {
				return []protocol.Message{protocol.NewTextMessage(protocol.RoleUser, "run")}
			},
		}
	}
	if _, err := h.RunTurn(context.Background(), input("session:web-1")); err != nil {
		t.Fatalf("first run in scope: %v", err)
	}
	// Same scope is fine.
	if _, err := h.RunTurn(context.Background(), input("session:web-1")); err != nil {
		t.Fatalf("same scope rerun: %v", err)
	}
	// Cross-scope reuse is rejected.
	if _, err := h.RunTurn(context.Background(), input("session:web-2")); err == nil {
		t.Fatal("expected cross-scope reuse to be rejected")
	} else if !strings.Contains(err.Error(), "bound to scope") {
		t.Fatalf("unexpected scope error: %v", err)
	}
	// ResetSession unbinds so a new scope may bind.
	if err := h.ResetSession(context.Background(), "s1"); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if _, err := h.RunTurn(context.Background(), input("session:web-2")); err != nil {
		t.Fatalf("run after reset in new scope: %v", err)
	}
}

// TestACPHarnessMapsErrorToEvent verifies P2 #4: a failed external turn emits
// the unified error_raised event before returning the error.
func TestACPHarnessMapsErrorToEvent(t *testing.T) {
	sink := &capturingSink{}
	// An ACP agent with no command fails fast at RunTurn.
	h := NewACPHarness("missing-agent", config.ACPAgentConfig{ID: "missing-agent"})
	_, err := h.RunTurn(context.Background(), HarnessTurnInput{
		SessionID: "s1",
		TurnID:    "t1",
		Sink:      sink,
		Messages: func() []protocol.Message {
			return []protocol.Message{protocol.NewTextMessage(protocol.RoleUser, "run")}
		},
	})
	if err == nil {
		t.Fatal("expected run to fail for missing command")
	}
	emitted := sink.Snapshot()
	found := false
	for _, event := range emitted {
		if event.Type == events.EventErrorRaised {
			found = true
		}
	}
	if !found {
		t.Fatal("expected error_raised event mapped from harness failure")
	}
}
