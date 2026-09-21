package agent

import (
	"context"
	"testing"

	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/decision"
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
