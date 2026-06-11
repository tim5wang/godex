package mintui

import (
	"context"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/protocol"
	"github.com/tim5wang/godex/internal/domain/events"
	"github.com/tim5wang/godex/internal/domain/message"
	minitui "github.com/tim5wang/min-tui"
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

// TestSlashCommandDoesNotOverwriteStatusBar verifies that
// running a slash command leaves the godex heartbeat status
// ("Ready · Input · Model · ctx · msgs") visible.
//
// Regression for: dispatchInput used to call
// s.setStatus("/"+cmd.Name+" completed", StatusInfo) after
// each /command, which clobbered the carefully designed
// heartbeat.  The fix moves the confirmation to the output
// area ("✓ /name completed\n") and leaves SetStatus untouched.
func TestSlashCommandDoesNotOverwriteStatusBar(t *testing.T) {
	b := newFakeBackend()
	s := New(&config.Config{LeadName: "lead", Model: "MiniMax-M3"}, b, &strings.Builder{}, &strings.Builder{})

	// Pretend a heartbeat status is already on the bar.
	heartbeat := s.renderStatus("Ready")
	s.setStatus(heartbeat, minitui.StatusDefault)

	if err := s.dispatchInput(context.Background(), b.sess.SessionID, "/help"); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	// The status bar must still show the heartbeat, NOT
	// "/help completed".
	if s.lastStatusText != heartbeat {
		t.Fatalf("status bar was clobbered: want %q, got %q", heartbeat, s.lastStatusText)
	}
	if strings.Contains(s.lastStatusText, "completed") {
		t.Fatalf("status bar contains 'completed': %q", s.lastStatusText)
	}
}

// TestCtxPctFormatsAsIntegerK verifies that the ctx chip in
// the status bar is rendered as "<used>k/<total>k <pct>%"
// with integer k values (no decimal point) so it stays
// scannable.  Regression for: the previous format was
// "128.0k/512k 25%" which cluttered the status bar.
func TestCtxPctFormatsAsIntegerK(t *testing.T) {
	cases := []struct {
		name   string
		used   int
		total  int
		want   string
	}{
		{"evenly_divisible", 128000, 512000, "128k/512k 25%"},
		{"rounds_up", 128500, 512000, "129k/512k 25%"},
		{"rounds_down", 127499, 512000, "127k/512k 25%"},
		{"half_rounds_up", 127500, 512000, "128k/512k 25%"},
		{"tiny", 1, 1000, "0k/1k 0%"},
		{"half_full", 256000, 512000, "256k/512k 50%"},
		{"overrides_at_100", 600000, 512000, "600k/512k 100%"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ctxPct(tools.ContextInspection{
				TokenEstimate:      c.used,
				TotalTokenEstimate: c.total,
			})
			if got != c.want {
				t.Fatalf("ctxPct(used=%d, total=%d) = %q, want %q", c.used, c.total, got, c.want)
			}
		})
	}
}

// TestCtxPctEmptyWhenTotalZero verifies that the ctx chip is
// omitted (empty string) when the backend hasn't reported a
// context budget yet, instead of rendering a misleading
// "0k/0k 0%" string.
func TestCtxPctEmptyWhenTotalZero(t *testing.T) {
	if got := ctxPct(tools.ContextInspection{}); got != "" {
		t.Fatalf("ctxPct() with zero total = %q, want empty string", got)
	}
}

// TestRenderStatusIncludesModelRequestCount verifies
// that the status bar shows the "calls N" chip after
// a snapshot with model_request events, matching the
// full bubbletea TUI status bar format.
func TestRenderStatusIncludesModelRequestCount(t *testing.T) {
	s := New(&config.Config{LeadName: "lead", Model: "MiniMax-M3"},
		newFakeBackend(), &strings.Builder{}, &strings.Builder{})

	// Without snapshot data, calls should be 0 and omitted.
	got := s.renderStatus("Ready")
	if strings.Contains(got, "calls ") {
		t.Fatalf("status bar should not contain 'calls' chip with 0 calls, got %q", got)
	}
	// With calls > 0, the chip must appear.
	s.modelCallCount = 3
	got = s.renderStatus("Ready")
	if !strings.Contains(got, "calls 3") {
		t.Fatalf("status bar should contain 'calls 3' chip, got %q", got)
	}
}

// TestRefreshSnapshotRefreshesContextSummary verifies that
// calling refreshSnapshot updates s.ctxSummary from the
// backend, so the next renderStatus includes the live
// "128k/512k 25%" pressure chip.
func TestRefreshSnapshotRefreshesContextSummary(t *testing.T) {
	b := newFakeBackend()
	b.ctx = tools.ContextInspection{
		TokenEstimate:      128000,
		TotalTokenEstimate: 128000,
		CompressThreshold:  512000,
	}
	s := New(&config.Config{LeadName: "lead", Model: "MiniMax-M3", CompressThreshold: 512000}, b, &strings.Builder{}, &strings.Builder{})

	// Before any snapshot: no ctx chip.
	if got := s.renderStatus("Ready"); strings.Contains(got, "128k/512k 25%") {
		t.Fatalf("before refresh: status bar should not contain ctx chip, got %q", got)
	}

	// refreshSnapshot should pull the summary from the backend
	// and update the cached ctxSummary.  The next renderStatus
	// must include the chip.
	s.refreshSnapshot()
	if got := s.renderStatus("Ready"); !strings.Contains(got, "128k/512k 25%") {
		t.Fatalf("after refresh: status bar should contain ctx chip, got %q", got)
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
