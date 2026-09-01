package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tim5wang/godex/internal/agent"
	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/core/conversation"
	"github.com/tim5wang/godex/internal/core/llmcapture"
	"github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/commands"
)

func newLlmCaptureTestServer(t *testing.T) (*httptest.Server, *llmcapture.Capture) {
	t.Helper()
	cfg := newTestConfig(t)
	manager := newTestManager(t, cfg)
	caller := &stubCaller{responses: []protocol.Response{
		{Content: []protocol.Block{protocol.TextBlock("done")}},
	}}
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, caller), commands.NewService(cfg))
	capture := llmcapture.New(llmcapture.Options{DumpDir: t.TempDir()})
	t.Cleanup(capture.Close)
	handler := NewHandlerWithDependencies(Dependencies{
		Config: manager, Backend: service,
		LlmCapture: capture,
	})
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server, capture
}

func TestLlmCaptureEndpointsRoundtrip(t *testing.T) {
	server, capture := newLlmCaptureTestServer(t)

	// 1. Status: starts disabled.
	resp, raw := doAgentTemplateJSON(t, http.MethodGet, server.URL+"/llm-capture/status", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status status = %d, want 200", resp.StatusCode)
	}
	var status struct {
		Enabled  bool   `json:"enabled"`
		DumpPath string `json:"dump_path"`
	}
	if err := json.Unmarshal(raw, &status); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if status.Enabled {
		t.Fatal("capture should start disabled")
	}

	// 2. Enable.
	resp, _ = doAgentTemplateJSON(t, http.MethodPost, server.URL+"/llm-capture/enable", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("enable status = %d, want 200", resp.StatusCode)
	}
	if !capture.Enabled() {
		t.Fatal("capture should be enabled after POST enable")
	}

	// 3. Fire a usage event through the real notifyUsage path (simulates a live
	// LLM call carrying a usage context), then list records.
	ctx := conversation.WithUsageContext(context.Background(), conversation.UsageContext{
		SessionID: "sess-cap",
		TurnID:    "turn-cap",
	})
	conversation.NotifyUsageHooksForTest(ctx, conversation.UsageEvent{
		Context: conversation.UsageContext{SessionID: "sess-cap", TurnID: "turn-cap"},
		Request: protocol.Request{Model: "cap-model", Messages: []protocol.APIMessage{{Role: "user", Content: []protocol.Block{{Type: protocol.BlockText, Text: "hi"}}}}},
		Response: &protocol.Response{
			Content: []protocol.Block{{Type: protocol.BlockText, Text: "hello"}},
			Usage:   &protocol.Usage{InputTokens: 3, OutputTokens: 2},
		},
	})

	resp, raw = doAgentTemplateJSON(t, http.MethodGet, server.URL+"/llm-capture/records", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("records status = %d, want 200", resp.StatusCode)
	}
	var list []llmcapture.Summary
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 record, got %d", len(list))
	}
	if list[0].Model != "cap-model" || list[0].InputTokens != 5 {
		t.Fatalf("unexpected summary: %+v", list[0])
	}

	// 4. Fetch the full record by id.
	resp, raw = doAgentTemplateJSON(t, http.MethodGet, server.URL+"/llm-capture/records/"+list[0].ID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("record status = %d, want 200", resp.StatusCode)
	}
	var rec llmcapture.Record
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatalf("decode record: %v", err)
	}
	if rec.Request.Model != "cap-model" || rec.Response == nil || len(rec.Response.Content) == 0 {
		t.Fatalf("record lost request/response: %+v", rec)
	}

	// 5. Unknown id → 404.
	resp, _ = doAgentTemplateJSON(t, http.MethodGet, server.URL+"/llm-capture/records/nope", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown record status = %d, want 404", resp.StatusCode)
	}

	// 6. Clear wipes the in-memory ring.
	resp, _ = doAgentTemplateJSON(t, http.MethodPost, server.URL+"/llm-capture/clear", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("clear status = %d, want 200", resp.StatusCode)
	}
	resp, raw = doAgentTemplateJSON(t, http.MethodGet, server.URL+"/llm-capture/records", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("records-after-clear status = %d, want 200", resp.StatusCode)
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("decode list after clear: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("expected 0 records after clear, got %d", len(list))
	}

	// 7. jsonl file on disk has the record even after memory clear.
	persisted, err := llmcapture.ReadAllFromFile(capture.DumpPath())
	if err != nil {
		t.Fatalf("read dump: %v", err)
	}
	if len(persisted) != 1 {
		t.Fatalf("expected 1 persisted line, got %d", len(persisted))
	}
}
