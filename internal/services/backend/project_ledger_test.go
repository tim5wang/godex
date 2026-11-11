package backend

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/domain/events"
	"github.com/tim5wang/godex/internal/domain/message"
)

func TestLedgerUsesCurrentRequestBeforeModelCall(t *testing.T) {
	cfg := newTestConfig(t)
	caller := &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("done")}}}}
	s := newTestService(cfg, caller)
	opened, err := s.OpenSession(context.Background(), SessionLocator{Channel: "web", Key: "ledger-current"})
	if err != nil {
		t.Fatal(err)
	}
	session, err := s.requireSession(opened.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	for i, text := range []string{"现在加载了哪些tools ?", "修复 LSP 初始化失败", "继续", "用中文回复我"} {
		prepared, _, err := s.startUserTurnLocked(session, message.NewTextEnvelope(message.SourceWeb, opened.SessionID, cfg.LeadName, text, time.Now()), false)
		if err != nil {
			t.Fatal(err)
		}
		if i == 1 && (!strings.Contains(prepared.RuntimeContext.ProjectLedger, "Goal: 修复 LSP 初始化失败") || strings.Contains(prepared.RuntimeContext.ProjectLedger, "Goal: 现在加载")) {
			t.Fatalf("model context contains stale goal: %s", prepared.RuntimeContext.ProjectLedger)
		}
		session.agent.AppendAssistantDelivery("English response", "", nil)
		if err := s.updateProjectLedgerFromTurn(session, prepared.TurnID, prepared.Envelope, "completed", nil, prepared.PriorMessageCount, time.Now(), nil, false); err != nil {
			t.Fatal(err)
		}
	}
	ledger, err := s.ProjectLedger(opened.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if ledger.Goal != "修复 LSP 初始化失败" || ledger.CurrentRequest != "用中文回复我" {
		t.Fatalf("lost task/request distinction: %+v", ledger)
	}
	if len(ledger.Decisions) != 0 || strings.Contains(ledger.Compact, "English") || len(ledger.RecentTurns) != 4 {
		t.Fatalf("summary replayed or history lost: %+v", ledger)
	}
}

func TestLegacyLedgerDoesNotInjectUnverifiedDecisionsOrFilePaths(t *testing.T) {
	ledger := ProjectLedger{Goal: "old goal", Decisions: []string{"assistant guessed everything passed"}, ChangedFiles: []string{"read-only.go", ".godex/tmp/probe.py"}}
	text := renderProjectLedgerCompact(ledger)
	if strings.Contains(text, "assistant guessed") || strings.Contains(text, "read-only.go") || strings.Contains(text, "probe.py") {
		t.Fatalf("legacy observations treated as facts: %s", text)
	}
	ledger.DecisionsExplicit = true
	ledger.ChangedFilesSource = "workspace_diff"
	if text = renderProjectLedgerCompact(ledger); !strings.Contains(text, "Recorded decisions") || !strings.Contains(text, "Observed file changes") {
		t.Fatalf("explicit records lost: %s", text)
	}
}

func TestLegacyLedgerRecoversLatestTaskWhenUserContinues(t *testing.T) {
	cfg := newTestConfig(t)
	s := newTestService(cfg, &stubCaller{})
	ledger := ProjectLedger{SessionID: "legacy", Goal: "现在加载了哪些tools ?", RecentTurns: []ProjectLedgerTurn{{User: "现在加载了哪些tools ?"}, {User: "修复 LSP 初始化失败"}, {User: "用中文回复我"}}}
	if err := s.writeProjectLedger("legacy", ledger); err != nil {
		t.Fatal(err)
	}
	if err := s.prepareProjectLedgerForTurn("legacy", "继续", time.Now()); err != nil {
		t.Fatal(err)
	}
	got, err := s.ProjectLedger("legacy")
	if err != nil || got.Goal != "修复 LSP 初始化失败" || got.CurrentRequest != "继续" {
		t.Fatalf("legacy goal remained stale: %+v %v", got, err)
	}
}

func TestLedgerFileListDisclosesOmittedEntries(t *testing.T) {
	var builder strings.Builder
	writeLedgerList(&builder, "files", []string{"a", "b", "c"}, 2)
	if !strings.Contains(builder.String(), "1 more") {
		t.Fatalf("silent truncation: %s", builder.String())
	}
}

func TestLedgerWorkflowSummariesFromTool(t *testing.T) {
	decisions, blockers := ledgerWorkflowSummariesFromTool(events.ToolCallPayload{
		Name: "workflow",
		Output: `{
			"workflow_id": "wf_review",
			"status": "completed",
			"completed": 2,
			"failed": 1,
			"nodes": [
				{"id": "plan", "status": "completed", "result_preview": "plan done"},
				{"id": "impl", "status": "error", "error": "build failed"}
			]
		}`,
	})
	joinedDecisions := strings.Join(decisions, "\n")
	joinedBlockers := strings.Join(blockers, "\n")
	if !strings.Contains(joinedDecisions, "workflow wf_review completed: 2 nodes") ||
		!strings.Contains(joinedDecisions, "workflow wf_review/plan: plan done") {
		t.Fatalf("expected workflow decisions, got %v", decisions)
	}
	if !strings.Contains(joinedBlockers, "workflow wf_review has 1 failed nodes") ||
		!strings.Contains(joinedBlockers, "workflow wf_review/impl failed: build failed") {
		t.Fatalf("expected workflow blockers, got %v", blockers)
	}
}
