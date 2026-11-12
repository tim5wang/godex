package backend

import (
	"context"
	"testing"

	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/core/workcontext"
)

// TestSessionTurnRecordsWorkContextEvent is the regression for the gap that made a
// backgrounded desktop blind: nothing recorded a linked conversation finishing a turn, so
// a user who left a desktop in the background could not tell that its conversation had
// finished. The signal must land in the SAME wc_events table the unread badge, the
// "changes since last look" list and the aging cadence already read, or the rail's status
// would need a second source of truth.
func TestSessionTurnRecordsWorkContextEvent(t *testing.T) {
	cfg := newTestConfig(t)
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("ok")}}}})
	defer service.CloseWorkContexts()
	store, err := service.WorkContexts()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	scope := workcontext.Scope{OwnerID: "local-operator"}
	stream, err := store.Create(ctx, scope, workcontext.ContextInput{Title: "后台可见性", Goal: "对话完成后桌面能看到"})
	if err != nil {
		t.Fatal(err)
	}
	link, err := service.LinkWorkSession(ctx, scope, stream.ID, WorkSessionInput{Topic: "排查对话"})
	if err != nil {
		t.Fatalf("LinkWorkSession: %v", err)
	}
	session, err := service.requireSession(link.SessionID)
	if err != nil {
		t.Fatalf("requireSession: %v", err)
	}

	// Before the turn event exists, the projection has nothing to report — which is what
	// the rail reads as "no conversation news".
	turns, err := store.NewestSessionTurnByContext(ctx, scope, workcontext.SessionTurnEventKind)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 0 {
		t.Fatalf("a stream with no finished turn must report none, got %+v", turns)
	}

	service.refreshWorkContextFeedback(session)
	service.recordWorkContextSessionTurn(session, "turn-1", "completed")

	detail, err := store.Get(ctx, scope, stream.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !hasEventKind(detail.Events, workcontext.SessionTurnEventKind) {
		t.Fatalf("a finished conversation turn was not recorded on the stream; events=%+v", detail.Events)
	}
	// The event carries the session in its SOURCE, because the rail matches the event back
	// to the linked conversation it names. Without that the card cannot say which
	// conversation moved.
	turns, err = store.NewestSessionTurnByContext(ctx, scope, workcontext.SessionTurnEventKind)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := turns[stream.ID]
	if !ok {
		t.Fatalf("the finished turn was not projected for its stream: %+v", turns)
	}
	if got.Source != "session://"+link.SessionID {
		t.Fatalf("turn event source should name the session, got %q", got.Source)
	}

	// Idempotent per turn: a replayed or recovered turn must not stack duplicate rows, or
	// the unread count would inflate every restart.
	service.recordWorkContextSessionTurn(session, "turn-1", "completed")
	detail, err = store.Get(ctx, scope, stream.ID)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, event := range detail.Events {
		if event.Kind == workcontext.SessionTurnEventKind {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("the same turn must be recorded once, got %d rows", count)
	}

	// A DIFFERENT turn is a new fact: the second turn must be recorded too.
	service.recordWorkContextSessionTurn(session, "turn-2", "error")
	detail, err = store.Get(ctx, scope, stream.ID)
	if err != nil {
		t.Fatal(err)
	}
	if countOfKind(detail.Events, workcontext.SessionTurnEventKind) != 2 {
		t.Fatalf("a second turn must add its own event, got %+v", detail.Events)
	}
}

// TestSessionTurnSkipsUnlinkedAndPendingApproval pins the two silent paths, because both
// would be wrong if they were noisy: a session nobody linked belongs to no stream, and a
// turn waiting on an approval is not progress — that IS decision-class and already has its
// own approval delivery, so recording it here would double-report the same event.
func TestSessionTurnSkipsUnlinkedAndPendingApproval(t *testing.T) {
	cfg := newTestConfig(t)
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("ok")}}}})
	defer service.CloseWorkContexts()
	store, err := service.WorkContexts()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	scope := workcontext.Scope{OwnerID: "local-operator"}
	stream, err := store.Create(ctx, scope, workcontext.ContextInput{Title: "有主线的流", Goal: "g"})
	if err != nil {
		t.Fatal(err)
	}
	link, err := service.LinkWorkSession(ctx, scope, stream.ID, WorkSessionInput{Topic: "对话"})
	if err != nil {
		t.Fatalf("LinkWorkSession: %v", err)
	}
	session, err := service.requireSession(link.SessionID)
	if err != nil {
		t.Fatalf("requireSession: %v", err)
	}

	// pending_approval is skipped: it is not progress, and it is delivered as an approval.
	service.recordWorkContextSessionTurn(session, "turn-pending", "pending_approval")
	detail, err := store.Get(ctx, scope, stream.ID)
	if err != nil {
		t.Fatal(err)
	}
	if hasEventKind(detail.Events, workcontext.SessionTurnEventKind) {
		t.Fatalf("a pending-approval turn must not be recorded as progress: %+v", detail.Events)
	}

	// A session that no stream links has no stream to report to, so nothing is written
	// anywhere. Asserted by the absence of any turn event under either stream.
	unlinked, err := service.OpenSession(ctx, SessionLocator{Channel: "web", Key: "unlinked-session"})
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	other, err := service.requireSession(unlinked.SessionID)
	if err != nil {
		t.Fatalf("requireSession(unlinked): %v", err)
	}
	service.recordWorkContextSessionTurn(other, "turn-unlinked", "completed")
	detail, err = store.Get(ctx, scope, stream.ID)
	if err != nil {
		t.Fatal(err)
	}
	if hasEventKind(detail.Events, workcontext.SessionTurnEventKind) {
		t.Fatalf("an unlinked session must not report into a stream: %+v", detail.Events)
	}
}

func countOfKind(events []workcontext.Event, kind string) int {
	count := 0
	for _, event := range events {
		if event.Kind == kind {
			count++
		}
	}
	return count
}
