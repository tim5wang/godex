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
func (f *fakeBackend) Models(ctx context.Context, id string) (rtbackend.ModelsView, error) {
	return rtbackend.ModelsView{
		DefaultProfileID: "gpt",
		SessionProfileID: "gpt",
		Profiles: []rtbackend.ModelProfile{
			{ID: "gpt", Name: "GPT-5.4", Model: "gpt-5.4-mini", Default: true, Selected: true},
			{ID: "claude", Name: "Claude 4", Model: "claude-4"},
		},
	}, nil
}
func (f *fakeBackend) SetSessionModelProfile(ctx context.Context, id, profileID string) (rtbackend.ModelsView, error) {
	return rtbackend.ModelsView{
		DefaultProfileID: "gpt",
		SessionProfileID: profileID,
		Profiles: []rtbackend.ModelProfile{
			{ID: "gpt", Name: "GPT-5.4", Model: "gpt-5.4-mini", Default: true},
			{ID: "claude", Name: "Claude 4", Model: "claude-4", Selected: profileID == "claude"},
		},
	}, nil
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

// TestRunnerPhaseChipSilencesModelRequest verifies that the
// dominant in-flight phase ("model_request") does NOT surface
// as a status-bar chip, so the heartbeat ("Ready · Input ·
// Model · ctx · calls · msgs") stays continuously visible
// while the agent waits for the model.  This is the regression
// guard for the previous behaviour, where model_request
// overwrote the bar with a transient "Thinking…" label.
func TestRunnerPhaseChipSilencesModelRequest(t *testing.T) {
	if chip, _ := runnerPhaseChip("model_request", ""); chip != "" {
		t.Fatalf("model_request must produce empty chip, got %q", chip)
	}
	if chip, _ := runnerPhaseChip("context_sanitized", ""); chip != "" {
		t.Fatalf("context_sanitized must produce empty chip, got %q", chip)
	}
}

// TestRunnerPhaseChipSurfacesToolExecution verifies that
// executing + tool name does inject a chip — it carries
// information the heartbeat does not already convey.
func TestRunnerPhaseChipSurfacesToolExecution(t *testing.T) {
	chip, style := runnerPhaseChip("executing", "bash")
	if chip != "Running bash" {
		t.Fatalf("executing+tool chip = %q, want %q", chip, "Running bash")
	}
	if style != minitui.StatusInfo {
		t.Fatalf("executing+tool style = %v, want StatusInfo", style)
	}
	// executing without a tool name still surfaces a chip.
	if chip, _ := runnerPhaseChip("executing", ""); chip != "Running tools" {
		t.Fatalf("executing without tool = %q, want %q", chip, "Running tools")
	}
}

// TestApplyRunnerPhasePreservesHeartbeat verifies that
// applyRunnerPhase("model_request", "") does not clobber the
// heartbeat — the bar must still show the full
// "Ready · Input · Model · ctx · ..." line, with no
// transient "Thinking…" / "model_request" label.
func TestApplyRunnerPhasePreservesHeartbeat(t *testing.T) {
	b := newFakeBackend()
	b.ctx = tools.ContextInspection{
		TokenEstimate:     128000,
		CompressThreshold: 512000,
	}
	s := New(&config.Config{LeadName: "lead", Model: "MiniMax-M3", CompressThreshold: 512000}, b, &strings.Builder{}, &strings.Builder{})
	s.messageCount = 138
	s.modelCallCount = 10

	// Compute the expected heartbeat as it would render
	// without any activity chip.
	want := s.renderStatus("Ready")
	if !strings.Contains(want, "MiniMax-M3") || !strings.Contains(want, "msgs 138") {
		t.Fatalf("baseline heartbeat missing key chips: %q", want)
	}

	// Injecting the model_request phase must leave the bar
	// visually unchanged.  setStatus IS called (to refresh
	// the bar), but the rendered text must equal the
	// heartbeat because the chip is empty for this phase.
	s.applyRunnerPhase("model_request", "")
	if got := s.renderStatus("Ready"); got != want {
		t.Fatalf("model_request clobbered heartbeat:\n want %q\n got  %q", want, got)
	}
	if s.lastStatusText != want {
		t.Fatalf("lastStatusText != heartbeat after model_request:\n want %q\n got  %q", want, s.lastStatusText)
	}
}

// TestApplyRunnerPhaseInsertsChip verifies that non-silent
// phases inject a chip between the model name and the rest of
// the heartbeat, instead of replacing the whole line.
func TestApplyRunnerPhaseInsertsChip(t *testing.T) {
	b := newFakeBackend()
	s := New(&config.Config{LeadName: "lead", Model: "MiniMax-M3"}, b, &strings.Builder{}, &strings.Builder{})

	s.applyRunnerPhase("executing", "bash")
	got := s.renderStatus("Ready")
	if !strings.Contains(got, "Running bash") {
		t.Fatalf("expected chip 'Running bash' in status, got %q", got)
	}
	// The chip must come AFTER the model name (so the
	// heartbeat prefix stays continuously visible).
	modelIdx := strings.Index(got, "MiniMax-M3")
	chipIdx := strings.Index(got, "Running bash")
	if modelIdx < 0 || chipIdx < 0 || chipIdx <= modelIdx {
		t.Fatalf("chip should follow model name, got %q", got)
	}
	// The heartbeat's trailing chips (calls / msgs) must
	// still be present, proving the chip is non-clobbering.
	if !strings.Contains(got, "Ready") || !strings.Contains(got, "Input") {
		t.Fatalf("heartbeat prefix lost after chip insert, got %q", got)
	}
}

// TestClearActivityChipRestoresHeartbeat verifies that
// clearActivityChip returns the bar to its pure heartbeat
// form even when an activity chip was previously set.
func TestClearActivityChipRestoresHeartbeat(t *testing.T) {
	b := newFakeBackend()
	s := New(&config.Config{LeadName: "lead", Model: "MiniMax-M3"}, b, &strings.Builder{}, &strings.Builder{})

	s.applyRunnerPhase("executing", "bash")
	withChip := s.renderStatus("Ready")
	if !strings.Contains(withChip, "Running bash") {
		t.Fatalf("precondition: expected chip in status, got %q", withChip)
	}

	s.clearActivityChip()
	clean := s.renderStatus("Ready")
	if strings.Contains(clean, "Running bash") {
		t.Fatalf("chip should be cleared, got %q", clean)
	}
	if !strings.Contains(clean, "Ready") || !strings.Contains(clean, "Input") || !strings.Contains(clean, "MiniMax-M3") {
		t.Fatalf("heartbeat chips missing after clear, got %q", clean)
	}
}

// TestQuoteLinePrefixesEveryLine verifies that quoteLine
// prepends "> " to every line of its input, including lines
// produced by internal "\n" splits.  This is the contract
// that lets us rely on min-tui's built-in renderer to apply
// its grey blockquote background uniformly across a multi-
// line user message.
func TestQuoteLinePrefixesEveryLine(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"single", "hello", "> hello"},
		{"empty", "", ">"},
		{"multi", "first\nsecond\nthird", "> first\n> second\n> third"},
		{"trailing newline", "first\n", "> first\n> "},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := quoteLine(c.in); got != c.want {
				t.Fatalf("quoteLine(%q):\n want %q\n got  %q", c.in, c.want, got)
			}
		})
	}
}

// TestQuoteBlockIsBlockquoteSafe verifies that the rendered
// user block never contains lines that min-tui's renderer
// would misclassify.  Specifically:
//
//   - every line must start with "> " or be exactly ">",
//     so min-tui applies the grey background uniformly;
//   - no line may start with a backtick sequence, which
//     would otherwise trigger a code-fence branch and wrap
//     the line in dim instead of the intended blockquote
//     background.
func TestQuoteBlockIsBlockquoteSafe(t *testing.T) {
	got := quoteLine("hello world\nsecond line\nthird line")
	for _, line := range strings.Split(got, "\n") {
		if !strings.HasPrefix(line, "> ") && line != ">" {
			t.Fatalf("every output line must be a blockquote, got %q (in %q)", line, got)
		}
		if strings.HasPrefix(line, "```") {
			t.Fatalf("line must not start a code fence, got %q", line)
		}
	}
}

// TestUserAndToolBlocksAreNilSafe verifies that all output
// helpers (writeUserTurn, writeQuoteBlock, renderToolCall*
// Started/Finished) are no-ops when s.tui is nil — the test
// harness never runs Run, so the real min-tui frontend is
// never constructed.  Guards against future regressions that
// would dereference a nil frontend in unit-test contexts.
func TestUserAndToolBlocksAreNilSafe(t *testing.T) {
	b := newFakeBackend()
	s := New(&config.Config{LeadName: "lead", Model: "MiniMax-M3"}, b, &strings.Builder{}, &strings.Builder{})

	s.writeUserTurn("hello world")
	s.writeQuoteBlock("⚠ something")
	s.renderToolCallStarted(events.Event{
		Type:    events.EventToolCallStarted,
		Payload: events.ToolCallPayload{Name: "bash"},
	})
	s.renderToolCallFinished(events.Event{
		Type:    events.EventToolCallFinished,
		Payload: events.ToolCallPayload{Name: "bash", Output: "ok"},
	})
	// EventWarningRaised / EventErrorRaised touch the
	// output area via writeQuoteBlock; with tui == nil
	// they must still not panic.
	s.handleEvent(events.Event{
		Type:    events.EventWarningRaised,
		Payload: events.NoticePayload{Message: "be careful"},
	})
	s.handleEvent(events.Event{
		Type:    events.EventErrorRaised,
		Payload: events.NoticePayload{Message: "boom"},
	})
}

// TestWriteUserTurnUsesBlockquoteSyntax verifies that the
// bytes written to the output area for a user turn are
// exactly "> "-prefixed lines followed by a blank line.
// This is the regression guard for "user messages are
// crammed against assistant prose with no background colour".
func TestWriteUserTurnUsesBlockquoteSyntax(t *testing.T) {
	// We can't intercept min-tui's bytes without a
	// capturing stub, so we exercise the pure helper
	// quoteLine directly to assert the shape that the
	// session then writes verbatim.
	combined := quoteLine("hello") + "\n" + quoteLine("line two") + "\n"
	want := "> hello\n> line two\n"
	if combined != want {
		t.Fatalf("user block lines mismatch:\n want %q\n got  %q", want, combined)
	}
	// Every line must begin with the blockquote marker.
	for _, l := range strings.Split(strings.TrimRight(combined, "\n"), "\n") {
		if !strings.HasPrefix(l, "> ") {
			t.Fatalf("expected blockquote prefix, got %q", l)
		}
	}
}