package agent

import (
	"context"
	"testing"

	"github.com/tim5wang/godex/internal/core/flow"
)

// flowAgentRefTestDef returns a flow whose step node pins an agent template
// via agent_ref (P1.4). The decision node is the entry so it completes
// synchronously in tests; the pinned step then runs as a subagent.
func flowAgentRefTestDef(ref string) *flow.Definition {
	return &flow.Definition{
		FlowID:  "fl_agentref",
		Name:    "AgentRef",
		Version: "1",
		Status:  "draft",
		Nodes: []flow.Node{
			{ID: "decide", Kind: flow.KindDecision, Prompt: "proceed?",
				Decision: &flow.DecisionSpec{DecisionType: "choice", Choices: []flow.Choice{{ID: "yes"}, {ID: "no"}}}},
			{ID: "work", Kind: flow.KindStep, Prompt: "do the work", AgentRef: ref},
		},
		Edges: []flow.Edge{
			{ID: "e1", From: "decide", To: "work", EdgeType: flow.EdgeDataDependency},
		},
	}
}

func TestFlowCompileCarriesAgentRef(t *testing.T) {
	a := newTestAgent(t, 4096)
	def := flowAgentRefTestDef("coder")
	v, err := a.CreateFlow(FlowCreateArgs{Def: def})
	if err != nil {
		t.Fatalf("create flow: %v", err)
	}
	// The compiled artifact must carry the reference to the engine layer.
	rec, err := a.flows.loadVersion("fl_agentref", v.Version)
	if err != nil {
		t.Fatalf("load version: %v", err)
	}
	if rec.Compiled == nil {
		t.Fatal("expected compiled artifact")
	}
	found := false
	for _, n := range rec.Compiled.Nodes {
		if n.ID == "work" {
			found = true
			if n.AgentRef != "coder" {
				t.Fatalf("expected agent_ref carried in compiled node, got %q", n.AgentRef)
			}
		}
	}
	if !found {
		t.Fatalf("missing work node in compiled: %+v", rec.Compiled.Nodes)
	}
}

func TestFlowAgentRefResolvesCapabilities(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)

	node := &workflowNode{ID: "work", AgentRef: "coder", Kind: agentGraphNodeSubagent}
	caps, err := a.resolveAgentRef(node)
	if err != nil {
		t.Fatalf("resolve agent_ref: %v", err)
	}
	if len(caps.Bundles) == 0 && len(caps.Tools) == 0 {
		t.Fatalf("expected coder template capabilities, got %+v", caps)
	}

	// Unresolved ref fails fast.
	node.AgentRef = "no_such_template"
	if _, err := a.resolveAgentRef(node); err == nil {
		t.Fatal("expected missing template to error")
	}
}

func TestFlowAgentRefMissingTemplateFailsNode(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)

	// agent_ref to a template that does not exist: starting the run must move
	// the pinned node to error instead of running with the default surface.
	def := flowAgentRefTestDef("definitely_missing_template")
	if _, err := a.CreateFlow(FlowCreateArgs{Def: def}); err != nil {
		t.Fatalf("create flow: %v", err)
	}
	if _, err := a.PublishFlow("fl_agentref", "1"); err != nil {
		t.Fatalf("publish: %v", err)
	}

	ctx := context.Background()
	run, err := a.CreateFlowRun(ctx, "fl_agentref", "", nil)
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	if _, err := a.StartFlowRun(ctx, "fl_agentref", run.RunID); err != nil {
		t.Fatalf("start run: %v", err)
	}

	state, err := a.workflowState(run.WorkflowID)
	if err != nil {
		t.Fatalf("workflow state: %v", err)
	}
	work := workflowNodeByID(state.Nodes, "work")
	if work == nil {
		t.Fatalf("missing work node: %+v", state.Nodes)
	}
	if work.Status != workflowStatusError {
		t.Fatalf("expected pinned node to error on missing template, got %q", work.Status)
	}
}

func TestFlowAgentRefInjectsBundlesIntoRun(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	// Drive the pinned step's subagent job with the fake client so the run is
	// deterministic without a real LLM worker (start must not error).
	a.client = repeatedTextCaller("work done")

	// "coder" is a built-in template with bundles/tools. Verify the node's
	// start request receives them (bundles/tools/write_scope merge).
	def := flowAgentRefTestDef("coder")
	if _, err := a.CreateFlow(FlowCreateArgs{Def: def}); err != nil {
		t.Fatalf("create flow: %v", err)
	}
	if _, err := a.PublishFlow("fl_agentref", "1"); err != nil {
		t.Fatalf("publish: %v", err)
	}

	ctx := context.Background()
	run, err := a.CreateFlowRun(ctx, "fl_agentref", "", nil)
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	if _, err := a.StartFlowRun(ctx, "fl_agentref", run.RunID); err != nil {
		t.Fatalf("start run: %v", err)
	}

	// The pinned node must have started a subagent job (bundles resolved
	// before dispatch), not errored.
	state, err := a.workflowState(run.WorkflowID)
	if err != nil {
		t.Fatalf("workflow state: %v", err)
	}
	work := workflowNodeByID(state.Nodes, "work")
	if work == nil {
		t.Fatalf("missing work node")
	}
	if work.Status == workflowStatusError {
		t.Fatalf("work node errored: %s", work.Error)
	}
	if work.JobID == "" {
		t.Fatalf("expected subagent job started for pinned node, got %+v", work)
	}
}
