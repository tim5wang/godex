package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/agent"
	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/llm"
	"github.com/tim5wang/godex/internal/core/protocol"
	"github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/commands"
	"github.com/tim5wang/godex/internal/services/usage"
	"gopkg.in/yaml.v3"
)

// =============================================================================
// Conversion unit tests
// =============================================================================

func responsesReqFromJSON(t *testing.T, body string) responsesGatewayRequest {
	t.Helper()
	var req responsesGatewayRequest
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("decode responses request: %v", err)
	}
	return req
}

func TestResponsesRequestToProtocolInstructionsToSystem(t *testing.T) {
	req := responsesReqFromJSON(t, `{
		"model": "M3",
		"instructions": "You are a coding agent.",
		"input": [
			{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "hi"}]}
		]
	}`)
	proto, err := responsesRequestToProtocol(req)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if proto.System != "You are a coding agent." {
		t.Fatalf("expected system from instructions, got %q", proto.System)
	}
	if len(proto.Messages) != 1 || proto.Messages[0].Role != protocol.RoleUser {
		t.Fatalf("expected one user message, got %#v", proto.Messages)
	}
	if got := protocol.BlocksText(proto.Messages[0].Content); got != "hi" {
		t.Fatalf("expected user text hi, got %q", got)
	}
}

func TestResponsesRequestToProtocolSystemMessageFlattened(t *testing.T) {
	req := responsesReqFromJSON(t, `{
		"model": "M3",
		"input": [
			{"type": "message", "role": "system", "content": [{"type": "input_text", "text": "be brief"}]},
			{"type": "message", "role": "user", "content": "what is 2+2?"}
		]
	}`)
	proto, err := responsesRequestToProtocol(req)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if proto.System != "be brief" {
		t.Fatalf("expected system from system message, got %q", proto.System)
	}
	if len(proto.Messages) != 1 || proto.Messages[0].Role != protocol.RoleUser {
		t.Fatalf("expected one user message after system flattening, got %#v", proto.Messages)
	}
}

func TestResponsesRequestToProtocolFunctionCallAndOutput(t *testing.T) {
	req := responsesReqFromJSON(t, `{
		"model": "M3",
		"input": [
			{"type": "message", "role": "user", "content": "read the file"},
			{"type": "function_call", "call_id": "call_1", "name": "read_file", "arguments": "{\"path\":\"/tmp/x\"}"},
			{"type": "function_call_output", "call_id": "call_1", "output": "file contents"}
		]
	}`)
	proto, err := responsesRequestToProtocol(req)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if len(proto.Messages) != 3 {
		t.Fatalf("expected 3 messages (user, assistant tool_use, user tool_result), got %#v", proto.Messages)
	}
	if proto.Messages[1].Role != protocol.RoleAssistant {
		t.Fatalf("expected assistant message for function_call, got %q", proto.Messages[1].Role)
	}
	toolUses := protocol.ToolUses(proto.Messages[1].Content)
	if len(toolUses) != 1 || toolUses[0].ID != "call_1" || toolUses[0].Name != "read_file" {
		t.Fatalf("expected one tool_use call_1/read_file, got %#v", proto.Messages[1].Content)
	}
	if toolUses[0].Input["path"] != "/tmp/x" {
		t.Fatalf("expected tool input path=/tmp/x, got %#v", toolUses[0].Input)
	}
	if proto.Messages[2].Role != protocol.RoleUser {
		t.Fatalf("expected user message for function_call_output, got %q", proto.Messages[2].Role)
	}
	var results []protocol.Block
	for _, b := range proto.Messages[2].Content {
		if b.Type == protocol.BlockToolResult {
			results = append(results, b)
		}
	}
	if len(results) != 1 || results[0].ToolUseID != "call_1" || results[0].Content != "file contents" {
		t.Fatalf("expected one tool_result call_1, got %#v", proto.Messages[2].Content)
	}
}

func TestResponsesRequestToProtocolSkipsUnknownItems(t *testing.T) {
	req := responsesReqFromJSON(t, `{
		"model": "M3",
		"input": [
			{"type": "reasoning", "id": "rs_1"},
			{"type": "message", "role": "user", "content": "hello"},
			{"type": "web_search_call", "id": "ws_1"}
		]
	}`)
	proto, err := responsesRequestToProtocol(req)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if len(proto.Messages) != 1 || proto.Messages[0].Role != protocol.RoleUser {
		t.Fatalf("expected only the user message to survive, got %#v", proto.Messages)
	}
}

func TestResponsesRequestToProtocolForwardsTools(t *testing.T) {
	req := responsesReqFromJSON(t, `{
		"model": "M3",
		"input": [{"type": "message", "role": "user", "content": "hi"}],
		"tools": [{"type": "function", "name": "bash", "description": "run a command",
			"parameters": {"type": "object", "properties": {"cmd": {"type": "string"}}, "required": ["cmd"]}}]
	}`)
	proto, err := responsesRequestToProtocol(req)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if len(proto.Tools) != 1 || proto.Tools[0].Name != "bash" || proto.Tools[0].Description != "run a command" {
		t.Fatalf("expected one bash tool forwarded, got %#v", proto.Tools)
	}
}

func TestResponsesRequestToProtocolForwardsMaxTokensAndRetention(t *testing.T) {
	req := responsesReqFromJSON(t, `{
		"model": "M3",
		"max_output_tokens": 4096,
		"prompt_cache_retention": "24h",
		"input": [{"type": "message", "role": "user", "content": "hi"}]
	}`)
	proto, err := responsesRequestToProtocol(req)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if proto.MaxTokens != 4096 {
		t.Fatalf("expected max_tokens 4096, got %d", proto.MaxTokens)
	}
	if proto.PromptCacheRetention != protocol.CacheRetentionLong {
		t.Fatalf("expected cache retention long, got %q", proto.PromptCacheRetention)
	}
}

func TestResponsesRequestToProtocolEmptyInputRejected(t *testing.T) {
	req := responsesReqFromJSON(t, `{"model": "M3", "input": [{"type": "reasoning"}]}`)
	if _, err := responsesRequestToProtocol(req); err == nil {
		t.Fatal("expected error for input with no usable message")
	}
}

// =============================================================================
// Outbound serialization tests
// =============================================================================

func TestProtocolToResponsesResponseMapsTextAndToolUse(t *testing.T) {
	resp := &protocol.Response{
		Content: []protocol.Block{
			protocol.ThinkingBlock("let me think", "", false),
			protocol.TextBlock("the answer"),
			protocol.ToolUseBlock("call_1", "bash", map[string]interface{}{"command": "pwd"}),
		},
		StopReason: "tool_use",
	}
	out := protocolToResponsesResponse(resp, "M3")
	if out["object"] != "response" {
		t.Fatalf("expected object=response, got %#v", out["object"])
	}
	output, ok := out["output"].([]map[string]interface{})
	if !ok {
		t.Fatalf("expected output array, got %#v", out["output"])
	}
	if len(output) != 3 {
		t.Fatalf("expected 3 output items, got %#v", output)
	}
	if output[0]["type"] != "reasoning" {
		t.Fatalf("expected reasoning first, got %#v", output[0])
	}
	if output[1]["type"] != "message" || output[1]["role"] != "assistant" {
		t.Fatalf("expected assistant message second, got %#v", output[1])
	}
	content, _ := output[1]["content"].([]map[string]interface{})
	if len(content) != 1 || content[0]["text"] != "the answer" {
		t.Fatalf("expected output_text the answer, got %#v", output[1]["content"])
	}
	if output[2]["type"] != "function_call" || output[2]["name"] != "bash" || output[2]["call_id"] != "call_1" {
		t.Fatalf("expected function_call bash, got %#v", output[2])
	}
	if got := out["status"]; got != "completed" {
		t.Fatalf("expected status completed for tool_use stop, got %#v", got)
	}
}

func TestProtocolToResponsesResponseMarksIncompleteOnLength(t *testing.T) {
	resp := &protocol.Response{
		Content:    []protocol.Block{protocol.TextBlock("partial")},
		StopReason: "length",
	}
	out := protocolToResponsesResponse(resp, "M3")
	if got := out["status"]; got != "incomplete" {
		t.Fatalf("expected status incomplete for length, got %#v", got)
	}
}

func TestProtocolUsageToResponsesAddsCachedBack(t *testing.T) {
	usage := protocolUsageToResponses(&protocol.Usage{
		InputTokens:     10,
		OutputTokens:    5,
		CacheReadTokens: 7,
	})
	if usage["input_tokens"] != 17 {
		t.Fatalf("expected input_tokens 17 (10+7 cached), got %#v", usage["input_tokens"])
	}
	if usage["output_tokens"] != 5 {
		t.Fatalf("expected output_tokens 5, got %#v", usage["output_tokens"])
	}
	details, _ := usage["input_tokens_details"].(map[string]interface{})
	if details["cached_tokens"] != 7 {
		t.Fatalf("expected cached_tokens 7, got %#v", details)
	}
}

// =============================================================================
// HTTP tests
// =============================================================================

// responsesStubSetup wires a stub Anthropic-compatible provider into the
// gateway and returns the ready handler + usage service + a created key.
func responsesStubSetup(t *testing.T, stubURL string) (http.Handler, *usage.Service, string) {
	t.Helper()
	cfg := newTestConfig(t)
	manager := newTestManager(t, cfg)

	providersValue, err := yaml.Marshal(map[string]llm.ProviderConfig{
		"stub": {
			ID:      "stub",
			Name:    "Stub",
			Type:    "anthropic_compatible",
			BaseURL: stubURL,
			APIKey:  "sk-test",
			Models: map[string]llm.ModelConfig{
				"stub-model": {Model: "stub-model"},
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal providers: %v", err)
	}
	if _, err := manager.Update(context.Background(), config.UpdateRequest{Values: map[string]any{
		"api.providers":       string(providersValue),
		"api.default_profile": "stub.stub-model",
		"api.default_model":   "stub.stub-model",
	}}); err != nil {
		t.Fatalf("update config: %v", err)
	}
	stubProfileID := manager.Current().DefaultProfileID
	if stubProfileID == "" {
		t.Fatalf("expected a default profile id after update, got empty")
	}

	store, err := usage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("new usage store: %v", err)
	}
	usageService := usage.NewService(store)
	if _, err := usageService.CreateModel(usage.ModelCreateRequest{
		PublicModel:     "M3",
		TargetProfileID: stubProfileID,
		TargetModel:     "stub-model",
		CreditWeight:    1,
	}); err != nil {
		t.Fatalf("create model mapping: %v", err)
	}
	created, err := usageService.CreateKey(usage.KeyCreateRequest{
		Name:          "responses",
		BudgetCredits: 100,
		AllowedModels: []string{"M3"},
	})
	if err != nil {
		t.Fatalf("create key: %v", err)
	}

	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, &dummyCaller{}), commands.NewService(cfg))
	handler := NewHandler(manager, service, nil, nil, nil, nil, usageService)
	return handler, usageService, created.Secret
}

func TestUsageGatewayResponsesNonStreaming(t *testing.T) {
	stubProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer stubProvider.Close()

	handler, _, secret := responsesStubSetup(t, stubProvider.URL)
	server := httptest.NewServer(handler)
	defer server.Close()

	body := `{"model":"M3","input":[{"type":"message","role":"user","content":"hi"}],"max_output_tokens":16}`
	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/responses", strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(respBody))
	}
	var out map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if out["object"] != "response" {
		t.Fatalf("expected object=response, got %#v", out["object"])
	}
	output, ok := out["output"].([]interface{})
	if !ok || len(output) != 1 {
		t.Fatalf("expected one output item, got %#v", out["output"])
	}
	item, _ := output[0].(map[string]interface{})
	if item["type"] != "message" || item["role"] != "assistant" {
		t.Fatalf("expected assistant message output, got %#v", item)
	}
	if _, ok := out["usage"].(map[string]interface{}); !ok {
		t.Fatalf("expected usage present, got %#v", out["usage"])
	}
}

func TestUsageGatewayResponsesStreamingSSE(t *testing.T) {
	stubProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"msg-1","role":"assistant","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`+"\n\n")
		_, _ = io.WriteString(w, `event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`+"\n\n")
		_, _ = io.WriteString(w, `event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hel"}}`+"\n\n")
		_, _ = io.WriteString(w, `event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"lo"}}`+"\n\n")
		_, _ = io.WriteString(w, `event: content_block_stop`+"\n"+`data: {"type":"content_block_stop","index":0}`+"\n\n")
		_, _ = io.WriteString(w, `event: message_delta`+"\n"+`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`+"\n\n")
		_, _ = io.WriteString(w, `event: message_stop`+"\n"+`data: {"type":"message_stop"}`+"\n\n")
	}))
	defer stubProvider.Close()

	handler, _, secret := responsesStubSetup(t, stubProvider.URL)
	server := httptest.NewServer(handler)
	defer server.Close()

	body := `{"model":"M3","stream":true,"input":[{"type":"message","role":"user","content":"hi"}]}`
	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/responses", strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200 with stream:true, got %d: %s", resp.StatusCode, string(respBody))
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("expected Content-Type text/event-stream, got %q", ct)
	}
	stream, err := readSSEDataLines(t, resp.Body)
	if err != nil {
		t.Fatalf("read sse: %v", err)
	}
	if !strings.Contains(stream, `"type":"response.output_text.delta"`) {
		t.Fatalf("expected response.output_text.delta in stream, got %q", stream)
	}
	if !strings.Contains(stream, `"delta":"hel"`) || !strings.Contains(stream, `"delta":"lo"`) {
		t.Fatalf("expected delta chunks hel+lo, got %q", stream)
	}
	if !strings.Contains(stream, `"type":"response.completed"`) {
		t.Fatalf("expected response.completed frame, got %q", stream)
	}
	if !strings.Contains(stream, "[DONE]") {
		t.Fatalf("expected [DONE] sentinel, got %q", stream)
	}
}

func TestUsageGatewayResponsesRejectsDisallowedModel(t *testing.T) {
	handler, usageService, _ := responsesStubSetup(t, "http://127.0.0.1:1")
	// Override: create a key that only allows a different model.
	store, err := usage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("new usage store: %v", err)
	}
	usageService2 := usage.NewService(store)
	_ = usageService2
	_ = usageService
	created, err := usageService.CreateKey(usage.KeyCreateRequest{
		Name:          "bad-model",
		BudgetCredits: 100,
		AllowedModels: []string{"OTHER"},
	})
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()

	body := `{"model":"M3","input":[{"type":"message","role":"user","content":"hi"}]}`
	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/responses", strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+created.Secret)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 403 for disallowed model, got %d: %s", resp.StatusCode, string(respBody))
	}
}

func TestUsageGatewayResponsesRejectsUnauthorized(t *testing.T) {
	handler, _, _ := responsesStubSetup(t, "http://127.0.0.1:1")
	server := httptest.NewServer(handler)
	defer server.Close()

	body := `{"model":"M3","input":[{"type":"message","role":"user","content":"hi"}]}`
	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/responses", strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer gdx_bogus")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 401 for bad key, got %d: %s", resp.StatusCode, string(respBody))
	}
}

func TestUsageGatewayResponsesWebTokenPath(t *testing.T) {
	stubProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer stubProvider.Close()

	handler, _, _ := responsesStubSetup(t, stubProvider.URL)
	// The stub setup uses a fresh config without a web token; re-create the
	// handler with a web token by wrapping the route through a config that
	// has one. Simplest: build a handler with web token via the existing
	// helper and re-inject the stub provider.
	_ = handler

	// Build a dedicated handler with a web token set.
	cfg := newTestConfig(t)
	cfg.WebToken = "web-tok-123"
	manager := newTestManager(t, cfg)
	providersValue, err := yaml.Marshal(map[string]llm.ProviderConfig{
		"stub": {
			ID:      "stub",
			Name:    "Stub",
			Type:    "anthropic_compatible",
			BaseURL: stubProvider.URL,
			APIKey:  "sk-test",
			Models: map[string]llm.ModelConfig{
				"stub-model": {Model: "stub-model"},
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal providers: %v", err)
	}
	if _, err := manager.Update(context.Background(), config.UpdateRequest{Values: map[string]any{
		"api.providers":       string(providersValue),
		"api.default_profile": "stub.stub-model",
		"api.default_model":   "stub.stub-model",
	}}); err != nil {
		t.Fatalf("update config: %v", err)
	}
	stubProfileID := manager.Current().DefaultProfileID
	store, err := usage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("new usage store: %v", err)
	}
	usageService := usage.NewService(store)
	if _, err := usageService.CreateModel(usage.ModelCreateRequest{
		PublicModel:     "M3",
		TargetProfileID: stubProfileID,
		TargetModel:     "stub-model",
		CreditWeight:    1,
	}); err != nil {
		t.Fatalf("create model mapping: %v", err)
	}
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, &dummyCaller{}), commands.NewService(cfg))
	handler = NewHandler(manager, service, nil, nil, nil, nil, usageService)

	server := httptest.NewServer(handler)
	defer server.Close()

	body := `{"model":"M3","input":[{"type":"message","role":"user","content":"hi"}]}`
	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/responses", strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer web-tok-123")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200 on web-token path, got %d: %s", resp.StatusCode, string(respBody))
	}
	var out map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if out["object"] != "response" {
		t.Fatalf("expected object=response, got %#v", out["object"])
	}
}
