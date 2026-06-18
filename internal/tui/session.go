package tui

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/domain/events"
	rtbackend "github.com/tim5wang/godex/internal/services/backend"
)

const captureMouseForTUI = false

func New(cfg *config.Config, backend Backend, stdout io.Writer) *Session {
	return &Session{
		cfg:     cfg,
		backend: backend,
		stdout:  stdout,
		now:     time.Now,
	}
}

// Run starts the TUI for the selected locator.
//
// The program is launched without tea.WithAltScreen so its output is
// streamed straight into the terminal's scrollback buffer. This matches
// how the agent's replies are produced (append-only chunks) and lets the
// user scroll back through earlier frames after the program exits.
// Heavyweight surfaces that genuinely need a dedicated canvas (the
// workspace dashboard, permission popovers, etc.) opt back into alt
// screen by spawning their own tea.Program with tea.WithAltScreen.
func (s *Session) Run(ctx context.Context, locator rtbackend.SessionLocator) error {
	opened, err := s.backend.OpenSession(ctx, locator)
	if err != nil {
		return fmt.Errorf("open tui session: %w", err)
	}

	snapshot, err := s.backend.Snapshot(ctx, opened.SessionID)
	if err != nil {
		return fmt.Errorf("load tui snapshot: %w", err)
	}

	m := newModelWithDeferredInit(ctx, s.cfg, s.backend, s.now, opened.SessionID, snapshot)
	options := []tea.ProgramOption{
		tea.WithContext(ctx),
		tea.WithOutput(s.stdout),
	}
	if captureMouseForTUI {
		options = append(options, tea.WithMouseCellMotion())
	}
	p := tea.NewProgram(m, options...)

	unsubscribe, err := s.backend.AttachSink(opened.SessionID, events.SinkFunc(func(event events.Event) {
		forwardEvent(ctx, p, event)
	}))
	if err != nil {
		return fmt.Errorf("attach tui event sink: %w", err)
	}
	defer unsubscribe()

	_, err = p.Run()
	return err
}

// forwardEvent hands a backend event to the tea program without
// blocking the emitting goroutine.
//
// events.Broadcaster.Emit dispatches synchronously on whatever
// goroutine produced the event (agent runner, subagent, tool loop,
// heartbeat, session repair, etc.). Calling p.Send directly on that
// goroutine is unsafe because tea.Program.Send documents that
// "If the program hasn't started yet this will be a blocking
// operation" and the in-program queue is also bounded. A blocked
// emitter freezes the backend event loop, which in turn blocks the
// very SnapshotReady / WindowSizeMsg / TurnCompleted events the TUI
// needs to render — the original "TUI appears frozen" and
// "re-launching TUI stuck on 'Loading TUI...'" symptoms.
//
// The fix: if the program hasn't been started yet, or its
// input channel cannot accept the message right now, drop the event.
// Dropping transient events is safe because the TUI's next snapshot
// fetch (driven by EventSnapshotReady and refreshViewport) is
// authoritative for visible state. Persistent state changes (turn
// completion, permission decisions) are always recoverable through
// Snapshot() on reconnect.
func forwardEvent(ctx context.Context, p *tea.Program, event events.Event) {
	if p == nil {
		return
	}
	select {
	case <-ctx.Done():
		return
	default:
	}
	// We cannot peek at p's internal queue length, so we use a
	// short-timeout guarded send. tea.Program.Send is a no-op
	// once the program has exited, so the only failure mode we need
	// to defend against is the pre-Run / full-queue blocking case.
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.Send(runtimeEventMsg{Event: event})
	}()
	select {
	case <-done:
	case <-time.After(50 * time.Millisecond):
		// The program is stuck (pre-Run, or render loop wedged).
		// Drop the event so the emitter can keep producing.
	case <-ctx.Done():
	}
}

func newModel(ctx context.Context, cfg *config.Config, backend Backend, now func() time.Time, sessionID string, snapshot rtbackend.Snapshot) *model {
	m := newModelWithDeferredInit(ctx, cfg, backend, now, sessionID, snapshot)
	// Synchronously create heavy components.
	m.highlighter = NewHighlighter()
	m.markdownRenderer = NewMarkdownRenderer(m.highlighter)
	return m
}

// newModelWithDeferredInit creates a model but defers initialization of heavy components.
// Heavy components (Highlighter, MarkdownRenderer) are created in Init() instead of newModel(),
// which allows the tea program to start quickly without blocking on heavy initialization.
func newModelWithDeferredInit(ctx context.Context, cfg *config.Config, backend Backend, now func() time.Time, sessionID string, snapshot rtbackend.Snapshot) *model {
	composer := textarea.New()
	composer.Prompt = "› "
	composer.Placeholder = "Type a message or /help. Enter sends. Alt+Enter newline. Ctrl+P/N recalls history."
	composer.ShowLineNumbers = false
	composer.CharLimit = 20000
	composer.SetHeight(3)
	composer.MaxHeight = 10
	composer.Focus()
	composer.FocusedStyle.Base = composer.FocusedStyle.Base.
		Foreground(lipgloss.AdaptiveColor{Light: "#111827", Dark: "#F9FAFB"})
	composer.FocusedStyle.Prompt = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.AdaptiveColor{Light: "#0F766E", Dark: "#5EEAD4"})
	composer.FocusedStyle.Placeholder = mutedLineStyle
	composer.BlurredStyle.Base = composer.BlurredStyle.Base.Foreground(lipgloss.AdaptiveColor{Light: "#374151", Dark: "#D1D5DB"})
	composer.BlurredStyle.Prompt = mutedLineStyle
	composer.BlurredStyle.Placeholder = mutedLineStyle

	vp := viewport.New(0, 0)
	vp.Style = lipgloss.NewStyle()
	vp.MouseWheelEnabled = true
	vp.MouseWheelDelta = 3

	status := fmt.Sprintf("Connected · %s:%s", snapshot.Locator.Channel, snapshot.Locator.Key)
	working := snapshot.Running
	workingSince := time.Time{}
	if working {
		workingSince = now()
		status = "Restored running session"
	}

	return &model{
		ctx:                ctx,
		cfg:                cfg,
		backend:            backend,
		now:                now,
		sessionID:          sessionID,
		locator:            snapshot.Locator,
		snapshot:           snapshot,
		status:             status,
		autoFollow:         true,
		focus:              focusComposer,
		activeWorkbenchTab: workbenchTabLogs,
		working:            working,
		workingSince:       workingSince,
		historyItems:       snapshotToItems(snapshot.Messages, snapshot.PendingPermissions, nil, sessionID),
		activePhase:        snapshot.ActivePhase,
		seenModelCallEvent: make(map[string]struct{}),
		inputHistoryIndex:  -1,
		clipboardWrite:     defaultClipboardWrite,
		// highlighter and markdownRenderer are created in Init() to avoid blocking.
		viewport:           vp,
		composer:           composer,
		selectedItemID:     "",
	}
}

func (m *model) Init() tea.Cmd {
	m.rebuildModelCallStats()

	// Initialize heavy components lazily to avoid blocking the tea program startup.
	// Highlighter and MarkdownRenderer involve I/O and parsing initialization,
	// so creating them in Init() allows the TUI to render immediately.
	if m.highlighter == nil {
		m.highlighter = NewHighlighter()
	}
	if m.markdownRenderer == nil {
		m.markdownRenderer = NewMarkdownRenderer(m.highlighter)
	}

	cmds := []tea.Cmd{
		m.composer.Focus(),
		func() tea.Msg { return textarea.Blink() },
		m.fetchContextSummaryCmd(),
		m.fetchWorkbenchCmd(),
	}
	if m.working {
		cmds = append(cmds, tickHeartbeat())
	}
	return tea.Batch(cmds...)
}