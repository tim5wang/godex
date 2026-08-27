package taskboard

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/tim5wang/godex/internal/pluginrt"
)

// fakeExecutor closes the execution loop synchronously: record start, then
// finish as completed (standing in for the durable-subagent adapter).
type fakeExecutor struct {
	ledger *Ledger
	lastID string
}

func (f *fakeExecutor) Execute(ctx context.Context, card Card) (string, string, error) {
	executionID := fmt.Sprintf("ex-%s", card.ID)
	sessionID := "session-fake"
	if _, err := f.ledger.StartExecution(card.ID, executionID, sessionID, "taskboard"); err != nil {
		return "", "", err
	}
	if _, err := f.ledger.FinishExecution(card.ID, executionID, ExecutionCompleted, "fake run finished"); err != nil {
		return "", "", err
	}
	f.lastID = card.ID
	return executionID, sessionID, nil
}

func newTestPlugin(t *testing.T) (*Plugin, *Ledger) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "taskboard", "ledger.json")
	ledger, err := OpenLedger(path, t.TempDir())
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	executor := &fakeExecutor{ledger: ledger}
	return NewPlugin(ledger, executor, nil), ledger
}

func call(t *testing.T, mux *http.ServeMux, method, target string, body any) map[string]any {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, target, reader)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code >= 400 {
		t.Fatalf("%s %s: status %d body %s", method, target, rec.Code, rec.Body.String())
	}
	var parsed map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("%s %s: non-JSON %q: %v", method, target, rec.Body.String(), err)
	}
	return parsed
}

func TestPluginHTTPSurfaceEndToEnd(t *testing.T) {
	plugin, _ := newTestPlugin(t)
	manager := pluginrt.NewManager(nil)
	if _, err := manager.Activate(context.Background(), plugin); err != nil {
		t.Fatalf("activate: %v", err)
	}
	root := http.NewServeMux()
	manager.MountRoutes(root)

	// projects: built-in default present
	projects := call(t, root, "GET", "/v1/taskboard/projects", nil)["projects"].([]any)
	if len(projects) != 1 {
		t.Fatalf("expected built-in default project, got %v", projects)
	}
	projectID := projects[0].(map[string]any)["id"].(string)

	// create card
	card := call(t, root, "POST", "/v1/taskboard/cards", map[string]any{
		"project_id": projectID, "title": "http card", "urgency": "urgent",
		"checklist": []string{"criterion-1"},
	})["card"].(map[string]any)
	cardID := card["id"].(string)
	version := int(card["version"].(float64))
	if version != 1 {
		t.Fatalf("expected version 1, got %d", version)
	}

	// move backlog -> todo
	patched := call(t, root, "PATCH", "/v1/taskboard/cards/"+cardID, map[string]any{
		"action": "move", "version": version, "to": StatusTodo,
	})["card"].(map[string]any)
	version = int(patched["version"].(float64))

	// execute (fake executor closes the loop)
	exec := call(t, root, "POST", "/v1/taskboard/cards/"+cardID+"/execute", nil)
	if exec["session_id"] != "session-fake" {
		t.Fatalf("unexpected execute result: %v", exec)
	}

	// card shows execution record (execution bumps version twice: start + finish)
	detail := call(t, root, "GET", "/v1/taskboard/cards/"+cardID, nil)["card"].(map[string]any)
	executions := detail["executions"].([]any)
	if len(executions) != 1 || executions[0].(map[string]any)["status"] != ExecutionCompleted {
		t.Fatalf("expected one completed execution, got %v", executions)
	}
	version = int(detail["version"].(float64))

	// human acceptance from in_review
	patched = call(t, root, "PATCH", "/v1/taskboard/cards/"+cardID, map[string]any{
		"action": "move", "version": version, "to": StatusInProgress,
	})["card"].(map[string]any)
	version = int(patched["version"].(float64))
	patched = call(t, root, "PATCH", "/v1/taskboard/cards/"+cardID, map[string]any{
		"action": "move", "version": version, "to": StatusInReview,
	})["card"].(map[string]any)
	version = int(patched["version"].(float64))
	done := call(t, root, "PATCH", "/v1/taskboard/cards/"+cardID, map[string]any{
		"action": "complete", "version": version, "force": true,
	})["card"].(map[string]any)
	if done["status"] != StatusDone {
		t.Fatalf("expected done via human acceptance, got %v", done["status"])
	}

	// soft delete
	call(t, root, "DELETE", "/v1/taskboard/cards/"+cardID, nil)
	remaining := call(t, root, "GET", "/v1/taskboard/cards", nil)["cards"].([]any)
	if len(remaining) != 0 {
		t.Fatalf("expected deleted card hidden, got %v", remaining)
	}

	// deactivate -> routes unmounted
	if err := manager.Deactivate(context.Background(), ManifestID); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	req := httptest.NewRequest("GET", "/v1/taskboard/cards", nil)
	rec := httptest.NewRecorder()
	root.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 after deactivation, got %d", rec.Code)
	}
}

func TestPluginExecuteGuards(t *testing.T) {
	plugin, _ := newTestPlugin(t)
	manager := pluginrt.NewManager(nil)
	if _, err := manager.Activate(context.Background(), plugin); err != nil {
		t.Fatalf("activate: %v", err)
	}
	root := http.NewServeMux()
	manager.MountRoutes(root)

	// executor nil path is covered by compilation; here: unknown card -> 404
	req := httptest.NewRequest("POST", "/v1/taskboard/cards/t-missing/execute", nil)
	rec := httptest.NewRecorder()
	root.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing card, got %d", rec.Code)
	}

	// ledger round-trip still intact through the plugin handle
	if got := len(plugin.Ledger().ListProjects()); got != 1 {
		t.Fatalf("expected built-in project via plugin ledger, got %d", got)
	}
}
