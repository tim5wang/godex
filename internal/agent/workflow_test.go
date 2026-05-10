package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkflowToolStartsReadyNodesAndWaits(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	a.client = repeatedTextCaller("workflow handoff")

	ctx := context.Background()
	created := runWorkflowTool(t, a, ctx, map[string]interface{}{
		"action":      "create",
		"workflow_id": "wf_test",
		"nodes": []map[string]interface{}{
			{"id": "plan", "title": "Plan", "prompt": "inspect and plan"},
			{"id": "impl", "title": "Implement", "prompt": "apply the plan", "depends_on": []string{"plan"}},
		},
	})
	if created.WorkflowID != "wf_test" || created.Pending != 2 {
		t.Fatalf("expected created pending workflow, got %+v", created)
	}

	started := runWorkflowTool(t, a, ctx, map[string]interface{}{
		"action":      "start",
		"workflow_id": "wf_test",
	})
	if len(started.Started) != 1 || started.Started[0] != "plan" {
		t.Fatalf("expected only first ready node to start, got %+v", started)
	}
	if nodeStatus(started.Nodes, "impl") != workflowStatusPending {
		t.Fatalf("expected dependent node to remain pending, got %+v", started.Nodes)
	}

	waited := runWorkflowTool(t, a, ctx, map[string]interface{}{
		"action":      "wait",
		"workflow_id": "wf_test",
		"mode":        "all",
		"timeout_ms":  2000,
	})
	if waited.Wait != nil && waited.Wait.Status != "completed" {
		t.Fatalf("expected wait summary to be completed when present, got %+v", waited)
	}
	if nodeStatus(waited.Nodes, "plan") != workflowStatusCompleted {
		t.Fatalf("expected plan node to complete, got %+v", waited.Nodes)
	}

	started = runWorkflowTool(t, a, ctx, map[string]interface{}{
		"action":      "start",
		"workflow_id": "wf_test",
	})
	if len(started.Started) != 1 || started.Started[0] != "impl" {
		t.Fatalf("expected dependent node to start after dependency completion, got %+v", started)
	}

	waited = runWorkflowTool(t, a, ctx, map[string]interface{}{
		"action":      "wait",
		"workflow_id": "wf_test",
		"mode":        "all",
		"timeout_ms":  2000,
	})
	if waited.Status != workflowStatusCompleted || waited.Completed != 2 {
		t.Fatalf("expected completed workflow, got %+v", waited)
	}
	if preview := nodeResultPreview(waited.Nodes, "impl"); preview != "workflow handoff" {
		t.Fatalf("expected result preview from subagent handoff, got %q", preview)
	}
	if handoffRef := nodeHandoffRef(waited.Nodes, "impl"); handoffRef == "" {
		t.Fatalf("expected impl handoff ref, got %+v", waited.Nodes)
	} else if strings.Contains(mustJSON(t, waited.Nodes), "apply the plan") {
		t.Fatalf("workflow view should not expose raw node prompts: %s", mustJSON(t, waited.Nodes))
	}

	for _, path := range []string{
		filepath.Join(a.workflows.dir, "wf_test", workflowSummaryFile),
		filepath.Join(a.workflows.dir, "wf_test", workflowNodesFile),
		filepath.Join(a.workflows.dir, "wf_test", workflowEventsFile),
		filepath.Join(a.workflows.dir, "wf_test", workflowHandoffsDir, "impl", "1.json"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected workflow file %s: %v", path, err)
		}
	}
	events := readWorkflowEvents(filepath.Join(a.workflows.dir, "wf_test", workflowEventsFile))
	if len(events) < 3 {
		t.Fatalf("expected workflow events to be persisted, got %v", events)
	}
	reloaded := newWorkflowStore(a.workflows.dir)
	state, err := reloaded.load("wf_test")
	if err != nil {
		t.Fatalf("reload workflow: %v", err)
	}
	if state.Summary.Status != workflowStatusCompleted {
		t.Fatalf("expected reloaded workflow to be completed, got %+v", state.Summary)
	}
}

func TestWorkflowHandoffArtifactsForManualAndErrorNodes(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)

	manual := runWorkflowTool(t, a, context.Background(), map[string]interface{}{
		"action":      "create",
		"workflow_id": "wf_manual",
		"nodes": []map[string]interface{}{
			{"id": "review", "prompt": "review manually"},
		},
	})
	if manual.Pending != 1 {
		t.Fatalf("expected pending workflow, got %+v", manual)
	}
	manual = runWorkflowTool(t, a, context.Background(), map[string]interface{}{
		"action":      "complete_node",
		"workflow_id": "wf_manual",
		"node_id":     "review",
		"result":      "Verdict: needs_fix\nPlease update tests.",
	})
	if got := nodeVerdict(manual.Nodes, "review"); got != workflowVerdictNeedsFix {
		t.Fatalf("expected parsed needs_fix verdict, got %q", got)
	}
	ref := nodeHandoffRef(manual.Nodes, "review")
	if ref == "" {
		t.Fatalf("expected manual handoff ref, got %+v", manual.Nodes)
	}
	var handoff workflowNodeHandoff
	if err := readJSONFile(filepath.Join(a.workflows.dir, "wf_manual", filepath.FromSlash(ref)), &handoff); err != nil {
		t.Fatalf("read manual handoff: %v", err)
	}
	if handoff.Digest == "" || handoff.Verdict != workflowVerdictNeedsFix || handoff.Summary == "" {
		t.Fatalf("unexpected manual handoff: %+v", handoff)
	}

	state, err := a.workflows.create("", "wf_error", []workflowNodeInput{{ID: "broken", Prompt: "will fail"}}, nil)
	if err != nil {
		t.Fatalf("create workflow: %v", err)
	}
	state.Nodes[0].Status = workflowStatusError
	state.Nodes[0].Error = "Verdict: blocked\nmissing dependency"
	state.Nodes[0].Attempt = 1
	if err := a.finalizeWorkflowNodeHandoff(&state, &state.Nodes[0], nil, ""); err != nil {
		t.Fatalf("finalize error handoff: %v", err)
	}
	if state.Nodes[0].Verdict != workflowVerdictBlocked || state.Nodes[0].HandoffDigest == "" {
		t.Fatalf("expected blocked error handoff, got %+v", state.Nodes[0])
	}
}

func TestWorkflowDependencyHandoffPromptIsBounded(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)

	runWorkflowTool(t, a, context.Background(), map[string]interface{}{
		"action":      "create",
		"workflow_id": "wf_handoff_prompt",
		"nodes": []map[string]interface{}{
			{"id": "plan", "prompt": "plan the work"},
			{"id": "impl", "prompt": "implement it", "depends_on": []string{"plan"}, "handoff_max_bytes": 180},
		},
	})
	runWorkflowTool(t, a, context.Background(), map[string]interface{}{
		"action":      "complete_node",
		"workflow_id": "wf_handoff_prompt",
		"node_id":     "plan",
		"result":      "Verdict: pass\n" + strings.Repeat("handoff detail ", 400),
	})
	state, err := a.workflowState("wf_handoff_prompt")
	if err != nil {
		t.Fatalf("workflow state: %v", err)
	}
	prompt, err := a.workflowNodePrompt(state, state.Nodes[1])
	if err != nil {
		t.Fatalf("workflow node prompt: %v", err)
	}
	if !strings.Contains(prompt, "Dependency handoffs:") || !strings.Contains(prompt, "node: plan") {
		t.Fatalf("expected dependency handoff in prompt, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "[dependency handoffs truncated]") {
		t.Fatalf("expected bounded handoff truncation, got:\n%s", prompt)
	}
	if strings.Contains(prompt, strings.Repeat("handoff detail ", 30)) {
		t.Fatalf("dependency handoff was not bounded enough:\n%s", prompt)
	}
}

func TestWorkflowPreviewMergeAppliesDependencyChangesToChildWorkspace(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)

	mainPath := filepath.Join(a.cfg.WorkspaceDir, "app.txt")
	if err := os.WriteFile(mainPath, []byte("base\n"), 0644); err != nil {
		t.Fatalf("write main file: %v", err)
	}
	dep, err := a.subagentJobs.StartWithOptions(subagentStartOptions{
		AgentType:  "general-purpose",
		Prompt:     "dependency edits app",
		WriteScope: []string{"app.txt"},
		MaxTurns:   1,
	})
	if err != nil {
		t.Fatalf("start dep job: %v", err)
	}
	dep, err = a.prepareSubagentWorkspace(dep)
	if err != nil {
		t.Fatalf("prepare dep workspace: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dep.WorktreeDir, "app.txt"), []byte("dep\n"), 0644); err != nil {
		t.Fatalf("write dep worktree: %v", err)
	}
	dep, err = a.subagentJobs.Finish(dep.ID, subagentStatusCompleted, "Verdict: pass\nupdated app", "")
	if err != nil {
		t.Fatalf("finish dep job: %v", err)
	}

	child, err := a.subagentJobs.StartWithOptions(subagentStartOptions{
		AgentType:     "general-purpose",
		Prompt:        "test dependency edits",
		WriteScope:    []string{"app.txt"},
		PreviewJobIDs: []string{dep.ID},
		MaxTurns:      1,
	})
	if err != nil {
		t.Fatalf("start child job: %v", err)
	}
	child, err = a.prepareSubagentWorkspace(child)
	if err != nil {
		t.Fatalf("prepare child workspace: %v", err)
	}
	mainData, err := os.ReadFile(mainPath)
	if err != nil {
		t.Fatalf("read main file: %v", err)
	}
	if string(mainData) != "base\n" {
		t.Fatalf("preview merge should not mutate main workspace, got %q", string(mainData))
	}
	for _, path := range []string{
		filepath.Join(child.WorktreeDir, "app.txt"),
		filepath.Join(child.BaselineDir, "app.txt"),
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read preview file %s: %v", path, err)
		}
		if string(data) != "dep\n" {
			t.Fatalf("expected preview dependency content at %s, got %q", path, string(data))
		}
	}
	if err := os.WriteFile(filepath.Join(child.WorktreeDir, "app.txt"), []byte("dep plus test\n"), 0644); err != nil {
		t.Fatalf("write child worktree: %v", err)
	}
	review, err := reviewSubagentJob(child)
	if err != nil {
		t.Fatalf("review child job: %v", err)
	}
	if len(review.Changes) != 1 || review.Changes[0].Path != "app.txt" || review.Changes[0].Status != "modified" {
		t.Fatalf("expected only child delta after preview baseline, got %+v", review.Changes)
	}
}

func TestWorkflowAppendNodeIsIdempotentAndValidatesDAG(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)

	runWorkflowTool(t, a, context.Background(), map[string]interface{}{
		"action":      "create",
		"workflow_id": "wf_append",
		"nodes": []map[string]interface{}{
			{"id": "plan", "prompt": "plan"},
		},
	})
	appended := runWorkflowTool(t, a, context.Background(), map[string]interface{}{
		"action":          "append_node",
		"workflow_id":     "wf_append",
		"idempotency_key": "fix-1",
		"parent_node_id":  "plan",
		"reason":          "tests failed",
		"nodes": []map[string]interface{}{
			{"id": "fix", "prompt": "fix it", "depends_on": []string{"plan"}},
		},
	})
	if len(appended.Appended) != 1 || appended.Appended[0] != "fix" || appended.Total != 2 {
		t.Fatalf("expected appended fix node, got %+v", appended)
	}
	replayed := runWorkflowTool(t, a, context.Background(), map[string]interface{}{
		"action":          "append_node",
		"workflow_id":     "wf_append",
		"idempotency_key": "fix-1",
		"nodes": []map[string]interface{}{
			{"id": "different", "prompt": "should not append"},
		},
	})
	if len(replayed.Appended) != 1 || replayed.Appended[0] != "fix" || replayed.Total != 2 {
		t.Fatalf("expected idempotent replay, got %+v", replayed)
	}
	if _, err := a.handleTool(context.Background(), "workflow", map[string]interface{}{
		"action":      "append_node",
		"workflow_id": "wf_append",
		"nodes": []map[string]interface{}{
			{"id": "bad", "prompt": "bad", "depends_on": []string{"missing"}},
		},
	}); err == nil {
		t.Fatal("expected append with unknown dependency to fail")
	}
	if _, err := a.handleTool(context.Background(), "workflow", map[string]interface{}{
		"action":      "append_node",
		"workflow_id": "wf_append",
		"nodes": []map[string]interface{}{
			{"id": "plan", "prompt": "duplicate"},
		},
	}); err == nil {
		t.Fatal("expected duplicate append node id to fail")
	}
}

func TestWorkflowAppendNodeRejectsNodeLimit(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)

	runWorkflowTool(t, a, context.Background(), map[string]interface{}{
		"action":      "create",
		"workflow_id": "wf_append_limit",
		"nodes": []map[string]interface{}{
			{"id": "root", "prompt": "root"},
		},
	})
	var nodes []map[string]interface{}
	for i := 0; i < workflowMaxNodes; i++ {
		_ = i
		nodes = append(nodes, map[string]interface{}{
			"id":     "extra",
			"prompt": "extra",
		})
	}
	if _, err := a.handleTool(context.Background(), "workflow", map[string]interface{}{
		"action":      "append_node",
		"workflow_id": "wf_append_limit",
		"nodes":       nodes,
	}); err == nil {
		t.Fatal("expected append over workflow node limit to fail")
	}
}

func TestWorkflowConditionalEdgeAppendsNodeOnce(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)

	runWorkflowTool(t, a, context.Background(), map[string]interface{}{
		"action":      "create",
		"workflow_id": "wf_edge",
		"nodes": []map[string]interface{}{
			{"id": "test", "kind": "test", "prompt": "run tests"},
		},
		"edges": []map[string]interface{}{
			{
				"id":             "test-failed",
				"from_kind":      "test",
				"when":           map[string]interface{}{"verdict": "needs_fix"},
				"iteration_key":  "repair",
				"max_iterations": 3,
				"append": map[string]interface{}{
					"id":         "fix_{iteration}",
					"kind":       "fix",
					"prompt":     "Fix issues reported by {source}.",
					"depends_on": []string{"{source}"},
				},
			},
		},
	})
	afterComplete := runWorkflowTool(t, a, context.Background(), map[string]interface{}{
		"action":      "complete_node",
		"workflow_id": "wf_edge",
		"node_id":     "test",
		"result":      "Verdict: needs_fix\nregression failed",
	})
	if afterComplete.Total != 2 || !nodeExists(afterComplete.Nodes, "fix_1") {
		t.Fatalf("expected conditional edge to append fix_1, got %+v", afterComplete)
	}
	reloaded := runWorkflowTool(t, a, context.Background(), map[string]interface{}{
		"action":      "status",
		"workflow_id": "wf_edge",
	})
	if reloaded.Total != 2 {
		t.Fatalf("expected edge not to append again on status reload, got %+v", reloaded)
	}
}

func TestWorkflowConditionalEdgeIterationCapStopsLoop(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)

	runWorkflowTool(t, a, context.Background(), map[string]interface{}{
		"action":      "create",
		"workflow_id": "wf_edge_cap",
		"nodes": []map[string]interface{}{
			{"id": "test_1", "kind": "test", "prompt": "run tests"},
		},
		"edges": []map[string]interface{}{
			{
				"id":             "test-failed",
				"from_kind":      "test",
				"when":           map[string]interface{}{"verdict": "needs_fix"},
				"iteration_key":  "repair",
				"max_iterations": 1,
				"append": map[string]interface{}{
					"id":         "fix_{iteration}",
					"kind":       "fix",
					"prompt":     "Fix issues reported by {source}.",
					"depends_on": []string{"{source}"},
				},
			},
		},
	})
	runWorkflowTool(t, a, context.Background(), map[string]interface{}{
		"action":      "complete_node",
		"workflow_id": "wf_edge_cap",
		"node_id":     "test_1",
		"result":      "Verdict: needs_fix\nfirst failure",
	})
	runWorkflowTool(t, a, context.Background(), map[string]interface{}{
		"action":      "append_node",
		"workflow_id": "wf_edge_cap",
		"nodes": []map[string]interface{}{
			{"id": "test_2", "kind": "test", "prompt": "run tests again"},
		},
	})
	capped := runWorkflowTool(t, a, context.Background(), map[string]interface{}{
		"action":      "complete_node",
		"workflow_id": "wf_edge_cap",
		"node_id":     "test_2",
		"result":      "Verdict: needs_fix\nsecond failure",
	})
	if capped.Status != workflowStatusError || nodeStatus(capped.Nodes, "test_2") != workflowStatusError {
		t.Fatalf("expected iteration cap to mark workflow and source node error, got %+v", capped)
	}
	if capped.Total != 3 {
		t.Fatalf("expected no second fix node after cap, got %+v", capped)
	}
}

func TestWorkflowToolRejectsInvalidDAG(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)

	if _, err := a.handleTool(context.Background(), "workflow", map[string]interface{}{
		"action": "create",
		"nodes": []map[string]interface{}{
			{"id": "impl", "prompt": "do work", "depends_on": []string{"missing"}},
		},
	}); err == nil {
		t.Fatal("expected unknown dependency to fail")
	}

	if _, err := a.handleTool(context.Background(), "workflow", map[string]interface{}{
		"action": "create",
		"nodes": []map[string]interface{}{
			{"id": "a", "prompt": "a", "depends_on": []string{"b"}},
			{"id": "b", "prompt": "b", "depends_on": []string{"a"}},
		},
	}); err == nil {
		t.Fatal("expected dependency cycle to fail")
	}

	if _, err := a.handleTool(context.Background(), "workflow", map[string]interface{}{
		"action":      "create",
		"workflow_id": "../escape",
		"nodes": []map[string]interface{}{
			{"id": "safe", "prompt": "do work"},
		},
	}); err == nil {
		t.Fatal("expected path-like workflow id to fail")
	}

	if _, err := a.handleTool(context.Background(), "workflow", map[string]interface{}{
		"action":      "create",
		"workflow_id": ".",
		"nodes": []map[string]interface{}{
			{"id": "safe", "prompt": "do work"},
		},
	}); err == nil {
		t.Fatal("expected dot workflow id to fail")
	}
}

func runWorkflowTool(t *testing.T, a *Agent, ctx context.Context, input map[string]interface{}) workflowView {
	t.Helper()
	output, err := a.handleTool(ctx, "workflow", input)
	if err != nil {
		t.Fatalf("workflow tool: %v", err)
	}
	var view workflowView
	if err := json.Unmarshal([]byte(output), &view); err != nil {
		t.Fatalf("unmarshal workflow view: %v\n%s", err, output)
	}
	return view
}

func nodeStatus(nodes []workflowNodeView, id string) string {
	for _, node := range nodes {
		if node.ID == id {
			return node.Status
		}
	}
	return ""
}

func nodeResultPreview(nodes []workflowNodeView, id string) string {
	for _, node := range nodes {
		if node.ID == id {
			return node.ResultPreview
		}
	}
	return ""
}

func nodeHandoffRef(nodes []workflowNodeView, id string) string {
	for _, node := range nodes {
		if node.ID == id {
			return node.HandoffRef
		}
	}
	return ""
}

func nodeVerdict(nodes []workflowNodeView, id string) string {
	for _, node := range nodes {
		if node.ID == id {
			return node.Verdict
		}
	}
	return ""
}

func nodeExists(nodes []workflowNodeView, id string) bool {
	for _, node := range nodes {
		if node.ID == id {
			return true
		}
	}
	return false
}

func mustJSON(t *testing.T, value interface{}) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal json: %v", err)
	}
	return string(data)
}
