package backend

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/core/memory"
	"github.com/tim5wang/godex/internal/core/workcontext"
	"github.com/tim5wang/godex/internal/domain/message"
)

func TestDreamCancellationRetainsWatermarkAndRecordsModelTiming(t *testing.T) {
	s := newTestService(newTestConfig(t), &stubCaller{})
	defer s.CloseWorkContexts()
	c := createDesktopTest(t, s, "desktop")
	store, _ := s.WorkContexts()
	in, _, err := store.BeginDream(context.Background(), desktopTestScope(), c.ID, false, false, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	s.dreamOrganize = func(ctx context.Context, _ string, _ string) (string, error) { <-ctx.Done(); return "", ctx.Err() }
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	s.runDream(ctx, store, desktopTestScope(), *in)
	state, _ := store.MemoryState(context.Background(), desktopTestScope(), c.ID)
	if state.Status != "failed" || state.ProcessedSeq != 0 || state.RunID != "" {
		t.Fatalf("cancellation consumed source or retained lease: %+v", state)
	}
	runs, _ := store.DreamRuns(context.Background(), desktopTestScope(), c.ID)
	raw, _ := json.Marshal(runs[0]["result"])
	var saved struct {
		Metrics *workcontext.DreamMetrics `json:"metrics"`
	}
	_ = json.Unmarshal(raw, &saved)
	if saved.Metrics == nil || saved.Metrics.InputBytes == 0 || saved.Metrics.TimeoutSeconds != 180 {
		t.Fatalf("failure timing missing: %s", raw)
	}
}

func TestDreamRejectsModelOutputAfterCancellation(t *testing.T) {
	s := newTestService(newTestConfig(t), &stubCaller{})
	defer s.CloseWorkContexts()
	c := createDesktopTest(t, s, "desktop")
	store, _ := s.WorkContexts()
	in, _, err := store.BeginDream(context.Background(), desktopTestScope(), c.ID, false, false, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	s.dreamOrganize = func(context.Context, string, string) (string, error) {
		time.Sleep(20 * time.Millisecond)
		return `{"summary":"late result must not apply"}`, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	s.runDream(ctx, store, desktopTestScope(), *in)
	state, _ := store.MemoryState(context.Background(), desktopTestScope(), c.ID)
	if state.Status != "failed" || state.Summary != "" || state.ProcessedSeq != 0 {
		t.Fatalf("late output was accepted: %+v", state)
	}
}

func desktopTestScope() workcontext.Scope {
	return workcontext.Scope{TenantID: "tenant", OwnerID: "alice"}
}
func desktopOwnerContext() context.Context {
	return memory.WithOwner(context.Background(), memory.Owner{TenantID: "tenant", UserID: "alice"})
}
func createDesktopTest(t *testing.T, s *Service, title string) workcontext.Context {
	t.Helper()
	store, err := s.WorkContexts()
	if err != nil {
		t.Fatal(err)
	}
	c, err := store.Create(context.Background(), desktopTestScope(), workcontext.ContextInput{Title: title, Goal: title})
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func linkDesktopTest(t *testing.T, s *Service, id, topic string) *sessionState {
	t.Helper()
	link, err := s.LinkWorkSession(desktopOwnerContext(), desktopTestScope(), id, WorkSessionInput{Topic: topic})
	if err != nil {
		t.Fatal(err)
	}
	session, err := s.requireSession(link.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	s.refreshWorkContextFeedback(session, "Vision OCR")
	return session
}
func confirmedDesktopFact(t *testing.T, s *Service, id string) workcontext.Fact {
	t.Helper()
	store, _ := s.WorkContexts()
	f, err := store.CreateFact(context.Background(), desktopTestScope(), id, workcontext.FactInput{Title: "Vision OCR", Content: "Vision OCR is available on the scoped macOS node", Kind: "method", Scope: `{"node":"mac-test","os":"darwin"}`, Source: "report://ocr/verified", ObservedAt: "2026-11-12T00:00:00Z", Provenance: "system_query"})
	if err != nil {
		t.Fatal(err)
	}
	f, err = store.ReviewFact(context.Background(), desktopTestScope(), id, f.ID, workcontext.FactReview{Status: "confirmed", IfVersion: f.Version})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestDesktopMemorySharedWithinDesktopAndPromotedExplicitly(t *testing.T) {
	cfg := newTestConfig(t)
	s := newTestService(cfg, &stubCaller{})
	defer s.CloseWorkContexts()
	a, b := createDesktopTest(t, s, "A"), createDesktopTest(t, s, "B")
	a1, a2 := linkDesktopTest(t, s, a.ID, "A-one"), linkDesktopTest(t, s, a.ID, "A-two")
	b1 := linkDesktopTest(t, s, b.ID, "B-one")
	store, _ := s.WorkContexts()
	personal := s.personalMemory(memory.Owner{TenantID: "tenant", UserID: "alice"})
	writer := func(session *sessionState, id string) *desktopMemory {
		return &desktopMemory{store: store, binding: workMemoryBinding{Scope: desktopTestScope(), ContextID: id}, sessionID: session.id, personal: personal}
	}
	wa, wa2, wb := writer(a1, a.ID), writer(a2, a.ID), writer(b1, b.ID)
	entry, err := wa.Remember(memory.SaveInput{Title: "local work observation", Summary: "private", Content: "desktop A only", Type: memory.TypeWorkFact})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = wa2.Get(entry.ID); err != nil {
		t.Fatal("same-desktop conversation cannot read work memory", err)
	}
	if _, err = wb.Get(entry.ID); err == nil {
		t.Fatal("other desktop read raw working memory")
	}
	if _, err = wa.AcceptCandidate(entry.ID); err == nil {
		t.Fatal("agent bypassed desktop review")
	}
	f := confirmedDesktopFact(t, s, a.ID)
	s.refreshWorkContextFeedback(a2, "Vision OCR")
	s.refreshWorkContextFeedback(b1, "Vision OCR")
	text := func(session *sessionState) string {
		var out string
		for _, m := range session.agent.GetMessages() {
			out += protocol.MessageText(m)
		}
		return out
	}
	if !strings.Contains(text(a2), f.ID) || strings.Contains(text(b1), f.ID) {
		t.Fatal("desktop snapshot crossed its bound scope")
	}
	if _, err = s.ProposeWorkMemory(desktopOwnerContext(), desktopTestScope(), a.ID, f.ID); err != nil {
		t.Fatal(err)
	}
	pending, err := s.ListMemoryCandidates(desktopOwnerContext())
	if err != nil || len(pending) != 1 || pending[0].Origin == nil {
		t.Fatalf("promotion provenance missing: %+v %v", pending, err)
	}
	legacy, _ := memory.NewManager(cfg.MemoryDir).ListCandidates()
	if len(legacy) != 0 {
		t.Fatal("desktop promotion leaked to instance-wide inbox")
	}
	before, _ := personal.FindRelevant("Vision OCR", 5)
	if len(before) != 0 {
		t.Fatal("pending promotion was auto-recalled")
	}
	accepted, err := s.AcceptMemoryCandidate(desktopOwnerContext(), memory.AcceptCandidateInput{Fingerprint: pending[0].Fingerprint})
	if err != nil {
		t.Fatal(err)
	}
	layers, err := personal.PreviewContextLayers("Vision OCR")
	if err != nil || len(layers.Relevant) != 1 || layers.Relevant[0].ID != accepted.ID {
		t.Fatalf("accepted knowledge absent: %+v %v", layers, err)
	}
	bob := s.personalMemory(memory.Owner{TenantID: "tenant", UserID: "bob"})
	other, _ := bob.FindRelevant("Vision OCR", 5)
	if len(other) != 0 {
		t.Fatal("personal knowledge crossed owners")
	}
	f, err = store.ReviewFact(context.Background(), desktopTestScope(), a.ID, f.ID, workcontext.FactReview{Status: "stale", IfVersion: f.Version})
	if err != nil {
		t.Fatal(err)
	}
	layers, err = personal.PreviewContextLayers("Vision OCR")
	if err != nil || len(layers.Relevant) != 0 {
		t.Fatalf("invalidated origin still injected: %+v %v", layers, err)
	}
	retained, err := personal.Get(accepted.ID)
	if err != nil || retained.Origin == nil {
		t.Fatal("source invalidation deleted history")
	}
}

func TestDesktopTurnKeepsOriginalWriteTargetAfterUnlink(t *testing.T) {
	s := newTestService(newTestConfig(t), &stubCaller{})
	defer s.CloseWorkContexts()
	store, _ := s.WorkContexts()
	a, b := createDesktopTest(t, s, "A"), createDesktopTest(t, s, "B")
	session := linkDesktopTest(t, s, a.ID, "topic")
	if _, err := store.LinkSession(context.Background(), desktopTestScope(), b.ID, workcontext.SessionLink{SessionID: session.id, Topic: "reference"}); err != nil {
		t.Fatal(err)
	}
	if err := store.UnlinkSession(context.Background(), desktopTestScope(), a.ID, session.id); err != nil {
		t.Fatal(err)
	}
	s.recordWorkContextSessionTurn(session, "frozen-turn", "completed")
	facts, _ := store.Get(context.Background(), desktopTestScope(), a.ID)
	other, _ := store.Get(context.Background(), desktopTestScope(), b.ID)
	if !hasEventKind(facts.Events, workcontext.SessionTurnEventKind) || hasEventKind(other.Events, workcontext.SessionTurnEventKind) {
		t.Fatal("completion followed mutable binding")
	}
	s.refreshWorkContextFeedback(session)
	if session.workContextID != b.ID {
		t.Fatal("next turn did not refresh its explicit primary")
	}
}

func waitDesktopDream(t *testing.T, s *Service, id string) workcontext.MemoryState {
	t.Helper()
	store, _ := s.WorkContexts()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		state, err := store.MemoryState(context.Background(), desktopTestScope(), id)
		if err != nil {
			t.Fatal(err)
		}
		if state.Status != "running" {
			return state
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("Dream did not complete")
	return workcontext.MemoryState{}
}
func TestScheduledDreamUsesDirtyWatermarkAndNoConversation(t *testing.T) {
	s := newTestService(newTestConfig(t), &stubCaller{})
	defer s.CloseWorkContexts()
	c := createDesktopTest(t, s, "background desktop")
	store, _ := s.WorkContexts()
	var calls atomic.Int32
	s.dreamOrganize = func(context.Context, string, string) (string, error) {
		calls.Add(1)
		return `{"summary":"scoped digest"}`, nil
	}
	_, err := store.ConfigureDream(context.Background(), desktopTestScope(), c.ID, true, 60, time.Now().Add(-2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	s.sweepMemoryDream(context.Background())
	state := waitDesktopDream(t, s, c.ID)
	if calls.Load() != 1 || state.ProcessedSeq != state.SourceSeq {
		t.Fatalf("scheduled change not processed: calls=%d state=%+v", calls.Load(), state)
	}
	for i := 0; i < 4; i++ {
		s.sweepMemoryDream(context.Background())
	}
	if calls.Load() != 1 {
		t.Fatal("unchanged schedule called a model")
	}
	result, err := s.OrganizeWorkContext(context.Background(), desktopTestScope(), c.ID)
	if err != nil || result["queued"] != false {
		t.Fatalf("manual incremental did not skip unchanged: %v %v", result, err)
	}
	result, err = s.OrganizeWorkContext(context.Background(), desktopTestScope(), c.ID, DreamOptions{Force: true})
	if err != nil || result["queued"] != true {
		t.Fatal("explicit full rebuild skipped")
	}
	waitDesktopDream(t, s, c.ID)
	if calls.Load() != 2 {
		t.Fatalf("manual full calls=%d", calls.Load())
	}
	if len(s.sessions) != 0 {
		t.Fatal("Dream created a user conversation")
	}
}

func TestDesktopSessionOwnerCannotBeReopenedOrLinkedByOtherOwner(t *testing.T) {
	s := newTestService(newTestConfig(t), &stubCaller{})
	defer s.CloseWorkContexts()
	desktop := createDesktopTest(t, s, "alice")
	session := linkDesktopTest(t, s, desktop.ID, "work")
	bobContext := memory.WithOwner(context.Background(), memory.Owner{TenantID: "tenant", UserID: "bob"})
	if _, err := s.OpenSession(bobContext, session.locator); err == nil {
		t.Fatal("resident session owner bypassed")
	}
	bobScope := workcontext.Scope{TenantID: "tenant", OwnerID: "bob"}
	store, _ := s.WorkContexts()
	bobDesktop, err := store.Create(bobContext, bobScope, workcontext.ContextInput{Title: "bob", Goal: "isolated"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.LinkWorkSession(bobContext, bobScope, bobDesktop.ID, WorkSessionInput{SessionID: session.id, Topic: "steal"}); err == nil {
		t.Fatal("foreign session linked")
	}
	if _, err = s.loadSession(session.id, session.locator, memory.Owner{TenantID: "tenant", UserID: "bob"}); err == nil {
		t.Fatal("persisted session owner bypassed")
	}
}

func TestDreamResponseRejectsTrailingOrUnknownOutput(t *testing.T) {
	for _, raw := range []string{`{"summary":"ok"} {"summary":"second"}`, `{"summary":"ok","confirm_all":true}`, `{"summary":"ok","skipped_candidates":[]}`, `not JSON`} {
		if _, err := parseDreamResult(raw); err == nil {
			t.Fatalf("accepted invalid response %q", raw)
		}
	}
	if _, err := parseDreamResult("```json\n{\"summary\":\"参考摘要\"}\n```"); err != nil {
		t.Fatal(err)
	}
}

func TestDesktopSessionCannotExposeMemoryThroughDirectTurnOrInspector(t *testing.T) {
	s := newTestService(newTestConfig(t), &stubCaller{})
	defer s.CloseWorkContexts()
	desktop := createDesktopTest(t, s, "alice")
	session := linkDesktopTest(t, s, desktop.ID, "work")
	bob := memory.WithOwner(context.Background(), memory.Owner{TenantID: "tenant", UserID: "bob"})
	if _, err := s.ContextInspector(bob, session.id); err == nil {
		t.Fatal("foreign Inspector exposed owner memory")
	}
	if _, err := s.Snapshot(bob, session.id); err == nil {
		t.Fatal("foreign snapshot exposed context")
	}
	if _, err := s.SubmitAsync(bob, session.id, message.NewTextEnvelope(message.SourceWeb, session.id, "bob", "read memories", time.Now())); err == nil {
		t.Fatal("foreign turn could run with owner's memory")
	}
	sessions, err := s.ListSessions(bob, SessionListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 0 {
		t.Fatalf("foreign session metadata listed: %+v", sessions)
	}
}
