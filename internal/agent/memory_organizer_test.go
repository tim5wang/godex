package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/contracts/protocol"
)

type organizerProbeCaller struct {
	request  protocol.Request
	response *protocol.Response
	calls    int
}

func (c *organizerProbeCaller) Call(_ context.Context, r protocol.Request) (*protocol.Response, error) {
	c.request = r
	c.calls++
	return c.response, nil
}

func TestMemoryOrganizerLeavesRoomForReasoningAndUsesNoTools(t *testing.T) {
	a := newTestAgent(t, 4096)
	cfg := *a.cfg
	cfg.MaxTokens = 8192
	cfg.ReasoningEffort = "low"
	probe := &organizerProbeCaller{response: &protocol.Response{Content: []protocol.Block{protocol.TextBlock(`{"summary":"bounded"}`)}, StopReason: "end_turn"}}
	shared := NewSharedDependenciesWithCaller(&cfg, probe)
	output, err := shared.OrganizeMemory(context.Background(), &cfg, "organize scoped evidence", "fixture")
	if err != nil || output != `{"summary":"bounded"}` {
		t.Fatalf("organize: %q %v", output, err)
	}
	if probe.request.MaxTokens <= 2048 || probe.request.MaxTokens > cfg.MaxTokens {
		t.Fatalf("thinking exhausts or exceeds configured budget: %d", probe.request.MaxTokens)
	}
	if len(probe.request.Tools) != 0 || len(probe.request.Messages) != 1 || probe.request.ReasoningEffort != "low" || probe.calls != 1 {
		t.Fatalf("unexpected request: %+v", probe.request)
	}
	cfg.MaxTokens = 1000
	if _, err = shared.OrganizeMemory(context.Background(), &cfg, "organize", "fixture"); err != nil {
		t.Fatal(err)
	}
	if probe.request.MaxTokens > 1000 {
		t.Fatal("organizer exceeded user's limit")
	}
}

func TestMemoryOrganizerRejectsEmptyAndTruncatedOutput(t *testing.T) {
	a := newTestAgent(t, 4096)
	for _, response := range []*protocol.Response{nil, {StopReason: "end_turn"}, {StopReason: "length", Content: []protocol.Block{protocol.TextBlock(`{"summary":"incomplete`)}}} {
		probe := &organizerProbeCaller{response: response}
		shared := NewSharedDependenciesWithCaller(a.cfg, probe)
		output, err := shared.OrganizeMemory(context.Background(), a.cfg, "organize", "fixture")
		if err == nil || output != "" {
			t.Fatalf("accepted incomplete output: %q %v", output, err)
		}
		if response != nil && response.StopReason == "length" && !strings.Contains(err.Error(), "budget") {
			t.Fatalf("truncation lacked actionable cause: %v", err)
		}
	}
}
