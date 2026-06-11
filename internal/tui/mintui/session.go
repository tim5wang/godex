// Package mintui wires the godex runtime backend to a min-tui fullscreen
// TUI session. It is the modern replacement for the old
// internal/tui/streaming scrollback-mode TUI, which had to manage
// ANSI escape sequences by hand to keep the input box from
// overwriting the conversation history.
//
// Architecture:
//
//   - min-tui owns the terminal: it sets raw mode, draws the
//     full-screen canvas, manages the input editor, and provides
//     a slash-command dropdown UI.
//   - This package translates godex backend events into min-tui
//     output (banner, history, assistant text deltas, tool calls,
//     slash command results, status updates) and registers
//     godex slash commands with min-tui so they appear in the
//     input box's `/`-triggered dropdown.
//   - The main loop reads input from min-tui and submits each
//     turn asynchronously so the user can keep typing (or
//     press Ctrl+C to cancel) while the agent is responding.
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
	minitui "github.com/tim5wang/godex/internal/tui/mintui/minitui"
	rtbackend "github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/commands"
	"github.com/tim5wang/godex/internal/tools"
)

// Backend is the surface area of the runtime backend that the
// min-tui frontend depends on. Mirrors the streaming Backend
// interface; trimming happens at the wiring site.
//
// SubmitAsync and CancelTurn are required so the main loop can
// return to tui.ReadLine() immediately and let the user cancel
// an in-flight turn with Ctrl+C.
type Backend interface {
	OpenSession(context.Context, rtbackend.SessionLocator) (*rtbackend.OpenedSession, error)
	Snapshot(context.Context, string) (rtbackend.Snapshot, error)
	ContextSummary(context.Context, string) (tools.ContextInspection, error)
	Submit(context.Context, string, message.Envelope) (*rtbackend.SubmitResult, error)
	SubmitAsync(context.Context, string, message.Envelope, ...rtbackend.SubmitOptions) (*rtbackend.SubmitResult, error)
	CancelTurn(context.Context, string, string) (*rtbackend.CancelTurnResult, error)
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

	// tui is the min-tui frontend. Lazily created in Run so that
	// callers (and tests) can construct a Session without
	// immediately touching the terminal.
	tui *minitui.TUI

	// statusMu serializes status-bar updates so a SetStatus call
	// never interleaves with a streaming write.
	statusMu sync.Mutex

	// activeTurnID is the turn id of the currently-running
	// background turn (empty if no turn is active). Used to
	// translate Ctrl+C into a CancelTurn call.
	activeTurnMu sync.Mutex
	activeTurnID string

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
// the backend event sink, registers slash commands with min-tui
// so they appear in the input dropdown, and reads user input in
// a loop. Each turn is submitted asynchronously so the user can
// keep typing (or cancel) while the agent is responding.
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

	// tui.Close() restores the terminal even on early returns.
	tui, err := minitui.NewWithConfig(minitui.Config{
		BorderColor:  "\x1b[2m", // dim border
		MaxInputRows: 8,
	})
	if err != nil {
		return fmt.Errorf("init min-tui: %w", err)
	}
	s.tui = tui
	defer tui.Close()

	// Register godex slash commands so they appear in the
	// /-triggered dropdown in the input box. Each command's
	// handler writes its output to the TUI through the
	// CommandContext min-tui provides.
	s.registerSlashCommands()

	// Banner + history go to the TUI output area (NOT raw
	// stdout) so min-tui's fullDraw can render them on the
	// canvas along with the input box.
	s.writeBanner(locator, initial)
	s.writeHistory(initial)
	s.setStatus(s.renderStatus("Ready"), minitui.StatusDefault)

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
		input, err := tui.ReadLine()
		if err != nil {
			if isInterruptErr(err) {
				// Ctrl+C: if a turn is running, cancel it
				// and continue the loop. Otherwise exit.
				if s.cancelActiveTurn() {
					s.setStatus("Turn cancelled", minitui.StatusWarning)
					continue
				}
				return nil
			}
			if errors.Is(err, io.EOF) {
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

// registerSlashCommands wires the godex slash-command list into
// min-tui's dropdown UI. When the user types `/` in the input
// box, min-tui shows a filterable list of these commands; arrow
// keys + Enter invokes the handler with a CommandContext that
// can write to the TUI and call back for multi-turn input.
func (s *Session) registerSlashCommands() {
	for _, item := range commands.AvailableMetadata() {
		cmd := item // capture for the closure
		s.tui.RegisterCommand(minitui.SlashCommand{
			Name:        cmd.Name,
			Description: cmd.Description,
			Handler: func(ctx *minitui.CommandContext) {
				// Reconstruct a commands.Command from the
				// typed line that min-tui stripped down to
				// "name args...".  We pass the raw line as
				// Raw so the dispatcher can re-split it.
				raw := "/" + cmd.Name
				if ctx.Args != "" {
					raw += " " + ctx.Args
				}
				parsed, _ := commands.Parse(raw)
				if parsed.Name == "" {
					ctx.Write(fmt.Sprintf("Error: failed to parse /%s arguments\n", cmd.Name))
					return
				}
				result, err := s.backend.ExecuteCommand(context.Background(), s.sessionID, parsed)
				if result.Output != "" {
					ctx.Write(result.Output)
					if !strings.HasSuffix(result.Output, "\n") {
						ctx.Write("\n")
					}
				}
				if result.DispatchError != "" {
					ctx.Write("Error: " + result.DispatchError + "\n")
				}
				if err != nil {
					ctx.Write("Error: " + err.Error() + "\n")
				}
				ctx.SetStatus("/"+cmd.Name+" completed", minitui.StatusInfo)
			},
		})
	}
}

// cancelActiveTurn cancels the currently-running turn, if any.
// Returns true if a turn was actually cancelled.
func (s *Session) cancelActiveTurn() bool {
	s.activeTurnMu.Lock()
	turnID := s.activeTurnID
	s.activeTurnMu.Unlock()
	if turnID == "" {
		return false
	}
	_, err := s.backend.CancelTurn(context.Background(), s.sessionID, turnID)
	return err == nil
}

// dispatchInput routes the user-typed line. Slash commands go
// to ExecuteCommand synchronously; everything else goes to
// SubmitAsync so the main loop returns to ReadLine immediately.
func (s *Session) dispatchInput(ctx context.Context, sessionID, input string) error {
	if strings.TrimSpace(input) == "" {
		return nil
	}
	if cmd, ok := commands.Parse(input); ok {
		result, err := s.backend.ExecuteCommand(ctx, sessionID, cmd)
		if s.tui != nil {
			if result.Output != "" {
				s.tui.WriteString(result.Output + "\n")
			}
			if result.DispatchError != "" {
				s.tui.WriteString("Error: " + result.DispatchError + "\n")
			}
		}
		s.setStatus("/"+cmd.Name+" completed", minitui.StatusInfo)
		return err
	}

	envelope := message.NewCLIEnvelope(sessionID, s.cfg.LeadName, input, s.now())
	res, err := s.backend.SubmitAsync(ctx, sessionID, envelope)
	if err != nil {
		return err
	}
	if res != nil {
		s.setActiveTurn(res.TurnID)
	}
	return nil
}

func (s *Session) setActiveTurn(turnID string) {
	s.activeTurnMu.Lock()
	s.activeTurnID = turnID
	s.activeTurnMu.Unlock()
}

func (s *Session) clearActiveTurn(turnID string) {
	s.activeTurnMu.Lock()
	if s.activeTurnID == turnID {
		s.activeTurnID = ""
	}
	s.activeTurnMu.Unlock()
}

// ── event handling ───────────────────────────────────────────────

// handleEvent consumes a single backend event and renders it to
// the TUI. It is called by the backend's sink goroutine.
func (s *Session) handleEvent(event events.Event) {
	switch event.Type {
	case events.EventAssistantTextDelta:
		text := extractTextField(event)
		s.appendAssistantText(text)
	case events.EventAssistantMessageComplete:
		text := extractTextField(event)
		s.finishAssistantBlock(text)
	case events.EventToolCallStarted:
		name := extractToolName(event)
		s.tui.WriteString("\n● " + name + "\n")
	case events.EventToolCallFinished:
		s.tui.WriteString("  done\n")
	case events.EventRunnerPhaseChanged:
		phase, tool := extractRunnerPhase(event)
		s.applyRunnerPhase(phase, tool)
	case events.EventTurnCompleted:
		s.clearActiveTurn(event.TurnID)
		s.refreshSnapshot()
		s.setStatus(s.renderStatus("Ready"), minitui.StatusDefault)
	case events.EventSnapshotReady:
		s.refreshSnapshot()
	case events.EventWarningRaised:
		if np, ok := event.Payload.(events.NoticePayload); ok && np.Message != "" {
			s.tui.WriteString("\n⚠ " + np.Message + "\n")
		}
	case events.EventErrorRaised:
		if np, ok := event.Payload.(events.NoticePayload); ok && np.Message != "" {
			s.tui.WriteString("\n✗ " + np.Message + "\n")
		}
	}
}

// appendAssistantText writes a streaming text delta to the
// current assistant block.  Min-tui buffers the bytes and
// renders them incrementally.
func (s *Session) appendAssistantText(text string) {
	if text == "" {
		return
	}
	s.tui.WriteString(text)
}

// finishAssistantBlock ensures the assistant block ends with a
// trailing newline.
func (s *Session) finishAssistantBlock(text string) {
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

// writeBanner writes the initial header to the TUI output area
// so it appears on the canvas above the input box.
func (s *Session) writeBanner(locator rtbackend.SessionLocator, snap rtbackend.Snapshot) {
	workspace := s.cfg.WorkspaceDir
	if workspace == "" {
		workspace = "(unknown workspace)"
	}
	s.tui.WriteString(fmt.Sprintf("🤖 GoDex · min-tui mode\n session %s:%s\n workspace %s\n model %s\n",
		locator.Channel, locator.Key, workspace, s.cfg.Model))
	for _, msg := range firstNMessages(snap.Messages, 30) {
		s.writeStoredMessage(msg)
	}
}

// writeHistory replays the most recent messages from a freshly
// opened session. We only print the most recent 30 to avoid
// dumping megabytes into the output area; older messages are
// available via the /history command.
func (s *Session) writeHistory(snap rtbackend.Snapshot) {
	msgs := snap.Messages
	if len(msgs) > 30 {
		s.tui.WriteString(fmt.Sprintf("\n… showing last 30 of %d messages; press /history to inspect older entries …\n\n", len(msgs)))
		msgs = msgs[len(msgs)-30:]
	}
	for _, msg := range msgs {
		s.writeStoredMessage(msg)
	}
}

func (s *Session) writeStoredMessage(msg protocol.Message) {
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
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) {
		return true
	}
	return strings.Contains(err.Error(), "interrupt")
}
