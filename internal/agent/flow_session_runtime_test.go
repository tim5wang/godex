package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/core/conversation"
	"github.com/tim5wang/godex/internal/core/flow"
)

type sessionFlowLLMTestCaller struct {
	mu           sync.Mutex
	reply        string
	failures     []error
	requests     []protocol.Request
	contexts     []context.Context
	entered      chan struct{}
	release      <-chan struct{}
	ignoreCancel bool
}

func (c *sessionFlowLLMTestCaller) Call(ctx context.Context, req protocol.Request) (*protocol.Response, error) {
	c.mu.Lock()
	call := len(c.requests)
	c.requests = append(c.requests, req)
	c.contexts = append(c.contexts, ctx)
	var callErr error
	if call < len(c.failures) {
		callErr = c.failures[call]
	}
	c.mu.Unlock()
	if c.entered != nil {
		select {
		case c.entered <- struct{}{}:
		default:
		}
	}
	if c.release != nil {
		if c.ignoreCancel {
			<-c.release
		} else {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-c.release:
			}
		}
	}
	if callErr != nil {
		return nil, callErr
	}
	return &protocol.Response{Content: []protocol.Block{protocol.TextBlock(c.reply)}}, nil
}

func flowSessionLLMDefinition(flowID string) *flow.Definition {
	return &flow.Definition{
		FlowID:        flowID,
		Version:       "1",
		Status:        FlowStatusDraft,
		ExecutionMode: flow.ExecutionModeSession,
		SessionWorkflow: &flow.SessionWorkflowSpec{Triggers: []flow.SessionTrigger{
			{EventType: "turn.update", EntryNode: "seed"},
		}},
		Inputs: []flow.VarDef{{Name: "locale", Type: "string", Required: true}},
		Nodes: []flow.Node{
			{
				ID:   "seed",
				Kind: flow.KindFunction,
				Function: &flow.FunctionSpec{
					Runtime: flow.FunctionRuntimeJS,
					Source: `function handle(ctx, event) {
						const count = Number(event.payload.count);
						return {count: count, session_state: {count: count, dialog_id: event.payload.dialog_id}};
					}`,
				},
				Outputs: []flow.VarDef{
					{Name: "count", Type: "number"},
					{Name: "session_state", Type: "object"},
				},
			},
			{
				ID:      "answer",
				Kind:    flow.KindLLM,
				Prompt:  "Answer {{event.payload.text}} in {{inputs.locale}}. dialog={{session.state.dialog_id}} count={{nodes.seed.outputs.count}}",
				Retry:   &flow.RetryPolicy{MaxAttempts: 2, InitialIntervalMS: 1, MaxIntervalMS: 1, RetryOn: []string{"provider_error"}},
				Outputs: []flow.VarDef{{Name: "answer", Type: "string"}, {Name: "session_state", Type: "object"}},
			},
			{
				ID:   "finish",
				Kind: flow.KindFunction,
				Function: &flow.FunctionSpec{
					Runtime: flow.FunctionRuntimeJS,
					Source: `function handle(ctx) {
						return {session_state: {
							count: ctx.session.state.count,
							dialog_id: ctx.session.state.dialog_id,
							answer: ctx.outputs.answer.answer
						}};
					}`,
				},
				Outputs: []flow.VarDef{{Name: "session_state", Type: "object"}},
			},
		},
		Edges: []flow.Edge{
			{ID: "seed-answer", From: "seed", To: "answer", EdgeType: flow.EdgeDataDependency},
			{ID: "answer-finish", From: "answer", To: "finish", EdgeType: flow.EdgeDataDependency},
		},
	}
}

func flowSessionRuntimeDefinition(flowID string) *flow.Definition {
	return &flow.Definition{
		FlowID:        flowID,
		Version:       "1",
		Status:        FlowStatusDraft,
		ExecutionMode: flow.ExecutionModeSession,
		SessionWorkflow: &flow.SessionWorkflowSpec{Triggers: []flow.SessionTrigger{
			{EventType: "turn.update", EntryNode: "bump"},
			{EventType: "state.snapshot", EntryNode: "bump", Delivery: flow.SessionDeliveryLatestWins},
		}},
		Nodes: []flow.Node{
			{
				ID:   "bump",
				Kind: flow.KindFunction,
				Function: &flow.FunctionSpec{
					Runtime: flow.FunctionRuntimeJS,
					Source: `function handle(ctx, event) {
						const counter = Number(ctx.session.state.counter || 0) + Number(event.payload.delta || 0);
						return {counter: counter, session_state: {counter: counter}};
					}`,
				},
				Outputs: []flow.VarDef{
					{Name: "counter", Type: "number"},
					{Name: "session_state", Type: "object"},
				},
			},
			{
				ID:   "derive",
				Kind: flow.KindFunction,
				Function: &flow.FunctionSpec{
					Runtime: flow.FunctionRuntimeJS,
					Source: `function handle(ctx, event) {
						const counter = ctx.outputs.bump.counter;
						return {derived: counter * 2, session_state: {counter: counter, derived: counter * 2}};
					}`,
				},
				Outputs: []flow.VarDef{
					{Name: "derived", Type: "number"},
					{Name: "session_state", Type: "object"},
				},
			},
		},
		Edges: []flow.Edge{
			{ID: "bump_to_derive", From: "bump", To: "derive", EdgeType: flow.EdgeDataDependency},
		},
	}
}

func flowSessionAgentDefinition(flowID string, delivery string) *flow.Definition {
	return &flow.Definition{
		FlowID:        flowID,
		Version:       "1",
		Status:        FlowStatusDraft,
		ExecutionMode: flow.ExecutionModeSession,
		SessionWorkflow: &flow.SessionWorkflowSpec{Triggers: []flow.SessionTrigger{
			{EventType: "turn.update", EntryNode: "agent", Delivery: delivery},
		}},
		Inputs: []flow.VarDef{{Name: "locale", Type: "string", Required: true}},
		Nodes: []flow.Node{{
			ID:        "agent",
			Kind:      flow.KindStep,
			Title:     "Handle turn",
			AgentType: "Explore",
			Prompt:    "Handle {{event.payload.text}} in {{inputs.locale}}",
			Outputs: []flow.VarDef{
				{Name: "answer", Type: "string", Required: true},
				{Name: "session_state", Type: "object", Required: true},
			},
		}},
	}
}

func flowSessionBranchDefinition(flowID string) *flow.Definition {
	return &flow.Definition{
		FlowID:        flowID,
		Version:       "1",
		Status:        FlowStatusDraft,
		ExecutionMode: flow.ExecutionModeSession,
		SessionWorkflow: &flow.SessionWorkflowSpec{Triggers: []flow.SessionTrigger{
			{EventType: "turn.update", EntryNode: "classify"},
		}},
		Nodes: []flow.Node{
			{
				ID:   "classify",
				Kind: flow.KindFunction,
				Function: &flow.FunctionSpec{
					Runtime: flow.FunctionRuntimeJS,
					Source: `function handle(ctx, event) {
						return {risk: event.payload.risk};
					}`,
				},
				Outputs: []flow.VarDef{{Name: "risk", Type: "string"}},
			},
			{
				ID:   "route",
				Kind: flow.KindBranch,
				Branch: &flow.BranchSpec{
					Cases: []flow.BranchCase{
						{
							Name: "safe",
							To:   "safe",
							Condition: flow.Condition{Output: &flow.FieldCompare{
								Path: "risk", Op: "eq", Value: "low",
							}},
						},
						{
							Name: "safe_late",
							To:   "safe_late",
							Condition: flow.Condition{Output: &flow.FieldCompare{
								Path: "risk", Op: "eq", Value: "low",
							}},
						},
					},
					DefaultTo: "fallback",
				},
			},
			{
				ID:   "safe",
				Kind: flow.KindFunction,
				Function: &flow.FunctionSpec{
					Runtime: flow.FunctionRuntimeJS,
					Source: `function handle(ctx) {
						return {handled: "safe", session_state: {route: "safe"}};
					}`,
				},
				Outputs: []flow.VarDef{
					{Name: "handled", Type: "string"},
					{Name: "session_state", Type: "object"},
				},
			},
			{
				ID:   "safe_late",
				Kind: flow.KindFunction,
				Function: &flow.FunctionSpec{
					Runtime: flow.FunctionRuntimeJS,
					Source: `function handle(ctx) {
						return {handled: "safe_late", session_state: {route: "safe_late"}};
					}`,
				},
				Outputs: []flow.VarDef{
					{Name: "handled", Type: "string"},
					{Name: "session_state", Type: "object"},
				},
			},
			{
				ID:   "fallback",
				Kind: flow.KindFunction,
				Function: &flow.FunctionSpec{
					Runtime: flow.FunctionRuntimeJS,
					Source: `function handle(ctx) {
						return {handled: "fallback", session_state: {route: "fallback"}};
					}`,
				},
				Outputs: []flow.VarDef{
					{Name: "handled", Type: "string"},
					{Name: "session_state", Type: "object"},
				},
			},
			{
				ID:   "safe_finish",
				Kind: flow.KindFunction,
				Function: &flow.FunctionSpec{
					Runtime: flow.FunctionRuntimeJS,
					Source: `function handle(ctx) {
						return {
							finalized: "safe",
							session_state: Object.assign({}, ctx.session.state, {after_route: "safe_finish"})
						};
					}`,
				},
				Outputs: []flow.VarDef{
					{Name: "finalized", Type: "string"},
					{Name: "session_state", Type: "object"},
				},
			},
			{
				ID:   "fallback_finish",
				Kind: flow.KindFunction,
				Function: &flow.FunctionSpec{
					Runtime: flow.FunctionRuntimeJS,
					Source: `function handle(ctx) {
						return {
							finalized: "fallback",
							session_state: Object.assign({}, ctx.session.state, {after_route: "fallback_finish"})
						};
					}`,
				},
				Outputs: []flow.VarDef{
					{Name: "finalized", Type: "string"},
					{Name: "session_state", Type: "object"},
				},
			},
		},
		Edges: []flow.Edge{
			{
				ID:       "classify-route",
				From:     "classify",
				To:       "route",
				EdgeType: flow.EdgeDataDependency,
			},
			{
				ID:       "safe-safe-finish",
				From:     "safe",
				To:       "safe_finish",
				EdgeType: flow.EdgeDataDependency,
			},
			{
				ID:       "fallback-fallback-finish",
				From:     "fallback",
				To:       "fallback_finish",
				EdgeType: flow.EdgeDataDependency,
			},
		},
	}
}

func TestFlowSessionAgentNodeRunsDurableSubagentAndMapsDeclaredOutputs(t *testing.T) {
	cfg := testFlowConfig(t.TempDir())
	a := New(cfg)
	caller := &sessionFlowLLMTestCaller{reply: `{"answer":"accepted","session_state":{"count":3,"answer":"accepted"}}`}
	a.client = caller
	def := flowSessionAgentDefinition("fl_session_agent_runtime", flow.SessionDeliveryDurable)
	if _, err := a.CreateFlow(FlowCreateArgs{Def: def}); err != nil {
		t.Fatalf("create session flow: %v", err)
	}
	session, err := a.CreateFlowSession(def.FlowID, def.Version, map[string]any{"locale": "en"})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := a.AppendFlowSessionEvent(def.FlowID, session.SessionID, FlowSessionEventInput{
		Source: "voice-adapter", Type: "turn.update",
		Payload: json.RawMessage(`{"text":"hello","count":3}`),
	}); err != nil {
		t.Fatalf("append event: %v", err)
	}

	result, err := a.AdvanceFlowSession(t.Context(), def.FlowID, session.SessionID, 64)
	if err != nil || result.Processed != 1 || result.Pending {
		t.Fatalf("advance session: result=%+v err=%v", result, err)
	}
	view, err := a.GetFlowSession(def.FlowID, session.SessionID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if view.State["answer"] != "accepted" || view.State["count"] != float64(3) ||
		len(view.LastProcessedNodes) != 1 || view.LastProcessedNodes[0] != "agent" {
		t.Fatalf("Agent outputs were not committed to session state: %+v", view)
	}
	if view.LastExecution == nil || view.LastExecution.Delivery != flow.SessionDeliveryDurable ||
		view.LastExecution.InputSequence != 1 || view.LastExecution.Status != "completed" ||
		view.LastExecution.OutputCount != 1 || len(view.LastExecution.Nodes) != 1 ||
		view.LastExecution.Nodes[0].NodeID != "agent" ||
		view.LastExecution.Nodes[0].Status != "completed" ||
		view.LastExecution.CompletedAt.Before(view.LastExecution.StartedAt) ||
		view.LastExecution.DurationMS < 0 || view.LastExecution.Nodes[0].DurationMS < 0 {
		t.Fatalf("expected durable node execution summary with timing: %+v", view.LastExecution)
	}
	events, err := a.FlowSessionEvents(def.FlowID, session.SessionID, 0, 10)
	if err != nil {
		t.Fatalf("read FlowSession output events: %v", err)
	}
	if len(events) != 2 || events[1].Type != FlowSessionOutputEventType || events[1].Source != "godex" {
		t.Fatalf("expected one durable session.output event after the input event, got %+v", events)
	}
	var output struct {
		OutputID      string         `json:"output_id"`
		InputSequence uint64         `json:"input_sequence"`
		NodeID        string         `json:"node_id"`
		Outputs       map[string]any `json:"outputs"`
	}
	if err := json.Unmarshal(events[1].Payload, &output); err != nil {
		t.Fatalf("decode session.output payload: %v", err)
	}
	if output.OutputID == "" || output.InputSequence != 1 || output.NodeID != "agent" ||
		output.Outputs["answer"] != "accepted" {
		t.Fatalf("unexpected session.output payload: %+v", output)
	}
	if _, exists := output.Outputs["session_state"]; exists {
		t.Fatalf("internal session_state leaked into external output: %+v", output.Outputs)
	}

	jobs := a.subagentJobs.List()
	if len(jobs) != 1 || jobs[0].Status != subagentStatusCompleted || jobs[0].IdempotencyKey == "" {
		t.Fatalf("expected one completed idempotent durable Agent job, got %+v", jobs)
	}
	if jobs[0].SessionID != session.SessionID {
		t.Fatalf("Agent job is not scoped to its FlowSession: job=%+v session=%q", jobs[0], session.SessionID)
	}
	if !strings.Contains(jobs[0].Prompt, "Handle hello in en") ||
		!strings.Contains(jobs[0].Prompt, "matching these declared outputs") {
		t.Fatalf("Agent prompt did not render event/input context and output contract: %s", jobs[0].Prompt)
	}
}

func TestFlowSessionAgentWorkReportsLiveNodeAndSubagentProgress(t *testing.T) {
	cfg := testFlowConfig(t.TempDir())
	a := New(cfg)
	if err := os.WriteFile(filepath.Join(cfg.WorkspaceDir, "progress_fixture.txt"), []byte("progress-ok"), 0644); err != nil {
		t.Fatalf("write Agent tool fixture: %v", err)
	}
	a.client = &sessionFlowProgressTestCaller{}
	def := flowSessionAgentDefinition("fl_session_agent_progress", flow.SessionDeliveryDurable)
	def.Nodes[0].Prompt = "Use read_file to inspect progress_fixture.txt, then answer {{event.payload.text}}."
	if _, err := a.CreateFlow(FlowCreateArgs{Def: def}); err != nil {
		t.Fatalf("create session flow: %v", err)
	}
	session, err := a.CreateFlowSession(def.FlowID, def.Version, map[string]any{"locale": "en"})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := a.AppendFlowSessionEvent(def.FlowID, session.SessionID, FlowSessionEventInput{
		Source: "voice-adapter", SourceSequence: 1, Type: "turn.update",
		Payload: json.RawMessage(`{"text":"hello"}`),
	}); err != nil {
		t.Fatalf("append event: %v", err)
	}
	work, err := a.PrepareFlowSessionWork(def.FlowID, session.SessionID)
	if err != nil || work == nil {
		t.Fatalf("prepare FlowSession work: work=%v err=%v", work, err)
	}
	progress := make(chan FlowSessionProgressUpdate, 32)
	work.SetProgressHandler(func(update FlowSessionProgressUpdate) {
		progress <- update
	})
	if _, err := a.ExecuteFlowSessionWork(t.Context(), work); err != nil {
		t.Fatalf("execute FlowSession work: %v", err)
	}

	seenAgentNode, seenSubagentReply := false, false
	seenToolStart, seenToolFinish := false, false
	for len(progress) > 0 {
		update := <-progress
		if update.NodeID == "agent" && update.Phase == "agent_started" {
			seenAgentNode = true
		}
		if update.NodeID == "agent" && update.Phase == "assistant_message" {
			seenSubagentReply = true
		}
		if update.NodeID == "agent" && update.Phase == "tool_started" && update.ToolName == "read_file" {
			seenToolStart = true
		}
		if update.NodeID == "agent" && update.Phase == "tool_finished" && update.ToolName == "read_file" {
			seenToolFinish = true
		}
	}
	if !seenAgentNode || !seenSubagentReply || !seenToolStart || !seenToolFinish {
		t.Fatalf("missing live FlowSession progress: agent_node=%t subagent_reply=%t tool_start=%t tool_finish=%t",
			seenAgentNode, seenSubagentReply, seenToolStart, seenToolFinish)
	}
}

type sessionFlowProgressTestCaller struct {
	mu    sync.Mutex
	calls int
}

func (c *sessionFlowProgressTestCaller) Call(_ context.Context, _ protocol.Request) (*protocol.Response, error) {
	c.mu.Lock()
	call := c.calls
	c.calls++
	c.mu.Unlock()
	if call == 0 {
		return &protocol.Response{
			Content:    []protocol.Block{protocol.ToolUseBlock("progress-read", "read_file", map[string]interface{}{"path": "progress_fixture.txt"})},
			StopReason: "tool_use",
		}, nil
	}
	return &protocol.Response{
		Content:    []protocol.Block{protocol.TextBlock(`{"answer":"progress-ok","session_state":{"seen":true}}`)},
		StopReason: "end_turn",
	}, nil
}

func TestFlowSessionBranchExecutesOnlySelectedRouteChainAndRecordsChoice(t *testing.T) {
	for _, test := range []struct {
		name            string
		risk            string
		wantRoute       string
		wantHandled     string
		wantTarget      string
		wantFollowup    string
		unwantedTargets []string
	}{
		{
			name:            "first matching case wins",
			risk:            "low",
			wantRoute:       "safe",
			wantHandled:     "safe",
			wantTarget:      "safe",
			wantFollowup:    "safe_finish",
			unwantedTargets: []string{"safe_late", "fallback", "fallback_finish"},
		},
		{
			name:            "default case",
			risk:            "high",
			wantRoute:       "default",
			wantHandled:     "fallback",
			wantTarget:      "fallback",
			wantFollowup:    "fallback_finish",
			unwantedTargets: []string{"safe", "safe_late", "safe_finish"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := testFlowConfig(t.TempDir())
			a := New(cfg)
			def := flowSessionBranchDefinition("fl_session_branch_" + strings.ReplaceAll(test.name, " ", "_"))
			if _, err := a.CreateFlow(FlowCreateArgs{Def: def}); err != nil {
				t.Fatalf("create session Flow: %v", err)
			}
			session, err := a.CreateFlowSession(def.FlowID, def.Version, nil)
			if err != nil {
				t.Fatalf("create FlowSession: %v", err)
			}
			if _, err := a.AppendFlowSessionEvent(def.FlowID, session.SessionID, FlowSessionEventInput{
				Source: "voice-adapter", Type: "turn.update",
				Payload: json.RawMessage(`{"risk":"` + test.risk + `"}`),
			}); err != nil {
				t.Fatalf("append event: %v", err)
			}
			result, err := a.AdvanceFlowSession(t.Context(), def.FlowID, session.SessionID, 64)
			if err != nil || result.Processed != 1 || result.Pending {
				t.Fatalf("advance FlowSession: result=%+v err=%v", result, err)
			}
			view, err := a.GetFlowSession(def.FlowID, session.SessionID)
			if err != nil {
				t.Fatalf("get FlowSession: %v", err)
			}
			if view.State["route"] != test.wantHandled || view.LastBranchRoutes["route"] != test.wantRoute {
				t.Fatalf("branch result not committed: state=%+v routes=%+v", view.State, view.LastBranchRoutes)
			}
			if view.State["after_route"] != test.wantFollowup {
				t.Fatalf("selected branch downstream did not update session state: state=%+v", view.State)
			}
			events, err := a.FlowSessionEvents(def.FlowID, session.SessionID, 0, 10)
			if err != nil {
				t.Fatalf("read selected route output: %v", err)
			}
			if len(events) != 2 || events[1].Type != FlowSessionOutputEventType {
				t.Fatalf("expected one session.output from the selected route chain, got %+v", events)
			}
			var output struct {
				NodeID  string         `json:"node_id"`
				Outputs map[string]any `json:"outputs"`
			}
			if err := json.Unmarshal(events[1].Payload, &output); err != nil {
				t.Fatalf("decode selected route output: %v", err)
			}
			if output.NodeID != test.wantFollowup || output.Outputs["finalized"] != test.wantHandled {
				t.Fatalf("expected output from selected terminal route node %q, got %+v", test.wantFollowup, output)
			}
			if !containsString(view.LastProcessedNodes, "classify") ||
				!containsString(view.LastProcessedNodes, "route") ||
				!containsString(view.LastProcessedNodes, test.wantTarget) ||
				!containsString(view.LastProcessedNodes, test.wantFollowup) {
				t.Fatalf("unexpected executed session nodes: %v", view.LastProcessedNodes)
			}
			for _, unwantedTarget := range test.unwantedTargets {
				if containsString(view.LastProcessedNodes, unwantedTarget) {
					t.Fatalf("unselected branch target %q was executed: %v", unwantedTarget, view.LastProcessedNodes)
				}
			}
		})
	}
}

func TestFlowSessionBranchMergesSelectedRoute(t *testing.T) {
	for _, test := range []struct {
		name       string
		risk       string
		wantRoute  string
		wantTarget string
		wantFinish string
	}{
		{name: "case route", risk: "low", wantRoute: "safe", wantTarget: "safe", wantFinish: "safe_finish"},
		{name: "default route", risk: "high", wantRoute: "default", wantTarget: "fallback", wantFinish: "fallback_finish"},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := testFlowConfig(t.TempDir())
			a := New(cfg)
			def := flowSessionBranchDefinition("fl_session_branch_merge_" + strings.ReplaceAll(test.name, " ", "_"))
			def.Nodes = append(def.Nodes, flow.Node{
				ID:   "merge",
				Kind: flow.KindFunction,
				Function: &flow.FunctionSpec{
					Runtime: flow.FunctionRuntimeJS,
					Source: `function handle(ctx) {
						return {
							merged_from: ctx.session.state.after_route,
							session_state: Object.assign({}, ctx.session.state, {after_route: "merged"})
						};
					}`,
				},
				Outputs: []flow.VarDef{
					{Name: "merged_from", Type: "string"},
					{Name: "session_state", Type: "object"},
				},
			})
			def.Edges = append(def.Edges,
				flow.Edge{ID: "safe-finish-merge", From: "safe_finish", To: "merge", EdgeType: flow.EdgeDataDependency},
				flow.Edge{ID: "fallback-finish-merge", From: "fallback_finish", To: "merge", EdgeType: flow.EdgeDataDependency},
			)
			if _, err := a.CreateFlow(FlowCreateArgs{Def: def}); err != nil {
				t.Fatalf("create session Flow: %v", err)
			}
			session, err := a.CreateFlowSession(def.FlowID, def.Version, nil)
			if err != nil {
				t.Fatalf("create FlowSession: %v", err)
			}
			if _, err := a.AppendFlowSessionEvent(def.FlowID, session.SessionID, FlowSessionEventInput{
				Source: "game-adapter", Type: "turn.update",
				Payload: json.RawMessage(`{"risk":"` + test.risk + `"}`),
			}); err != nil {
				t.Fatalf("append event: %v", err)
			}
			result, err := a.AdvanceFlowSession(t.Context(), def.FlowID, session.SessionID, 64)
			if err != nil || result.Processed != 1 || result.Pending {
				t.Fatalf("advance FlowSession: result=%+v err=%v", result, err)
			}
			view, err := a.GetFlowSession(def.FlowID, session.SessionID)
			if err != nil {
				t.Fatalf("get FlowSession: %v", err)
			}
			if view.LastBranchRoutes["route"] != test.wantRoute {
				t.Fatalf("wrong branch route selected: %+v", view.LastBranchRoutes)
			}
			if view.State["after_route"] != "merged" {
				t.Fatalf("shared join did not update state: %+v", view.State)
			}
			if !containsString(view.LastProcessedNodes, test.wantTarget) ||
				!containsString(view.LastProcessedNodes, test.wantFinish) ||
				!containsString(view.LastProcessedNodes, "merge") {
				t.Fatalf("selected route did not reach the merge: %v", view.LastProcessedNodes)
			}
			mergeCount := 0
			for _, nodeID := range view.LastProcessedNodes {
				if nodeID == "merge" {
					mergeCount++
				}
			}
			if mergeCount != 1 {
				t.Fatalf("shared merge must run exactly once, got %d executions: %v", mergeCount, view.LastProcessedNodes)
			}
			unselected := "fallback"
			if test.wantTarget == "fallback" {
				unselected = "safe"
			}
			unselectedFinish := unselected + "_finish"
			if containsString(view.LastProcessedNodes, unselected) || containsString(view.LastProcessedNodes, unselectedFinish) {
				t.Fatalf("unselected route ran before the merge: %v", view.LastProcessedNodes)
			}
			events, err := a.FlowSessionEvents(def.FlowID, session.SessionID, 0, 10)
			if err != nil {
				t.Fatalf("read session output: %v", err)
			}
			if len(events) != 2 || events[1].Type != FlowSessionOutputEventType {
				t.Fatalf("expected one merged output, got %+v", events)
			}
			var output struct {
				NodeID  string         `json:"node_id"`
				Outputs map[string]any `json:"outputs"`
			}
			if err := json.Unmarshal(events[1].Payload, &output); err != nil {
				t.Fatalf("decode merged output: %v", err)
			}
			if output.NodeID != "merge" || output.Outputs["merged_from"] != test.wantFinish {
				t.Fatalf("merge did not consume selected route state: %+v", output)
			}
		})
	}
}

func TestFlowSessionBranchRejectsNestedBranch(t *testing.T) {
	def := flowSessionBranchDefinition("fl_session_branch_nested")
	def.Nodes = append(def.Nodes,
		flow.Node{
			ID:   "nested_route",
			Kind: flow.KindBranch,
			Branch: &flow.BranchSpec{
				Cases: []flow.BranchCase{{
					Name: "nested",
					To:   "nested_target",
					Condition: flow.Condition{Output: &flow.FieldCompare{
						Path: "handled", Op: "eq", Value: "safe",
					}},
				}},
				DefaultTo: "nested_target",
			},
		},
		flow.Node{
			ID:   "nested_target",
			Kind: flow.KindFunction,
			Function: &flow.FunctionSpec{
				Runtime: flow.FunctionRuntimeJS,
				Source:  `function handle() { return {done: true}; }`,
			},
			Outputs: []flow.VarDef{{Name: "done", Type: "boolean"}},
		},
	)
	def.Edges = append(def.Edges, flow.Edge{
		ID: "safe-nested-route", From: "safe", To: "nested_route", EdgeType: flow.EdgeDataDependency,
	})

	_, err := flow.Compile(def)
	if err == nil || !strings.Contains(err.Error(), "nested branch node") {
		t.Fatalf("expected nested branch rejection, got %v", err)
	}
}

func TestFlowSessionBranchCanDispatchSelectedDurableAgent(t *testing.T) {
	cfg := testFlowConfig(t.TempDir())
	a := New(cfg)
	a.client = &sessionFlowLLMTestCaller{
		reply: `{"answer":"accepted","session_state":{"route":"agent","answer":"accepted"}}`,
	}
	def := flowSessionBranchDefinition("fl_session_branch_agent_route")
	def.Nodes[2].Kind = flow.KindStep
	def.Nodes[2].Function = nil
	def.Nodes[2].AgentType = "Explore"
	def.Nodes[2].Prompt = "Handle {{event.payload.text}}"
	def.Nodes[2].Outputs = []flow.VarDef{
		{Name: "answer", Type: "string", Required: true},
		{Name: "session_state", Type: "object", Required: true},
	}
	if _, err := a.CreateFlow(FlowCreateArgs{Def: def}); err != nil {
		t.Fatalf("create session Flow: %v", err)
	}
	session, err := a.CreateFlowSession(def.FlowID, def.Version, nil)
	if err != nil {
		t.Fatalf("create FlowSession: %v", err)
	}
	if _, err := a.AppendFlowSessionEvent(def.FlowID, session.SessionID, FlowSessionEventInput{
		Source: "voice-adapter", Type: "turn.update",
		Payload: json.RawMessage(`{"risk":"low","text":"hello"}`),
	}); err != nil {
		t.Fatalf("append event: %v", err)
	}
	result, err := a.AdvanceFlowSession(t.Context(), def.FlowID, session.SessionID, 64)
	if err != nil || result.Processed != 1 || result.Pending {
		t.Fatalf("advance FlowSession: result=%+v err=%v", result, err)
	}

	view, err := a.GetFlowSession(def.FlowID, session.SessionID)
	if err != nil {
		t.Fatalf("get FlowSession: %v", err)
	}
	if view.LastBranchRoutes["route"] != "safe" ||
		view.State["answer"] != "accepted" ||
		view.State["route"] != "agent" {
		t.Fatalf("selected Agent result was not committed: state=%+v routes=%+v", view.State, view.LastBranchRoutes)
	}
	for _, expected := range []string{"classify", "route", "safe"} {
		if !containsString(view.LastProcessedNodes, expected) {
			t.Fatalf("selected route execution is missing %q: %v", expected, view.LastProcessedNodes)
		}
	}
	for _, unselected := range []string{"safe_late", "fallback"} {
		if containsString(view.LastProcessedNodes, unselected) {
			t.Fatalf("unselected route %q was executed: %v", unselected, view.LastProcessedNodes)
		}
	}
	jobs := a.subagentJobs.List()
	if len(jobs) != 1 || jobs[0].Status != subagentStatusCompleted ||
		jobs[0].SessionID != session.SessionID ||
		!strings.Contains(jobs[0].Prompt, "Handle hello") {
		t.Fatalf("expected one completed durable Agent job for the selected route, got %+v", jobs)
	}
}

func TestFlowSessionAgentReplayReusesCompletedJobBeforeCheckpoint(t *testing.T) {
	cfg := testFlowConfig(t.TempDir())
	a1 := New(cfg)
	a1.client = &sessionFlowLLMTestCaller{reply: `{"answer":"persisted","session_state":{"answer":"persisted"}}`}
	def := flowSessionAgentDefinition("fl_session_agent_replay", flow.SessionDeliveryDurable)
	if _, err := a1.CreateFlow(FlowCreateArgs{Def: def}); err != nil {
		t.Fatalf("create session flow: %v", err)
	}
	session, err := a1.CreateFlowSession(def.FlowID, def.Version, map[string]any{"locale": "en"})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := a1.AppendFlowSessionEvent(def.FlowID, session.SessionID, FlowSessionEventInput{
		Source: "voice-adapter", Type: "turn.update", Payload: json.RawMessage(`{"text":"hello"}`),
	}); err != nil {
		t.Fatalf("append event: %v", err)
	}
	work, err := a1.PrepareFlowSessionWork(def.FlowID, session.SessionID)
	if err != nil || work == nil {
		t.Fatalf("prepare first attempt: work=%v err=%v", work, err)
	}
	if _, err := a1.ExecuteFlowSessionWork(t.Context(), work); err != nil {
		t.Fatalf("execute first attempt: %v", err)
	}
	firstJobs := a1.subagentJobs.List()
	if len(firstJobs) != 1 || firstJobs[0].Status != subagentStatusCompleted {
		t.Fatalf("expected first durable Agent job to complete, got %+v", firstJobs)
	}

	// Recreate the Agent and replay the still-uncheckpointed event. The same
	// idempotency key must return the persisted result without another model call.
	a2 := New(cfg)
	replayCaller := &sessionFlowLLMTestCaller{reply: `{"answer":"should not run","session_state":{}}`}
	a2.client = replayCaller
	replayedWork, err := a2.PrepareFlowSessionWork(def.FlowID, session.SessionID)
	if err != nil || replayedWork == nil {
		t.Fatalf("prepare replay: work=%v err=%v", replayedWork, err)
	}
	result, err := a2.ExecuteFlowSessionWork(t.Context(), replayedWork)
	if err != nil || result.Error != "" || result.State["answer"] != "persisted" {
		t.Fatalf("replay did not reuse completed job output: result=%+v err=%v", result, err)
	}
	if got := len(replayCaller.requests); got != 0 {
		t.Fatalf("replay unexpectedly started another model request: count=%d", got)
	}
	replayedJobs := a2.subagentJobs.List()
	if len(replayedJobs) != 1 || replayedJobs[0].ID != firstJobs[0].ID {
		t.Fatalf("replay created a duplicate durable job: first=%+v replay=%+v", firstJobs, replayedJobs)
	}
	if err := a2.CommitFlowSessionWork(replayedWork, result); err != nil {
		t.Fatalf("commit replayed work: %v", err)
	}
}

func TestFlowSessionAgentPauseFencesOldGenerationAndReplaysWithNewJob(t *testing.T) {
	cfg := testFlowConfig(t.TempDir())
	a1 := New(cfg)
	oldCaller := &sessionFlowLLMTestCaller{
		reply:   `{"answer":"stale","session_state":{"answer":"stale"}}`,
		entered: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	a1.client = oldCaller
	def := flowSessionAgentDefinition("fl_session_agent_fence", flow.SessionDeliveryDurable)
	if _, err := a1.CreateFlow(FlowCreateArgs{Def: def}); err != nil {
		t.Fatalf("create session flow: %v", err)
	}
	session, err := a1.CreateFlowSession(def.FlowID, def.Version, map[string]any{"locale": "en"})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := a1.AppendFlowSessionEvent(def.FlowID, session.SessionID, FlowSessionEventInput{
		Source: "voice-adapter", Type: "turn.update", Payload: json.RawMessage(`{"text":"hello"}`),
	}); err != nil {
		t.Fatalf("append event: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := a1.AdvanceFlowSession(ctx, def.FlowID, session.SessionID, 64)
		done <- err
	}()
	select {
	case <-oldCaller.entered:
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("session Agent subagent did not start")
	}
	paused, err := a1.PauseFlowSession(def.FlowID, session.SessionID)
	if err != nil {
		cancel()
		t.Fatalf("pause session: %v", err)
	}
	cancel() // Mirrors the backend scheduler canceling in-flight work on pause.
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected canceled session work, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("session Agent worker did not stop after pause")
	}
	oldJobs := a1.subagentJobs.List()
	if len(oldJobs) != 1 || oldJobs[0].Status != subagentStatusCanceled {
		t.Fatalf("expected old-generation Agent job to be canceled, got %+v", oldJobs)
	}

	a2 := New(cfg)
	a2.client = &sessionFlowLLMTestCaller{reply: `{"answer":"fresh","session_state":{"answer":"fresh"}}`}
	if _, err := a2.ResumeFlowSession(def.FlowID, session.SessionID); err != nil {
		t.Fatalf("resume session: %v", err)
	}
	result, err := a2.AdvanceFlowSession(t.Context(), def.FlowID, session.SessionID, 64)
	if err != nil || result.Pending {
		t.Fatalf("replay after resume: result=%+v err=%v", result, err)
	}
	view, err := a2.GetFlowSession(def.FlowID, session.SessionID)
	if err != nil {
		t.Fatalf("get resumed session: %v", err)
	}
	if view.State["answer"] != "fresh" || view.ExecutionGeneration <= paused.ExecutionGeneration {
		t.Fatalf("session accepted stale result or failed to advance: %+v", view)
	}
	newJobs := a2.subagentJobs.List()
	if len(newJobs) != 2 || newJobs[0].IdempotencyKey == newJobs[1].IdempotencyKey {
		t.Fatalf("resume must isolate generations with a new durable job key: %+v", newJobs)
	}
}

func TestFlowSessionAgentPermissionApprovalFailsWithoutStallingEvent(t *testing.T) {
	cfg := testFlowConfig(t.TempDir())
	a := New(cfg)
	a.client = &sequenceCaller{responses: []protocol.Response{{
		Content: []protocol.Block{
			protocol.ToolUseBlock("tool-shell", "bash", map[string]interface{}{"command": "command -v sh"}),
		},
	}}}
	def := flowSessionAgentDefinition("fl_session_agent_approval", flow.SessionDeliveryDurable)
	def.Nodes[0].AgentType = "general-purpose"
	def.Nodes[0].WriteScope = []string{"notes"}
	if _, err := a.CreateFlow(FlowCreateArgs{Def: def}); err != nil {
		t.Fatalf("create session flow: %v", err)
	}
	session, err := a.CreateFlowSession(def.FlowID, def.Version, map[string]any{"locale": "en"})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := a.AppendFlowSessionEvent(def.FlowID, session.SessionID, FlowSessionEventInput{
		Source: "voice-adapter", Type: "turn.update", Payload: json.RawMessage(`{"text":"inspect shell"}`),
	}); err != nil {
		t.Fatalf("append event: %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	result, err := a.AdvanceFlowSession(ctx, def.FlowID, session.SessionID, 64)
	if err != nil || result.Processed != 1 || result.Pending {
		t.Fatalf("permission request stalled durable event: result=%+v err=%v", result, err)
	}
	view, err := a.GetFlowSession(def.FlowID, session.SessionID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if view.LastError == "" || !strings.Contains(view.LastError, "FlowSession cannot resume") {
		t.Fatalf("expected explicit unsupported-approval diagnostic, got %+v", view)
	}
	if view.LastExecution == nil || view.LastExecution.Status != "failed" ||
		len(view.LastExecution.Nodes) != 1 || view.LastExecution.Nodes[0].NodeID != "agent" ||
		view.LastExecution.Nodes[0].Status != "failed" {
		t.Fatalf("failed Agent node status was not exposed: %+v", view.LastExecution)
	}
	jobs := a.subagentJobs.List()
	if len(jobs) != 1 || jobs[0].Status != subagentStatusCanceled {
		t.Fatalf("pending-approval subagent should be canceled instead of left waiting: %+v", jobs)
	}
	if pending := a.PendingPermissions(session.SessionID); len(pending) != 0 {
		t.Fatalf("canceled FlowSession Agent left pending permission requests: %+v", pending)
	}
}

func TestFlowSessionLLMNodeExecutesInWorkerSnapshotAndFeedsDownstreamState(t *testing.T) {
	cfg := testFlowConfig(t.TempDir())
	a := New(cfg)
	caller := &sessionFlowLLMTestCaller{reply: `{"answer":"accepted","session_state":{"count":7,"dialog_id":"d-42","answer":"accepted"}}`}
	a.client = caller
	def := flowSessionLLMDefinition("fl_session_llm_runtime")
	if _, err := a.CreateFlow(FlowCreateArgs{Def: def}); err != nil {
		t.Fatalf("create session flow: %v", err)
	}
	session, err := a.CreateFlowSession(def.FlowID, def.Version, map[string]any{"locale": "en"})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := a.AppendFlowSessionEvent(def.FlowID, session.SessionID, FlowSessionEventInput{
		Source: "voice-adapter", Type: "turn.update",
		Payload: json.RawMessage(`{"count":7,"dialog_id":"d-42","text":"hello"}`),
	}); err != nil {
		t.Fatalf("append event: %v", err)
	}

	result, err := a.AdvanceFlowSession(t.Context(), def.FlowID, session.SessionID, 64)
	if err != nil || result.Processed != 1 || result.Pending {
		t.Fatalf("advance session: result=%+v err=%v", result, err)
	}
	view, err := a.GetFlowSession(def.FlowID, session.SessionID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if view.State["answer"] != "accepted" || view.State["count"] != float64(7) ||
		view.State["dialog_id"] != "d-42" {
		t.Fatalf("expected LLM output and updated session state, got %#v", view.State)
	}
	if len(view.LastProcessedNodes) != 3 ||
		view.LastProcessedNodes[0] != "seed" ||
		view.LastProcessedNodes[1] != "answer" ||
		view.LastProcessedNodes[2] != "finish" {
		t.Fatalf("expected dependent nodes to execute in order, got %v", view.LastProcessedNodes)
	}

	caller.mu.Lock()
	defer caller.mu.Unlock()
	if len(caller.requests) != 1 {
		t.Fatalf("expected one model request, got %d", len(caller.requests))
	}
	if len(caller.contexts) != 1 {
		t.Fatalf("expected one model request context, got %d", len(caller.contexts))
	}
	request := caller.requests[0]
	if request.Model != cfg.Model || request.MaxTokens != cfg.MaxTokens || request.Stream || len(request.Tools) != 0 {
		t.Fatalf("expected a bounded non-tool LLM request, got %+v", request)
	}
	if len(request.Messages) != 1 || request.Messages[0].Role != protocol.RoleUser {
		t.Fatalf("expected one user prompt, got %+v", request.Messages)
	}
	prompt := protocol.BlocksText(request.Messages[0].Content)
	for _, part := range []string{"hello", "en", "dialog=d-42", "count=7"} {
		if !strings.Contains(prompt, part) {
			t.Fatalf("rendered LLM prompt is missing %q: %s", part, prompt)
		}
	}
	usage, ok := conversation.UsageContextFromContext(caller.contexts[0])
	if !ok || usage.Kind != "flow_llm" || usage.SessionID != session.SessionID {
		t.Fatalf("expected session-scoped flow_llm usage attribution, got %+v (present=%t)", usage, ok)
	}
}

func TestFlowSessionLLMLateResultIsFencedAfterPause(t *testing.T) {
	cfg := testFlowConfig(t.TempDir())
	a := New(cfg)
	release := make(chan struct{})
	caller := &sessionFlowLLMTestCaller{
		reply:        `{"answer":"stale","session_state":{"answer":"stale"}}`,
		entered:      make(chan struct{}, 1),
		release:      release,
		ignoreCancel: true,
	}
	a.client = caller
	def := flowSessionLLMDefinition("fl_session_llm_fence")
	if _, err := a.CreateFlow(FlowCreateArgs{Def: def}); err != nil {
		t.Fatalf("create session flow: %v", err)
	}
	session, err := a.CreateFlowSession(def.FlowID, def.Version, map[string]any{"locale": "en"})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := a.AppendFlowSessionEvent(def.FlowID, session.SessionID, FlowSessionEventInput{
		Source: "voice-adapter", Type: "turn.update",
		Payload: json.RawMessage(`{"count":1,"dialog_id":"d-1","text":"hello"}`),
	}); err != nil {
		t.Fatalf("append event: %v", err)
	}
	work, err := a.PrepareFlowSessionWork(def.FlowID, session.SessionID)
	if err != nil || work == nil {
		t.Fatalf("prepare work: work=%v err=%v", work, err)
	}
	done := make(chan FlowSessionWorkResult, 1)
	go func() {
		result, _ := a.ExecuteFlowSessionWork(context.Background(), work)
		done <- result
	}()
	select {
	case <-caller.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("LLM caller did not start")
	}
	paused, err := a.PauseFlowSession(def.FlowID, session.SessionID)
	if err != nil {
		t.Fatalf("pause session: %v", err)
	}
	close(release)
	var result FlowSessionWorkResult
	select {
	case result = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("LLM worker did not return")
	}
	if err := a.CommitFlowSessionWork(work, result); !errors.Is(err, ErrFlowSessionStaleWork) {
		t.Fatalf("expected late LLM result to be fenced, got %v", err)
	}
	view, err := a.GetFlowSession(def.FlowID, session.SessionID)
	if err != nil {
		t.Fatalf("get paused session: %v", err)
	}
	if view.Status != "paused" || view.ExecutionGeneration != paused.ExecutionGeneration ||
		view.ProcessedSequence != 0 || view.State["answer"] != nil {
		t.Fatalf("late LLM result mutated the paused session: %+v", view)
	}
}

func TestFlowSessionLLMRetryPolicyRetriesTransientProviderFailure(t *testing.T) {
	cfg := testFlowConfig(t.TempDir())
	a := New(cfg)
	caller := &sessionFlowLLMTestCaller{
		reply:    `{"answer":"accepted","session_state":{"answer":"accepted"}}`,
		failures: []error{errors.New("provider returned HTTP 503")},
	}
	a.client = caller
	def := flowSessionLLMDefinition("fl_session_llm_retry")
	if _, err := a.CreateFlow(FlowCreateArgs{Def: def}); err != nil {
		t.Fatalf("create session flow: %v", err)
	}
	session, err := a.CreateFlowSession(def.FlowID, def.Version, map[string]any{"locale": "en"})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := a.AppendFlowSessionEvent(def.FlowID, session.SessionID, FlowSessionEventInput{
		Source: "voice-adapter", Type: "turn.update",
		Payload: json.RawMessage(`{"count":1,"dialog_id":"d-1","text":"hello"}`),
	}); err != nil {
		t.Fatalf("append event: %v", err)
	}
	result, err := a.AdvanceFlowSession(t.Context(), def.FlowID, session.SessionID, 64)
	if err != nil || result.Processed != 1 || result.Pending {
		t.Fatalf("advance session: result=%+v err=%v", result, err)
	}
	caller.mu.Lock()
	defer caller.mu.Unlock()
	if len(caller.requests) != 2 {
		t.Fatalf("expected one retry after transient 503, got %d calls", len(caller.requests))
	}
}

func TestFlowSessionLLMCallCancellationLeavesDurableEventPending(t *testing.T) {
	cfg := testFlowConfig(t.TempDir())
	a := New(cfg)
	caller := &sessionFlowLLMTestCaller{
		reply:   `{"answer":"should not commit","session_state":{"answer":"should not commit"}}`,
		entered: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	a.client = caller
	def := flowSessionLLMDefinition("fl_session_llm_cancel")
	if _, err := a.CreateFlow(FlowCreateArgs{Def: def}); err != nil {
		t.Fatalf("create session flow: %v", err)
	}
	session, err := a.CreateFlowSession(def.FlowID, def.Version, map[string]any{"locale": "en"})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := a.AppendFlowSessionEvent(def.FlowID, session.SessionID, FlowSessionEventInput{
		Source: "voice-adapter", Type: "turn.update",
		Payload: json.RawMessage(`{"count":1,"dialog_id":"d-1","text":"hello"}`),
	}); err != nil {
		t.Fatalf("append event: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := a.AdvanceFlowSession(ctx, def.FlowID, session.SessionID, 64)
		done <- err
	}()
	select {
	case <-caller.entered:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("LLM caller did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected canceled session work, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("LLM call did not observe cancellation")
	}
	view, err := a.GetFlowSession(def.FlowID, session.SessionID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if view.ProcessedSequence != 0 || view.State["answer"] != nil {
		t.Fatalf("canceled LLM work must leave its durable event recoverable: %+v", view)
	}
}

func TestCancelFlowSessionWorkCheckpointsCanceledAndFencesLateResult(t *testing.T) {
	cfg := testFlowConfig(t.TempDir())
	a := New(cfg)
	def := flowSessionLLMDefinition("fl_session_llm_interrupt")
	if _, err := a.CreateFlow(FlowCreateArgs{Def: def}); err != nil {
		t.Fatalf("create session flow: %v", err)
	}
	session, err := a.CreateFlowSession(def.FlowID, def.Version, map[string]any{"locale": "en"})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := a.AppendFlowSessionEvent(def.FlowID, session.SessionID, FlowSessionEventInput{
		Source: "device-1", SourceSequence: 9, Type: "turn.update",
		Payload: json.RawMessage(`{"text":"old turn"}`),
	}); err != nil {
		t.Fatalf("append event: %v", err)
	}
	work, err := a.PrepareFlowSessionWork(def.FlowID, session.SessionID)
	if err != nil || work == nil {
		t.Fatalf("prepare work: work=%v err=%v", work, err)
	}

	canceled, err := a.CancelFlowSessionWork(work)
	if err != nil || !canceled {
		t.Fatalf("cancel work: canceled=%v err=%v", canceled, err)
	}
	view, err := a.GetFlowSession(def.FlowID, session.SessionID)
	if err != nil {
		t.Fatalf("get canceled session: %v", err)
	}
	if view.ProcessedSequence != 1 || view.State["answer"] != nil ||
		view.LastExecution == nil || view.LastExecution.Status != "canceled" ||
		view.LastExecution.InputSequence != 1 || view.LastExecution.OutputCount != 0 {
		t.Fatalf("canceled work should checkpoint without applying state or outputs: %+v", view)
	}

	if err := a.CommitFlowSessionWork(work, FlowSessionWorkResult{
		State: map[string]any{"answer": "late result"},
	}); !errors.Is(err, ErrFlowSessionStaleWork) {
		t.Fatalf("late result should be fenced, got %v", err)
	}
	if retried, err := a.PrepareFlowSessionWork(def.FlowID, session.SessionID); err != nil || retried != nil {
		t.Fatalf("canceled event should not be replayed: work=%v err=%v", retried, err)
	}
	if canceled, err := a.CancelFlowSessionWork(work); err != nil || canceled {
		t.Fatalf("repeat cancellation should be an idempotent no-op: canceled=%v err=%v", canceled, err)
	}
}

func TestAdvanceFlowSessionReplaysDurableEventsThroughFunctionRegion(t *testing.T) {
	cfg := testFlowConfig(t.TempDir())
	a := New(cfg)
	def := flowSessionRuntimeDefinition("fl_session_runtime")
	if _, err := a.CreateFlow(FlowCreateArgs{Def: def}); err != nil {
		t.Fatalf("create session flow: %v", err)
	}
	session, err := a.CreateFlowSession(def.FlowID, def.Version, nil)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	for i, delta := range []int{2, 3} {
		payload, _ := json.Marshal(map[string]any{"delta": delta})
		if _, err := a.AppendFlowSessionEvent(def.FlowID, session.SessionID, FlowSessionEventInput{
			Source:         "voice-adapter",
			SourceSequence: uint64(i + 1),
			Type:           "turn.update",
			Payload:        payload,
		}); err != nil {
			t.Fatalf("append event %d: %v", i, err)
		}
	}

	result, err := a.AdvanceFlowSession(t.Context(), def.FlowID, session.SessionID, 64)
	if err != nil {
		t.Fatalf("advance session: %v", err)
	}
	if result.Processed != 2 || result.Pending || result.Status != "active" {
		t.Fatalf("unexpected advance result: %+v", result)
	}
	view, err := a.GetFlowSession(def.FlowID, session.SessionID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	state, ok := view.State["counter"].(float64)
	if !ok || state != 5 || view.State["derived"] != float64(10) {
		t.Fatalf("expected persisted multi-node state counter=5 derived=10, got %#v", view.State)
	}
	if view.LastSequence != 4 || view.ProcessedSequence != 4 || view.StateVersion <= view.LastSequence {
		t.Fatalf("event and state cursors were not checkpointed independently: %+v", view)
	}
	if len(view.LastProcessedNodes) != 2 || view.LastProcessedNodes[0] != "bump" || view.LastProcessedNodes[1] != "derive" {
		t.Fatalf("expected both region nodes to execute in dependency order, got %v", view.LastProcessedNodes)
	}
}

func TestFlowSessionWorkResultIsFencedAcrossPauseAndResume(t *testing.T) {
	cfg := testFlowConfig(t.TempDir())
	a := New(cfg)
	def := flowSessionRuntimeDefinition("fl_session_work_fence")
	if _, err := a.CreateFlow(FlowCreateArgs{Def: def}); err != nil {
		t.Fatalf("create session flow: %v", err)
	}
	session, err := a.CreateFlowSession(def.FlowID, def.Version, nil)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := a.AppendFlowSessionEvent(def.FlowID, session.SessionID, FlowSessionEventInput{
		Source: "voice-adapter", Type: "turn.update", Payload: json.RawMessage(`{"delta":9}`),
	}); err != nil {
		t.Fatalf("append event: %v", err)
	}
	work, err := a.PrepareFlowSessionWork(def.FlowID, session.SessionID)
	if err != nil || work == nil {
		t.Fatalf("prepare work: work=%v err=%v", work, err)
	}
	result, err := a.ExecuteFlowSessionWork(t.Context(), work)
	if err != nil {
		t.Fatalf("execute prepared work: %v", err)
	}

	paused, err := a.PauseFlowSession(def.FlowID, session.SessionID)
	if err != nil {
		t.Fatalf("pause session: %v", err)
	}
	if paused.ExecutionGeneration <= session.ExecutionGeneration {
		t.Fatalf("pause did not advance execution generation: before=%d after=%d", session.ExecutionGeneration, paused.ExecutionGeneration)
	}
	if err := a.CommitFlowSessionWork(work, result); !errors.Is(err, ErrFlowSessionStaleWork) {
		t.Fatalf("expected paused worker result to be fenced, got %v", err)
	}
	view, err := a.GetFlowSession(def.FlowID, session.SessionID)
	if err != nil {
		t.Fatalf("read paused session: %v", err)
	}
	if view.ProcessedSequence != 0 || view.State["counter"] != nil {
		t.Fatalf("stale work mutated session state: %+v", view)
	}

	if _, err := a.ResumeFlowSession(def.FlowID, session.SessionID); err != nil {
		t.Fatalf("resume session: %v", err)
	}
	advanced, err := a.AdvanceFlowSession(t.Context(), def.FlowID, session.SessionID, 64)
	if err != nil || advanced.Processed != 3 || advanced.Pending {
		t.Fatalf("process events after resume: result=%+v err=%v", advanced, err)
	}
	view, err = a.GetFlowSession(def.FlowID, session.SessionID)
	if err != nil || view.State["counter"] != float64(9) ||
		view.ProcessedSequence != 4 || view.LastSequence != 4 {
		t.Fatalf("resumed event did not commit: view=%+v err=%v", view, err)
	}
}

func TestAdvanceFlowSessionRecoversUnprocessedEventsAndIsolatesSessions(t *testing.T) {
	cfg := testFlowConfig(t.TempDir())
	a1 := New(cfg)
	def := flowSessionRuntimeDefinition("fl_session_recovery")
	if _, err := a1.CreateFlow(FlowCreateArgs{Def: def}); err != nil {
		t.Fatalf("create session flow: %v", err)
	}
	first, err := a1.CreateFlowSession(def.FlowID, def.Version, nil)
	if err != nil {
		t.Fatalf("create first session: %v", err)
	}
	second, err := a1.CreateFlowSession(def.FlowID, def.Version, nil)
	if err != nil {
		t.Fatalf("create second session: %v", err)
	}
	if _, err := a1.AppendFlowSessionEvent(def.FlowID, first.SessionID, FlowSessionEventInput{
		Source: "game-adapter", SourceSequence: 1, Type: "turn.update",
		Payload: json.RawMessage(`{"delta":7}`),
	}); err != nil {
		t.Fatalf("append unprocessed event: %v", err)
	}

	// Recreate the Agent to simulate a process restart before the coordinator
	// handles the durable journal entry.
	a2 := New(cfg)
	result, err := a2.AdvanceFlowSession(t.Context(), def.FlowID, first.SessionID, 64)
	if err != nil || result.Processed != 1 || result.Pending {
		t.Fatalf("recover pending event: result=%+v err=%v", result, err)
	}
	firstView, err := a2.GetFlowSession(def.FlowID, first.SessionID)
	if err != nil {
		t.Fatalf("read recovered session: %v", err)
	}
	secondView, err := a2.GetFlowSession(def.FlowID, second.SessionID)
	if err != nil {
		t.Fatalf("read isolated session: %v", err)
	}
	if firstView.State["counter"] != float64(7) || secondView.State["counter"] != nil {
		t.Fatalf("session state leaked or failed to recover: first=%#v second=%#v", firstView.State, secondView.State)
	}
}

func TestLatestWinsSignalOutputIsCoalescedOutsideEventJournal(t *testing.T) {
	cfg := testFlowConfig(t.TempDir())
	a := New(cfg)
	def := flowSessionRuntimeDefinition("fl_session_signal")
	if _, err := a.CreateFlow(FlowCreateArgs{Def: def}); err != nil {
		t.Fatalf("create session flow: %v", err)
	}
	session, err := a.CreateFlowSession(def.FlowID, def.Version, nil)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := a.AppendFlowSessionEvent(def.FlowID, session.SessionID, FlowSessionEventInput{
		Source: "game-adapter", Type: "state.snapshot", Payload: json.RawMessage(`{"delta":1}`),
	}); err == nil {
		t.Fatal("latest-wins trigger should not be accepted by the durable events endpoint")
	}
	if err := a.ProcessFlowSessionSignal(t.Context(), def.FlowID, session.SessionID, FlowSessionEventInput{
		Source: "game-adapter", Type: "state.snapshot", Payload: json.RawMessage(`{"delta":4}`),
	}); err != nil {
		t.Fatalf("process latest-wins signal: %v", err)
	}
	// Session state and its coalesced output snapshot must survive the Agent
	// being reconstructed for a later request.
	a = New(cfg)
	view, err := a.GetFlowSession(def.FlowID, session.SessionID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	events, err := a.FlowSessionEvents(def.FlowID, session.SessionID, 0, 10)
	if err != nil {
		t.Fatalf("read durable journal: %v", err)
	}
	if view.LastSequence != 0 || view.ProcessedSequence != 0 ||
		view.State["counter"] != float64(4) || len(events) != 0 {
		t.Fatalf("latest-wins signal and output must not grow the durable event journal: view=%+v events=%+v", view, events)
	}
	if view.LastExecution == nil || view.LastExecution.Delivery != flow.SessionDeliveryLatestWins ||
		view.LastExecution.Status != "completed" || view.LastExecution.OutputCount != 1 ||
		len(view.LatestSignalOutputs) != 1 || view.LatestSignalOutputs[0].NodeID != "derive" ||
		view.LatestSignalOutputs[0].Outputs["derived"] != float64(8) {
		t.Fatalf("latest-wins output was not coalesced into the session snapshot: view=%+v", view)
	}
}
