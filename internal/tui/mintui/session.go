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
	minitui "github.com/tim5wang/min-tui"
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
	Models(context.Context, string) (rtbackend.ModelsView, error)
	SetSessionModelProfile(context.Context, string, string) (rtbackend.ModelsView, error)
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

	// messageCount tracks the number of messages in the
	// session, refreshed on each snapshot.
	messageCount int

	// ctxSummary is the latest context-usage snapshot from
	// backend.ContextSummary.  Refreshed on every snapshot so
	// the status bar can show live "128k/512k 25%" pressure.
	ctxSummary tools.ContextInspection

	// lastStatusText / lastStatusStyle are the most recent
	// values passed to setStatus.  Cached locally (under
	// statusMu) so tests can assert what the bar would show
	// without needing a real min-tui frontend.
	lastStatusText  string
	lastStatusStyle minitui.StatusStyle

	// snapshot tracks the latest backend snapshot so the status
	// bar can surface permission blockers.
	snapshot         rtbackend.Snapshot
	modelCallCount   int
	seenModelCallIDs map[string]struct{}
}

// New constructs a Session bound to the given backend. It does
// not touch the terminal; the terminal is acquired in Run.
func New(cfg *config.Config, backend Backend, stdout, stderr io.Writer) *Session {
	return &Session{
		cfg:              cfg,
		backend:          backend,
		stdout:           stdout,
		stderr:           stderr,
		now:              time.Now,
		seenModelCallIDs: make(map[string]struct{}),
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
	// Set an initial status now so the bar has a heartbeat
	// before the context summary round-trip completes; the
	// final status with the ctx chip is set just below.
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
	// Cache it on the session so subsequent turn-completion
	// redraws continue to show the live "128k/512k 25%"
	// pressure until the next refresh.
	if summary, err := s.backend.ContextSummary(ctx, opened.SessionID); err == nil {
		s.ctxSummary = summary
		s.setStatus(s.renderStatusWith(s.ctxSummary, "Ready"), minitui.StatusDefault)
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
				s.handleSlashCommand(ctx, cmd)
			},
		})
	}
}

// handleSlashCommand dispatches one slash command.  Most commands
// are forwarded to ExecuteCommand exactly as before.  /model with
// no arguments opens an interactive dropdown selector so the user
// can pick a model with arrow keys instead of typing a profile ID.
func (s *Session) handleSlashCommand(ctx *minitui.CommandContext, cmd commands.CommandMetadata) {
	if cmd.Name == "model" && strings.TrimSpace(ctx.Args) == "" {
		s.handleModelSelect(ctx)
		return
	}

	// Reconstruct a commands.Command from the typed line that
	// min-tui stripped down to "name args...".  We pass the raw
	// line as Raw so the dispatcher can re-split it.
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
	ctx.Write("✓ /" + cmd.Name + " completed\n")
}

// handleModelSelect shows a secondary dropdown to pick a model
// profile.  The currently active profile is pre-selected so the
// user can just press Enter to confirm, or use ↑/↓ to switch.
func (s *Session) handleModelSelect(ctx *minitui.CommandContext) {
	mv, err := s.backend.Models(context.Background(), s.sessionID)
	if err != nil {
		ctx.Write("Error: failed to list models: " + err.Error() + "\n")
		return
	}
	if len(mv.Profiles) == 0 {
		ctx.Write("No model profiles configured.\n")
		return
	}

	// Build select options with the currently selected profile
	// marked so the dropdown auto-focuses on it.
	currentProfileID := mv.SessionProfileID
	if currentProfileID == "" {
		currentProfileID = mv.DefaultProfileID
	}
	options := make([]minitui.SelectOption, 0, len(mv.Profiles))
	selectedIdx := 0
	for i, profile := range mv.Profiles {
		desc := profile.Model
		if profile.Provider != "" {
			desc += " · " + profile.Provider
		}
		if profile.Selected || profile.ID == currentProfileID {
			selectedIdx = i
			desc += " [active]"
		}
		options = append(options, minitui.SelectOption{
			Label:       profile.Name,
			Description: desc,
		})
	}

	// The select API puts the cursor on the first item.  Since
	// min-tui v0.2.0 doesn't have a SetSelectIndex API, we
	// rotate the options so the active profile is at index 0
	// and cursor lands on it.
	if selectedIdx > 0 {
		options = append(options[selectedIdx:], options[:selectedIdx]...)
	}

	idx := ctx.Select("Choose model · ↑↓ navigate · Enter confirm · Esc cancel", options)
	if idx < 0 {
		ctx.Write("Model selection cancelled.\n")
		return
	}

	// Map rotated index back to actual profile index.
	actualIdx := idx + selectedIdx
	if actualIdx >= len(mv.Profiles) {
		actualIdx -= len(mv.Profiles)
	}
	chosen := mv.Profiles[actualIdx]

	// Switch the session to the chosen profile.
	newMV, err := s.backend.SetSessionModelProfile(context.Background(), s.sessionID, chosen.ID)
	if err != nil {
		ctx.Write("Error: failed to switch model: " + err.Error() + "\n")
		return
	}

	ctx.Write(fmt.Sprintf("Model switched to %s (%s).\n", chosen.Name, chosen.Model))

	// Refresh the snapshot so the status bar picks up the new
	// model name on the next renderStatus call.
	s.refreshSnapshot()

	// Show the updated model list.
	ctx.Write("Current: ")
	for _, p := range newMV.Profiles {
		if p.Selected || (p.ID == newMV.SessionProfileID) {
			ctx.Write(fmt.Sprintf("%s (%s)", p.Name, p.Model))
			break
		}
	}
	ctx.Write("\n✓ /model completed\n")
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
			// Short confirmation in the output area; do not
			// touch the status bar so the godex heartbeat
			// stays visible.
			s.tui.WriteString("✓ /" + cmd.Name + " completed\n")
		}
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
		s.renderToolCallStarted(event)
	case events.EventToolCallFinished:
		s.renderToolCallFinished(event)
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

// renderToolCallStarted renders a tool call start with name and
// condensed input summary so the user can see what the agent is
// doing without needing to expand.
func (s *Session) renderToolCallStarted(event events.Event) {
	name := extractToolName(event)
	if name == "" {
		return
	}
	// Build a one-line summary: name + optional input snippet.
	line := "\n▶ " + name
	// Extract a short input preview (up to ~120 chars).
	if input := extractToolInputSummary(event); input != "" {
		line += "  " + input
	}
	s.tui.WriteString(line + "\n")
}

// renderToolCallFinished renders completion status: ✓ done
// or ✗ error with a short output/error summary.
func (s *Session) renderToolCallFinished(event events.Event) {
	name := extractToolName(event)
	output, errText := extractToolOutputError(event)

	switch {
	case errText != "":
		s.tui.WriteString("  ✗ " + errText + "\n")
	case output != "":
		// Show a condensed output (first line, capped).
		summary := firstLine(output, 200)
		s.tui.WriteString("  ✓ " + summary + "\n")
	default:
		if name == "" {
			s.tui.WriteString("  ✓ done\n")
		} else {
			s.tui.WriteString("  ✓ " + name + "\n")
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
	s.snapshot = snap
	s.messageCount = len(snap.Messages)
	// Rebuild model-call count from timeline events.
	s.modelCallCount = 0
	s.seenModelCallIDs = make(map[string]struct{})
	for _, ev := range snap.Timeline {
		if ev.Type != events.EventRunnerPhaseChanged {
			continue
		}
		switch p := ev.Payload.(type) {
		case events.RunnerPhasePayload:
			if p.Phase == "model_request" {
				key := ev.TurnID + "|" + ev.Timestamp.String()
				if _, ok := s.seenModelCallIDs[key]; !ok {
					s.seenModelCallIDs[key] = struct{}{}
					s.modelCallCount++
				}
			}
		case map[string]interface{}:
			if phase, _ := p["phase"].(string); phase == "model_request" {
				key := ev.TurnID + "|" + ev.Timestamp.String()
				if _, ok := s.seenModelCallIDs[key]; !ok {
					s.seenModelCallIDs[key] = struct{}{}
					s.modelCallCount++
				}
			}
		}
	}
	// Refresh the context-usage snapshot so the status bar
	// shows live "128k/512k 25%" pressure.  Best-effort: a
	// failure here is non-fatal because the status bar just
	// omits the ctx chip until the next refresh succeeds.
	if summary, sumErr := s.backend.ContextSummary(context.Background(), s.sessionID); sumErr == nil {
		s.ctxSummary = summary
	}
	s.setStatus(s.renderStatusWith(s.ctxSummary, "Ready"), minitui.StatusDefault)
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
	// Cache the latest text locally so tests can assert the
	// bar's content without needing a real min-tui frontend
	// (s.tui may be nil in tests).
	s.lastStatusText = text
	s.lastStatusStyle = style
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
	return s.renderStatusWith(s.ctxSummary, label)
}

// resolveModelName returns the model name the session is actually
// using, falling back to the config default.  Mirrors the bubbletea
// TUI's activeModelLabel logic so the status bar shows the
// session-specific model (not always the config default).
func (s *Session) resolveModelName() string {
	if s.cfg == nil {
		return "unknown"
	}
	profileID := strings.TrimSpace(s.snapshot.ModelProfileID)
	if profileID == "" {
		profileID = strings.TrimSpace(s.cfg.DefaultProfileID)
	}
	if profile, ok := s.cfg.ModelProfileByID(profileID); ok {
		if model := strings.TrimSpace(profile.Model); model != "" {
			return model
		}
	}
	// Fall back to config default model.
	if model := strings.TrimSpace(s.cfg.Model); model != "" {
		return model
	}
	return "unknown"
}

func (s *Session) renderStatusWith(summary tools.ContextInspection, label string) string {
	parts := []string{label}
	parts = append(parts, "Input", s.resolveModelName())
	// Permission blocker takes priority if present.
	if blocker := s.snapshot.ActivePermissionBlocker; blocker != nil {
		blockerParts := []string{"Blocked"}
		if id := strings.TrimSpace(blocker.RequestID); id != "" {
			blockerParts = append(blockerParts, id)
		}
		if action := strings.TrimSpace(blocker.ToolName + " " + blocker.Action); strings.TrimSpace(action) != "" {
			blockerParts = append(blockerParts, action)
		}
		parts = append(parts, strings.Join(blockerParts, " "))
	}
	// Context usage: "128k/512k 25%" — denominator is the
	// compress threshold (same as bubbletea TUI), not
	// TotalTokenEstimate which often equals TokenEstimate.
	if pct := ctxPctWithThreshold(summary, s.cfg.CompressThreshold); pct != "" {
		parts = append(parts, pct)
	}
	// Largest context source (top 1).
	if len(summary.LargestContextSources) > 0 {
		parts = append(parts, fmt.Sprintf("top %s %dk",
			summary.LargestContextSources[0].Source,
			(summary.LargestContextSources[0].Tokens+500)/1000))
	}
	// Model call count.
	if s.modelCallCount > 0 {
		parts = append(parts, fmt.Sprintf("calls %d", s.modelCallCount))
	}
	// Message count.
	if s.messageCount > 0 {
		parts = append(parts, fmt.Sprintf("msgs %d", s.messageCount))
	}
	return strings.Join(parts, " · ")
}

// ctxPctWithThreshold formats a "used/threshold pct" string
// matching the bubbletea TUI semantics: numerator is the
// current total context estimate, denominator is the compress
// threshold.  Falls back through the same cascade as the
// bubbletea TUI's contextUsageText.
func ctxPctWithThreshold(summary tools.ContextInspection, cfgThreshold int) string {
	used := summary.TotalTokenEstimate
	if used <= 0 {
		used = summary.TokenEstimate
	}
	if used <= 0 {
		used = summary.TokenBreakdown.Total
	}
	if used <= 0 {
		return ""
	}
	threshold := summary.CompressThreshold
	if threshold <= 0 {
		threshold = cfgThreshold
	}
	if threshold <= 0 {
		// No threshold known: just show used.
		usedK := (used + 500) / 1000
		return fmt.Sprintf("%dk", usedK)
	}
	pct := int((float64(used) * 100.0 / float64(threshold)) + 0.5)
	if pct > 100 {
		pct = 100
	}
	usedK := (used + 500) / 1000
	thresholdK := (threshold + 500) / 1000
	return fmt.Sprintf("%dk/%dk %d%%", usedK, thresholdK, pct)
}

// ctxPct is kept for test compatibility. It uses the summary's
// own TotalTokenEstimate field as denominator.
func ctxPct(summary tools.ContextInspection) string {
	if summary.TotalTokenEstimate <= 0 {
		return ""
	}
	pct := int((float64(summary.TokenEstimate) * 100.0 / float64(summary.TotalTokenEstimate)) + 0.5)
	if pct > 100 {
		pct = 100
	}
	usedK := (summary.TokenEstimate + 500) / 1000
	totalK := (summary.TotalTokenEstimate + 500) / 1000
	return fmt.Sprintf("%dk/%dk %d%%", usedK, totalK, pct)
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

// extractToolInputSummary returns a one-line text preview of the
// tool call input. Capped to ~120 display characters so it fits
// in the output area without cluttering.
func extractToolInputSummary(event events.Event) string {
	if event.Payload == nil {
		return ""
	}
	var input map[string]interface{}
	if tp, ok := event.Payload.(events.ToolCallPayload); ok {
		input = tp.Input
	} else if m, ok := event.Payload.(map[string]interface{}); ok {
		if v, ok := m["input"].(map[string]interface{}); ok {
			input = v
		}
	}
	if len(input) == 0 {
		return ""
	}
	// Pick the first meaningful string value as a preview.
	for _, key := range []string{"command", "path", "pattern", "url", "content", "query"} {
		if v, ok := input[key]; ok {
			s := strings.TrimSpace(fmt.Sprint(v))
			if s != "" {
				return key + ":" + ellipsizeMint(s, 100)
			}
		}
	}
	return ""
}

// extractToolOutputError returns the output and error fields from a
// ToolCallPayload. Output is empty on errors so the caller sees at
// most one of the two.
func extractToolOutputError(event events.Event) (output, errText string) {
	if tp, ok := event.Payload.(events.ToolCallPayload); ok {
		output = strings.TrimSpace(tp.Output)
		errText = strings.TrimSpace(tp.Error)
		return
	}
	if m, ok := event.Payload.(map[string]interface{}); ok {
		output, _ = m["output"].(string)
		errText, _ = m["error"].(string)
	}
	return strings.TrimSpace(output), strings.TrimSpace(errText)
}

// firstLine returns the first non-empty line of text, capped at maxLen.
func firstLine(text string, maxLen int) string {
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if len(line) > maxLen {
			return line[:maxLen] + "…"
		}
		return line
	}
	return ""
}

// ellipsizeMint truncates s to maxLen, appending "…" if needed.
func ellipsizeMint(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "…"
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
