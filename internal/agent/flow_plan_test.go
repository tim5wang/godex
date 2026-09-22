package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/core/flow"
)

// TestParseFlowSpecFromLLMPlain verifies a plain JSON object (no fences) parses.
func TestParseFlowSpecFromLLMPlain(t *testing.T) {
	text := `{"flow_id": "fl_refund", "version": "1", "status": "draft", "nodes": [{"id": "a", "kind": "step", "prompt": "do"}]}`
	def, err := parseFlowSpecFromLLM(text)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if def.FlowID != "fl_refund" || len(def.Nodes) != 1 {
		t.Fatalf("unexpected def: %+v", def)
	}
}

// TestParseFlowSpecFromLLMFence verifies a markdown-fenced + prose-wrapped
// response still parses (LLM output tolerance).
func TestParseFlowSpecFromLLMFence(t *testing.T) {
	text := "Here is the flow:\n```json\n{\"flow_id\": \"fl_x\", \"version\": \"1\", \"status\": \"draft\", \"nodes\": []}\n```\nHope that helps."
	def, err := parseFlowSpecFromLLM(text)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if def.FlowID != "fl_x" {
		t.Fatalf("unexpected def: %+v", def)
	}
}

// TestParseFlowSpecFromLLMGarbage verifies a non-JSON response is rejected.
func TestParseFlowSpecFromLLMGarbage(t *testing.T) {
	if _, err := parseFlowSpecFromLLM("I cannot do that."); err == nil {
		t.Fatal("expected parse error for non-JSON response")
	}
}

// TestGenerateFlowSpecValid verifies the LLM-generated definition is parsed
// and validated (a real Flow Spec with nodes passes Validate).
func TestGenerateFlowSpecValid(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.client = repeatedTextCaller(`{"flow_id": "fl_gen", "version": "1", "status": "draft",
	  "nodes": [
	    {"id": "classify", "kind": "step", "prompt": "classify the request"},
	    {"id": "decide", "kind": "decision", "prompt": "auto?",
	      "decision": {"decision_type": "choice", "choices": [{"id": "auto"}, {"id": "manual"}]}},
	    {"id": "br", "kind": "branch",
	      "branch": {"cases": [{"name": "auto", "to": "done", "condition": {"choice": "auto"}}], "default_to": "done"}},
	    {"id": "done", "kind": "step", "prompt": "finalize"}
	  ],
	  "edges": [
	    {"id": "e1", "from": "classify", "to": "decide", "edge_type": "data_dependency"},
	    {"id": "e2", "from": "decide", "to": "br", "edge_type": "data_dependency"}
	  ]
	}`)
	def, err := a.GenerateFlowSpec(context.Background(), "退款流程：自动退款或转人工")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if def.FlowID != "fl_gen" || len(def.Nodes) != 4 {
		t.Fatalf("unexpected generated def: %+v", def)
	}
}

// TestGenerateFlowSpecInvalid verifies a generated definition that fails
// validation (unknown node reference) is rejected instead of saved.
func TestGenerateFlowSpecInvalid(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.client = repeatedTextCaller(`{"flow_id": "fl_bad", "version": "1", "status": "draft",
	  "nodes": [{"id": "a", "kind": "step", "prompt": "do"}],
	  "edges": [{"id": "e1", "from": "a", "to": "ghost", "edge_type": "data_dependency"}]
	}`)
	if _, err := a.GenerateFlowSpec(context.Background(), "bad flow"); err == nil {
		t.Fatal("expected validation error for unknown edge target")
	}
}

// TestGenerateFlowSpecEmpty verifies empty/no-node responses are rejected.
func TestGenerateFlowSpecEmpty(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.client = repeatedTextCaller(`{"flow_id": "fl_empty", "version": "1", "status": "draft", "nodes": []}`)
	if _, err := a.GenerateFlowSpec(context.Background(), "empty"); err == nil || !strings.Contains(err.Error(), "no nodes") {
		t.Fatalf("expected no-nodes error, got %v", err)
	}
}

// TestAmendFlowSpecValid verifies incremental modification: current definition
// + change request → LLM returns the FULL amended definition (validated, not
// saved).
func TestAmendFlowSpecValid(t *testing.T) {
	a := newTestAgent(t, 4096)
	current := &flow.Definition{
		FlowID: "fl_refund", Version: "1", Status: "draft",
		Nodes: []flow.Node{
			{ID: "classify", Kind: "step", Prompt: "classify"},
			{ID: "done", Kind: "step", Prompt: "finalize"},
		},
		Edges: []flow.Edge{{From: "classify", To: "done"}},
	}
	// LLM returns the amended definition with an added human node.
	a.client = repeatedTextCaller(`{"flow_id": "fl_refund", "version": "1", "status": "draft",
	  "nodes": [
	    {"id": "classify", "kind": "step", "prompt": "classify"},
	    {"id": "review", "kind": "human", "prompt": "review", "human": {"queue": "ops", "assignee_policy": "any", "result_var": "approved"}},
	    {"id": "done", "kind": "step", "prompt": "finalize"}
	  ],
	  "edges": [
	    {"from": "classify", "to": "review"},
	    {"from": "review", "to": "done"}
	  ]}`)
	def, err := a.AmendFlowSpec(context.Background(), current, "金额超过 1000 元需要人工审批")
	if err != nil {
		t.Fatalf("amend: %v", err)
	}
	if def.FlowID != "fl_refund" || len(def.Nodes) != 3 {
		t.Fatalf("expected amended def with 3 nodes, got %+v", def)
	}
	humanFound := false
	for _, n := range def.Nodes {
		if n.Kind == "human" {
			humanFound = true
		}
	}
	if !humanFound {
		t.Fatalf("expected human node in amended def, got %+v", def.Nodes)
	}
}

// TestAmendFlowSpecInvalid verifies an amended definition that fails
// validation (unknown edge target) is rejected, never silently accepted.
func TestAmendFlowSpecInvalid(t *testing.T) {
	a := newTestAgent(t, 4096)
	current := &flow.Definition{
		FlowID: "fl_refund", Version: "1", Status: "draft",
		Nodes: []flow.Node{{ID: "a", Kind: "step", Prompt: "do"}},
	}
	a.client = repeatedTextCaller(`{"flow_id": "fl_refund", "version": "1", "status": "draft",
	  "nodes": [{"id": "a", "kind": "step", "prompt": "do"}],
	  "edges": [{"from": "a", "to": "ghost", "edge_type": "data_dependency"}]}`)
	if _, err := a.AmendFlowSpec(context.Background(), current, "改动"); err == nil {
		t.Fatal("expected validation error for unknown edge target")
	}
}

// TestAmendFlowSpecEmpty verifies missing change / definition are rejected.
func TestAmendFlowSpecEmpty(t *testing.T) {
	a := newTestAgent(t, 4096)
	if _, err := a.AmendFlowSpec(context.Background(), nil, "change"); err == nil {
		t.Fatal("expected error for nil current definition")
	}
	if _, err := a.AmendFlowSpec(context.Background(), &flow.Definition{}, "  "); err == nil {
		t.Fatal("expected error for empty change request")
	}
}
