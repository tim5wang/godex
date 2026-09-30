package backend

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/agent"
	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/core/flow"
)

func flowSessionWorkerDefinition(flowID string) *flow.Definition {
	return &flow.Definition{
		FlowID:        flowID,
		Version:       "1",
		Status:        "draft",
		ExecutionMode: flow.ExecutionModeSession,
		SessionWorkflow: &flow.SessionWorkflowSpec{Triggers: []flow.SessionTrigger{
			{EventType: "tick", EntryNode: "worker"},
		}},
		Nodes: []flow.Node{
			{ID: "worker", Kind: flow.KindFunction, Function: &flow.FunctionSpec{
				Runtime: flow.FunctionRuntimeJS,
				Source:  `function handle() { return {session_state: {from: "js"}}; }`,
			}, Outputs: []flow.VarDef{{Name: "session_state", Type: "object"}}},
		},
	}
}

func flowSessionLLMWorkerDefinition(flowID string) *flow.Definition {
	return &flow.Definition{
		FlowID:        flowID,
		Version:       "1",
		Status:        "draft",
		ExecutionMode: flow.ExecutionModeSession,
		SessionWorkflow: &flow.SessionWorkflowSpec{Triggers: []flow.SessionTrigger{
			{EventType: "turn.update", EntryNode: "answer"},
		}},
		Nodes: []flow.Node{{
			ID:         "answer",
			Kind:       flow.KindLLM,
			Prompt:     "Respond to {{event.payload.text}} for session {{session.id}}.",
			TimeoutSec: 5,
			Outputs: []flow.VarDef{
				{Name: "answer", Type: "string"},
				{Name: "session_state", Type: "object"},
			},
		}},
	}
}

func TestFlowSessionLLMNodeRunsThroughBoundedAsyncWorker(t *testing.T) {
	cfg := newTestConfig(t)
	caller := &stubCaller{responses: []protocol.Response{{
		Content: []protocol.Block{protocol.TextBlock(`{"answer":"accepted","session_state":{"answer":"accepted"}}`)},
	}}}
	service := newTestService(cfg, caller)
	def := flowSessionLLMWorkerDefinition("fl_session_llm_worker")
	if _, err := service.CreateFlow(agent.FlowCreateArgs{Def: def}); err != nil {
		t.Fatalf("create session flow: %v", err)
	}
	session, err := service.CreateFlowSession(def.FlowID, def.Version, nil)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	if err := service.Start(ctx); err != nil {
		cancel()
		t.Fatalf("start service: %v", err)
	}
	defer func() {
		cancel()
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer stopCancel()
		if err := service.Stop(stopCtx); err != nil {
			t.Errorf("stop service: %v", err)
		}
	}()

	if _, err := service.AppendFlowSessionEvent(def.FlowID, session.SessionID, agent.FlowSessionEventInput{
		Source: "voice-adapter", Type: "turn.update", Payload: json.RawMessage(`{"text":"hello"}`),
	}); err != nil {
		t.Fatalf("append event: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		view, err := service.GetFlowSession(def.FlowID, session.SessionID)
		if err != nil {
			t.Fatalf("get session: %v", err)
		}
		if view.State["answer"] == "accepted" && view.ProcessedSequence >= 1 {
			if view.State["answer"] != "accepted" || len(view.LastProcessedNodes) != 1 || view.LastProcessedNodes[0] != "answer" {
				t.Fatalf("unexpected LLM worker checkpoint: %+v", view)
			}
			caller.mu.Lock()
			defer caller.mu.Unlock()
			if len(caller.requests) != 1 {
				t.Fatalf("expected one call through the configured worker caller, got %d", len(caller.requests))
			}
			request := caller.requests[0]
			if request.Model != cfg.Model || len(request.Tools) != 0 || request.Stream {
				t.Fatalf("expected one-shot tool-free model call, got %+v", request)
			}
			if len(request.Messages) != 1 || !strings.Contains(protocol.BlocksText(request.Messages[0].Content), "hello") ||
				!strings.Contains(protocol.BlocksText(request.Messages[0].Content), session.SessionID) {
				t.Fatalf("session/event values were not rendered into the LLM prompt: %+v", request.Messages)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("session scheduler did not commit the asynchronous LLM result")
}

func TestFlowSessionTurnInterruptSkipsOldWorkAndFencesLateResult(t *testing.T) {
	cfg := newTestConfig(t)
	service := newTestService(cfg, nil)
	def := flowSessionLLMWorkerDefinition("fl_session_turn_interrupt")
	if _, err := service.CreateFlow(agent.FlowCreateArgs{Def: def}); err != nil {
		t.Fatalf("create session flow: %v", err)
	}
	session, err := service.CreateFlowSession(def.FlowID, def.Version, nil)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	if err := service.Start(ctx); err != nil {
		cancel()
		t.Fatalf("start service: %v", err)
	}
	defer func() {
		cancel()
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer stopCancel()
		if err := service.Stop(stopCtx); err != nil {
			t.Errorf("stop service: %v", err)
		}
	}()

	firstStarted := make(chan struct{})
	firstCanceled := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondStarted := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseFirst) }) }
	defer release()
	var calls atomic.Int32
	service.currentFlowSessionScheduler().workRunner = func(ctx context.Context, _ *agent.Agent, work *agent.FlowSessionWork) (agent.FlowSessionWorkResult, error) {
		switch calls.Add(1) {
		case 1:
			close(firstStarted)
			<-ctx.Done()
			close(firstCanceled)
			<-releaseFirst
			return agent.FlowSessionWorkResult{State: map[string]any{"answer": "stale"}}, nil
		case 2:
			close(secondStarted)
			progress := work.Progress()
			now := time.Now().UTC()
			return agent.FlowSessionWorkResult{
				State: map[string]any{"answer": "fresh"},
				Execution: &agent.FlowSessionExecutionSummary{
					Delivery: progress.Delivery, InputSequence: progress.InputSequence,
					EventType: progress.EventType, Source: progress.Source,
					SourceSequence: progress.SourceSequence, Status: "completed",
					StartedAt: now, CompletedAt: now,
				},
			}, nil
		default:
			return agent.FlowSessionWorkResult{}, nil
		}
	}
	if _, err := service.AppendFlowSessionEvent(def.FlowID, session.SessionID, agent.FlowSessionEventInput{
		Source: "device-1", SourceSequence: 9, Type: "turn.update",
		Payload: json.RawMessage(`{"text":"old turn"}`),
	}); err != nil {
		t.Fatalf("append old turn: %v", err)
	}
	select {
	case <-firstStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("old turn worker did not start")
	}

	if interrupted, err := service.InterruptFlowSessionTurn(def.FlowID, session.SessionID, "device-1", "turn.update", 9); err != nil || interrupted {
		t.Fatalf("same source sequence must not interrupt a turn: interrupted=%v err=%v", interrupted, err)
	}
	select {
	case <-firstCanceled:
		t.Fatal("same-sequence retry canceled the running turn")
	default:
	}

	if interrupted, err := service.InterruptFlowSessionTurn(def.FlowID, session.SessionID, "device-1", "turn.update", 10); err != nil || !interrupted {
		t.Fatalf("newer turn should durably interrupt old work: interrupted=%v err=%v", interrupted, err)
	}
	select {
	case <-firstCanceled:
	case <-time.After(3 * time.Second):
		t.Fatal("newer turn did not cancel the old worker context")
	}
	view, err := service.GetFlowSession(def.FlowID, session.SessionID)
	if err != nil {
		t.Fatalf("read interrupted session: %v", err)
	}
	if view.ProcessedSequence != 1 || view.State["answer"] != nil ||
		view.LastExecution == nil || view.LastExecution.Status != "canceled" {
		t.Fatalf("old turn was not checkpointed as canceled: %+v", view)
	}

	if _, err := service.AppendFlowSessionEvent(def.FlowID, session.SessionID, agent.FlowSessionEventInput{
		Source: "device-1", SourceSequence: 10, Type: "turn.update",
		Payload: json.RawMessage(`{"text":"new turn"}`),
	}); err != nil {
		t.Fatalf("append new turn: %v", err)
	}
	select {
	case <-secondStarted:
		t.Fatal("new turn overlapped the canceled worker before it returned")
	default:
	}
	release()
	select {
	case <-secondStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("new turn did not run after old worker returned")
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		view, err = service.GetFlowSession(def.FlowID, session.SessionID)
		if err != nil {
			t.Fatalf("read new turn result: %v", err)
		}
		if view.ProcessedSequence == 2 {
			if view.State["answer"] != "fresh" || view.LastExecution == nil || view.LastExecution.Status != "completed" {
				t.Fatalf("new turn result was not committed: %+v", view)
			}
			if calls.Load() != 2 {
				t.Fatalf("expected old event to be skipped rather than replayed; worker calls=%d", calls.Load())
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("new turn did not finish after the old work was released")
}

func TestGetFlowSessionReportsEphemeralInFlightExecution(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseWorker := func() {
		releaseOnce.Do(func() { close(release) })
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case started <- struct{}{}:
		default:
		}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	cfg := newTestConfig(t)
	service := newTestService(cfg, nil)
	def := &flow.Definition{
		FlowID:        "fl_session_inflight",
		Version:       "1",
		Status:        "draft",
		ExecutionMode: flow.ExecutionModeSession,
		SessionWorkflow: &flow.SessionWorkflowSpec{Triggers: []flow.SessionTrigger{
			{EventType: "turn.update", EntryNode: "request"},
		}},
		Network: &flow.NetworkPolicy{
			Policy:            "allowlist",
			AllowedDomains:    []string{"127.0.0.1"},
			AllowPrivateHosts: true,
			TimeoutSeconds:    5,
		},
		Nodes: []flow.Node{{
			ID:         "request",
			Kind:       flow.KindService,
			TimeoutSec: 5,
			Service: &flow.ServiceSpec{
				Method: "POST",
				URL:    server.URL,
				Body:   json.RawMessage(`{}`),
			},
			Outputs: []flow.VarDef{
				{Name: "status_code", Type: "number"},
				{Name: "body", Type: "object"},
			},
		}},
	}
	if _, err := service.CreateFlow(agent.FlowCreateArgs{Def: def}); err != nil {
		t.Fatalf("create session flow: %v", err)
	}
	session, err := service.CreateFlowSession(def.FlowID, def.Version, nil)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := service.Start(ctx); err != nil {
		cancel()
		t.Fatalf("start service: %v", err)
	}
	defer func() {
		releaseWorker()
		cancel()
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer stopCancel()
		if err := service.Stop(stopCtx); err != nil {
			t.Errorf("stop service: %v", err)
		}
	}()

	receipt, err := service.AppendFlowSessionEvent(def.FlowID, session.SessionID, agent.FlowSessionEventInput{
		Source: "voice-adapter", SourceSequence: 17, Type: "turn.update", CorrelationID: "turn-1",
	})
	if err != nil {
		t.Fatalf("append event: %v", err)
	}
	select {
	case <-started:
	case <-time.After(4 * time.Second):
		t.Fatal("session worker did not start the blocking service call")
	}

	view, err := service.GetFlowSession(def.FlowID, session.SessionID)
	if err != nil {
		t.Fatalf("read in-flight session: %v", err)
	}
	if view.InFlight == nil {
		t.Fatalf("expected an in-flight execution in the session view: %+v", view)
	}
	progress := view.InFlight
	if progress.Delivery != flow.SessionDeliveryDurable || progress.Status != "running" ||
		progress.InputSequence != receipt.Sequence || progress.EventType != "turn.update" ||
		progress.Source != "voice-adapter" || progress.SourceSequence != 17 ||
		progress.CorrelationID != "turn-1" || progress.QueuedAt.IsZero() || progress.StartedAt == nil {
		t.Fatalf("unexpected in-flight execution metadata: %+v", progress)
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("marshal session view: %v", err)
	}
	var response map[string]any
	if err := json.Unmarshal(encoded, &response); err != nil {
		t.Fatalf("decode session view: %v", err)
	}
	if _, ok := response["in_flight"].(map[string]any); !ok {
		t.Fatalf("session JSON is missing its in_flight object: %s", encoded)
	}

	releaseWorker()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		view, err = service.GetFlowSession(def.FlowID, session.SessionID)
		if err != nil {
			t.Fatalf("read completed session: %v", err)
		}
		if view.LastExecution != nil && view.LastExecution.InputSequence == receipt.Sequence &&
			view.ProcessedSequence == view.LastSequence {
			if view.InFlight != nil {
				t.Fatalf("completed execution remained in-flight: %+v", view.InFlight)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("blocking service execution did not complete")
}

func TestFlowSessionAsyncServiceRegionUsesEventSessionContextAndCommitsDownstreamState(t *testing.T) {
	var requests atomic.Int32
	var expectedSessionID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if got, want := r.Header.Get("Idempotency-Key"), expectedSessionID+":1"; got != want {
			t.Errorf("expected session/event idempotency key %q, got %q", want, got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode service request: %v", err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if body["tenant"] != "tenant-a" || body["text"] != "hello" ||
			body["dialog_id"] != "dialog-42" || body["sequence"] != float64(1) {
			t.Errorf("service template context was not rendered: %#v", body)
		}
		if requests.Add(1) == 1 {
			http.Error(w, "temporary failure", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"answer":"accepted:hello"}`))
	}))
	defer server.Close()

	cfg := newTestConfig(t)
	service := newTestService(cfg, nil)
	def := &flow.Definition{
		FlowID:        "fl_session_service_region",
		Version:       "1",
		Status:        "draft",
		ExecutionMode: flow.ExecutionModeSession,
		SessionWorkflow: &flow.SessionWorkflowSpec{Triggers: []flow.SessionTrigger{
			{EventType: "turn.update", EntryNode: "seed"},
		}},
		Inputs: []flow.VarDef{{Name: "tenant", Type: "string", Required: true}},
		Network: &flow.NetworkPolicy{
			Policy:            "allowlist",
			AllowedDomains:    []string{"127.0.0.1"},
			AllowPrivateHosts: true,
			TimeoutSeconds:    2,
		},
		Nodes: []flow.Node{
			{
				ID:   "seed",
				Kind: flow.KindFunction,
				Function: &flow.FunctionSpec{
					Runtime: flow.FunctionRuntimeJS,
					Source: `function handle(ctx, event) {
						return {session_state: {
							dialog_id: event.payload.dialog_id,
							turn: Number(ctx.session.state.turn || 0) + 1
						}};
					}`,
				},
				Outputs: []flow.VarDef{{Name: "session_state", Type: "object"}},
			},
			{
				ID:         "dispatch",
				Kind:       flow.KindService,
				TimeoutSec: 5,
				Retry: &flow.RetryPolicy{
					MaxAttempts:       2,
					InitialIntervalMS: 1,
					MaxIntervalMS:     1,
				},
				Service: &flow.ServiceSpec{
					Method: "POST",
					URL:    server.URL + "/v1/turns",
					Headers: map[string]string{
						"Idempotency-Key": "{{event.flow_session_id}}:{{event.sequence}}",
					},
					Body: json.RawMessage(`{
						"tenant":"{{inputs.tenant}}",
						"text":"{{event.payload.text}}",
						"dialog_id":"{{session.state.dialog_id}}",
						"sequence":"{{event.sequence}}"
					}`),
				},
				Outputs: []flow.VarDef{
					{Name: "status_code", Type: "number"},
					{Name: "body", Type: "object"},
				},
			},
			{
				ID:   "finish",
				Kind: flow.KindFunction,
				Function: &flow.FunctionSpec{
					Runtime: flow.FunctionRuntimeJS,
					Source: `function handle(ctx) {
						return {session_state: {
							dialog_id: ctx.session.state.dialog_id,
							turn: ctx.session.state.turn,
							answer: ctx.outputs.dispatch.body.answer
						}};
					}`,
				},
				Outputs: []flow.VarDef{{Name: "session_state", Type: "object"}},
			},
		},
		Edges: []flow.Edge{
			{ID: "seed-dispatch", From: "seed", To: "dispatch", EdgeType: flow.EdgeDataDependency},
			{ID: "dispatch-finish", From: "dispatch", To: "finish", EdgeType: flow.EdgeDataDependency},
		},
	}
	if _, err := service.CreateFlow(agent.FlowCreateArgs{Def: def}); err != nil {
		t.Fatalf("create session flow: %v", err)
	}
	session, err := service.CreateFlowSession(def.FlowID, def.Version, map[string]any{"tenant": "tenant-a"})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	expectedSessionID = session.SessionID
	ctx, cancel := context.WithCancel(context.Background())
	if err := service.Start(ctx); err != nil {
		cancel()
		t.Fatalf("start service: %v", err)
	}
	defer func() {
		cancel()
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer stopCancel()
		if err := service.Stop(stopCtx); err != nil {
			t.Errorf("stop service: %v", err)
		}
	}()

	if _, err := service.AppendFlowSessionEvent(def.FlowID, session.SessionID, agent.FlowSessionEventInput{
		Source: "voice-adapter",
		Type:   "turn.update",
		Payload: json.RawMessage(`{
			"dialog_id":"dialog-42",
			"text":"hello"
		}`),
	}); err != nil {
		t.Fatalf("append event: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		view, err := service.GetFlowSession(def.FlowID, session.SessionID)
		if err != nil {
			t.Fatalf("read session: %v", err)
		}
		if view.ProcessedSequence == 1 {
			if requests.Load() != 2 {
				t.Fatalf("expected one configured retry, got %d HTTP requests", requests.Load())
			}
			if view.State["dialog_id"] != "dialog-42" || view.State["turn"] != float64(1) ||
				view.State["answer"] != "accepted:hello" {
				t.Fatalf("downstream result was not committed to session state: %#v", view.State)
			}
			if len(view.LastProcessedNodes) != 3 || strings.Join(view.LastProcessedNodes, ",") != "seed,dispatch,finish" {
				t.Fatalf("expected ordered JS → service → JS region, got %v", view.LastProcessedNodes)
			}
			if view.LastError != "" {
				t.Fatalf("successful retry left a session error: %q", view.LastError)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("async service region did not commit the event")
}

func TestFlowSessionSlowFunctionDoesNotBlockDurableIngress(t *testing.T) {
	cfg := newTestConfig(t)
	service := newTestService(cfg, nil)
	def := &flow.Definition{
		FlowID:        "fl_session_slow",
		Version:       "1",
		Status:        "draft",
		ExecutionMode: flow.ExecutionModeSession,
		SessionWorkflow: &flow.SessionWorkflowSpec{Triggers: []flow.SessionTrigger{
			{EventType: "tick", EntryNode: "slow"},
		}},
		Nodes: []flow.Node{
			{ID: "slow", Kind: flow.KindFunction, Function: &flow.FunctionSpec{
				Runtime: flow.FunctionRuntimeJS,
				Source: `function handle(ctx, event) {
					const until = Date.now() + 1200;
					while (Date.now() < until) {}
					return {session_state: {count: Number(ctx.session.state.count || 0) + 1}};
				}`,
			}},
		},
	}
	if _, err := service.CreateFlow(agent.FlowCreateArgs{Def: def}); err != nil {
		t.Fatalf("create session flow: %v", err)
	}
	session, err := service.CreateFlowSession(def.FlowID, def.Version, nil)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	if err := service.Start(ctx); err != nil {
		cancel()
		t.Fatalf("start service: %v", err)
	}
	defer func() {
		cancel()
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer stopCancel()
		if err := service.Stop(stopCtx); err != nil {
			t.Errorf("stop service: %v", err)
		}
	}()

	appendEvent := func(sequence uint64) {
		t.Helper()
		_, err := service.AppendFlowSessionEvent(def.FlowID, session.SessionID, agent.FlowSessionEventInput{
			Source: "test-adapter", SourceSequence: sequence, Type: "tick",
		})
		if err != nil {
			t.Fatalf("append event %d: %v", sequence, err)
		}
	}
	appendEvent(1)
	time.Sleep(150 * time.Millisecond)
	started := time.Now()
	appendEvent(2)
	if elapsed := time.Since(started); elapsed >= 700*time.Millisecond {
		t.Fatalf("durable ingress waited for the slow function worker: %s", elapsed)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		view, err := service.GetFlowSession(def.FlowID, session.SessionID)
		if err != nil {
			t.Fatalf("read session: %v", err)
		}
		if view.ProcessedSequence == 2 {
			if view.State["count"] != float64(2) {
				t.Fatalf("expected both durable events to execute, state=%#v", view.State)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("durable events were not drained by the session coordinator")
}

func TestFlowSessionLatestWinsSignalsCoalesceAndStayVolatile(t *testing.T) {
	cfg := newTestConfig(t)
	service := newTestService(cfg, nil)
	def := &flow.Definition{
		FlowID:        "fl_session_latest",
		Version:       "1",
		Status:        "draft",
		ExecutionMode: flow.ExecutionModeSession,
		SessionWorkflow: &flow.SessionWorkflowSpec{Triggers: []flow.SessionTrigger{
			{EventType: "hold", EntryNode: "hold"},
			{EventType: "snapshot", EntryNode: "snapshot", Delivery: flow.SessionDeliveryLatestWins},
		}},
		Nodes: []flow.Node{
			{ID: "hold", Kind: flow.KindFunction, Function: &flow.FunctionSpec{
				Runtime: flow.FunctionRuntimeJS,
				Source: `function handle(ctx, event) {
					const until = Date.now() + 900;
					while (Date.now() < until) {}
					return {session_state: {held: true}};
				}`,
			}},
			{ID: "snapshot", Kind: flow.KindFunction, Function: &flow.FunctionSpec{
				Runtime: flow.FunctionRuntimeJS,
				Source: `function handle(ctx, event) {
					return {latest: event.payload.value, session_state: {latest: event.payload.value}};
				}`,
			}, Outputs: []flow.VarDef{{Name: "latest", Type: "number"}}},
		},
	}
	if _, err := service.CreateFlow(agent.FlowCreateArgs{Def: def}); err != nil {
		t.Fatalf("create session flow: %v", err)
	}
	session, err := service.CreateFlowSession(def.FlowID, def.Version, nil)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := service.Start(ctx); err != nil {
		cancel()
		t.Fatalf("start service: %v", err)
	}
	defer func() {
		cancel()
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer stopCancel()
		if err := service.Stop(stopCtx); err != nil {
			t.Errorf("stop service: %v", err)
		}
	}()

	if _, err := service.AppendFlowSessionEvent(def.FlowID, session.SessionID, agent.FlowSessionEventInput{
		Source: "test-adapter", Type: "hold",
	}); err != nil {
		t.Fatalf("append durable hold event: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	for value := 1; value <= 3; value++ {
		payload, _ := json.Marshal(map[string]any{"value": value})
		receipt, err := service.PublishFlowSessionSignal(def.FlowID, session.SessionID, agent.FlowSessionEventInput{
			Source: "game-adapter", Type: "snapshot", Payload: payload,
		})
		if err != nil {
			t.Fatalf("publish latest-wins signal %d: %v", value, err)
		}
		if receipt.Coalesced != (value > 1) {
			t.Fatalf("signal %d coalesced=%v", value, receipt.Coalesced)
		}
	}

	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		view, err := service.GetFlowSession(def.FlowID, session.SessionID)
		if err != nil {
			t.Fatalf("read session: %v", err)
		}
		if view.State["latest"] == float64(3) && view.ProcessedSequence == view.LastSequence {
			events, err := service.FlowSessionEvents(def.FlowID, session.SessionID, 0, 10)
			if err != nil {
				t.Fatalf("read event journal: %v", err)
			}
			if len(events) != 1 || events[0].Type != "hold" || view.LastSequence != 1 {
				t.Fatalf("latest-wins input/output should not grow the durable journal: events=%+v view=%+v", events, view)
			}
			if view.LastExecution == nil ||
				view.LastExecution.Delivery != flow.SessionDeliveryLatestWins ||
				view.LastExecution.EventType != "snapshot" ||
				view.LastExecution.Source != "game-adapter" ||
				view.LastExecution.OutputCount != 1 ||
				len(view.LatestSignalOutputs) != 1 ||
				view.LatestSignalOutputs[0].NodeID != "snapshot" ||
				view.LatestSignalOutputs[0].Outputs["latest"] != float64(3) {
				t.Fatalf("unexpected latest-wins output snapshot: view=%+v", view)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("coalesced latest-wins signal was not applied")
}

func TestFlowSessionLanesRunIndependentlyAndFenceLateResults(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseSlow := func() { releaseOnce.Do(func() { close(release) }) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case started <- struct{}{}:
		default:
		}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	defer releaseSlow()

	cfg := newTestConfig(t)
	service := newTestService(cfg, nil)
	def := &flow.Definition{
		FlowID:        "fl_session_independent_lanes",
		Version:       "1",
		Status:        "draft",
		ExecutionMode: flow.ExecutionModeSession,
		SessionWorkflow: &flow.SessionWorkflowSpec{
			Triggers: []flow.SessionTrigger{
				{EventType: "state.slow", EntryNode: "slow", Delivery: flow.SessionDeliveryLatestWins, LaneID: "strategic"},
				{EventType: "state.fast", EntryNode: "fast", Delivery: flow.SessionDeliveryLatestWins, LaneID: "realtime"},
			},
			Lanes: []flow.SessionLane{
				{ID: "strategic", Cadence: flow.SessionLaneCadenceEvent, DeadlineMS: 5000, Class: flow.SessionLaneClassSlow},
				{ID: "realtime", Cadence: flow.SessionLaneCadenceEvent, DeadlineMS: 1000, Class: flow.SessionLaneClassFast},
			},
		},
		Network: &flow.NetworkPolicy{
			Policy: "allowlist", AllowedDomains: []string{"127.0.0.1"}, AllowPrivateHosts: true, TimeoutSeconds: 5,
		},
		Nodes: []flow.Node{
			{
				ID: "slow", Kind: flow.KindService,
				Service: &flow.ServiceSpec{Method: "POST", URL: server.URL, Body: json.RawMessage(`{}`)},
				Outputs: []flow.VarDef{{Name: "status_code", Type: "number"}, {Name: "body", Type: "object"}},
			},
			{
				ID: "fast", Kind: flow.KindFunction,
				Function: &flow.FunctionSpec{
					Runtime: flow.FunctionRuntimeJS,
					Source:  `function handle(ctx, event) { return {value: event.payload.value, session_state: {fast: event.payload.value}}; }`,
				},
				Outputs: []flow.VarDef{{Name: "value", Type: "string"}, {Name: "session_state", Type: "object"}},
			},
		},
	}
	if _, err := service.CreateFlow(agent.FlowCreateArgs{Def: def}); err != nil {
		t.Fatalf("create session flow: %v", err)
	}
	session, err := service.CreateFlowSession(def.FlowID, def.Version, nil)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	if err := service.Start(ctx); err != nil {
		cancel()
		t.Fatalf("start service: %v", err)
	}
	defer func() {
		cancel()
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer stopCancel()
		if err := service.Stop(stopCtx); err != nil {
			t.Errorf("stop service: %v", err)
		}
	}()

	if _, err := service.PublishFlowSessionSignal(def.FlowID, session.SessionID, agent.FlowSessionEventInput{
		Source: "game", Type: "state.slow",
	}); err != nil {
		t.Fatalf("publish slow lane state: %v", err)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("slow lane did not start its blocking service call")
	}

	if _, err := service.PublishFlowSessionSignal(def.FlowID, session.SessionID, agent.FlowSessionEventInput{
		Source: "game", Type: "state.fast", Payload: json.RawMessage(`{"value":"fresh"}`),
	}); err != nil {
		t.Fatalf("publish fast lane state: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		view, err := service.GetFlowSession(def.FlowID, session.SessionID)
		if err != nil {
			t.Fatalf("read session: %v", err)
		}
		if view.State["fast"] == "fresh" {
			foundSlow := false
			for _, inFlight := range view.InFlightLanes {
				if inFlight.LaneID == "strategic" && inFlight.Status == "running" {
					foundSlow = true
				}
			}
			if !foundSlow {
				t.Fatalf("slow lane was not reported while fast lane completed: %+v", view.InFlightLanes)
			}
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	view, err := service.GetFlowSession(def.FlowID, session.SessionID)
	if err != nil {
		t.Fatalf("read committed fast lane result: %v", err)
	}
	if view.State["fast"] != "fresh" {
		t.Fatalf("fast lane waited behind slow lane or failed to commit: %#v", view.State)
	}

	releaseSlow()
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		view, err = service.GetFlowSession(def.FlowID, session.SessionID)
		if err != nil {
			t.Fatalf("read session after slow lane: %v", err)
		}
		if len(view.InFlightLanes) == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if view.State["fast"] != "fresh" || view.LastExecution == nil || view.LastExecution.LaneID != "realtime" {
		t.Fatalf("late slow-lane result overwrote newer state: state=%#v execution=%+v", view.State, view.LastExecution)
	}
}

func TestFlowSessionPeriodicLaneRunsWithoutJournalGrowth(t *testing.T) {
	cfg := newTestConfig(t)
	service := newTestService(cfg, nil)
	def := &flow.Definition{
		FlowID:        "fl_session_periodic_lane",
		Version:       "1",
		Status:        "draft",
		ExecutionMode: flow.ExecutionModeSession,
		SessionWorkflow: &flow.SessionWorkflowSpec{
			Triggers: []flow.SessionTrigger{{
				EventType: "timer.tick", EntryNode: "tick", Delivery: flow.SessionDeliveryLatestWins, LaneID: "clock",
			}},
			Lanes: []flow.SessionLane{{
				ID: "clock", Cadence: flow.SessionLaneCadencePeriodic, IntervalMS: 50,
				DeadlineMS: 1000, Class: flow.SessionLaneClassFast,
			}},
		},
		Nodes: []flow.Node{{
			ID: "tick", Kind: flow.KindFunction,
			Function: &flow.FunctionSpec{
				Runtime: flow.FunctionRuntimeJS,
				Source: `function handle(ctx) {
					const count = Number(ctx.session.state.count || 0) + 1;
					return {count, session_state: {count}};
				}`,
			},
			Outputs: []flow.VarDef{{Name: "count", Type: "number"}, {Name: "session_state", Type: "object"}},
		}},
	}
	if _, err := service.CreateFlow(agent.FlowCreateArgs{Def: def}); err != nil {
		t.Fatalf("create session flow: %v", err)
	}
	session, err := service.CreateFlowSession(def.FlowID, def.Version, nil)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := service.Start(ctx); err != nil {
		cancel()
		t.Fatalf("start service: %v", err)
	}
	defer func() {
		cancel()
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer stopCancel()
		if err := service.Stop(stopCtx); err != nil {
			t.Errorf("stop service: %v", err)
		}
	}()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		view, err := service.GetFlowSession(def.FlowID, session.SessionID)
		if err != nil {
			t.Fatalf("read session: %v", err)
		}
		count, _ := view.State["count"].(float64)
		if count >= 2 {
			events, err := service.FlowSessionEvents(def.FlowID, session.SessionID, 0, 10)
			if err != nil {
				t.Fatalf("read event journal: %v", err)
			}
			if len(events) != 0 || view.LastSequence != 0 || view.LastExecution == nil ||
				view.LastExecution.EventType != "timer.tick" || view.LastExecution.LaneID != "clock" {
				t.Fatalf("periodic lane should stay volatile and observable: events=%+v view=%+v", events, view)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("periodic lane did not execute")
}

func TestFlowSessionPendingJournalResumesWhenBackendStarts(t *testing.T) {
	cfg := newTestConfig(t)
	firstService := newTestService(cfg, nil)
	def := &flow.Definition{
		FlowID:        "fl_session_restart",
		Version:       "1",
		Status:        "draft",
		ExecutionMode: flow.ExecutionModeSession,
		SessionWorkflow: &flow.SessionWorkflowSpec{Triggers: []flow.SessionTrigger{
			{EventType: "connected", EntryNode: "mark"},
		}},
		Nodes: []flow.Node{
			{ID: "mark", Kind: flow.KindFunction, Function: &flow.FunctionSpec{
				Runtime: flow.FunctionRuntimeJS,
				Source:  `function handle(ctx, event) { return {session_state: {connected: true}}; }`,
			}},
		},
	}
	if _, err := firstService.CreateFlow(agent.FlowCreateArgs{Def: def}); err != nil {
		t.Fatalf("create session flow: %v", err)
	}
	session, err := firstService.CreateFlowSession(def.FlowID, def.Version, nil)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := firstService.AppendFlowSessionEvent(def.FlowID, session.SessionID, agent.FlowSessionEventInput{
		Source: "voice-adapter", SourceSequence: 1, Type: "connected",
	}); err != nil {
		t.Fatalf("persist event before runtime start: %v", err)
	}

	restartedService := newTestService(cfg, nil)
	ctx, cancel := context.WithCancel(context.Background())
	if err := restartedService.Start(ctx); err != nil {
		cancel()
		t.Fatalf("start restarted backend: %v", err)
	}
	defer func() {
		cancel()
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer stopCancel()
		if err := restartedService.Stop(stopCtx); err != nil {
			t.Errorf("stop restarted backend: %v", err)
		}
	}()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		view, err := restartedService.GetFlowSession(def.FlowID, session.SessionID)
		if err != nil {
			t.Fatalf("read recovered session: %v", err)
		}
		if view.ProcessedSequence == 1 {
			if view.State["connected"] != true {
				t.Fatalf("recovered event did not update session state: %#v", view.State)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("backend startup did not resume the pending durable session event")
}

func TestFlowSessionWorkerPauseFencesLateResultAndResumeReplays(t *testing.T) {
	cfg := newTestConfig(t)
	service := newTestService(cfg, nil)
	def := flowSessionWorkerDefinition("fl_session_worker_pause")
	if _, err := service.CreateFlow(agent.FlowCreateArgs{Def: def}); err != nil {
		t.Fatalf("create session flow: %v", err)
	}
	session, err := service.CreateFlowSession(def.FlowID, def.Version, nil)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	if err := service.Start(ctx); err != nil {
		cancel()
		t.Fatalf("start service: %v", err)
	}
	defer func() {
		cancel()
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer stopCancel()
		if err := service.Stop(stopCtx); err != nil {
			t.Errorf("stop service: %v", err)
		}
	}()

	started := make(chan struct{})
	firstCanceled := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondDone := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseFirst) }) }
	defer release()
	var calls atomic.Int32
	scheduler := service.currentFlowSessionScheduler()
	scheduler.workRunner = func(ctx context.Context, _ *agent.Agent, _ *agent.FlowSessionWork) (agent.FlowSessionWorkResult, error) {
		switch calls.Add(1) {
		case 1:
			close(started)
			<-ctx.Done()
			close(firstCanceled)
			<-releaseFirst
			return agent.FlowSessionWorkResult{State: map[string]any{"value": "stale"}}, nil
		case 2:
			close(secondDone)
		}
		return agent.FlowSessionWorkResult{State: map[string]any{"value": "fresh"}}, nil
	}
	if _, err := service.AppendFlowSessionEvent(def.FlowID, session.SessionID, agent.FlowSessionEventInput{
		Source: "test-adapter", Type: "tick",
	}); err != nil {
		t.Fatalf("append event: %v", err)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("fake worker did not start")
	}
	if _, err := service.PauseFlowSession(def.FlowID, session.SessionID); err != nil {
		t.Fatalf("pause session: %v", err)
	}
	select {
	case <-firstCanceled:
	case <-time.After(3 * time.Second):
		t.Fatal("pause did not cancel the in-flight worker")
	}
	paused, err := service.GetFlowSession(def.FlowID, session.SessionID)
	if err != nil {
		t.Fatalf("read paused session: %v", err)
	}
	if paused.ProcessedSequence != 0 || paused.State["value"] != nil {
		t.Fatalf("late worker result changed paused session: %+v", paused)
	}
	if _, err := service.ResumeFlowSession(def.FlowID, session.SessionID); err != nil {
		t.Fatalf("resume session: %v", err)
	}
	select {
	case <-secondDone:
		t.Fatal("resume launched overlapping work before canceled worker returned")
	default:
	}
	release()
	select {
	case <-secondDone:
	case <-time.After(3 * time.Second):
		t.Fatal("resumed session did not dispatch a fresh worker")
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		view, err := service.GetFlowSession(def.FlowID, session.SessionID)
		if err != nil {
			t.Fatalf("read resumed session: %v", err)
		}
		if view.ProcessedSequence == 3 {
			if view.State["value"] != "fresh" {
				t.Fatalf("expected only fresh result to be applied, got %#v", view.State)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("resumed session did not drain its event and lifecycle journal")
}

func TestFlowSessionWorkerRestartReplaysUncommittedWork(t *testing.T) {
	cfg := newTestConfig(t)
	firstService := newTestService(cfg, nil)
	def := flowSessionWorkerDefinition("fl_session_worker_restart")
	if _, err := firstService.CreateFlow(agent.FlowCreateArgs{Def: def}); err != nil {
		t.Fatalf("create session flow: %v", err)
	}
	session, err := firstService.CreateFlowSession(def.FlowID, def.Version, nil)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := firstService.Start(ctx); err != nil {
		cancel()
		t.Fatalf("start first service: %v", err)
	}
	started := make(chan struct{})
	firstService.currentFlowSessionScheduler().workRunner = func(ctx context.Context, _ *agent.Agent, _ *agent.FlowSessionWork) (agent.FlowSessionWorkResult, error) {
		close(started)
		<-ctx.Done()
		return agent.FlowSessionWorkResult{State: map[string]any{"value": "lost"}}, nil
	}
	if _, err := firstService.AppendFlowSessionEvent(def.FlowID, session.SessionID, agent.FlowSessionEventInput{
		Source: "test-adapter", Type: "tick",
	}); err != nil {
		cancel()
		t.Fatalf("append event: %v", err)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("fake worker did not start")
	}
	cancel()
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer stopCancel()
	if err := firstService.Stop(stopCtx); err != nil {
		t.Fatalf("stop first service: %v", err)
	}

	restartedService := newTestService(cfg, nil)
	ctx2, cancel2 := context.WithCancel(context.Background())
	if err := restartedService.Start(ctx2); err != nil {
		cancel2()
		t.Fatalf("start restarted service: %v", err)
	}
	defer func() {
		cancel2()
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer stopCancel()
		if err := restartedService.Stop(stopCtx); err != nil {
			t.Errorf("stop restarted service: %v", err)
		}
	}()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		view, err := restartedService.GetFlowSession(def.FlowID, session.SessionID)
		if err != nil {
			t.Fatalf("read recovered session: %v", err)
		}
		if view.ProcessedSequence == 1 {
			if view.State["from"] != "js" || view.State["value"] == "lost" {
				t.Fatalf("restart should re-run and commit the durable work: %#v", view.State)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("restarted service did not replay uncommitted worker work")
}
