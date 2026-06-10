package streaming

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/protocol"
	"github.com/tim5wang/godex/internal/domain/events"
	"github.com/tim5wang/godex/internal/domain/message"
	rtbackend "github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/commands"
	"github.com/tim5wang/godex/internal/tools"
)

// fakeBackend is the test double used in place of the real
// *backend.Service. It records every interaction so we can assert
// that the streaming layer forwards inputs correctly.
type fakeBackend struct {
	mu sync.Mutex

	openSession rtbackend.OpenedSession
	snapshot    rtbackend.Snapshot
	submitted   []message.Envelope
	executed    []commands.Command
	approved    []string
	denied      []string
	pending     []tools.PendingPermission

	sink         events.Sink
	sinkAttached bool
}

func (f *fakeBackend) OpenSession(ctx context.Context, locator rtbackend.SessionLocator) (*rtbackend.OpenedSession, error) {
	_ = ctx
	_ = locator
	return &f.openSession, nil
}

func (f *fakeBackend) Snapshot(ctx context.Context, sessionID string) (rtbackend.Snapshot, error) {
	_ = ctx
	_ = sessionID
	return f.snapshot, nil
}

func (f *fakeBackend) ContextSummary(ctx context.Context, sessionID string) (tools.ContextInspection, error) {
	_ = ctx
	_ = sessionID
	return tools.ContextInspection{}, nil
}

func (f *fakeBackend) Submit(ctx context.Context, sessionID string, envelope message.Envelope) (*rtbackend.SubmitResult, error) {
	_ = ctx
	_ = sessionID
	f.mu.Lock()
	f.submitted = append(f.submitted, envelope)
	f.mu.Unlock()
	return &rtbackend.SubmitResult{SessionID: sessionID, TurnID: "turn-1"}, nil
}

func (f *fakeBackend) ExecuteCommand(ctx context.Context, sessionID string, cmd commands.Command) (commands.Result, error) {
	_ = ctx
	_ = sessionID
	f.mu.Lock()
	f.executed = append(f.executed, cmd)
	f.mu.Unlock()
	return commands.Result{Name: cmd.Name, Output: "ok"}, nil
}

func (f *fakeBackend) PendingPermissions(ctx context.Context, sessionID string) ([]tools.PendingPermission, error) {
	_ = ctx
	_ = sessionID
	return f.pending, nil
}

func (f *fakeBackend) ApprovePermission(ctx context.Context, sessionID, requestID string, scope tools.PermissionGrantScope) (tools.PermissionResolution, error) {
	_ = ctx
	_ = scope
	f.mu.Lock()
	f.approved = append(f.approved, requestID)
	f.mu.Unlock()
	return tools.PermissionResolution{RequestID: requestID, Decision: tools.PermissionAllow}, nil
}

func (f *fakeBackend) DenyPermission(ctx context.Context, sessionID, requestID, reason string) (tools.PermissionResolution, error) {
	_ = ctx
	_ = sessionID
	_ = reason
	f.mu.Lock()
	f.denied = append(f.denied, requestID)
	f.mu.Unlock()
	return tools.PermissionResolution{RequestID: requestID, Decision: tools.PermissionDeny}, nil
}

func (f *fakeBackend) AttachSink(sessionID string, sink events.Sink) (func(), error) {
	_ = sessionID
	f.mu.Lock()
	f.sink = sink
	f.sinkAttached = true
	f.mu.Unlock()
	return func() {}, nil
}

func newFakeBackend(t *testing.T) *fakeBackend {
	t.Helper()
	return &fakeBackend{
		openSession: rtbackend.OpenedSession{
			SessionID: "session-1",
			Locator:   rtbackend.SessionLocator{Channel: "local", Key: "default"},
		},
		snapshot: rtbackend.Snapshot{
			Locator:  rtbackend.SessionLocator{Channel: "local", Key: "default"},
			Messages: []protocol.Message{},
		},
	}
}

func newTestSession(t *testing.T, backend Backend) (*Session, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	out := &bytes.Buffer{}
	errOut := &bytes.Buffer{}
	cfg := &config.Config{LeadName: "lead", Model: "test-model", WorkspaceDir: "/workspace"}
	s := New(cfg, backend, out, errOut)
	return s, out, errOut
}

// TestPrintBannerIncludesLocatorWorkspaceAndModel verifies the
// initial banner prints the session locator, workspace directory,
// and configured model so users can confirm they opened the right
// session.
func TestPrintBannerIncludesLocatorWorkspaceAndModel(t *testing.T) {
	backend := newFakeBackend(t)
	backend.snapshot.Locator = rtbackend.SessionLocator{Channel: "local", Key: "default"}
	s, out, _ := newTestSession(t, backend)
	s.cfg.WorkspaceDir = "/workspace"
	s.cfg.Model = "test-model"
	s.printBanner(backend.snapshot)

	got := out.String()
	for _, want := range []string{"GoDex", "streaming mode", "local:default", "/workspace", "test-model"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected banner to contain %q, got %q", want, got)
		}
	}
}

// TestStreamAssistantTextOpensAndClosesBlock verifies the streaming
// handler prints a "● " prefix on the first delta and closes the
// block with a trailing newline on completion.
func TestStreamAssistantTextOpensAndClosesBlock(t *testing.T) {
	backend := newFakeBackend(t)
	s, out, _ := newTestSession(t, backend)

	s.handleEvent(events.Event{TurnID: "turn-1", Type: events.EventAssistantTextDelta, Payload: events.TextPayload{Text: "hello"}})
	s.handleEvent(events.Event{TurnID: "turn-1", Type: events.EventAssistantTextDelta, Payload: events.TextPayload{Text: " world"}})
	s.handleEvent(events.Event{TurnID: "turn-1", Type: events.EventAssistantMessageComplete, Payload: events.TextPayload{}})

	got := out.String()
	if !strings.HasPrefix(got, "● hello world\n") {
		t.Fatalf("expected output to start with %q, got %q", "● hello world\n", got)
	}
	// The complete event also refreshes the status bar; verify it
	// landed in the same write.
	if !strings.Contains(got, "Assistant replied") {
		t.Fatalf("expected status-bar refresh after completion, got %q", got)
	}
}

// TestStreamAssistantTextSwitchesBlockOnTurnChange verifies that a
// new turn ID closes the previous block and starts a new "● " prefix.
func TestStreamAssistantTextSwitchesBlockOnTurnChange(t *testing.T) {
	backend := newFakeBackend(t)
	s, out, _ := newTestSession(t, backend)

	s.handleEvent(events.Event{TurnID: "turn-1", Type: events.EventAssistantTextDelta, Payload: events.TextPayload{Text: "first"}})
	s.handleEvent(events.Event{TurnID: "turn-2", Type: events.EventAssistantTextDelta, Payload: events.TextPayload{Text: "second"}})
	s.handleEvent(events.Event{TurnID: "turn-2", Type: events.EventAssistantMessageComplete, Payload: events.TextPayload{}})

	got := out.String()
	if !strings.HasPrefix(got, "● first\n● second\n") {
		t.Fatalf("expected output to start with two assistant blocks, got %q", got)
	}
}

// TestHandleEventToolCallLifecycle verifies tool events print on
// start AND on finish lines, with the start line carrying the tool
// name and the finish line carrying a truncated output preview.
func TestHandleEventToolCallLifecycle(t *testing.T) {
	backend := newFakeBackend(t)
	s, out, _ := newTestSession(t, backend)

	s.handleEvent(events.Event{TurnID: "turn-1", Type: events.EventToolCallStarted, Payload: events.ToolCallPayload{
		Name: "bash", Input: map[string]interface{}{"command": "pwd"},
	}})
	s.handleEvent(events.Event{TurnID: "turn-1", Type: events.EventToolCallFinished, Payload: events.ToolCallPayload{
		Name: "bash", Input: map[string]interface{}{"command": "pwd"}, Output: "/workspace",
	}})

	got := out.String()
	for _, want := range []string{"⏺ bash", "✓ bash", "/workspace"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected output to contain %q, got %q", want, got)
		}
	}
}

// TestHandleEventToolCallFailureMarker verifies a failed tool prints
// the failure marker.
func TestHandleEventToolCallFailureMarker(t *testing.T) {
	backend := newFakeBackend(t)
	s, out, _ := newTestSession(t, backend)

	s.handleEvent(events.Event{TurnID: "turn-1", Type: events.EventToolCallStarted, Payload: events.ToolCallPayload{Name: "bash"}})
	s.handleEvent(events.Event{TurnID: "turn-1", Type: events.EventToolCallFinished, Payload: events.ToolCallPayload{
		Name: "bash", Error: "exit code1",
	}})

	got := out.String()
	if !strings.Contains(got, "✗ bash") || !strings.Contains(got, "exit code1") {
		t.Fatalf("expected failure marker, got %q", got)
	}
}

// TestHandleEventTodoListRendersCheckboxes verifies the todo list
// event renders with [x]/[] markers.
func TestHandleEventTodoListRendersCheckboxes(t *testing.T) {
	backend := newFakeBackend(t)
	s, out, _ := newTestSession(t, backend)

	s.handleEvent(events.Event{TurnID: "turn-1", Type: events.EventTodoListUpdated, Payload: events.TodoListPayload{
		Items: []events.TodoItemPayload{
			{Content: "Inspect changes", Status: "completed"},
			{Content: "Run tests", Status: "pending"},
		},
	}})

	got := out.String()
	for _, want := range []string{"[x] Inspect changes", "[] Run tests"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected todo output to contain %q, got %q", want, got)
		}
	}
}

// TestHandleEventNoticeRoutesToStderr verifies warning/error events
// go to the stderr buffer, not the chat output.
func TestHandleEventNoticeRoutesToStderr(t *testing.T) {
	backend := newFakeBackend(t)
	s, out, errOut := newTestSession(t, backend)

	s.handleEvent(events.Event{TurnID: "turn-1", Type: events.EventWarningRaised, Payload: events.NoticePayload{Message: "careful"}})
	s.handleEvent(events.Event{TurnID: "turn-1", Type: events.EventErrorRaised, Payload: events.NoticePayload{Message: "broken"}})

	if !strings.Contains(errOut.String(), "Warning: careful") {
		t.Fatalf("expected warning on stderr, got %q", errOut.String())
	}
	if !strings.Contains(errOut.String(), "Error: broken") {
		t.Fatalf("expected error on stderr, got %q", errOut.String())
	}
	if out.Len() != 0 {
		t.Fatalf("expected stdout to stay empty, got %q", out.String())
	}
}

// TestHandleEventTurnCompletedEndsBlock verifies a turn-complete
// event closes any in-flight assistant block and transitions to
// idle state.
func TestHandleEventTurnCompletedEndsBlock(t *testing.T) {
	backend := newFakeBackend(t)
	s, out, _ := newTestSession(t, backend)

	s.handleEvent(events.Event{TurnID: "turn-1", Type: events.EventAssistantTextDelta, Payload: events.TextPayload{Text: "partial"}})
	s.handleEvent(events.Event{TurnID: "turn-1", Type: events.EventTurnCompleted, Payload: events.TurnPayload{Status: "completed"}})

	if !strings.Contains(out.String(), "● partial\n") {
		t.Fatalf("expected partial block to end with newline, got %q", out.String())
	}
	if !strings.Contains(out.String(), "Turn completed") {
		t.Fatalf("expected status bar to mention Turn completed, got %q", out.String())
	}
	if s.working {
		t.Fatal("expected working=false after turn completion")
	}
}

// TestHandleEventTurnCompletedIsIdempotent verifies repeated
// turn-complete events do not double-print the assistant block, but
// each one does refresh the status bar.
func TestHandleEventTurnCompletedIsIdempotent(t *testing.T) {
	backend := newFakeBackend(t)
	s, out, _ := newTestSession(t, backend)

	s.handleEvent(events.Event{TurnID: "turn-1", Type: events.EventAssistantTextDelta, Payload: events.TextPayload{Text: "partial"}})
	s.handleEvent(events.Event{TurnID: "turn-1", Type: events.EventAssistantMessageComplete, Payload: events.TextPayload{}})
	s.handleEvent(events.Event{TurnID: "turn-1", Type: events.EventAssistantMessageComplete, Payload: events.TextPayload{}})

	got := out.String()
	if strings.Count(got, "● partial\n") !=1 {
		t.Fatalf("expected exactly one assistant block, got %q", got)
	}
	if strings.Count(got, "Assistant replied") !=2 {
		t.Fatalf("expected two status-bar refreshes, got %d in %q", strings.Count(got, "Assistant replied"), got)
	}
}

// TestDispatchInputSubmitsTurn verifies a non-slash line is
// forwarded to Submit with the configured sender name.
func TestDispatchInputSubmitsTurn(t *testing.T) {
	backend := newFakeBackend(t)
	s, _, _ := newTestSession(t, backend)

	ctx := context.Background()
	if err := s.dispatchInput(ctx, "session-1", "hello world"); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	backend.mu.Lock()
	defer backend.mu.Unlock()
	if len(backend.submitted) != 1 {
		t.Fatalf("expected one submission, got %d", len(backend.submitted))
	}
	if backend.submitted[0].Text != "hello world" {
		t.Fatalf("unexpected text: %q", backend.submitted[0].Text)
	}
}

// TestDispatchInputRoutesSlashCommand verifies a slash line goes to
// ExecuteCommand and not Submit.
func TestDispatchInputRoutesSlashCommand(t *testing.T) {
	backend := newFakeBackend(t)
	s, out, _ := newTestSession(t, backend)

	ctx := context.Background()
	if err := s.dispatchInput(ctx, "session-1", "/tasks"); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	backend.mu.Lock()
	defer backend.mu.Unlock()
	if len(backend.executed) != 1 || backend.executed[0].Name != "tasks" {
		t.Fatalf("expected /tasks to execute, got %+v", backend.executed)
	}
	if len(backend.submitted) != 0 {
		t.Fatalf("expected no submission for slash command, got %d", len(backend.submitted))
	}
	if !strings.Contains(out.String(), "ok") {
		t.Fatalf("expected command output in stdout, got %q", out.String())
	}
}

// TestPrintHistoryPrintsMostRecentMessages verifies a freshly
// opened session prints its most recent messages to stdout so the
// scrollback is consistent with the backend.
func TestPrintHistoryPrintsMostRecentMessages(t *testing.T) {
	backend := newFakeBackend(t)
	backend.snapshot.Messages = []protocol.Message{
		protocol.NewTextMessage(protocol.RoleUser, "first"),
		protocol.NewTextMessage(protocol.RoleAssistant, "second"),
	}
	s, out, _ := newTestSession(t, backend)
	s.printHistory(backend.snapshot)

	got := out.String()
	for _, want := range []string{"›", "first", "● second"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected history to contain %q, got %q", want, got)
		}
	}
}

// TestPrintHistoryTruncatesLongHistory verifies a long history
// prints a leading truncation notice before the last30 messages.
func TestPrintHistoryTruncatesLongHistory(t *testing.T) {
	backend := newFakeBackend(t)
	messages := make([]protocol.Message, 100)
	for i := range messages {
		messages[i] = protocol.NewTextMessage(protocol.RoleUser, "msg")
	}
	backend.snapshot.Messages = messages
	s, out, _ := newTestSession(t, backend)
	s.printHistory(backend.snapshot)

	got := out.String()
	if !strings.Contains(got, "showing last 30 of 100") {
		t.Fatalf("expected truncation notice, got %q", got)
	}
}

// TestLineEditorRuneBufferParity verifies the rune-buffer primitives
// agree with byte offsets so cursor movement stays grapheme-aware.
func TestLineEditorRuneBufferParity(t *testing.T) {
	content := []rune("héllo")
	for _, pos := range []int{0, 1, 2, 3, 4, 5} {
		bytePos := runeOffsetToByteOffset(content, pos)
		back := byteOffsetToRuneOffset(content, bytePos)
		if back != pos {
			t.Fatalf("round-trip mismatch: rune pos %d -> byte %d -> rune %d", pos, bytePos, back)
		}
	}
}

// TestLineEditorGraphemeCursorOnMultiByteChar verifies left/right
// arrow movement on a2-byte UTF-8 character hops by grapheme, not
// by byte.
func TestLineEditorGraphemeCursorOnMultiByteChar(t *testing.T) {
	// "é" is a2-byte UTF-8 sequence (0xC30xA9).
	line := "héllo"
	// Position of the "é" character is byte1, runes1.
	if left := graphemeCursorLeft(line, 1); left != 0 {
		t.Fatalf("expected left from byte1 to byte0, got %d", left)
	}
	if right := graphemeCursorRight(line, 0); right != 1 {
		t.Fatalf("expected right from byte0 to byte1, got %d", right)
	}
}

// TestIsInterruptRecognizesEOF verifies the isInterrupt helper
// classifies io.EOF as a graceful exit, not an error.
func TestIsInterruptRecognizesEOF(t *testing.T) {
	if !isInterrupt(io.EOF) {
		t.Fatal("expected io.EOF to be classified as interrupt")
	}
	if isInterrupt(errors.New("some other error")) {
		t.Fatal("expected a non-EOF error not to be classified as interrupt")
	}
}

// TestMarkWorkingAndIdle flips the working flag in a way that
// concurrent setStatus/markWorking calls cannot deadlock on the
// editor mutex.
func TestMarkWorkingAndIdle(t *testing.T) {
	backend := newFakeBackend(t)
	s, _, _ := newTestSession(t, backend)

	done := make(chan struct{})
	go func() {
		s.markWorking()
		s.markIdle()
		s.setStatusOverride("hello")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("working/idle/status cycle deadlocked")
	}
}

// TestStreamAssistantTextEmptyDeltaIgnored verifies a zero-length
// delta does not open an empty "● " block.
func TestStreamAssistantTextEmptyDeltaIgnored(t *testing.T) {
	backend := newFakeBackend(t)
	s, out, _ := newTestSession(t, backend)

	s.handleEvent(events.Event{TurnID: "turn-1", Type: events.EventAssistantTextDelta, Payload: events.TextPayload{Text: ""}})

	if out.Len() != 0 {
		t.Fatalf("expected empty output for empty delta, got %q", out.String())
	}
}

// TestHandleEventCommandCompletedPrintsOutput verifies a successful
// slash command completion prints its Output to stdout.
func TestHandleEventCommandCompletedPrintsOutput(t *testing.T) {
	backend := newFakeBackend(t)
	s, out, _ := newTestSession(t, backend)

	s.handleEvent(events.Event{TurnID: "cmd-1", Type: events.EventCommandCompleted, Payload: events.CommandPayload{
		Name: "tasks", Output: "TASKS OK",
	}})

	if !strings.Contains(out.String(), "TASKS OK") {
		t.Fatalf("expected command output on stdout, got %q", out.String())
	}
}

// TestHandleEventCommandCompletedErrorMarker verifies a failed
// slash command prints an error marker.
func TestHandleEventCommandCompletedErrorMarker(t *testing.T) {
	backend := newFakeBackend(t)
	s, out, _ := newTestSession(t, backend)

	s.handleEvent(events.Event{TurnID: "cmd-1", Type: events.EventCommandCompleted, Payload: events.CommandPayload{
		Name: "tasks", Error: "boom",
	}})

	if !strings.Contains(out.String(), "Command error: boom") {
		t.Fatalf("expected error marker, got %q", out.String())
	}
}

// TestRenderStatusBarReadyShape verifies the idle status bar
// carries the model name and the focus chip and looks like the
// legacy TUI's "Ready · Focus: Input" line.
func TestRenderStatusBarReadyShape(t *testing.T) {
	backend := newFakeBackend(t)
	s, _, _ := newTestSession(t, backend)
	s.cfg.Model = "test-model"
	s.editorMu.Lock()
	s.width =200
	text := s.renderStatusBar()
	s.editorMu.Unlock()

	for _, want := range []string{"Ready", "Input", "test-model"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected status bar to contain %q, got %q", want, text)
		}
	}
}

// TestRenderStatusBarContextChip verifies the status bar shows the
// context usage chip once the context summary has data.
func TestRenderStatusBarContextChip(t *testing.T) {
	backend := newFakeBackend(t)
	s, _, _ := newTestSession(t, backend)
	s.cfg.Model = "test-model"
	s.editorMu.Lock()
	s.contextSummary = tools.ContextInspection{
		TotalTokenEstimate:6800,
		CompressThreshold:256000,
	}
	s.width =200
	text := s.renderStatusBar()
	s.editorMu.Unlock()

	for _, want := range []string{"6.8k/256k", "3%"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected status bar to contain %q, got %q", want, text)
		}
	}
}

// TestRenderStatusBarModelCallCountChip verifies the calls chip
// appears only when at least one model_request event has been seen.
func TestRenderStatusBarModelCallCountChip(t *testing.T) {
	backend := newFakeBackend(t)
	s, _, _ := newTestSession(t, backend)
	s.editorMu.Lock()
	s.modelCallCount =5
	s.messageCount =2
	s.width =200
	text := s.renderStatusBar()
	s.editorMu.Unlock()

	for _, want := range []string{"calls 5", "msgs 2"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected status bar to contain %q, got %q", want, text)
		}
	}
}

// TestRenderStatusBarPermissionBlockerChip verifies the blocker
// chip appears only when ActivePermissionBlocker is non-nil.
func TestRenderStatusBarPermissionBlockerChip(t *testing.T) {
	backend := newFakeBackend(t)
	s, _, _ := newTestSession(t, backend)
	s.editorMu.Lock()
	s.pendingApproval = &rtbackend.PermissionBlocker{
		RequestID: "perm-1",
		ToolName: "bash",
		Action: "exec",
		Expiry: "expires in 4m",
	}
	s.width =200
	text := s.renderStatusBar()
	s.editorMu.Unlock()

	for _, want := range []string{"Blocked by approval", "perm-1", "bash exec"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected status bar to contain %q, got %q", want, text)
		}
	}
}

// TestRenderStatusBarWorkingPhase verifies a model_request runner
// phase flips the base label from Ready to a "Thinking" chip.
func TestRenderStatusBarWorkingPhase(t *testing.T) {
	backend := newFakeBackend(t)
	s, _, _ := newTestSession(t, backend)
	s.editorMu.Lock()
	s.working = true
	s.workingSince = s.now().Add(-3 * time.Second)
	s.activePhase = "model_request"
	s.width =200
	text := s.renderStatusBar()
	s.editorMu.Unlock()

	if !strings.Contains(text, "Thinking") {
		t.Fatalf("expected status bar to mention Thinking, got %q", text)
	}
}

// TestRecordRunnerPhaseCountsModelRequest verifies a single
// model_request runner phase event bumps the counter by one and a
// duplicate event does not bump it again.
func TestRecordRunnerPhaseCountsModelRequest(t *testing.T) {
	backend := newFakeBackend(t)
	s, _, _ := newTestSession(t, backend)

	at := time.Unix(1700000000,0)
	event := events.Event{
		TurnID: "turn-1",
		Type: events.EventRunnerPhaseChanged,
		Timestamp: at,
		Payload: events.RunnerPhasePayload{Phase: "model_request", Iteration:1},
	}
	s.recordRunnerPhase(event)
	s.recordRunnerPhase(event)
	s.recordRunnerPhase(events.Event{
		TurnID: "turn-1",
		Type: events.EventRunnerPhaseChanged,
		Timestamp: at.Add(time.Second),
		Payload: events.RunnerPhasePayload{Phase: "model_request", Iteration:2},
	})

	if s.modelCallCount !=2 {
		t.Fatalf("expected two distinct model_request events, got %d", s.modelCallCount)
	}
}

// TestApplySnapshotPullsMessageCountAndBlocker verifies the
// bookkeeping helper copies the fields the status bar needs.
func TestApplySnapshotPullsMessageCountAndBlocker(t *testing.T) {
	backend := newFakeBackend(t)
	s, _, _ := newTestSession(t, backend)

	snap := &rtbackend.Snapshot{
		Messages: []protocol.Message{
			protocol.NewTextMessage(protocol.RoleUser, "a"),
			protocol.NewTextMessage(protocol.RoleAssistant, "b"),
		},
		ActivePermissionBlocker: &rtbackend.PermissionBlocker{RequestID: "perm-1"},
	}
	s.applySnapshot(snap)

	if s.messageCount !=2 {
		t.Fatalf("expected messageCount=2, got %d", s.messageCount)
	}
	if s.pendingApproval == nil || s.pendingApproval.RequestID != "perm-1" {
		t.Fatalf("expected blocker perm-1, got %+v", s.pendingApproval)
	}
}

// TestFormatElapsed verifies the duration formatter produces the
// expected shape for the common buckets we display in the status bar.
func TestFormatElapsed(t *testing.T) {
	cases := []struct {
		d time.Duration
		want string
	}{
		{500 * time.Millisecond, "<1s"},
		{3 * time.Second, "3s"},
		{65 * time.Second, "1m5s"},
		{120 * time.Second, "2m"},
	}
	for _, c := range cases {
		if got := formatElapsed(c.d); got != c.want {
			t.Fatalf("formatElapsed(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

// TestEllipsizeANSILeavesShortStringsAlone verifies the ellipsizer
// only truncates strings that would otherwise overflow.
func TestEllipsizeANSILeavesShortStringsAlone(t *testing.T) {
	if got := ellipsizeANSI("hello",100); got != "hello" {
		t.Fatalf("expected short string to pass through, got %q", got)
	}
	if got := ellipsizeANSI("hello",3); !strings.HasSuffix(got, "\u2026") {
		t.Fatalf("expected long string to be ellipsized, got %q", got)
	}
}
