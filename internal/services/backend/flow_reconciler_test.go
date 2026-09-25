package backend

import (
	"context"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/agent"
	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/core/flow"
)

func TestFlowRunReconcilerStartsAsyncDependents(t *testing.T) {
	cfg := newTestConfig(t)
	caller := &stubCaller{responses: []protocol.Response{
		{Content: []protocol.Block{protocol.TextBlock("first complete")}},
		{Content: []protocol.Block{protocol.TextBlock("second complete")}},
	}}
	service := newTestService(cfg, caller)
	def := &flow.Definition{
		FlowID: "fl_reconciler", Version: "1", Status: "draft",
		Nodes: []flow.Node{
			{ID: "first", Kind: flow.KindStep, Prompt: "finish first"},
			{ID: "second", Kind: flow.KindStep, Prompt: "finish second"},
		},
		Edges: []flow.Edge{
			{ID: "first_to_second", From: "first", To: "second", EdgeType: flow.EdgeDataDependency},
		},
	}
	if _, err := service.CreateFlow(agent.FlowCreateArgs{Def: def}); err != nil {
		t.Fatalf("create flow: %v", err)
	}
	if _, err := service.PublishFlow(def.FlowID, def.Version); err != nil {
		t.Fatalf("publish flow: %v", err)
	}
	run, err := service.CreateFlowRun(context.Background(), def.FlowID, "", nil)
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	if _, err := service.StartFlowRun(context.Background(), def.FlowID, run.RunID); err != nil {
		t.Fatalf("start run: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := service.Start(ctx); err != nil {
		t.Fatalf("start backend lifecycle: %v", err)
	}
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
		defer stopCancel()
		if err := service.Stop(stopCtx); err != nil {
			t.Errorf("stop backend lifecycle: %v", err)
		}
	}()

	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		view, err := service.RefreshFlowRun(def.FlowID, run.RunID)
		if err != nil {
			t.Fatalf("refresh run: %v", err)
		}
		if view.Status == "completed" {
			events, err := service.FlowRunEvents(def.FlowID, run.RunID)
			if err != nil {
				t.Fatalf("flow run events: %v", err)
			}
			for _, event := range events {
				if event["event"] == "node_completed" && event["node_id"] == "second" {
					return
				}
			}
			t.Fatal("run completed without recording completion of the dependent node")
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("expected backend reconciler to start and complete async dependents")
}
