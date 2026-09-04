package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/coder/acp-go-sdk"
	"github.com/tim5wang/godex/internal/contracts/protocol"
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
			} else if os.Getenv("GODEX_ACP_TOOL_HELPER_MCP") == "1" {
				// Echo a marker session id when the configured stdio MCP
				// servers actually reached the wire (M3a).
				var newSessionParams struct {
					McpServers []map[string]any `json:"mcpServers"`
				}
				sessionID := "tool-fake-1"
				if err := json.Unmarshal(req.Params, &newSessionParams); err == nil {
					for _, server := range newSessionParams.McpServers {
						if name, _ := server["name"].(string); name == "files" {
							sessionID = "tool-fake-mcp"
						}
					}
				}
				result = map[string]any{"sessionId": sessionID}
			} else {
				result = map[string]any{"sessionId": "tool-fake-1"}
			}
		case "session/prompt":
			result = map[string]any{"stopReason": "end_turn"}
			if os.Getenv("GODEX_ACP_TOOL_HELPER_USAGE") == "1" {
				// Advertise per-turn usage on the prompt result and stream a
				// usage_update context-window watermark so the client-side
				// capture path (ACPRunResult.Usage / SessionUsage) is tested
				// against the real wire format.
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
						"sessionId": "tool-fake-1",
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

// TestStreamACPAgentCapturesUsage verifies the client-side usage plumbing:
// per-turn tokens from the session/prompt result (Usage) and the streamed
// usage_update context-window watermark (SessionUsage) are both captured from
// the real wire protocol and surfaced on the run result + streamed updates.
func TestStreamACPAgentCapturesUsage(t *testing.T) {
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
			"GODEX_ACP_TOOL_HELPER":       "1",
			"GODEX_ACP_TOOL_HELPER_USAGE": "1",
		},
	}
	var streamed []ACPUpdate
	result, err := StreamACPAgent(context.Background(), agent, t.TempDir(), "hello", 30, func(update ACPUpdate) {
		streamed = append(streamed, update)
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if result.Usage == nil {
		t.Fatal("expected per-turn usage from prompt result")
	}
	usage := result.Usage
	if usage.InputTokens != 1000 || usage.OutputTokens != 250 ||
		usage.CachedReadTokens != 700 || usage.CachedWriteTokens != 50 ||
		usage.ThoughtTokens != 20 || usage.TotalTokens != 1270 {
		t.Fatalf("unexpected per-turn usage: %+v", usage)
	}
	if result.SessionUsage == nil {
		t.Fatal("expected usage_update watermark on the run result")
	}
	if result.SessionUsage.Used != 12345 || result.SessionUsage.Size != 200000 {
		t.Fatalf("unexpected session usage watermark: %+v", result.SessionUsage)
	}
	if result.SessionUsage.Cost == nil || result.SessionUsage.Cost.Amount != 0.5 || result.SessionUsage.Cost.Currency != "USD" {
		t.Fatalf("unexpected session usage cost: %+v", result.SessionUsage.Cost)
	}
	foundWatermark := false
	for _, update := range streamed {
		if update.Kind == "usage_update" {
			foundWatermark = true
			if update.SessionUsage == nil || update.SessionUsage.Used != 12345 || update.SessionUsage.Size != 200000 {
				t.Fatalf("unexpected streamed usage_update: %+v", update)
			}
		}
	}
	if !foundWatermark {
		t.Fatalf("expected a streamed usage_update, got %+v", streamed)
	}
}

// TestRunACPAgentForwardsMcpServers verifies M3a: configured stdio MCP
// servers are passed through the standard session/new mcpServers field so the
// external agent can connect to them (the bridge for exposing godex-local
// tools to the agent).
func TestRunACPAgentForwardsMcpServers(t *testing.T) {
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
			"GODEX_ACP_TOOL_HELPER":     "1",
			"GODEX_ACP_TOOL_HELPER_MCP": "1",
		},
		McpServers: []config.ACPMcpServer{
			{Name: "files", Command: "/usr/bin/true", Args: []string{"--stdio"}, Env: map[string]string{"GODEX_EXT": "1"}},
		},
	}
	result, err := RunACPAgent(context.Background(), agent, t.TempDir(), "hello", 30)
	if err != nil {
		t.Fatalf("run acp agent with mcp servers: %v", err)
	}
	if result.SessionID != "tool-fake-mcp" {
		t.Fatalf("expected configured MCP servers on session/new, got session id %q", result.SessionID)
	}
}

// TestDenyACPPermissionRequest verifies the default M4 permission decision:
// it picks the agent's "reject once" option, falls back to "reject always",
// and errors when only allow options are offered.
func TestDenyACPPermissionRequest(t *testing.T) {
	option := func(id string, kind acp.PermissionOptionKind) acp.PermissionOption {
		return acp.PermissionOption{OptionId: acp.PermissionOptionId(id), Name: id, Kind: kind}
	}
	req := func(opts ...acp.PermissionOption) ACPPermissionRequest {
		return ACPPermissionRequest{Options: opts}
	}
	// Prefers reject_once over reject_always.
	resp, err := DenyACPPermissionRequest(req(
		option("allow", acp.PermissionOptionKindAllowOnce),
		option("deny_always", acp.PermissionOptionKindRejectAlways),
		option("deny", acp.PermissionOptionKindRejectOnce),
	))
	if err != nil {
		t.Fatalf("deny: %v", err)
	}
	if resp.Outcome.Selected == nil || string(resp.Outcome.Selected.OptionId) != "deny" {
		t.Fatalf("expected reject_once option selected, got %+v", resp.Outcome)
	}
	// Falls back to reject_always.
	resp, err = DenyACPPermissionRequest(req(option("allow", acp.PermissionOptionKindAllowAlways), option("deny_always", acp.PermissionOptionKindRejectAlways)))
	if err != nil {
		t.Fatalf("deny fallback: %v", err)
	}
	if resp.Outcome.Selected == nil || string(resp.Outcome.Selected.OptionId) != "deny_always" {
		t.Fatalf("expected reject_always fallback, got %+v", resp.Outcome)
	}
	// No rejection option: errors (answered as a JSON-RPC error = denied).
	if _, err := DenyACPPermissionRequest(req(option("allow", acp.PermissionOptionKindAllowOnce))); err == nil {
		t.Fatal("expected error when no rejection option is offered")
	}
	// SelectACPPermissionOption picks an explicit option.
	resp, err = SelectACPPermissionOption(req(option("allow", acp.PermissionOptionKindAllowOnce), option("deny", acp.PermissionOptionKindRejectOnce)), "allow")
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if resp.Outcome.Selected == nil || string(resp.Outcome.Selected.OptionId) != "allow" {
		t.Fatalf("expected allow option selected, got %+v", resp.Outcome)
	}
	if _, err := SelectACPPermissionOption(req(option("deny", acp.PermissionOptionKindRejectOnce)), "nope"); err == nil {
		t.Fatal("expected error when selecting an option the agent did not offer")
	}
}

// TestACPContentBlocksForMessage verifies M2 content conversion: text always
// passes through, image blocks are included only with includeImages, whitespace
// text is dropped, and data-URI image payloads are unwrapped to raw base64.
func TestACPContentBlocksForMessage(t *testing.T) {
	msg := protocol.NewMessage(protocol.RoleUser,
		protocol.TextBlock("look at this"),
		protocol.ImageBlock("image/png", "aGVsbG8="),
		protocol.TextBlock("   "),
	)
	withImage := ACPContentBlocksForMessage(msg, true)
	if len(withImage) != 2 {
		t.Fatalf("expected 2 content blocks (text+image), got %d", len(withImage))
	}
	if withImage[0].Text == nil || withImage[0].Text.Text != "look at this" {
		t.Fatalf("unexpected first block: %+v", withImage[0])
	}
	if withImage[1].Image == nil {
		t.Fatalf("expected image block, got %+v", withImage[1])
	}
	if withImage[1].Image.MimeType != "image/png" || withImage[1].Image.Data != "aGVsbG8=" {
		t.Fatalf("unexpected image block content: %+v", withImage[1].Image)
	}
	// Without image support the image block is dropped; whitespace text too.
	textOnly := ACPContentBlocksForMessage(msg, false)
	if len(textOnly) != 1 || textOnly[0].Text == nil || textOnly[0].Text.Text != "look at this" {
		t.Fatalf("unexpected text-only conversion: %+v", textOnly)
	}
	// data URI payload is unwrapped to the raw base64 body.
	uriMsg := protocol.NewMessage(protocol.RoleUser, protocol.ImageBlock("image/png", "data:image/png;base64,YWJjZA=="))
	converted, ok := ACPImageContentBlock(uriMsg.Content[0])
	if !ok {
		t.Fatal("expected image conversion to succeed")
	}
	if converted.Image == nil || converted.Image.Data != "YWJjZA==" || converted.Image.MimeType != "image/png" {
		t.Fatalf("unexpected data-URI unwrap: %+v", converted.Image)
	}
	// Non-image blocks do not convert.
	textBlock := protocol.NewMessage(protocol.RoleUser, protocol.TextBlock("x"))
	if _, ok := ACPImageContentBlock(textBlock.Content[0]); ok {
		t.Fatal("text block must not convert to an image block")
	}
}

// TestSessionUpdateToolCallInputFallback verifies that a tool_call_update
// which omits rawInput keeps the parameters announced by the originating
// tool_call (pi-acp/dsh send the completion update without re-sending the
// input, which previously made the finished tool row un-expandable), and that
// very long tool titles (pi-acp streams the full bash command as the title)
// are shortened so the status bar / tool row stays single-line.
func TestSessionUpdateToolCallInputFallbackAndTitleTruncation(t *testing.T) {
	client := &acpSDKClient{}
	var updates []ACPUpdate
	client.onUpdate = func(u ACPUpdate) {
		updates = append(updates, u)
	}

	title := "bash"
	for i := 0; i < 20; i++ {
		title += " --flag-with-a-very-long-name=" + string(rune('a'+i%26))
	}

	// Initial tool_call with raw input.
	err := client.SessionUpdate(context.Background(), acp.SessionNotification{
		SessionId: "s1",
		Update: acp.SessionUpdate{
			ToolCall: &acp.SessionUpdateToolCall{
				Title:      title,
				ToolCallId: "call-1",
				RawInput:   map[string]any{"command": "echo hello", "cwd": "/repo"},
			},
		},
	})
	if err != nil {
		t.Fatalf("tool_call update: %v", err)
	}

	// Completion update WITHOUT rawInput (like pi-acp / dsh).
	err = client.SessionUpdate(context.Background(), acp.SessionNotification{
		SessionId: "s1",
		Update: acp.SessionUpdate{
			ToolCallUpdate: &acp.SessionToolCallUpdate{
				ToolCallId: "call-1",
				Status:     ptrTo(acp.ToolCallStatusCompleted),
			},
		},
	})
	if err != nil {
		t.Fatalf("tool_call_update: %v", err)
	}

	if len(updates) != 2 {
		t.Fatalf("expected 2 updates (start + finish), got %d: %+v", len(updates), updates)
	}
	started := updates[0]
	if started.Kind != "tool_call" {
		t.Fatalf("expected first update kind tool_call, got %q", started.Kind)
	}
	if started.Input["command"] != "echo hello" {
		t.Fatalf("expected started input command, got %+v", started.Input)
	}
	// The long bash command title must be shortened.
	if len([]rune(started.Name)) > 60 {
		t.Fatalf("expected shortened tool title, got %d runes: %q", len([]rune(started.Name)), started.Name)
	}
	finished := updates[1]
	if finished.Kind != "tool_call_update" {
		t.Fatalf("expected second update kind tool_call_update, got %q", finished.Kind)
	}
	// Input must fall back to the parameters recorded by the originating
	// tool_call even though the update carried none.
	if finished.Input["command"] != "echo hello" {
		t.Fatalf("expected finished input to fall back to started input, got %+v", finished.Input)
	}
	if finished.Input["cwd"] != "/repo" {
		t.Fatalf("expected finished input cwd preserved, got %+v", finished.Input)
	}
	if finished.Name != started.Name {
		t.Fatalf("expected finished name to match started name, got %q vs %q", finished.Name, started.Name)
	}
}

func ptrTo[T any](v T) *T { return &v }
