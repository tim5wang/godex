package taskboard

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// toolRunner is the callable face of a built taskboard tool (Tool.Execute
// returns the result JSON string).
type toolRunner interface {
	Name() string
	Execute(ctx context.Context, args map[string]interface{}) (string, error)
}

func toolsByName(t *testing.T) map[string]toolRunner {
	t.Helper()
	byName := map[string]toolRunner{}
	for _, tool := range NewTaskboardTools(openTestLedger(t)) {
		byName[tool.Name()] = tool.(toolRunner)
	}
	if len(byName) != 8 {
		t.Fatalf("expected 8 taskboard tools, got %d", len(byName))
	}
	return byName
}

func parseResult(t *testing.T, toolName string, out string) map[string]any {
	t.Helper()
	var parsed map[string]any
	if unmarshalErr := json.Unmarshal([]byte(out), &parsed); unmarshalErr != nil {
		t.Fatalf("%s: non-JSON result %q: %v", toolName, out, unmarshalErr)
	}
	return parsed
}

func resultVersion(t *testing.T, m map[string]any) int {
	t.Helper()
	return int(m["version"].(float64))
}

func TestTaskboardToolsEndToEnd(t *testing.T) {
	byName := toolsByName(t)
	ctx := context.Background()

	// create
	res := parseResult(t, "taskboard_create", mustExec(t, byName, "taskboard_create", map[string]interface{}{
		"title":     "tool card",
		"urgency":   "urgent",
		"checklist": []interface{}{"step-1"},
	}))
	card := res["card"].(map[string]any)
	cardID := card["id"].(string)
	version := int(res["version"].(float64))
	if cardID == "" || version != 1 {
		t.Fatalf("unexpected created card: %v", res)
	}

	// list
	res = parseResult(t, "taskboard_list", mustExec(t, byName, "taskboard_list", map[string]interface{}{}))
	if got := int(res["count"].(float64)); got < 1 {
		t.Fatalf("expected at least 1 card, got %v", res)
	}

	// get (full card: version lives inside card)
	res = parseResult(t, "taskboard_get", mustExec(t, byName, "taskboard_get", map[string]interface{}{"card_id": cardID}))
	version = int(res["card"].(map[string]any)["version"].(float64))

	// protocol gate through the tool: move to done is refused
	_, err := byName["taskboard_move"].Execute(ctx, map[string]interface{}{
		"card_id": cardID, "version": version, "to": StatusDone,
	})
	if err == nil || !strings.Contains(err.Error(), "human") {
		t.Fatalf("expected done gate through tool, got %v", err)
	}

	// legal move: backlog -> todo
	res = parseResult(t, "taskboard_move", mustExec(t, byName, "taskboard_move", map[string]interface{}{
		"card_id": cardID, "version": version, "to": StatusTodo,
	}))
	version = resultVersion(t, res)

	// update title
	res = parseResult(t, "taskboard_update", mustExec(t, byName, "taskboard_update", map[string]interface{}{
		"card_id": cardID, "version": version, "title": "tool card v2",
	}))
	version = resultVersion(t, res)

	// checklist add + check
	res = parseResult(t, "taskboard_checklist", mustExec(t, byName, "taskboard_checklist", map[string]interface{}{
		"card_id": cardID, "version": version, "action": "add", "text": "extra criterion",
	}))
	version = int(res["version"].(float64))
	res = parseResult(t, "taskboard_checklist", mustExec(t, byName, "taskboard_checklist", map[string]interface{}{
		"card_id": cardID, "version": version, "action": "check", "index": float64(0), "evidence": "proof note",
	}))
	if got := int(res["checklist_done"].(float64)); got != 1 {
		t.Fatalf("expected 1 checked item (index 0 only), got %v", res)
	}
	version = int(res["version"].(float64))

	// comment
	res = parseResult(t, "taskboard_comment_add", mustExec(t, byName, "taskboard_comment_add", map[string]interface{}{
		"card_id": cardID, "version": version, "text": "progress: on track",
	}))
	version = resultVersion(t, res)

	// delete
	res = parseResult(t, "taskboard_delete", mustExec(t, byName, "taskboard_delete", map[string]interface{}{
		"card_id": cardID, "version": version,
	}))
	if res["deleted"] != true {
		t.Fatalf("expected deleted flag, got %v", res)
	}
}

func mustExec(t *testing.T, byName map[string]toolRunner, name string, args map[string]interface{}) string {
	t.Helper()
	run, ok := byName[name]
	if !ok {
		t.Fatalf("tool %s not found", name)
	}
	out, err := run.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return out
}
