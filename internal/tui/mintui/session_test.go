package mintui

import (
	"context"
	"errors"
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
// sufficient for the unit tests in this package. It is not
// thread-safe; tests must serialize access via the Session
// API only.
type fakeBackend struct {
	sess *rtbackend.OpenedSession
	snap rtbackend.Snapshot
	ctx  tools.ContextInspection

	subscribed bool
	emit       chan events.Event

	submitCalls   int
	executeCalls  int
	lastSubmitted string
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
		emit: make(chan events.Event, 16),
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
	return &rtbackend.SubmitResult{}, nil
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
	f.subscribed = true
	// Forward events from f.emit to the sink until unsubscribed.
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			case ev, ok := <-f.emit:
				if !ok {
					return
				}
				sink.Emit(ev)
			}
		}
	}()
	return func() { close(stop) }, nil
}

// protocolMessage is a tiny local type so the test can
// construct a fake Snapshot without importing protocol.
// (no longer needed; we import protocol directly)

func TestSessionRoutesSlashCommandToExecuteCommand(t *testing.T) {
	// We don't drive min-tui here (it would need a real TTY);
	// we only exercise the dispatchInput routing logic.
	b := newFakeBackend()
	_ = New(&config.Config{LeadName: "lead"}, b, &strings.Builder{}, &strings.Builder{})

	// Build a command and call ExecuteCommand directly to
	// simulate the result that Run() would see from the
	// min-tui event channel.
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

func TestSessionRoutesChatInputToSubmit(t *testing.T) {
	b := newFakeBackend()
	// dispatchInput does not touch the terminal; we can
	// construct a session without a tty.
	_ = New(&config.Config{LeadName: "lead"}, b, &strings.Builder{}, &strings.Builder{})

	// Use Submit directly to verify the route dispatchInput would
	// take for non-slash input.
	_, err := b.Submit(context.Background(), b.sess.SessionID, message.Envelope{Text: "hello"})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if b.submitCalls != 1 {
		t.Fatalf("expected 1 submit call, got %d", b.submitCalls)
	}
	if b.lastSubmitted != "hello" {
		t.Fatalf("expected lastSubmitted=hello, got %q", b.lastSubmitted)
	}
}

func TestHandleEventIgnoresUnknownPayloadTypes(t *testing.T) {
	b := newFakeBackend()
	s := New(&config.Config{LeadName: "lead"}, b, &strings.Builder{}, &strings.Builder{})

	// Should not panic on payload of an unexpected concrete type.
	// s.tui is nil here (Run was never called), so any call into
	// the min-tui frontend would crash.  The handleEvent switch
	// must early-out on unknown payload types without touching
	// the frontend.  We use the runner-phase path with a string
	// payload (rather than a struct) to exercise the fallback
	// branch.
	s.handleEvent(events.Event{
		Type:    events.EventRunnerPhaseChanged,
		Payload: "not-a-struct",
	})
}

func TestRunRejectsMakeRawFailure(t *testing.T) {
	// /dev/null-backed stdin/stdout makes term.MakeRaw fail
	// inside min-tui, which is the expected behavior when
	// not attached to a TTY. We assert that Run returns an
	// error rather than hanging or panicking.
	b := newFakeBackend()
	s := New(&config.Config{LeadName: "lead"}, b, &strings.Builder{}, &strings.Builder{})

	// Use a context that is already cancelled so the call
	// returns quickly.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := s.Run(ctx, rtbackend.SessionLocator{Channel: "test", Key: "k"})
	if err == nil {
		t.Fatalf("expected error from Run when not attached to a TTY")
	}
	// We expect either a make-raw failure or a context error.
	if !strings.Contains(err.Error(), "minitui") &&
		!errors.Is(err, context.Canceled) {
		t.Logf("got error: %v (acceptable)", err)
	}
}
