package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/core/flow"
)

// TestParseFlowDiagnosisFromLLMPlain verifies a plain JSON diagnosis object
// (no fences) parses into root cause + suggestions + fixed definition.
func TestParseFlowDiagnosisFromLLMPlain(t *testing.T) {
	text := `{"root_cause": "decision node times out", "summary": "decision slow",
	  "suggestions": ["increase timeout_ms", "use fail_open"],
	  "fixed_definition": {"flow_id": "fl_x", "version": "1", "status": "draft",
	    "nodes": [{"id": "a", "kind": "step", "prompt": "do"}], "edges": []}}`
	d, err := parseFlowDiagnosisFromLLM(text)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if d.RootCause != "decision node times out" || len(d.Suggestions) != 2 {
		t.Fatalf("unexpected diagnosis: %+v", d)
	}
	if d.FixedDefinition == nil || len(d.FixedDefinition.Nodes) != 1 {
		t.Fatalf("expected fixed definition parsed: %+v", d)
	}
}

// TestParseFlowDiagnosisFromLLMFence verifies a fenced + prose-wrapped
// response still parses (LLM output tolerance).
func TestParseFlowDiagnosisFromLLMFence(t *testing.T) {
	text := "Here is the diagnosis:\n```json\n{\"root_cause\": \"x\", \"suggestions\": [\"y\"]}\n```\nLet me know."
	d, err := parseFlowDiagnosisFromLLM(text)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if d.RootCause != "x" || len(d.Suggestions) != 1 || d.FixedDefinition != nil {
		t.Fatalf("unexpected diagnosis: %+v", d)
	}
}

// TestParseFlowDiagnosisFromLLMGarbage verifies a non-JSON response is rejected.
func TestParseFlowDiagnosisFromLLMGarbage(t *testing.T) {
	if _, err := parseFlowDiagnosisFromLLM("I cannot diagnose this."); err == nil {
		t.Fatal("expected parse error for non-JSON response")
	}
}

// TestSummarizeDefinitionBounds verifies the digest stays compact.
func TestSummarizeDefinitionBounds(t *testing.T) {
	def := &flow.Definition{
		FlowID: "fl_big", Version: "1", Status: "draft",
		Nodes: []flow.Node{{ID: "a", Kind: "step", Prompt: strings.Repeat("x", 1000)}},
		Edges: []flow.Edge{{From: "a", To: "a"}},
	}
	sum := string(summarizeDefinition(def))
	if len(sum) > 600 {
		t.Fatalf("expected bounded summary, got %d bytes", len(sum))
	}
	if !strings.Contains(sum, `"id":"a"`) {
		t.Fatalf("expected node id in summary: %s", sum)
	}
}

// TestDiagnoseFlowRunValid verifies the end-to-end diagnose path: a failed run
// with events → LLM returns root cause + a valid fixed definition → the
// diagnosis is returned with the definition validated (NOT saved).
func TestDiagnoseFlowRunValid(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)

	// A flow with a function node that fails (source throws).
	def := functionFlowDef()
	def.FlowID = "fl_diag"
	def.Nodes[0].Function.Source = "function handle(ctx, event) { throw new Error('boom'); }"
	if _, err := a.CreateFlow(FlowCreateArgs{Def: def}); err != nil {
		t.Fatalf("create flow: %v", err)
	}
	ctx := context.Background()
	run, err := a.CreateFlowRun(ctx, "fl_diag", "", map[string]any{"task": "x"})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	if _, err := a.StartFlowRun(ctx, "fl_diag", run.RunID); err != nil {
		t.Fatalf("start run: %v", err)
	}

	// Mock LLM: root cause + fixed definition (valid, complete).
	a.client = repeatedTextCaller(`{"root_cause": "handler throws", "summary": "fn fails",
	  "suggestions": ["fix the js handler source"],
	  "fixed_definition": {"flow_id": "fl_diag", "version": "1", "status": "draft",
	    "nodes": [{"id": "fn", "kind": "function", "title": "echo-task",
	      "function": {"runtime": "js", "handler": "handle", "source": "function handle(ctx, event) { return { ok: true }; }"}}],
	    "edges": []}}`)

	diag, err := a.DiagnoseFlowRun(ctx, "fl_diag", run.RunID)
	if err != nil {
		t.Fatalf("diagnose: %v", err)
	}
	if diag.RunID != run.RunID || diag.FlowID != "fl_diag" || diag.Version != "1" {
		t.Fatalf("unexpected diagnosis identity: %+v", diag)
	}
	if diag.FixedDefinition == nil || len(diag.FixedDefinition.Nodes) != 1 {
		t.Fatalf("expected fixed definition, got %+v", diag)
	}
	if diag.FixedDefinition.Nodes[0].Function == nil || diag.FixedDefinition.Nodes[0].Function.Source == "" {
		t.Fatalf("expected fixed function source, got %+v", diag.FixedDefinition.Nodes[0])
	}
	// NOT saved: the flow still has only version 1 (the original definition).
	v, err := a.GetFlowVersion("fl_diag", "1")
	if err != nil {
		t.Fatalf("get version: %v", err)
	}
	if v.Definition.Nodes[0].Function.Source != def.Nodes[0].Function.Source {
		t.Fatalf("diagnose must not mutate the stored definition")
	}
}

// TestDiagnoseFlowRunInvalidFixedDefinitionRejected verifies a malformed fixed
// definition from the LLM fails the diagnose call (never silently accepted).
func TestDiagnoseFlowRunInvalidFixedDefinitionRejected(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)

	def := functionFlowDef()
	def.FlowID = "fl_diag_bad"
	def.Nodes[0].Function.Source = "function handle(ctx, event) { throw new Error('boom'); }"
	if _, err := a.CreateFlow(FlowCreateArgs{Def: def}); err != nil {
		t.Fatalf("create flow: %v", err)
	}
	ctx := context.Background()
	run, err := a.CreateFlowRun(ctx, "fl_diag_bad", "", nil)
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	if _, err := a.StartFlowRun(ctx, "fl_diag_bad", run.RunID); err != nil {
		t.Fatalf("start run: %v", err)
	}

	// Fixed definition references an unknown node → Validate must reject.
	a.client = repeatedTextCaller(`{"root_cause": "x", "suggestions": ["y"],
	  "fixed_definition": {"flow_id": "fl_diag_bad", "version": "1", "status": "draft",
	    "nodes": [{"id": "fn", "kind": "function", "function": {"runtime": "js", "source": "function handle(ctx,e){return {};}"}}],
	    "edges": [{"from": "missing", "to": "fn"}]}}`)

	if _, err := a.DiagnoseFlowRun(ctx, "fl_diag_bad", run.RunID); err == nil {
		t.Fatal("expected invalid fixed definition to be rejected")
	}
}
