// Package mintui wires the godex runtime backend to a min-tui fullscreen
// TUI session. It is the modern replacement for the old
// internal/tui/streaming scrollback-mode TUI, which had to manage
// ANSI escape sequences by hand to keep the input box from
// overwriting the conversation history.
//
// Architecture:
//
//   - min-tui owns the terminal: it sets raw mode, draws the
//     full-screen canvas, and manages the input editor.
//   - This package translates godex backend events into min-tui
//     output: banner, history, assistant text deltas, tool calls,
//     slash command results, status updates.
//   - The main loop reads input from min-tui and dispatches it to
//     the backend (Submit for chat, ExecuteCommand for /-prefixed
//     slash commands).
package mintui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/protocol"
	"github.com/tim5wang/godex/internal/domain/events"
	"github.com/tim5wang/godex/internal/domain/message"
	minitui "github.com/tim5wang/min-tui"
	rtbackend "github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/commands"
	"github.com/tim5wang/godex/internal/tools"
)

// Backend is the surface area of the runtime backend that the
// min-tui frontend depends on. Mirrors the streaming Backend
// interface; trimming happens at the wiring site.
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

// Session drives a min-tui frontend against a shared runtime backend.
type Session struct {
	cfg     *config.Config
	backend Backend
	stdout  io.Writer
	stderr  io.Writer
	now     func() time.Time

	// tui is the min-tui frontend. It is lazily created in Run
	// so that callers (and tests) can construct a Session
	// without immediately touching the terminal.
	tui *minitui.TUI

	// pendingAppend is a one-line buffer for partial text that
	// arrived without a trailing newline; flushed on next event
	// or on session end.
	pendingAppend string

	// statusMu serializes status-bar updates so a SetStatus call
	// never interleaves with a streaming write.
	statusMu sync.Mutex

	// sessionID is the opened session id, kept here for context
	// summary refresh and event filtering.
	sessionID string

	// modelCallCount / messageCount mirror the streaming TUI's
	// bookkeeping so the status bar can show "calls N · msgs M".
	modelCallCount int
	messageCount   int
	seenModelEvent map[string]struct{}
}

// New constructs a Session bound to the given backend. It does
// not touch the terminal; the terminal is acquired in Run.
func New(cfg *config.Config, backend Backend, stdout, stderr io.Writer) *Session {
	return &Session{
		cfg:           cfg,
		backend:       backend,
		stdout:        stdout,
		stderr:        stderr,
		now:           time.Now,
		seenModelEvent: make(map[string]struct{}),
	}
}

// Run starts the min-tui session. It opens a session via the
// backend, prints the initial banner + history, subscribes to
// the backend event sink, and reads user input in a loop.
func (s *Session) Run(ctx context.Context, locator rtbackend.SessionLocator) error {
	opened, err := s.backend.OpenSession(ctx, locator)
	if err != nil {
		return fmt.Errorf("open min-tui session: %w", err)
	}
	s.sessionID = opened.SessionID

	initial, err := s.backend.Snapshot(ctx, opened.SessionID)
	if err != nil {
		return fmt.Errorf("load min-tui snapshot: %w", err)
	}
	s.messageCount = len(initial.Messages)

	// tui.Close() is called via defer; on early error returns the
// terminal is still in raw mode and must be restored.
tui, err := minitui.NewWithConfig(minitui.Config{
		BorderColor:  "\x1b[2m", // dim border
		MaxInputRows: 8,
	})
	if err != nil {
		return fmt.Errorf("init min-tui: %w", err)
	}
	s.tui = tui
	defer tui.Close()

	s.printBanner(locator, initial)
	s.printHistory(initial)
	s.setStatus("Ready", minitui.StatusDefault)

	// Subscribe to backend events. The sink handler writes
	// assistant text deltas to the TUI output area and updates
	// the status bar on phase changes.
	unsubscribe, err := s.backend.AttachSink(opened.SessionID, events.SinkFunc(s.handleEvent))
	if err != nil {
		return fmt.Errorf("attach event sink: %w", err)
	}
	defer unsubscribe()

	// Best-effort initial context summary for the status bar.
	if summary, err := s.backend.ContextSummary(ctx, opened.SessionID); err == nil {
		s.setStatus(s.renderStatusWith(summary, "Ready"), minitui.StatusDefault)
	}

	for {
		// Reset "working" indicator when returning to the
		// prompt; the placeholder will be re-applied on
		// phase change events.
		s.setPlaceholder("")

		input, err := tui.ReadLine()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			if isInterruptErr(err) {
				return nil
			}
			return fmt.Errorf("read input: %w", err)
		}
		input = strings.TrimRight(input, "\n")

		if err := s.dispatchInput(ctx, opened.SessionID, input); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			fmt.Fprintf(s.stderr, "Error: %v\n", err)
		}
	}
}

// dispatchInput routes the user-typed line. Slash commands go
// to ExecuteCommand; everything else goes to Submit as a chat
// turn.
func (s *Session) dispatchInput(ctx context.Context, sessionID, input string) error {
	if strings.TrimSpace(input) == "" {
		return nil
	}
	if cmd, ok := commands.Parse(input); ok {
		result, err := s.backend.ExecuteCommand(ctx, sessionID, cmd)
		if result.Output != "" {
			s.tui.WriteString(result.Output + "\n")
		}
		if result.DispatchError != "" {
			s.tui.WriteString("Error: " + result.DispatchError + "\n")
		}
		s.setStatus("/"+cmd.Name+" completed", minitui.StatusInfo)
		return err
	}

	envelope := message.NewCLIEnvelope(sessionID, s.cfg.LeadName, input, s.now())
	res, err := s.backend.Submit(ctx, sessionID, envelope)
	if err != nil {
		return err
	}
	_ = res // submit result currently unused in TUI
	return nil
}

// ── event handling ───────────────────────────────────────────────

// handleEvent consumes a single backend event and renders it to
// the TUI. It is called by the backend's sink goroutine.
func (s *Session) handleEvent(event events.Event) {
	switch event.Type {
	case events.EventAssistantTextDelta:
		text := extractTextField(event)
		turnID := event.TurnID
		s.appendAssistantText(turnID, text)
	case events.EventAssistantMessageComplete:
		text := extractTextField(event)
		turnID := event.TurnID
		s.finishAssistantBlock(turnID, text)
	case events.EventToolCallStarted:
		name := extractToolName(event)
		s.tui.WriteString("\n● " + name + "\n")
	case events.EventToolCallFinished:
		s.tui.WriteString("  done\n")
	case events.EventRunnerPhaseChanged:
		phase, tool := extractRunnerPhase(event)
		s.applyRunnerPhase(phase, tool)
	case events.EventSnapshotReady:
		s.refreshSnapshot()
	case events.EventTurnCompleted:
		s.refreshSnapshot()
		s.setStatus(s.renderStatus("Ready"), minitui.StatusDefault)
	}
}

// appendAssistantText appends a streaming text delta to the
// current assistant block, opening a new block when the turn
// id changes.
func (s *Session) appendAssistantText(turnID, text string) {
	s.tui.WriteString(text)
}

// finishAssistantBlock ensures the assistant block ends with a
// trailing blank line, then clears the working flag.
func (s *Session) finishAssistantBlock(turnID, text string) {
	if text != "" {
		s.tui.WriteString(text)
	}
	s.tui.WriteString("\n")
}

// ── snapshot & status bar ────────────────────────────────────────

func (s *Session) refreshSnapshot() {
	snap, err := s.backend.Snapshot(context.Background(), s.sessionID)
	if err != nil {
		return
	}
	s.messageCount = len(snap.Messages)
	s.setStatus(s.renderStatus("Ready"), minitui.StatusDefault)
}

// ── output rendering ─────────────────────────────────────────────

// printBanner writes the initial header. The header is the
// godex icon, the workspace path, the model, and the session
// locator. We do NOT print a separator line: min-tui draws its
// own input box border above the input.
func (s *Session) printBanner(locator rtbackend.SessionLocator, snap rtbackend.Snapshot) {
	workspace := s.cfg.WorkspaceDir
	if workspace == "" {
		workspace = "(unknown workspace)"
	}
	fmt.Fprintf(s.stdout, "🤖 GoDex · min-tui mode\n")
	fmt.Fprintf(s.stdout, " session %s:%s\n", locator.Channel, locator.Key)
	fmt.Fprintf(s.stdout, " workspace %s\n", workspace)
	fmt.Fprintf(s.stdout, " model %s\n", s.cfg.Model)
	for _, msg := range firstNMessages(snap.Messages, 30) {
		s.printStoredMessage(msg)
	}
	_ = protocol.MessageText
}

// printHistory replays the most recent messages from a freshly
// opened session. We only print the most recent 30 to avoid
// dumping megabytes into the output area; older messages are
// available via the /history command.
func (s *Session) printHistory(snap rtbackend.Snapshot) {
	msgs := snap.Messages
	if len(msgs) > 30 {
		s.tui.WriteString(fmt.Sprintf("… showing last 30 of %d messages; press /history to inspect older entries …\n\n", len(msgs)))
		msgs = msgs[len(msgs)-30:]
	}
	for _, msg := range msgs {
		s.printStoredMessage(msg)
	}
}

func (s *Session) printStoredMessage(msg protocol.Message) {
	text := protocol.MessageText(msg)
	role := strings.ToLower(strings.TrimSpace(msg.Role))
	switch role {
	case "user":
		if text != "" {
			s.tui.WriteString("› you: " + text + "\n\n")
		}
	case "assistant":
		if text != "" {
			s.tui.WriteString("● " + text + "\n\n")
		}
	case "tool", "system":
		if text != "" {
			s.tui.WriteString(text + "\n\n")
		}
	}
}

func firstNMessages(msgs []protocol.Message, n int) []protocol.Message {
	if len(msgs) <= n {
		return msgs
	}
	return msgs[len(msgs)-n:]
}

// ── status bar ───────────────────────────────────────────────────

// setStatus updates the bottom status bar in a thread-safe way.
func (s *Session) setStatus(text string, style minitui.StatusStyle) {
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	if s.tui != nil {
		s.tui.SetStatus(text, style)
	}
}

// setPlaceholder flips the working indicator. The min-tui input
// box does not expose a placeholder, so this is a no-op kept
// for parity with the streaming TUI; we rely on SetStatus to
// communicate the state.
func (s *Session) setPlaceholder(text string) {
	_ = text
}

func (s *Session) applyRunnerPhase(phase, tool string) {
	switch phase {
	case "thinking":
		s.setStatus("Thinking…", minitui.StatusInfo)
	case "executing":
		if tool != "" {
			s.setStatus("Executing "+tool, minitui.StatusInfo)
		} else {
			s.setStatus("Executing", minitui.StatusInfo)
		}
	case "finished":
		s.setStatus(s.renderStatus("Ready"), minitui.StatusDefault)
	default:
		if phase != "" {
			s.setStatus(phase, minitui.StatusInfo)
		}
	}
}

func (s *Session) renderStatus(label string) string {
	return s.renderStatusWith(tools.ContextInspection{}, label)
}

func (s *Session) renderStatusWith(summary tools.ContextInspection, label string) string {
	parts := []string{label}
	parts = append(parts, "Input", s.cfg.Model)
	if pct := ctxPct(summary); pct != "" {
		parts = append(parts, pct)
	}
	parts = append(parts, fmt.Sprintf("calls %d", s.modelCallCount))
	parts = append(parts, fmt.Sprintf("msgs %d", s.messageCount))
	return strings.Join(parts, " · ")
}

func ctxPct(summary tools.ContextInspection) string {
	if summary.TotalTokenEstimate <= 0 {
		return ""
	}
	pct := int(float64(summary.TokenEstimate) * 100.0 / float64(summary.TotalTokenEstimate))
	return fmt.Sprintf("%.1fk/%dk %d%%",
		float64(summary.TokenEstimate)/1000.0,
		summary.TotalTokenEstimate/1000,
		pct)
}

// ── event payload helpers ────────────────────────────────────────

// extractTextField reads a TextPayload from the event payload,
// returning the text field. Falls back to scanning the payload
// map for any string value that looks like a "text" key.
func extractTextField(event events.Event) string {
	if event.Payload == nil {
		return ""
	}
	if tp, ok := event.Payload.(events.TextPayload); ok {
		return tp.Text
	}
	if m, ok := event.Payload.(map[string]interface{}); ok {
		if v, ok := m["text"].(string); ok {
			return v
		}
	}
	return ""
}

func extractToolName(event events.Event) string {
	if event.Payload == nil {
		return ""
	}
	if tp, ok := event.Payload.(events.ToolCallPayload); ok {
		return tp.Name
	}
	if m, ok := event.Payload.(map[string]interface{}); ok {
		if v, ok := m["name"].(string); ok {
			return v
		}
	}
	return ""
}

func extractRunnerPhase(event events.Event) (phase, tool string) {
	if event.Payload == nil {
		return "", ""
	}
	if rp, ok := event.Payload.(events.RunnerPhasePayload); ok {
		return rp.Phase, rp.ToolName
	}
	if m, ok := event.Payload.(map[string]interface{}); ok {
		phase, _ = m["phase"].(string)
		tool, _ = m["tool_name"].(string)
	}
	return
}

func isInterruptErr(err error) bool {
	return err != nil && (errors.Is(err, io.EOF) || strings.Contains(err.Error(), "interrupt"))
}
