package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/contracts/protocol"
)

// The agent_graph tests cover the four contract areas of roadmap 2.4:
//  1. graph creation + node_type/edge_type exposure (可观测视图)
//  2. dependency scheduling (data_dependency / handoff edges gate start)
//  3. dynamic add/remove/cancel (动态增删)
//  4. control-flow append + merge_point + user_input semantics
//
// They reuse newTestAgent + repeatedTextCaller so the durable subagent
// runtime is deterministic: every started node completes with the canned
// text, and the verdict parser derives pass/fail from the "Verdict:" line.

func runAgentGraphTool(t *testing.T, a *Agent, ctx context.Context, input map[string]interface{}) agentGraphView {
	t.Helper()
	output, err := a.handleTool(ctx, "agent_graph", input)
	if err != nil {
		t.Fatalf("agent_graph tool: %v", err)
	}
	var view agentGraphView
	if err := json.Unmarshal([]byte(output), &view); err != nil {
		t.Fatalf("unmarshal agent_graph view: %v\n%s", err, output)
	}
	return view
}

func graphNodeByID(nodes []workflowNodeView, id string) *workflowNodeView {
	for i := range nodes {
		if nodes[i].ID == id {
			return &nodes[i]
		}
	}
	return nil
}

func graphNodeType(nodes []workflowNodeView, id string) string {
	if n := graphNodeByID(nodes, id); n != nil {
		return n.NodeType
	}
	return ""
}

func graphEdgesOfType(edges []agentGraphEdgeView, edgeType string) []agentGraphEdgeView {
	var out []agentGraphEdgeView
	for _, e := range edges {
		if e.Type == edgeType {
			out = append(out, e)
		}
	}
	return out
}

// sequenceCallerFromStrings builds a deterministic caller that returns each
// canned text once, in order (used to drive multi-step control-flow graphs).
func sequenceCallerFromStrings(texts []string) *sequenceCaller {
	responses := make([]protocol.Response, 0, len(texts))
	for _, text := range texts {
		responses = append(responses, protocol.Response{Content: []protocol.Block{protocol.TextBlock(text)}})
	}
	return &sequenceCaller{responses: responses}
}

func TestAgentGraphCreateExposesNodeAndEdgeTypes(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	a.client = repeatedTextCaller("Verdict: pass\nwork done")

	ctx := context.Background()
	view := runAgentGraphTool(t, a, ctx, map[string]interface{}{
		"action":   "create",
		"graph_id": "g_obs",
		"nodes": []map[string]interface{}{
			{"id": "plan", "node_type": "llm_task", "prompt": "plan the work"},
			{"id": "impl", "node_type": "subagent_task", "prompt": "implement it"},
			{"id": "merge", "node_type": "merge_point", "prompt": "synthesize"},
		},
		"edges": []map[string]interface{}{
			{"edge_type": "data_dependency", "from": "plan", "to": "impl"},
			{"edge_type": "handoff", "from": "impl", "to": "merge"},
		},
	})
	if view.WorkflowID != "g_obs" {
		t.Fatalf("expected graph id g_obs, got %s", view.WorkflowID)
	}
	if view.Total != 3 || view.Pending != 3 {
		t.Fatalf("expected 3 pending nodes, got total=%d pending=%d", view.Total, view.Pending)
	}
	// node_type exposure
	if graphNodeType(view.Nodes, "plan") != agentGraphNodeLLMTask {
		t.Fatalf("expected plan node_type llm_task, got %q", graphNodeType(view.Nodes, "plan"))
	}
	if graphNodeType(view.Nodes, "impl") != agentGraphNodeSubagent {
		t.Fatalf("expected impl node_type subagent_task, got %q", graphNodeType(view.Nodes, "impl"))
	}
	if graphNodeType(view.Nodes, "merge") != agentGraphNodeMergePoint {
		t.Fatalf("expected merge node_type merge_point, got %q", graphNodeType(view.Nodes, "merge"))
	}
	// edge_type exposure: data_dependency plan->impl, handoff impl->merge
	if len(graphEdgesOfType(view.Edges, agentGraphEdgeDataDependency)) != 1 {
		t.Fatalf("expected 1 data_dependency edge, got %+v", view.Edges)
	}
	if len(graphEdgesOfType(view.Edges, agentGraphEdgeHandoff)) != 1 {
		t.Fatalf("expected 1 handoff edge, got %+v", view.Edges)
	}
	// handoff implies dependency: merge depends on impl, impl depends on plan
	merge := graphNodeByID(view.Nodes, "merge")
	if merge == nil || len(merge.DependsOn) != 1 || merge.DependsOn[0] != "impl" {
		t.Fatalf("expected merge depends_on [impl], got %+v", merge)
	}
	impl := graphNodeByID(view.Nodes, "impl")
	if impl == nil || len(impl.DependsOn) != 1 || impl.DependsOn[0] != "plan" {
		t.Fatalf("expected impl depends_on [plan], got %+v", impl)
	}
}

func TestAgentGraphDependencyScheduling(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	a.client = repeatedTextCaller("Verdict: pass\nimplemented")

	ctx := context.Background()
	runAgentGraphTool(t, a, ctx, map[string]interface{}{
		"action":   "create",
		"graph_id": "g_sched",
		"nodes": []map[string]interface{}{
			{"id": "a", "prompt": "do a"},
			{"id": "b", "prompt": "do b"},
			{"id": "c", "prompt": "do c"},
		},
		"edges": []map[string]interface{}{
			{"edge_type": "data_dependency", "from": "a", "to": "b"},
			{"edge_type": "data_dependency", "from": "a", "to": "c"},
		},
	})
	// run: only a has no deps, so only a starts (b and c gate on a)
	ran := runAgentGraphTool(t, a, ctx, map[string]interface{}{
		"action":   "run",
		"graph_id": "g_sched",
	})
	if len(ran.Started) != 1 || ran.Started[0] != "a" {
		t.Fatalf("expected only a to start first, got %+v", ran.Started)
	}
	// wait for a
	runAgentGraphTool(t, a, ctx, map[string]interface{}{
		"action":   "wait",
		"graph_id": "g_sched",
		"mode":     "all",
		"timeout_ms": 2000,
	})
	// run again: b and c are now ready (parallel fan-out)
	ran = runAgentGraphTool(t, a, ctx, map[string]interface{}{
		"action":   "run",
		"graph_id": "g_sched",
	})
	if len(ran.Started) != 2 {
		t.Fatalf("expected b and c to start in parallel, got %+v", ran.Started)
	}
	runAgentGraphTool(t, a, ctx, map[string]interface{}{
		"action":   "wait",
		"graph_id": "g_sched",
		"mode":     "all",
		"timeout_ms": 2000,
	})
	got := runAgentGraphTool(t, a, ctx, map[string]interface{}{
		"action":   "get",
		"graph_id": "g_sched",
	})
	if got.Status != workflowStatusCompleted || got.Completed != 3 {
		t.Fatalf("expected completed graph, got %+v", got)
	}
}

func TestAgentGraphMergePointAutoCompletesWithSummary(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	a.client = repeatedTextCaller("Verdict: pass\nimplemented feature")

	ctx := context.Background()
	runAgentGraphTool(t, a, ctx, map[string]interface{}{
		"action":   "create",
		"graph_id": "g_merge",
		"nodes": []map[string]interface{}{
			{"id": "w1", "prompt": "worker one"},
			{"id": "w2", "prompt": "worker two"},
			{"id": "m", "node_type": "merge_point", "prompt": "merge"},
		},
		"edges": []map[string]interface{}{
			{"edge_type": "data_dependency", "from": "w1", "to": "m"},
			{"edge_type": "data_dependency", "from": "w2", "to": "m"},
		},
	})
	// run 1: w1 + w2 start in parallel (both dep-free)
	ran := runAgentGraphTool(t, a, ctx, map[string]interface{}{"action": "run", "graph_id": "g_merge"})
	if len(ran.Started) != 2 {
		t.Fatalf("expected w1,w2 to start in parallel, got %+v", ran.Started)
	}
	runAgentGraphTool(t, a, ctx, map[string]interface{}{"action": "wait", "graph_id": "g_merge", "mode": "all", "timeout_ms": 2000})
	// run 2: merge point deps complete -> auto-complete (no subagent job)
	ran = runAgentGraphTool(t, a, ctx, map[string]interface{}{"action": "run", "graph_id": "g_merge"})
	if len(ran.Started) != 0 {
		t.Fatalf("merge point must not start a subagent job, got %+v", ran.Started)
	}
	got := runAgentGraphTool(t, a, ctx, map[string]interface{}{"action": "get", "graph_id": "g_merge"})
	merge := graphNodeByID(got.Nodes, "m")
	if merge == nil || merge.Status != workflowStatusCompleted {
		t.Fatalf("expected merge point completed, got %+v", merge)
	}
	if merge.JobID != "" {
		t.Fatalf("merge point should not own a subagent job, got job %s", merge.JobID)
	}
	if merge.ResultPreview == "" {
		t.Fatalf("expected merge point to synthesize a summary, got %+v", merge)
	}
	if !strings.Contains(merge.ResultPreview, "implemented feature") {
		t.Fatalf("expected merge summary to include worker handoff content, got %q", merge.ResultPreview)
	}
	if got.Status != workflowStatusCompleted {
		t.Fatalf("expected completed graph, got %+v", got)
	}
}

func TestAgentGraphUserInputBlocksUntilCompleteNode(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	a.client = repeatedTextCaller("Verdict: pass\nprepared")

	ctx := context.Background()
	runAgentGraphTool(t, a, ctx, map[string]interface{}{
		"action":   "create",
		"graph_id": "g_input",
		"nodes": []map[string]interface{}{
			{"id": "prep", "prompt": "prepare"},
			{"id": "u", "node_type": "user_input", "prompt": "await decision"},
			{"id": "act", "prompt": "act on decision"},
		},
		"edges": []map[string]interface{}{
			{"edge_type": "data_dependency", "from": "prep", "to": "u"},
			{"edge_type": "data_dependency", "from": "u", "to": "act"},
		},
	})
	runAgentGraphTool(t, a, ctx, map[string]interface{}{"action": "run", "graph_id": "g_input"})
	runAgentGraphTool(t, a, ctx, map[string]interface{}{"action": "wait", "graph_id": "g_input", "mode": "all", "timeout_ms": 2000})
	// run: u is a user_input control node -> stays pending, no subagent job
	ran := runAgentGraphTool(t, a, ctx, map[string]interface{}{"action": "run", "graph_id": "g_input"})
	if len(ran.Started) != 0 {
		t.Fatalf("user_input must not auto-start, got %+v", ran.Started)
	}
	got := runAgentGraphTool(t, a, ctx, map[string]interface{}{"action": "get", "graph_id": "g_input"})
	if n := graphNodeByID(got.Nodes, "u"); n == nil || n.Status != workflowStatusPending {
		t.Fatalf("expected user_input pending, got %+v", n)
	}
	if n := graphNodeByID(got.Nodes, "act"); n == nil || n.Status != workflowStatusPending {
		t.Fatalf("expected act pending behind user_input, got %+v", n)
	}
	// orchestrator feeds input
	runAgentGraphTool(t, a, ctx, map[string]interface{}{
		"action":   "complete_node",
		"graph_id": "g_input",
		"node_id":  "u",
		"result":   "approved: proceed with plan A",
	})
	ran = runAgentGraphTool(t, a, ctx, map[string]interface{}{"action": "run", "graph_id": "g_input"})
	if len(ran.Started) != 1 || ran.Started[0] != "act" {
		t.Fatalf("expected act to start after user input, got %+v", ran.Started)
	}
	// let act finish so the subagent job is terminal before teardown
	runAgentGraphTool(t, a, ctx, map[string]interface{}{"action": "wait", "graph_id": "g_input", "mode": "all", "timeout_ms": 2000})
}

func TestAgentGraphDynamicAddRemoveCancel(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	a.client = repeatedTextCaller("Verdict: pass\nwork")

	ctx := context.Background()
	runAgentGraphTool(t, a, ctx, map[string]interface{}{
		"action":   "create",
		"graph_id": "g_dyn",
		"nodes": []map[string]interface{}{
			{"id": "a", "prompt": "do a"},
			{"id": "b", "prompt": "do b"},
		},
		"edges": []map[string]interface{}{
			{"edge_type": "data_dependency", "from": "a", "to": "b"},
		},
	})
	// remove a dependent node: b depends on a -> removal of a must fail
	if _, err := a.handleTool(ctx, "agent_graph", map[string]interface{}{
		"action": "remove_node", "graph_id": "g_dyn", "node_id": "a",
	}); err == nil || !strings.Contains(err.Error(), "still depends") {
		t.Fatalf("expected removal of a to fail with dependency error, got %v", err)
	}
	// remove leaf node b -> ok
	view := runAgentGraphTool(t, a, ctx, map[string]interface{}{
		"action": "remove_node", "graph_id": "g_dyn", "node_id": "b",
	})
	if graphNodeByID(view.Nodes, "b") != nil || view.Total != 1 {
		t.Fatalf("expected b removed, got %+v", view)
	}
	// add node c + edge a->c
	view = runAgentGraphTool(t, a, ctx, map[string]interface{}{
		"action": "add_node",
		"graph_id": "g_dyn",
		"nodes": []map[string]interface{}{
			{"id": "c", "node_type": "tool_call", "prompt": "run check"},
		},
		"edges": []map[string]interface{}{
			{"edge_type": "data_dependency", "from": "a", "to": "c"},
		},
	})
	c := graphNodeByID(view.Nodes, "c")
	if c == nil || c.NodeType != agentGraphNodeToolCall {
		t.Fatalf("expected c with node_type tool_call, got %+v", c)
	}
	if len(c.DependsOn) != 1 || c.DependsOn[0] != "a" {
		t.Fatalf("expected c depends_on [a], got %+v", c)
	}
	// add a static edge a->c again must be idempotent (dedupe)
	runAgentGraphTool(t, a, ctx, map[string]interface{}{
		"action": "add_edge",
		"graph_id": "g_dyn",
		"edges": []map[string]interface{}{
			{"edge_type": "data_dependency", "from": "a", "to": "c"},
		},
	})
	got := runAgentGraphTool(t, a, ctx, map[string]interface{}{"action": "get", "graph_id": "g_dyn"})
	c = graphNodeByID(got.Nodes, "c")
	if len(c.DependsOn) != 1 {
		t.Fatalf("expected deduped depends_on, got %+v", c.DependsOn)
	}
	// cancel pending node c
	view = runAgentGraphTool(t, a, ctx, map[string]interface{}{
		"action": "cancel_node", "graph_id": "g_dyn", "node_id": "c",
	})
	if n := graphNodeByID(view.Nodes, "c"); n == nil || n.Status != workflowStatusCanceled {
		t.Fatalf("expected c canceled, got %+v", n)
	}
}

func TestAgentGraphControlFlowEdgeAppendsNodeOnOutcome(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	// First response fails, second passes: the control-flow edge should only
	// fire for the failing source.
	responses := []string{
		"Verdict: fail\nbuild broke",
		"Verdict: pass\nfixed",
	}
	a.client = sequenceCallerFromStrings(responses)

	ctx := context.Background()
	runAgentGraphTool(t, a, ctx, map[string]interface{}{
		"action":   "create",
		"graph_id": "g_cf",
		"nodes": []map[string]interface{}{
			{"id": "build", "prompt": "run build"},
		},
		"edges": []map[string]interface{}{
			{
				"edge_type": "control_flow",
				"from":      "build",
				"when":      "fail",
				"append": map[string]interface{}{
					"id":     "retry_1",
					"prompt": "fix build failure from {source}",
					"title":  "fix",
				},
			},
		},
	})
	// run + wait: build completes with fail verdict
	runAgentGraphTool(t, a, ctx, map[string]interface{}{"action": "run", "graph_id": "g_cf"})
	runAgentGraphTool(t, a, ctx, map[string]interface{}{"action": "wait", "graph_id": "g_cf", "mode": "all", "timeout_ms": 2000})
	// get: processWorkflowEdges should have appended retry_1 on fail verdict
	got := runAgentGraphTool(t, a, ctx, map[string]interface{}{"action": "get", "graph_id": "g_cf"})
	retry := graphNodeByID(got.Nodes, "retry_1")
	if retry == nil {
		t.Fatalf("expected control_flow append node retry_1, got %+v", got.Nodes)
	}
	cfEdges := graphEdgesOfType(got.Edges, agentGraphEdgeControlFlow)
	if len(cfEdges) != 1 || cfEdges[0].Verdict != "fail" {
		t.Fatalf("expected one control_flow edge with verdict fail, got %+v", got.Edges)
	}
	// run retry_1 (passes now), wait, graph completes
	runAgentGraphTool(t, a, ctx, map[string]interface{}{"action": "run", "graph_id": "g_cf"})
	runAgentGraphTool(t, a, ctx, map[string]interface{}{"action": "wait", "graph_id": "g_cf", "mode": "all", "timeout_ms": 2000})
	got = runAgentGraphTool(t, a, ctx, map[string]interface{}{"action": "get", "graph_id": "g_cf"})
	if got.Status != workflowStatusCompleted || got.Completed != 2 {
		t.Fatalf("expected completed graph, got %+v", got)
	}
}
