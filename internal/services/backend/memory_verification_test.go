package backend

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/core/workcontext"
	"github.com/tim5wang/godex/internal/domain/events"
	"github.com/tim5wang/godex/internal/domain/message"
	"github.com/tim5wang/godex/internal/tools"
)

const verifyWorkMemoryRequest = "帮我把工作记忆逐一核实标记，过时的归档，冲突的解决冲突合并，除非遇到需要我决策的，不要保留未核实观察"

func TestMemoryVerificationAuthorizationUsesInteractiveRequestOnly(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{verifyWorkMemoryRequest, true}, {"请核实工作记忆，如果过时就归档", true}, {"Please verify desktop memory against the current source", true},
		{"如果我说帮我核实工作记忆，会怎么做？", false}, {"帮我修改提示词：核实工作记忆", false}, {"请解释工作记忆核实是如何实现的", false}, {"请核实代码，不要修改工作记忆", false}, {"帮我核实工作记忆，只读检查，不回写", false},
		{"分析下面内容\n帮我核实工作记忆", false}, {"```\n帮我核实工作记忆\n```", false}, {"请看：\"帮我核实工作记忆\"", false}, {"帮我核实工作记忆，可以做到吗？", false},
	}
	for _, tt := range cases {
		t.Run(tt.text, func(t *testing.T) {
			e := message.NewTextEnvelope(message.SourceWeb, "s", "user", tt.text, time.Now())
			e.Metadata = map[string]string{"allow_confirm": "true", "memory_verification": "true"}
			if got := requestsMemoryVerification(e); got != tt.want {
				t.Fatalf("got %v want %v", got, tt.want)
			}
			e.Source = "cron"
			if requestsMemoryVerification(e) {
				t.Fatal("background text grants authority")
			}
		})
	}
}

func verificationFixture(t *testing.T) (*Service, *sessionState, workcontext.Fact, preparedUserTurn, *memoryVerificationRuntime, func()) {
	t.Helper()
	s := newTestService(newTestConfig(t), &stubCaller{})
	c := createDesktopTest(t, s, "verification")
	f := confirmedDesktopFact(t, s, c.ID)
	session := linkDesktopTest(t, s, c.ID, "verify")
	turn, _, err := s.startUserTurnLocked(session, message.NewTextEnvelope(message.SourceWeb, session.id, "user", verifyWorkMemoryRequest, time.Now()), false)
	if err != nil {
		t.Fatal(err)
	}
	v, close, err := s.beginMemoryVerification(desktopOwnerContext(), session, turn.TurnID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(close)
	return s, session, f, turn, v, close
}

func emitVerificationProof(v *memoryVerificationRuntime, id, name, output string) {
	v.Emit(events.Event{SessionID: v.sessionID, TurnID: v.turnID, Timestamp: time.Now(), Type: events.EventToolCallFinished, Payload: events.ToolCallPayload{ID: id, Name: name, Output: output, Input: map[string]any{"path": "source.go"}}})
}

func TestMemoryVerificationGateReceiptsAndCompletion(t *testing.T) {
	s, session, f, _, v, close := verificationFixture(t)
	ctx := desktopOwnerContext()
	tool := s.newWorkContextTool(session.id)
	apply := map[string]any{"action": "memory_apply", "operations": []any{map[string]any{"action": "confirm", "fact_id": f.ID, "if_version": f.Version, "reason": "source supports scoped claim", "evidence_ids": []string{"forged"}}}}
	if _, err := tool.Execute(ctx, apply); err == nil {
		t.Fatal("invented proof accepted")
	}
	if _, err := tool.Execute(ctx, map[string]any{"action": "memory_finish"}); err == nil {
		t.Fatal("unfinished inventory reported success")
	}
	emitVerificationProof(v, "summary", "work_context", "claimed verified")
	emitVerificationProof(v, "read", "read_file", "actual source")
	emitVerificationProof(v, "old", "read_file", "actual source")
	v.mu.Lock()
	delete(v.proofs, "old")
	v.mu.Unlock()
	v.Emit(events.Event{SessionID: session.id, TurnID: "other-turn", Timestamp: time.Now(), Type: events.EventToolCallFinished, Payload: events.ToolCallPayload{ID: "old", Name: "read_file", Output: "old result"}})
	status, err := s.memoryVerificationStatus(ctx, v)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(status.EvidenceIDs, ",") != "read" {
		t.Fatal("non-task evidence accepted", status)
	}
	apply["operations"].([]any)[0].(map[string]any)["evidence_ids"] = []string{"read"}
	if _, err = tool.Execute(ctx, apply); err != nil {
		t.Fatal(err)
	}
	status, err = s.memoryVerificationStatus(ctx, v)
	if err != nil || status.Processed != 1 || len(status.Remaining) != 0 {
		t.Fatal(status, err)
	}
	if _, err = tool.Execute(ctx, map[string]any{"action": "memory_finish"}); err != nil {
		t.Fatal(err)
	}
	if _, err = tool.Execute(ctx, map[string]any{"action": "memory_inventory"}); err == nil {
		t.Fatal("ended task retains authority")
	}
	close()
	if _, err = tool.Execute(ctx, apply); err == nil {
		t.Fatal("authority leaked into ordinary turn")
	}
	history, _ := func() ([]workcontext.MemoryVerificationRecord, error) {
		store, _ := s.WorkContexts()
		return store.MemoryVerificationHistory(ctx, desktopTestScope(), v.grant.ContextID, session.id, v.turnID)
	}()
	if len(history) != 1 || history[0].Entries[0].Evidence[0].Excerpt != "actual source\n" {
		t.Fatal("host evidence not persisted", history)
	}
}

func TestMemoryVerificationFailedChecksAndCancelledTask(t *testing.T) {
	s, session, f, _, v, _ := verificationFixture(t)
	ctx := desktopOwnerContext()
	tool := s.newWorkContextTool(session.id)
	code := 1
	v.Emit(events.Event{SessionID: session.id, TurnID: v.turnID, Timestamp: time.Now(), Type: events.EventToolCallFinished, Payload: events.ToolCallPayload{ID: "failed-test", Name: "bash", Output: "FAIL", ExitCode: &code}})
	op := map[string]any{"action": "confirm", "fact_id": f.ID, "if_version": f.Version, "reason": "test failed", "evidence_ids": []string{"failed-test"}}
	if _, err := tool.Execute(ctx, map[string]any{"action": "memory_apply", "operations": []any{op}}); err == nil {
		t.Fatal("failed test confirmed fact")
	}
	op["action"] = "block"
	op["block_kind"] = "access"
	if _, err := tool.Execute(ctx, map[string]any{"action": "memory_apply", "operations": []any{op}}); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := tool.Execute(cancelled, map[string]any{"action": "memory_status"}); err == nil {
		t.Fatal("cancelled task still writes")
	}
}

func TestMemoryVerificationFrozenInventoryAndChangedBinding(t *testing.T) {
	s, session, f, turn, v, close := verificationFixture(t)
	ctx := desktopOwnerContext()
	store, _ := s.WorkContexts()
	later := confirmedDesktopFact(t, s, v.grant.ContextID)
	emitVerificationProof(v, "read", "read_file", "source")
	_, err := s.applyMemoryVerificationTool(ctx, v, []memoryVerificationToolOperation{{Action: "archive", FactID: later.ID, IfVersion: later.Version, Reason: "not in frozen set", EvidenceIDs: []string{"read"}}})
	if err == nil {
		t.Fatal("new record silently added to task")
	}
	inventory, err := s.memoryVerificationInventory(ctx, v, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	facts := inventory.(map[string]any)["facts"].([]workcontext.Fact)
	if len(facts) != 1 || facts[0].ID != f.ID || inventory.(map[string]any)["next_cursor"] != -1 {
		t.Fatal("frozen inventory changed")
	}
	close()
	// A restart retains the authorization and real receipts for this task only.
	session.updateTurnStatus(turn.TurnID, "interrupted", "", "checkpoint", time.Now())
	if err = s.persistSession(session, time.Now()); err != nil {
		t.Fatal(err)
	}
	restored := newTestService(s.cfg, &stubCaller{})
	if _, err = restored.OpenSession(ctx, session.locator); err != nil {
		t.Fatal(err)
	}
	reopened, err := restored.requireSessionFor(ctx, session.id)
	if err != nil {
		t.Fatal(err)
	}
	resumed, cleanup, err := restored.beginMemoryVerification(ctx, reopened, turn.TurnID)
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
	if resumed == nil || len(resumed.grant.Facts) != 1 {
		t.Fatal("grant not durable")
	}
	other := createDesktopTest(t, s, "other")
	if _, err = store.LinkSession(ctx, desktopTestScope(), other.ID, workcontext.SessionLink{SessionID: session.id}); err != nil {
		t.Fatal(err)
	}
	if err = store.UnlinkSession(ctx, desktopTestScope(), v.grant.ContextID, session.id); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.beginMemoryVerification(ctx, session, turn.TurnID); err == nil {
		t.Fatal("resume followed changed desktop")
	}
}

func TestMemoryVerificationMCPReceiptMatchesOperation(t *testing.T) {
	if !objectiveVerificationTool("codebase-memory__get_code_snippet", nil) || !objectiveVerificationTool("call_mcp_tool", map[string]any{"tool": "search_graph"}) {
		t.Fatal("real code query rejected")
	}
	if objectiveVerificationTool("call_mcp_tool", map[string]any{"tool": "remember", "arguments": map[string]any{"text": "get_code_snippet"}}) || objectiveVerificationTool("mcp", map[string]any{"text": "search_graph"}) {
		t.Fatal("keyword masqueraded as a code query")
	}
}

func TestMemoryVerificationAgentRoundTrip(t *testing.T) {
	cfg := newTestConfig(t)
	caller := &stubCaller{}
	s := newTestService(cfg, caller)
	c := createDesktopTest(t, s, "roundtrip")
	store, _ := s.WorkContexts()
	f := confirmedDesktopFact(t, s, c.ID)
	session := linkDesktopTest(t, s, c.ID, "roundtrip")
	path := filepath.Join(cfg.WorkspaceDir, "source.go")
	if err := os.WriteFile(path, []byte("package fixture\nconst VisionAvailable = true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	response := func(id string, input map[string]any) protocol.Response {
		return protocol.Response{Content: []protocol.Block{protocol.ToolUseBlock(id, "work_context", input)}}
	}
	caller.responses = []protocol.Response{
		response("inventory", map[string]any{"action": "memory_inventory"}),
		{Content: []protocol.Block{protocol.ToolUseBlock("actual-read", "read_file", map[string]any{"path": path})}},
		response("apply", map[string]any{"action": "memory_apply", "operations": []any{map[string]any{"action": "confirm", "fact_id": f.ID, "if_version": f.Version, "reason": "source.go declares VisionAvailable on the fixture node", "evidence_ids": []string{"actual-read"}}}}),
		response("finish", map[string]any{"action": "memory_finish"}),
		{Content: []protocol.Block{protocol.TextBlock("已核实并回写工作记忆。")}},
	}
	result, err := s.Submit(desktopOwnerContext(), session.id, message.NewTextEnvelope(message.SourceWeb, session.id, "user", verifyWorkMemoryRequest, time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "completed" {
		t.Fatalf("turn not completed: %+v", result)
	}
	got, _ := store.Fact(context.Background(), desktopTestScope(), c.ID, f.ID)
	history, _ := store.MemoryVerificationHistory(context.Background(), desktopTestScope(), c.ID, session.id, result.TurnID)
	if got.Version != f.Version+1 || !strings.HasPrefix(got.Source, "verification://") || len(history) != 1 || !strings.Contains(history[0].Entries[0].Evidence[0].Excerpt, "VisionAvailable") {
		raw, _ := json.Marshal(history)
		t.Fatalf("roundtrip did not write actual evidence: %+v audit=%s", got, raw)
	}
	// A normal conversation cannot reuse its preceding task's authority or receipts.
	caller.mu.Lock()
	caller.responses = append(caller.responses, response("unauthorized", map[string]any{"action": "memory_apply", "operations": []any{map[string]any{"action": "archive", "fact_id": f.ID, "if_version": got.Version, "reason": "try old authority", "evidence_ids": []string{"actual-read"}}}}), protocol.Response{Content: []protocol.Block{protocol.TextBlock("无核实授权。")}})
	caller.mu.Unlock()
	if _, err = s.Submit(desktopOwnerContext(), session.id, message.NewTextEnvelope(message.SourceWeb, session.id, "user", "看看当前进展", time.Now())); err != nil {
		t.Fatal(err)
	}
	unchanged, _ := store.Fact(context.Background(), desktopTestScope(), c.ID, f.ID)
	if unchanged.Version != got.Version || unchanged.Status != "confirmed" {
		t.Fatal("ordinary turn bypassed host gate")
	}
}

func TestMemoryVerificationIncompleteCanResume(t *testing.T) {
	cfg := newTestConfig(t)
	caller := &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("还没有逐项核实。")}}}}
	s := newTestService(cfg, caller)
	c := createDesktopTest(t, s, "incomplete")
	confirmedDesktopFact(t, s, c.ID)
	session := linkDesktopTest(t, s, c.ID, "incomplete")
	result, err := s.Submit(desktopOwnerContext(), session.id, message.NewTextEnvelope(message.SourceWeb, session.id, "user", verifyWorkMemoryRequest, time.Now()))
	if err == nil || result.Status != "interrupted" {
		t.Fatal("incomplete task reported completed", result, err)
	}
	record, err := session.resumableTurnRecord(result.TurnID)
	if err != nil || record.MemoryVerification == nil {
		t.Fatal("incomplete task cannot resume", err)
	}
	if session.memoryVerification != nil {
		t.Fatal("closed turn retains write authority")
	}
	store, _ := s.WorkContexts()
	facts, _ := store.Facts(context.Background(), desktopTestScope(), c.ID)
	f := facts[0]
	path := filepath.Join(cfg.WorkspaceDir, "fixture.go")
	if err = os.WriteFile(path, []byte("const VisionAvailable = true"), 0600); err != nil {
		t.Fatal(err)
	}
	caller.mu.Lock()
	caller.responses = append(caller.responses,
		protocol.Response{Content: []protocol.Block{protocol.ToolUseBlock("resume-read", "read_file", map[string]any{"path": path})}},
		protocol.Response{Content: []protocol.Block{protocol.ToolUseBlock("resume-apply", "work_context", map[string]any{"action": "memory_apply", "operations": []any{map[string]any{"action": "confirm", "fact_id": f.ID, "if_version": f.Version, "reason": "fixture source confirms availability", "evidence_ids": []string{"resume-read"}}}})}},
		protocol.Response{Content: []protocol.Block{protocol.ToolUseBlock("resume-finish", "work_context", map[string]any{"action": "memory_finish"})}},
		protocol.Response{Content: []protocol.Block{protocol.TextBlock("已续跑并完成核实。")}})
	caller.mu.Unlock()
	if _, err = s.ResumeTurnAsync(desktopOwnerContext(), session.id, result.TurnID); err != nil {
		t.Fatal(err)
	}
	finished := waitForBackendSnapshot(t, s, session.id, func(snapshot Snapshot) bool {
		return !snapshot.Running && turnRecordStatus(snapshot.Turns, result.TurnID) == "completed"
	})
	if len(finished.Turns) != 1 {
		t.Fatal("resume started a new task", finished.Turns)
	}
	history, err := store.MemoryVerificationHistory(context.Background(), desktopTestScope(), c.ID, session.id, result.TurnID)
	if err != nil || len(history) != 1 {
		t.Fatal("resume failed to persist audit", history, err)
	}
}

func TestMemoryVerificationInventoryPaginationAndConcurrentEdits(t *testing.T) {
	s := newTestService(newTestConfig(t), &stubCaller{})
	c := createDesktopTest(t, s, "pages")
	for i := 0; i < 27; i++ {
		confirmedDesktopFact(t, s, c.ID)
	}
	session := linkDesktopTest(t, s, c.ID, "pages")
	ctx := desktopOwnerContext()
	turn, _, err := s.startUserTurnLocked(session, message.NewTextEnvelope(message.SourceWeb, session.id, "user", verifyWorkMemoryRequest, time.Now()), false)
	if err != nil {
		t.Fatal(err)
	}
	v, close, err := s.beginMemoryVerification(ctx, session, turn.TurnID)
	if err != nil {
		t.Fatal(err)
	}
	defer close()
	seen := map[string]bool{}
	cursor := 0
	var one workcontext.Fact
	for cursor >= 0 {
		raw, e := s.memoryVerificationInventory(ctx, v, cursor, 12)
		if e != nil {
			t.Fatal(e)
		}
		page := raw.(map[string]any)
		for _, f := range page["facts"].([]workcontext.Fact) {
			if seen[f.ID] {
				t.Fatal("duplicate page record")
			}
			seen[f.ID] = true
			one = f
		}
		cursor = page["next_cursor"].(int)
	}
	if len(seen) != 27 {
		t.Fatal("pagination skipped targets", len(seen))
	}
	emitVerificationProof(v, "read", "read_file", "actual source")
	_, err = s.applyMemoryVerificationTool(ctx, v, []memoryVerificationToolOperation{{Action: "confirm", FactID: one.ID, IfVersion: one.Version, Reason: "source checked", EvidenceIDs: []string{"read"}}})
	if err != nil {
		t.Fatal(err)
	}
	store, _ := s.WorkContexts()
	current, _ := store.Fact(ctx, desktopTestScope(), c.ID, one.ID)
	if _, err = store.ReviewFact(ctx, desktopTestScope(), c.ID, one.ID, workcontext.FactReview{Status: "stale", IfVersion: current.Version}); err != nil {
		t.Fatal(err)
	}
	status, err := s.memoryVerificationStatus(ctx, v)
	if err != nil || status.Processed != 0 || len(status.Remaining) != 27 {
		t.Fatal("concurrent changes silently counted as verified", status, err)
	}
}

func TestMemoryVerificationPermissionResumeKeepsOriginalTask(t *testing.T) {
	cfg := newTestConfig(t)
	caller := &stubCaller{}
	s := newTestService(cfg, caller)
	c := createDesktopTest(t, s, "permission")
	store, _ := s.WorkContexts()
	f, err := store.CreateFact(context.Background(), desktopTestScope(), c.ID, workcontext.FactInput{Title: "shell availability", Content: "sh can be located on this node", Kind: "method", Scope: `{"node":"fixture"}`})
	if err != nil {
		t.Fatal(err)
	}
	session := linkDesktopTest(t, s, c.ID, "permission")
	caller.responses = []protocol.Response{
		{Content: []protocol.Block{protocol.ToolUseBlock("load-shell", "tool_exchange", map[string]any{"enable_bundles": []string{"core_code"}})}},
		{Content: []protocol.Block{protocol.ToolUseBlock("load-approved", "tool_exchange", map[string]any{"enable_bundles": []string{"core_code"}})}},
		{Content: []protocol.Block{protocol.ToolUseBlock("pending-shell", "bash", map[string]any{"command": "command -v sh"})}},
		{Content: []protocol.Block{protocol.ToolUseBlock("approved-shell", "bash", map[string]any{"command": "command -v sh"})}},
		{Content: []protocol.Block{protocol.ToolUseBlock("apply", "work_context", map[string]any{"action": "memory_apply", "operations": []any{map[string]any{"action": "confirm", "fact_id": f.ID, "if_version": f.Version, "reason": "command -v sh returned an executable path on the fixture node", "evidence_ids": []string{"approved-shell"}}}})}},
		{Content: []protocol.Block{protocol.ToolUseBlock("finish", "work_context", map[string]any{"action": "memory_finish"})}},
		{Content: []protocol.Block{protocol.TextBlock("已完成核实。")}},
	}
	result, err := s.Submit(desktopOwnerContext(), session.id, message.NewTextEnvelope(message.SourceWeb, session.id, "user", verifyWorkMemoryRequest, time.Now()))
	if err != nil || result.Status != "pending_approval" {
		for _, m := range session.agent.GetMessages() {
			raw, _ := json.Marshal(m)
			t.Log(string(raw))
		}
		t.Fatal(result, err)
	}
	requestID := result.PendingRequestID
	var resolution tools.PermissionResolution
	for attempts := 0; attempts < 3; attempts++ {
		resolution, err = s.ApprovePermission(desktopOwnerContext(), session.id, requestID, tools.PermissionGrantSession)
		if err != nil || resolution.ResumeTurnID != result.TurnID {
			t.Fatal("permission resume lost original verification task", resolution, err)
		}
		if resolution.ResumeStatus != "pending_approval" {
			break
		}
		requestID = resolution.ResumePendingRequestID
	}
	if resolution.ResumeStatus != "completed" {
		for _, m := range session.agent.GetMessages() {
			raw, _ := json.Marshal(m)
			t.Log(string(raw))
		}
		t.Fatal("permission task failed", resolution)
	}

	history, err := store.MemoryVerificationHistory(context.Background(), desktopTestScope(), c.ID, session.id, result.TurnID)
	if err != nil || len(history) != 1 || history[0].Entries[0].Evidence[0].ToolCallID != "approved-shell" {
		t.Fatal("approved evidence not committed", history, err)
	}
}
