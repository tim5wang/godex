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

// Run starts the full-screen TUI for the selected locator.
func (s *Session) Run(ctx context.Context, locator rtbackend.SessionLocator) error {
	opened, err := s.backend.OpenSession(ctx, locator)
	if err != nil {
		return fmt.Errorf("open tui session: %w", err)
	}

	snapshot, err := s.backend.Snapshot(ctx, opened.SessionID)
	if err != nil {
		return fmt.Errorf("load tui snapshot: %w", err)
	}

	m := newModel(ctx, s.cfg, s.backend, s.now, opened.SessionID, snapshot)
	options := []tea.ProgramOption{
		tea.WithAltScreen(),
		tea.WithContext(ctx),
		tea.WithOutput(s.stdout),
	}
	if captureMouseForTUI {
		options = append(options, tea.WithMouseCellMotion())
	}
	p := tea.NewProgram(m, options...)

	unsubscribe, err := s.backend.AttachSink(opened.SessionID, events.SinkFunc(func(event events.Event) {
		p.Send(runtimeEventMsg{Event: event})
	}))
	if err != nil {
		return fmt.Errorf("attach tui event sink: %w", err)
	}
	defer unsubscribe()

	_, err = p.Run()
	return err
}

func newModel(ctx context.Context, cfg *config.Config, backend Backend, now func() time.Time, sessionID string, snapshot rtbackend.Snapshot) *model {
	composer := textarea.New()
	composer.Prompt = "› "
	composer.Placeholder = "Type a message or /help. Enter sends. Ctrl+P/N recalls input history."
	composer.ShowLineNumbers = false
	composer.CharLimit = 20000
	composer.SetHeight(3)
	composer.MaxHeight = 6
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
		working:            working,
		workingSince:       workingSince,
		historyItems:       snapshotToItems(snapshot.Messages, snapshot.PendingPermissions, nil, sessionID),
		activePhase:        snapshot.ActivePhase,
		seenModelCallEvent: make(map[string]struct{}),
		inputHistoryIndex:  -1,
		clipboardWrite:     defaultClipboardWrite,
		viewport:           vp,
		composer:           composer,
		selectedItemID:     "",
	}
}

func (m *model) Init() tea.Cmd {
	m.rebuildModelCallStats()
	cmds := []tea.Cmd{
		m.composer.Focus(),
		func() tea.Msg { return textarea.Blink() },
		m.fetchContextSummaryCmd(),
	}
	if m.working {
		cmds = append(cmds, tickHeartbeat())
	}
	return tea.Batch(cmds...)
}
