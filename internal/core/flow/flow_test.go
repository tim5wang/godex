package flow

import (
	"fmt"
	"strings"
	"testing"
)

// sampleFlow builds a "step → decision → (branch: auto/llm)" flow used by
// compile tests. n.auto / n.llm are branch targets (append templates).
func sampleFlow() *Definition {
	return &Definition{
		FlowID: "fl_order_recovery",
		Version: "1",
		Status:  "draft",
		Nodes: []Node{
			{ID: "start", Kind: KindStep, Prompt: "classify the order issue"},
			{ID: "decide", Kind: KindDecision, Prompt: "should we auto-refund?",
				Decision: &DecisionSpec{DecisionType: "choice", Choices: []Choice{{ID: "auto"}, {ID: "llm"}}}},
			{ID: "auto", Kind: KindStep, Prompt: "auto-refund the order"},
			{ID: "llm", Kind: KindStep, Prompt: "escalate to an agent"},
			{ID: "br", Kind: KindBranch,
				Branch: &BranchSpec{
					Cases: []BranchCase{
						{Name: "auto", To: "auto", Condition: Condition{Choice: "auto"}},
						{Name: "llm", To: "llm", Condition: Condition{Choice: "llm"}},
					},
					DefaultTo: "llm",
				}},
		},
		Edges: []Edge{
			{ID: "e1", From: "start", To: "decide", EdgeType: EdgeDataDependency},
			{ID: "e2", From: "decide", To: "br", EdgeType: EdgeDataDependency},
			{ID: "e3", From: "start", To: "decide", EdgeType: EdgeHandoff},
		},
	}
}

func TestValidateRejectsEmptyFlowID(t *testing.T) {
	d := sampleFlow()
	d.FlowID = ""
	if err := Validate(d); err == nil || !strings.Contains(err.Error(), "flow_id") {
		t.Fatalf("expected flow_id error, got %v", err)
	}
}

func TestValidateRejectsDuplicateNodeID(t *testing.T) {
	d := sampleFlow()
	d.Nodes = append(d.Nodes, Node{ID: "start", Kind: KindStep, Prompt: "dup"})
	if err := Validate(d); err == nil || !strings.Contains(err.Error(), "duplicate node id") {
		t.Fatalf("expected duplicate node id error, got %v", err)
	}
}

func TestValidateRejectsMissingPrompt(t *testing.T) {
	d := sampleFlow()
	d.Nodes[0].Prompt = ""
	if err := Validate(d); err == nil || !strings.Contains(err.Error(), "requires prompt") {
		t.Fatalf("expected prompt error, got %v", err)
	}
}

func TestValidateRejectsUnknownEdgeSource(t *testing.T) {
	d := sampleFlow()
	d.Edges = append(d.Edges, Edge{ID: "bad", From: "nope", To: "decide"})
	if err := Validate(d); err == nil || !strings.Contains(err.Error(), "unknown node") {
		t.Fatalf("expected unknown node error, got %v", err)
	}
}

func TestValidateRejectsDependencyCycle(t *testing.T) {
	d := sampleFlow()
	d.Edges = append(d.Edges,
		Edge{ID: "c1", From: "decide", To: "start"},
		Edge{ID: "c2", From: "br", To: "decide"},
	)
	if err := Validate(d); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("expected cycle error, got %v", err)
	}
}

func TestValidateRejectsBranchCaseUnknownTarget(t *testing.T) {
	d := sampleFlow()
	d.Nodes[4].Branch.Cases[0].To = "missing"
	if err := Validate(d); err == nil || !strings.Contains(err.Error(), "unknown node") {
		t.Fatalf("expected branch unknown target error, got %v", err)
	}
}

func TestValidateRejectsNodeCapExceeded(t *testing.T) {
	d := sampleFlow()
	for i := 0; i < MaxNodes+1; i++ {
		d.Nodes = append(d.Nodes, Node{ID: fmt.Sprintf("extra_%d", i), Kind: KindStep, Prompt: "x"})
	}
	if err := Validate(d); err == nil || !strings.Contains(err.Error(), "exceeds cap") {
		t.Fatalf("expected cap error, got %v", err)
	}
}

func TestCompileLoopProducesNotExitEdge(t *testing.T) {
	d := sampleFlow()
	d.Nodes = append(d.Nodes, Node{ID: "loop1", Kind: KindLoop, Loop: &LoopSpec{
		Body: []string{"auto"}, ExitWhen: Condition{Choice: "done"}, MaxIterations: 3,
	}})
	d.Edges = append(d.Edges, Edge{ID: "loop_e", From: "decide", To: "loop1", EdgeType: EdgeDataDependency})
	if err := Validate(d); err != nil {
		t.Fatalf("Validate should allow loop structure, got %v", err)
	}
	c, err := Compile(d)
	if err != nil {
		t.Fatalf("Compile should lower loop to a control_flow edge, got %v", err)
	}
	// The loop compiles to one control_flow append edge whose when is the
	// NEGATION of exit_when (continue while NOT exit), with iteration bounds.
	found := false
	for _, e := range c.Edges {
		if e.ID != "loop1_loop" {
			continue
		}
		found = true
		if e.From != "decide" {
			t.Fatalf("loop edge From = %q, want decide", e.From)
		}
		if e.When.Not == nil {
			t.Fatal("expected Not(exit_when) condition on loop edge")
		}
		if e.When.Not.Choice != "done" {
			t.Fatalf("expected Not{choice: done}, got %+v", e.When)
		}
		if e.MaxIterations != 3 {
			t.Fatalf("expected max_iterations 3, got %d", e.MaxIterations)
		}
		if e.IterationKey != "loop1" {
			t.Fatalf("expected iteration_key loop1, got %q", e.IterationKey)
		}
		if e.Append.ID != "auto" {
			t.Fatalf("expected append template for body node auto, got %+v", e.Append)
		}
	}
	if !found {
		t.Fatalf("expected loop edge loop1_loop in compiled edges: %+v", c.Edges)
	}
}

func TestCompileLoopRequiresBodyAndSource(t *testing.T) {
	d := sampleFlow()
	// No data_dependency source for the loop.
	d.Nodes = append(d.Nodes, Node{ID: "loop1", Kind: KindLoop, Loop: &LoopSpec{
		Body: []string{"auto"}, ExitWhen: Condition{Choice: "done"}, MaxIterations: 3,
	}})
	if _, err := Compile(d); err == nil || !strings.Contains(err.Error(), "data_dependency source") {
		t.Fatalf("expected missing source error, got %v", err)
	}
}

func TestCompileFoldStaticEdges(t *testing.T) {
	c, err := Compile(sampleFlow())
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	byID := map[string]CompiledNode{}
	for _, n := range c.Nodes {
		byID[n.ID] = n
	}
	// start -> decide data_dependency + handoff
	decide, ok := byID["decide"]
	if !ok {
		t.Fatalf("missing static node decide: %+v", c.Nodes)
	}
	if !containsStr(decide.DependsOn, "start") {
		t.Errorf("decide.DependsOn missing start: %+v", decide.DependsOn)
	}
	if !containsStr(decide.HandoffFrom, "start") {
		t.Errorf("decide.HandoffFrom missing start: %+v", decide.HandoffFrom)
	}
	// branch is a static gateway node
	br, ok := byID["br"]
	if !ok || br.Branch == nil {
		t.Fatalf("branch gateway not static: %+v", c.Nodes)
	}
	if !containsStr(br.DependsOn, "decide") {
		t.Errorf("branch.DependsOn missing decide: %+v", br.DependsOn)
	}
	// branch targets are demoted (not static)
	if _, ok := byID["auto"]; ok {
		t.Errorf("branch target auto should not be a static node")
	}
	if _, ok := byID["llm"]; ok {
		t.Errorf("branch target llm should not be a static node")
	}
	if c.Digest == "" {
		t.Error("expected non-empty digest")
	}
}

func TestCompileConditionEdgeAppendsTemplate(t *testing.T) {
	d := sampleFlow()
	d.Edges = append(d.Edges, Edge{
		ID: "ce", From: "decide", To: "auto", EdgeType: EdgeCondition,
		When: &Condition{Choice: "auto"},
	})
	c, err := Compile(d)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	found := false
	for _, e := range c.Edges {
		if e.ID == "ce" {
			found = true
			if e.Append.ID != "auto" || e.Append.Kind != KindStep {
				t.Errorf("condition edge append mismatch: %+v", e.Append)
			}
			if e.Append.DependsOn != nil {
				t.Errorf("append template should not carry DependsOn: %+v", e.Append)
			}
		}
	}
	if !found {
		t.Errorf("condition edge not compiled: %+v", c.Edges)
	}
	// 'auto' now referenced by both branch case and condition edge -> mixed
	// append references are fine (both are templates); no static node.
}

func TestCompileRejectsMixedStaticAndAppend(t *testing.T) {
	d := sampleFlow()
	// add a static in-edge into 'auto' (a branch target) -> mixed use
	d.Edges = append(d.Edges, Edge{ID: "mix", From: "start", To: "auto", EdgeType: EdgeDataDependency})
	if _, err := Compile(d); err == nil || !strings.Contains(err.Error(), "mixed use") {
		t.Fatalf("expected mixed-use error, got %v", err)
	}
}

func TestCompileDigestStable(t *testing.T) {
	c1, err := Compile(sampleFlow())
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	c2, err := Compile(sampleFlow())
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if c1.Digest != c2.Digest {
		t.Errorf("digest not stable: %s vs %s", c1.Digest, c2.Digest)
	}
}

func containsStr(items []string, want string) bool {
	for _, it := range items {
		if it == want {
			return true
		}
	}
	return false
}
