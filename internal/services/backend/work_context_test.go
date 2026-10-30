package backend

import (
	"context"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/core/workcontext"
	"github.com/tim5wang/godex/internal/plugins/taskboard"
)

func TestWorkContextDelegationRecoveryAndKnowledgeLoop(t *testing.T) {
	cfg := newTestConfig(t)
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("ok")}}}})
	store, err := service.WorkContexts()
	if err != nil {
		t.Fatal(err)
	}
	defer service.CloseWorkContexts()
	ctx := context.Background()
	scope := workcontext.Scope{OwnerID: "local-operator"}
	stream, err := store.Create(ctx, scope, workcontext.ContextInput{Title: "真实闭环", Background: "跨任务可恢复", Goal: "证据驱动"})
	if err != nil {
		t.Fatal(err)
	}
	stream, err = store.SaveCheckpoint(ctx, scope, stream.ID, stream.Version, workcontext.Checkpoint{Summary: "等待回归", NextActions: []string{"跑固定评测"}})
	if err != nil {
		t.Fatal(err)
	}
	ledger := service.TaskboardLedger()
	if ledger == nil {
		t.Fatal("ledger missing")
	}
	project := ledger.ListProjects()[0]
	input := WorkDelegationInput{RequestID: "request-one", Title: "验证并回报", Acceptance: []string{"提交可运行验证证据"}, ProjectID: project.ID}
	first, err := service.DelegateWork(ctx, scope, stream.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := service.DelegateWork(ctx, scope, stream.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if first.Card.ID != retry.Card.ID {
		t.Fatal("retry duplicated card")
	}
	input.Title = "不同任务"
	if _, err := service.DelegateWork(ctx, scope, stream.ID, input); err == nil {
		t.Fatal("request reuse accepted different input")
	}
	if !strings.Contains(first.Card.Prompt, "等待回归") || !strings.Contains(first.Card.Prompt, "不得自行 done") {
		t.Fatal("context or handoff rules missing")
	}
	card, err := ledger.MoveCard(first.Card.ID, first.Card.Version, taskboard.StatusInProgress, "human")
	if err != nil {
		t.Fatal(err)
	}
	card, err = ledger.MoveCard(card.ID, card.Version, taskboard.StatusInReview, "human")
	if err != nil {
		t.Fatal(err)
	}
	// No evidence means no fake knowledge.
	if _, err := service.CaptureWorkResult(ctx, scope, stream.ID, first.Delegation.ID); err == nil {
		t.Fatal("captured unverified result")
	}
	card, err = ledger.ChecklistCheck(card.ID, card.Version, "human", 0, "go test ./... passed; report://unit/1")
	if err != nil {
		t.Fatal(err)
	}
	fact, err := service.CaptureWorkResult(ctx, scope, stream.ID, first.Delegation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fact.Status != "candidate" {
		t.Fatal("result bypassed review")
	}
	again, err := service.CaptureWorkResult(ctx, scope, stream.ID, first.Delegation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != fact.ID {
		t.Fatal("duplicate result capture")
	}
	fact, err = store.ReviewFact(ctx, scope, stream.ID, fact.ID, workcontext.FactReview{IfVersion: fact.Version, Status: "confirmed"})
	if err != nil {
		t.Fatal(err)
	}
	fact, err = service.ProposeWorkMemory(ctx, scope, stream.ID, fact.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fact.MemoryCandidateID == "" {
		t.Fatal("memory proposal absent")
	}
	pending, err := service.ListMemoryCandidates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) == 0 {
		t.Fatal("existing candidate inbox not used")
	}
	view, err := service.WorkContextDetail(ctx, scope, stream.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Delegations) != 1 || view.Delegations[0].Card == nil || view.Delegations[0].Card.Status != "in_review" {
		t.Fatal("live task projection missing")
	}
	if _, err := store.Get(ctx, workcontext.Scope{OwnerID: "other"}, stream.ID); err == nil {
		t.Fatal("cross-owner leak")
	}
}

func TestWorkContextSessionFeedbackRefreshesWithoutChangingIdentity(t *testing.T) {
	cfg := newTestConfig(t)
	service := newTestService(cfg, &stubCaller{})
	store, err := service.WorkContexts()
	if err != nil {
		t.Fatal(err)
	}
	defer service.CloseWorkContexts()
	ctx := context.Background()
	scope := workcontext.Scope{OwnerID: "local-operator"}
	stream, err := store.Create(ctx, scope, workcontext.ContextInput{Title: "长期主线", Goal: "恢复背景"})
	if err != nil {
		t.Fatal(err)
	}
	link, err := service.LinkWorkSession(ctx, scope, stream.ID, WorkSessionInput{Topic: "上下文主题"})
	if err != nil {
		t.Fatal(err)
	}
	session, err := service.requireSession(link.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	before := session.locator
	service.refreshWorkContextFeedback(session)
	text := ""
	for _, msg := range session.agent.GetMessages() {
		text += protocol.MessageText(msg)
	}
	if !strings.Contains(text, "长期主线") || !strings.Contains(text, "NOT verified") {
		t.Fatal("missing safe feedback")
	}
	stream, err = store.SaveCheckpoint(ctx, scope, stream.ID, stream.Version, workcontext.Checkpoint{Summary: "已定位入口", NextActions: []string{"跑验证"}})
	if err != nil {
		t.Fatal(err)
	}
	service.refreshWorkContextFeedback(session)
	count := 0
	text = ""
	for _, msg := range session.agent.GetMessages() {
		body := protocol.MessageText(msg)
		if strings.HasPrefix(body, workContextFeedbackPrefix) {
			count++
			text = body
		}
	}
	if count != 1 || !strings.Contains(text, "已定位入口") {
		t.Fatalf("feedback did not replace: %d %s", count, text)
	}
	if session.locator.Key != before.Key || session.locator.Channel != before.Channel {
		t.Fatal("identity changed")
	}
	if err := store.UnlinkSession(ctx, scope, stream.ID, link.SessionID); err != nil {
		t.Fatal(err)
	}
	service.refreshWorkContextFeedback(session)
	for _, msg := range session.agent.GetMessages() {
		if strings.HasPrefix(protocol.MessageText(msg), workContextFeedbackPrefix) {
			t.Fatal("stale feedback retained after unlink")
		}
	}
}

func TestOrganizeWorkContextRefusesUnlinkedStream(t *testing.T) {
	cfg := newTestConfig(t)
	service := newTestService(cfg, &stubCaller{})
	store, err := service.WorkContexts()
	if err != nil {
		t.Fatal(err)
	}
	defer service.CloseWorkContexts()
	ctx := context.Background()
	scope := workcontext.Scope{OwnerID: "local-operator"}
	stream, err := store.Create(ctx, scope, workcontext.ContextInput{Title: "未绑定对话", Goal: "巡检"})
	if err != nil {
		t.Fatal(err)
	}
	// On-demand maintenance runs ON the linked conversation; a stream without one
	// must be refused rather than silently creating or picking a session.
	if _, err := service.OrganizeWorkContext(ctx, scope, stream.ID); err == nil {
		t.Fatal("organize accepted a stream with no linked session")
	}
	archivedStatus := "archived"
	archived, err := store.Patch(ctx, scope, stream.ID, workcontext.ContextPatch{IfVersion: stream.Version, Status: &archivedStatus})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.OrganizeWorkContext(ctx, scope, archived.ID); err == nil {
		t.Fatal("organize accepted an archived stream")
	}
}
