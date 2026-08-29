package taskboard

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// toolRunner is the callable face of the taskboard tool (Tool.Execute
// returns the result JSON string).
type toolRunner interface {
	Name() string
	Execute(ctx context.Context, args map[string]interface{}) (string, error)
}

func newTool(t *testing.T) toolRunner {
	t.Helper()
	runner, ok := NewTaskboardTool(openTestLedger(t)).(toolRunner)
	if !ok {
		t.Fatalf("tool does not implement Execute")
	}
	if runner.Name() != "taskboard" {
		t.Fatalf("expected single taskboard tool, got %q", runner.Name())
	}
	return runner
}

func parseResult(t *testing.T, out string) map[string]any {
	t.Helper()
	var parsed map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("non-JSON result %q: %v", out, err)
	}
	return parsed
}

func mustExec(t *testing.T, tool toolRunner, args map[string]interface{}) map[string]any {
	t.Helper()
	out, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("%v: %v", args["action"], err)
	}
	return parseResult(t, out)
}

func resultVersion(m map[string]any) int { return int(m["version"].(float64)) }

func TestTaskboardToolEndToEnd(t *testing.T) {
	tool := newTool(t)

	// create
	res := mustExec(t, tool, map[string]interface{}{
		"action":      "create",
		"title":       "tool card",
		"urgency":     "urgent",
		"checklist":   []interface{}{"step-1"},
		"prompt":      "do the thing",
		"template_id": "geek",
	})
	card := res["card"].(map[string]any)
	cardID := card["id"].(string)
	version := resultVersion(res)
	if cardID == "" || version != 1 || card["title"] != "tool card" {
		t.Fatalf("unexpected created card: %v", res)
	}
	if card["template_id"] != "geek" {
		t.Fatalf("expected template_id geek through create, got %v", card["template_id"])
	}

	// create without title is refused
	if _, err := tool.Execute(context.Background(), map[string]interface{}{"action": "create"}); err == nil {
		t.Fatalf("expected create without title to fail")
	}

	// list
	res = mustExec(t, tool, map[string]interface{}{"action": "list"})
	if got := int(res["count"].(float64)); got < 1 {
		t.Fatalf("expected at least 1 card, got %v", res)
	}

	// get (full card: version lives inside card)
	res = mustExec(t, tool, map[string]interface{}{"action": "get", "card_id": cardID})
	version = int(res["card"].(map[string]any)["version"].(float64))

	// writes without version are refused
	if _, err := tool.Execute(context.Background(), map[string]interface{}{"action": "move", "card_id": cardID, "to": "todo"}); err == nil {
		t.Fatalf("expected move without version to fail")
	}

	// protocol gate through the tool: move to done is refused
	_, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "move", "card_id": cardID, "version": version, "to": StatusDone,
	})
	if err == nil || !strings.Contains(err.Error(), "human") {
		t.Fatalf("expected done gate through tool, got %v", err)
	}

	// legal move: backlog -> todo
	res = mustExec(t, tool, map[string]interface{}{
		"action": "move", "card_id": cardID, "version": version, "to": StatusTodo,
	})
	version = resultVersion(res)

	// update title + template_id
	res = mustExec(t, tool, map[string]interface{}{
		"action": "update", "card_id": cardID, "version": version, "title": "tool card v2", "template_id": "reviewer",
	})
	version = resultVersion(res)
	if got := res["card"].(map[string]any)["template_id"]; got != "reviewer" {
		t.Fatalf("expected template_id reviewer after update, got %v", got)
	}

	// checklist add + check
	res = mustExec(t, tool, map[string]interface{}{
		"action": "checklist", "card_id": cardID, "version": version, "check_action": "add", "text": "extra criterion",
	})
	version = resultVersion(res)
	res = mustExec(t, tool, map[string]interface{}{
		"action": "checklist", "card_id": cardID, "version": version, "check_action": "check", "index": float64(0), "evidence": "proof note",
	})
	if got := int(res["checklist_done"].(float64)); got != 1 {
		t.Fatalf("expected 1 checked item, got %v", res)
	}
	version = resultVersion(res)

	// comment
	res = mustExec(t, tool, map[string]interface{}{
		"action": "comment_add", "card_id": cardID, "version": version, "text": "progress: on track",
	})
	version = resultVersion(res)

	// delete
	res = mustExec(t, tool, map[string]interface{}{
		"action": "delete", "card_id": cardID, "version": version,
	})
	if res["deleted"] != true {
		t.Fatalf("expected deleted flag, got %v", res)
	}

	// unknown action is refused
	if _, err := tool.Execute(context.Background(), map[string]interface{}{"action": "nope"}); err == nil {
		t.Fatalf("expected unknown action to fail")
	}
}

func TestTaskboardToolDispatch(t *testing.T) {
	ledger := openTestLedger(t)
	exec := &fakeExecutor{ledger: ledger}
	runner, ok := NewTaskboardToolWithExecutor(ledger, exec).(toolRunner)
	if !ok {
		t.Fatalf("tool does not implement Execute")
	}

	card, err := ledger.CreateCard(CreateCardInput{ProjectID: ledger.ListProjects()[0].ID, Title: "dispatch me"})
	if err != nil {
		t.Fatalf("create card: %v", err)
	}

	// dispatch without a configured executor is a clear error
	bare, ok := NewTaskboardTool(openTestLedger(t)).(toolRunner)
	if !ok {
		t.Fatalf("bare tool does not implement Execute")
	}
	if _, err := bare.Execute(context.Background(), map[string]interface{}{"action": "dispatch", "card_id": card.ID}); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("expected unavailable error without executor, got %v", err)
	}

	// dispatch with executor returns execution/session ids and records the run
	res := mustExec(t, runner, map[string]interface{}{"action": "dispatch", "card_id": card.ID})
	if res["execution_id"] != "ex-"+card.ID {
		t.Fatalf("unexpected execution_id %v", res["execution_id"])
	}
	if res["session_id"] != "session-fake" {
		t.Fatalf("unexpected session_id %v", res["session_id"])
	}
	if exec.lastID != card.ID {
		t.Fatalf("expected executor to run card %q, got %q", card.ID, exec.lastID)
	}
	got, err := ledger.GetCard(card.ID)
	if err != nil {
		t.Fatalf("get card: %v", err)
	}
	if len(got.Executions) != 1 || got.Executions[0].Status != ExecutionCompleted {
		t.Fatalf("expected 1 completed execution, got %+v", got.Executions)
	}
}

func TestTaskboardChecklistBatchAdd(t *testing.T) {
	tool := newTool(t)
	res := mustExec(t, tool, map[string]interface{}{
		"action":    "create",
		"title":     "batch card",
		"checklist": []interface{}{"a", "b", "c"},
	})
	card := res["card"].(map[string]any)
	if got := int(card["checklist_total"].(float64)); got != 3 {
		t.Fatalf("expected 3 checklist items at create, got %v", res)
	}
	version := resultVersion(res)

	// add several items in ONE call via the checklist array (batch)
	res = mustExec(t, tool, map[string]interface{}{
		"action": "checklist", "card_id": card["id"].(string), "version": version,
		"check_action": "add", "checklist": []interface{}{"d", "e", "f"},
	})
	if got := int(res["checklist_total"].(float64)); got != 6 {
		t.Fatalf("expected 6 items after batch add, got %v", res)
	}
	version = resultVersion(res)

	// legacy single-text add still works
	res = mustExec(t, tool, map[string]interface{}{
		"action": "checklist", "card_id": card["id"].(string), "version": version,
		"check_action": "add", "text": "single",
	})
	if got := int(res["checklist_total"].(float64)); got != 7 {
		t.Fatalf("expected 7 items after single add, got %v", res)
	}
}

func TestTaskboardVersionConflictRetry(t *testing.T) {
	ledger := openTestLedger(t)
	runner, ok := NewTaskboardTool(ledger).(toolRunner)
	if !ok {
		t.Fatalf("tool does not implement Execute")
	}
	card, err := ledger.CreateCard(CreateCardInput{ProjectID: ledger.ListProjects()[0].ID, Title: "concurrent card"})
	if err != nil {
		t.Fatalf("create card: %v", err)
	}

	// Simulate a concurrent write bumping the version to v2 in the background.
	if _, err := ledger.AddComment(card.ID, card.Version, "other", "bump"); err != nil {
		t.Fatalf("bump: %v", err)
	}

	// Caller still holds the stale v1; the tool must auto-re-read and retry once.
	res := mustExec(t, runner, map[string]interface{}{
		"action": "comment_add", "card_id": card.ID, "version": card.Version, "text": "new comment",
	})
	if got := resultVersion(res); got != 3 {
		t.Fatalf("expected auto-retry to land at v3, got %v", res)
	}
	got, err := ledger.GetCard(card.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got.Comments) != 2 {
		t.Fatalf("expected both comments persisted, got %+v", got.Comments)
	}
}

func TestTaskboardVersionConflictMessage(t *testing.T) {
	ledger := openTestLedger(t)
	card, err := ledger.CreateCard(CreateCardInput{ProjectID: ledger.ListProjects()[0].ID, Title: "conflict msg card"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := ledger.AddComment(card.ID, card.Version, "other", "bump"); err != nil {
		t.Fatalf("bump: %v", err)
	}
	// Bypass retry: call the ledger directly with a stale version.
	_, err = ledger.AddComment(card.ID, card.Version, "agent", "stale")
	if err == nil || !strings.Contains(err.Error(), "re-read the card then retry") {
		t.Fatalf("expected retry hint in conflict error, got %v", err)
	}
}

// observedStub is an ObservedExecutor that records the observability/recovery
// calls for tool-level assertion without a real session.
type observedStub struct {
	fakeExecutor
	lastObserveCard  string
	lastObserveExec  string
	lastRecoverCard  string
	lastRecoverExec  string
	lastRecoverText  string
	lastRetryCard    string
	lastRetryExec    string
	reconcileCalls   int
	returnedObs      ExecutionObservation
	returnedLive     bool
	returnedSession  string
	returnedTurn     string
}

func (s *observedStub) Observe(ctx context.Context, cardID, executionID string) (ExecutionObservation, bool, error) {
	s.lastObserveCard, s.lastObserveExec = cardID, executionID
	return s.returnedObs, s.returnedLive, nil
}

func (s *observedStub) Reconcile(ctx context.Context) (ReconcileReport, error) {
	s.reconcileCalls++
	return ReconcileReport{Scanned: 2, Observed: 1, Finalized: 1}, nil
}

func (s *observedStub) Recover(ctx context.Context, cardID, executionID, text string) (string, error) {
	s.lastRecoverCard, s.lastRecoverExec, s.lastRecoverText = cardID, executionID, text
	return s.returnedSession, nil
}

func (s *observedStub) Retry(ctx context.Context, cardID, executionID string) (string, error) {
	s.lastRetryCard, s.lastRetryExec = cardID, executionID
	return s.returnedTurn, nil
}

func TestTaskboardToolObservabilityActions(t *testing.T) {
	ledger := openTestLedger(t)
	stub := &observedStub{returnedObs: ExecutionObservation{Stage: StageWaitingApproval}, returnedLive: true, returnedSession: "session-1", returnedTurn: "turn-9"}
	runner, ok := NewTaskboardToolWithExecutor(ledger, stub).(toolRunner)
	if !ok {
		t.Fatalf("tool does not implement Execute")
	}
	card, err := ledger.CreateCard(CreateCardInput{ProjectID: ledger.ListProjects()[0].ID, Title: "obs card"})
	if err != nil {
		t.Fatalf("create card: %v", err)
	}
	if _, err := ledger.StartExecution(card.ID, "ex-1", "session-1", "session-1", nil); err != nil {
		t.Fatalf("start execution: %v", err)
	}

	// observe returns the snapshot and live flag.
	res := mustExec(t, runner, map[string]interface{}{"action": "observe", "card_id": card.ID, "execution_id": "ex-1"})
	if stub.lastObserveCard != card.ID || stub.lastObserveExec != "ex-1" {
		t.Fatalf("observe not forwarded: %+v", stub)
	}
	if res["live"] != true {
		t.Fatalf("expected live=true, got %v", res)
	}

	// reconcile returns the report.
	res = mustExec(t, runner, map[string]interface{}{"action": "reconcile"})
	report := res["reconcile_report"].(map[string]any)
	if int(report["finalized"].(float64)) != 1 || stub.reconcileCalls != 1 {
		t.Fatalf("unexpected reconcile result %v", res)
	}

	// recover forwards the message text.
	res = mustExec(t, runner, map[string]interface{}{"action": "recover", "card_id": card.ID, "execution_id": "ex-1", "text": "重试一下"})
	if stub.lastRecoverText != "重试一下" || res["session_id"] != "session-1" {
		t.Fatalf("recover not forwarded: %+v", stub)
	}

	// retry forverts the execution id and returns the new turn.
	res = mustExec(t, runner, map[string]interface{}{"action": "retry", "card_id": card.ID, "execution_id": "ex-1"})
	if stub.lastRetryExec != "ex-1" || res["turn_id"] != "turn-9" {
		t.Fatalf("retry not forwarded: %+v", stub)
	}
}

// A taskboard tool WITHOUT an observed executor refuses observe/recover/retry
// with a clear "does not support observability" error (never panics).
func TestTaskboardToolObservabilityRequiresObservedExecutor(t *testing.T) {
	ledger := openTestLedger(t)
	fake := &fakeExecutor{ledger: ledger}
	runner, ok := NewTaskboardToolWithExecutor(ledger, fake).(toolRunner)
	if !ok {
		t.Fatalf("tool does not implement Execute")
	}
	card, err := ledger.CreateCard(CreateCardInput{ProjectID: ledger.ListProjects()[0].ID, Title: "no obs"})
	if err != nil {
		t.Fatalf("create card: %v", err)
	}
	if _, err := ledger.StartExecution(card.ID, "ex-1", "session-1", "session-1", nil); err != nil {
		t.Fatalf("start execution: %v", err)
	}
	if _, err := runner.Execute(context.Background(), map[string]interface{}{"action": "observe", "card_id": card.ID, "execution_id": "ex-1"}); err == nil || !strings.Contains(err.Error(), "observability") {
		t.Fatalf("expected observability-unavailable error, got %v", err)
	}
}
