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
