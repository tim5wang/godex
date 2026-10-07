package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/core/compress"
	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/decision"
	"github.com/tim5wang/godex/internal/core/llm"
)

type compactionBatchStub struct {
	reqs []decision.Request
}

func (c *compactionBatchStub) DecideBatch(_ context.Context, reqs []decision.Request) ([]decision.Result, error) {
	c.reqs = append([]decision.Request(nil), reqs...)
	out := make([]decision.Result, len(reqs))
	for i := range out {
		out[i].Score = float64(i) / 10
	}
	return out, nil
}

type failingJevHistoryCaller struct{}

func (failingJevHistoryCaller) DecideToolHistory(context.Context, string, []string) ([]float64, error) {
	return nil, errors.New("jev unavailable")
}

func TestJevCompactionAdapterUsesSharedStateAndBooleanQuestions(t *testing.T) {
	stub := &compactionBatchStub{}
	scores, err := (jevCompactionAdapter{caller: stub}).DecideToolHistory(context.Background(), "shared", []string{"q1", "q2"})
	if err != nil {
		t.Fatalf("adapter: %v", err)
	}
	if len(scores) != 2 || scores[1] != 0.1 || len(stub.reqs) != 2 {
		t.Fatalf("unexpected adapter scores/requests: %v %+v", scores, stub.reqs)
	}
	for _, req := range stub.reqs {
		if req.State != "shared" || req.DecisionType != decision.TypeBoolean {
			t.Fatalf("unexpected decision request: %+v", req)
		}
	}
}

func TestBuildJevCompactionCallerOnlyResolvesLayaProvider(t *testing.T) {
	if got := buildJevCompactionCaller(&config.Config{}); got != nil {
		t.Fatalf("expected no Jev compaction caller for unconfigured provider, got %T", got)
	}
	cfg := &config.Config{
		Decision: config.DecisionConfig{Provider: "cheap"},
		LLMProviders: map[string]llm.ProviderConfig{
			"cheap": {Type: llm.ProviderLaya, BaseURL: "http://127.0.0.1:8100"},
		},
	}
	if got := buildJevCompactionCaller(cfg); got == nil {
		t.Fatal("expected Jev compaction caller for Laya provider")
	}
}

func TestJevCompactionModeAndFailureFallback(t *testing.T) {
	a := newTestAgent(t, 100000)
	a.compressor.SetKeepRecent(1)
	a.compressor.SetRetainTokens(1)
	a.jevCompactionCaller = failingJevHistoryCaller{}
	summarizer, mode := a.compactionSummarizer("jev")
	if mode != "jev" {
		t.Fatalf("expected Jev compaction mode, got %q", mode)
	}
	if _, ok := summarizer.(*compress.JevSessionSummarizer); !ok {
		t.Fatalf("expected Jev summarizer, got %T", summarizer)
	}

	history := []protocol.Message{
		protocol.NewTextMessage(protocol.RoleUser, "Original goal"),
		protocol.NewMessage(protocol.RoleAssistant, protocol.ToolUseBlock("tool-1", "read_file", map[string]interface{}{"path": "x"})),
		protocol.NewMessage(protocol.RoleUser, protocol.ToolResultBlock("tool-1", strings.Repeat("old result ", 80))),
		protocol.NewTextMessage(protocol.RoleUser, "Latest request"),
	}
	result, err := a.runCompaction(context.Background(), "jev", compress.SessionSummaryRequest{History: history})
	if err != nil {
		t.Fatalf("Jev failure should fall back to rule compression: %v", err)
	}
	if result.Mode != "fast" || len(result.Messages) == 0 || result.Messages[0].Metadata == nil || result.Messages[0].Metadata.Kind != protocol.KindSummary {
		t.Fatalf("expected fast fallback summary, got %+v", result)
	}
}

func TestJevCompactionModeWithoutProviderUsesRules(t *testing.T) {
	a := newTestAgent(t, 100000)
	summarizer, mode := a.compactionSummarizerFor("jev", a.compressor)
	if mode != "fast" {
		t.Fatalf("missing Jev provider should report fallback mode, got %q", mode)
	}
	if _, ok := summarizer.(*compress.RuleBasedSessionSummarizer); !ok {
		t.Fatalf("missing Jev provider should use rule summarizer, got %T", summarizer)
	}
}
