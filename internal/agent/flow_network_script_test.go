package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/core/flow"
)

// TestHTTPGetWithPolicyDenials verifies the network-policy bridge at unit
// level (no real network IO): blocked domains are always denied, allowlist
// rejects anything outside the allowed set.
func TestHTTPGetWithPolicyDenials(t *testing.T) {
	ctx := context.Background()

	// Blocked domain: denied regardless of policy.
	_, err := httpGetWithPolicy(ctx, "https://blocked.example.org/x", flowNetworkPolicy{
		Policy:         "allow_all",
		BlockedDomains: []string{"*.example.org"},
		TimeoutSeconds: 5,
	})
	if err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("expected blocked-domain denial, got %v", err)
	}

	// Allowlist: domain outside the allowed set is denied before any request.
	_, err = httpGetWithPolicy(ctx, "https://other.example.org/x", flowNetworkPolicy{
		Policy:         "allowlist",
		AllowedDomains: []string{"api.openai.com"},
		TimeoutSeconds: 5,
	})
	if err == nil || !strings.Contains(err.Error(), "not in the flow's allowed") {
		t.Fatalf("expected allowlist denial, got %v", err)
	}

	// Allowlist hit proceeds to the network layer (no assertion on the
	// response; local sandbox has no egress) — just ensure it is NOT denied
	// by policy (the error, if any, comes from transport).
	_, err = httpGetWithPolicy(ctx, "https://api.openai.com/v1/models", flowNetworkPolicy{
		Policy:         "allowlist",
		AllowedDomains: []string{"api.openai.com"},
		TimeoutSeconds: 5,
	})
	if err != nil && strings.Contains(err.Error(), "blocked") {
		t.Fatalf("allowed domain must not be denied by policy, got %v", err)
	}

	// Non-http scheme is rejected.
	_, err = httpGetWithPolicy(ctx, "file:///etc/passwd", flowNetworkPolicy{Policy: "allow_all", TimeoutSeconds: 5})
	if err == nil || !strings.Contains(err.Error(), "http/https only") {
		t.Fatalf("expected non-http rejection, got %v", err)
	}
}

// TestWorkflowFunctionNodePrePostScripts verifies E3b: a function node with
// pre_script/post_script runs both around the handler and captures their
// stdout onto node.ScriptOutput, visible on the refreshed state.
func TestWorkflowFunctionNodePrePostScripts(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)

	def := &flow.Definition{
		FlowID:  "fl_script",
		Version: "1",
		Status:  "draft",
		Nodes: []flow.Node{
			{
				ID:    "fn",
				Kind:  flow.KindFunction,
				Title: "scripted",
				Function: &flow.FunctionSpec{
					Runtime: flow.FunctionRuntimeJS,
					Handler: "handle",
					Source:  "function handle(ctx, event) { return { v: 42 }; }",
				},
				PreScript:  "echo pre-ran >> pre_marker.txt",
				PostScript: "echo post-ran >> post_marker.txt",
			},
		},
		Edges: []flow.Edge{},
	}
	if _, err := a.CreateFlow(FlowCreateArgs{Def: def}); err != nil {
		t.Fatalf("create flow: %v", err)
	}
	ctx := context.Background()
	run, err := a.CreateFlowRun(ctx, "fl_script", "", nil)
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	if _, err := a.StartFlowRun(ctx, "fl_script", run.RunID); err != nil {
		t.Fatalf("start run: %v", err)
	}
	state, err := a.workflowState(run.WorkflowID)
	if err != nil {
		t.Fatalf("workflow state: %v", err)
	}
	fn := workflowNodeByID(state.Nodes, "fn")
	if fn == nil {
		t.Fatalf("expected function node, got %+v", state.Nodes)
	}
	if fn.Status != workflowStatusCompleted {
		t.Fatalf("expected function completed, got %q (err=%s)", fn.Status, fn.Error)
	}
	if fn.ScriptOutput == nil {
		t.Fatalf("expected script_output populated, got nil")
	}
	if fn.ScriptOutput["pre_exit"] != float64(0) {
		t.Fatalf("expected pre_script exit 0, got %+v", fn.ScriptOutput)
	}
	if fn.ScriptOutput["post_exit"] != float64(0) {
		t.Fatalf("expected post_script exit 0, got %+v", fn.ScriptOutput)
	}
}

// TestWorkflowFunctionNodeNetworkPolicyBlocked verifies E3a: the js sandbox
// http.get() bridge honors the Flow network policy BEFORE any request is made
// — a blocked domain is denied without network IO.
func TestWorkflowFunctionNodeNetworkPolicyBlocked(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)

	def := &flow.Definition{
		FlowID:  "fl_net",
		Version: "1",
		Status:  "draft",
		Network: &flow.NetworkPolicy{
			Policy:         "allowlist",
			AllowedDomains: []string{"example.com"},
		},
		Nodes: []flow.Node{
			{
				ID:    "fn",
				Kind:  flow.KindFunction,
				Title: "net",
				Function: &flow.FunctionSpec{
					Runtime: flow.FunctionRuntimeJS,
					Handler: "handle",
					Source:  "function handle(ctx, event) { const r = http.get('https://blocked.example.org/x'); return { ok: r.ok, err: r.error || '' }; }",
				},
			},
		},
		Edges: []flow.Edge{},
	}
	if _, err := a.CreateFlow(FlowCreateArgs{Def: def}); err != nil {
		t.Fatalf("create flow: %v", err)
	}
	ctx := context.Background()
	run, err := a.CreateFlowRun(ctx, "fl_net", "", nil)
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	if _, err := a.StartFlowRun(ctx, "fl_net", run.RunID); err != nil {
		t.Fatalf("start run: %v", err)
	}
	state, err := a.workflowState(run.WorkflowID)
	if err != nil {
		t.Fatalf("workflow state: %v", err)
	}
	fn := workflowNodeByID(state.Nodes, "fn")
	if fn == nil {
		t.Fatalf("expected function node, got %+v", state.Nodes)
	}
	if fn.Status != workflowStatusCompleted {
		t.Fatalf("expected function completed, got %q (err=%s)", fn.Status, fn.Error)
	}
	if fn.Outputs["ok"] != false {
		t.Fatalf("expected blocked domain to be denied, got %+v", fn.Outputs)
	}
	if s, _ := fn.Outputs["err"].(string); s == "" {
		t.Fatalf("expected denial error text, got %+v", fn.Outputs)
	}
}
