package conversation

import (
	"context"
	"fmt"
	"testing"

	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/llm"
	"github.com/tim5wang/godex/internal/core/protocol"
)

type fallbackTestCaller struct {
	err     error
	models  *[]string
	efforts *[]string
}

func (c fallbackTestCaller) Call(ctx context.Context, req protocol.Request) (*protocol.Response, error) {
	_ = ctx
	*c.models = append(*c.models, req.Model)
	if c.efforts != nil {
		*c.efforts = append(*c.efforts, req.ReasoningEffort)
	}
	if c.err != nil {
		return nil, c.err
	}
	return &protocol.Response{Content: []protocol.Block{protocol.TextBlock("ok")}}, nil
}

func TestFallbackCallerRetriesRetryableErrorsWithNextProfile(t *testing.T) {
	models := []string{}
	caller := &fallbackCaller{
		profiles: []config.ModelProfileConfig{
			{ID: "primary", Model: "model-a", MaxTokens: 1},
			{ID: "backup", Model: "model-b", MaxTokens: 2},
		},
		callers: []Caller{
			fallbackTestCaller{err: fmt.Errorf("timeout"), models: &models},
			fallbackTestCaller{models: &models},
		},
	}
	resp, err := caller.Call(context.Background(), protocol.Request{Model: "original", MaxTokens: 99})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if got := fallbackTestText(resp.Content); got != "ok" {
		t.Fatalf("unexpected response: %q", got)
	}
	if len(models) != 2 || models[0] != "model-a" || models[1] != "model-b" {
		t.Fatalf("expected profile models in order, got %#v", models)
	}
}

func TestFallbackCallerAppliesProfileReasoningEffort(t *testing.T) {
	models := []string{}
	efforts := []string{}
	caller := &fallbackCaller{
		profiles: []config.ModelProfileConfig{
			{ID: "primary", Model: "model-a", MaxTokens: 1, ReasoningEffort: "low"},
			{ID: "backup", Model: "model-b", MaxTokens: 2, ReasoningEffort: "high"},
		},
		callers: []Caller{
			fallbackTestCaller{err: fmt.Errorf("timeout"), models: &models, efforts: &efforts},
			fallbackTestCaller{models: &models, efforts: &efforts},
		},
	}
	resp, err := caller.Call(context.Background(), protocol.Request{Model: "original", MaxTokens: 99, ReasoningEffort: "medium"})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if got := fallbackTestText(resp.Content); got != "ok" {
		t.Fatalf("unexpected response: %q", got)
	}
	if len(efforts) != 2 || efforts[0] != "low" || efforts[1] != "high" {
		t.Fatalf("expected profile reasoning efforts in order, got %#v", efforts)
	}
}

func fallbackTestText(blocks []protocol.Block) string {
	text := ""
	for _, block := range blocks {
		if block.Type == "text" {
			text += block.Text
		}
	}
	return text
}

func TestFallbackCallerDoesNotRetryNonRetryableErrors(t *testing.T) {
	models := []string{}
	caller := &fallbackCaller{
		profiles: []config.ModelProfileConfig{
			{ID: "primary", Model: "model-a"},
			{ID: "backup", Model: "model-b"},
		},
		callers: []Caller{
			fallbackTestCaller{err: fmt.Errorf("invalid request"), models: &models},
			fallbackTestCaller{models: &models},
		},
	}
	if _, err := caller.Call(context.Background(), protocol.Request{}); err == nil {
		t.Fatal("expected error")
	}
	if len(models) != 1 || models[0] != "model-a" {
		t.Fatalf("expected no fallback, got %#v", models)
	}
}

func TestFallbackCallerRoundRobinRotatesPrimaryCandidate(t *testing.T) {
	models := []string{}
	caller := &fallbackCaller{
		strategy: llm.StrategyRoundRobin,
		profiles: []config.ModelProfileConfig{
			{ID: "primary", Model: "model-a"},
			{ID: "backup", Model: "model-b"},
		},
		callers: []Caller{
			fallbackTestCaller{models: &models},
			fallbackTestCaller{models: &models},
		},
	}
	if _, err := caller.Call(context.Background(), protocol.Request{}); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if _, err := caller.Call(context.Background(), protocol.Request{}); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if len(models) != 2 || models[0] != "model-a" || models[1] != "model-b" {
		t.Fatalf("expected round-robin order, got %#v", models)
	}
}
