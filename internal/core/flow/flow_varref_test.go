package flow

import (
	"strings"
	"testing"
)

// sampleVarFlow returns a flow with declared inputs, a node output, and a
// downstream prompt referencing both (Flow Spec §3.4, P2.3).
func sampleVarFlow() *Definition {
	return &Definition{
		FlowID:  "fl_vars",
		Version: "1",
		Status:  "draft",
		Inputs:  []VarDef{{Name: "order_id", Type: "string"}, {Name: "amount", Type: "number"}},
		Nodes: []Node{
			{ID: "lookup", Kind: KindStep, Prompt: "look up {{inputs.order_id}}",
				Outputs: []VarDef{{Name: "found", Type: "boolean"}}},
			{ID: "decide", Kind: KindDecision, Prompt: "refund?",
				Decision: &DecisionSpec{DecisionType: "choice", Choices: []Choice{{ID: "yes"}, {ID: "no"}}}},
			{ID: "done", Kind: KindStep, Prompt: "order {{inputs.order_id}} found={{nodes.lookup.outputs.found}} choice={{nodes.decide.outputs.choice}}"},
		},
		Edges: []Edge{
			{ID: "e1", From: "lookup", To: "decide", EdgeType: EdgeDataDependency},
			{ID: "e2", From: "decide", To: "done", EdgeType: EdgeDataDependency},
		},
	}
}

func TestValidateIODecls(t *testing.T) {
	d := sampleVarFlow()
	if err := Validate(d); err != nil {
		t.Fatalf("valid flow rejected: %v", err)
	}
}

func TestValidateRejectsUnknownInputRef(t *testing.T) {
	d := sampleVarFlow()
	d.Nodes[2].Prompt = "refund {{inputs.no_such_input}}"
	if err := Validate(d); err == nil || !strings.Contains(err.Error(), "unknown input") {
		t.Fatalf("expected unknown input error, got %v", err)
	}
}

func TestValidateRejectsUnknownNodeOutputRef(t *testing.T) {
	d := sampleVarFlow()
	d.Nodes[2].Prompt = "found={{nodes.missing.outputs.found}}"
	if err := Validate(d); err == nil || !strings.Contains(err.Error(), "unknown node") {
		t.Fatalf("expected unknown node error, got %v", err)
	}
}

func TestValidateRejectsUndeclaredOutputField(t *testing.T) {
	d := sampleVarFlow()
	d.Nodes[2].Prompt = "extra={{nodes.lookup.outputs.nope}}"
	if err := Validate(d); err == nil || !strings.Contains(err.Error(), "does not declare output") {
		t.Fatalf("expected undeclared output error, got %v", err)
	}
}

func TestValidateRejectsUnknownVarScope(t *testing.T) {
	d := sampleVarFlow()
	d.Nodes[2].Prompt = "bad={{bogus.x}}"
	if err := Validate(d); err == nil || !strings.Contains(err.Error(), "unknown variable scope") {
		t.Fatalf("expected unknown scope error, got %v", err)
	}
}

func TestValidateRejectsInvalidNodeOutputDecl(t *testing.T) {
	d := sampleVarFlow()
	d.Nodes[0].Outputs = []VarDef{{Name: "found", Type: "invalid_type"}}
	if err := Validate(d); err == nil || !strings.Contains(err.Error(), "invalid variable type") {
		t.Fatalf("expected invalid type error, got %v", err)
	}
}

func TestCompileCarriesOutputs(t *testing.T) {
	d := sampleVarFlow()
	c, err := Compile(d)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	for _, n := range c.Nodes {
		if n.ID == "lookup" {
			if len(n.Outputs) != 1 || n.Outputs[0].Name != "found" {
				t.Fatalf("expected lookup outputs carried, got %+v", n.Outputs)
			}
		}
	}
}
