package agent

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/core/decision"
)

// scriptedDecisionCaller is a deterministic decision.Caller for engine tests.
// It returns fail errs in order, then falls back to result.
type scriptedDecisionCaller struct {
	mu      sync.Mutex
	fail    int
	err     error
	result  decision.Result
	calls   int
	lastReq decision.Request
	lastCtx context.Context
}

func (c *scriptedDecisionCaller) Decide(ctx context.Context, req decision.Request) (decision.Result, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	c.lastReq = req
	c.lastCtx = ctx
	if c.calls <= c.fail {
		return decision.Result{}, c.err
	}
	return c.result, nil
}

func decisionTestWorkflow(t *testing.T, a *Agent, workflowID string, edges []map[string]interface{}) workflowView {
	t.Helper()
	return runWorkflowTool(t, a, context.Background(), map[string]interface{}{
		"action":      "create",
		"workflow_id": workflowID,
		"nodes": []map[string]interface{}{
			{
				"id":     "decide",
				"kind":   "decision",
				"prompt": "Should the fixed flow auto-execute?",
				"decision": map[string]interface{}{
					"decision_type": "choice",
					"question":      "Should the fixed flow auto-execute?",
					"choices": []map[string]interface{}{
						{"id": "auto", "label": "Auto execute"},
						{"id": "llm", "label": "Escalate to LLM agent"},
					},
				},
			},
		},
		"edges": edges,
	})
}

func branchEdges() []map[string]interface{} {
	return []map[string]interface{}{
		{
			"id":            "decision-auto",
			"from":          "decide",
			"when":          map[string]interface{}{"choice": "auto"},
			"append":        map[string]interface{}{"id": "auto_run", "kind": "task", "prompt": "run the fixed flow automatically"},
			"iteration_key": "branch-auto",
		},
		{
			"id":            "decision-fallback",
			"from":          "decide",
			"when":          map[string]interface{}{"choice": decisionErrorChoice},
			"append":        map[string]interface{}{"id": "llm_fallback", "kind": "task", "prompt": "handle the long-tail case with an LLM agent"},
			"iteration_key": "branch-fallback",
		},
	}
}

func TestWorkflowDecisionNodeHighConfidenceAutoBranches(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	caller := &scriptedDecisionCaller{result: decision.Result{Choice: "auto", Confidence: 0.93, Model: "jev-test"}}
	a.SetDecisionCaller(caller)

	created := decisionTestWorkflow(t, a, "wf_decision_auto", branchEdges())
	cleanupWorkflowAfterTest(t, a, "wf_decision_auto")
	if created.Pending != 1 {
		t.Fatalf("expected one pending decision node, got %+v", created)
	}
	started := runWorkflowTool(t, a, context.Background(), map[string]interface{}{
		"action": "start", "workflow_id": "wf_decision_auto",
	})
	if nodeStatus(started.Nodes, "decide") != workflowStatusCompleted {
		t.Fatalf("expected decision node completed synchronously, got %+v", started.Nodes)
	}
	if got := nodeVerdict(started.Nodes, "decide"); got != "" && got != workflowVerdictPass {
		t.Fatalf("unexpected verdict for confident decision: %q", got)
	}
	if !nodeExists(started.Nodes, "auto_run") || nodeExists(started.Nodes, "llm_fallback") {
		t.Fatalf("expected only the auto branch to append, got %+v", started.Nodes)
	}
	for _, node := range started.Nodes {
		if node.ID == "decide" && node.JobID != "" {
			t.Fatalf("decision node must not start a subagent job, got %q", node.JobID)
		}
		if node.ID == "decide" && (node.Decision == nil || node.Decision.Choice != "auto" || node.Decision.Confidence != 0.93) {
			t.Fatalf("decision result not persisted: %+v", node.Decision)
		}
	}
	caller.mu.Lock()
	defer caller.mu.Unlock()
	if caller.calls != 1 {
		t.Fatalf("expected exactly one decision call, got %d", caller.calls)
	}
	if len(caller.lastReq.Choices) != 2 {
		t.Fatalf("expected closed choice set passed to caller, got %+v", caller.lastReq.Choices)
	}
}

func TestWorkflowDecisionNodeConfidencePredicateRoutes(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	a.SetDecisionCaller(&scriptedDecisionCaller{result: decision.Result{Choice: "auto", Confidence: 0.42}})

	edges := []map[string]interface{}{
		{
			"id": "confident", "from": "decide",
			"when":   map[string]interface{}{"confidence": map[string]interface{}{"op": "gte", "value": 0.8}},
			"append": map[string]interface{}{"id": "auto_run", "prompt": "auto"},
		},
		{
			"id": "unsure", "from": "decide",
			"when":   map[string]interface{}{"confidence": map[string]interface{}{"op": "lt", "value": 0.8}},
			"append": map[string]interface{}{"id": "review_run", "prompt": "review"},
		},
	}
	decisionTestWorkflow(t, a, "wf_decision_conf", edges)
	cleanupWorkflowAfterTest(t, a, "wf_decision_conf")
	started := runWorkflowTool(t, a, context.Background(), map[string]interface{}{
		"action": "start", "workflow_id": "wf_decision_conf",
	})
	if !nodeExists(started.Nodes, "review_run") || nodeExists(started.Nodes, "auto_run") {
		t.Fatalf("expected low-confidence branch, got %+v", started.Nodes)
	}
}

func TestWorkflowDecisionFailClosedRoutesToFallbackWithoutCaller(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	// No SetDecisionCaller: decision model is not configured.

	decisionTestWorkflow(t, a, "wf_decision_closed", branchEdges())
	cleanupWorkflowAfterTest(t, a, "wf_decision_closed")
	started := runWorkflowTool(t, a, context.Background(), map[string]interface{}{
		"action": "start", "workflow_id": "wf_decision_closed",
	})
	decide := nodeByID(started.Nodes, "decide")
	if decide == nil || decide.Status != workflowStatusCompleted || decide.Verdict != workflowVerdictBlocked {
		t.Fatalf("expected fail_closed decision completed with blocked verdict, got %+v", decide)
	}
	if decide.Decision == nil || decide.Decision.Choice != decisionErrorChoice {
		t.Fatalf("expected reserved error choice, got %+v", decide)
	}
	if !nodeExists(started.Nodes, "llm_fallback") || nodeExists(started.Nodes, "auto_run") {
		t.Fatalf("expected fallback branch only, got %+v", started.Nodes)
	}
}

func TestWorkflowDecisionFailOpenUsesDefaultChoice(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	a.SetDecisionCaller(&scriptedDecisionCaller{fail: 10, err: errors.New("model 503 unavailable")})

	runWorkflowTool(t, a, context.Background(), map[string]interface{}{
		"action":      "create",
		"workflow_id": "wf_decision_open",
		"nodes": []map[string]interface{}{
			{
				"id":     "decide",
				"kind":   "decision",
				"prompt": "decide",
				"decision": map[string]interface{}{
					"decision_type":  "choice",
					"question":       "decide",
					"on_error":       "fail_open",
					"default_choice": "auto",
					"choices": []map[string]interface{}{
						{"id": "auto"}, {"id": "llm"},
					},
				},
			},
		},
		"edges": []map[string]interface{}{
			{"id": "e", "from": "decide", "when": map[string]interface{}{"choice": "auto"},
				"append": map[string]interface{}{"id": "auto_run", "prompt": "auto"}},
		},
	})
	cleanupWorkflowAfterTest(t, a, "wf_decision_open")
	started := runWorkflowTool(t, a, context.Background(), map[string]interface{}{
		"action": "start", "workflow_id": "wf_decision_open",
	})
	decide := nodeByID(started.Nodes, "decide")
	if decide == nil || decide.Status != workflowStatusCompleted || decide.Decision == nil || decide.Decision.Choice != "auto" {
		t.Fatalf("expected fail_open default choice auto, got %+v", decide)
	}
	if !nodeExists(started.Nodes, "auto_run") {
		t.Fatalf("expected default-choice branch, got %+v", started.Nodes)
	}
}

func TestWorkflowDecisionOnErrorFailMarksNodeError(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	a.SetDecisionCaller(&scriptedDecisionCaller{fail: 10, err: errors.New("boom")})

	runWorkflowTool(t, a, context.Background(), map[string]interface{}{
		"action":      "create",
		"workflow_id": "wf_decision_fail",
		"nodes": []map[string]interface{}{
			{
				"id":     "decide",
				"kind":   "decision",
				"prompt": "decide",
				"decision": map[string]interface{}{
					"decision_type": "boolean",
					"question":      "decide",
					"on_error":      "fail",
				},
			},
		},
	})
	started := runWorkflowTool(t, a, context.Background(), map[string]interface{}{
		"action": "start", "workflow_id": "wf_decision_fail",
	})
	if nodeStatus(started.Nodes, "decide") != workflowStatusError {
		t.Fatalf("expected on_error=fail node error, got %+v", started.Nodes)
	}
}

func TestWorkflowDecisionRetriesTransientFailureThenSucceeds(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	caller := &scriptedDecisionCaller{fail: 2, err: errors.New("decision model timeout"), result: decision.Result{Choice: "auto", Confidence: 0.91}}
	a.SetDecisionCaller(caller)

	runWorkflowTool(t, a, context.Background(), map[string]interface{}{
		"action":      "create",
		"workflow_id": "wf_decision_retry",
		"nodes": []map[string]interface{}{
			{
				"id":     "decide",
				"kind":   "decision",
				"prompt": "decide",
				"retry": map[string]interface{}{
					"max_attempts":        3,
					"initial_interval_ms": 50,
					"max_interval_ms":     100,
					"jitter":              0,
				},
				"decision": map[string]interface{}{
					"decision_type": "choice",
					"question":      "decide",
					"choices":       []map[string]interface{}{{"id": "auto"}},
				},
			},
		},
	})
	start := func() workflowView {
		return runWorkflowTool(t, a, context.Background(), map[string]interface{}{
			"action": "start", "workflow_id": "wf_decision_retry",
		})
	}
	first := start()
	if nodeStatus(first.Nodes, "decide") != workflowStatusPending {
		t.Fatalf("expected node back in pending with backoff, got %+v", first.Nodes)
	}
	// Backoff gate: immediate restart must not execute another attempt.
	immediate := start()
	caller.mu.Lock()
	callsAfterImmediate := caller.calls
	caller.mu.Unlock()
	if callsAfterImmediate != 1 {
		t.Fatalf("retry backoff gate leaked an early attempt: %d", callsAfterImmediate)
	}
	if nodeStatus(immediate.Nodes, "decide") != workflowStatusPending {
		t.Fatalf("expected still pending inside backoff window, got %+v", immediate.Nodes)
	}
	time.Sleep(70 * time.Millisecond)
	second := start()
	time.Sleep(120 * time.Millisecond)
	final := start()
	if nodeStatus(final.Nodes, "decide") != workflowStatusCompleted {
		t.Fatalf("expected decision to succeed after retries, got %+v", second)
	}
	caller.mu.Lock()
	defer caller.mu.Unlock()
	if caller.calls != 3 {
		t.Fatalf("expected three attempts (1 fail + 1 fail + success), got %d", caller.calls)
	}
	events := readWorkflowEvents(workflowEventsPath(a, "wf_decision_retry"))
	retries := 0
	for _, event := range events {
		if event["event"] == "node_retry" {
			retries++
		}
	}
	if retries != 2 {
		t.Fatalf("expected two node_retry events, got %d (%v)", retries, events)
	}
}

// ---------------------------------------------------------------------------
// Pure-function kernel tests
// ---------------------------------------------------------------------------

func TestWorkflowDecisionStopsAtMaxAttemptsThenFailClosed(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	caller := &scriptedDecisionCaller{fail: 100, err: errors.New("503 upstream unavailable")}
	a.SetDecisionCaller(caller)

	runWorkflowTool(t, a, context.Background(), map[string]interface{}{
		"action":      "create",
		"workflow_id": "wf_decision_retry_cap",
		"nodes": []map[string]interface{}{
			{
				"id":     "decide",
				"kind":   "decision",
				"prompt": "decide",
				"retry": map[string]interface{}{
					"max_attempts":        2,
					"initial_interval_ms": 5,
					"max_interval_ms":     10,
					"jitter":              0,
				},
				"decision": map[string]interface{}{
					"decision_type": "choice",
					"question":      "decide",
					"choices":       []map[string]interface{}{{"id": "auto"}},
				},
			},
		},
		"edges": []map[string]interface{}{
			{"id": "to-fallback", "from": "decide",
				"when":   map[string]interface{}{"choice": decisionErrorChoice},
				"append": map[string]interface{}{"id": "llm_fallback", "prompt": "fallback"}},
		},
	})
	cleanupWorkflowAfterTest(t, a, "wf_decision_retry_cap")
	start := func() workflowView {
		return runWorkflowTool(t, a, context.Background(), map[string]interface{}{
			"action": "start", "workflow_id": "wf_decision_retry_cap",
		})
	}
	start()
	time.Sleep(15 * time.Millisecond)
	final := start()
	decide := nodeByID(final.Nodes, "decide")
	if decide == nil || decide.Status != workflowStatusCompleted || decide.Verdict != workflowVerdictBlocked {
		t.Fatalf("expected fail_closed completion after exhausted retries, got %+v", decide)
	}
	if decide.Decision == nil || decide.Decision.Choice != decisionErrorChoice {
		t.Fatalf("expected reserved error choice, got %+v", decide)
	}
	if !nodeExists(final.Nodes, "llm_fallback") {
		t.Fatalf("expected fallback branch after retries exhausted, got %+v", final.Nodes)
	}
	caller.mu.Lock()
	defer caller.mu.Unlock()
	if caller.calls != 2 {
		t.Fatalf("attempt cap violated: expected exactly 2 calls, got %d", caller.calls)
	}
}

// ---------------------------------------------------------------------------
// Pure-function kernel tests (continued)
// ---------------------------------------------------------------------------

func TestWorkflowRetryPolicyBackoffAndCap(t *testing.T) {
	p := normalizeWorkflowRetryPolicy(&workflowRetryPolicy{MaxAttempts: 6, InitialIntervalMS: 100, BackoffCoefficient: 2, MaxIntervalMS: 1000, Jitter: 0})
	d0 := workflowRetryDelay(p, 0, nil)
	d1 := workflowRetryDelay(p, 1, nil)
	d5 := workflowRetryDelay(p, 5, nil)
	if d0 != 100*time.Millisecond || d1 != 200*time.Millisecond {
		t.Fatalf("unexpected exponential delays: %v %v", d0, d1)
	}
	if d5 != time.Second {
		t.Fatalf("expected max interval cap, got %v", d5)
	}
	if retry, _ := shouldWorkflowRetry(p, 6, retryKindProviderError); retry {
		t.Fatal("retries must stop at max_attempts")
	}
	if retry, _ := shouldWorkflowRetry(p, 2, retryKindToolTransient); !retry {
		t.Fatal("transient tool failure should retry")
	}
	denied := normalizeWorkflowRetryPolicy(&workflowRetryPolicy{MaxAttempts: 5, NonRetryable: []string{"provider_error"}})
	if retry, _ := shouldWorkflowRetry(denied, 1, retryKindProviderError); retry {
		t.Fatal("explicit non_retryable kind must not retry")
	}
	if retry, _ := shouldWorkflowRetry(nil, 1, retryKindProviderError); retry {
		t.Fatal("nil retry policy must not retry")
	}
}

func TestClassifyWorkflowFailureMatrix(t *testing.T) {
	cases := []struct {
		status string
		err    string
		want   string
	}{
		{"timeout", "", retryKindProviderTimeout},
		{"error", "context deadline exceeded", retryKindProviderTimeout},
		{"error", "429 rate limit exceeded", retryKindProviderError},
		{"error", "503 service unavailable", retryKindProviderError},
		{"error", "connection reset by peer", retryKindToolTransient},
		{"error", "dial tcp: connection refused", retryKindToolTransient},
		{"error", "permission denied: /etc/hosts", ""},
		{"error", "verdict: fail, schema mismatch", ""},
		{"error", "", ""},
		{"canceled", "", ""},
	}
	for _, tc := range cases {
		if got := classifyWorkflowFailure(tc.status, tc.err); got != tc.want {
			t.Errorf("classify(%q,%q)=%q want %q", tc.status, tc.err, got, tc.want)
		}
	}
}

func TestWorkflowEdgeConditionPredicates(t *testing.T) {
	node := workflowNode{
		Status:   workflowStatusCompleted,
		Decision: &workflowDecisionResult{Choice: "auto", Confidence: 0.7},
		Outputs: map[string]any{
			"tier":   "gray",
			"count":  float64(3),
			"tags":   []any{"fix", "review"},
			"nested": map[string]any{"risk": "low"},
		},
	}
	match := func(c workflowEdgeCondition) bool { return workflowConditionMatchesNode(c, node) }
	if !match(workflowEdgeCondition{Choice: "auto"}) {
		t.Error("choice predicate should match")
	}
	if match(workflowEdgeCondition{Choice: "llm"}) {
		t.Error("choice predicate should not match")
	}
	if !match(workflowEdgeCondition{Confidence: &workflowNumCompare{Op: "gte", Value: 0.7}}) {
		t.Error("confidence gte should match")
	}
	if match(workflowEdgeCondition{Confidence: &workflowNumCompare{Op: "gt", Value: 0.7}}) {
		t.Error("confidence gt should not match at equality")
	}
	if !match(workflowEdgeCondition{Output: &workflowFieldCompare{Path: "tier", Op: "eq", Value: "gray"}}) {
		t.Error("output eq should match")
	}
	if !match(workflowEdgeCondition{Output: &workflowFieldCompare{Path: "outputs.tier", Op: "ne", Value: "green"}}) {
		t.Error("outputs.-prefixed path should work")
	}
	if !match(workflowEdgeCondition{Output: &workflowFieldCompare{Path: "tags", Op: "contains", Value: "review"}}) {
		t.Error("contains should match slice element")
	}
	if !match(workflowEdgeCondition{Output: &workflowFieldCompare{Path: "nested.risk", Op: "eq", Value: "low"}}) {
		t.Error("dotted path should match")
	}
	if !match(workflowEdgeCondition{Output: &workflowFieldCompare{Path: "count", Op: "gte", Value: 3}}) {
		t.Error("numeric output compare should match")
	}
	if match(workflowEdgeCondition{Output: &workflowFieldCompare{Path: "missing", Op: "eq", Value: "x"}}) {
		t.Error("missing path should not match")
	}
	all := workflowEdgeCondition{All: []workflowEdgeCondition{
		{Choice: "auto"},
		{Confidence: &workflowNumCompare{Op: "gte", Value: 0.7}},
	}}
	if !match(all) {
		t.Error("all[] should match when every clause matches")
	}
	anyCond := workflowEdgeCondition{Any: []workflowEdgeCondition{
		{Choice: "llm"},
		{Output: &workflowFieldCompare{Path: "tier", Op: "eq", Value: "gray"}},
	}}
	if !match(anyCond) {
		t.Error("any[] should match when one clause matches")
	}
}

func TestNormalizeWorkflowDecisionSpecValidation(t *testing.T) {
	if _, err := normalizeWorkflowDecisionSpec(&workflowDecisionSpec{DecisionType: "choice"}); err == nil {
		t.Error("choice spec without choices must fail")
	}
	dup := &workflowDecisionSpec{
		DecisionType: "choice",
		Choices:      []workflowDecisionChoice{{ID: "a"}, {ID: "a"}},
	}
	if _, err := normalizeWorkflowDecisionSpec(dup); err == nil {
		t.Error("duplicate choice ids must fail")
	}
	failOpenNoDefault := &workflowDecisionSpec{
		DecisionType: "choice",
		OnError:      "fail_open",
		Choices:      []workflowDecisionChoice{{ID: "a"}},
	}
	if _, err := normalizeWorkflowDecisionSpec(failOpenNoDefault); err == nil {
		t.Error("fail_open without default_choice must fail")
	}
	boolean, err := normalizeWorkflowDecisionSpec(&workflowDecisionSpec{DecisionType: "boolean", OnError: "fail"})
	if err != nil {
		t.Fatalf("boolean spec without choices should be valid: %v", err)
	}
	if boolean.OnError != "fail" {
		t.Fatalf("on_error not preserved: %+v", boolean)
	}
	invalidType := &workflowDecisionSpec{DecisionType: "essay"}
	if _, err := normalizeWorkflowDecisionSpec(invalidType); err == nil {
		t.Error("unknown decision_type must fail")
	}
}

func nodeByID(nodes []workflowNodeView, id string) *workflowNodeView {
	for i := range nodes {
		if nodes[i].ID == id {
			return &nodes[i]
		}
	}
	return nil
}

func workflowEventsPath(a *Agent, workflowID string) string {
	return a.workflows.dir + "/" + workflowID + "/" + workflowEventsFile
}
