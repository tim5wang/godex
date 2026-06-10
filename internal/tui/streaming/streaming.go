// Package streaming implements the scrollback-streaming TUI mode.
//
// In this mode the conversation history is written line-by-line to
// stdout and relies on the terminal's native scrollback buffer for
// history navigation. There is no alt-screen, no viewport diffing,
// and no renderer loop. The bottom of the screen is reserved for the
// line editor (input prompt) and a status bar that is updated in
// place using ANSI escape sequences.
//
// Layout:
//
// ┌──────────────────────────────────────────┐
// │ Header (printed once at startup) │
// │ GoDex · model · workspace │
// │──────────────────────────────────────────│
// │ conversation history (stdout, terminal │
// │ scrolls naturally — no viewport) │
// │──────────────────────────────────────────│
// │ > _ (line editor) │
// │ status bar (ANSI in-place update) │
// └──────────────────────────────────────────┘
//
// The status bar carries the same information the legacy bubbletea
// TUI's `renderHeartbeatLine` prints (Ready / Working / Thinking,
// focus chip, model name, context usage, model call count, message
// count) but in a flatter, terminal-scrollback-friendly layout.
//
// Concurrency model:
//
// - main goroutine: reads keystrokes via lineEditor.
// - event goroutine: receives backend events from the sink channel
// and writes formatted output to stdout under printMu.
// - SIGWINCH goroutine: updates s.width on terminal resize so the
// status bar ellipsizes correctly.
//
// All stdout writes are serialized through printMu so a status-bar
// refresh never interleaves with a streaming text delta. The line
// editor and the status bar share the bottom rows; cross-region
// redraws go through editorMu.
package streaming

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/x/term"
	"github.com/rivo/uniseg"

	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/protocol"
	"github.com/tim5wang/godex/internal/domain/events"
	"github.com/tim5wang/godex/internal/domain/message"
	rtbackend "github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/commands"
	"github.com/tim5wang/godex/internal/tools"
)

// statusFocus mirrors tui.focusMode but only the values that the
// streaming TUI exposes. It exists so the status bar can render the
// same "Focus: Input / Workbench" hint the legacy TUI prints without
// pulling in the bubbletea model.
type statusFocus int

const (
	focusInput statusFocus = iota
	focusWorkbench
)

// Session runs the streaming TUI loop against the shared runtime
// backend. It is the streaming-mode counterpart of tui.Session.
type Session struct {
	cfg *config.Config
	backend Backend
	stdout io.Writer
	stderr io.Writer
	now func() time.Time
	printMu sync.Mutex

	// editorMu guards the working flag, the focus mode, and the
	// other status-bar fields that the line editor and the bottom
	// status bar share. All cross-region redraws go through this
	// mutex so we never tear the bottom rows.
	editorMu sync.Mutex

	// working indicates the agent is producing output and the input
	// prompt should be replaced with a "waiting" placeholder.
	working bool

	// workingSince tracks when the current turn started so the status
	// bar can show an elapsed-time indicator while working.
	workingSince time.Time

	// activePhase / activeToolName mirror the legacy TUI's runner
	// phase tracking. They drive the "Thinking3s" / "Executing bash"
	// labels in the status bar.
	activePhase string
	activeToolName string

	// focus tracks which region the user is interacting with. The
	// streaming TUI currently exposes only "Input" because the
	// workbench lives behind a separate command, but we still surface
	// the focus hint so muscle-memory for the legacy TUI carries over.
	focus statusFocus

	// pendingApproval is the most recent pending permission blocker.
	// While non-nil the status bar shows a "Blocked by approval" chip
	// alongside the regular fields.
	pendingApproval *rtbackend.PermissionBlocker

	// contextSummary is the latest context-usage snapshot from
	// backend.ContextSummary. Refreshed after each turn completion
	// and on demand via the snapshot ready event.
	contextSummary tools.ContextInspection

	// modelCallCount counts the number of model_request runner-phase
	// events seen so far in this session. Drives the "calls N" chip.
	modelCallCount int

	// seenModelCallEvent tracks the dedup key of model_request events
	// so the same event emitted twice (e.g. backend replay) does not
	// inflate the call counter.
	seenModelCallEvent map[string]struct{}

	// messageCount is the number of messages in the most recent
	// snapshot. Refreshed whenever a snapshot arrives.
	messageCount int

	// sessionID is the opened session id, kept here so renderStatus
	// does not need to thread it through every call.
	sessionID string

	// locator is the session locator (channel:key) used by the
	// banner and the status bar.
	locator rtbackend.SessionLocator

	// statusOverride is a free-form status message that takes
	// precedence over the auto-derived labels (Ready / Thinking /
	// etc.) until the next runner phase change or turn completion.
	statusOverride string

	// width is the latest terminal width observed via SIGWINCH or
	// term.GetSize. Used to ellipsize the status bar so we never wrap
	// onto a second line.
	width int

	// currentTurnID tracks the assistant turn we are appending text
	// to, so consecutive deltas land on the same logical block.
	currentTurnID string

	// lastAssistantBlank records whether the most recent output to
	// the assistant block ended in a newline. We use this to avoid
	// printing redundant blank lines between tool calls.
	lastAssistantBlank bool
}

// Backend is the surface area of the runtime backend that the
// streaming TUI depends on. Mirrors the legacy tui.Backend interface
// but trimmed to what we actually use here.
type Backend interface {
	OpenSession(context.Context, rtbackend.SessionLocator) (*rtbackend.OpenedSession, error)
	Snapshot(context.Context, string) (rtbackend.Snapshot, error)
	ContextSummary(context.Context, string) (tools.ContextInspection, error)
	Submit(context.Context, string, message.Envelope) (*rtbackend.SubmitResult, error)
	ExecuteCommand(context.Context, string, commands.Command) (commands.Result, error)
	PendingPermissions(context.Context, string) ([]tools.PendingPermission, error)
	ApprovePermission(context.Context, string, string, tools.PermissionGrantScope) (tools.PermissionResolution, error)
	DenyPermission(context.Context, string, string, string) (tools.PermissionResolution, error)
	AttachSink(string, events.Sink) (func(), error)
}

// New creates a streaming TUI session bound to the given backend.
func New(cfg *config.Config, backend Backend, stdout, stderr io.Writer) *Session {
	width, _, _ := term.GetSize(os.Stdout.Fd())
	if width <= 0 {
		width =100
	}
	return &Session{
		cfg: cfg,
		backend: backend,
		stdout: stdout,
		stderr: stderr,
		now: time.Now,
		focus: focusInput,
		width: width,
		seenModelCallEvent: make(map[string]struct{}),
	}
}

// Run starts the streaming loop for the given session locator.
//
// The loop:
//
//1. opens (or resumes) a session via the backend;
//2. prints the header and an initial snapshot of recent history;
//3. attaches an event sink that streams new content to stdout;
//4. reads input from the line editor and submits it.
//
// Ctrl+C cancels the current agent turn (if any) and exits on the
// second press. Ctrl+D exits immediately.
func (s *Session) Run(ctx context.Context, locator rtbackend.SessionLocator) error {
	opened, err := s.backend.OpenSession(ctx, locator)
	if err != nil {
		return fmt.Errorf("open streaming session: %w", err)
	}
	s.sessionID = opened.SessionID
	s.locator = locator

	initial, err := s.backend.Snapshot(ctx, opened.SessionID)
	if err != nil {
		return fmt.Errorf("load streaming snapshot: %w", err)
	}
	s.applySnapshot(&initial)

	unsubscribe, err := s.backend.AttachSink(opened.SessionID, events.SinkFunc(s.handleEvent))
	if err != nil {
		return fmt.Errorf("attach streaming event sink: %w", err)
	}
	defer unsubscribe()

	// Best-effort initial context summary. Failure here is non-fatal
	// because the status bar just omits the ctx chip until the next
	// snapshot refresh surfaces one.
	if summary, err := s.backend.ContextSummary(ctx, opened.SessionID); err == nil {
		s.editorMu.Lock()
		s.contextSummary = summary
		s.editorMu.Unlock()
	}

	// Listen for SIGWINCH so the status bar can ellipsize itself to
	// the new terminal width. We do not interrupt the line editor
	// mid-keystroke; the next loop iteration redraws the bottom rows
	// with the new width.
	winchCh := make(chan os.Signal,1)
	signal.Notify(winchCh, syscall.SIGWINCH)
	defer signal.Stop(winchCh)
	go s.watchWidth(winchCh)

	s.printBanner(initial)
	s.printHistory(initial)
	// Print the initial layout: separator, prompt placeholder,
	// status bar. After this the cursor sits on the input prompt row
	// ready for the line editor to take over.
	s.printInitialBottom()

	ed := newLineEditor("> ")

	for {
		// While the agent is producing output we replace the prompt
		// with a "waiting" placeholder. We don't disable the editor
		// outright so that an interrupt (Ctrl+C) is still possible.
		if s.working {
			s.drawPrompt("(waiting for response...)")
		} else {
			s.drawPrompt("")
		}
		s.refreshStatusBar()

		line, err := ed.ReadLine()
		if err != nil {
			if errors.Is(err, io.EOF) || isInterrupt(err) {
				return nil
			}
			return fmt.Errorf("read input: %w", err)
		}

		input := strings.TrimSpace(line)
		if input == "" {
			continue
		}

		if err := s.dispatchInput(ctx, opened.SessionID, input); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			s.printf(s.stderr, "Error: %v\n", err)
		}
	}
}

// dispatchInput routes a user-typed line. Slash commands go to
// ExecuteCommand; everything else goes to Submit as a chat turn.
func (s *Session) dispatchInput(ctx context.Context, sessionID, input string) error {
	if cmd, ok := commands.Parse(input); ok {
		result, err := s.backend.ExecuteCommand(ctx, sessionID, cmd)
		if result.Output != "" {
			s.println(s.stdout, result.Output)
		}
		if result.DispatchError != "" {
			s.println(s.stdout, "Error: "+result.DispatchError)
		}
		s.setStatusOverride("/" + cmd.Name + " completed")
		s.refreshStatusBar()
		return err
	}

	res, err := s.backend.Submit(ctx, sessionID, message.NewCLIEnvelope(sessionID, s.cfg.LeadName, input, s.now()))
	if res != nil && res.PendingApproval {
		items, pendingErr := s.backend.PendingPermissions(ctx, sessionID)
		if pendingErr != nil {
			s.printf(s.stderr, "Warning: failed to load pending approvals: %v\n", pendingErr)
		} else {
			s.println(s.stdout, renderPendingApproval(res.PendingRequestID, sessionID, items))
		}
	}
	if err == nil {
		s.markWorking()
		s.setStatusOverride("Submitted, waiting for assistant")
		s.refreshStatusBar()
	}
	return err
}

// handleEvent is invoked by the backend on every event the TUI has
// subscribed to. It writes user-facing content to stdout under the
// print mutex and updates the status-bar fields.
func (s *Session) handleEvent(event events.Event) {
	switch event.Type {
	case events.EventAssistantTextDelta:
		if payload, ok := event.Payload.(events.TextPayload); ok && payload.Text != "" {
			s.streamAssistantText(event.TurnID, payload.Text)
		}
	case events.EventAssistantMessageComplete:
		s.finishAssistantBlock(event.TurnID)
		s.markIdle()
		s.setStatusOverride("Assistant replied")
		s.refreshStatusBar()
	case events.EventUserMessageAccepted:
		if payload, ok := event.Payload.(events.MessagePayload); ok {
			s.println(s.stdout, renderUserLine(payload.Sender, payload.Text))
		}
	case events.EventToolCallStarted:
		if payload, ok := event.Payload.(events.ToolCallPayload); ok {
			if payload.Name == "todo_write" {
				return
			}
			s.println(s.stdout, renderToolStartLine(payload.Name, payload.Input))
		}
	case events.EventToolCallFinished:
		if payload, ok := event.Payload.(events.ToolCallPayload); ok {
			if payload.Name == "todo_write" && strings.TrimSpace(payload.Error) == "" {
				return
			}
			s.println(s.stdout, renderToolFinishLine(payload.Name, payload.Output, payload.Error))
		}
	case events.EventTodoListUpdated:
		if payload, ok := event.Payload.(events.TodoListPayload); ok {
			s.println(s.stdout, renderTodoList(payload))
		}
	case events.EventCommandCompleted:
		if payload, ok := event.Payload.(events.CommandPayload); ok {
			if payload.Error != "" {
				s.println(s.stdout, "Command error: "+payload.Error)
			} else if payload.Output != "" {
				s.println(s.stdout, payload.Output)
			}
			s.markIdle()
			s.setStatusOverride("/" + payload.Name + " completed")
			s.refreshStatusBar()
		}
	case events.EventWarningRaised:
		if payload, ok := event.Payload.(events.NoticePayload); ok && payload.Message != "" {
			s.println(s.stderr, "Warning: "+payload.Message)
		}
	case events.EventErrorRaised:
		if payload, ok := event.Payload.(events.NoticePayload); ok && payload.Message != "" {
			s.println(s.stderr, "Error: "+payload.Message)
		}
	case events.EventRunnerPhaseChanged:
		s.recordRunnerPhase(event)
		s.refreshStatusBar()
	case events.EventTurnCompleted:
		s.finishAssistantBlock(event.TurnID)
		s.markIdle()
		if payload, ok := event.Payload.(events.TurnPayload); ok {
			s.setStatusOverride("Turn " + payload.Status)
		} else {
			s.setStatusOverride("Turn completed")
		}
		// Refresh context summary in the background; the next status
		// bar draw will pick it up.
		go s.refreshContextSummary()
		s.refreshStatusBar()
	case events.EventSnapshotReady:
		// Fetch the snapshot on a background goroutine so the
		// event sink stays responsive. The next status bar draw will
		// pick up the new messageCount / pendingApproval.
		go s.refreshSnapshot()
	}
}

// applySnapshot copies the bookkeeping fields the status bar needs
// out of the snapshot into the session. Caller must hold no locks;
// this acquires editorMu.
func (s *Session) applySnapshot(snap *rtbackend.Snapshot) {
	if snap == nil {
		return
	}
	s.editorMu.Lock()
	defer s.editorMu.Unlock()
	s.messageCount = len(snap.Messages)
	s.pendingApproval = snap.ActivePermissionBlocker
	s.rebuildModelCallStatsFromTimeline(snap.Timeline)
}

// refreshSnapshot reloads the snapshot from the backend and applies
// the bookkeeping fields. Safe to call from a background goroutine.
func (s *Session) refreshSnapshot() {
	if s.sessionID == "" {
		return
	}
	snap, err := s.backend.Snapshot(context.Background(), s.sessionID)
	if err != nil {
		return
	}
	s.applySnapshot(&snap)
	s.refreshStatusBar()
}

// refreshContextSummary reloads the context summary from the
// backend. Safe to call from a background goroutine.
func (s *Session) refreshContextSummary() {
	if s.sessionID == "" {
		return
	}
	summary, err := s.backend.ContextSummary(context.Background(), s.sessionID)
	if err != nil {
		return
	}
	s.editorMu.Lock()
	s.contextSummary = summary
	s.editorMu.Unlock()
	s.refreshStatusBar()
}

// watchWidth listens for SIGWINCH and updates s.width so the status
// bar ellipsizes correctly. Runs until the channel is closed.
func (s *Session) watchWidth(ch <-chan os.Signal) {
	for range ch {
		w, _, err := term.GetSize(os.Stdout.Fd())
		if err != nil || w <= 0 {
			continue
		}
		s.editorMu.Lock()
		s.width = w
		s.editorMu.Unlock()
		s.refreshStatusBar()
	}
}

// streamAssistantText appends text to the in-progress assistant block.
// The first delta opens a "● " prefix; subsequent deltas are appended
// inline. We don't try to update the existing line in-place — the
// terminal scrollback handles wrapping and line breaks naturally.
func (s *Session) streamAssistantText(turnID, text string) {
	s.printMu.Lock()
	defer s.printMu.Unlock()

	if s.currentTurnID != turnID {
		// New turn: close the previous block (if any) and start fresh.
		if s.currentTurnID != "" && !s.lastAssistantBlank {
			fmt.Fprintln(s.stdout)
			s.lastAssistantBlank = true
		}
		s.currentTurnID = turnID
		fmt.Fprint(s.stdout, "● ")
		s.lastAssistantBlank = false
	}
	fmt.Fprint(s.stdout, text)
}

// finishAssistantBlock ends the assistant block for the given turn
// with a trailing newline. Safe to call multiple times.
func (s *Session) finishAssistantBlock(turnID string) {
	s.printMu.Lock()
	defer s.printMu.Unlock()
	if s.currentTurnID != "" && !s.lastAssistantBlank {
		fmt.Fprintln(s.stdout)
		s.lastAssistantBlank = true
	}
	if s.currentTurnID == turnID {
		s.currentTurnID = ""
	}
}

func (s *Session) markWorking() {
	s.editorMu.Lock()
	s.working = true
	s.workingSince = s.now()
	s.editorMu.Unlock()
}

func (s *Session) markIdle() {
	s.editorMu.Lock()
	s.working = false
	s.workingSince = time.Time{}
	s.activePhase = ""
	s.activeToolName = ""
	s.editorMu.Unlock()
}

func (s *Session) setStatusOverride(text string) {
	s.editorMu.Lock()
	s.statusOverride = text
	s.editorMu.Unlock()
}

// recordRunnerPhase updates the activePhase / activeToolName fields
// from an EventRunnerPhaseChanged and bumps the model-call counter
// when the phase is "model_request".
func (s *Session) recordRunnerPhase(event events.Event) {
	payload, ok := event.Payload.(events.RunnerPhasePayload)
	if !ok {
		return
	}
	s.editorMu.Lock()
	defer s.editorMu.Unlock()
	s.activePhase = payload.Phase
	s.activeToolName = payload.ToolName
	if payload.Phase == "model_request" {
		key := modelCallEventKey(event)
		if _, dup := s.seenModelCallEvent[key]; !dup {
			s.seenModelCallEvent[key] = struct{}{}
			s.modelCallCount++
		}
	}
	s.statusOverride = "" // auto-derived labels win again
}

// rebuildModelCallStatsFromTimeline walks a snapshot's timeline once
// to rebuild the model-call counter, mirroring the legacy TUI's
// rebuildModelCallStats.
func (s *Session) rebuildModelCallStatsFromTimeline(timeline []events.Event) {
	s.seenModelCallEvent = make(map[string]struct{})
	s.modelCallCount =0
	for _, event := range timeline {
		if event.Type != events.EventRunnerPhaseChanged {
			continue
		}
		payload, ok := event.Payload.(events.RunnerPhasePayload)
		if !ok {
			continue
		}
		if payload.Phase != "model_request" {
			continue
		}
		key := modelCallEventKey(event)
		if _, dup := s.seenModelCallEvent[key]; dup {
			continue
		}
		s.seenModelCallEvent[key] = struct{}{}
		s.modelCallCount++
	}
}

// modelCallEventKey produces a dedup key for a model_request event.
// It intentionally mirrors the legacy TUI's implementation so the
// two TUIs agree on what counts as a "call".
func modelCallEventKey(event events.Event) string {
	payload, ok := event.Payload.(events.RunnerPhasePayload)
	phase := ""
	iter :=0
	if ok {
		phase = payload.Phase
		iter = payload.Iteration
	}
	return fmt.Sprintf("%s|%s|%s|%s|%d", event.Type, event.TurnID, event.Timestamp.UTC().Format(time.RFC3339Nano), phase, iter)
}

// printInitialBottom writes the prompt row + the first status bar
// row, ending with the cursor on the prompt line so the line editor
// can take over.
func (s *Session) printInitialBottom() {
	s.printMu.Lock()
	defer s.printMu.Unlock()
	sep := strings.Repeat("─", s.width)
	fmt.Fprintln(s.stdout, sep)
	fmt.Fprint(s.stdout, "> ")
	fmt.Fprintf(s.stdout, "\x1b[2m%s\x1b[0m", s.renderStatusBar())
	fmt.Fprintln(s.stdout)
}

// drawPrompt rewrites the input prompt region. `placeholder` is shown
// in dim gray when the agent is working; empty string shows the live
// line editor prompt.
//
// The line editor expects to be in charge of its own prompt row.
// We only normalize the cursor position before the editor starts so
// any stale ANSI sequences from a previous status-bar refresh are
// cleared.
func (s *Session) drawPrompt(placeholder string) {
	s.editorMu.Lock()
	defer s.editorMu.Unlock()
	s.printMu.Lock()
	defer s.printMu.Unlock()
	if placeholder == "" {
		fmt.Fprint(s.stdout, "\r\x1b[K> ")
		return
	}
	fmt.Fprintf(s.stdout, "\r\x1b[K\x1b[2m> %s\x1b[0m", placeholder)
}

// refreshStatusBar rewrites the status bar in place. It assumes the
// cursor is on the prompt row (one row above the status bar). We
// move down one row, clear it, redraw, then move back up to the
// prompt row.
//
// Safe to call from any goroutine.
func (s *Session) refreshStatusBar() {
	s.editorMu.Lock()
	width := s.width
	text := s.renderStatusBar()
	s.editorMu.Unlock()

	s.printMu.Lock()
	defer s.printMu.Unlock()
	// \x1b[1B: down1 row (to status bar)
	// \r: column0
	// \x1b[K: clear to end of line
	// text + dim reset
	// \x1b[1A: back up to prompt row
	fmt.Fprintf(s.stdout, "\x1b[1B\r\x1b[K\x1b[2m%s\x1b[0m\x1b[1A", ellipsizeANSI(text, width))
}

// renderStatusBar builds the compact status bar text from the
// current session state. Caller must hold editorMu.
//
// Layout: Ready · Input · MiniMax-M3 ·6.8k/256k3% · calls5 · msgs2
//
// Working turns prepend a heartbeat label:
//
// Thinking3s · Input · MiniMax-M3 · ...
func (s *Session) renderStatusBar() string {
	parts := make([]string,0,8)

	//1. base status (Ready / Working / Thinking / Executing ...)
	parts = append(parts, s.baseStatusLabel())

	//2. focus
	parts = append(parts, s.focusLabel())

	//3. model name
	if model := s.modelLabel(); model != "" {
		parts = append(parts, model)
	}

	//4. context usage (only if summary has data)
	if ctx := s.contextUsageLabel(); ctx != "" {
		parts = append(parts, ctx)
	}

	//5. permission blocker (only when pending)
	if block := s.permissionBlockerLabel(); block != "" {
		parts = append(parts, block)
	}

	//6. call count
	if s.modelCallCount >0 {
		parts = append(parts, fmt.Sprintf("calls %d", s.modelCallCount))
	}

	//7. message count
	if s.messageCount >0 {
		parts = append(parts, fmt.Sprintf("msgs %d", s.messageCount))
	}

	//8. transient override (slash command results, transient errors)
	if s.statusOverride != "" {
		parts = append(parts, s.statusOverride)
	}

	return strings.Join(parts, " \u00b7 ")
}

// baseStatusLabel produces the leading "Ready" / "Thinking3s" /
// "Executing bash" label. Mirrors the legacy TUI's baseRuntimeStatus.
//
// The free-form statusOverride (slash command results, transient
// errors) is appended at the end of renderStatusBar so we do not
// duplicate it here.
func (s *Session) baseStatusLabel() string {
	if !s.working && s.statusOverride == "" {
		return "Ready"
	}
	if !s.working {
		// When idle but a status override is set (slash command just
		// ran) we still want a "Ready" prefix so the override reads
		// naturally: "Ready · /tasks completed".
		return "Ready"
	}
	elapsed := ""
	if !s.workingSince.IsZero() {
		elapsed = formatElapsed(s.now().Sub(s.workingSince))
	}
	phase := strings.TrimSpace(s.activePhase)
	tool := strings.TrimSpace(s.activeToolName)
	switch {
	case tool != "":
		return "Executing " + tool + " " + elapsed
	case phase == "model_request", phase == "context_sanitized":
		return "Thinking " + elapsed
	case phase == "awaiting_tools":
		return "Awaiting tools " + elapsed
	case phase == "tools_completed":
		return "Processing results " + elapsed
	case phase == "final_response":
		return "Writing response " + elapsed
	case phase == "recovery_attempted":
		return "Recovering " + elapsed
	case phase == "interrupted", phase == "error":
		return "Handling error " + elapsed
	case phase == "injection_drained":
		return "Processing input " + elapsed
	default:
		return "Working " + elapsed
	}
}

// focusLabel returns the focus chip. In streaming mode the focus is
// always Input because the workbench is a separate command, but we
// still surface the chip so users can tell which keys are live.
func (s *Session) focusLabel() string {
	switch s.focus {
	case focusWorkbench:
		return "Workbench"
	default:
		return "Input"
	}
}

// modelLabel returns the configured model name. We use cfg.Model
// directly for now; a later iteration can resolve the active profile
// to pull a friendlier display name the way the legacy TUI does.
func (s *Session) modelLabel() string {
	if s.cfg == nil {
		return ""
	}
	name := strings.TrimSpace(s.cfg.Model)
	if name == "" {
		return ""
	}
	return name
}

// contextUsageLabel produces the "ctx6.8k/256k3%" chip. Returns
// empty string when no context usage is known so the caller can omit
// the chip entirely.
func (s *Session) contextUsageLabel() string {
	total := s.contextSummary.TotalTokenEstimate
	if total <= 0 {
		total = s.contextSummary.TokenEstimate
	}
	if total <= 0 {
		total = s.contextSummary.TokenBreakdown.Total
	}
	if total <= 0 {
		return ""
	}
	threshold := s.contextSummary.CompressThreshold
	if threshold <= 0 && s.cfg != nil {
		threshold = s.cfg.CompressThreshold
	}
	if threshold <= 0 {
		return "ctx " + formatCompactNumber(total)
	}
	pct := int(math.Round(float64(total) / float64(threshold) *100))
	if pct <0 {
		pct =0
	}
	if pct >100 {
		pct =100
	}
	return fmt.Sprintf("%s/%s %d%%", formatCompactNumber(total), formatCompactNumber(threshold), pct)
}

// permissionBlockerLabel returns the "Blocked by approval" chip when
// a pending approval is blocking the turn. Empty otherwise.
func (s *Session) permissionBlockerLabel() string {
	if s.pendingApproval == nil {
		return ""
	}
	parts := []string{"Blocked by approval"}
	if id := strings.TrimSpace(s.pendingApproval.RequestID); id != "" {
		parts = append(parts, id)
	}
	action := strings.TrimSpace(s.pendingApproval.ToolName) + " " + strings.TrimSpace(s.pendingApproval.Action)
	action = strings.Join(strings.Fields(action), " ")
	if action != "" {
		parts = append(parts, action)
	}
	if expiry := strings.TrimSpace(s.pendingApproval.Expiry); expiry != "" {
		parts = append(parts, expiry)
	}
	return strings.Join(parts, " ")
}

// formatElapsed renders a duration as "1s", "12s", "2m3s", etc.
func formatElapsed(d time.Duration) string {
	if d <0 {
		d =0
	}
	if d < time.Second {
		return "<1s"
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	minutes := int(d.Minutes())
	seconds := int(d.Seconds()) - minutes*60
	if seconds == 0 {
		return fmt.Sprintf("%dm", minutes)
	}
	return fmt.Sprintf("%dm%ds", minutes, seconds)
}

// formatCompactNumber formats a token count with k/m suffixes.
func formatCompactNumber(value int) string {
	switch {
	case value >=1_000_000:
		return fmt.Sprintf("%.1fm", float64(value)/1_000_000)
	case value >=10_000:
		return fmt.Sprintf("%dk", value/1000)
	case value >=1_000:
		return fmt.Sprintf("%.1fk", float64(value)/1_000)
	default:
		return fmt.Sprintf("%d", value)
	}
}

// ellipsizeANSI truncates a string to fit maxWidth display columns,
// accounting for ANSI escape sequences so a `\x1b[2m...\x1b[0m`
// wrapper does not get cut in half. We approximate by counting only
// printable runes; this is good enough for the short status chips.
func ellipsizeANSI(s string, maxWidth int) string {
	if maxWidth <= 0 {
		return s
	}
	// Walk the string, skipping CSI sequences (\x1b[...m).
	width :=0
	out := strings.Builder{}
	inEscape := false
	for _, r := range s {
		if r == 0x1b {
			inEscape = true
			out.WriteRune(r)
			continue
		}
		if inEscape {
			out.WriteRune(r)
			if r == 'm' || r == 'K' || r == 'A' || r == 'B' {
				inEscape = false
			}
			continue
		}
		w := uniseg.StringWidth(string(r))
		if width+w > maxWidth-1 {
			out.WriteString("\u2026")
			break
		}
		width += w
		out.WriteRune(r)
	}
	return out.String()
}

// printBanner prints the initial header that lives at the top of the
// scrollback buffer.
func (s *Session) printBanner(snap rtbackend.Snapshot) {
	s.println(s.stdout, "🤖 GoDex · streaming mode")
	s.println(s.stdout, fmt.Sprintf(" session %s:%s", snap.Locator.Channel, snap.Locator.Key))
	if s.cfg.WorkspaceDir != "" {
		s.println(s.stdout, " workspace "+s.cfg.WorkspaceDir)
	}
	if s.cfg.Model != "" {
		s.println(s.stdout, " model "+s.cfg.Model)
	}
	s.println(s.stdout, "")
}

// printHistory prints the existing messages from a freshly opened
// session so the scrollback buffer is consistent with what the
// backend knows. We do not print every message from a long history;
// only the most recent N to avoid dumping megabytes into the buffer.
func (s *Session) printHistory(snap rtbackend.Snapshot) {
	const maxRecent =30
	msgs := snap.Messages
	if len(msgs) > maxRecent {
		s.println(s.stdout, fmt.Sprintf("… showing last %d of %d messages; press /history to inspect older entries …", maxRecent, len(msgs)))
		s.println(s.stdout, "")
		msgs = msgs[len(msgs)-maxRecent:]
	}
	for _, msg := range msgs {
		s.printStoredMessage(msg)
	}
}

func (s *Session) printStoredMessage(msg protocol.Message) {
	text := protocol.MessageText(msg)
	switch strings.ToLower(strings.TrimSpace(msg.Role)) {
	case "user":
		s.println(s.stdout, renderUserLine("", text))
	case "assistant":
		if text != "" {
			s.println(s.stdout, "● "+text)
			s.println(s.stdout, "")
		}
	}
}

func (s *Session) println(w io.Writer, args ...interface{}) {
	s.printMu.Lock()
	defer s.printMu.Unlock()
	fmt.Fprintln(w, args...)
}

func (s *Session) printf(w io.Writer, format string, args ...interface{}) {
	s.printMu.Lock()
	defer s.printMu.Unlock()
	fmt.Fprintf(w, format, args...)
}

// isInterrupt reports whether the error from lineEditor.ReadLine was
// caused by a Ctrl+C press. Our editor returns io.EOF for Ctrl+C, but
// we still guard against implementations that surface it differently.
func isInterrupt(err error) bool {
	return err != nil && (errors.Is(err, io.EOF) || strings.Contains(err.Error(), "interrupt"))
}

// ---- input handling ----

// lineEditor is a minimal grapheme-aware line editor that reads one
// line from stdin and writes the prompt/echo to stdout. It is a
// near-clone of internal/runtime/repl.editor — duplicated here so
// the streaming package does not pull in the full REPL surface area.
type lineEditor struct {
	prompt string
	content []rune
	pos int
}

func newLineEditor(prompt string) *lineEditor {
	return &lineEditor{prompt: prompt}
}

// ReadLine reads one line of input from os.Stdin.
//
// The editor puts stdin in raw mode so it can read individual
// keystrokes (including ANSI escape sequences for arrow keys).
// The terminal is restored before ReadLine returns.
func (e *lineEditor) ReadLine() (string, error) {
	state, err := term.MakeRaw(os.Stdin.Fd())
	if err != nil {
		return "", fmt.Errorf("line editor: enter raw mode: %w", err)
	}
	defer term.Restore(os.Stdin.Fd(), state)

	e.content = e.content[:0]
	e.pos =0

	for {
		buf := make([]byte,64)
		n, err := os.Stdin.Read(buf)
		if err != nil {
			return "", err
		}
		b := buf[:n]

		if b[0] == '\x1b' && n >1 {
			e.handleEscape(b)
			continue
		}

		for i :=0; i < n; {
			r, size := utf8.DecodeRune(b[i:])
			if r == utf8.RuneError && size ==1 {
				i++
				continue
			}
			switch {
			case r == '\r' || r == '\n':
				fmt.Fprint(os.Stdout, "\r\n")
				return string(e.content), nil
			case r == 0x03 || r == 0x04:
				fmt.Fprint(os.Stdout, "\r\n")
				return "", io.EOF
			case r == 0x7f || r == 0x08:
				e.deleteRuneBefore()
			case r == 0x15:
				e.content = e.content[:0]
				e.pos =0
			default:
				e.insertRune(r)
			}
			i += size
		}

		e.draw()
	}
}

func (e *lineEditor) handleEscape(b []byte) {
	if len(b) <3 {
		return
	}
	if b[0] != '\x1b' || b[1] != '[' {
		return
	}
	switch b[2] {
	case 'C':
		e.moveCursorRight()
	case 'D':
		e.moveCursorLeft()
	}
}

func (e *lineEditor) draw() {
	line := string(e.content)
	before := string(e.content[:e.pos])
	cursorCol := uniseg.StringWidth(e.prompt) + uniseg.StringWidth(before)
	fmt.Fprintf(os.Stdout, "\r%s%s\x1b[K", e.prompt, line)
	if cursorCol >0 {
		fmt.Fprintf(os.Stdout, "\x1b[%dG", cursorCol+1)
	}
}

func (e *lineEditor) insertRune(r rune) {
	e.content = append(e.content,0)
	copy(e.content[e.pos+1:], e.content[e.pos:])
	e.content[e.pos] = r
	e.pos++
}

func (e *lineEditor) deleteRuneBefore() {
	if e.pos <= 0 {
		return
	}
	e.pos--
	e.content = append(e.content[:e.pos], e.content[e.pos+1:]...)
}

func (e *lineEditor) moveCursorLeft() {
	if e.pos <= 0 {
		return
	}
	line := string(e.content[:e.pos])
	bytePos := runeOffsetToByteOffset(e.content[:e.pos], e.pos)
	newBytePos := graphemeCursorLeft(line, bytePos)
	e.pos = byteOffsetToRuneOffset(e.content, newBytePos)
	e.draw()
}

func (e *lineEditor) moveCursorRight() {
	if e.pos >= len(e.content) {
		return
	}
	line := string(e.content)
	bytePos := runeOffsetToByteOffset(e.content, e.pos)
	newBytePos := graphemeCursorRight(line, bytePos)
	e.pos = byteOffsetToRuneOffset(e.content, newBytePos)
	e.draw()
}

func runeOffsetToByteOffset(runes []rune, pos int) int {
	if pos <= 0 {
		return 0
	}
	if pos >= len(runes) {
		pos = len(runes)
	}
	offset :=0
	for i :=0; i < pos; i++ {
		offset += utf8.RuneLen(runes[i])
	}
	return offset
}

func byteOffsetToRuneOffset(runes []rune, bytePos int) int {
	if bytePos <= 0 {
		return 0
	}
	offset :=0
	for i, r := range runes {
		offset += utf8.RuneLen(r)
		if offset > bytePos {
			return i
		}
	}
	return len(runes)
}

// graphemeCursorLeft returns the byte offset of the grapheme cluster
// immediately before the one starting at from.
func graphemeCursorLeft(line string, from int) int {
	if len(line) == 0 {
		return 0
	}
	if from <= 0 {
		return 0
	}
	if from >= len(line) {
		from = len(line)
	}
	clusters := uniseg.NewGraphemes(line)
	candidate :=0
	for clusters.Next() {
		start, end := clusters.Positions()
		if end > from {
			return candidate
		}
		candidate = start
	}
	return candidate
}

// graphemeCursorRight returns the byte offset of the grapheme cluster
// immediately after the one ending at from.
func graphemeCursorRight(line string, from int) int {
	if len(line) == 0 {
		return 0
	}
	if from >= len(line) {
		return len(line)
	}
	if from <= 0 {
		from =0
	}
	boundary := nextRuneBoundary(line, from)
	clusters := uniseg.NewGraphemes(line[boundary:])
	for clusters.Next() {
		_, end := clusters.Positions()
		return boundary + end
	}
	return len(line)
}

func nextRuneBoundary(s string, i int) int {
	if i <= 0 {
		return 0
	}
	if i >= len(s) {
		return len(s)
	}
	for i < len(s) && utf8Continuation(s[i]) {
		i++
	}
	return i
}

func utf8Continuation(b byte) bool {
	return b&0xC0 == 0x80
}

// ---- rendering helpers ----

func renderUserLine(sender, text string) string {
	if sender == "" {
		sender = "you"
	}
	return fmt.Sprintf("› %s: %s", sender, text)
}

func renderToolStartLine(name string, input map[string]interface{}) string {
	if name == "" {
		name = "tool"
	}
	return fmt.Sprintf("⏺ %s(%s)", name, formatToolInput(input))
}

func renderToolFinishLine(name, output, errMsg string) string {
	if name == "" {
		name = "tool"
	}
	preview := output
	if len(preview) >240 {
		preview = preview[:240] + "…"
	}
	if errMsg != "" {
		return fmt.Sprintf(" ✗ %s failed: %s", name, errMsg)
	}
	return fmt.Sprintf(" ✓ %s → %s", name, preview)
}

func renderTodoList(p events.TodoListPayload) string {
	var b strings.Builder
	b.WriteString(" todos:\n")
	for _, item := range p.Items {
		mark := "[]"
		if item.Status == "completed" {
			mark = "[x]"
		}
		b.WriteString(fmt.Sprintf(" %s %s\n", mark, item.Content))
	}
	return strings.TrimRight(b.String(), "\n")
}

func formatToolInput(input map[string]interface{}) string {
	if len(input) == 0 {
		return ""
	}
	if cmd, ok := input["command"].(string); ok {
		return fmt.Sprintf("%q", truncate(cmd,80))
	}
	if path, ok := input["path"].(string); ok {
		return fmt.Sprintf("%q", path)
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func renderPendingApproval(requestID, sessionID string, items []tools.PendingPermission) string {
	requestID = strings.TrimSpace(requestID)
	item, ok := findPendingApproval(requestID, items)
	if ok && requestID == "" {
		requestID = strings.TrimSpace(item.ID)
	}
	if requestID == "" {
		requestID = "pending"
	}
	lines := []string{
		"Pending approval required.",
		"Request: " + requestID,
	}
	if sessionID = strings.TrimSpace(sessionID); sessionID != "" {
		lines = append(lines, "Session: "+sessionID)
	}
	if ok {
		req := item.Request
		lines = append(lines, "Tool: "+strings.TrimSpace(req.ToolName))
		if action := strings.TrimSpace(req.Action); action != "" {
			lines = append(lines, "Action: "+action)
		}
		if command := strings.TrimSpace(req.Command); command != "" {
			lines = append(lines, "Command: "+command)
		}
	}
	lines = append(lines,
		"",
		"Approve once: /approve",
		"Approve task: /approve task",
		"Approve session: /approve session",
		"Deny: /deny "+requestID,
	)
	return strings.Join(lines, "\n")
}

func findPendingApproval(requestID string, items []tools.PendingPermission) (tools.PendingPermission, bool) {
	requestID = strings.TrimSpace(requestID)
	if requestID != "" {
		for _, item := range items {
			if strings.TrimSpace(item.ID) == requestID {
				return item, true
			}
		}
	}
	if len(items) >0 {
		return items[0], true
	}
	return tools.PendingPermission{}, false
}
