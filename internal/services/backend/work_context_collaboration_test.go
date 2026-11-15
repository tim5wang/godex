package backend

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/core/memory"
	"github.com/tim5wang/godex/internal/core/workcontext"
	"github.com/tim5wang/godex/internal/domain/message"
	"github.com/tim5wang/godex/internal/plugins/taskboard"
)

func TestWorkCollaborationDelegatesAndBindsWorkerBeforeFirstTurn(t *testing.T) {
	service := newTestService(newTestConfig(t), &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("worker result")}}}})
	store, err := service.WorkContexts()
	if err != nil {
		t.Fatal(err)
	}
	scope := workcontext.Scope{TenantID: "tenant-lead", OwnerID: "owner-lead"}
	ctx := memory.WithOwner(context.Background(), memory.Owner{TenantID: scope.TenantID, UserID: scope.OwnerID})
	stream, err := store.Create(ctx, scope, workcontext.ContextInput{Title: "leader desk", Goal: "short context"})
	if err != nil {
		t.Fatal(err)
	}
	lead, err := service.LinkWorkSession(ctx, scope, stream.ID, WorkSessionInput{Topic: "main"})
	if err != nil || lead.Role != "lead" {
		t.Fatal(lead, err)
	}
	tool := service.newWorkContextTool(lead.SessionID)
	foreignToolCtx := memory.WithOwner(context.Background(), memory.Owner{TenantID: scope.TenantID, UserID: "intruder"})
	if _, err := tool.Execute(foreignToolCtx, map[string]any{"action": "resources"}); err == nil {
		t.Fatal("foreign caller used a bound collaboration tool")
	}
	created, err := tool.Execute(ctx, map[string]any{"action": "delegate", "delegation": map[string]any{"request_id": "one-task", "title": "verify a bounded change", "description": "artifact reference, not transcript", "template_id": "coder", "acceptance": []string{"record one check"}}})
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		ID string `json:"delegation_id"`
	}
	if err := json.Unmarshal([]byte(created), &result); err != nil {
		t.Fatal(err)
	}
	did := result.ID
	dispatched, err := tool.Execute(ctx, map[string]any{"action": "dispatch", "delegation_id": did})
	if err != nil {
		t.Fatal(err)
	}
	var dispatchResult map[string]string
	if err := json.Unmarshal([]byte(dispatched), &dispatchResult); err != nil {
		t.Fatal(err)
	}
	sid := dispatchResult["session_id"]
	waitForBackendSnapshot(t, service, sid, func(s Snapshot) bool { return !s.Running })
	boundScope, boundID, err := store.SessionBinding(ctx, sid)
	if err != nil || boundScope != scope || boundID != stream.ID {
		t.Fatal("worker lost desktop", boundScope, boundID, err)
	}
	role, err := store.SessionRole(ctx, scope, stream.ID, sid)
	if err != nil || role != "worker" {
		t.Fatal(role, err)
	}
	worker, _ := service.requireSession(sid)
	if !sessionMemoryOwnerMatches(ctx, worker) || !worker.agent.WorkContextToolMounted() {
		t.Fatal("worker missing owner or desktop capability")
	}
	joined := ""
	for _, m := range worker.agent.GetMessages() {
		joined += protocol.MessageText(m)
	}
	if !strings.Contains(joined, "Persisted session role: worker") || !strings.Contains(joined, "leader desk") {
		t.Fatal("first worker turn did not receive its bound state")
	}
	workerTool := service.newWorkContextTool(sid)
	for _, action := range []string{"delegate", "dispatch", "checkpoint"} {
		if _, err := workerTool.Execute(ctx, map[string]any{"action": action, "delegation_id": did}); err == nil {
			t.Fatal("worker unexpectedly allowed", action)
		}
	}
	if _, err := tool.Execute(ctx, map[string]any{"action": "dispatch", "delegation_id": "another-desktop-task"}); err == nil {
		t.Fatal("cross-desktop task dispatch accepted")
	}
	view, err := service.WorkContextDetail(ctx, scope, stream.ID)
	if err != nil || len(view.Sessions) != 2 || len(view.Delegations) != 1 || view.Delegations[0].Card == nil {
		t.Fatal("desktop cannot show worker/task", err)
	}
	card := *view.Delegations[0].Card
	foreign := memory.WithOwner(context.Background(), memory.Owner{TenantID: scope.TenantID, UserID: "intruder"})
	if _, _, err := NewTaskboardExecutor(service, service.TaskboardLedger()).Execute(foreign, card); err == nil {
		t.Fatal("foreign owner executed desktop delegation")
	}
	// Changing the lead changes tool authority on the very next call.
	other, err := service.LinkWorkSession(ctx, scope, stream.ID, WorkSessionInput{Topic: "new lead"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetMainSession(ctx, scope, stream.ID, other.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Execute(ctx, map[string]any{"action": "delegate"}); err == nil {
		t.Fatal("old lead retained authority")
	}
}

func TestWorkResourcesCaptureOnlyUserLinksAndExposeAgentMaintenance(t *testing.T) {
	service := newTestService(newTestConfig(t), &stubCaller{})
	store, _ := service.WorkContexts()
	ctx := context.Background()
	scope := workcontext.Scope{OwnerID: "local-operator"}
	stream, err := store.Create(ctx, scope, workcontext.ContextInput{Title: "links"})
	if err != nil {
		t.Fatal(err)
	}
	link, err := service.LinkWorkSession(ctx, scope, stream.ID, WorkSessionInput{Topic: "main"})
	if err != nil {
		t.Fatal(err)
	}
	session, _ := service.requireSession(link.SessionID)
	service.refreshWorkContextFeedback(session)
	envelope := message.NewTextEnvelope(message.SourceWeb, session.id, "user", "参考 [论文](https://arxiv.org/abs/2611.05306v1)，以及 https://example.com/docs. 不要保存 https://example.com/?token=secret 或 https://user:pass@example.com/private", time.Now())
	service.captureWorkResources(session, envelope, "resource-turn")
	service.captureWorkResources(session, envelope, "resource-turn")
	service.captureWorkResources(session, message.NewTextEnvelope(message.SourceBackground, session.id, "agent", "https://ignored.example.com/", time.Now()), "background")
	detail, _ := store.Get(ctx, scope, stream.ID)
	if len(detail.Resources) != 2 {
		t.Fatalf("expected two deduplicated safe user links: %+v", detail.Resources)
	}
	for _, r := range detail.Resources {
		if !strings.Contains(r.Source, "resource-turn") {
			t.Fatal("missing provenance", r)
		}
	}
	tool := service.newWorkContextTool(session.id)
	if _, err := tool.Execute(ctx, map[string]any{"action": "resource_put", "resource": map[string]any{"title": "code", "kind": "directory", "locator": "/srv/project", "node_id": "node-a", "purpose": "implementation"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Execute(ctx, map[string]any{"action": "resource_put", "resource": map[string]any{"title": "unbound", "kind": "directory", "locator": "/srv/unknown"}}); err == nil {
		t.Fatal("unknown node accepted")
	}
	got, err := tool.Execute(ctx, map[string]any{"action": "resources"})
	var resources []workcontext.Resource
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(got), &resources); err != nil || len(resources) != 3 {
		t.Fatal(got, err)
	}
}

func TestWorkTurnEventHasTaskAndReportNotJustConversationTitle(t *testing.T) {
	service := newTestService(newTestConfig(t), &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("定位已完成；详见 report.txt")}}}})
	store, _ := service.WorkContexts()
	ctx := context.Background()
	scope := workcontext.Scope{OwnerID: "local-operator"}
	stream, err := store.Create(ctx, scope, workcontext.ContextInput{Title: "history"})
	if err != nil {
		t.Fatal(err)
	}
	link, err := service.LinkWorkSession(ctx, scope, stream.ID, WorkSessionInput{Topic: "proposal"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := service.Submit(ctx, link.SessionID, message.NewTextEnvelope(message.SourceWeb, link.SessionID, "user", "定位资源入口问题", time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	detail, _ := store.Get(ctx, scope, stream.ID)
	for _, event := range detail.Events {
		if event.Kind != workcontext.SessionTurnEventKind {
			continue
		}
		out, err := store.EventDetail(ctx, scope, stream.ID, event.ID)
		if err != nil || out.TurnID != res.TurnID || !strings.Contains(out.Request, "资源入口") || !strings.Contains(out.Result, "report.txt") || !strings.Contains(out.Title, "资源入口") {
			t.Fatal(out, err)
		}
		return
	}
	t.Fatal("turn event missing")
}

func TestTaskboardExecutorKeepsConflictGateForDesktopDispatch(t *testing.T) {
	service, executor, seeded := newTestTaskboardExecutor(t)
	active, err := executor.ledger.CreateCard(taskboard.CreateCardInput{ProjectID: seeded.ProjectID, Title: "active", StartStatus: taskboard.StatusTodo, TouchedPaths: []string{"internal/core/workcontext"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := executor.ledger.MoveCard(active.ID, active.Version, taskboard.StatusInProgress, "human"); err != nil {
		t.Fatal(err)
	}
	overlap, err := executor.ledger.CreateCard(taskboard.CreateCardInput{ProjectID: seeded.ProjectID, Title: "overlap", TouchedPaths: []string{"internal/core/workcontext"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := executor.Execute(context.Background(), overlap); !errors.Is(err, taskboard.ErrPathConflict) {
		t.Fatal("desktop dispatch bypassed conflict gate", err)
	}
	sessions, err := service.ListSessions(context.Background(), SessionListFilter{Channel: taskboardSessionChannel})
	if err != nil || len(sessions) != 0 {
		t.Fatal("blocked dispatch created an execution conversation", sessions, err)
	}
}

func TestWorkDelegationPromptUsesReferencesNotFullState(t *testing.T) {
	large := strings.Repeat("irrelevant worker detail ", 1000)
	pack := frozenWorkPack{Context: workcontext.Context{ID: "desk", Title: "work", Checkpoint: workcontext.Checkpoint{Summary: "current phase"}}, Facts: []workcontext.Fact{{Status: "confirmed", Content: large}}, Resources: []workcontext.Resource{{Locator: large}}, Input: WorkDelegationInput{Title: "bounded task", Acceptance: []string{"one check"}}}
	prompt := workPackPrompt(pack)
	if len(prompt) > 2500 || strings.Contains(prompt, large) || !strings.Contains(prompt, "workcontext://desk") || !strings.Contains(prompt, "不得自行 done") {
		t.Fatal("prompt copied full state or lost boundaries", len(prompt))
	}
}
