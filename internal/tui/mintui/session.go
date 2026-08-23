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
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/protocol"
	"github.com/tim5wang/godex/internal/domain/events"
	"github.com/tim5wang/godex/internal/domain/message"
	rtbackend "github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/commands"
	"github.com/tim5wang/godex/internal/services/localbash"
	"github.com/tim5wang/godex/internal/tools"
	minitui "github.com/tim5wang/min-tui"
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
	ListSessions(context.Context, rtbackend.SessionListFilter) ([]rtbackend.ListedSession, error)
	CreateNewSession(context.Context) (rtbackend.SessionLocator, error)

	// LongTask surface used by the Ctrl+B background-task popup.
	// Implementations are expected to project agent.LongTaskView
	// into the rtbackend.LongTaskRow / LongTaskDetail shapes so
	// this package does not need to import internal/agent.
	ListLongTasks(ctx context.Context, sessionID string) ([]rtbackend.LongTaskRow, error)
	GetLongTask(ctx context.Context, sessionID, workflowID string) (rtbackend.LongTaskDetail, error)
	CancelLongTask(ctx context.Context, sessionID, workflowID string) error

	// Subagent and advanced LongTask operations used by the
	// Ctrl+W workbench and longtask detail popup.
	ListSubagents(ctx context.Context, sessionID string) ([]rtbackend.SubagentRow, error)
	LookupLongTask(ctx context.Context, sessionID, commit, longtaskID string) (rtbackend.LongTaskLookupResult, error)
	RollbackLongTaskStory(ctx context.Context, sessionID, workflowID, nodeID, reason string) (rtbackend.LongTaskRollbackResult, error)
	GCLongTaskArtifacts(ctx context.Context, sessionID, workflowID string, olderThanSeconds int, apply bool) (rtbackend.LongTaskGCSweepResult, error)
}

// tuiFrontend is the minimal min-tui surface the Session uses
// directly. *minitui.TUI satisfies it via its built-in method
// set. Defining the boundary here lets tests inject a capturing
// stub that records WriteString calls without depending on a
// real terminal.
type tuiFrontend interface {
	WriteString(s string) (int, error)
	SetStatus(text string, style minitui.StatusStyle)
	RegisterCommand(cmd minitui.SlashCommand)
	// PushPopup / SetGlobalKeyHandler back the Ctrl+B background
	// task popup.  They are safe to call from any goroutine
	// according to min-tui's contract; the OnKey callback is the
	// one place we must NOT call back into the frontend.
	PushPopup(p minitui.Popup)
	PopPopup()
	SetGlobalKeyHandler(fn func(minitui.KeyEvent) bool)
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
	//
	// The field is typed as a local interface so tests can swap
	// in a capturing stub that records WriteString calls. The
	// concrete *minitui.TUI satisfies this interface through its
	// built-in method set; production code does not change.
	tui tuiFrontend

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

	// unsubscribe detaches the event sink from the current session.
	// Nil when no sink is attached.
	unsubscribe func()

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

	// assistantStreaming is true while a turn is producing
	// assistant_text_delta events.  Used to avoid writing the
	// full text on assistant_message_completed (which would
	// duplicate the already-streamed deltas).
	assistantStreaming bool

	// activityChip / activityStyle describe a short "what's
	// happening right now" indicator that is injected into the
	// status bar right after the model name (i.e. between the
	// "Ready · Input · <Model>" prefix and the rest of the
	// chips).  Guarded by statusMu (same lock as setStatus).
	//
	// Crucially, the chip is a *non-clobbering* overlay: we
	// never replace the entire status bar line to surface a
	// phase change.  model_request — which dominates ~95% of
	// any in-flight turn — is intentionally NOT mapped to a
	// chip so the underlying heartbeat ("Ready · Input ·
	// MiniMax-M3 · 132k/256k 51% · top tool_results 104k ·
	// calls 10 · msgs 138") stays continuously visible while
	// the user waits for the model.
	activityChip  string
	activityStyle minitui.StatusStyle

	// inputHistory stores submitted inputs so the user can recall
	// them with ↑/↓.  Most-recent-first ordering: history[0] is
	// the newest entry.
	inputHistory []string
	historyPos   int // current cursor into inputHistory, -1 = not navigating

	// longTasks backs the Ctrl+B background-task popup.  Kept
	// on the Session (not on the Popup itself) so the cached
	// row set, cursor, and filter survive the user closing
	// and reopening the popup within the same session.
	longTasks longTaskUI

	// workbench backs the Ctrl+W workbench popup (tasks +
	// workers).  Kept on the Session for the same reason:
	// cache survives popup close/reopen.
	workbench workbenchUI

	// permUI backs the Ctrl+P permission approval popup.
	permUI permUI

	// bashCancel is set while a local ! command is running.
	// The main loop checks it on Ctrl+C to cancel the bash
	// process without quitting the TUI.
	bashCancel context.CancelFunc

	// runCtx is the context passed to Run().  The Ctrl+B popup
	// reuses it for the in-flight ListLongTasks / GetLongTask /
	// CancelLongTask calls so a Ctrl+C from the main loop
	// also cancels the popup's network I/O.
	runCtx context.Context
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

const maxInputHistory = 1000

// historyFn implements min-tui's HistoryFn callback for input history
// navigation with ↑/↓.  inputHistory is stored most-recent-first
// (index 0 = newest).  historyPos tracks the current position
// (-1 = not navigating).
//
// When the user edits or submits, min-tui exits recall mode; we
// detect that by comparing the current input against the text at
// the current history position and reset historyPos accordingly.
func (s *Session) historyFn(direction int, current string) string {
	if len(s.inputHistory) == 0 {
		return current
	}

	// Reset position when not mid-navigation (e.g. after edit/submit).
	if s.historyPos >= 0 && current != s.inputHistory[s.historyPos] {
		s.historyPos = -1
	}

	switch direction {
	case -1: // ↑ — go to older entry
		if s.historyPos+1 < len(s.inputHistory) {
			s.historyPos++
		} else {
			return current // at boundary, signals "no more"
		}
	case +1: // ↓ — go to newer entry
		if s.historyPos > 0 {
			s.historyPos--
		} else {
			s.historyPos = -1
			return current // at boundary, min-tui restores draft
		}
	}
	return s.inputHistory[s.historyPos]
}

// recordInputHistory appends a non-empty input to the history list.
// Deduplicates consecutive identical entries and caps the list at
// maxInputHistory so the session never leaks memory.
func (s *Session) recordInputHistory(input string) {
	if len(s.inputHistory) > 0 && s.inputHistory[0] == input {
		return // deduplicate consecutive identical entries
	}
	if len(s.inputHistory) >= maxInputHistory {
		s.inputHistory = s.inputHistory[:maxInputHistory-1]
	}
	s.inputHistory = append([]string{input}, s.inputHistory...)
}

// Run starts the min-tui session. It opens a session via the
// backend, prints the initial banner + history, subscribes to
// the backend event sink, registers slash commands with min-tui
// so they appear in the input dropdown, and reads user input in
// a loop. Each turn is submitted asynchronously so the user can
// keep typing (or cancel) while the agent is responding.
func (s *Session) Run(ctx context.Context, locator rtbackend.SessionLocator) error {
	s.runCtx = ctx
	opened, err := s.backend.OpenSession(ctx, locator)
	if err != nil {
		return fmt.Errorf("open min-tui session: %w", err)
	}
	s.sessionID = opened.SessionID

	initial, err := s.backend.Snapshot(ctx, opened.SessionID)
	if err != nil {
		return fmt.Errorf("load min-tui snapshot: %w", err)
	}
	s.snapshot = initial
	s.messageCount = len(initial.Messages)

	// tui.Close() restores the terminal even on early returns.
	tui, err := minitui.NewWithConfig(minitui.Config{
		BorderColor:  "\x1b[2m", // dim border
		MaxInputRows: 8,
		// Spacious=true asks min-tui to insert a blank line
		// before/after markdown headings, code fences, and
		// tables — which gives the conversation a natural
		// rhythm instead of letting every block collapse
		// onto adjacent rows.  Without it, every header the
		// assistant emits hugs the previous paragraph and
		// the conversation becomes a wall of text.
		Spacious: true,
		// No custom RenderLine: we delegate markdown
		// rendering to min-tui's built-in renderer so we
		// inherit its native styling, including the grey
		// background it applies to blockquote lines.  We
		// use the blockquote syntax ourselves to give user
		// messages, tool calls, and warnings a coloured
		// background that visually separates them from
		// assistant prose.
		HistoryFn: s.historyFn,
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

	// Register the Ctrl+B global hotkey for the background-
	// task popup.  This must happen AFTER slash commands so
	// any later code paths that unregister a command don't
	// accidentally clear the hotkey.
	s.registerGlobalHotkeys()

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
	s.unsubscribe = unsubscribe
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
				// Ctrl+C: if a local bash command is running,
				// cancel it first.  Otherwise, if a turn is
				// running, cancel the turn.  Otherwise exit.
				if s.bashCancel != nil {
					s.bashCancel()
					s.bashCancel = nil
					s.tui.SetStatus("Bash cancelled", minitui.StatusWarning)
					continue
				}
				if s.cancelActiveTurn() {
					// Preserve the heartbeat: surface
					// cancellation as an activity
					// chip instead of overwriting the
					// whole bar (the previous
					// behaviour clobbered
					// "Ready · Input · <Model> · ctx"
					// for the entire turn after
					// cancel).
					s.setActivityChip("Cancelled", minitui.StatusWarning)
					s.refreshStatusBar()
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

		if err := s.dispatchInput(ctx, s.sessionID, input); err != nil {
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

// registerGlobalHotkeys installs the Session's permanent
// keybindings on the min-tui frontend.  The current set is:
//
//	Ctrl+B   open the background-task popup
//	Ctrl+W   open the workbench (tasks + workers)
//	Ctrl+P   open the permission approval popup (when blocked)
//	Ctrl+H   open the help popup (keyboard shortcuts)
//
// Hotkeys are evaluated on every keystroke in normal (non-popup)
// mode; returning true consumes the key so it does not reach
// the input editor.  When a popup is open, min-tui routes keys
// to the popup's own OnKey and the global handler is bypassed
// — so pressing Ctrl+B while a popup is open does not open a
// second one.
func (s *Session) registerGlobalHotkeys() {
	s.tui.SetGlobalKeyHandler(func(k minitui.KeyEvent) bool {
		if k.Ctrl && (k.Rune == 'b' || k.Rune == 'B') {
			s.openLongTaskList(s.runCtx)
			return true
		}
		if k.Ctrl && (k.Rune == 'w' || k.Rune == 'W') {
			s.openWorkbench(s.runCtx)
			return true
		}
		if k.Ctrl && (k.Rune == 'p' || k.Rune == 'P') {
			s.openPermissionPopup(s.runCtx)
			return true
		}
		if k.Ctrl && (k.Rune == 'h' || k.Rune == 'H') {
			s.openHelp()
			return true
		}
		return false
	})
}

// handleSlashCommand dispatches one slash command.  Most commands
// are forwarded to ExecuteCommand exactly as before.  /model and
// /resume with no arguments open an interactive dropdown selector.
func (s *Session) handleSlashCommand(ctx *minitui.CommandContext, cmd commands.CommandMetadata) {
	// /help opens the interactive help popup instead of text output.
	if cmd.Name == "help" {
		s.openHelp()
		return
	}
	if cmd.Name == "model" && strings.TrimSpace(ctx.Args) == "" {
		s.handleModelSelect(ctx)
		return
	}
	if cmd.Name == "new" && strings.TrimSpace(ctx.Args) == "" {
		s.handleNewSession(ctx)
		return
	}
	if cmd.Name == "resume" && strings.TrimSpace(ctx.Args) == "" {
		s.handleResumeSelect(ctx)
		return
	}
	if cmd.Name == "resume" && strings.TrimSpace(ctx.Args) != "" {
		s.handleResumeArgs(ctx, strings.TrimSpace(ctx.Args))
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
	// Surface a transient "Running /name…" chip while the command
	// executes (mirrors dispatchInput) so slow commands like
	// /compact show feedback; cleared when it completes.
	s.setActivityChip("Running /"+parsed.Name+"…", minitui.StatusInfo)
	s.refreshStatusBar()
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
	s.clearActivityChip()
	s.refreshStatusBar()
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

	// Single model: no need for a select UI.
	if len(mv.Profiles) == 1 {
		ctx.Write(fmt.Sprintf("Only one model available: %s (%s).\n",
			mv.Profiles[0].Name, mv.Profiles[0].Model))
		ctx.Write("✓ /model completed\n")
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
		// Prefix every option with the provider display name so the user can
		// always tell which provider a model belongs to even with many
		// profiles across several providers. Profiles are already ordered by
		// ID ("<provider>.<model>"), so models of one provider are adjacent.
		providerLabel := strings.TrimSpace(profile.ProviderName)
		if providerLabel == "" {
			providerLabel = strings.TrimSpace(profile.Provider)
		}
		label := strings.TrimSpace(profile.Name)
		if providerLabel != "" {
			label = providerLabel + " / " + label
		}
		desc := profile.Model
		if profile.Selected || profile.ID == currentProfileID {
			selectedIdx = i
			desc += " [active]"
		}
		options = append(options, minitui.SelectOption{
			Label:       label,
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

	// Render a header line in the output area so the user
	// knows a selection is in progress (the dropdown appears
	// as an overlay, not inline).
	ctx.Write(fmt.Sprintf("Available models (%d profiles):\n", len(mv.Profiles)))

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

	// No-op if selecting the already-active profile.
	if chosen.Selected || chosen.ID == currentProfileID {
		ctx.Write(fmt.Sprintf("Already using %s (%s).\n", chosen.Name, chosen.Model))
		ctx.Write("✓ /model completed\n")
		return
	}

	// Switch the session to the chosen profile.
	_, err = s.backend.SetSessionModelProfile(context.Background(), s.sessionID, chosen.ID)
	if err != nil {
		ctx.Write("Error: failed to switch model: " + err.Error() + "\n")
		return
	}

	ctx.Write(fmt.Sprintf("Model switched to %s (%s).\n", chosen.Name, chosen.Model))

	// Refresh the snapshot so the status bar picks up the new
	// model name on the next renderStatus call.
	s.refreshSnapshot()
	ctx.Write("✓ /model completed\n")
}

// handleResumeSelect shows a secondary dropdown to pick a session
// to resume. Sessions from the current workspace are listed first,
// sorted by most recent activity (newest first).
func (s *Session) handleResumeSelect(ctx *minitui.CommandContext) {
	allSessions, err := s.backend.ListSessions(context.Background(), rtbackend.SessionListFilter{})
	if err != nil {
		ctx.Write("Error: failed to list sessions: " + err.Error() + "\n")
		return
	}
	if len(allSessions) == 0 {
		ctx.Write("No saved sessions found.\n")
		return
	}

	// Determine current workspace project dir
	currentProjectDir := ""
	if s.cfg != nil {
		currentProjectDir = strings.TrimSpace(s.cfg.WorkspaceDir)
		if currentProjectDir == "" {
			currentProjectDir = strings.TrimSpace(s.cfg.ProjectDir)
		}
	}
	currentProjectDir = filepath.Clean(currentProjectDir)

	// Split into current workspace vs others, both already sorted by UpdatedAt desc
	var current, others []rtbackend.ListedSession
	for _, session := range allSessions {
		sessionProjectDir := ""
		if session.Locator.Metadata != nil {
			sessionProjectDir = filepath.Clean(strings.TrimSpace(session.Locator.Metadata["project_dir"]))
		}
		if currentProjectDir != "" && sessionProjectDir == currentProjectDir {
			current = append(current, session)
		} else {
			others = append(others, session)
		}
	}

	// Build select options: current workspace first, then others
	options := make([]minitui.SelectOption, 0, len(allSessions))
	for _, session := range current {
		options = append(options, sessionSelectOption(session))
	}
	if len(current) > 0 && len(others) > 0 {
		options = append(options, minitui.SelectOption{
			Label:       "────────── other workspaces ──────────",
			Description: "",
		})
	}
	for _, session := range others {
		options = append(options, sessionSelectOption(session))
	}

	ctx.Write(fmt.Sprintf("Sessions (%d total):\n", len(allSessions)))

	idx := ctx.Select("Choose session · ↑↓ navigate · Enter select · Esc cancel", options)
	if idx < 0 {
		ctx.Write("Session selection cancelled.\n")
		return
	}

	// Map back through our split lists
	var chosen rtbackend.ListedSession
	if idx < len(current) {
		chosen = current[idx]
	} else if len(current) > 0 && len(others) > 0 && idx == len(current) {
		ctx.Write("Session selection cancelled.\n")
		return
	} else {
		offset := idx - len(current)
		if len(current) > 0 && len(others) > 0 {
			offset--
		}
		if offset >= 0 && offset < len(others) {
			chosen = others[offset]
		} else {
			ctx.Write("Session selection cancelled.\n")
			return
		}
	}

	ctx.Write(fmt.Sprintf("Switching to session %s:%s...\n", chosen.Locator.Channel, chosen.Locator.Key))

	locator := rtbackend.SessionLocator{
		Channel:  chosen.Locator.Channel,
		Key:      chosen.Locator.Key,
		Metadata: chosen.Locator.Metadata,
	}
	if err := s.switchSession(context.Background(), locator); err != nil {
		ctx.Write("Error: failed to switch session: " + err.Error() + "\n")
		return
	}
	ctx.Write("✓ /resume completed\n")
}

// handleNewSession creates a new session and switches to it.
func (s *Session) handleNewSession(ctx *minitui.CommandContext) {
	ctx.Write("Creating new session...\n")
	locator, err := s.backend.CreateNewSession(context.Background())
	if err != nil {
		ctx.Write("Error: failed to create new session: " + err.Error() + "\n")
		return
	}
	if err := s.switchSession(context.Background(), locator); err != nil {
		ctx.Write("Error: failed to switch to new session: " + err.Error() + "\n")
		return
	}
	ctx.Write(fmt.Sprintf("✓ New session %s:%s ready\n", locator.Channel, locator.Key))
}

// handleResumeArgs searches for a session matching the query (by session ID,
// key, or title) and switches to it if a unique match is found. If multiple
// matches are found, it lists them.
func (s *Session) handleResumeArgs(ctx *minitui.CommandContext, query string) {
	allSessions, err := s.backend.ListSessions(context.Background(), rtbackend.SessionListFilter{})
	if err != nil {
		ctx.Write("Error: " + err.Error() + "\n")
		return
	}

	queryLower := strings.ToLower(query)
	var matches []rtbackend.ListedSession
	for _, session := range allSessions {
		if strings.HasPrefix(strings.ToLower(session.SessionID), queryLower) ||
			strings.EqualFold(session.Locator.Key, query) ||
			strings.Contains(strings.ToLower(session.Title), queryLower) {
			matches = append(matches, session)
		}
	}

	if len(matches) == 0 {
		ctx.Write(fmt.Sprintf("No session found matching %q.\n", query))
		return
	}

	if len(matches) == 1 {
		chosen := matches[0]
		ctx.Write(fmt.Sprintf("Switching to %s:%s...\n", chosen.Locator.Channel, chosen.Locator.Key))
		locator := rtbackend.SessionLocator{
			Channel:  chosen.Locator.Channel,
			Key:      chosen.Locator.Key,
			Metadata: chosen.Locator.Metadata,
		}
		if err := s.switchSession(context.Background(), locator); err != nil {
			ctx.Write("Error: " + err.Error() + "\n")
			return
		}
		ctx.Write("✓ /resume completed\n")
		return
	}

	// Multiple matches: list them
	ctx.Write(fmt.Sprintf("Multiple sessions match %q:\n", query))
	for _, session := range matches {
		ctx.Write("  " + sessionSelectDesc(session) + "\n")
	}
	ctx.Write("Use /resume (no args) to pick from the full list.\n")
}

// sessionSelectDesc builds a one-line description string for a session.
func sessionSelectDesc(session rtbackend.ListedSession) string {
	name := strings.TrimSpace(session.Title)
	if name == "" || name == "-" {
		name = session.Locator.Key
	}
	desc := fmt.Sprintf("%s · %s:%s", name, session.Locator.Channel, session.Locator.Key)
	if !session.LastActivityAt.IsZero() {
		desc += fmt.Sprintf(" · %s", session.LastActivityAt.Format("2006-01-02 15:04"))
	}
	if session.Running {
		desc += " [running]"
	}
	return desc
}

// switchSession detaches from the current session and attaches to a new one,
// clearing the display and reloading the new session's state and history.
func (s *Session) switchSession(ctx context.Context, locator rtbackend.SessionLocator) error {
	// Unsubscribe from old session events
	if s.unsubscribe != nil {
		s.unsubscribe()
		s.unsubscribe = nil
	}

	// Clear any lingering activity chip
	s.clearActivityChip()

	opened, err := s.backend.OpenSession(ctx, locator)
	if err != nil {
		return fmt.Errorf("open session: %w", err)
	}
	s.sessionID = opened.SessionID

	snapshot, err := s.backend.Snapshot(ctx, opened.SessionID)
	if err != nil {
		return fmt.Errorf("load snapshot: %w", err)
	}
	s.snapshot = snapshot
	s.messageCount = len(snapshot.Messages)
	s.seenModelCallIDs = make(map[string]struct{})
	s.modelCallCount = 0
	s.assistantStreaming = false

	// Reset output: min-tui doesn't expose a clear method, so we output
	// a visual separator then re-render the new session's banner + history.
	if s.tui != nil {
		s.tui.WriteString("\n─── session switched ───\n\n")
		s.writeBanner(locator, snapshot)
		s.writeHistory(snapshot)
	}

	// Subscribe to new session events
	unsub, err := s.backend.AttachSink(opened.SessionID, events.SinkFunc(s.handleEvent))
	if err != nil {
		return fmt.Errorf("attach event sink: %w", err)
	}
	s.unsubscribe = unsub

	// Refresh context summary
	if summary, err := s.backend.ContextSummary(ctx, opened.SessionID); err == nil {
		s.ctxSummary = summary
	}
	s.setStatus(s.renderStatus("Ready"), minitui.StatusDefault)

	return nil
}

// sessionSelectOption builds a SelectOption from a ListedSession for /resume.
// Format: name · date · channel:key · working-dir
func sessionSelectOption(session rtbackend.ListedSession) minitui.SelectOption {
	key := session.Locator.Key
	channel := session.Locator.Channel

	// Build description: name · date · channel:key · working-dir
	var parts []string

	name := strings.TrimSpace(session.Title)
	if name == "" || name == "-" {
		name = key
	}
	parts = append(parts, name)

	if !session.LastActivityAt.IsZero() {
		parts = append(parts, session.LastActivityAt.Format("2006-01-02 15:04"))
	}

	parts = append(parts, fmt.Sprintf("%s:%s", channel, key))

	if session.Locator.Metadata != nil {
		if projectDir := strings.TrimSpace(session.Locator.Metadata["project_dir"]); projectDir != "" {
			parts = append(parts, truncatePathTailMint(projectDir, 40))
		}
	}

	if session.Running {
		parts = append(parts, "[running]")
	}

	return minitui.SelectOption{
		Label:       key,
		Description: strings.Join(parts, " · "),
	}
}

// truncatePathTailMint keeps only the last maxLen chars of a path, adding
// "..." prefix when truncation occurs.
func truncatePathTailMint(path string, maxLen int) string {
	if len(path) <= maxLen {
		return path
	}
	return "..." + path[len(path)-maxLen:]
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
	s.recordInputHistory(input)

	// ── local bash: ! prefix ────────────────────────────
	if strings.HasPrefix(input, "!") {
		go s.runLocalBash(ctx, strings.TrimPrefix(input, "!"))
		return nil
	}

	// ── slash commands ──────────────────────────────────
	// Run the command asynchronously so slow commands (e.g.
	// /compact) don't block the input loop, and surface a
	// "Running /name…" activity chip while it executes.
	if cmd, ok := commands.Parse(input); ok {
		s.setActivityChip("Running /"+cmd.Name+"…", minitui.StatusInfo)
		s.refreshStatusBar()
		go func() {
			result, err := s.backend.ExecuteCommand(context.Background(), sessionID, cmd)
			if s.tui != nil {
				if result.Output != "" {
					s.tui.WriteString(result.Output + "\n")
				}
				if result.DispatchError != "" {
					s.tui.WriteString("Error: " + result.DispatchError + "\n")
				}
				if err != nil {
					s.tui.WriteString("Error: " + err.Error() + "\n")
				}
				// Short confirmation in the output area; do not
				// touch the status bar so the godex heartbeat
				// stays visible.
				s.tui.WriteString("✓ /" + cmd.Name + " completed\n")
			}
			s.clearActivityChip()
			s.refreshStatusBar()
		}()
		return nil
	}

	// Echo the user's message to the output area immediately
	// so it appears inline with the assistant response that
	// follows.  Without this the message only exists
	// server-side and only appears after a session reload.
	// writeUserTurn renders the message as a markdown
	// blockquote so min-tui's built-in renderer paints it
	// with a grey background, separating it visually from
	// the assistant's plain-background prose.
	if s.tui != nil {
		s.writeUserTurn(input)
	}

	envelope := message.NewCLIEnvelope(sessionID, s.cfg.LeadName, input, s.now())
	res, err := s.backend.SubmitAsync(ctx, sessionID, envelope)
	if err != nil {
		return err
	}
	if res != nil {
		s.setActiveTurn(res.TurnID)

		// A new turn is starting.  Surface a brief "Sending…"
		// chip so the user sees the message was accepted while
		// waiting for the runner's first phase/delta event; the
		// first assistant delta (or a real phase chip) replaces
		// it.
		s.setActivityChip("Sending…", minitui.StatusInfo)
		s.refreshStatusBar()
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
//
// Output layout strategy
// ---------------------
//
// Every non-prose block (user message, tool call, warning,
// error) is emitted as a MARKDOWN BLOCKQUOTE — that is, every
// line is prefixed with "> ".  min-tui v0.3.0's built-in
// renderer recognises lines starting with "> " and applies a
// grey background (ANSI 100m), which gives us a coloured
// backdrop "for free" without us having to inject any ANSI
// escape codes ourselves.  Assistant prose, by contrast, is
// written verbatim so min-tui's renderer can apply bold,
// italic, inline code, headings, code fences, tables, etc.
//
// Why blockquote and not custom ANSI?
//
//   - We get the background colour without writing a custom
//     RenderLine, so min-tui's native markdown pipeline
//     (including Spacious-mode gaps around headings / fences
//     / tables) keeps working unmodified.
//   - Every quoted line is non-empty by construction (it
//     contains at least "> "), so it survives min-tui's
//     appendRendered guard that would otherwise drop blank
//     lines.
//   - Visual contrast is automatic: grey backdrop = anything
//     but the assistant's prose, plain backdrop = assistant
//     prose.  No colour-palette coordination required.
func (s *Session) handleEvent(event events.Event) {
	switch event.Type {
	case events.EventAssistantTextDelta:
		// First delta of a new block: ensure the
		// previous turn's content is visually closed
		// off with a blank line, then let deltas
		// stream straight through to the output
		// area.  Subsequent deltas in the same block
		// skip this guard.
		if !s.assistantStreaming {
			s.tui.WriteString("\n")
		}
		// The agent is now visibly producing output:
		// drop the transient "Sending…" chip so the
		// heartbeat returns to its clean form.
		s.clearActivityChip()
		s.assistantStreaming = true
		text := extractTextField(event)
		s.appendAssistantText(text)
	case events.EventAssistantMessageComplete:
		text := extractTextField(event)
		// When deltas were streamed, the text is already on
		// screen via appendAssistantText.  Emitting it again
		// here would duplicate the entire assistant response.
		// Only render the full text when no deltas preceded it
		// (e.g. an instant non-streaming response).
		if !s.assistantStreaming {
			s.appendAssistantText(text)
		}
		// Always close the block with a blank line so
		// the next blockquote (user/tool/warning) has
		// visual breathing room.
		s.tui.WriteString("\n\n")
		s.assistantStreaming = false
	case events.EventToolCallStarted:
		s.renderToolCallStarted(event)
	case events.EventToolCallFinished:
		s.renderToolCallFinished(event)
	case events.EventRunnerPhaseChanged:
		phase, tool := extractRunnerPhase(event)
		// The model_request phase is deliberately silent (keeps
		// the heartbeat intact) — but if we are still showing the
		// transient "Sending…" chip from a just-submitted
		// message, leave it in place until real output (a delta
		// or a tool call) replaces it.
		if phase == "model_request" && strings.TrimSpace(s.activityChip) == "Sending…" {
			s.refreshStatusBar()
			break
		}
		s.applyRunnerPhase(phase, tool)
	case events.EventTurnCompleted:
		s.clearActiveTurn(event.TurnID)
		s.assistantStreaming = false
		s.refreshSnapshot()
		// refreshSnapshot already re-renders the bar; we
		// additionally drop any lingering activity chip so
		// the heartbeat returns to its pure "Ready" form
		// even if the runner emitted no terminal phase
		// event before completing.
		s.clearActivityChip()
		s.setStatus(s.renderStatus("Ready"), minitui.StatusDefault)
	case events.EventSnapshotReady:
		s.refreshSnapshot()
	case events.EventWarningRaised:
		if np, ok := event.Payload.(events.NoticePayload); ok && np.Message != "" {
			s.writeQuoteBlock("⚠ " + np.Message)
		}
		s.setActivityChip("Warning", minitui.StatusWarning)
		s.refreshStatusBar()
	case events.EventErrorRaised:
		if np, ok := event.Payload.(events.NoticePayload); ok && np.Message != "" {
			s.writeQuoteBlock("✗ " + np.Message)
		}
		s.setActivityChip("Error", minitui.StatusError)
		s.refreshStatusBar()
	}
}

// renderToolCallStarted renders a tool call start with name
// and a condensed input summary as a blockquote block.  The
// grey background visually distinguishes tool activity from
// both user input and assistant prose.
func (s *Session) renderToolCallStarted(event events.Event) {
	if s.tui == nil {
		return
	}
	name := extractToolName(event)
	if name == "" {
		return
	}
	inputSummary := extractToolInputSummary(event)
	line := "> ▶ " + name
	if inputSummary != "" {
		line += " " + inputSummary
	}
	s.tui.WriteString(line + "\n")
}

// renderToolCallFinished appends a status line (✓ / ✗) to
// the same blockquote block opened by renderToolCallStarted.
// A trailing blank line closes the block and provides visual
// separation from the next tool call or user input.
//
// For read tools (read_file, attach_file) we additionally
// show a short preview of the file content.
// For edit tools (edit_file) we render a unified diff inside
// a markdown code fence so min-tui can display it as a
// formatted code block.
func (s *Session) renderToolCallFinished(event events.Event) {
	if s.tui == nil {
		return
	}
	name := extractToolName(event)
	output, errText := extractToolOutputError(event)
	input := extractToolInput(event)

	switch {
	case errText != "":
		s.tui.WriteString(">   ✗ " + errText + "\n")
	case output != "":
		if isReadTool(name) {
			// read_file already shows path:range in the start line;
			// emit "✓ N lines" so the user sees the actual read
			// amount next to the preview code block.
			s.tui.WriteString(fmt.Sprintf(">   ✓ %d lines\n", countReadOutputLines(output)))
		} else {
			// Show a condensed output (first line, capped).
			summary := firstLine(output, 200)
			s.tui.WriteString(">   ✓ " + summary + "\n")
		}
	default:
		if name == "" {
			s.tui.WriteString(">   ✓ done\n")
		} else {
			s.tui.WriteString(">   ✓ " + name + "\n")
		}
	}

	// Trailing blank closes the block.  Even when no
	// start event was received, the cost of one empty
	// row is small and the readability win is large.
	s.tui.WriteString("\n")

	// Enhanced rendering for read and edit tools. Only
	// these add an extra blank line above the preview /
	// diff block so it has breathing room from the ✓ row.
	switch {
	case isReadTool(name) && output != "":
		s.tui.WriteString("\n")
		s.renderReadPreview(input, output)
	case isEditTool(name) && input != nil:
		s.tui.WriteString("\n")
		s.renderEditDiffBlock(input)
	}
}

// isReadTool reports whether name is a file-reading tool.
func isReadTool(name string) bool {
	switch name {
	case "read_file", "attach_file":
		return true
	}
	return false
}

// isEditTool reports whether name is a file-editing tool.
func isEditTool(name string) bool {
	return name == "edit_file"
}

// readPreviewMaxLines caps how many lines of file content
// are echoed back after a successful read_file/attach_file.
const readPreviewMaxLines = 5

// readFileTruncationMarker matches the " ... (truncated: ...)"
// suffix that read_file appends when it returns a slice of
// a larger file. We strip it before re-emitting the content
// in a preview code block so the marker never appears inside
// a fenced block.
const readFileTruncationMarker = "... (truncated:"

// renderReadPreview emits a short preview of file content
// inside a code fence so the user can see what was read
// without having to inspect the tool output separately.
//
// read_file's Output is line-numbered ("     1\tline") and
// may end with a "... (truncated: ...)" status line. We
// strip both before placing the content in the code block.
func (s *Session) renderReadPreview(input map[string]interface{}, output string) {
	path, _ := input["path"].(string)
	if path == "" {
		return
	}

	lines := previewLinesFromReadOutput(output, readPreviewMaxLines)
	if len(lines) == 0 {
		return
	}

	// Determine a file-extension-based language hint for the
	// code fence so min-tui can apply syntax highlighting.
	lang := langFromPath(path)
	preview := strings.Join(lines, "\n")

	var b strings.Builder
	b.WriteString("```")
	b.WriteString(lang)
	b.WriteString("\n")
	b.WriteString(preview)
	if !strings.HasSuffix(preview, "\n") {
		b.WriteString("\n")
	}
	b.WriteString("```\n")
	s.tui.WriteString(b.String())
}

// previewLinesFromReadOutput returns up to max non-empty
// content lines extracted from a read_file Output, with the
// leading "     N\t" line number stripped and any trailing
// "... (truncated: ...)" status line removed.
func previewLinesFromReadOutput(output string, max int) []string {
	var lines []string
	for _, raw := range strings.Split(output, "\n") {
		l := strings.TrimRight(raw, "\r")
		// Drop read_file's truncation status line; it would
		// otherwise appear inside the code block as a
		// syntactically-valid but semantically wrong entry.
		if strings.Contains(l, readFileTruncationMarker) {
			continue
		}
		// Strip the "     N\t" line-number prefix added by
		// read_file. If the line is not prefixed, return it
		// unchanged so the preview still works for content
		// that didn't go through read_file (e.g. attach_file).
		l = stripReadLineNumber(l)
		if strings.TrimSpace(l) == "" {
			continue
		}
		lines = append(lines, l)
		if len(lines) >= max {
			break
		}
	}
	return lines
}

// stripReadLineNumber removes a leading read_file-style
// "<spaces>N\t" line-number prefix. Anything else is returned
// unchanged.
//
// read_file formats numbers with %6d (right-aligned, 6 wide),
// producing 1-to-5 leading spaces before the digits. We match
// that shape: zero or more leading spaces, then a run of
// digits, then a literal tab. The tab is mandatory — it
// disambiguates from genuine content lines that happen to
// start with digits (e.g. a markdown ordered list item).
func stripReadLineNumber(line string) string {
	i := 0
	for i < len(line) && line[i] == ' ' {
		i++
	}
	if i == len(line) {
		return line
	}
	j := i
	for j < len(line) && line[j] >= '0' && line[j] <= '9' {
		j++
	}
	if j == i || j == len(line) || line[j] != '\t' {
		return line
	}
	return line[j+1:]
}

// langFromPath returns a short language tag for common file
// extensions so min-tui's syntax highlighter can colour the
// read-preview code block.
func langFromPath(p string) string {
	ext := strings.ToLower(filepath.Ext(p))
	switch ext {
	case ".go":
		return "go"
	case ".py":
		return "python"
	case ".js", ".mjs", ".cjs":
		return "javascript"
	case ".ts", ".tsx", ".mts", ".cts":
		return "typescript"
	case ".rs":
		return "rust"
	case ".sh", ".bash", ".zsh":
		return "bash"
	case ".sql":
		return "sql"
	case ".yaml", ".yml":
		return "yaml"
	case ".json":
		return "json"
	case ".md", ".markdown":
		return "markdown"
	case ".html", ".htm":
		return "html"
	case ".css":
		return "css"
	case ".toml":
		return "toml"
	case ".xml":
		return "xml"
	case ".c", ".h":
		return "c"
	case ".cpp", ".cc", ".cxx", ".hpp":
		return "cpp"
	case ".java":
		return "java"
	case ".proto":
		return "protobuf"
	case ".tf":
		return "hcl"
	case ".dockerfile", "Dockerfile":
		return "dockerfile"
	case ".makefile", "Makefile":
		return "makefile"
	}
	return ""
}

// renderEditDiffBlock generates a unified diff of old/new text
// and renders it using min-tui's built-in RenderDiff which
// applies ANSI colouring, line numbers, and syntax highlighting.
//
// Header layout: the path / "N edits" line is emitted exactly
// once at the top, even when the input carries multiple edits.
// We deliberately do NOT prepend an "edit i/N" sub-header per
// edit — repeating the same path three times is noisy and the
// diff content is self-describing.
func (s *Session) renderEditDiffBlock(input map[string]interface{}) {
	edits := parseEditInputMint(input)
	if len(edits) == 0 {
		return
	}

	filePath, _ := input["path"].(string)
	if filePath != "" {
		header := fmt.Sprintf("── %s ──", filePath)
		if len(edits) > 1 {
			header = fmt.Sprintf("── %s (%d edits) ──", filePath, len(edits))
		}
		s.tui.WriteString(header + "\n")
	}

	for i, edit := range edits {
		diffText := toUnifiedDiff(filePath, edit.oldText, edit.newText)
		if diffText != "" {
			s.tui.WriteString(minitui.RenderDiff(diffText, true))
			s.tui.WriteString("\n")
		}

		if i < len(edits)-1 {
			s.tui.WriteString("\n")
		}
	}
}

// editPairMint mirrors the edit pair parsed from tool input.
type editPairMint struct {
	oldText string
	newText string
}

// parseEditInputMint extracts edit pairs from the tool input map.
// edit_file's schema (see EditFileDefinition) supports two shapes:
//
//   - Single edit:    { "old_text": "...", "new_text": "..." }
//   - Multiple edits: { "edits": [ { "old_text": "...", "new_text": "..." }, ... ] }
//
// Keys are read in both snake_case (canonical) and camelCase
// form to stay robust against any wrapping layer that renames
// fields mid-flight.
//
// Shape-selection rule: the presence of the "edits" key — at
// any type or length — is treated as authoritative. If the key
// exists we never silently fall through to the single-edit
// branch, even if the list is empty or every element fails to
// parse. That fall-through used to mask mis-wired callers
// (e.g. a tool that emits `edits: []` for "no-op edit" would
// suddenly start rendering whatever the input happened to also
// contain in `old_text` / `new_text`). The single-edit branch
// is consulted only when "edits" is absent.
func parseEditInputMint(input map[string]interface{}) []editPairMint {
	if input == nil {
		return nil
	}

	// Multiple-edits form: if "edits" is present, it is
	// authoritative regardless of length or element shape.
	if _, hasEdits := input["edits"]; hasEdits {
		rawEdits := input["edits"]
		editsList, ok := rawEdits.([]interface{})
		if !ok {
			// "edits" is present but not a JSON array.
			// Honour the caller's intent (multi-edit) by
			// returning nil rather than falling through to
			// the single-edit shape, which would render
			// unrelated fields.
			return nil
		}
		var result []editPairMint
		for _, raw := range editsList {
			editMap, ok := raw.(map[string]interface{})
			if !ok {
				// Skip non-map entries (e.g. nil) rather
				// than bailing out — the surrounding list
				// may still contain valid edits.
				continue
			}
			oldText := pickString(editMap, "old_text", "oldText")
			newText := pickString(editMap, "new_text", "newText")
			if oldText == "" && newText == "" {
				continue
			}
			result = append(result, editPairMint{oldText: oldText, newText: newText})
		}
		return result
	}

	// Single-edit form (only consulted when "edits" is absent).
	oldText := pickString(input, "old_text", "oldText")
	newText := pickString(input, "new_text", "newText")
	if oldText == "" && newText == "" {
		return nil
	}
	return []editPairMint{{oldText: oldText, newText: newText}}
}

// pickString returns the first non-empty string value found
// under any of the given keys. Used to tolerate both
// snake_case (canonical schema) and camelCase variants.
func pickString(m map[string]interface{}, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok {
			return v
		}
	}
	return ""
}

// toUnifiedDiff returns a standard unified diff string suitable
// for minitui.RenderDiff.  It includes --- / +++ / @@ headers
// so that RenderDiff can colour hunk headers, file paths, and
// +/- lines, and apply per-line syntax highlighting.
//
// When the old and new text are identical it returns "" so
// callers can skip rendering a no-op diff block.
func toUnifiedDiff(path, oldText, newText string) string {
	if oldText == newText {
		return ""
	}

	oldLines := splitLinesPreserve(oldText)
	newLines := splitLinesPreserve(newText)

	var b strings.Builder

	// Use a simple path so the diff header is compact;
	// RenderDiff extracts the language from +++/--- paths.
	file := path
	if file == "" {
		file = "file"
	}
	b.WriteString("--- a/" + file + "\n")
	b.WriteString("+++ b/" + file + "\n")
	b.WriteString(fmt.Sprintf("@@ -1,%d +1,%d @@\n", len(oldLines), len(newLines)))

	oldIdx, newIdx := 0, 0
	for oldIdx < len(oldLines) || newIdx < len(newLines) {
		switch {
		case oldIdx < len(oldLines) && newIdx < len(newLines) &&
			oldLines[oldIdx] == newLines[newIdx]:
			fmt.Fprintf(&b, " %s", oldLines[oldIdx])
			oldIdx++
			newIdx++
		case oldIdx < len(oldLines) && newIdx < len(newLines):
			// Changed line: - old / + new pair.
			fmt.Fprintf(&b, "-%s", oldLines[oldIdx])
			fmt.Fprintf(&b, "+%s", newLines[newIdx])
			oldIdx++
			newIdx++
		case oldIdx < len(oldLines):
			fmt.Fprintf(&b, "-%s", oldLines[oldIdx])
			oldIdx++
		default:
			fmt.Fprintf(&b, "+%s", newLines[newIdx])
			newIdx++
		}
	}
	return b.String()
}

// splitLinesPreserve returns the lines of s, including their
// trailing \n. An input that does not end with \n keeps its
// last line without a trailing newline. The empty string
// yields a single empty (newline-less) line, matching
// common diff-tool conventions for a one-line file.
func splitLinesPreserve(s string) []string {
	if s == "" {
		return []string{""}
	}
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] != '\n' {
			continue
		}
		lines = append(lines, s[start:i+1])
		start = i + 1
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

// extractToolInput extracts the input map from a tool call event.
func extractToolInput(event events.Event) map[string]interface{} {
	if event.Payload == nil {
		return nil
	}
	if tp, ok := event.Payload.(events.ToolCallPayload); ok {
		return tp.Input
	}
	if m, ok := event.Payload.(map[string]interface{}); ok {
		if v, ok := m["input"].(map[string]interface{}); ok {
			return v
		}
	}
	return nil
}

// ── turn rendering helpers ─────────────────────────────────────

// appendAssistantText writes a streaming text delta to the
// current assistant block.  Min-tui buffers the bytes and
// renders them incrementally.  We do NOT inject any prefix —
// assistant prose goes straight through so min-tui's markdown
// pipeline (bold, italic, inline code, headings, code
// fences, tables) applies unchanged.
func (s *Session) appendAssistantText(text string) {
	if text == "" {
		return
	}
	s.tui.WriteString(text)
}

// quoteLine formats a single body line as a markdown
// blockquote line ("> ...") that min-tui's built-in renderer
// will paint with a grey background.  Internal newlines are
// preserved by prefixing every line of the input.
func quoteLine(text string) string {
	if text == "" {
		return ">"
	}
	var b strings.Builder
	first := true
	for _, line := range strings.Split(text, "\n") {
		if !first {
			b.WriteByte('\n')
		}
		b.WriteString("> ")
		b.WriteString(line)
		first = false
	}
	return b.String()
}

// writeQuoteBlock emits the given text as a blockquote block
// — one "> "-prefixed line per source line, followed by a
// trailing blank.  Used for user messages, tool calls,
// warnings and errors: anything that is NOT assistant prose.
//
// Every line in the resulting block is non-empty (because of
// the "> " prefix), which means min-tui's appendRendered
// guard keeps them on the canvas; a plain blank line would
// have been dropped and the visual gap would disappear.
func (s *Session) writeQuoteBlock(text string) {
	if s.tui == nil || text == "" {
		return
	}
	s.tui.WriteString(quoteLine(text))
	s.tui.WriteString("\n\n")
}

// writeUserTurn emits the user's message as a blockquote
// block so min-tui's renderer paints it with the grey
// background.  This is the core of the readability fix:
// every line of the user's message sits on a coloured
// backdrop that visually distinguishes it from the assistant's
// plain-background prose.
//
// The leading blank gives the block visual breathing room
// above; this is the live-rendering entry point.  Replay
// from stored history must use replayUserTurn instead, since
// the previous turn's trailing blank already supplies the
// separator and an extra "\n" would compound into three
// consecutive blank lines between two stored user messages.
func (s *Session) writeUserTurn(text string) {
	if s.tui == nil || text == "" {
		return
	}
	s.tui.WriteString("\n")
	s.writeQuoteBlock(text)
}

// replayUserTurn re-emits a stored user message during
// history reload.  It is the stored-history counterpart to
// writeUserTurn and intentionally omits the leading blank:
// the previous stored turn (or the canvas top) already
// provides separation, and writeQuoteBlock's own trailing
// "\n\n" closes the block.
func (s *Session) replayUserTurn(text string) {
	if s.tui == nil || text == "" {
		return
	}
	s.writeQuoteBlock(text)
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
	s.setStatus(s.renderStatusWith(s.ctxSummary, s.statusLabel()), minitui.StatusDefault)
}

// ── output rendering ─────────────────────────────────────────────

// writeBanner writes the initial header to the TUI output area
// as plain markdown so min-tui's renderer applies its native
// styling (bold for the title, italic / inline code for the
// metadata).  This avoids us having to hand-roll a colour
// palette; the visual hierarchy comes from markdown itself.
func (s *Session) writeBanner(locator rtbackend.SessionLocator, snap rtbackend.Snapshot) {
	workspace := s.cfg.WorkspaceDir
	if workspace == "" {
		workspace = "(unknown workspace)"
	}
	var b strings.Builder
	b.WriteString("# 🤖 GoDex — min-tui\n")
	b.WriteString("\n")
	b.WriteString("- session:  `")
	b.WriteString(locator.Channel + ":" + locator.Key)
	b.WriteString("`\n")
	b.WriteString("- workspace: `")
	b.WriteString(workspace)
	b.WriteString("`\n")
	b.WriteString("- model:     `")
	b.WriteString(s.resolveModelName())
	b.WriteString("`\n")
	b.WriteString("\n")
	s.tui.WriteString(b.String())
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

// writeStoredMessage re-emits a single replayed message
// using the same blockquote / prose split as live events:
// user → quoted, assistant → plain markdown, tool/system →
// plain markdown.  Keeping the layout identical to live
// rendering avoids the jarring "history was styled one way,
// live messages are styled another" mismatch.
func (s *Session) writeStoredMessage(msg protocol.Message) {
	text := protocol.MessageText(msg)
	role := strings.ToLower(strings.TrimSpace(msg.Role))
	switch role {
	case "user":
		if text != "" {
			// Replay path: no leading blank (the prior turn
			// already separated this message). See
			// writeUserTurn vs replayUserTurn for why these
			// two paths must stay distinct.
			s.replayUserTurn(text)
		}
	case "assistant":
		if text != "" {
			// Replayed assistant prose flows verbatim;
			// the trailing blank closes the block the same
			// way EventAssistantMessageComplete does.
			s.tui.WriteString(text)
			s.tui.WriteString("\n\n")
		}
	case "tool", "system":
		if text != "" {
			s.tui.WriteString(text)
			s.tui.WriteString("\n\n")
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

// applyRunnerPhase translates a runner-phase event into a
// short activity chip that is *inserted* into the heartbeat
// status bar instead of replacing it.  The reasoning is that
// during a normal turn the model_request phase dominates the
// wall-clock time (it is when the agent is waiting on the
// upstream model) and is exactly when the user most wants to
// see the live context-pressure/calls/msgs heartbeat.  So we
// only surface phases that carry new, scannable information
// beyond "the model is thinking":
//
//   - executing + tool name        -> "Running <tool>"
//   - awaiting_tools / final_...   -> "Waiting for tools" / etc.
//   - recovery / error / interrupted -> error-styled chip
//
// model_request (and the closely related context_sanitized
// "thinking" indicator) are deliberately not rendered: the
// streaming assistant text deltas already make it visually
// obvious that work is happening, and overwriting the
// heartbeat with a transient "Thinking…" label was the
// regression this rewrite fixes.
func (s *Session) applyRunnerPhase(phase, tool string) {
	chip, style := runnerPhaseChip(phase, tool)
	s.setActivityChip(chip, style)
	s.refreshStatusBar()
}

// runnerPhaseChip returns the chip text + style for a given
// runner phase.  Empty chip means "do not surface this
// phase", which keeps the heartbeat intact.
func runnerPhaseChip(phase, tool string) (string, minitui.StatusStyle) {
	switch phase {
	case "model_request", "context_sanitized", "":
		// Deliberately silent: this is the steady state of
		// any in-flight turn and would otherwise drown out
		// the heartbeat.
		return "", minitui.StatusDefault
	case "executing":
		if tool != "" {
			return "Running " + tool, minitui.StatusInfo
		}
		return "Running tools", minitui.StatusInfo
	case "awaiting_tools":
		return "Awaiting tools", minitui.StatusInfo
	case "tools_completed":
		return "Processing results", minitui.StatusInfo
	case "final_response":
		return "Writing response", minitui.StatusInfo
	case "recovery_attempted":
		return "Recovering", minitui.StatusWarning
	case "interrupted":
		return "Interrupted", minitui.StatusWarning
	case "error":
		return "Error", minitui.StatusError
	case "injection_drained":
		return "Processing input", minitui.StatusInfo
	default:
		// Unknown phases are surfaced only if they look
		// user-meaningful; otherwise stay silent so we do
		// not regress the heartbeat for benign runtime
		// noise.
		if phase == "" {
			return "", minitui.StatusDefault
		}
		return phase, minitui.StatusInfo
	}
}

// setActivityChip stores a new activity chip under statusMu.
// It does NOT push the change to the bar — callers should
// invoke refreshStatusBar (or the next status refresh will
// pick it up on its own, e.g. via refreshSnapshot).
func (s *Session) setActivityChip(chip string, style minitui.StatusStyle) {
	s.statusMu.Lock()
	s.activityChip = chip
	s.activityStyle = style
	s.statusMu.Unlock()
}

// clearActivityChip removes any active chip so the bar
// returns to its pure heartbeat form.  Used when a turn
// finishes or the user cancels.
func (s *Session) clearActivityChip() {
	s.setActivityChip("", minitui.StatusDefault)
}

// statusLabel returns the status-bar label: "Ready" when
// idle, "Working" when a turn is active.  This lets the user
// distinguish "waiting for the model" from "agent crashed
// silently" even when no streaming text deltas are flowing.
func (s *Session) statusLabel() string {
	s.activeTurnMu.Lock()
	turnID := s.activeTurnID
	s.activeTurnMu.Unlock()
	if turnID != "" {
		return "Working"
	}
	return "Ready"
}

// refreshStatusBar re-renders the heartbeat status line using
// the currently cached ctx summary, message count, model
// call count, and activity chip, and pushes it to the
// min-tui frontend.  Cheap to call: it does no I/O against
// the backend.
func (s *Session) refreshStatusBar() {
	s.setStatus(s.renderStatus(s.statusLabel()), minitui.StatusDefault)
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
	// Activity chip (if any) sits between the model name and
	// the blocker chip so the heartbeat prefix remains
	// continuously visible.  Empty when no special phase is
	// active — this is the common case (including the
	// long-running model_request phase, which we deliberately
	// do not surface as a chip so the heartbeat stays calm).
	if chip := strings.TrimSpace(s.activityChip); chip != "" {
		parts = append(parts, chip)
	}
	// Permission blocker takes priority if present.
	if blocker := s.snapshot.ActivePermissionBlocker; blocker != nil {
		blockerParts := []string{"Blocked"}
		if id := strings.TrimSpace(blocker.RequestID); id != "" {
			blockerParts = append(blockerParts, id)
		}
		if action := strings.TrimSpace(blocker.ToolName + " " + blocker.Action); strings.TrimSpace(action) != "" {
			blockerParts = append(blockerParts, action)
		}
		blockerParts = append(blockerParts, "Ctrl+P approve/deny")
		parts = append(parts, strings.Join(blockerParts, " "))
	}
	// Context usage: "128k/512k 25%" — denominator is the
	// compress threshold (same as bubbletea TUI), not
	// TotalTokenEstimate which often equals TokenEstimate.
	if pct := ctxPctWithThreshold(summary, s.cfg.CompressThreshold); pct != "" {
		parts = append(parts, pct)
	}
	// Cumulative session token consumption ("tok 1.2M"): provider-reported
	// totals that survive compactions and conversation clears.
	if total := summary.CumulativeTokens; total > 0 {
		parts = append(parts, fmt.Sprintf("tok %s", compactTokenCount(total)))
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

// compactTokenCount formats a token count in the same compact style as the
// rest of the status bar ("128k", "1.2M", "845").
func compactTokenCount(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%dk", (n+500)/1000)
	default:
		return fmt.Sprintf("%d", n)
	}
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
	// read_file gets a richer summary: path + offset + limit.
	if extractToolName(event) == "read_file" {
		return readFileInputSummary(input)
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

// readFileInputSummary formats the read_file input as an IDE-style
// path:line-range. When offset > 1 or limit > 0 the range is appended;
// otherwise only the path is shown.
func readFileInputSummary(input map[string]interface{}) string {
	path, _ := input["path"].(string)
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	off, hasOff := numFromAny(input["offset"])
	lim, hasLim := numFromAny(input["limit"])
	if hasOff && hasLim && lim > 0 {
		end := off + lim - 1
		return fmt.Sprintf("%s:%d-%d", ellipsizeMint(path, 80), off, end)
	}
	if hasOff && off > 1 {
		return fmt.Sprintf("%s (from line %d)", ellipsizeMint(path, 80), off)
	}
	return ellipsizeMint(path, 80)
}

// numFromAny extracts an integer from an interface{} value that may
// be int, int64, float64 (JSON), or json.Number.
func numFromAny(v interface{}) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	case fmt.Stringer:
		var i int
		if _, err := fmt.Sscanf(n.String(), "%d", &i); err == nil {
			return i, true
		}
	}
	return 0, false
}

// countReadOutputLines returns the number of content lines in a
// read_file Output, excluding the truncation marker line.
func countReadOutputLines(output string) int {
	n := 0
	for _, line := range strings.Split(output, "\n") {
		l := strings.TrimRight(line, "\r")
		if l == "" || strings.Contains(l, readFileTruncationMarker) {
			continue
		}
		n++
	}
	return n
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

// runLocalBash executes a shell command via localbash and
// streams the output back to the TUI.  It is triggered by
// typing !command in the input box.
//
// The output is wrapped in a code fence so min-tui applies
// syntax highlighting.  The status bar is updated with the
// command being run.
func (s *Session) runLocalBash(parentCtx context.Context, shellCmd string) {
	shellCmd = strings.TrimSpace(shellCmd)
	if shellCmd == "" {
		return
	}
	if s.tui == nil {
		return
	}

	// Echo the command.
	s.tui.WriteString("\n> !" + shellCmd + "\n\n")
	s.tui.SetStatus("Running: "+shellCmd, minitui.StatusWarning)

	// Create a cancellable context so the user can interrupt.
	ctx, cancel := context.WithCancel(parentCtx)
	defer cancel()

	// Store cancel so the main loop can trigger it on Ctrl+C.
	s.bashCancel = cancel
	defer func() { s.bashCancel = nil }()

	// Stream output.
	ch := localbash.RunWithTimeout(ctx, s.cfg.WorkspaceDir, shellCmd)
	for chunk := range ch {
		if chunk.Output != "" {
			s.tui.WriteString(chunk.Output)
		}
		if chunk.Err != nil {
			if errors.Is(chunk.Err, context.Canceled) ||
				errors.Is(chunk.Err, context.DeadlineExceeded) {
				s.tui.WriteString("\n✗ Cancelled\n")
			} else {
				s.tui.WriteString("\n✗ " + chunk.Err.Error() + "\n")
			}
			s.tui.SetStatus("Bash error", minitui.StatusError)
			return
		}
	}

	s.tui.SetStatus("Bash completed", minitui.StatusSuccess)
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
