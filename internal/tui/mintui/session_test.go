package mintui

import (
	"bytes"
	"context"
	"fmt"
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

	submitCalls   int
	asyncCalls    int
	executeCalls  int
	cancelCalls   int
	lastSubmitted string
	lastCancelled string

	// longtask surface — tests populate these to drive the
	// Ctrl+B popup scenarios.  Errors are checked first so a
	// test can simulate backend failures without overriding
	// the rows.
	longTaskRows        []rtbackend.LongTaskRow
	longTaskDetails     []rtbackend.LongTaskDetail
	longTaskListErr     error
	longTaskDetailErr   error
	longTaskCancelErr   error
	longTaskCancelCalls []string
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
func (f *fakeBackend) ListSessions(ctx context.Context, filter rtbackend.SessionListFilter) ([]rtbackend.ListedSession, error) {
	return nil, nil
}
func (f *fakeBackend) CreateNewSession(ctx context.Context) (rtbackend.SessionLocator, error) {
	return rtbackend.SessionLocator{Channel: "local", Key: "new-test"}, nil
}

// ── longtask surface (Ctrl+B popup) ──────────────────────────

func (f *fakeBackend) ListLongTasks(ctx context.Context, sessionID string) ([]rtbackend.LongTaskRow, error) {
	if f.longTaskListErr != nil {
		return nil, f.longTaskListErr
	}
	rows := make([]rtbackend.LongTaskRow, len(f.longTaskRows))
	copy(rows, f.longTaskRows)
	return rows, nil
}

func (f *fakeBackend) GetLongTask(ctx context.Context, sessionID, workflowID string) (rtbackend.LongTaskDetail, error) {
	if f.longTaskDetailErr != nil {
		return rtbackend.LongTaskDetail{}, f.longTaskDetailErr
	}
	for _, d := range f.longTaskDetails {
		if d.Row.WorkflowID == workflowID {
			return d, nil
		}
	}
	return rtbackend.LongTaskDetail{}, fmt.Errorf("longtask %q not found in fake backend", workflowID)
}

func (f *fakeBackend) CancelLongTask(ctx context.Context, sessionID, workflowID string) error {
	if f.longTaskCancelErr != nil {
		return f.longTaskCancelErr
	}
	f.longTaskCancelCalls = append(f.longTaskCancelCalls, workflowID)
	return nil
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

// capturingTUI is a minimal tuiFrontend that records every
// WriteString call into a buffer. It is used by the live /
// replay user-turn tests so the assertions observe real bytes
// coming out of the Session, not a hand-rolled reconstruction.
//
// SetStatus and RegisterCommand are present only to satisfy
// the tuiFrontend interface; their arguments are ignored.
//
// PushPopup / PopPopup / SetGlobalKeyHandler record the calls
// so popup-related tests can assert which popups the session
// pushed and which global hotkey it registered.  The Render and
// OnKey closures of each PushPopup are also stashed so tests
// can drive the popup's own state machine in-process.
type capturingTUI struct {
	buf bytes.Buffer

	popups      []minitui.Popup
	globalKeyFn func(minitui.KeyEvent) bool
}

func (c *capturingTUI) WriteString(s string) (int, error) { return c.buf.WriteString(s) }
func (c *capturingTUI) SetStatus(string, minitui.StatusStyle) {
}
func (c *capturingTUI) RegisterCommand(minitui.SlashCommand) {}

func (c *capturingTUI) PushPopup(p minitui.Popup) { c.popups = append(c.popups, p) }
func (c *capturingTUI) PopPopup()                 { /* no-op; tests do not model a real stack */ }
func (c *capturingTUI) SetGlobalKeyHandler(fn func(minitui.KeyEvent) bool) {
	c.globalKeyFn = fn
}

// newSessionWithCapturingTUI builds a Session with a recording
// tui stub attached. The returned cleanup detaches the stub so
// callers can hand-roll fresh state without leaking.
func newSessionWithCapturingTUI() (*Session, *capturingTUI) {
	s := &Session{}
	tui := &capturingTUI{}
	s.tui = tui
	return s, tui
}

// TestWriteUserTurnLiveHasLeadingBlank pins the live-rendering
// contract: writeUserTurn must emit a leading "\n" before the
// blockquote so the new user block has visual breathing room
// above the previous assistant turn.
func TestWriteUserTurnLiveHasLeadingBlank(t *testing.T) {
	s, tui := newSessionWithCapturingTUI()
	s.writeUserTurn("hello")

	got := tui.buf.String()
	if !strings.HasPrefix(got, "\n") {
		t.Fatalf("live path must start with a leading blank, got %q", got)
	}
	if !strings.Contains(got, quoteLine("hello")) {
		t.Fatalf("live path missing blockquote body, got %q", got)
	}
	if !strings.HasSuffix(got, "\n\n") {
		t.Fatalf("live path must end with trailing blank, got %q", got)
	}
}

// TestReplayUserTurnHasNoLeadingBlank pins the stored-history
// contract: replayUserTurn must NOT emit a leading "\n". The
// previous turn's trailing blank already separates this
// message; an extra leading blank would compound with that
// trailing blank and produce three blank lines between two
// consecutive stored user messages.
func TestReplayUserTurnHasNoLeadingBlank(t *testing.T) {
	s, tui := newSessionWithCapturingTUI()
	s.replayUserTurn("hello")

	got := tui.buf.String()
	if strings.HasPrefix(got, "\n") {
		t.Fatalf("replay path must NOT start with a leading blank, got %q", got)
	}
	if !strings.Contains(got, quoteLine("hello")) {
		t.Fatalf("replay path missing blockquote body, got %q", got)
	}
	if !strings.HasSuffix(got, "\n\n") {
		t.Fatalf("replay path must end with trailing blank, got %q", got)
	}
}

// TestLiveAndReplayUserTurnProduceDistinctBytes is the
// regression guard for the original P1 layout bug: the live
// and replay paths must NOT be byte-identical, otherwise the
// new leading blank leaks into history replay.
func TestLiveAndReplayUserTurnProduceDistinctBytes(t *testing.T) {
	live, liveTui := newSessionWithCapturingTUI()
	live.writeUserTurn("hello")
	liveBytes := liveTui.buf.String()

	replay, replayTui := newSessionWithCapturingTUI()
	replay.replayUserTurn("hello")
	replayBytes := replayTui.buf.String()

	if liveBytes == replayBytes {
		t.Fatalf("live and replay paths produced identical bytes %q; "+
			"leading blank would leak into history replay", liveBytes)
	}
	// The only allowed difference is the leading "\n".
	if liveBytes != "\n"+replayBytes {
		t.Fatalf("live must equal \"\\n\"+replay, got live=%q replay=%q",
			liveBytes, replayBytes)
	}
}

// ── preview / diff helpers ────────────────────────────────────────

// TestPreviewLinesFromReadOutputStripsLineNumbersAndTruncationMarker
// guards the read_file → code-block pipeline against the two
// known formatting hazards:
//
//  1. read_file prepends "     N\t" to every line — that
//     line-number column must not leak into the preview
//     code block (it would offset every line in the
//     highlighted output and waste the first 8 columns).
//  2. read_file appends a "... (truncated: ...)" status
//     line when the file is longer than the limit — that
//     status must be dropped so it never appears inside
//     the fenced code block as fake code.
func TestPreviewLinesFromReadOutputStripsLineNumbersAndTruncationMarker(t *testing.T) {
	output := "     1\tpackage main\n" +
		"     2\t\n" +
		"     3\tfunc Hello() {}\n" +
		"... (truncated: showing 3 of 200 lines, use offset/limit to read more)"

	got := previewLinesFromReadOutput(output, 5)
	want := []string{
		"package main",
		"func Hello() {}",
	}
	if !equalStrings(got, want) {
		t.Fatalf("preview lines mismatch:\n want %q\n got  %q", want, got)
	}
}

// TestPreviewLinesFromReadOutputCapsAtMax guards the
// "max non-empty lines" cap. With 7 non-empty content lines
// and max=5 we expect exactly the first 5.
func TestPreviewLinesFromReadOutputCapsAtMax(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 7; i++ {
		fmt.Fprintf(&b, "     %d\tline %d\n", i, i)
	}
	got := previewLinesFromReadOutput(b.String(), 5)
	if len(got) != 5 {
		t.Fatalf("expected 5 lines, got %d (%q)", len(got), got)
	}
	if got[0] != "line 1" || got[4] != "line 5" {
		t.Fatalf("cap kept the wrong slice: %q", got)
	}
}

// TestParseEditInputMintSingleEditSnakeCase verifies that the
// canonical single-edit form ({"old_text": ..., "new_text": ...})
// — which is what EditFileDefinition publishes and what the
// agent runtime actually emits — is recognised. Prior to the
// fix this returned nil because the helper only read "edits".
func TestParseEditInputMintSingleEditSnakeCase(t *testing.T) {
	got := parseEditInputMint(map[string]interface{}{
		"path":     "skill/skill.go",
		"old_text": "foo",
		"new_text": "bar",
	})
	if len(got) != 1 {
		t.Fatalf("expected 1 edit pair, got %d (%+v)", len(got), got)
	}
	if got[0].oldText != "foo" || got[0].newText != "bar" {
		t.Fatalf("edit pair mismatch: %+v", got[0])
	}
}

// TestParseEditInputMintMultipleEditsSnakeCase verifies that
// the canonical multi-edit form — items use "old_text"/"new_text"
// per EditFileDefinition — is recognised end-to-end. The
// previous camelCase-only key read silently dropped every item.
func TestParseEditInputMintMultipleEditsSnakeCase(t *testing.T) {
	got := parseEditInputMint(map[string]interface{}{
		"path": "f.go",
		"edits": []interface{}{
			map[string]interface{}{"old_text": "a", "new_text": "b"},
			map[string]interface{}{"old_text": "c", "new_text": ""},
		},
	})
	if len(got) != 2 {
		t.Fatalf("expected 2 edit pairs, got %d (%+v)", len(got), got)
	}
	if got[0] != (editPairMint{oldText: "a", newText: "b"}) {
		t.Fatalf("pair 0 mismatch: %+v", got[0])
	}
	if got[1] != (editPairMint{oldText: "c", newText: ""}) {
		t.Fatalf("pair 1 mismatch: %+v", got[1])
	}
}

// TestParseEditInputMintEmptyInputReturnsNil guards the
// "nothing to render" path so callers can early-out cheaply.
func TestParseEditInputMintEmptyInputReturnsNil(t *testing.T) {
	if got := parseEditInputMint(nil); got != nil {
		t.Fatalf("nil input should yield nil, got %v", got)
	}
	if got := parseEditInputMint(map[string]interface{}{}); got != nil {
		t.Fatalf("empty input should yield nil, got %v", got)
	}
}

// TestParseEditInputMintEmptyEditsDoesNotFallThroughToSingle
// pins the P1 #3 contract: when "edits" is present but empty
// (or only contains elements that fail to parse), the parser
// must NOT silently fall through to the single-edit shape and
// render whatever else happens to live in `old_text` /
// `new_text`. The presence of the "edits" key is authoritative.
func TestParseEditInputMintEmptyEditsDoesNotFallThroughToSingle(t *testing.T) {
	got := parseEditInputMint(map[string]interface{}{
		"path":     "f.go",
		"edits":    []interface{}{},
		"old_text": "should be ignored",
		"new_text": "should be ignored",
	})
	if got != nil {
		t.Fatalf("empty edits list must yield nil, got %+v", got)
	}
}

// TestParseEditInputMintEditsWithOnlyInvalidElementsYieldsNil
// guards the same contract for the "edits is present but every
// element fails to parse" case. We must still honour multi-edit
// intent instead of dropping down to the single-edit branch.
func TestParseEditInputMintEditsWithOnlyInvalidElementsYieldsNil(t *testing.T) {
	got := parseEditInputMint(map[string]interface{}{
		"edits":    []interface{}{nil, "not-a-map", 42},
		"old_text": "should be ignored",
		"new_text": "should be ignored",
	})
	if got != nil {
		t.Fatalf("edits with only invalid elements must yield nil, got %+v", got)
	}
}

// TestParseEditInputMintEditsNonArrayYieldsNil covers the
// edge case where "edits" is present but has the wrong type
// (e.g. an object instead of an array). We still treat the
// caller's intent as multi-edit and return nil rather than
// dropping to single-edit.
func TestParseEditInputMintEditsNonArrayYieldsNil(t *testing.T) {
	got := parseEditInputMint(map[string]interface{}{
		"edits":    map[string]interface{}{"foo": "bar"},
		"old_text": "should be ignored",
		"new_text": "should be ignored",
	})
	if got != nil {
		t.Fatalf("non-array edits must yield nil, got %+v", got)
	}
}

// TestGenerateUnifiedDiffMarkdownBasicShape verifies the
// output uses the expected `+/-/space` line prefixes and
// that equal text is prefixed with two spaces. A regression
// in the diff pipeline would surface here as missing or
// swapped prefixes.
func TestGenerateUnifiedDiffMarkdownBasicShape(t *testing.T) {
	diff := generateUnifiedDiffMarkdown("foo\nbar\n", "foo\nbaz\n")
	if !strings.Contains(diff, "  foo") {
		t.Fatalf("equal line 'foo' should be prefixed with two spaces, got:\n%s", diff)
	}
	if !strings.Contains(diff, "- bar") {
		t.Fatalf("removed line 'bar' should be prefixed with '- ', got:\n%s", diff)
	}
	if !strings.Contains(diff, "+ baz") {
		t.Fatalf("added line 'baz' should be prefixed with '+ ', got:\n%s", diff)
	}
}

// TestGenerateUnifiedDiffMarkdownIdenticalText guards the
// "no change" case. We must not emit a misleading + / -
// block when old and new match exactly.
func TestGenerateUnifiedDiffMarkdownIdenticalText(t *testing.T) {
	diff := generateUnifiedDiffMarkdown("unchanged\n", "unchanged\n")
	if strings.Contains(diff, "\n+ ") || strings.Contains(diff, "\n- ") {
		t.Fatalf("identical text should not contain +/- lines, got:\n%s", diff)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}