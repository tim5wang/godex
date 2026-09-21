package decision

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/contracts/protocol"
)

type staticCaller struct{ text string }

func (c staticCaller) Call(ctx context.Context, req protocol.Request) (*protocol.Response, error) {
	return &protocol.Response{Content: []protocol.Block{protocol.TextBlock(c.text)}}, nil
}

type errCaller struct{ err error }

func (c errCaller) Call(ctx context.Context, req protocol.Request) (*protocol.Response, error) {
	return nil, c.err
}

func TestChoiceDecisionAccepted(t *testing.T) {
	c := NewLLMCaller(LLMCallerOptions{Provider: "jev", Caller: staticCaller{
		text: "```json\n{\"choice\": \"auto\", \"confidence\": 0.93, \"calibrated\": true}\n```",
	}})
	res, err := c.Decide(context.Background(), Request{
		Question:     "classify this refund request",
		DecisionType: TypeChoice,
		Choices:      []Choice{{ID: "auto"}, {ID: "review"}, {ID: "llm"}},
	})
	if err != nil {
		t.Fatalf("decide: %v", err)
	}
	if res.Choice != "auto" || res.Confidence != 0.93 || !res.Calibrated || res.Model != "jev" {
		t.Fatalf("unexpected result: %+v", res)
	}
	if res.Latency <= 0 {
		t.Fatalf("expected latency recorded")
	}
	out := res.Outputs()
	if out["choice"] != "auto" || out["confidence"] != 0.93 {
		t.Fatalf("unexpected outputs: %+v", out)
	}
}

func TestChoiceOutsideSetRejected(t *testing.T) {
	c := NewLLMCaller(LLMCallerOptions{Caller: staticCaller{
		text: `{"choice": "explode", "confidence": 0.99}`,
	}})
	_, err := c.Decide(context.Background(), Request{
		DecisionType: TypeChoice,
		Choices:      []Choice{{ID: "auto"}, {ID: "review"}},
	})
	if !errors.Is(err, ErrInvalidChoice) {
		t.Fatalf("expected ErrInvalidChoice, got %v", err)
	}
}

func TestBooleanDecision(t *testing.T) {
	c := NewLLMCaller(LLMCallerOptions{Caller: staticCaller{
		text: `{"bool": false, "confidence": 0.7}`,
	}})
	res, err := c.Decide(context.Background(), Request{DecisionType: TypeBoolean, Question: "is this high risk?"})
	if err != nil {
		t.Fatalf("decide: %v", err)
	}
	if res.Choice != "false" || res.Confidence != 0.7 {
		t.Fatalf("unexpected boolean result: %+v", res)
	}
}

func TestScoreDecision(t *testing.T) {
	c := NewLLMCaller(LLMCallerOptions{Caller: staticCaller{
		text: `{"score": 0.42, "confidence": 0.8}`,
	}})
	res, err := c.Decide(context.Background(), Request{DecisionType: TypeScore, Question: "rate the risk"})
	if err != nil {
		t.Fatalf("decide: %v", err)
	}
	if res.Score != 0.42 || res.Choice != "" {
		t.Fatalf("unexpected score result: %+v", res)
	}
}

func TestDecisionCallErrorPropagates(t *testing.T) {
	c := NewLLMCaller(LLMCallerOptions{Caller: errCaller{err: context.DeadlineExceeded}})
	_, err := c.Decide(context.Background(), Request{DecisionType: TypeBoolean, Question: "x", Timeout: 10 * time.Millisecond})
	if err == nil || !strings.Contains(err.Error(), "decision model call") {
		t.Fatalf("expected wrapped call error, got %v", err)
	}
}

func TestChoiceRequiresSet(t *testing.T) {
	c := NewLLMCaller(LLMCallerOptions{Caller: staticCaller{text: `{"choice":"auto","confidence":0.9}`}})
	if _, err := c.Decide(context.Background(), Request{DecisionType: TypeChoice, Question: "x"}); err == nil {
		t.Fatalf("expected error for choice decision without choices")
	}
}

func TestNilCallerRejected(t *testing.T) {
	if NewLLMCaller(LLMCallerOptions{}) != nil {
		t.Fatalf("expected nil caller without conversation client")
	}
}
