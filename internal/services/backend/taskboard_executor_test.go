package backend

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/tim5wang/godex/internal/core/protocol"
	"github.com/tim5wang/godex/internal/plugins/taskboard"
)

// newTestTaskboardExecutor wires a backend Service with a fresh taskboard
// ledger and the M3 executor.
func newTestTaskboardExecutor(t *testing.T) (*Service, *TaskboardExecutor, taskboard.Card) {
	t.Helper()
	cfg := newTestConfig(t)
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{
		{Content: []protocol.Block{protocol.TextBlock("ok")}},
		{Content: []protocol.Block{protocol.TextBlock("ok")}},
		{Content: []protocol.Block{protocol.TextBlock("ok")}},
		{Content: []protocol.Block{protocol.TextBlock("ok")}},
	}})
	ledger, err := taskboard.OpenLedger(filepath.Join(cfg.StateDir, "taskboard", "ledger.json"), cfg.WorkspaceDir)
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	executor := NewTaskboardExecutor(service, ledger)

	projects := ledger.ListProjects()
	if len(projects) == 0 {
		t.Fatal("expected a seeded built-in project")
	}
	card, err := ledger.CreateCard(taskboard.CreateCardInput{
		ProjectID:  projects[0].ID,
		Title:      "template-pinned task",
		TemplateID: "geek",
	})
	if err != nil {
		t.Fatalf("create card: %v", err)
	}
	return service, executor, card
}

func TestTaskboardExecutorOpensTemplatePinnedSession(t *testing.T) {
	service, executor, card := newTestTaskboardExecutor(t)

	executionID, sessionID, err := executor.Execute(context.Background(), card)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if executionID == "" || sessionID == "" {
		t.Fatalf("expected execution and session ids, got %q %q", executionID, sessionID)
	}
	// The submitted turn runs in a background goroutine; wait for it to finish
	// so the tempdir is not torn down mid-write (checkpoint files).
	waitForBackendSnapshot(t, service, sessionID, func(snapshot Snapshot) bool {
		return !snapshot.Running
	})

	// The execution is recorded as running with host = the execution session.
	got, err := executor.ledger.GetCard(card.ID)
	if err != nil {
		t.Fatalf("get card: %v", err)
	}
	if len(got.Executions) != 1 {
		t.Fatalf("expected 1 execution, got %d", len(got.Executions))
	}
	ex := got.Executions[0]
	if ex.Status != taskboard.ExecutionRunning {
		t.Fatalf("expected running execution, got %q", ex.Status)
	}
	if ex.Host == nil || ex.Host.SessionID != sessionID {
		t.Fatalf("expected host session %q, got %+v", sessionID, ex.Host)
	}
	if ex.Host == nil || ex.Host.Channel != taskboardSessionChannel {
		t.Fatalf("expected host channel %q, got %+v", taskboardSessionChannel, ex.Host)
	}

	// The execution session is pinned to the card's template via locator
	// metadata: reopening the same locator yields the same session id.
	projects := executor.ledger.ListProjects()
	rootDir := ""
	for _, p := range projects {
		if p.ID == card.ProjectID {
			rootDir = p.RootDir
			break
		}
	}
	reopened, err := service.OpenSession(context.Background(), SessionLocator{
		Channel: taskboardSessionChannel,
		Key:     "card-" + card.ID,
		Metadata: map[string]string{
			sessionProjectDirMetadataKey: rootDir,
			"template":                   card.TemplateID,
		},
	})
	if err != nil {
		t.Fatalf("reopen session: %v", err)
	}
	if reopened.SessionID != sessionID {
		t.Fatalf("expected same session id on reopen, got %q != %q", reopened.SessionID, sessionID)
	}
}

func TestTaskboardExecutorReusesSessionPerCard(t *testing.T) {
	_, executor, card := newTestTaskboardExecutor(t)

	exec1, session1, err := executor.Execute(context.Background(), card)
	if err != nil {
		t.Fatalf("execute #1: %v", err)
	}
	// The ledger protocol gate forbids a second execution while the first is
	// still running; finish it to release the card before the next run.
	if _, err := executor.ledger.FinishExecution(card.ID, exec1, taskboard.ExecutionCompleted, "done"); err != nil {
		t.Fatalf("finish #1: %v", err)
	}
	// Let the first submitted turn finish before starting the second run.
	waitForBackendSnapshot(t, executor.service, session1, func(snapshot Snapshot) bool {
		return !snapshot.Running
	})
	exec2, session2, err := executor.Execute(context.Background(), card)
	if err != nil {
		t.Fatalf("execute #2: %v", err)
	}
	// Same card reuses one conversation; each run gets its own execution id.
	if session2 != session1 {
		t.Fatalf("expected same session per card, got %q != %q", session2, session1)
	}
	if exec2 == exec1 {
		t.Fatalf("expected distinct execution ids per run, both %q", exec1)
	}

	// A second card gets its own session.
	projects := executor.ledger.ListProjects()
	other, err := executor.ledger.CreateCard(taskboard.CreateCardInput{
		ProjectID: projects[0].ID,
		Title:     "another card",
	})
	if err != nil {
		t.Fatalf("create second card: %v", err)
	}
	_, session3, err := executor.Execute(context.Background(), other)
	if err != nil {
		t.Fatalf("execute other card: %v", err)
	}
	if session3 == session1 {
		t.Fatalf("expected distinct session per card, got %q", session3)
	}
	// Let the final submitted turn finish before the tempdir is torn down.
	waitForBackendSnapshot(t, executor.service, session3, func(snapshot Snapshot) bool {
		return !snapshot.Running
	})
}
