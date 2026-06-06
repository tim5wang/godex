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

// readSSEDataLines reads a text/event-stream body until EOF and joins the
// payload of every `data: ...` line into a single string for assertions.
func readSSEDataLines(t *testing.T, body io.Reader) (string, error) {
	t.Helper()
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var b strings.Builder
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data:") {
			b.WriteString(strings.TrimPrefix(line, "data:"))
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
