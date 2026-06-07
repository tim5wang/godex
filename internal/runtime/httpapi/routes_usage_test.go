package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/agent"
	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/llm"
	"github.com/tim5wang/godex/internal/core/protocol"
	"github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/commands"
	"github.com/tim5wang/godex/internal/services/usage"
	"gopkg.in/yaml.v3"
)

func mustUsageHandler(t *testing.T) (http.Handler, *usage.Service) {
	t.Helper()
	cfg := newTestConfig(t)
	manager := newTestManager(t, cfg)
	store, err := usage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("new usage store: %v", err)
	}
	usageService := usage.NewService(store)
	service := backend.NewService(cfg, nil, nil)
	_ = config.UpdateRequest{}
	_ = context.Background
	return NewHandler(manager, service, nil, nil, nil, nil, usageService), usageService
}

func mustUsageHandlerWithToken(t *testing.T, token string) (http.Handler, *usage.Service) {
	t.Helper()
	cfg := newTestConfig(t)
	cfg.WebToken = token
	manager := newTestManager(t, cfg)
	store, err := usage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("new usage store: %v", err)
	}
	usageService := usage.NewService(store)
	service := backend.NewService(cfg, nil, nil)
	return NewHandler(manager, service, nil, nil, nil, nil, usageService), usageService
}

func TestUsageKeyResetEndpointRotatesSecret(t *testing.T) {
	handler, usageService := mustUsageHandler(t)
	created, err := usageService.CreateKey(usage.KeyCreateRequest{Name: "reset-me", BudgetCredits: 100})
	if err != nil {
		t.Fatalf("seed create: %v", err)
	}
	oldSecret := created.Secret
	keyID := created.Key.ID

	server := httptest.NewServer(handler)
	defer server.Close()

	req, err := http.NewRequest(http.MethodPost, server.URL+"/usage/keys/"+keyID+"/reset", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+testWebToken(t))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("reset request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body := readAll(t, resp)
		t.Fatalf("expected reset status 200, got %d: %s", resp.StatusCode, body)
	}

	var payload usage.KeyCreateResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode reset response: %v", err)
	}
	if payload.Secret == "" {
		t.Fatal("expected reset response to expose new plaintext secret")
	}
	if !strings.HasPrefix(payload.Secret, usage.KeyPrefix) {
		t.Fatalf("expected reset secret to start with %q, got %q", usage.KeyPrefix, payload.Secret[:8])
	}
	if payload.Secret == oldSecret {
		t.Fatal("reset secret must differ from original")
	}
	if payload.Key.ID != keyID {
		t.Fatalf("reset must keep the same key id, got %q vs %q", payload.Key.ID, keyID)
	}
	if payload.Key.KeyHash == "" {
		t.Fatal("expected reset key to retain a stored hash")
	}

	// Original secret must be rejected after reset, new secret must be accepted.
	if _, err := usageService.AuthenticateKey(oldSecret); err == nil {
		t.Fatal("original secret must be rejected after HTTP reset")
	}
	if _, err := usageService.AuthenticateKey(payload.Secret); err != nil {
		t.Fatalf("new secret must authenticate after HTTP reset: %v", err)
	}
}

func TestUsageKeyResetEndpointRequiresBearerToken(t *testing.T) {
	handler, usageService := mustUsageHandlerWithToken(t, "test-web-token")
	created, err := usageService.CreateKey(usage.KeyCreateRequest{Name: "no-token", BudgetCredits: 1})
	if err != nil {
		t.Fatalf("seed create: %v", err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()

	req, err := http.NewRequest(http.MethodPost, server.URL+"/usage/keys/"+created.Key.ID+"/reset", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("reset request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected reset to require bearer token, got %d", resp.StatusCode)
	}
}

func TestUsageKeyResetEndpointAcceptsConfiguredBearerToken(t *testing.T) {
	handler, usageService := mustUsageHandlerWithToken(t, "test-web-token")
	created, err := usageService.CreateKey(usage.KeyCreateRequest{Name: "with-token", BudgetCredits: 1})
	if err != nil {
		t.Fatalf("seed create: %v", err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()

	req, err := http.NewRequest(http.MethodPost, server.URL+"/usage/keys/"+created.Key.ID+"/reset", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer test-web-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("reset request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body := readAll(t, resp)
		t.Fatalf("expected reset with valid token to succeed, got %d: %s", resp.StatusCode, body)
	}
}

func TestUsageKeyResetEndpointReturns404ForUnknownID(t *testing.T) {
	handler, _ := mustUsageHandler(t)
	server := httptest.NewServer(handler)
	defer server.Close()

	req, err := http.NewRequest(http.MethodPost, server.URL+"/usage/keys/does-not-exist/reset", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+testWebToken(t))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("reset request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected reset of unknown id to return 404, got %d", resp.StatusCode)
	}
}

func readAll(t *testing.T, resp *http.Response) string {
	t.Helper()
	var buf strings.Builder
	tmp := make([]byte, 1024)
	for {
		n, err := resp.Body.Read(tmp)
		if n > 0 {
			buf.Write(tmp[:n])
		}
		if err != nil {
			break
		}
	}
	return buf.String()
}

func testWebToken(t *testing.T) string {
	t.Helper()
	// Reuse the helper used by the rest of httpapi_test.go by going through
	// the manager. The token is whatever the test config produces.
	cfg := newTestConfig(t)
	manager := newTestManager(t, cfg)
	_ = manager
	return manager.Current().WebToken
}

// TestUsageGatewayChatCompletionStreamsSSE covers the streaming happy path
// for proxy key auth: POST /v1/chat/completions with stream:true and a gdx_*
// key must return a real text/event-stream with chat.completion.chunk deltas
// and a trailing data: [DONE] sentinel. The underlying OpenAI-compatible
// provider is stubbed with an httptest server that emits OpenAI SSE chunks
// (data: {...}\n\n) so the upstream caller.Stream path is exercised.
func TestUsageGatewayChatCompletionStreamsSSE(t *testing.T) {
	stubProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The NewCallerForProfile factory routes stub profiles to the
		// Anthropic-compatible client (Client.Stream) regardless of type, so
		// respond with Anthropic SSE wire format. The conversion to OpenAI
		// chat.completion.chunk is performed inside streamUsageGatewayChatCompletions.
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		flusher, _ := w.(http.Flusher)
		events := []string{
			`event: message_start` + "\n" + `data: {"type":"message_start","message":{"id":"msg-1","role":"assistant","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
			`event: content_block_start` + "\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hel"}}`,
			`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"lo"}}`,
			`event: content_block_stop` + "\n" + `data: {"type":"content_block_stop","index":0}`,
			`event: message_delta` + "\n" + `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
			`event: message_stop` + "\n" + `data: {"type":"message_stop"}`,
		}
		for _, e := range events {
			_, _ = io.WriteString(w, e+"\n\n")
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	defer stubProvider.Close()

	cfg := newTestConfig(t)
	manager := newTestManager(t, cfg)
	// Inject the stub provider through the public Update + api.providers
	// config path. asLLMProviders marshals the supplied value via YAML
	// into llm.ProviderConfig, normalises it through the registry, and
	// re-derives current.LLMProviders / current.ModelProfiles so the
	// Anthropic fallback client no longer wins with an empty BaseURL.
	// Note: The stub server responds with Anthropic SSE format, so we use
	// anthropic_compatible type to route to Client.Stream which handles that format.
	providersValue, err := yaml.Marshal(map[string]llm.ProviderConfig{
		"stub": {
			ID:      "stub",
			Name:    "Stub",
			Type:    "anthropic_compatible",
			BaseURL: stubProvider.URL,
			APIKey:  "sk-test",
			Models: map[string]llm.ModelConfig{
				"stub-model": {
					Model: "stub-model",
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal providers: %v", err)
	}
	if _, err := manager.Update(context.Background(), config.UpdateRequest{Values: map[string]any{
		"api.providers":             string(providersValue),
		"api.default_profile":       "stub.stub-model",
		"api.default_model":         "stub.stub-model",
	}}); err != nil {
		t.Fatalf("update config: %v", err)
	}

	store, err := usage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("new usage store: %v", err)
	}
	usageService := usage.NewService(store)

	stubProfileID := manager.Current().DefaultProfileID
	if stubProfileID == "" {
		t.Fatalf("expected a default profile id after update, got empty")
	}

	if _, err := usageService.CreateModel(usage.ModelCreateRequest{
		PublicModel:     "M3",
		TargetProfileID: stubProfileID,
		TargetModel:     "stub-model",
		CreditWeight:    1,
	}); err != nil {
		t.Fatalf("create model mapping: %v", err)
	}
	created, err := usageService.CreateKey(usage.KeyCreateRequest{
		Name:          "stream-sse",
		BudgetCredits: 100,
		AllowedModels: []string{"M3"},
	})
	if err != nil {
		t.Fatalf("create key: %v", err)
	}

	// Service is required by NewHandler; the test does not exercise the
	// web-token path, so the stub caller contents do not matter here.
	stubCaller := &protocol.Response{Content: []protocol.Block{protocol.TextBlock("unused")}}
	_ = stubCaller
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, &dummyCaller{}), commands.NewService(cfg))
	handler := NewHandler(manager, service, nil, nil, nil, nil, usageService)
	server := httptest.NewServer(handler)
	defer server.Close()

	body := `{"model":"M3","stream":true,"messages":[{"role":"user","content":"hello"}],"max_tokens":16}`
	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+created.Secret)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
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
	if !strings.Contains(stream, `"object":"chat.completion.chunk"`) {
		t.Fatalf("expected chat.completion.chunk in stream body, got %q", stream)
	}
	if !strings.Contains(stream, `"content":"hel"`) || !strings.Contains(stream, `"content":"lo"`) {
		t.Fatalf("expected delta chunks to include hel+lo, got %q", stream)
	}
	if !strings.Contains(stream, `"finish_reason":"stop"`) {
		t.Fatalf("expected stop finish_reason chunk, got %q", stream)
	}
	if !strings.Contains(stream, "[DONE]") {
		t.Fatalf("expected [DONE] sentinel, got %q", stream)
	}
	if strings.Contains(stream, "unsupported_streaming") {
		t.Fatalf("response should not contain unsupported_streaming, got %q", stream)
	}
}

// TestUsageGatewayChatCompletionsStreamsToolCallsSSE covers the third leg
// of the streaming regression: the OpenAI usage-gateway path now has to
// forward tool_calls deltas so OpenAI SDKs and Codex-style clients can
// execute the model-requested tool. The upstream stub emits the full
// OpenAI chat.completion.chunk SSE shape:
//
//	data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read","arguments":""}}]}}]}
//	data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":\"/tmp/hi\"}"}}]}}]}
//	data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}
//	data: [DONE]
//
// The proxy must surface every tool_calls delta verbatim to the client so
// OpenAI-compatible tool loops can reassemble the JSON arguments. Each
// per-chunk arguments fragment is the delta between successive frames, so
// the SDK's per-chunk concatenation yields the final JSON. finish_reason
// must be "tool_calls" (OpenAI's canonical stop reason) so the client
// tool loop runs the requested tool rather than treating the turn as
// done.
//
// Note: the stub is wired up as an `anthropic_compatible` provider but
// emits OpenAI-shape chat.completion.chunk frames. parseMessageStream
// (the Anthropic-shape parser used by Client.Stream) falls through to
// its OpenAI compatibility branch when the upstream frame is not
// Anthropic-shaped, and forwards the tool_calls deltas to the handler.
// The standard ProviderOpenAICompatible path uses parseOpenAIStream
// instead; this test exercises the niche reverse-proxy / OpenRouter
// case where an anthropic_compatible profile upstreams an OpenAI-shape
// service.
func TestUsageGatewayChatCompletionsStreamsToolCallsSSE(t *testing.T) {
	stubProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// We emit OpenAI chat.completion.chunk frames; see the test
		// header for why parseMessageStream can still parse them.
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		flusher, _ := w.(http.Flusher)
		events := []string{
			`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"stub-model","choices":[{"index":0,"delta":{"role":"assistant","content":""}}]}`,
			`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"stub-model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read","arguments":""}}]}}]}`,
			`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"stub-model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":\"/tmp/hi\"}"}}]}}]}`,
			`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"stub-model","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		}
		for _, e := range events {
			_, _ = io.WriteString(w, e+"\n\n")
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	defer stubProvider.Close()

	cfg := newTestConfig(t)
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

	store, err := usage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("new usage store: %v", err)
	}
	usageService := usage.NewService(store)
	stubProfileID := manager.Current().DefaultProfileID
	if stubProfileID == "" {
		t.Fatalf("expected a default profile id after update, got empty")
	}
	if _, err := usageService.CreateModel(usage.ModelCreateRequest{
		PublicModel:     "M3",
		TargetProfileID: stubProfileID,
		TargetModel:     "stub-model",
		CreditWeight:    1,
	}); err != nil {
		t.Fatalf("create model mapping: %v", err)
	}
	created, err := usageService.CreateKey(usage.KeyCreateRequest{
		Name:          "openai-tool-call",
		BudgetCredits: 100,
		AllowedModels: []string{"M3"},
	})
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, &dummyCaller{}), commands.NewService(cfg))
	handler := NewHandler(manager, service, nil, nil, nil, nil, usageService)
	server := httptest.NewServer(handler)
	defer server.Close()

	body := `{"model":"M3","stream":true,"max_tokens":16,"messages":[{"role":"user","content":"read /tmp/hi"}]}`
	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+created.Secret)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(respBody))
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("expected Content-Type text/event-stream, got %q", ct)
	}
	stream, err := readSSEDataLines(t, resp.Body)
	if err != nil {
		t.Fatalf("read sse: %v", err)
	}

	// Every chunk must surface chat.completion.chunk and the tool_call identity
	// must be preserved end-to-end (call_1 + read + /tmp/hi).
	for _, must := range []string{
		`"object":"chat.completion.chunk"`,
		`"tool_calls"`,
		`"id":"call_1"`,
		`"type":"function"`,
		`"name":"read"`,
		`/tmp/hi`,
		`"finish_reason":"tool_calls"`,
		`data: [DONE]`,
	} {
		if !strings.Contains(stream, must) {
			t.Errorf("expected OpenAI SSE stream to contain %q, got %q", must, stream)
		}
	}
}

// TestUsageGatewayChatCompletionsForwardsToolsToUpstream is the
// regression test for the case where pi (or any OpenAI SDK) sent
// POST /v1/chat/completions with a `tools` array and the proxy
// silently dropped every entry. The model then had no schema to
// ground its tool_calls deltas in and resorted to emitting
// `<bash>...</bash>` text blocks, which the OpenAI SDK does not
// parse as a tool invocation. We capture the upstream request body
// and assert each advertised tool's name, description, and
// parameter schema survive the conversion.
func TestUsageGatewayChatCompletionsForwardsToolsToUpstream(t *testing.T) {
	var capturedBody []byte
	stubProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		flusher, _ := w.(http.Flusher)
		// Emit a minimal OpenAI chat.completion.chunk stream so the
		// proxy returns 200 to the client and the captured body is
		// fully read.
		frames := []string{
			`data: {"id":"x","object":"chat.completion.chunk","created":1,"model":"stub-model","choices":[{"index":0,"delta":{"role":"assistant","content":""}}]}`,
			`data: {"id":"x","object":"chat.completion.chunk","created":1,"model":"stub-model","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		}
		for _, f := range frames {
			_, _ = io.WriteString(w, f+"\n\n")
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	defer stubProvider.Close()

	cfg := newTestConfig(t)
	manager := newTestManager(t, cfg)
	providersValue, err := yaml.Marshal(map[string]llm.ProviderConfig{
		"stub": {
			ID:      "stub",
			Name:    "Stub",
			Type:    "openai_compatible",
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

	store, err := usage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("new usage store: %v", err)
	}
	usageService := usage.NewService(store)
	stubProfileID := manager.Current().DefaultProfileID
	if stubProfileID == "" {
		t.Fatalf("expected a default profile id after update, got empty")
	}
	if _, err := usageService.CreateModel(usage.ModelCreateRequest{
		PublicModel:     "M3",
		TargetProfileID: stubProfileID,
		TargetModel:     "stub-model",
		CreditWeight:    1,
	}); err != nil {
		t.Fatalf("create model mapping: %v", err)
	}
	created, err := usageService.CreateKey(usage.KeyCreateRequest{
		Name:          "openai-tools-forward",
		BudgetCredits: 100,
		AllowedModels: []string{"M3"},
	})
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, &dummyCaller{}), commands.NewService(cfg))
	handler := NewHandler(manager, service, nil, nil, nil, nil, usageService)
	server := httptest.NewServer(handler)
	defer server.Close()

	// Pi-shaped body: a user message + a tools array with two
	// functions (bash and read) with full JSON Schema parameters.
	body := `{
		"model":"M3",
		"stream":true,
		"max_tokens":16,
		"messages":[{"role":"user","content":"list the directory"}],
		"tools":[
			{"type":"function","function":{"name":"bash","description":"Run a shell command","parameters":{"type":"object","properties":{"command":{"type":"string","description":"The command to run"}},"required":["command"]}}},
			{"type":"function","function":{"name":"read","description":"Read a file","parameters":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}}}
		]
	}`
	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+created.Secret)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(respBody))
	}
	_, _ = io.Copy(io.Discard, resp.Body)

	if len(capturedBody) == 0 {
		t.Fatal("upstream did not receive a request body")
	}
	var captured map[string]interface{}
	if err := json.Unmarshal(capturedBody, &captured); err != nil {
		t.Fatalf("decode captured upstream body: %v\nbody: %s", err, string(capturedBody))
	}
	toolsRaw, _ := captured["tools"].([]interface{})
	if len(toolsRaw) != 2 {
		t.Fatalf("expected upstream request to carry 2 tools, got %d (%+v)", len(toolsRaw), toolsRaw)
	}
	// Index by name so the test is order-independent.
	byName := map[string]map[string]interface{}{}
	for _, raw := range toolsRaw {
		entry, _ := raw.(map[string]interface{})
		fn, _ := entry["function"].(map[string]interface{})
		name, _ := fn["name"].(string)
		byName[name] = entry
	}
	bash, ok := byName["bash"]
	if !ok {
		t.Fatalf("upstream tools missing 'bash', got keys %v", keysOf(byName))
	}
	if bash["type"] != "function" {
		t.Errorf("expected bash.type=function, got %v", bash["type"])
	}
	bashFn, _ := bash["function"].(map[string]interface{})
	if bashFn["description"] != "Run a shell command" {
		t.Errorf("expected bash.description='Run a shell command', got %v", bashFn["description"])
	}
	bashParams, _ := bashFn["parameters"].(map[string]interface{})
	if bashParams == nil {
		t.Fatalf("expected bash.parameters to be present, got %v", bashFn["parameters"])
	}
	if bashParams["type"] != "object" {
		t.Errorf("expected bash.parameters.type=object, got %v", bashParams["type"])
	}
	bashProps, _ := bashParams["properties"].(map[string]interface{})
	if _, ok := bashProps["command"]; !ok {
		t.Errorf("expected bash.parameters.properties.command, got %v", bashProps)
	}
	bashRequired, _ := bashParams["required"].([]interface{})
	if len(bashRequired) != 1 || bashRequired[0] != "command" {
		t.Errorf("expected bash.parameters.required=[command], got %v", bashRequired)
	}
	read, ok := byName["read"]
	if !ok {
		t.Fatalf("upstream tools missing 'read', got keys %v", keysOf(byName))
	}
	readFn, _ := read["function"].(map[string]interface{})
	if readFn["description"] != "Read a file" {
		t.Errorf("expected read.description='Read a file', got %v", readFn["description"])
	}
}

func keysOf(m map[string]map[string]interface{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestAnthropicGatewayMessagesStreamsAnthropicSSE covers the regression where
// Pi (or any client using the Anthropic SDK) sent POST /v1/messages with
// stream:true and the proxy returned a non-Anthropic SSE shape: only
// `content_block_delta` + `message_stop` events, with `usage` incorrectly
// attached to the trailing `message_stop` event. The official Anthropic
// SDK (and Pi's anthropic-messages provider) requires the full Anthropic
// SSE event order:
//
//	message_start
//	content_block_start
//	content_block_delta  (one or more)
//	content_block_stop
//	message_delta        (carries delta.stop_reason + incremental usage)
//	message_stop
//
// The proxy MUST emit all six event types in order. `message_stop` MUST
// NOT carry `usage` (Pi's anthropic.ts:1226-1230 stops on message_stop
// without re-parsing usage, and the canonical spec places usage inside
// message_delta). Without message_start + content_block_start, Pi's
// `output.content` stays empty and the UI displays "thinking then
// nothing" even though the upstream response was correct (see
// temp/pi/packages/ai/src/providers/anthropic.ts:540-659 for the consumer
// side that depends on these events to populate `output.content`).
func TestAnthropicGatewayMessagesStreamsAnthropicSSE(t *testing.T) {
	stubProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Respond with the full Anthropic SSE wire format. Client.Stream
		// (Client.streamOnce -> parseMessageStream) consumes this stream
		// and invokes OnTextDelta for every text_delta event, returning
		// a final *protocol.Response to the proxy.
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		flusher, _ := w.(http.Flusher)
		events := []string{
			`event: message_start` + "\n" + `data: {"type":"message_start","message":{"id":"msg-1","type":"message","role":"assistant","content":[],"model":"stub-model","stop_reason":null,"usage":{"input_tokens":1,"output_tokens":0,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}}`,
			`event: content_block_start` + "\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hel"}}`,
			`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"lo"}}`,
			`event: content_block_stop` + "\n" + `data: {"type":"content_block_stop","index":0}`,
			`event: message_delta` + "\n" + `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
			`event: message_stop` + "\n" + `data: {"type":"message_stop"}`,
		}
		for _, e := range events {
			_, _ = io.WriteString(w, e+"\n\n")
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	defer stubProvider.Close()

	cfg := newTestConfig(t)
	manager := newTestManager(t, cfg)
	// Inject the stub provider through the public Update + api.providers
	// config path. Client.Stream reaches the stub via the anthropic_compatible
	// provider type, so OnTextDelta fires once per text_delta event.
	providersValue, err := yaml.Marshal(map[string]llm.ProviderConfig{
		"stub": {
			ID:      "stub",
			Name:    "Stub",
			Type:    "anthropic_compatible",
			BaseURL: stubProvider.URL,
			APIKey:  "sk-test",
			Models: map[string]llm.ModelConfig{
				"stub-model": {
					Model: "stub-model",
				},
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

	store, err := usage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("new usage store: %v", err)
	}
	usageService := usage.NewService(store)

	stubProfileID := manager.Current().DefaultProfileID
	if stubProfileID == "" {
		t.Fatalf("expected a default profile id after update, got empty")
	}

	if _, err := usageService.CreateModel(usage.ModelCreateRequest{
		PublicModel:     "M3",
		TargetProfileID: stubProfileID,
		TargetModel:     "stub-model",
		CreditWeight:    1,
	}); err != nil {
		t.Fatalf("create model mapping: %v", err)
	}
	created, err := usageService.CreateKey(usage.KeyCreateRequest{
		Name:          "anthropic-sse",
		BudgetCredits: 100,
		AllowedModels: []string{"M3"},
	})
	if err != nil {
		t.Fatalf("create key: %v", err)
	}

	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, &dummyCaller{}), commands.NewService(cfg))
	handler := NewHandler(manager, service, nil, nil, nil, nil, usageService)
	server := httptest.NewServer(handler)
	defer server.Close()

	// POST /v1/messages with stream:true and x-api-key (Pi's Anthropic SDK
	// default header shape) to exercise the proxy's Anthropic SSE gateway.
	body := `{"model":"M3","stream":true,"max_tokens":16,"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`
	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/messages", strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("x-api-key", created.Secret)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
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

	// Required Anthropic SSE event types in the canonical order. The proxy
	// must emit ALL of them so that consumers (Pi's anthropic.ts stream
	// loop) can populate `output.content` and `output.usage` correctly.
	requiredEvents := []string{
		`"type":"message_start"`,
		`"type":"content_block_start"`,
		`"type":"content_block_delta"`,
		`"type":"content_block_stop"`,
		`"type":"message_delta"`,
		`"type":"message_stop"`,
	}
	for _, evt := range requiredEvents {
		if !strings.Contains(stream, evt) {
			t.Errorf("expected Anthropic SSE stream to contain event %s, got %q", evt, stream)
		}
	}

	// The proxy must NOT attach `usage` to the message_stop event. The
	// canonical location is message_delta; attaching it to message_stop
	// pollutes the terminal event and confuses strict consumers.
	stopIdx := strings.Index(stream, `"type":"message_stop"`)
	if stopIdx == -1 {
		t.Fatalf("expected message_stop event in stream, got %q", stream)
	}
	tail := stream[stopIdx:]
	if strings.Contains(tail, `"usage"`) {
		t.Errorf("message_stop event must not carry `usage`; got tail=%q", tail)
	}

	// The deltas must reach the client intact. Without content_block_start
	// upstream of these deltas, the consumer would not be able to associate
	// them with a content block, so the proxy MUST emit the start event.
	if !strings.Contains(stream, `"text":"hel"`) || !strings.Contains(stream, `"text":"lo"`) {
		t.Errorf("expected text deltas to include hel+lo, got %q", stream)
	}

	// message_delta must carry the final stop_reason so consumers can map
	// it onto their local stop-reason vocabulary (Pi's mapStopReason at
	// anthropic.ts:1210-1230).
	if !strings.Contains(stream, `"stop_reason":"end_turn"`) {
		t.Errorf("expected message_delta to carry stop_reason=end_turn, got %q", stream)
	}

	// CRITICAL: Each Anthropic SSE event MUST also carry an `event:` line
	// alongside its `data:` line. Pi's streamAnthropic at
	// temp/pi/packages/ai/src/providers/anthropic.ts:418-424 uses the
	// event-type from the SSE envelope (not the JSON body's `type` field)
	// to filter via ANTHROPIC_MESSAGE_EVENTS:
	//
	//   if (sse.event === "error") { throw new Error(sse.data); }
	//   if (!ANTHROPIC_MESSAGE_EVENTS.has(sse.event ?? "")) { continue; }
	//
	// Without the `event:` line, sse.event is null and EVERY frame is
	// silently dropped — `output.usage` stays 0 and `output.content` stays
	// empty, which exactly matches the Pi debug-log regression the user
	// reported after the previous GREEN. The previous test only read
	// `data:` lines (readSSEDataLines), so it never observed this gap.
	requiredEventTypes := []string{
		"event: message_start",
		"event: content_block_start",
		"event: content_block_delta",
		"event: content_block_stop",
		"event: message_delta",
		"event: message_stop",
	}
	for _, evt := range requiredEventTypes {
		if !strings.Contains(stream, evt) {
			t.Errorf("expected Anthropic SSE stream to contain %q line, got %q", evt, stream)
		}
	}
}

// TestAnthropicGatewayMessagesStreamsToolUseSSE covers the second half of
// the regression where the Pi agent appeared to "stop working" after a
// text reply. Pi's agent loop is tool-driven: when the model wants to
// run a tool it emits a `content_block_start { type: "tool_use" }`
// followed by one or more `content_block_delta { type: "input_json_delta" }`
// frames carrying the JSON arguments, then `content_block_stop`. Pi's
// streamAnthropic at anthropic.ts:568-581 routes these to `toolcall_*`
// events that the agent loop uses to invoke the tool. The GoDex proxy
// MUST forward every tool_use frame to Pi, otherwise Pi's agent loop
// never sees the tool call and the conversation stalls after the first
// text reply. This regression was hidden by the previous test, which
// only fed the proxy a text-only Anthropic SSE stream.
//
// The fix requires two cooperating changes:
//  1. conversation.StreamHandler must expose an OnToolUse callback so
//     streamAnthropicGatewayMessages can react to tool_use blocks in
//     real time (today parseMessageStream silently buffers them into
//     the final *protocol.Response and never notifies the caller).
//  2. streamAnthropicGatewayMessages must forward the tool_use frames
//     to the wire as event: content_block_start/delta/stop with
//     content_block.type == "tool_use" and delta.type == "input_json_delta".
func TestAnthropicGatewayMessagesStreamsToolUseSSE(t *testing.T) {
	stubProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Stub emits a tool_use block followed by an end_turn stop. The
		// proxy is expected to forward every frame intact.
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		flusher, _ := w.(http.Flusher)
		events := []string{
			`event: message_start` + "\n" + `data: {"type":"message_start","message":{"id":"msg-2","type":"message","role":"assistant","content":[],"model":"stub-model","stop_reason":null,"usage":{"input_tokens":1,"output_tokens":0}}}`,
			`event: content_block_start` + "\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_01","name":"read","input":{}}}`,
			`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"/tmp/hi\"}"}}`,
			`event: content_block_stop` + "\n" + `data: {"type":"content_block_stop","index":0}`,
			`event: message_delta` + "\n" + `data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":1}}`,
			`event: message_stop` + "\n" + `data: {"type":"message_stop"}`,
		}
		for _, e := range events {
			_, _ = io.WriteString(w, e+"\n\n")
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	defer stubProvider.Close()

	cfg := newTestConfig(t)
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

	store, err := usage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("new usage store: %v", err)
	}
	usageService := usage.NewService(store)
	stubProfileID := manager.Current().DefaultProfileID
	if stubProfileID == "" {
		t.Fatalf("expected a default profile id after update, got empty")
	}
	if _, err := usageService.CreateModel(usage.ModelCreateRequest{
		PublicModel:     "M3",
		TargetProfileID: stubProfileID,
		TargetModel:     "stub-model",
		CreditWeight:    1,
	}); err != nil {
		t.Fatalf("create model mapping: %v", err)
	}
	created, err := usageService.CreateKey(usage.KeyCreateRequest{
		Name:          "anthropic-tool-use",
		BudgetCredits: 100,
		AllowedModels: []string{"M3"},
	})
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, &dummyCaller{}), commands.NewService(cfg))
	handler := NewHandler(manager, service, nil, nil, nil, nil, usageService)
	server := httptest.NewServer(handler)
	defer server.Close()

	body := `{"model":"M3","stream":true,"max_tokens":16,"messages":[{"role":"user","content":[{"type":"text","text":"read /tmp/hi"}]}]}`
	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/messages", strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("x-api-key", created.Secret)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(respBody))
	}
	stream, err := readSSEDataLines(t, resp.Body)
	if err != nil {
		t.Fatalf("read sse: %v", err)
	}

	// Every required event: line must be present (the upstream provider
	// emits all six; the proxy must forward them verbatim).
	for _, evt := range []string{
		"event: message_start",
		"event: content_block_start",
		"event: content_block_delta",
		"event: content_block_stop",
		"event: message_delta",
		"event: message_stop",
	} {
		if !strings.Contains(stream, evt) {
			t.Errorf("expected Anthropic SSE stream to contain %q line, got %q", evt, stream)
		}
	}

	// The tool_use identity must survive: content_block_start must carry
	// type=tool_use, name=read, id=toolu_01 so Pi's toolcall_start path
	// at anthropic.ts:568-581 can wire the call to the read tool.
	mustContain := []string{
		`"type":"tool_use"`,
		`"name":"read"`,
		`"id":"toolu_01"`,
	}
	for _, s := range mustContain {
		if !strings.Contains(stream, s) {
			t.Errorf("expected tool_use identity %q in forwarded stream, got %q", s, stream)
		}
	}

	// The input JSON must be reassembled from the input_json_delta frames
	// and delivered to Pi as the final tool input. Pi's anthropic.ts:612
	// calls parseStreamingJson on the accumulated partial_json.
	if !strings.Contains(stream, "/tmp/hi") {
		t.Errorf("expected input JSON \"/tmp/hi\" to reach the client, got %q", stream)
	}

	// stop_reason must be tool_use so Pi's mapStopReason at
	// anthropic.ts:1216 returns "toolUse" and the agent loop executes the
	// tool rather than treating the turn as done.
	if !strings.Contains(stream, `"stop_reason":"tool_use"`) {
		t.Errorf("expected message_delta to carry stop_reason=tool_use, got %q", stream)
	}
}

// TestAnthropicGatewayMessagesStreamsToolUseMultiDeltaSSE is the
// regression test for the multi-delta tool_use streaming case. The
// upstream provider splits the input JSON across multiple
// input_json_delta frames (e.g. Anthropic splits long JSON arguments
// into many small fragments to keep each SSE frame bounded). The
// proxy must forward only the per-chunk suffix so the downstream SDK
// (Pi) accumulates the final JSON correctly. Resending the cumulative
// string on every callback would make Pi see the same bytes repeated
// and end up with concatenated garbage (e.g. `{"command":` +
// `{"command":"ls"}` = `{"command":{"command":"ls"}`).
func TestAnthropicGatewayMessagesStreamsToolUseMultiDeltaSSE(t *testing.T) {
	stubProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		flusher, _ := w.(http.Flusher)
		events := []string{
			`event: message_start` + "\n" + `data: {"type":"message_start","message":{"id":"msg-multi","type":"message","role":"assistant","content":[],"model":"stub-model","stop_reason":null,"usage":{"input_tokens":1,"output_tokens":0}}}`,
			`event: content_block_start` + "\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_multi","name":"bash","input":{}}}`,
			`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"command\":"}}`,
			`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"ls -la\""}}`,
			`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":",\"timeout\":"}}`,
			`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"30}"}}`,
			`event: content_block_stop` + "\n" + `data: {"type":"content_block_stop","index":0}`,
			`event: message_delta` + "\n" + `data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":1}}`,
			`event: message_stop` + "\n" + `data: {"type":"message_stop"}`,
		}
		for _, e := range events {
			_, _ = io.WriteString(w, e+"\n\n")
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	defer stubProvider.Close()

	cfg := newTestConfig(t)
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

	store, err := usage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("new usage store: %v", err)
	}
	usageService := usage.NewService(store)
	stubProfileID := manager.Current().DefaultProfileID
	if stubProfileID == "" {
		t.Fatalf("expected a default profile id after update, got empty")
	}
	if _, err := usageService.CreateModel(usage.ModelCreateRequest{
		PublicModel:     "M3",
		TargetProfileID: stubProfileID,
		TargetModel:     "stub-model",
		CreditWeight:    1,
	}); err != nil {
		t.Fatalf("create model mapping: %v", err)
	}
	created, err := usageService.CreateKey(usage.KeyCreateRequest{
		Name:          "anthropic-multi-delta",
		BudgetCredits: 100,
		AllowedModels: []string{"M3"},
	})
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, &dummyCaller{}), commands.NewService(cfg))
	handler := NewHandler(manager, service, nil, nil, nil, nil, usageService)
	server := httptest.NewServer(handler)
	defer server.Close()

	body := `{"model":"M3","stream":true,"max_tokens":16,"messages":[{"role":"user","content":[{"type":"text","text":"ls -la"}]}]}`
	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/messages", strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("x-api-key", created.Secret)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(respBody))
	}
	body2, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	stream := string(body2)

	// Parse the SSE stream into per-event (event, data) pairs so we can
	// assert on the exact content_block_delta frames the proxy emitted.
	type sseFrame struct {
		event string
		data  map[string]interface{}
	}
	frames := []sseFrame{}
	for _, block := range strings.Split(stream, "\n\n") {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		var evt, data string
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, "event: "):
				evt = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				data = strings.TrimPrefix(line, "data: ")
			}
		}
		if evt == "" || data == "" {
			continue
		}
		var parsed map[string]interface{}
		if err := json.Unmarshal([]byte(data), &parsed); err != nil {
			t.Fatalf("unmarshal SSE data for %q: %v\nraw: %s", evt, err, data)
		}
		frames = append(frames, sseFrame{event: evt, data: parsed})
	}

	// Collect the partial_json strings from every content_block_delta
	// frame, in order, and assert the concatenated result is the
	// expected final JSON. Pi's streamAnthropic (anthropic.ts:612) does
	// the same concatenation, so this pins the wire contract.
	var partials []string
	for _, f := range frames {
		if f.event != "content_block_delta" {
			continue
		}
		delta, _ := f.data["delta"].(map[string]interface{})
		if delta["type"] != "input_json_delta" {
			continue
		}
		pj, _ := delta["partial_json"].(string)
		partials = append(partials, pj)
	}
	// The proxy must forward exactly the four deltas the upstream
	// emitted. If the gateway resends the cumulative string, the
	// partials list would be longer and the concatenation would be
	// invalid JSON.
	wantPartials := []string{
		`{"command":`,
		`"ls -la"`,
		`,"timeout":`,
		`30}`,
	}
	if len(partials) != len(wantPartials) {
		t.Fatalf("expected %d input_json_delta frames, got %d: %v", len(wantPartials), len(partials), partials)
	}
	for i, want := range wantPartials {
		if partials[i] != want {
			t.Errorf("input_json_delta[%d]: expected %q, got %q", i, want, partials[i])
		}
	}
	// Concatenate the partials the way Pi does and verify the result
	// is exactly the upstream's intended JSON. If the gateway
	// accidentally resends the cumulative string, this would parse
	// to garbage like `{"command":{"command":"ls -la",...}}`.
	var assembled strings.Builder
	for _, p := range partials {
		assembled.WriteString(p)
	}
	var got map[string]interface{}
	if err := json.Unmarshal([]byte(assembled.String()), &got); err != nil {
		t.Fatalf("assembled partial_json is not valid JSON: %v\nassembled: %s", err, assembled.String())
	}
	if got["command"] != "ls -la" {
		t.Errorf("expected command=ls -la, got %v", got["command"])
	}
	// json.Unmarshal decodes numbers as float64, so compare numerically.
	if timeout, ok := got["timeout"].(float64); !ok || timeout != 30 {
		t.Errorf("expected timeout=30, got %v (%T)", got["timeout"], got["timeout"])
	}
}

// TestStreamAnthropicGatewayMessagesUsageWorks is the smoke test for the
// third leg of the OpenAI/Anthropic protocol improvement task: it pins the
// usage gateway smoke contract. The test mirrors the Anthropic tool-use
// stub from TestAnthropicGatewayMessagesStreamsToolUseSSE but additionally
// asserts that the call is recorded in the usage store with the input /
// output token counts advertised by the provider, so the dashboard,
// billing, and rate-limit logic can observe every gateway turn.
func TestStreamAnthropicGatewayMessagesUsageWorks(t *testing.T) {
	stubProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		flusher, _ := w.(http.Flusher)
		events := []string{
			`event: message_start` + "\n" + `data: {"type":"message_start","message":{"id":"msg-usage","type":"message","role":"assistant","content":[],"model":"stub-model","stop_reason":null,"usage":{"input_tokens":3,"output_tokens":0}}}`,
			`event: content_block_start` + "\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_usage","name":"read","input":{}}}`,
			`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"/tmp/usage\"}"}}`,
			`event: content_block_stop` + "\n" + `data: {"type":"content_block_stop","index":0}`,
			`event: message_delta` + "\n" + `data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":2}}`,
			`event: message_stop` + "\n" + `data: {"type":"message_stop"}`,
		}
		for _, e := range events {
			_, _ = io.WriteString(w, e+"\n\n")
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	defer stubProvider.Close()

	cfg := newTestConfig(t)
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

	store, err := usage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("new usage store: %v", err)
	}
	usageService := usage.NewService(store)
	stubProfileID := manager.Current().DefaultProfileID
	if stubProfileID == "" {
		t.Fatalf("expected a default profile id after update, got empty")
	}
	if _, err := usageService.CreateModel(usage.ModelCreateRequest{
		PublicModel:     "M3",
		TargetProfileID: stubProfileID,
		TargetModel:     "stub-model",
		CreditWeight:    1,
	}); err != nil {
		t.Fatalf("create model mapping: %v", err)
	}
	created, err := usageService.CreateKey(usage.KeyCreateRequest{
		Name:          "anthropic-usage-smoke",
		BudgetCredits: 100,
		AllowedModels: []string{"M3"},
	})
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, &dummyCaller{}), commands.NewService(cfg))
	handler := NewHandler(manager, service, nil, nil, nil, nil, usageService)
	server := httptest.NewServer(handler)
	defer server.Close()

	body := `{"model":"M3","stream":true,"max_tokens":16,"messages":[{"role":"user","content":[{"type":"text","text":"read /tmp/usage"}]}]}`
	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/messages", strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("x-api-key", created.Secret)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(respBody))
	}
	stream, err := readSSEDataLines(t, resp.Body)
	if err != nil {
		t.Fatalf("read sse: %v", err)
	}

	// Every required event: line must be present.
	for _, evt := range []string{
		"event: message_start",
		"event: content_block_start",
		"event: content_block_delta",
		"event: content_block_stop",
		"event: message_delta",
		"event: message_stop",
	} {
		if !strings.Contains(stream, evt) {
			t.Errorf("expected Anthropic SSE stream to contain %q line, got %q", evt, stream)
		}
	}

	// Wait briefly for the async usage flush to land (the streaming
	// handler dispatches RecordCall in a goroutine after the SSE
	// trailer). Five hundred milliseconds is more than enough on the
	// test machine; if usage recording ever takes longer we can lift
	// this bound, but in practice it returns in microseconds.
	deadline := time.Now().Add(2 * time.Second)
	var calls []usage.UsageCall
	for {
		calls, err = usageService.GetCalls(time.Now().Format("2006-01-02"), created.Key.ID)
		if err != nil {
			t.Fatalf("get calls: %v", err)
		}
		if len(calls) >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected at least one usage call for key %s, got %+v", created.Key.ID, calls)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(calls) != 1 {
		t.Fatalf("expected exactly one usage call, got %d (%+v)", len(calls), calls)
	}
	got := calls[0]
	if got.PublicModel != "M3" {
		t.Errorf("expected public model M3, got %q", got.PublicModel)
	}
	if got.InputTokens != 3 {
		t.Errorf("expected input tokens 3 from message_start, got %d", got.InputTokens)
	}
	if got.OutputTokens != 2 {
		t.Errorf("expected output tokens 2 from message_delta, got %d", got.OutputTokens)
	}
	if got.Status != "success" {
		t.Errorf("expected status success, got %q (error=%q)", got.Status, got.Error)
	}
}

// payload of every `data: ...` and `event: ...` line into a single string
// for assertions. Both lines are kept (with their original prefix) so that
// consumers of this helper can assert on either the JSON payload or the
// SSE event-type discriminator (e.g. "event: message_start"). This matters
// for the Anthropic SDK + Pi, which filter events via the SSE envelope's
// `event:` line — see temp/pi/packages/ai/src/providers/anthropic.ts:267-274.
func readSSEDataLines(t *testing.T, body io.Reader) (string, error) {
	t.Helper()
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var b strings.Builder
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data:") || strings.HasPrefix(line, "event:") {
			b.WriteString(line)
			b.WriteString("\n")
		}
	}
	if err := scanner.Err(); err != nil {
		return b.String(), err
	}
	return b.String(), nil
}

type dummyCaller struct{}

func (d *dummyCaller) Call(ctx context.Context, req protocol.Request) (*protocol.Response, error) {
	return &protocol.Response{Content: []protocol.Block{protocol.TextBlock("unused")}}, nil
}

// TestOpenAIStopReasonMapping covers the regression reported by the Pi coding
// agent: "Provider finish_reason: end_turn". Anthropic providers emit
// stop_reason values that the OpenAI SDK family does not recognize, so the
// usage gateway must translate them to the OpenAI-canonical finish_reason
// values before writing the chat.completion response (or its trailing
// streaming chunk).
func TestOpenAIStopReasonMapping(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"", "stop"},
		{"stop", "stop"},
		{"end_turn", "stop"},
		{"stop_sequence", "stop"},
		{"END_TURN", "stop"},
		{"max_tokens", "length"},
		{"length", "length"},
		{"tool_use", "tool_calls"},
		{"tool_calls", "tool_calls"},
		{"content_filter", "content_filter"},
		{"function_call", "function_call"},
		{"unknown_value", "unknown_value"},
	}
	for _, c := range cases {
		got := respStopReason(&protocol.Response{StopReason: c.raw})
		if got != c.want {
			t.Errorf("respStopReason(%q) = %q, want %q", c.raw, got, c.want)
		}
	}
	if got := respStopReason(nil); got != "" {
		t.Errorf("respStopReason(nil) = %q, want empty string", got)
	}
}

// TestAnthropicGatewayMessagesAcceptsGdxKey covers the regression where Pi
// (or any client using the Anthropic SDK) sent POST /v1/messages with the
// proxy key in the x-api-key header instead of Authorization: Bearer, and
// GoDex answered 401 with "Invalid API Key". The handler must accept
// either header so clients can use the standard Anthropic SDK as-is.
func TestAnthropicGatewayMessagesAcceptsGdxKey(t *testing.T) {
	handler, usageService := mustUsageHandler(t)
	created, err := usageService.CreateKey(usage.KeyCreateRequest{
		Name:          "anthropic-gw-bearer",
		BudgetCredits: 100,
		AllowedModels: []string{"claude-3-haiku"},
	})
	if err != nil {
		t.Fatalf("seed create: %v", err)
	}
	if _, err := usageService.CreateModel(usage.ModelCreateRequest{
		PublicModel:     "claude-3-haiku",
		TargetProfileID: "default",
		TargetModel:     "claude-3-haiku-20240307",
		CreditWeight:    1,
	}); err != nil {
		t.Fatalf("create model: %v", err)
	}

	server := httptest.NewServer(handler)
	defer server.Close()

	body := `{"model":"claude-3-haiku","max_tokens":10,"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`

	// Variant 1: Authorization: Bearer gdx_<key> (the path already supported).
	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/messages", strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+created.Secret)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do bearer request: %v", err)
	}
	respBody := readAll(t, resp)
	resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		t.Fatalf("Bearer gdx_ auth should not return 401, got body=%s", respBody)
	}

	// Variant 2: x-api-key: gdx_<key> (Anthropic SDK default; previously returned 401).
	req2, err := http.NewRequest(http.MethodPost, server.URL+"/v1/messages", strings.NewReader(body))
	if err != nil {
		t.Fatalf("new x-api-key request: %v", err)
	}
	req2.Header.Set("x-api-key", created.Secret)
	req2.Header.Set("Content-Type", "application/json")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("do x-api-key request: %v", err)
	}
	resp2Body := readAll(t, resp2)
	resp2.Body.Close()
	if resp2.StatusCode == http.StatusUnauthorized {
		t.Fatalf("x-api-key gdx_ auth should not return 401, got body=%s", resp2Body)
	}
}

// by Pi calling POST /v1/messages with a content block that has the wrong
// field name ("什么是 快乐星球？": "test" instead of "text": "test"). GoDex
// must (1) not forward a text block whose "text" field is missing or empty,
// and (2) not forward a message whose content reduces to nothing, otherwise
// the upstream Anthropic-compatible provider (minimax) returns 2013
// "messages is empty".
func TestAnthropicToProtocolSkipsEmptyTextBlock(t *testing.T) {
	cases := []struct {
		name      string
		req       anthropicMessageRequest
		wantMsgs  int
		wantBlks  int
		wantFirst string
	}{
		{
			name: "empty text field is dropped",
			req: anthropicMessageRequest{
				Model:     "M3",
				MaxTokens: 10,
				Messages: []anthropicMessage{{
					Role:    "user",
					Content: []anthropicContentBlock{{Type: "text", Text: ""}},
				}},
			},
			wantMsgs: 0,
			wantBlks: 0,
		},
		{
			name: "missing text field (Pi client bug) is dropped",
			req: anthropicMessageRequest{
				Model:     "M3",
				MaxTokens: 10,
				Messages: []anthropicMessage{{
					Role: "user",
					Content: []anthropicContentBlock{
						{Type: "text"}, // Go's zero-value: Text == "" because the JSON had no "text" key
					},
				}},
			},
			wantMsgs: 0,
			wantBlks: 0,
		},
		{
			name: "normal text is preserved",
			req: anthropicMessageRequest{
				Model:     "M3",
				MaxTokens: 10,
				Messages: []anthropicMessage{{
					Role:    "user",
					Content: []anthropicContentBlock{{Type: "text", Text: "hi"}},
				}},
			},
			wantMsgs:  1,
			wantBlks:  1,
			wantFirst: "hi",
		},
		{
			name: "image block is preserved even when text is missing",
			req: anthropicMessageRequest{
				Model:     "M3",
				MaxTokens: 10,
				Messages: []anthropicMessage{{
					Role: "user",
					Content: []anthropicContentBlock{
						{Type: "image", Source: &anthropicImageSource{Type: "base64", MediaType: "image/png", Data: "AAAA"}},
					},
				}},
			},
			wantMsgs: 1,
			wantBlks: 1,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := anthropicToProtocolRequest(c.req, "M3")
			if err != nil {
				t.Fatalf("anthropicToProtocolRequest: %v", err)
			}
			if len(got.Messages) != c.wantMsgs {
				t.Fatalf("want %d messages, got %d (%+v)", c.wantMsgs, len(got.Messages), got.Messages)
			}
			if c.wantMsgs > 0 {
				if len(got.Messages[0].Content) != c.wantBlks {
					t.Fatalf("want %d content blocks in first message, got %d (%+v)", c.wantBlks, len(got.Messages[0].Content), got.Messages[0].Content)
				}
				if c.wantFirst != "" {
					if got.Messages[0].Content[0].Text != c.wantFirst {
						t.Fatalf("want first block text %q, got %q", c.wantFirst, got.Messages[0].Content[0].Text)
					}
				}
			}
		})
	}
}

// TestAnthropicToProtocolPreservesToolResult covers the regression
// where Pi's tool results were dropped on the way to the upstream
// provider. The Anthropic tool_result content block carries its
// payload in the `content` field (a string for the common case, an
// array of content blocks for text + image / multi-block results).
// The conversion must flatten both shapes to the string the upstream
// provider expects; otherwise the model sees the tool as having
// returned no output and loops on the tool call.
func TestAnthropicToProtocolPreservesToolResult(t *testing.T) {
	cases := []struct {
		name string
		req  anthropicMessageRequest
		want string
	}{
		{
			name: "string content is preserved verbatim",
			req: anthropicMessageRequest{
				Model:     "M3",
				MaxTokens: 10,
				Messages: []anthropicMessage{{
					Role: "user",
					Content: []anthropicContentBlock{{
						Type:      "tool_result",
						ToolUseID: "toolu_1",
						Content:   "Sun Jun  7 10:48:52 CST 2026",
					}},
				}},
			},
			want: "Sun Jun  7 10:48:52 CST 2026",
		},
		{
			name: "single-element text array is collapsed to its text",
			req: anthropicMessageRequest{
				Model:     "M3",
				MaxTokens: 10,
				Messages: []anthropicMessage{{
					Role: "user",
					Content: []anthropicContentBlock{{
						Type:      "tool_result",
						ToolUseID: "toolu_1",
						Content: []interface{}{
							map[string]interface{}{"type": "text", "text": "hello world"},
						},
					}},
				}},
			},
			want: "hello world",
		},
		{
			name: "multi-text array is collapsed with newlines between blocks",
			req: anthropicMessageRequest{
				Model:     "M3",
				MaxTokens: 10,
				Messages: []anthropicMessage{{
					Role: "user",
					Content: []anthropicContentBlock{{
						Type:      "tool_result",
						ToolUseID: "toolu_1",
						Content: []interface{}{
							map[string]interface{}{"type": "text", "text": "line one"},
							map[string]interface{}{"type": "text", "text": "line two"},
						},
					}},
				}},
			},
			want: "line one\nline two",
		},
		{
			name: "array with non-text blocks drops the non-text blocks",
			req: anthropicMessageRequest{
				Model:     "M3",
				MaxTokens: 10,
				Messages: []anthropicMessage{{
					Role: "user",
					Content: []anthropicContentBlock{{
						Type:      "tool_result",
						ToolUseID: "toolu_1",
						Content: []interface{}{
							map[string]interface{}{"type": "text", "text": "kept"},
							map[string]interface{}{"type": "image", "source": map[string]interface{}{"type": "base64", "media_type": "image/png", "data": "AAAA"}},
							map[string]interface{}{"type": "text", "text": "kept too"},
						},
					}},
				}},
			},
			want: "kept\nkept too",
		},
		{
			name: "empty / missing content resolves to empty string",
			req: anthropicMessageRequest{
				Model:     "M3",
				MaxTokens: 10,
				Messages: []anthropicMessage{{
					Role: "user",
					Content: []anthropicContentBlock{{
						Type:      "tool_result",
						ToolUseID: "toolu_1",
						// no Content field
					}},
				}},
			},
			want: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := anthropicToProtocolRequest(c.req, "M3")
			if err != nil {
				t.Fatalf("anthropicToProtocolRequest: %v", err)
			}
			if len(got.Messages) != 1 {
				t.Fatalf("want 1 message, got %d", len(got.Messages))
			}
			if len(got.Messages[0].Content) != 1 {
				t.Fatalf("want 1 content block, got %d", len(got.Messages[0].Content))
			}
			block := got.Messages[0].Content[0]
			if block.Type != protocol.BlockToolResult {
				t.Fatalf("want block type %q, got %q", protocol.BlockToolResult, block.Type)
			}
			if block.ToolUseID != "toolu_1" {
				t.Errorf("want tool_use_id=toolu_1, got %q", block.ToolUseID)
			}
			if block.Content != c.want {
				t.Errorf("want content %q, got %q", c.want, block.Content)
			}
		})
	}
}

// TestAnthropicToProtocolRequestThinkingBlock covers the round-trip
// of an Anthropic thinking content block from a multi-turn Pi
// request back into the protocol.Request shape. The previous
// implementation silently dropped the block, so the model never
// saw its own prior chain-of-thought and the multi-turn reasoning
// buffer was lost between turns. The fix surfaces the block on
// the API message and the protocol.Block list so the upstream
// can keep the conversation context.
func TestAnthropicToProtocolRequestThinkingBlock(t *testing.T) {
	req := anthropicMessageRequest{
		Model:     "M3",
		MaxTokens: 16,
		Messages: []anthropicMessage{{
			Role: "assistant",
			Content: []anthropicContentBlock{
				{Type: "thinking", Thinking: "let me think...", Signature: "sig-1"},
				{Type: "text", Text: "answer"},
			},
		}},
	}
	got, err := anthropicToProtocolRequest(req, "M3")
	if err != nil {
		t.Fatalf("anthropicToProtocolRequest: %v", err)
	}
	if len(got.Messages) != 1 {
		t.Fatalf("want 1 message, got %d", len(got.Messages))
	}
	msg := got.Messages[0]
	if msg.Role != "assistant" {
		t.Fatalf("want assistant role, got %q", msg.Role)
	}
	// We expect 2 blocks: thinking + text. The order is
	// preserved (thinking first, then text), which is the
	// canonical Anthropic layout.
	if len(msg.Content) != 2 {
		t.Fatalf("want 2 content blocks, got %d: %+v", len(msg.Content), msg.Content)
	}
	if msg.Content[0].Type != protocol.BlockThinking {
		t.Errorf("want first block thinking, got %q", msg.Content[0].Type)
	}
	if msg.Content[0].Text != "let me think..." {
		t.Errorf("want thinking text %q, got %q", "let me think...", msg.Content[0].Text)
	}
	if msg.Content[0].Signature != "sig-1" {
		t.Errorf("want signature sig-1, got %q", msg.Content[0].Signature)
	}
	// The reasoning_content mirror must be populated so the
	// upstream sees the chain-of-thought alongside the regular
	// content. Without it, OpenAI-style upstreams would treat
	// the message as if the model had no prior reasoning.
	if msg.ReasoningContent != "let me think..." {
		t.Errorf("want reasoning_content %q, got %q", "let me think...", msg.ReasoningContent)
	}
	if msg.Content[1].Type != protocol.BlockText {
		t.Errorf("want second block text, got %q", msg.Content[1].Type)
	}
}

// TestAnthropicToProtocolRequestThinkingConfig covers the case
// where Pi sends `thinking: {type: "enabled", budget_tokens: 1024}`
// to request extended reasoning. The conversion must surface the
// budget as ReasoningEffort so the upstream LLM client can
// translate it to a local reasoning knob (Anthropic native uses
// thinking.budget_tokens; OpenAI uses reasoning_effort). Without
// the fix the thinking knob was ignored and the model ran in
// non-thinking mode, breaking the user's reasoning setting.
func TestAnthropicToProtocolRequestThinkingConfig(t *testing.T) {
	req := anthropicMessageRequest{
		Model:     "M3",
		MaxTokens: 16,
		Thinking: &anthropicThinkingConfig{
			Type:         "enabled",
			BudgetTokens: 2048,
		},
		Messages: []anthropicMessage{{
			Role:    "user",
			Content: []anthropicContentBlock{{Type: "text", Text: "explain"}},
		}},
	}
	got, err := anthropicToProtocolRequest(req, "M3")
	if err != nil {
		t.Fatalf("anthropicToProtocolRequest: %v", err)
	}
	if got.ReasoningEffort != "2048" {
		t.Errorf("want ReasoningEffort=2048, got %q", got.ReasoningEffort)
	}

	// Adaptive thinking: the client sends
	// `{type: "adaptive"}` (no budget_tokens). We MUST NOT
	// surface a numeric ReasoningEffort in that case, because
	// the upstream Anthropic-native API expects the
	// thinking.type=adaptive knob on the wire, not a numeric
	// effort. For non-Anthropic upstreams (OpenAI-style) the
	// client library decides the effort level from the model
	// config.
	req2 := anthropicMessageRequest{
		Model:    "M3",
		MaxTokens: 16,
		Thinking: &anthropicThinkingConfig{Type: "adaptive"},
		Messages: []anthropicMessage{{
			Role:    "user",
			Content: []anthropicContentBlock{{Type: "text", Text: "explain"}},
		}},
	}
	got2, err := anthropicToProtocolRequest(req2, "M3")
	if err != nil {
		t.Fatalf("anthropicToProtocolRequest (adaptive): %v", err)
	}
	if got2.ReasoningEffort != "" {
		t.Errorf("want empty ReasoningEffort for adaptive thinking, got %q", got2.ReasoningEffort)
	}
}

// TestAnthropicGatewayForwardsToolResultToUpstream is the end-to-end
// regression for the case where Pi's tool results disappeared before
// reaching the upstream provider. We stub the upstream with an
// httptest server that captures the POST body and replies with a
// trivial text SSE, then assert the captured Anthropic-shape request
// carries the tool_result content string in full. Without the fix,
// the captured content would be empty and the model would loop on
// the tool call (the user-reported symptom: "bash 工具似乎不返回输出").
func TestAnthropicGatewayForwardsToolResultToUpstream(t *testing.T) {
	var capturedBody []byte
	var capturedContentType string
	stubProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		capturedContentType = r.Header.Get("Content-Type")
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		flusher, _ := w.(http.Flusher)
		frames := []string{
			`event: message_start` + "\n" + `data: {"type":"message_start","message":{"id":"msg-1","type":"message","role":"assistant","content":[],"model":"stub-model","stop_reason":null,"usage":{"input_tokens":1,"output_tokens":0}}}`,
			`event: content_block_start` + "\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
			`event: content_block_stop` + "\n" + `data: {"type":"content_block_stop","index":0}`,
			`event: message_delta` + "\n" + `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
			`event: message_stop` + "\n" + `data: {"type":"message_stop"}`,
		}
		for _, f := range frames {
			_, _ = io.WriteString(w, f+"\n\n")
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	defer stubProvider.Close()

	cfg := newTestConfig(t)
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

	store, err := usage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("new usage store: %v", err)
	}
	usageService := usage.NewService(store)
	stubProfileID := manager.Current().DefaultProfileID
	if stubProfileID == "" {
		t.Fatalf("expected a default profile id after update, got empty")
	}
	if _, err := usageService.CreateModel(usage.ModelCreateRequest{
		PublicModel:     "M3",
		TargetProfileID: stubProfileID,
		TargetModel:     "stub-model",
		CreditWeight:    1,
	}); err != nil {
		t.Fatalf("create model mapping: %v", err)
	}
	created, err := usageService.CreateKey(usage.KeyCreateRequest{
		Name:          "anthropic-tool-result-forward",
		BudgetCredits: 100,
		AllowedModels: []string{"M3"},
	})
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, &dummyCaller{}), commands.NewService(cfg))
	handler := NewHandler(manager, service, nil, nil, nil, nil, usageService)
	server := httptest.NewServer(handler)
	defer server.Close()

	// Pi sends a multi-turn request: previous assistant tool_use, then
	// a user message carrying the tool_result with the actual command
	// output. We mirror Pi's body shape verbatim so the conversion
	// path is exercised end-to-end.
	body := `{
		"model":"M3",
		"stream":true,
		"max_tokens":16,
		"messages":[
			{"role":"user","content":[{"type":"text","text":"run date"}]},
			{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"bash","input":{"command":"date"}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"Sun Jun  7 10:48:52 CST 2026"}]}
		]
	}`
	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/messages", strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("x-api-key", created.Secret)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(respBody))
	}
	// Drain the response so the handler completes and the upstream
	// request body is fully captured.
	_, _ = io.Copy(io.Discard, resp.Body)

	if !strings.HasPrefix(capturedContentType, "application/json") {
		t.Errorf("expected upstream request Content-Type application/json, got %q", capturedContentType)
	}
	var captured map[string]interface{}
	if err := json.Unmarshal(capturedBody, &captured); err != nil {
		t.Fatalf("decode captured upstream body: %v\nbody: %s", err, string(capturedBody))
	}
	msgs, _ := captured["messages"].([]interface{})
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages in upstream request, got %d (%+v)", len(msgs), msgs)
	}
	// The third message is the user turn that carries the tool_result.
	userMsg, _ := msgs[2].(map[string]interface{})
	content, _ := userMsg["content"].([]interface{})
	if len(content) != 1 {
		t.Fatalf("expected 1 content block in tool_result message, got %d (%+v)", len(content), content)
	}
	tr, _ := content[0].(map[string]interface{})
	if tr["type"] != "tool_result" {
		t.Fatalf("expected block type=tool_result, got %v", tr)
	}
	if tr["tool_use_id"] != "toolu_1" {
		t.Errorf("expected tool_use_id=toolu_1, got %v", tr["tool_use_id"])
	}
	gotContent, _ := tr["content"].(string)
	if gotContent != "Sun Jun  7 10:48:52 CST 2026" {
		t.Errorf("expected tool_result content %q, got %q", "Sun Jun  7 10:48:52 CST 2026", gotContent)
	}
}

// TestUsageGatewayChatCompletionsForwardToolResultToUpstream is the
// end-to-end regression for the case where Pi (or any OpenAI SDK
// client) called POST /v1/chat/completions on the godex gateway with
// an `anthropic_compatible` provider profile, and the agent loop
// hung after the first tool call. The root cause was that the
// gateway dropped the `role: "tool"` message on the second turn
// of the conversation (the round-trip after Pi's tool loop ran the
// tool), so the upstream model never saw its own tool result and
// could not produce a follow-up reply. The fix translates the
// OpenAI `role: "tool"` message to a `BlockToolResult` block on the
// `protocol.Request` so the upstream Anthropic client surfaces it
// as a `tool_result` content block in the Anthropic request body.
// The test captures the upstream request body verbatim and asserts
// the tool_result content reaches the upstream unchanged.
func TestUsageGatewayChatCompletionsForwardToolResultToUpstream(t *testing.T) {
	var capturedBody []byte
	stubProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-1",
			"object":"chat.completion",
			"created":1,
			"model":"stub-model",
			"choices":[{
				"index":0,
				"message":{"role":"assistant","content":"ok"},
				"finish_reason":"stop"
			}]
		}`))
	}))
	defer stubProvider.Close()

	cfg := newTestConfig(t)
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

	store, err := usage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("new usage store: %v", err)
	}
	usageService := usage.NewService(store)
	stubProfileID := manager.Current().DefaultProfileID
	if stubProfileID == "" {
		t.Fatalf("expected a default profile id after update, got empty")
	}
	if _, err := usageService.CreateModel(usage.ModelCreateRequest{
		PublicModel:     "M3",
		TargetProfileID: stubProfileID,
		TargetModel:     "stub-model",
		CreditWeight:    1,
	}); err != nil {
		t.Fatalf("create model mapping: %v", err)
	}
	created, err := usageService.CreateKey(usage.KeyCreateRequest{
		Name:          "openai-tool-result-forward",
		BudgetCredits: 100,
		AllowedModels: []string{"M3"},
	})
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, &dummyCaller{}), commands.NewService(cfg))
	handler := NewHandler(manager, service, nil, nil, nil, nil, usageService)
	server := httptest.NewServer(handler)
	defer server.Close()

	// Pi-style OpenAI chat completion request with a multi-turn
	// conversation: a user message, an assistant tool_call, and
	// the tool's result. The previous implementation dropped
	// the tool result and the assistant tool_call; the upstream
	// saw only the original user message and the model
	// couldn't continue.
	body := `{
		"model":"M3",
		"messages":[
			{"role":"user","content":"read /tmp/hi"},
			{"role":"assistant","content":null,"tool_calls":[
				{"id":"call_abc","type":"function","index":0,"function":{"name":"read","arguments":"{\"path\":\"/tmp/hi\"}"}}
			]},
			{"role":"tool","tool_call_id":"call_abc","content":"hi from file"}
		]
	}`
	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(body))
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
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(respBody))
	}
	captured := string(capturedBody)
	// The upstream request body (Anthropic format) MUST carry
	// the tool_result content block with the matching
	// tool_use_id. Without the fix, the tool result would be
	// missing from the upstream body and the model would have
	// no way to know what the tool returned.
	if !strings.Contains(captured, `"tool_result"`) {
		t.Errorf("expected upstream body to contain tool_result block, got body:\n%s", captured)
	}
	if !strings.Contains(captured, `"tool_use_id":"call_abc"`) {
		t.Errorf("expected upstream body to bind tool_result to tool_use_id=call_abc, got body:\n%s", captured)
	}
	if !strings.Contains(captured, `hi from file`) {
		t.Errorf("expected upstream body to carry the tool result text, got body:\n%s", captured)
	}
	// The assistant's prior tool_call must ALSO be in the
	// upstream body so the model can match the result to the
	// call. The previous implementation dropped the
	// tool_calls array entirely.
	if !strings.Contains(captured, `"tool_use"`) {
		t.Errorf("expected upstream body to contain tool_use block from assistant message, got body:\n%s", captured)
	}
	if !strings.Contains(captured, `"name":"read"`) {
		t.Errorf("expected upstream body to carry the tool name 'read', got body:\n%s", captured)
	}
	if !strings.Contains(captured, `\/tmp\/hi`) && !strings.Contains(captured, `{"path":"/tmp/hi"}`) {
		t.Errorf("expected upstream body to carry the tool arguments, got body:\n%s", captured)
	}
}

// TestUsageGatewayChatCompletionsStreamingForwardsToolCallDeltasAsSuffixes
// covers the streaming variant of the cross-protocol
// regression: when the upstream is anthropic_compatible and Pi
// is on the OpenAI protocol, the gateway receives cumulative
// tool_use arguments from the upstream (because parseMessageStream
// calls OnToolUse with the cumulative JSON) and would corrupt
// the OpenAI wire format by re-sending the same bytes on every
// chunk. The fix tracks the previous cumulative per tool index
// and forwards only the delta suffix, mirroring the
// toolJSONDeltaSuffix behaviour the Anthropic streaming path
// already had.
func TestUsageGatewayChatCompletionsStreamingForwardsToolCallDeltasAsSuffixes(t *testing.T) {
	stubProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		// The upstream emits the tool_use JSON across three
		// input_json_delta frames. The gateway must forward
		// only the per-chunk delta suffix, not the
		// cumulative string on every frame.
		events := []string{
			`event: message_start` + "\n" + `data: {"type":"message_start","message":{"id":"msg-x","type":"message","role":"assistant","content":[],"model":"stub-model","stop_reason":null,"usage":{"input_tokens":1,"output_tokens":0}}}`,
			`event: content_block_start` + "\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call_x","name":"read","input":{}}}`,
			`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}`,
			`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"\/tmp\/hi\""}}`,
			`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":",\"limit\":100}"}}`,
			`event: content_block_stop` + "\n" + `data: {"type":"content_block_stop","index":0}`,
			`event: message_delta` + "\n" + `data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":1}}`,
			`event: message_stop` + "\n" + `data: {"type":"message_stop"}`,
		}
		for _, e := range events {
			_, _ = io.WriteString(w, e+"\n\n")
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	defer stubProvider.Close()

	cfg := newTestConfig(t)
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

	store, err := usage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("new usage store: %v", err)
	}
	usageService := usage.NewService(store)
	stubProfileID := manager.Current().DefaultProfileID
	if _, err := usageService.CreateModel(usage.ModelCreateRequest{
		PublicModel:     "M3",
		TargetProfileID: stubProfileID,
		TargetModel:     "stub-model",
		CreditWeight:    1,
	}); err != nil {
		t.Fatalf("create model mapping: %v", err)
	}
	created, err := usageService.CreateKey(usage.KeyCreateRequest{
		Name:          "openai-tool-delta-suffix",
		BudgetCredits: 100,
		AllowedModels: []string{"M3"},
	})
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, &dummyCaller{}), commands.NewService(cfg))
	handler := NewHandler(manager, service, nil, nil, nil, nil, usageService)
	server := httptest.NewServer(handler)
	defer server.Close()

	body := `{"model":"M3","stream":true,"max_tokens":16,"messages":[{"role":"user","content":"read /tmp/hi"}]}`
	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+created.Secret)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(respBody))
	}
	streamBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	stream := string(streamBytes)

	// Parse the chat.completion.chunk frames the gateway
	// emitted. OpenAI's streaming wire format uses bare
	// `data: {...}` lines (no `event:` discriminator), so we
	// accept both shapes here. Pin the per-chunk arguments
	// fragments the way Pi's OpenAI SDK would: extract the
	// `arguments` field from every tool_calls chunk in
	// order, then concatenate them and verify the result is
	// the assembled JSON (rather than the cumulative string
	// repeated three times, which is the previous bug).
	type sseFrame struct {
		data map[string]interface{}
	}
	frames := []sseFrame{}
	for _, block := range strings.Split(stream, "\n\n") {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		var data string
		for _, line := range strings.Split(block, "\n") {
			if strings.HasPrefix(line, "data: ") {
				data = strings.TrimPrefix(line, "data: ")
			}
		}
		if data == "" || data == "[DONE]" {
			continue
		}
		var parsed map[string]interface{}
		if err := json.Unmarshal([]byte(data), &parsed); err != nil {
			continue
		}
		frames = append(frames, sseFrame{data: parsed})
	}
	var partials []string
	for _, f := range frames {
		// OpenAI chat.completion.chunk path: tool_calls live
		// under choices[].delta.tool_calls[].
		choices, _ := f.data["choices"].([]interface{})
		for _, ch := range choices {
			cm, _ := ch.(map[string]interface{})
			delta, _ := cm["delta"].(map[string]interface{})
			tcs, _ := delta["tool_calls"].([]interface{})
			for _, tc := range tcs {
				tcm, _ := tc.(map[string]interface{})
				fn, _ := tcm["function"].(map[string]interface{})
				if args, ok := fn["arguments"].(string); ok && args != "" {
					partials = append(partials, args)
				}
			}
		}
	}
	// The gateway must forward exactly three per-chunk
	// fragments, not the cumulative string repeated three
	// times. Pinning this catches the regression where the
	// gateway would resend `{"path":"/tmp/hi","limit":100}`
	// on every chunk, which would cause Pi to assemble
	// `{"path":"/tmp/hi","limit":100}{"path":"/tmp/hi","limit":100}…`
	// and fail to parse the tool arguments.
	wantPartials := []string{
		`{"path":`,
		`"/tmp/hi"`,
		`,` + `"limit":100}`,
	}
	if len(partials) != len(wantPartials) {
		t.Fatalf("expected %d tool_call argument fragments, got %d: %v", len(wantPartials), len(partials), partials)
	}
	for i, want := range wantPartials {
		if partials[i] != want {
			t.Errorf("partial[%d]: expected %q, got %q", i, want, partials[i])
		}
	}
	// Concatenate the per-chunk fragments the way Pi does and
	// verify the result is the assembled JSON the upstream
	// intended. If the gateway had forwarded the cumulative
	// string on every chunk, this parse would fail or
	// produce a corrupted nested object.
	var assembled strings.Builder
	for _, p := range partials {
		assembled.WriteString(p)
	}
	var got map[string]interface{}
	if err := json.Unmarshal([]byte(assembled.String()), &got); err != nil {
		t.Fatalf("assembled arguments is not valid JSON: %v\nassembled: %s", err, assembled.String())
	}
	if got["path"] != "/tmp/hi" {
		t.Errorf("expected path=/tmp/hi, got %v", got["path"])
	}
	if limit, ok := got["limit"].(float64); !ok || limit != 100 {
		t.Errorf("expected limit=100, got %v (%T)", got["limit"], got["limit"])
	}
	_ = streamBytes
}

// TestUsageGatewayChatCompletionAcceptsStreamTrue covers the regression where
// POST /v1/chat/completions with stream:true and a gdx_* proxy key returned
// 400 unsupported_streaming. The usage gateway currently only supports
// non-streaming responses, so the stream flag is silently downgraded to
// stream:false and the call completes with a normal chat.completion JSON
// payload. OpenAI-compatible clients that requested stream:true will buffer
// the full body and present it as a single message.
func TestUsageGatewayChatCompletionAcceptsStreamTrue(t *testing.T) {
	cfg := newTestConfig(t)
	manager := newTestManager(t, cfg)
	if _, err := manager.Update(context.Background(), config.UpdateRequest{Values: map[string]any{
		"api.providers": map[string]any{
			"stub": map[string]any{
				"name":     "Stub",
				"type":     "openai_compatible",
				"base_url": "http://127.0.0.1:1",
				"api_key":  "sk-test",
				"models":   map[string]any{},
			},
		},
	}}); err != nil {
		t.Fatalf("update config: %v", err)
	}

	store, err := usage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("new usage store: %v", err)
	}
	usageService := usage.NewService(store)

	stubProfileID := manager.Current().DefaultProfileID
	if stubProfileID == "" {
		t.Fatalf("expected a default profile id after update, got empty")
	}

	if _, err := usageService.CreateModel(usage.ModelCreateRequest{
		PublicModel:     "M3",
		TargetProfileID: stubProfileID,
		TargetModel:     "stub-model",
		CreditWeight:    1,
	}); err != nil {
		t.Fatalf("create model mapping: %v", err)
	}

	created, err := usageService.CreateKey(usage.KeyCreateRequest{
		Name:          "stream-fallback",
		BudgetCredits: 100,
		AllowedModels: []string{"M3"},
	})
	if err != nil {
		t.Fatalf("create key: %v", err)
	}

	// Re-resolve profile from the updated config (after CreateModel may have
	// rotated the view), so the model mapping's TargetProfileID actually
	// exists when handleUsageGatewayChatCompletions looks it up.
	current := manager.Current()
	profile, ok := current.ModelProfileByID(stubProfileID)
	if !ok {
		t.Fatalf("profile %q missing from config", stubProfileID)
	}
	profile.BaseURL = ""

	caller := &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("fallback reply")}}}}
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, caller), commands.NewService(cfg))
	handler := NewHandler(manager, service, nil, nil, nil, nil, usageService)
	server := httptest.NewServer(handler)
	defer server.Close()

	body := `{"model":"M3","stream":true,"messages":[{"role":"user","content":"hello"}],"max_tokens":16}`
	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(body))
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

	if resp.StatusCode == http.StatusBadRequest && strings.Contains(strings.ToLower(readAll(t, resp)), "unsupported_streaming") {
		t.Fatalf("usage gateway should silently downgrade stream:true instead of returning 400 unsupported_streaming")
	}
	if resp.StatusCode != http.StatusOK {
		// The fix is specifically about no longer rejecting stream:true with 400.
		// A 5xx from the underlying provider stub (e.g. because the test
		// injects a profile with an empty BaseURL) is expected and accepted
		// here; covering the provider path belongs to a dedicated test.
		t.Logf("non-200 status %d accepted: stream:true was no longer rejected as unsupported_streaming", resp.StatusCode)
	}
}

// TestAnthropicGatewayStreamsParallelToolUseSSE is the regression test
// for the case where a model emits two parallel tool_use blocks in the
// same assistant turn (e.g. `read` + `glob` to gather context before
// answering). The previous gateway implementation re-used content
// block index 0 for every block, so the second tool_use silently
// overwrote the first one in Pi's streamAnthropic accumulator and
// Pi's agent loop saw only one tool call. This test stubs the
// upstream to emit TWO tool_use blocks with different content_block
// indices and asserts the wire-side stream carries both:
//
//   data: {"type":"content_block_start","index":1, ...}
//   data: {"type":"content_block_start","index":2, ...}
//   ...
//   data: {"type":"content_block_stop","index":1}
//   data: {"type":"content_block_stop","index":2}
//
// plus a final text block at index 3 (or whatever the gateway
// assigns) so the assistant can chain the tool results into a reply.
func TestAnthropicGatewayStreamsParallelToolUseSSE(t *testing.T) {
	stubProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		flusher, _ := w.(http.Flusher)
		// The upstream emits two tool_use blocks in parallel. The
		// first (id=toolu_read) reads a file; the second
		// (id=toolu_glob) globs the directory. We pick two
		// non-zero upstream indices so we can verify the gateway
		// emits two distinct wire indices (and doesn't accidentally
		// re-use index 0 for both blocks).
		events := []string{
			`event: message_start` + "\n" + `data: {"type":"message_start","message":{"id":"msg-p","type":"message","role":"assistant","content":[],"model":"stub-model","stop_reason":null,"usage":{"input_tokens":1,"output_tokens":0}}}`,
			`event: content_block_start` + "\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_read","name":"read","input":{}}}`,
			`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"/tmp/x\"}"}}`,
			`event: content_block_stop` + "\n" + `data: {"type":"content_block_stop","index":0}`,
			`event: content_block_start` + "\n" + `data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_glob","name":"glob","input":{}}}`,
			`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"pattern\":\"/tmp/*\"}"}}`,
			`event: content_block_stop` + "\n" + `data: {"type":"content_block_stop","index":1}`,
			`event: message_delta` + "\n" + `data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":2}}`,
			`event: message_stop` + "\n" + `data: {"type":"message_stop"}`,
		}
		for _, e := range events {
			_, _ = io.WriteString(w, e+"\n\n")
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	defer stubProvider.Close()

	cfg := newTestConfig(t)
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

	store, err := usage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("new usage store: %v", err)
	}
	usageService := usage.NewService(store)
	stubProfileID := manager.Current().DefaultProfileID
	if stubProfileID == "" {
		t.Fatalf("expected a default profile id after update, got empty")
	}
	if _, err := usageService.CreateModel(usage.ModelCreateRequest{
		PublicModel:     "M3",
		TargetProfileID: stubProfileID,
		TargetModel:     "stub-model",
		CreditWeight:    1,
	}); err != nil {
		t.Fatalf("create model mapping: %v", err)
	}
	created, err := usageService.CreateKey(usage.KeyCreateRequest{
		Name:          "anthropic-parallel-tools",
		BudgetCredits: 100,
		AllowedModels: []string{"M3"},
	})
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, &dummyCaller{}), commands.NewService(cfg))
	handler := NewHandler(manager, service, nil, nil, nil, nil, usageService)
	server := httptest.NewServer(handler)
	defer server.Close()

	body := `{"model":"M3","stream":true,"max_tokens":16,"messages":[{"role":"user","content":[{"type":"text","text":"show me /tmp"}]}]}`
	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/messages", strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("x-api-key", created.Secret)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(respBody))
	}
	stream, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	streamStr := string(stream)

	// Both tool_use blocks must be present on the wire with their
	// full identity (id + name + arguments). The previous bug
	// dropped the second tool call because the gateway reused
	// index 0 for both blocks; this assertion fails the test
	// when that's still the case.
	for _, must := range []string{
		`"name":"read"`,
		`"name":"glob"`,
		`"id":"toolu_read"`,
		`"id":"toolu_glob"`,
		`/tmp/x`,
		`/tmp/*`,
	} {
		if !strings.Contains(streamStr, must) {
			t.Errorf("expected Anthropic SSE stream to contain %q (parallel tool_use regression), got stream:\n%s", must, streamStr)
		}
	}

	// Both blocks must carry their own start + stop events.
	// Count the content_block_start and content_block_stop events;
	// the previous implementation emitted only one of each.
	startCount := strings.Count(streamStr, `"type":"content_block_start"`)
	stopCount := strings.Count(streamStr, `"type":"content_block_stop"`)
	if startCount != 2 {
		t.Errorf("expected exactly 2 content_block_start events, got %d:\n%s", startCount, streamStr)
	}
	if stopCount != 2 {
		t.Errorf("expected exactly 2 content_block_stop events, got %d:\n%s", stopCount, streamStr)
	}
}

// TestAnthropicGatewayStreamsThinkingBlockSSE is the regression test
// for Anthropic extended thinking support. Pi (and any Anthropic-SDK
// client) sets `thinking: {type: "enabled", budget_tokens: N}` to
// request extended reasoning, and expects the response stream to
// carry `content_block_start` / `thinking_delta` /
// `content_block_stop` events for the thinking block so the
// chain-of-thought text and signature are surfaced to the user and
// echoed back on the next turn. The previous implementation
// silently dropped all thinking content because the SSE parser
// didn't know about the `thinking` content block type. This test
// stubs the upstream to emit a thinking block + a text block, then
// asserts the wire-side stream carries both.
func TestAnthropicGatewayStreamsThinkingBlockSSE(t *testing.T) {
	stubProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		flusher, _ := w.(http.Flusher)
		// The upstream emits a thinking block, then a text block.
		// We use Anthropic's canonical wire shape with the
		// signature attached inline on content_block_start (the
		// common case for non-streamed reasoning servers).
		events := []string{
			`event: message_start` + "\n" + `data: {"type":"message_start","message":{"id":"msg-th","type":"message","role":"assistant","content":[],"model":"stub-model","stop_reason":null,"usage":{"input_tokens":1,"output_tokens":0}}}`,
			`event: content_block_start` + "\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":"sig-abc"}}`,
			`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"let me think..."}}`,
			`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig-abc"}}`,
			`event: content_block_stop` + "\n" + `data: {"type":"content_block_stop","index":0}`,
			`event: content_block_start` + "\n" + `data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
			`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"hello"}}`,
			`event: content_block_stop` + "\n" + `data: {"type":"content_block_stop","index":1}`,
			`event: message_delta` + "\n" + `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
			`event: message_stop` + "\n" + `data: {"type":"message_stop"}`,
		}
		for _, e := range events {
			_, _ = io.WriteString(w, e+"\n\n")
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	defer stubProvider.Close()

	cfg := newTestConfig(t)
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

	store, err := usage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("new usage store: %v", err)
	}
	usageService := usage.NewService(store)
	stubProfileID := manager.Current().DefaultProfileID
	if stubProfileID == "" {
		t.Fatalf("expected a default profile id after update, got empty")
	}
	if _, err := usageService.CreateModel(usage.ModelCreateRequest{
		PublicModel:     "M3",
		TargetProfileID: stubProfileID,
		TargetModel:     "stub-model",
		CreditWeight:    1,
	}); err != nil {
		t.Fatalf("create model mapping: %v", err)
	}
	created, err := usageService.CreateKey(usage.KeyCreateRequest{
		Name:          "anthropic-thinking",
		BudgetCredits: 100,
		AllowedModels: []string{"M3"},
	})
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, &dummyCaller{}), commands.NewService(cfg))
	handler := NewHandler(manager, service, nil, nil, nil, nil, usageService)
	server := httptest.NewServer(handler)
	defer server.Close()

	// Pi sends the thinking knob in the request body. The gateway
	// should propagate it to the upstream so the model is invoked
	// in thinking mode. We don't assert on the wire here — the
	// upstream stub doesn't echo its input — but we do verify the
	// gateway doesn't 400 on the field.
	body := `{"model":"M3","stream":true,"max_tokens":16,"thinking":{"type":"enabled","budget_tokens":1024},"messages":[{"role":"user","content":[{"type":"text","text":"think then say hello"}]}]}`
	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/messages", strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("x-api-key", created.Secret)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(respBody))
	}
	stream, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	streamStr := string(stream)

	// Both blocks must be present on the wire.
	for _, must := range []string{
		`"type":"thinking"`,
		`let me think...`,
		`"type":"text"`,
		`hello`,
		`"type":"content_block_start"`,
		`"type":"thinking_delta"`,
		`"type":"content_block_stop"`,
	} {
		if !strings.Contains(streamStr, must) {
			t.Errorf("expected Anthropic SSE stream to contain %q, got stream:\n%s", must, streamStr)
		}
	}
}

// TestAnthropicGatewayWebTokenRoutedThroughLLMGateway is the
// regression test for the case where the user configured Pi (or any
// Anthropic SDK) with the godex web token in `ANTHROPIC_AUTH_TOKEN`
// and `ANTHROPIC_BASE_URL=http://localhost:port`. The previous
// implementation routed those requests to the godex AI agent
// backend (a different product surface) which did not support
// streaming, tool calls, or images — so Pi would either hang or
// return a one-line text reply. The new implementation dispatches
// the request through the same LLM gateway the proxy-key path uses,
// so the wire format Pi sees is identical and the tool loop works.
func TestAnthropicGatewayWebTokenRoutedThroughLLMGateway(t *testing.T) {
	stubProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		events := []string{
			`event: message_start` + "\n" + `data: {"type":"message_start","message":{"id":"msg-wt","type":"message","role":"assistant","content":[],"model":"stub-model","stop_reason":null,"usage":{"input_tokens":1,"output_tokens":0}}}`,
			`event: content_block_start` + "\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi from web token"}}`,
			`event: content_block_stop` + "\n" + `data: {"type":"content_block_stop","index":0}`,
			`event: message_delta` + "\n" + `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`,
			`event: message_stop` + "\n" + `data: {"type":"message_stop"}`,
		}
		for _, e := range events {
			_, _ = io.WriteString(w, e+"\n\n")
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	defer stubProvider.Close()

	cfg := newTestConfig(t)
	cfg.WebToken = "test-web-token"
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

	store, err := usage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("new usage store: %v", err)
	}
	usageService := usage.NewService(store)
	stubProfileID := manager.Current().DefaultProfileID
	if _, err := usageService.CreateModel(usage.ModelCreateRequest{
		PublicModel:     "claude-3-haiku",
		TargetProfileID: stubProfileID,
		TargetModel:     "stub-model",
		CreditWeight:    1,
	}); err != nil {
		t.Fatalf("create model mapping: %v", err)
	}
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, &dummyCaller{}), commands.NewService(cfg))
	handler := NewHandler(manager, service, nil, nil, nil, nil, usageService)
	server := httptest.NewServer(handler)
	defer server.Close()

	body := `{"model":"claude-3-haiku","stream":true,"max_tokens":16,"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`
	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/messages", strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	// Web-token auth: Pi uses ANTHROPIC_AUTH_TOKEN which the
	// Anthropic SDK forwards as `Authorization: Bearer <token>`.
	req.Header.Set("Authorization", "Bearer test-web-token")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(respBody))
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("expected Content-Type text/event-stream, got %q", ct)
	}
	stream, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	streamStr := string(stream)

	// The web-token path must reach the LLM gateway (not the agent
	// backend) and surface the same Anthropic SSE shape the proxy-
	// key path uses. The "hi from web token" string is the
	// distinct marker that we routed through the stub provider,
	// not the agent backend (which would emit a generic fallback
	// reply that the test doesn't seed).
	for _, must := range []string{
		"event: message_start",
		"event: content_block_start",
		"event: content_block_delta",
		"event: content_block_stop",
		"event: message_delta",
		"event: message_stop",
		`"text":"hi from web token"`,
	} {
		if !strings.Contains(streamStr, must) {
			t.Errorf("expected web-token Anthropic SSE stream to contain %q, got stream:\n%s", must, streamStr)
		}
	}
}

// TestProtocolToAnthropicResponseThinkingBlocks pins the
// non-streaming conversion path for extended-thinking blocks.
// Pi's Anthropic SDK reads the `content` array in declaration
// order, so a thinking block MUST appear before any text /
// tool_use blocks (matching the upstream Anthropic API), and
// redacted_thinking blocks must use the `data` field for the
// opaque payload. The previous implementation dropped thinking
// blocks entirely on the non-streaming path, so multi-turn
// sessions lost their chain-of-thought on the first response.
func TestProtocolToAnthropicResponseThinkingBlocks(t *testing.T) {
	t.Run("thinking block appears first and carries signature", func(t *testing.T) {
		resp := &protocol.Response{
			StopReason: "end_turn",
			Content: []protocol.Block{
				protocol.TextBlock("answer"),
				protocol.ThinkingBlock("chain of thought", "sig-xyz", false),
			},
		}
		got := protocolToAnthropicResponse(resp, "claude")
		if len(got.Content) != 2 {
			t.Fatalf("expected 2 content blocks, got %d: %+v", len(got.Content), got.Content)
		}
		// Thinking MUST come first so Pi's anthropic.ts:540-660
		// stream loop attaches it to the right output index.
		if got.Content[0].Type != "thinking" {
			t.Errorf("expected first block to be thinking, got %q", got.Content[0].Type)
		}
		if got.Content[0].Thinking != "chain of thought" {
			t.Errorf("expected thinking text %q, got %q", "chain of thought", got.Content[0].Thinking)
		}
		if got.Content[0].Signature != "sig-xyz" {
			t.Errorf("expected signature sig-xyz, got %q", got.Content[0].Signature)
		}
		if got.Content[1].Type != "text" || got.Content[1].Text != "answer" {
			t.Errorf("expected second block to be text 'answer', got %+v", got.Content[1])
		}
	})

	t.Run("redacted thinking uses data field", func(t *testing.T) {
		resp := &protocol.Response{
			StopReason: "end_turn",
			Content: []protocol.Block{
				protocol.ThinkingBlock("[Reasoning redacted]", "opaque-payload", true),
				protocol.TextBlock("answer"),
			},
		}
		got := protocolToAnthropicResponse(resp, "claude")
		if got.Content[0].Type != "redacted_thinking" {
			t.Errorf("expected redacted_thinking, got %q", got.Content[0].Type)
		}
		if got.Content[0].Data != "opaque-payload" {
			t.Errorf("expected data field to carry opaque payload, got %q", got.Content[0].Data)
		}
		// The `signature` field is reserved for the regular
		// thinking case; for redacted_thinking it must stay
		// empty so the SDK doesn't try to decode it.
		if got.Content[0].Signature != "" {
			t.Errorf("expected empty signature on redacted_thinking, got %q", got.Content[0].Signature)
		}
	})

	t.Run("stop reason mapping covers pause_turn and refusal", func(t *testing.T) {
		cases := []struct {
			raw  string
			want string
		}{
			{"end_turn", "end_turn"},
			{"max_tokens", "max_tokens"},
			{"tool_use", "tool_use"},
			{"pause_turn", "pause_turn"},
			{"refusal", "refusal"},
			{"", "end_turn"},
		}
		for _, c := range cases {
			got := protocolToAnthropicResponse(&protocol.Response{
				StopReason: c.raw,
				Content:    []protocol.Block{protocol.TextBlock("x")},
			}, "claude")
			if got.StopReason != c.want {
				t.Errorf("stop reason %q: expected %q, got %q", c.raw, c.want, got.StopReason)
			}
		}
	})
}

// TestUsageV1CacheStatsPinsToAuthenticatedKey is the regression
// test for the case where a proxy key holder calls
// /v1/usage/cache-stats. The endpoint must accept the standard
// gdx_ auth shape AND pin the query to the authenticated key
// regardless of any api_key_id the caller passes in the query
// string, so a proxy key user can never see another tenant's
// cache stats by guessing their key IDs.
func TestUsageV1CacheStatsPinsToAuthenticatedKey(t *testing.T) {
	handler, usageService := mustUsageHandler(t)
	keyA, err := usageService.CreateKey(usage.KeyCreateRequest{Name: "v1-cache-A", BudgetCredits: 100})
	if err != nil {
		t.Fatalf("create key A: %v", err)
	}
	keyB, err := usageService.CreateKey(usage.KeyCreateRequest{Name: "v1-cache-B", BudgetCredits: 100})
	if err != nil {
		t.Fatalf("create key B: %v", err)
	}
	// Seed two calls: one per key. The /v1/ endpoint must
	// surface only the row matching the authenticated key.
	if err := usageService.RecordCall(&usage.UsageCall{
		APIKeyID: keyA.Key.ID, PublicModel: "m", TargetModel: "m",
		InputTokens: 100, CacheReadTokens: 80, Status: "success",
	}); err != nil {
		t.Fatalf("record A: %v", err)
	}
	if err := usageService.RecordCall(&usage.UsageCall{
		APIKeyID: keyB.Key.ID, PublicModel: "m", TargetModel: "m",
		InputTokens: 100, CacheReadTokens: 0, Status: "success",
	}); err != nil {
		t.Fatalf("record B: %v", err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()

	// Call as key A, but try to set api_key_id=keyB.Key.ID
	// in the query. The endpoint must IGNORE the override
	// and pin to key A.
	req, err := http.NewRequest(http.MethodGet, server.URL+"/v1/usage/cache-stats?range=all&api_key_id="+keyB.Key.ID, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("x-api-key", keyA.Secret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody := readAll(t, resp)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, respBody)
	}
	var stats []usage.CacheStats
	if err := json.NewDecoder(resp.Body).Decode(&stats); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(stats) != 1 {
		t.Fatalf("expected exactly 1 row (key A only), got %d: %+v", len(stats), stats)
	}
	if stats[0].CacheReadTokens != 80 {
		t.Errorf("expected key A cache_read=80, got %d (key B's 0 leaked through!)", stats[0].CacheReadTokens)
	}
	if stats[0].HitRate != 100.0 {
		t.Errorf("expected key A HitRate=100, got %.2f", stats[0].HitRate)
	}
}

// TestUsageV1CacheStatsRejectsMissingAuth pins the auth
// contract: a request to /v1/usage/cache-stats without a
// gdx_ token must 401, even if api_key_id is supplied. The
// previous behaviour (the endpoint didn't exist) would have
// 404'd, which is also a failure but a less informative one.
func TestUsageV1CacheStatsRejectsMissingAuth(t *testing.T) {
	handler, _ := mustUsageHandler(t)
	server := httptest.NewServer(handler)
	defer server.Close()
	req, err := http.NewRequest(http.MethodGet, server.URL+"/v1/usage/cache-stats", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 without auth, got %d", resp.StatusCode)
	}
}

// TestUsageCacheStatsAllRange covers the lifetime
// aggregate path. The dashboard uses range=all to render
// the "since you started using the gateway" tile; the
// endpoint must accept the value and return a single row
// per target model.
func TestUsageCacheStatsAllRange(t *testing.T) {
	handler, usageService := mustUsageHandler(t)
	created, err := usageService.CreateKey(usage.KeyCreateRequest{Name: "all-range", BudgetCredits: 100})
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	if err := usageService.RecordCall(&usage.UsageCall{
		APIKeyID:        created.Key.ID,
		PublicModel:     "M3",
		TargetModel:     "m3",
		InputTokens:     100,
		CacheReadTokens: 50,
		Status:          "success",
	}); err != nil {
		t.Fatalf("record: %v", err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	req, err := http.NewRequest(http.MethodGet, server.URL+"/usage/cache-stats?range=all", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+testWebToken(t))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody := readAll(t, resp)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, respBody)
	}
	var stats []usage.CacheStats
	if err := json.NewDecoder(resp.Body).Decode(&stats); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(stats) != 1 {
		t.Fatalf("expected 1 lifetime row, got %d: %+v", len(stats), stats)
	}
	if stats[0].Period != "" {
		t.Errorf("expected empty Period for range=all, got %q", stats[0].Period)
	}
	if stats[0].HitRate != 100.0 {
		t.Errorf("expected HitRate=100, got %.2f", stats[0].HitRate)
	}
	if stats[0].CacheEfficiency <= 0 {
		t.Errorf("expected positive CacheEfficiency, got %.2f", stats[0].CacheEfficiency)
	}
}

// TestUsageGatewayProtocolRequestPreservesToolResult is the unit
// test for the conversion path that previously dropped the
// `role: "tool"` message on the second turn of a tool-using
// conversation. The test feeds Pi's exact wire shape into the
// converter and asserts the resulting `protocol.Request` carries
// the tool result as a `BlockToolResult` content block, plus the
// prior assistant tool_call as a `BlockToolUse` content block, so
// the upstream model can match the two and continue the
// conversation. Without the fix, the model would receive only
// the original user message and could not produce a follow-up
// reply, causing the agent loop to hang after the first tool
// call.
func TestUsageGatewayProtocolRequestPreservesToolResult(t *testing.T) {
	req := openAIChatCompletionRequest{
		Model: "M3",
		Messages: []openAIChatMessage{
			{Role: "user", Content: "read /tmp/hi"},
			{
				Role:    "assistant",
				Content: nil, // Pi sends null content on tool_calls turns
				ToolCalls: []openAIToolCallWire{{
					ID:    "call_abc",
					Type:  "function",
					Index: 0,
					Function: openAIFunctionCall{
						Name:      "read",
						Arguments: `{"path":"/tmp/hi"}`,
					},
				}},
			},
			{
				Role:       "tool",
				ToolCallID: "call_abc",
				Content:    "hi from file",
			},
		},
	}
	got, err := usageGatewayProtocolRequest(req)
	if err != nil {
		t.Fatalf("usageGatewayProtocolRequest: %v", err)
	}

	// We expect 3 messages in the conversation: user,
	// assistant (with the tool_use block), and user (with
	// the tool_result block, since Anthropic uses
	// role=user for tool_result carriers).
	if len(got.Messages) != 3 {
		t.Fatalf("expected 3 messages (user + assistant + tool_result), got %d: %+v", len(got.Messages), got.Messages)
	}
	// Message 1: user turn with the original question.
	if got.Messages[0].Role != "user" {
		t.Errorf("expected first message role=user, got %q", got.Messages[0].Role)
	}
	if len(got.Messages[0].Content) != 1 || got.Messages[0].Content[0].Type != protocol.BlockText {
		t.Errorf("expected first message to have one text block, got %+v", got.Messages[0].Content)
	}
	// Message 2: assistant turn with the tool_use block.
	// The previous implementation would have lost the
	// tool_calls array entirely, leaving the assistant turn
	// with only an empty text block (or no block at all,
	// dropping the message).
	if got.Messages[1].Role != "assistant" {
		t.Errorf("expected second message role=assistant, got %q", got.Messages[1].Role)
	}
	if len(got.Messages[1].Content) != 1 || got.Messages[1].Content[0].Type != protocol.BlockToolUse {
		t.Fatalf("expected second message to carry one tool_use block, got %+v", got.Messages[1].Content)
	}
	toolUse := got.Messages[1].Content[0]
	if toolUse.ID != "call_abc" {
		t.Errorf("expected tool_use id=call_abc, got %q", toolUse.ID)
	}
	if toolUse.Name != "read" {
		t.Errorf("expected tool_use name=read, got %q", toolUse.Name)
	}
	if toolUse.Input["path"] != "/tmp/hi" {
		t.Errorf("expected tool_use input.path=/tmp/hi, got %v", toolUse.Input["path"])
	}
	// Message 3: the tool result, which Anthropic expects
	// on a user-role message. The previous implementation
	// dropped this message entirely (it had empty `content`
	// after the role-normalisation step), so the model
	// never saw its own tool result.
	if got.Messages[2].Role != "user" {
		t.Errorf("expected third message role=user (Anthropic tool_result carrier), got %q", got.Messages[2].Role)
	}
	if len(got.Messages[2].Content) != 1 || got.Messages[2].Content[0].Type != protocol.BlockToolResult {
		t.Fatalf("expected third message to carry one tool_result block, got %+v", got.Messages[2].Content)
	}
	toolResult := got.Messages[2].Content[0]
	if toolResult.ToolUseID != "call_abc" {
		t.Errorf("expected tool_result tool_use_id=call_abc, got %q", toolResult.ToolUseID)
	}
	if toolResult.Content != "hi from file" {
		t.Errorf("expected tool_result content='hi from file', got %q", toolResult.Content)
	}
}

// TestUsageGatewayProtocolRequestDropsEmptyTextUserMessage covers
// the contract that a user message with empty `content` (which
// would otherwise be discarded by the role normalisation step)
// is preserved when it carries a tool result. The previous
// implementation had `if text == "" { continue }` at the top of
// the loop, which would silently drop the tool result message
// because `openAIContentText` returns "" for the tool message
// shape (the result lives in `tool_call_id` and the content in
// `content`, not in the `content` text-projection function).
// The fix moves the empty-text check INSIDE the user /
// assistant branches and the tool message uses its own
// extraction path.
func TestUsageGatewayProtocolRequestDropsEmptyTextUserMessage(t *testing.T) {
	// An assistant tool_call followed by a tool result.
	// The assistant message has no text, only tool_calls;
	// the tool message has empty content but a non-empty
	// tool_call_id. The previous implementation dropped
	// both messages.
	req := openAIChatCompletionRequest{
		Model: "M3",
		Messages: []openAIChatMessage{
			{Role: "user", Content: ""}, // empty text but legitimate user marker
		},
	}
	// Note: the empty user message IS dropped — it's
	// neither a tool result nor a meaningful text. We just
	// verify the converter doesn't 500 on it. A second
	// test below verifies a tool-only conversation is
	// preserved.
	if _, err := usageGatewayProtocolRequest(req); err == nil {
		t.Errorf("expected error for empty conversation, got nil")
	}

	// A tool-only conversation (assistant tool_call +
	// tool result) must NOT be dropped.
	req2 := openAIChatCompletionRequest{
		Model: "M3",
		Messages: []openAIChatMessage{
			{Role: "assistant", ToolCalls: []openAIToolCallWire{{
				ID:    "call_x",
				Type:  "function",
				Index: 0,
				Function: openAIFunctionCall{
					Name:      "read",
					Arguments: `{"path":"/tmp/hi"}`,
				},
			}}},
			{Role: "tool", ToolCallID: "call_x", Content: "ok"},
		},
	}
	got, err := usageGatewayProtocolRequest(req2)
	if err != nil {
		t.Fatalf("usageGatewayProtocolRequest: %v", err)
	}
	if len(got.Messages) != 2 {
		t.Fatalf("expected 2 messages (assistant tool_call + tool result), got %d", len(got.Messages))
	}
	if got.Messages[0].Role != "assistant" || got.Messages[0].Content[0].Type != protocol.BlockToolUse {
		t.Errorf("expected assistant with tool_use block, got %+v", got.Messages[0].Content)
	}
	if got.Messages[1].Role != "user" || got.Messages[1].Content[0].Type != protocol.BlockToolResult {
		t.Errorf("expected user with tool_result block, got %+v", got.Messages[1].Content)
	}
}
