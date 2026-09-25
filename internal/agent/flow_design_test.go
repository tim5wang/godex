package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/core/flow"
)

// errFlowSpecDraftForTest is a stand-in validation error for unit tests.
var errFlowSpecDraftForTest = errors.New("nodes[br]: unknown node \"ghost\"")

// TestFlowDesignGenerateInvalidDraftHandedBack verifies the flow_design
// generate tool, when the LLM returns a parseable-but-invalid draft, returns a
// ToolResult (not a hard error) carrying the near-correct draft + errors so the
// Agent can continue with amend instead of restarting.
func TestFlowDesignGenerateInvalidDraftHandedBack(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.client = repeatedTextCaller(`{"flow_id": "fl_bad", "version": "1", "status": "draft",
	  "nodes": [{"id": "a", "kind": "step", "prompt": "do"}],
	  "edges": [{"id": "e1", "from": "a", "to": "ghost", "edge_type": "data_dependency"}]
	}`)
	tool := newFlowDesignTool(a)
	out, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":      "generate",
		"description": "bad flow",
	})
	if err != nil {
		t.Fatalf("expected draft-error result (not hard error), got %v", err)
	}
	// Text carries the amend handoff hint.
	if !strings.Contains(out, "action=amend") {
		t.Fatalf("expected amend handoff hint in output, got %q", out)
	}
	if !strings.Contains(out, "unknown node") {
		t.Fatalf("expected validation error listed in output, got %q", out)
	}
}

// TestFlowDesignDraftErrorResultPreservesDraft verifies flowDesignDraftErrorResult
// puts the near-correct draft + raw output into the structured payload so a
// downstream amend can pick it up.
func TestFlowDesignDraftErrorResultPreservesDraft(t *testing.T) {
	draft := &flow.Definition{
		FlowID:  "fl_x",
		Version: "1",
		Nodes:   []flow.Node{{ID: "a", Kind: "step", Prompt: "do"}},
	}
	res := flowDesignDraftErrorResult(&FlowSpecDraftError{
		Label:   "generate flow",
		Draft:   draft,
		Raw:     `{"flow_id": "fl_x", ...}`,
		Cause:   errFlowSpecDraftForTest,
		Attempt: 1,
	})
	m, ok := res.Structured.(map[string]interface{})
	if !ok {
		t.Fatalf("expected structured map, got %T", res.Structured)
	}
	if m["ok"] != false || m["stage"] != "draft" {
		t.Fatalf("expected ok=false stage=draft, got %+v", m)
	}
	if _, hasDraft := m["draft"]; !hasDraft {
		t.Fatal("expected draft preserved in structured payload")
	}
	if raw, _ := m["raw_output"].(string); !strings.Contains(raw, "fl_x") {
		t.Fatalf("expected raw output preserved, got %q", raw)
	}
	// The model-visible Text must embed the full draft JSON (the wire format
	// drops Structured), so amend can pass it back verbatim.
	if !strings.Contains(res.Text, "action=amend") {
		t.Fatalf("expected amend hint in text, got %q", res.Text)
	}
	if !strings.Contains(res.Text, `"flow_id":"fl_x"`) && !strings.Contains(res.Text, `"flow_id": "fl_x"`) {
		t.Fatalf("expected full draft JSON embedded in text, got %q", res.Text)
	}
	if !strings.Contains(res.Text, "unknown node") {
		t.Fatalf("expected validation error listed in text, got %q", res.Text)
	}
}

func TestFlowDesignSuccessTextIncludesFullDefinition(t *testing.T) {
	def := &flow.Definition{
		FlowID: "fl_ready", Name: "Ready Flow", Version: "3", Status: "draft",
		Inputs: []flow.VarDef{{Name: "task", Type: "string"}},
		Nodes:  []flow.Node{{ID: "run", Kind: flow.KindStep, Prompt: "Process {{inputs.task}}"}},
		Edges:  []flow.Edge{},
	}
	result := flowDesignResult(def, "generated")
	for _, want := range []string{
		"Flow Spec JSON:",
		`"flow_id":"fl_ready"`,
		`"name":"Ready Flow"`,
		`"inputs":[{"name":"task","type":"string"}]`,
		`"prompt":"Process {{inputs.task}}"`,
	} {
		if !strings.Contains(result.Text, want) {
			t.Fatalf("expected model-visible output to include %q, got %q", want, result.Text)
		}
	}
}

// TestFlowDesignGenerateUnparsableStillFails verifies a genuinely unusable
// LLM response (no JSON at all) surfaces as a diagnostic ToolResult (ok=false,
// raw_output preserved) instead of a hard error that loses the output — the
// Agent needs the raw text to decide whether a targeted retry is worth it.
func TestFlowDesignGenerateUnparsableStillFails(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.client = repeatedTextCaller("I cannot do that.")
	tool := newFlowDesignTool(a)
	out, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":      "generate",
		"description": "some flow",
	})
	if err != nil {
		t.Fatalf("expected diagnostic result (not hard error), got %v", err)
	}
	if !strings.Contains(out, "I cannot do that.") {
		t.Fatalf("expected raw output preserved in text for diagnostics, got %q", out)
	}
}

// TestFlowInvalidResultActionableContext verifies flow_design validate surfaces
// the concrete error PLUS the declared node/edge inventory, so the Agent can
// spot a dangling reference (e.g. branch case → unknown node) from the result
// alone (复盘 #3: validate 报错需可行动上下文).
func TestFlowInvalidResultActionableContext(t *testing.T) {
	def := &flow.Definition{
		FlowID: "fl_x", Version: "1",
		Nodes: []flow.Node{
			{ID: "a", Kind: "step", Prompt: "do"},
			{ID: "br", Kind: "branch", Branch: &flow.BranchSpec{
				Cases:     []flow.BranchCase{{Name: "go", To: "ghost", Condition: flow.Condition{Choice: "auto"}}},
				DefaultTo: "a",
			}},
		},
		Edges: []flow.Edge{{ID: "e1", From: "a", To: "br", EdgeType: flow.EdgeDataDependency}},
	}
	vErr := flow.Validate(def)
	if vErr == nil {
		t.Fatal("expected validation error for unknown branch target")
	}
	res := flowInvalidResult(def, vErr)
	m, ok := res.Structured.(map[string]interface{})
	if !ok {
		t.Fatalf("expected structured map, got %T", res.Structured)
	}
	if m["valid"] != false {
		t.Fatalf("expected valid=false, got %+v", m)
	}
	// 可行动上下文：已声明节点清单包含 br、已声明边清单包含 e1。
	if !strings.Contains(res.Text, "a(step)") || !strings.Contains(res.Text, "br(branch)") {
		t.Fatalf("expected declared node inventory in text, got %q", res.Text)
	}
	if !strings.Contains(res.Text, "e1") {
		t.Fatalf("expected declared edge inventory in text, got %q", res.Text)
	}
	if !strings.Contains(res.Text, "ghost") {
		t.Fatalf("expected concrete error (unknown node ghost) in text, got %q", res.Text)
	}
	if _, hasIDs := m["declared_ids"]; !hasIDs {
		t.Fatalf("expected declared_ids map in structured payload, got %+v", m)
	}
}
