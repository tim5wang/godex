package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/decision"
	"github.com/tim5wang/godex/internal/core/llm"
)

// staticConversationCaller is a minimal conversation.Caller for wiring tests.
type staticConversationCaller struct{}

func (staticConversationCaller) Call(ctx context.Context, req protocol.Request) (*protocol.Response, error) {
	return &protocol.Response{Content: []protocol.Block{protocol.TextBlock(`{"choice":"auto","confidence":0.9}`)}}, nil
}

func TestBuildDecisionCallerWiring(t *testing.T) {
	client := staticConversationCaller{}

	// Disabled config yields nil (decision nodes fail_closed).
	disabled := &config.Config{Decision: config.DecisionConfig{Enabled: false}}
	if got := buildDecisionCaller(disabled, client); got != nil {
		t.Fatalf("expected nil caller when decision disabled, got %#v", got)
	}

	// Missing chat client yields nil even when enabled.
	enabled := &config.Config{Decision: config.DecisionConfig{Enabled: true, Provider: "llm", TimeoutMS: 500, MaxTokens: 32}}
	if got := buildDecisionCaller(enabled, nil); got != nil {
		t.Fatalf("expected nil caller when chat client missing, got %#v", got)
	}

	// Enabled + client yields a working caller whose defaults come from config.
	caller := buildDecisionCaller(enabled, client)
	if caller == nil {
		t.Fatal("expected non-nil caller when decision enabled and client present")
	}
	res, err := caller.Decide(context.Background(), decision.Request{
		Provider:     "llm",
		Question:     "proceed?",
		DecisionType: decision.TypeChoice,
		Choices:      []decision.Choice{{ID: "auto"}, {ID: "stop"}},
	})
	if err != nil {
		t.Fatalf("decide: %v", err)
	}
	if res.Choice != "auto" {
		t.Fatalf("expected choice auto, got %q", res.Choice)
	}

	// Nil config is tolerated and yields nil.
	if got := buildDecisionCaller(nil, client); got != nil {
		t.Fatalf("expected nil caller for nil config, got %#v", got)
	}
}

// TestBuildDecisionCallerJevRouting verifies that when decision.provider
// names a laya_jev provider, buildDecisionCaller wires the native Jev caller
// (straight to the laya /predict endpoint) instead of the chat LLM adapter.
func TestBuildDecisionCallerJevRouting(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path != "/predict" {
			t.Errorf("expected /predict, got %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"rl-agent","answers":{"q1":{"type":"choice","choice":"auto","confidence":0.9}}}`))
	}))
	defer srv.Close()

	cfg := &config.Config{
		Decision: config.DecisionConfig{Enabled: true, Provider: "laya", TimeoutMS: 500, MaxTokens: 32},
		LLMProviders: map[string]llm.ProviderConfig{
			"laya": {Type: llm.ProviderLaya, BaseURL: srv.URL},
		},
	}
	caller := buildDecisionCaller(cfg, staticConversationCaller{})
	if caller == nil {
		t.Fatal("expected non-nil Jev caller")
	}
	res, err := caller.Decide(context.Background(), decision.Request{
		Provider:     "laya",
		Question:     "auto process?",
		DecisionType: decision.TypeChoice,
		Choices:      []decision.Choice{{ID: "auto"}, {ID: "manual"}},
	})
	if err != nil {
		t.Fatalf("decide via jev: %v", err)
	}
	if res.Choice != "auto" {
		t.Fatalf("expected choice auto, got %q", res.Choice)
	}
	if hits != 1 {
		t.Fatalf("expected 1 laya /predict call, got %d (chat adapter would not hit /predict)", hits)
	}

	// Unknown provider type (or missing provider) falls back to the LLM
	// adapter — no /predict hit.
	cfg2 := &config.Config{
		Decision: config.DecisionConfig{Enabled: true, Provider: "other", TimeoutMS: 500, MaxTokens: 32},
		LLMProviders: map[string]llm.ProviderConfig{
			"other": {Type: "openai_compatible", BaseURL: srv.URL},
		},
	}
	if got := buildDecisionCaller(cfg2, staticConversationCaller{}); got == nil {
		t.Fatal("expected LLM-adapter caller for non-laya provider")
	}
}
