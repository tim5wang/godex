package agent

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/decision"
	"github.com/tim5wang/godex/internal/core/flow"
)

func flowTestDef() *flow.Definition {
	return &flow.Definition{
		FlowID:      "fl_order_recovery",
		Name:        "Order Recovery",
		Description: "recover stuck orders",
		Version:     "1",
		Status:      "draft",
		Nodes: []flow.Node{
			{ID: "decide", Kind: flow.KindDecision, Prompt: "should we auto-refund?",
				Decision: &flow.DecisionSpec{DecisionType: "choice", Choices: []flow.Choice{{ID: "auto"}, {ID: "llm"}}}},
			{ID: "auto_run", Kind: flow.KindStep, Prompt: "auto-refund the order"},
			{ID: "llm_run", Kind: flow.KindStep, Prompt: "escalate to an agent"},
			{ID: "br", Kind: flow.KindBranch,
				Branch: &flow.BranchSpec{
					Cases: []flow.BranchCase{
						{Name: "auto", To: "auto_run", Condition: flow.Condition{Choice: "auto"}},
						{Name: "llm", To: "llm_run", Condition: flow.Condition{Choice: "llm"}},
					},
					DefaultTo: "llm_run",
				}},
		},
		Edges: []flow.Edge{
			{ID: "e1", From: "decide", To: "br", EdgeType: flow.EdgeDataDependency},
		},
	}
}

func TestFlowStoreCreateListGetPublish(t *testing.T) {
	a := newTestAgent(t, 4096)
	if a.flows == nil {
		t.Fatal("expected flows store to be wired")
	}

	v, err := a.CreateFlow(FlowCreateArgs{Def: flowTestDef()})
	if err != nil {
		t.Fatalf("create flow: %v", err)
	}
	if v.Version != "1" || v.Status != FlowStatusDraft || v.Nodes != 4 {
		t.Fatalf("unexpected created view: %+v", v)
	}
	if v.Digest == "" {
		t.Fatal("expected compiled digest")
	}

	summaries, err := a.ListFlows()
	if err != nil {
		t.Fatalf("list flows: %v", err)
	}
	if len(summaries) != 1 || summaries[0].FlowID != "fl_order_recovery" || summaries[0].Draft != "1" {
		t.Fatalf("unexpected summaries: %+v", summaries)
	}

	versions, err := a.ListFlowVersions("fl_order_recovery")
	if err != nil {
		t.Fatalf("list versions: %v", err)
	}
	if len(versions) != 1 {
		t.Fatalf("expected 1 version, got %d", len(versions))
	}

	// Publish.
	pub, err := a.PublishFlow("fl_order_recovery", "1")
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if pub.Status != FlowStatusPublished {
		t.Fatalf("expected published, got %q", pub.Status)
	}
	summaries, _ = a.ListFlows()
	if summaries[0].Published != "1" {
		t.Fatalf("expected published lane set: %+v", summaries[0])
	}

	// ResolveRunVersion prefers published.
	rec, err := a.ResolveRunVersion("fl_order_recovery", "")
	if err != nil {
		t.Fatalf("resolve run version: %v", err)
	}
	if rec.Version != "1" || rec.Compiled == nil {
		t.Fatalf("unexpected resolved record: %+v", rec)
	}
}

func TestFlowStoreRejectsInvalidDefinition(t *testing.T) {
	a := newTestAgent(t, 4096)
	def := flowTestDef()
	def.Nodes[0].Prompt = "" // decision requires prompt
	if _, err := a.CreateFlow(FlowCreateArgs{Def: def}); err == nil {
		t.Fatal("expected create to reject invalid flow")
	}
}

func TestFlowRunLifecycle(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	a.SetDecisionCaller(&scriptedDecisionCaller{result: decision.Result{Choice: "auto", Confidence: 0.93}})

	if _, err := a.CreateFlow(FlowCreateArgs{Def: flowTestDef()}); err != nil {
		t.Fatalf("create flow: %v", err)
	}
	if _, err := a.PublishFlow("fl_order_recovery", "1"); err != nil {
		t.Fatalf("publish: %v", err)
	}

	ctx := context.Background()
	run, err := a.CreateFlowRun(ctx, "fl_order_recovery", "", map[string]any{"order_id": "o-1"})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	if run.Status != workflowStatusPending || run.WorkflowID == "" {
		t.Fatalf("unexpected run: %+v", run)
	}

	started, err := a.StartFlowRun(ctx, "fl_order_recovery", run.RunID)
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	if started.Status == workflowStatusError {
		t.Fatalf("run errored: %+v", started)
	}

	// Decision + branch gateways complete synchronously; subagent targets
	// (auto_run/llm_run) need a real worker and stay running in the test env.
	view, err := a.workflowState(started.WorkflowID)
	if err != nil {
		t.Fatalf("workflow state: %v", err)
	}
	decide := workflowNodeByID(view.Nodes, "decide")
	if decide == nil || decide.Status != workflowStatusCompleted {
		t.Fatalf("expected decision completed, got %+v", decide)
	}
	br := workflowNodeByID(view.Nodes, "br")
	if br == nil || br.Outputs["choice"] != "auto" {
		t.Fatalf("expected branch routed to auto: %+v", br)
	}
	// The branch's chosen target was appended as a durable node.
	if workflowNodeByID(view.Nodes, "auto_run") == nil {
		t.Fatalf("expected auto_run appended, nodes: %+v", view.Nodes)
	}

	// Refresh syncs run status from the workflow (running while subagent jobs
	// are in flight, completed when no jobs remain).
	refreshed, err := a.RefreshFlowRun("fl_order_recovery", run.RunID)
	if err != nil {
		t.Fatalf("refresh run: %v", err)
	}
	if refreshed.Status == "" {
		t.Fatalf("expected a run status after refresh: %+v", refreshed)
	}

	// Runs are listed newest-first.
	runs, err := a.ListFlowRuns("fl_order_recovery")
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("expected 1 run, got %d", len(runs))
	}

	// Cancel a second run.
	run2, err := a.CreateFlowRun(ctx, "fl_order_recovery", "", nil)
	if err != nil {
		t.Fatalf("create run 2: %v", err)
	}
	canceled, err := a.CancelFlowRun(ctx, "fl_order_recovery", run2.RunID)
	if err != nil {
		t.Fatalf("cancel run: %v", err)
	}
	if canceled.Status != workflowStatusCanceled {
		t.Fatalf("expected canceled, got %q", canceled.Status)
	}
}

func TestFlowStorePersistsAcrossAgents(t *testing.T) {
	workspace := t.TempDir()
	cfg := testFlowConfig(workspace)
	a1 := New(cfg)
	if _, err := a1.CreateFlow(FlowCreateArgs{Def: flowTestDef()}); err != nil {
		t.Fatalf("create on a1: %v", err)
	}
	a2 := New(cfg)
	summaries, err := a2.ListFlows()
	if err != nil {
		t.Fatalf("list on a2: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("expected persisted flow visible on a2, got %+v", summaries)
	}
}

// testFlowConfig builds a minimal config sharing the given workspace so two
// agents can observe the same on-disk flow store.
func testFlowConfig(workspace string) *config.Config {
	return &config.Config{
		Model:             "test-model",
		BaseURL:           "http://127.0.0.1",
		MaxTokens:         1024,
		WorkspaceDir:      workspace,
		StateDir:          filepath.Join(workspace, ".godex"),
		TeamDir:           filepath.Join(workspace, ".godex", ".team"),
		TasksDir:          filepath.Join(workspace, ".godex", ".tasks"),
		TodosDir:          filepath.Join(workspace, ".godex", ".todos"),
		MemoryDir:         filepath.Join(workspace, ".godex", "memory"),
		RulesDir:          filepath.Join(workspace, ".godex", "rules"),
		SkillsDir:         filepath.Join(workspace, ".godex", "skills"),
		MCPConfigPath:     filepath.Join(workspace, ".godex", "mcp.json"),
		TempDir:           filepath.Join(workspace, ".godex", ".tmp"),
		TranscriptsDir:    filepath.Join(workspace, ".godex", ".transcripts"),
		CompressThreshold: 4096,
		LeadName:          "lead",
		TeamName:          "default",
		Tools:             config.ToolsConfig{},
	}
}

var _ = time.Now
var _ = strings.TrimSpace
