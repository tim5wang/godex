package backend

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/contracts/protocol"
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
		TemplateID: "coder",
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
			if len(p.WorkDirs) > 0 {
				rootDir = p.WorkDirs[0]
			}
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

func TestTaskboardExecutorRejectsUnknownTemplate(t *testing.T) {
	_, executor, card := newTestTaskboardExecutor(t)
	card.TemplateID = "missing-template"
	if _, _, err := executor.Execute(context.Background(), card); err == nil || !strings.Contains(err.Error(), "missing-template") {
		t.Fatalf("expected unknown template error, got %v", err)
	}
}

func TestTaskboardExecutorSessionTitleUsesCardTitle(t *testing.T) {
	service, executor, card := newTestTaskboardExecutor(t)

	executionID, sessionID, err := executor.Execute(context.Background(), card)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	waitForBackendSnapshot(t, service, sessionID, func(snapshot Snapshot) bool {
		return !snapshot.Running
	})

	// The execution session's conversation title should reflect the card title,
	// not the long claim-and-execute workload prompt.
	sessions, err := service.ListSessions(context.Background(), SessionListFilter{Channel: taskboardSessionChannel})
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	var found *ListedSession
	for i := range sessions {
		if sessions[i].SessionID == sessionID {
			found = &sessions[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("execution session %s not listed", sessionID)
	}
	if found.Title != card.Title {
		t.Fatalf("expected execution session title %q, got %q", card.Title, found.Title)
	}
	if strings.HasPrefix(found.Title, "任务看板") {
		t.Fatalf("execution session title must not leak the workload prompt, got %q", found.Title)
	}
	if _, err := executor.ledger.FinishExecution(card.ID, executionID, taskboard.ExecutionCompleted, "done"); err != nil {
		t.Fatalf("finish execution: %v", err)
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

// Observe inspects the running execution's session and writes the observation
// snapshot back into the ledger (stage / error / last tool), so the board
// reflects where the run is stuck without opening the conversation.
func TestTaskboardExecutorObserveWritesObservation(t *testing.T) {
	service, executor, card := newTestTaskboardExecutor(t)

	executionID, sessionID, err := executor.Execute(context.Background(), card)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	// Let the submitted turn finish so the session is no longer running.
	waitForBackendSnapshot(t, service, sessionID, func(snapshot Snapshot) bool {
		return !snapshot.Running
	})

	obs, live, err := executor.Observe(context.Background(), card.ID, executionID)
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	// The stub turn finished, so it is not live; the observation should still be
	// non-empty and written back to the ledger.
	if live {
		t.Fatalf("expected stub turn to be finished (not live)")
	}
	if obs.Stage == "" {
		t.Fatalf("expected a stage from observe, got empty: %+v", obs)
	}
	got, err := executor.ledger.GetCard(card.ID)
	if err != nil {
		t.Fatalf("get card: %v", err)
	}
	if got.Executions[0].Stage == "" {
		t.Fatalf("expected observation written to ledger, got %+v", got.Executions[0])
	}
}

// Recover appends a recovery message into the card's execution session so a
// running/stalled task can be nudged back (break a thinking loop, apply new
// params, give a fresh instruction). The message must land in the session.
func TestTaskboardExecutorRecoverAppendsMessage(t *testing.T) {
	service, executor, card := newTestTaskboardExecutor(t)

	executionID, sessionID, err := executor.Execute(context.Background(), card)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if _, err := executor.ledger.FinishExecution(card.ID, executionID, taskboard.ExecutionCompleted, "done"); err != nil {
		t.Fatalf("finish: %v", err)
	}
	waitForBackendSnapshot(t, service, sessionID, func(snapshot Snapshot) bool {
		return !snapshot.Running
	})

	// Recover into the (now idle) execution session.
	dest, err := executor.Recover(context.Background(), card.ID, executionID, "调高输出阈值后重试")
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if dest != sessionID {
		t.Fatalf("expected recovery delivered to %q, got %q", sessionID, dest)
	}

	// The added message should appear in the session feed (search by literal text;
	// recovery envelopes are background-sourced but may not carry KindBackground).
	waitForBackendSnapshot(t, service, sessionID, func(snapshot Snapshot) bool {
		return strings.Contains(protocolMessagesText(snapshot.Messages), "调高输出阈值后重试")
	})
	// Wait for the recovery round to finish writing checkpoint files before the
	// tempdir is torn down (SubmitAsync runs the turn on a background goroutine), so
	// the teardown does not race the async persist.
	waitForBackendSnapshot(t, service, sessionID, func(snapshot Snapshot) bool {
		return !snapshot.Running
	})
}

// protocolMessagesText concatenates the text of protocol messages for assertion.
func protocolMessagesText(messages []protocol.Message) string {
	var b strings.Builder
	for _, msg := range messages {
		b.WriteString(protocol.MessageText(msg))
		b.WriteString("\n")
	}
	return b.String()
}

// TestExecutionPromptResearchSplit verifies 方案A 上下文传递: a card carrying
// structured research is injected into the execution prompt as two clearly
// split sections — verified (trust, don't re-investigate) vs open points
// (verify yourself). This ensures the coder only verifies open questions
// instead of re-doing the PJM/planner调研.
func TestExecutionPromptResearchSplit(t *testing.T) {
	card := taskboard.Card{
		ID:    "t-x",
		Title: "task",
		Research: &taskboard.Research{
			Facts:         []string{"已确认 Card 模型在 internal/plugins/taskboard/types.go"},
			Locations:     []string{"internal/plugins/taskboard/types.go:141"},
			ExcludedPaths: []string{"internal/tools"},
			OpenQuestions: []string{"确认 UpdateCard 是否同步写入 research 字段"},
		},
	}
	prompt := executionPrompt(card, "/workspace")

	for _, want := range []string{"### 已由 PJM 验证（不必重复排查）", "### 执行时需自行验证的开放点"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing section %q\n%s", want, prompt)
		}
	}
	for _, want := range []string{
		"已确认 Card 模型在 internal/plugins/taskboard/types.go",
		"internal/plugins/taskboard/types.go:141",
		"internal/tools",
		"确认 UpdateCard 是否同步写入 research 字段",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing research detail %q\n%s", want, prompt)
		}
	}
}

// TestExecutionPromptWithoutResearch verifies a card with no research does not
// emit the (misleading) verified/open-points sections — so the executor never
// sees a stale/unpopulated block on cards that were not pre-investigated.
func TestExecutionPromptWithoutResearch(t *testing.T) {
	prompt := executionPrompt(taskboard.Card{ID: "t-y", Title: "plain"}, "")
	if strings.Contains(prompt, "### 已由 PJM 验证") || strings.Contains(prompt, "### 执行时需自行验证的开放点") {
		t.Errorf("prompt should not show research sections on a card without research:\n%s", prompt)
	}
}
