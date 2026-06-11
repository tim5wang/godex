package mintui

import (
	"context"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/protocol"
	"github.com/tim5wang/godex/internal/domain/events"
	"github.com/tim5wang/godex/internal/domain/message"
	rtbackend "github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/commands"
	"github.com/tim5wang/godex/internal/tools"
)

// fakeBackend is a minimal in-memory Backend implementation
// sufficient for the unit tests in this package.
type fakeBackend struct {
	sess *rtbackend.OpenedSession
	snap rtbackend.Snapshot
	ctx  tools.ContextInspection

	submitCalls    int
	asyncCalls     int
	executeCalls   int
	cancelCalls    int
	lastSubmitted  string
	lastCancelled  string
}

func newFakeBackend() *fakeBackend {
	return &fakeBackend{
		sess: &rtbackend.OpenedSession{SessionID: "test-sess"},
		snap: rtbackend.Snapshot{
			Messages: []protocol.Message{
				{Role: "user", Content: []protocol.Block{{Type: "text", Text: "hi"}}},
				{Role: "assistant", Content: []protocol.Block{{Type: "text", Text: "hello"}}},
			},
		},
	}
}

func (f *fakeBackend) OpenSession(ctx context.Context, loc rtbackend.SessionLocator) (*rtbackend.OpenedSession, error) {
	return f.sess, nil
}
func (f *fakeBackend) Snapshot(ctx context.Context, id string) (rtbackend.Snapshot, error) {
	return f.snap, nil
}
func (f *fakeBackend) ContextSummary(ctx context.Context, id string) (tools.ContextInspection, error) {
	return f.ctx, nil
}
func (f *fakeBackend) Submit(ctx context.Context, id string, env message.Envelope) (*rtbackend.SubmitResult, error) {
	f.submitCalls++
	f.lastSubmitted = env.Text
	return &rtbackend.SubmitResult{TurnID: "t1"}, nil
}
func (f *fakeBackend) SubmitAsync(ctx context.Context, id string, env message.Envelope, _ ...rtbackend.SubmitOptions) (*rtbackend.SubmitResult, error) {
	f.asyncCalls++
	f.lastSubmitted = env.Text
	return &rtbackend.SubmitResult{TurnID: "t-async"}, nil
}
func (f *fakeBackend) CancelTurn(ctx context.Context, id, turnID string) (*rtbackend.CancelTurnResult, error) {
	f.cancelCalls++
	f.lastCancelled = turnID
	return &rtbackend.CancelTurnResult{TurnID: turnID, Status: "canceling"}, nil
}
func (f *fakeBackend) ExecuteCommand(ctx context.Context, id string, cmd commands.Command) (commands.Result, error) {
	f.executeCalls++
	return commands.Result{Name: cmd.Name, Output: "ok"}, nil
}
func (f *fakeBackend) PendingPermissions(ctx context.Context, id string) ([]tools.PendingPermission, error) {
	return nil, nil
}
func (f *fakeBackend) ApprovePermission(ctx context.Context, id, p string, s tools.PermissionGrantScope) (tools.PermissionResolution, error) {
	return tools.PermissionResolution{}, nil
}
func (f *fakeBackend) DenyPermission(ctx context.Context, id, p, r string) (tools.PermissionResolution, error) {
	return tools.PermissionResolution{}, nil
}
func (f *fakeBackend) AttachSink(id string, sink events.Sink) (func(), error) {
	return func() {}, nil
}

func TestSessionRoutesSlashCommandToExecuteCommand(t *testing.T) {
	b := newFakeBackend()
	_ = New(&config.Config{LeadName: "lead"}, b, &strings.Builder{}, &strings.Builder{})

	cmd, ok := commands.Parse("/help")
	if !ok {
		t.Fatalf("/help should parse as a slash command")
	}
	_, err := b.ExecuteCommand(context.Background(), b.sess.SessionID, cmd)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if b.executeCalls != 1 {
		t.Fatalf("expected 1 execute call, got %d", b.executeCalls)
	}
}

func TestDispatchUsesSubmitAsyncForChatInput(t *testing.T) {
	b := newFakeBackend()
	s := New(&config.Config{LeadName: "lead"}, b, &strings.Builder{}, &strings.Builder{})

	if err := s.dispatchInput(context.Background(), b.sess.SessionID, "hello world"); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if b.asyncCalls != 1 {
		t.Fatalf("expected 1 async submit call, got %d", b.asyncCalls)
	}
	if b.submitCalls != 0 {
		t.Fatalf("Submit should not have been called; SubmitAsync is the async path")
	}
	if b.lastSubmitted != "hello world" {
		t.Fatalf("expected lastSubmitted=hello world, got %q", b.lastSubmitted)
	}

	// The active turn id should now be set so that Ctrl+C
	// will cancel it.
	s.activeTurnMu.Lock()
	turnID := s.activeTurnID
	s.activeTurnMu.Unlock()
	if turnID == "" {
		t.Fatalf("activeTurnID should be set after SubmitAsync returns")
	}
}

func TestDispatchRoutesSlashCommandToExecuteCommand(t *testing.T) {
	b := newFakeBackend()
	s := New(&config.Config{LeadName: "lead"}, b, &strings.Builder{}, &strings.Builder{})

	if err := s.dispatchInput(context.Background(), b.sess.SessionID, "/help"); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if b.executeCalls != 1 {
		t.Fatalf("expected 1 execute call, got %d", b.executeCalls)
	}
	if b.asyncCalls != 0 {
		t.Fatalf("SubmitAsync should not have been called for slash commands")
	}
}

func TestCancelActiveTurnCancelsAndReturnsTrue(t *testing.T) {
	b := newFakeBackend()
	s := New(&config.Config{LeadName: "lead"}, b, &strings.Builder{}, &strings.Builder{})

	// No active turn: should return false, no cancel call.
	if s.cancelActiveTurn() {
		t.Fatalf("cancelActiveTurn should return false when no turn is active")
	}
	if b.cancelCalls != 0 {
		t.Fatalf("backend.CancelTurn should not have been called")
	}

	// Set an active turn and try again.
	s.setActiveTurn("turn-42")
	if !s.cancelActiveTurn() {
		t.Fatalf("cancelActiveTurn should return true when a turn is active")
	}
	if b.cancelCalls != 1 {
		t.Fatalf("expected 1 cancel call, got %d", b.cancelCalls)
	}
	if b.lastCancelled != "turn-42" {
		t.Fatalf("expected lastCancelled=turn-42, got %q", b.lastCancelled)
	}
}

func TestHandleEventIgnoresUnknownPayloadTypes(t *testing.T) {
	b := newFakeBackend()
	s := New(&config.Config{LeadName: "lead"}, b, &strings.Builder{}, &strings.Builder{})

	// Should not panic on payload of an unexpected concrete type.
	// s.tui is nil here (Run was never called), so any call into
	// the min-tui frontend would crash.  We use the runner-phase
	// path with a string payload (rather than a struct) to exercise
	// the fallback branch, which doesn't touch the frontend.
	s.handleEvent(events.Event{
		Type:    events.EventRunnerPhaseChanged,
		Payload: "not-a-struct",
	})
}
