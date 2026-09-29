package agent

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/core/flow"
)

func sessionTestDefinition() *flow.Definition {
	return &flow.Definition{
		FlowID:        "fl_session_test",
		Version:       "1",
		Status:        FlowStatusDraft,
		ExecutionMode: flow.ExecutionModeSession,
		SessionWorkflow: &flow.SessionWorkflowSpec{Triggers: []flow.SessionTrigger{
			{EventType: "turn.started", EntryNode: "respond"},
		}},
		Nodes: []flow.Node{
			{ID: "respond", Kind: flow.KindFunction, Function: &flow.FunctionSpec{
				Runtime: flow.FunctionRuntimeJS,
				Source:  `function handle(ctx, event) { return {}; }`,
			}},
		},
	}
}

func TestFlowSessionLifecycleAndRunModeIsolation(t *testing.T) {
	cfg := testFlowConfig(t.TempDir())
	a1 := New(cfg)
	if _, err := a1.CreateFlow(FlowCreateArgs{Def: sessionTestDefinition()}); err != nil {
		t.Fatalf("create session flow: %v", err)
	}
	if _, _, err := a1.CreateFlowRunIdempotent(t.Context(), "fl_session_test", "1", nil, "", ""); err == nil || !strings.Contains(err.Error(), "session execution mode") {
		t.Fatalf("expected regular FlowRun to reject session flow, got %v", err)
	}

	session, err := a1.CreateFlowSession("fl_session_test", "1", nil)
	if err != nil {
		t.Fatalf("create FlowSession: %v", err)
	}
	if session.SessionID == "" || session.Status != "active" || session.Version != "1" || session.Digest == "" {
		t.Fatalf("unexpected FlowSession snapshot: %+v", session)
	}
	if err := a1.DeleteFlowVersion("fl_session_test", "1"); err == nil || !strings.Contains(err.Error(), "active session") {
		t.Fatalf("expected active session to protect its Flow version, got %v", err)
	}
	changedDefinition := sessionTestDefinition()
	changedDefinition.Nodes[0].Prompt = "changed while pinned"
	if _, err := a1.CreateFlow(FlowCreateArgs{Def: changedDefinition}); err == nil || !strings.Contains(err.Error(), "pinned by active FlowSession") {
		t.Fatalf("expected active session to pin its compiled version, got %v", err)
	}

	// Backend flow agents are rebuilt per request. Concurrent duplicate
	// submissions through separate agents must still share one durable
	// sequence and only append once.
	a2 := New(cfg)
	const submitters = 16
	var wg sync.WaitGroup
	receipts := make(chan FlowSessionEventReceipt, submitters)
	errs := make(chan error, submitters)
	for i := 0; i < submitters; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			ag := a1
			if index%2 == 1 {
				ag = a2
			}
			payload := `{"turn":1,"nested":{"a":1,"b":2}}`
			if index%2 == 1 {
				payload = `{ "nested" : { "b" : 2, "a" : 1 }, "turn" : 1 }`
			}
			receipt, err := ag.AppendFlowSessionEvent("fl_session_test", session.SessionID, FlowSessionEventInput{
				Source:         "voice-adapter",
				SourceSequence: 1,
				Type:           "turn.started",
				Payload:        json.RawMessage(payload),
			})
			if err != nil {
				errs <- err
				return
			}
			receipts <- receipt
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("append duplicate event: %v", err)
	}
	close(receipts)
	accepted, duplicate := 0, 0
	for receipt := range receipts {
		if receipt.Sequence != 1 {
			t.Errorf("expected all retries to reference sequence 1, got %+v", receipt)
		}
		if receipt.Duplicate {
			duplicate++
		} else {
			accepted++
		}
	}
	if accepted != 1 || duplicate != submitters-1 {
		t.Fatalf("expected one append and %d duplicate receipts, got accepted=%d duplicate=%d", submitters-1, accepted, duplicate)
	}
	if _, err := a1.AppendFlowSessionEvent("fl_session_test", session.SessionID, FlowSessionEventInput{
		Source:         "voice-adapter",
		SourceSequence: 1,
		Type:           "turn.started",
		Payload:        json.RawMessage(`{"turn":2}`),
	}); !errors.Is(err, ErrFlowSessionConflict) {
		t.Fatalf("reusing a source sequence with different content must conflict, got %v", err)
	}

	// A separately-created Agent reads the same persisted event and lifecycle.
	events, err := a2.FlowSessionEvents("fl_session_test", session.SessionID, 0, 10)
	if err != nil {
		t.Fatalf("read session events: %v", err)
	}
	if len(events) != 1 || events[0].Sequence != 1 || events[0].Digest != session.Digest {
		t.Fatalf("unexpected persisted events: %+v", events)
	}
	view, err := a2.GetFlowSession("fl_session_test", session.SessionID)
	if err != nil || view.LastSequence != 1 {
		t.Fatalf("expected persisted sequence 1, view=%+v err=%v", view, err)
	}

	paused, err := a2.PauseFlowSession("fl_session_test", session.SessionID)
	if err != nil || paused.Status != "paused" || paused.LastSequence != 2 {
		t.Fatalf("pause: view=%+v err=%v", paused, err)
	}
	duplicateReceipt, err := a1.AppendFlowSessionEvent("fl_session_test", session.SessionID, FlowSessionEventInput{
		Source:         "voice-adapter",
		SourceSequence: 1,
		Type:           "turn.started",
		Payload:        json.RawMessage(`{"nested":{"b":2,"a":1},"turn":1}`),
	})
	if err != nil || !duplicateReceipt.Duplicate || duplicateReceipt.Sequence != 1 {
		t.Fatalf("an acknowledged retry should remain idempotent after pause: receipt=%+v err=%v", duplicateReceipt, err)
	}
	if _, err := a1.AppendFlowSessionEvent("fl_session_test", session.SessionID, FlowSessionEventInput{
		Source: "voice-adapter", Type: "turn.partial", Payload: json.RawMessage(`{}`),
	}); !errors.Is(err, ErrFlowSessionConflict) {
		t.Fatalf("expected paused session to reject ingress, got %v", err)
	}
	resumed, err := a1.ResumeFlowSession("fl_session_test", session.SessionID)
	if err != nil || resumed.Status != "active" || resumed.LastSequence != 3 {
		t.Fatalf("resume: view=%+v err=%v", resumed, err)
	}
	ended, err := a2.EndFlowSession("fl_session_test", session.SessionID, "canceled")
	if err != nil || ended.Status != "canceled" || ended.LastSequence != 4 || ended.EndedAt.IsZero() {
		t.Fatalf("end: view=%+v err=%v", ended, err)
	}

	listed, err := a1.ListFlowSessions("fl_session_test")
	if err != nil || len(listed) != 1 || listed[0].Status != "canceled" {
		t.Fatalf("list sessions: sessions=%+v err=%v", listed, err)
	}
	if err := a1.DeleteFlowVersion("fl_session_test", "1"); err != nil {
		t.Fatalf("terminal session should no longer protect its Flow version: %v", err)
	}
}

func TestFlowSessionEventValidation(t *testing.T) {
	cfg := testFlowConfig(t.TempDir())
	a := New(cfg)
	if _, err := a.CreateFlow(FlowCreateArgs{Def: sessionTestDefinition()}); err != nil {
		t.Fatalf("create session flow: %v", err)
	}
	session, err := a.CreateFlowSession("fl_session_test", "1", nil)
	if err != nil {
		t.Fatalf("create FlowSession: %v", err)
	}
	for _, input := range []FlowSessionEventInput{
		{Source: "adapter", Type: "bad.payload", Payload: json.RawMessage(`[]`)},
		{Source: "adapter", Type: "bad.payload", Payload: json.RawMessage(strings.Repeat("x", MaxFlowSessionEventPayloadBytes+1))},
		{Source: "godex", Type: "reserved"},
	} {
		if _, err := a.AppendFlowSessionEvent("fl_session_test", session.SessionID, input); err == nil {
			t.Errorf("expected invalid event to be rejected: %+v", input)
		}
	}
}

func TestFlowSessionRecoversEventAheadOfSnapshot(t *testing.T) {
	cfg := testFlowConfig(t.TempDir())
	a1 := New(cfg)
	if _, err := a1.CreateFlow(FlowCreateArgs{Def: sessionTestDefinition()}); err != nil {
		t.Fatalf("create session flow: %v", err)
	}
	session, err := a1.CreateFlowSession("fl_session_test", "1", nil)
	if err != nil {
		t.Fatalf("create FlowSession: %v", err)
	}
	input, err := normalizeFlowSessionEventInput(FlowSessionEventInput{
		Source:         "adapter",
		SourceSequence: 5,
		Type:           "state.updated",
		Payload:        json.RawMessage(`{"state":{"ready":true}}`),
	})
	if err != nil {
		t.Fatalf("normalize input: %v", err)
	}
	dir, err := a1.flows.flowSessionDir("fl_session_test", session.SessionID)
	if err != nil {
		t.Fatalf("session dir: %v", err)
	}
	event := FlowSessionEvent{
		SessionID:      session.SessionID,
		FlowID:         "fl_session_test",
		Version:        session.Version,
		Digest:         session.Digest,
		Source:         input.Source,
		SourceSequence: input.SourceSequence,
		Sequence:       1,
		StateVersion:   1,
		Type:           input.Type,
		ReceivedAt:     session.StartedAt.Add(time.Second),
		Payload:        input.Payload,
	}
	if err := appendFlowSessionEvent(filepath.Join(dir, "events.jsonl"), event); err != nil {
		t.Fatalf("append event before checkpoint: %v", err)
	}

	// A fresh Agent must reconcile the write-ahead event with the stale
	// summary, including the source-sequence idempotency cursor.
	a2 := New(cfg)
	view, err := a2.GetFlowSession("fl_session_test", session.SessionID)
	if err != nil || view.LastSequence != 1 {
		t.Fatalf("recover snapshot: view=%+v err=%v", view, err)
	}
	receipt, err := a2.AppendFlowSessionEvent("fl_session_test", session.SessionID, input)
	if err != nil || !receipt.Duplicate || receipt.Sequence != 1 {
		t.Fatalf("recovered source cursor: receipt=%+v err=%v", receipt, err)
	}
	file, err := os.OpenFile(filepath.Join(dir, "events.jsonl"), os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatalf("open event journal: %v", err)
	}
	if _, err := file.WriteString(`{"sequence":2`); err != nil {
		_ = file.Close()
		t.Fatalf("write incomplete tail: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close event journal: %v", err)
	}
	view, err = a2.GetFlowSession("fl_session_test", session.SessionID)
	if err != nil || view.LastSequence != 1 {
		t.Fatalf("recover incomplete append tail: view=%+v err=%v", view, err)
	}
	events, err := a2.FlowSessionEvents("fl_session_test", session.SessionID, 0, 10)
	if err != nil || len(events) != 1 || events[0].Sequence != 1 {
		t.Fatalf("incomplete event tail should be truncated: events=%+v err=%v", events, err)
	}
}

func TestFlowSessionRecoversPendingOutputOutbox(t *testing.T) {
	for _, journalWritten := range []bool{false, true} {
		name := "before journal append"
		if journalWritten {
			name = "after journal append"
		}
		t.Run(name, func(t *testing.T) {
			cfg := testFlowConfig(t.TempDir())
			a := New(cfg)
			if _, err := a.CreateFlow(FlowCreateArgs{Def: sessionTestDefinition()}); err != nil {
				t.Fatalf("create session flow: %v", err)
			}
			session, err := a.CreateFlowSession("fl_session_test", "1", nil)
			if err != nil {
				t.Fatalf("create FlowSession: %v", err)
			}
			if _, err := a.AppendFlowSessionEvent("fl_session_test", session.SessionID, FlowSessionEventInput{
				Source: "adapter", Type: "turn.started", Payload: json.RawMessage(`{"turn":1}`),
			}); err != nil {
				t.Fatalf("append input event: %v", err)
			}

			rec, err := a.flows.loadFlowSession("fl_session_test", session.SessionID)
			if err != nil {
				t.Fatalf("load FlowSession record: %v", err)
			}
			rec.ProcessedSequence = 1
			rec.State = map[string]any{"committed": true}
			rec.StateVersion++
			rec.ExecutionGeneration++
			rec.LastProcessedEventType = "turn.started"
			if err := stageFlowSessionOutputEvents(&rec, []flowSessionOutput{{
				OutputID:       "fso_recovery",
				InputSequence:  1,
				InputEventType: "turn.started",
				InputSource:    "adapter",
				NodeID:         "respond",
				Outputs:        map[string]any{"text": "hello"},
			}}); err != nil {
				t.Fatalf("stage output event: %v", err)
			}
			dir, err := a.flows.flowSessionDir("fl_session_test", session.SessionID)
			if err != nil {
				t.Fatalf("resolve FlowSession directory: %v", err)
			}
			summary, err := json.Marshal(rec)
			if err != nil {
				t.Fatalf("encode pending output checkpoint: %v", err)
			}
			if err := os.WriteFile(filepath.Join(dir, "summary.json"), summary, 0644); err != nil {
				t.Fatalf("write pending output checkpoint: %v", err)
			}
			if journalWritten {
				if err := appendFlowSessionEvent(filepath.Join(dir, "events.jsonl"), rec.PendingOutputEvents[0]); err != nil {
					t.Fatalf("append output event before recovery: %v", err)
				}
			}

			view, err := a.GetFlowSession("fl_session_test", session.SessionID)
			if err != nil || view.ProcessedSequence != 1 || view.LastSequence != 2 || view.State["committed"] != true {
				t.Fatalf("recover output checkpoint: view=%+v err=%v", view, err)
			}
			events, err := a.FlowSessionEvents("fl_session_test", session.SessionID, 0, 10)
			if err != nil || len(events) != 2 || events[1].Type != FlowSessionOutputEventType {
				t.Fatalf("expected exactly one recovered output event: events=%+v err=%v", events, err)
			}
			var payload struct {
				OutputID string `json:"output_id"`
			}
			if err := json.Unmarshal(events[1].Payload, &payload); err != nil || payload.OutputID != "fso_recovery" {
				t.Fatalf("unexpected recovered output identity: payload=%+v err=%v", payload, err)
			}
		})
	}
}

func TestFlowSessionRecoversTerminalEventAheadOfSnapshot(t *testing.T) {
	for _, tc := range []struct {
		eventType string
		status    string
	}{
		{eventType: "session.completed", status: "completed"},
		{eventType: "session.canceled", status: "canceled"},
	} {
		t.Run(tc.status, func(t *testing.T) {
			cfg := testFlowConfig(t.TempDir())
			a1 := New(cfg)
			if _, err := a1.CreateFlow(FlowCreateArgs{Def: sessionTestDefinition()}); err != nil {
				t.Fatalf("create session flow: %v", err)
			}
			session, err := a1.CreateFlowSession("fl_session_test", "1", nil)
			if err != nil {
				t.Fatalf("create FlowSession: %v", err)
			}
			dir, err := a1.flows.flowSessionDir("fl_session_test", session.SessionID)
			if err != nil {
				t.Fatalf("session dir: %v", err)
			}
			endedAt := session.StartedAt.Add(time.Second)
			event := FlowSessionEvent{
				SessionID:    session.SessionID,
				FlowID:       "fl_session_test",
				Version:      session.Version,
				Digest:       session.Digest,
				Source:       "godex",
				Sequence:     1,
				StateVersion: 1,
				Type:         tc.eventType,
				ReceivedAt:   endedAt,
				Payload:      json.RawMessage(`{}`),
			}
			if err := appendFlowSessionEvent(filepath.Join(dir, "events.jsonl"), event); err != nil {
				t.Fatalf("append terminal event before checkpoint: %v", err)
			}

			a2 := New(cfg)
			view, err := a2.GetFlowSession("fl_session_test", session.SessionID)
			if err != nil {
				t.Fatalf("recover terminal snapshot: %v", err)
			}
			if view.Status != tc.status || view.LastSequence != 1 || view.ProcessedSequence != 1 || !view.EndedAt.Equal(endedAt) {
				t.Fatalf("terminal event recovery mismatch: got %+v", view)
			}
		})
	}
}
