package flow

import (
	"strings"
	"testing"
)

// humanFlow returns a flow with a human fallback node ("approve") between a
// start step and an end step, plus a decision that routes to it.
func humanFlow() *Definition {
	return &Definition{
		FlowID:  "fl_human_check",
		Version: "1",
		Status:  "draft",
		Nodes: []Node{
			{ID: "start", Kind: KindStep, Prompt: "classify the order"},
			{ID: "decide", Kind: KindDecision, Prompt: "high risk?",
				Decision: &DecisionSpec{DecisionType: "choice", Choices: []Choice{{ID: "yes"}, {ID: "no"}}}},
			{ID: "approve", Kind: KindHuman, Prompt: "Approve this high-risk refund",
				Human: &HumanSpec{Queue: "ops", TimeoutMS: 60000, OnTimeout: "escalate:managers"}},
			{ID: "end", Kind: KindStep, Prompt: "finalize"},
		},
		Edges: []Edge{
			{ID: "e1", From: "start", To: "decide", EdgeType: EdgeDataDependency},
			{ID: "e2", From: "decide", To: "approve", EdgeType: EdgeDataDependency},
			{ID: "e3", From: "approve", To: "end", EdgeType: EdgeDataDependency},
		},
	}
}

func TestValidateAllowsHumanNode(t *testing.T) {
	if err := Validate(humanFlow()); err != nil {
		t.Fatalf("Validate should allow a valid human node, got %v", err)
	}
}

func TestValidateRejectsHumanMissingSpec(t *testing.T) {
	d := humanFlow()
	d.Nodes[2].Human = nil
	err := Validate(d)
	if err == nil {
		t.Fatal("expected human node without spec to be rejected")
	}
	if !strings.Contains(err.Error(), "human") {
		t.Fatalf("error should mention human spec, got %v", err)
	}
}

func TestValidateRejectsHumanMissingQueue(t *testing.T) {
	d := humanFlow()
	d.Nodes[2].Human.Queue = ""
	err := Validate(d)
	if err == nil || !strings.Contains(err.Error(), "queue") {
		t.Fatalf("expected missing queue error, got %v", err)
	}
}

func TestValidateRejectsHumanInvalidOnTimeout(t *testing.T) {
	d := humanFlow()
	d.Nodes[2].Human.OnTimeout = "nonsense"
	err := Validate(d)
	if err == nil || !strings.Contains(err.Error(), "on_timeout") {
		t.Fatalf("expected invalid on_timeout error, got %v", err)
	}
}

func TestValidateRejectsHumanEscalateWithoutTarget(t *testing.T) {
	d := humanFlow()
	d.Nodes[2].Human.OnTimeout = "escalate:"
	err := Validate(d)
	if err == nil || !strings.Contains(err.Error(), "escalate") {
		t.Fatalf("expected escalate target error, got %v", err)
	}
}

func TestCompileCarriesHumanSpec(t *testing.T) {
	c, err := Compile(humanFlow())
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	byID := map[string]CompiledNode{}
	for _, n := range c.Nodes {
		byID[n.ID] = n
	}
	approve, ok := byID["approve"]
	if !ok {
		t.Fatalf("missing static human node approve: %+v", c.Nodes)
	}
	if approve.Kind != KindHuman {
		t.Fatalf("expected human kind, got %q", approve.Kind)
	}
	if approve.Human == nil {
		t.Fatal("expected human spec carried into compiled node")
	}
	if approve.Human.Queue != "ops" || approve.Human.OnTimeout != "escalate:managers" {
		t.Fatalf("unexpected human spec: %+v", approve.Human)
	}
}
