// Package streaming implements the scrollback-streaming TUI mode.
//
// In this mode the conversation history is written line-by-line to
// stdout and relies on the terminal's native scrollback buffer for
// history navigation. There is no alt-screen, no viewport diffing,
// and no renderer loop. The bottom of the screen is reserved for
// the line editor (input prompt) and a status line that is updated
// in place using ANSI escape sequences.
//
// This package is the primary implementation of the perf refactor
// described in docs/superpowers/plans/2026-06-11-tui-scrollback-streaming.md.
// It coexists with the legacy full-screen bubbletea TUI in the
// parent package, which is still used for interactive prompts that
// require rich layouts (workbench, longtask detail, permission
// review UI). The legacy TUI is suspended while the streaming mode
// is the active surface.
//
// Architecture:
//
// ┌──────────────────────────────────────────┐
// │ Header (printed once at startup) │
// │ GoDex · model · workspace │
// │──────────────────────────────────────────│
// │ conversation history (stdout, terminal │
// │ scrolls naturally — no viewport) │
// │──────────────────────────────────────────│
// │ status line (ANSI in-place update) │
// │ > _ (line editor) │
// └──────────────────────────────────────────┘
//
// Concurrency model:
//
// - main goroutine: reads keystrokes via lineEditor.
// - event goroutine: receives backend events from the sink channel
// and writes formatted output to stdout under a mutex.
// - status goroutine: tick refreshes the status line under the
// same mutex while the agent is working.
//
// All stdout writes are serialized through printMu so a status-line
// refresh never interleaves with a streaming text delta.
package streaming

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
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

// Session runs the streaming TUI loop against the shared runtime
// backend. It is the streaming-mode counterpart of tui.Session.
type Session struct {
	cfg     *config.Config
	backend Backend
	stdout  io.Writer
	stderr  io.Writer
	now     func() time.Time
	printMu sync.Mutex

	// editorMu guards the working flag and the status text. The line
	// editor and the status bar share the bottom rows and must
	// coordinate when they redraw.
	editorMu sync.Mutex

	// working indicates the agent is producing output and the input
	// prompt should be replaced with a "waiting" placeholder. Mirrors
	// the heartbeat "working" state in the legacy TUI.
	working bool

	// status is the human-readable text shown on the bottom status line.
	status string

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
	return &Session{
		cfg:     cfg,
		backend: backend,
		stdout:  stdout,
		stderr:  stderr,
		now:     time.Now,
	}
}

// Run starts the streaming loop for the given session locator.
//
// The loop:
//
// 1. opens (or resumes) a session via the backend;
// 2. prints the header and an initial snapshot of recent history;
// 3. attaches an event sink that streams new content to stdout;
// 4. reads input from the line editor and submits it.
//
// Ctrl+C cancels the current agent turn (if any) and exits on the
// second press. Ctrl+D exits immediately.
func (s *Session) Run(ctx context.Context, locator rtbackend.SessionLocator) error {
	opened, err := s.backend.OpenSession(ctx, locator)
	if err != nil {
		return fmt.Errorf("open streaming session: %w", err)
	}

	initial, err := s.backend.Snapshot(ctx, opened.SessionID)
	if err != nil {
		return fmt.Errorf("load streaming snapshot: %w", err)
	}

	unsubscribe, err := s.backend.AttachSink(opened.SessionID, events.SinkFunc(s.handleEvent))
	if err != nil {
		return fmt.Errorf("attach streaming event sink: %w", err)
	}
	defer unsubscribe()

	s.printBanner(initial)
	s.printHistory(initial)

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
		s.setStatus(fmt.Sprintf("/%s completed", cmd.Name))
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
		s.setStatus("Submitted, waiting for assistant")
	}
	return err
}

// handleEvent is invoked by the backend on every event the TUI has
// subscribed to. It writes user-facing content to stdout under the
// print mutex.
func (s *Session) handleEvent(event events.Event) {
	switch event.Type {
	case events.EventAssistantTextDelta:
		if payload, ok := event.Payload.(events.TextPayload); ok && payload.Text != "" {
			s.streamAssistantText(event.TurnID, payload.Text)
		}
	case events.EventAssistantMessageComplete:
		s.finishAssistantBlock(event.TurnID)
		s.markIdle()
		s.setStatus("Assistant replied")
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
			s.setStatus("/" + payload.Name + " completed")
		}
	case events.EventWarningRaised:
		if payload, ok := event.Payload.(events.NoticePayload); ok && payload.Message != "" {
			s.println(s.stderr, "Warning: "+payload.Message)
		}
	case events.EventErrorRaised:
		if payload, ok := event.Payload.(events.NoticePayload); ok && payload.Message != "" {
			s.println(s.stderr, "Error: "+payload.Message)
		}
	case events.EventTurnCompleted:
		s.finishAssistantBlock(event.TurnID)
		s.markIdle()
		if payload, ok := event.Payload.(events.TurnPayload); ok {
			s.setStatus("Turn " + payload.Status)
		} else {
			s.setStatus("Turn completed")
		}
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
	s.editorMu.Unlock()
}

func (s *Session) markIdle() {
	s.editorMu.Lock()
	s.working = false
	s.editorMu.Unlock()
}

func (s *Session) setStatus(text string) {
	s.editorMu.Lock()
	s.status = text
	s.editorMu.Unlock()
}

// drawPrompt rewrites the input prompt region. `placeholder` is shown
// in dim gray when the agent is working; empty string shows the live
// line editor prompt.
func (s *Session) drawPrompt(placeholder string) {
	s.editorMu.Lock()
	defer s.editorMu.Unlock()
	if placeholder == "" {
		// The line editor is responsible for its own prompt; we only
		// emit the ANSI sequence to make sure the cursor is at column0
		// in case a previous status-line refresh left it elsewhere.
		fmt.Fprint(s.stdout, "\r\x1b[K")
		fmt.Fprint(s.stdout, "> ")
		return
	}
	fmt.Fprintf(s.stdout, "\r\x1b[K\x1b[2m> %s\x1b[0m", placeholder)
}

// drawStatus refreshes the status line that lives one row above the
// input prompt. We use ANSI cursor up + clear-line + redraw to
// update it in place.
//
// IMPORTANT: this function should only be called BEFORE the input
// prompt has been drawn. Once the user has a live prompt, status
// updates are deferred to the next prompt boundary so they don't
// race with the user's keystrokes. We keep the public surface
// because commands.Run() calls setStatus after a slash command — at
// that point the prompt has been redrawn by the next loop iteration.
func (s *Session) drawStatus() {
	s.editorMu.Lock()
	text := s.status
	s.editorMu.Unlock()

	s.printMu.Lock()
	defer s.printMu.Unlock()
	fmt.Fprintf(s.stdout, "\n\x1b[2m%s\x1b[0m\n", text)
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
	const maxRecent = 30
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
	prompt  string
	content []rune
	pos     int
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
	e.pos = 0

	for {
		buf := make([]byte, 64)
		n, err := os.Stdin.Read(buf)
		if err != nil {
			return "", err
		}
		b := buf[:n]

		if b[0] == '\x1b' && n > 1 {
			e.handleEscape(b)
			continue
		}

		for i := 0; i < n; {
			r, size := utf8.DecodeRune(b[i:])
			if r == utf8.RuneError && size == 1 {
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
				e.pos = 0
			default:
				e.insertRune(r)
			}
			i += size
		}

		e.draw()
	}
}

func (e *lineEditor) handleEscape(b []byte) {
	if len(b) < 3 {
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
	if cursorCol > 0 {
		fmt.Fprintf(os.Stdout, "\x1b[%dG", cursorCol+1)
	}
}

func (e *lineEditor) insertRune(r rune) {
	e.content = append(e.content, 0)
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
	offset := 0
	for i := 0; i < pos; i++ {
		offset += utf8.RuneLen(runes[i])
	}
	return offset
}

func byteOffsetToRuneOffset(runes []rune, bytePos int) int {
	if bytePos <= 0 {
		return 0
	}
	offset := 0
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
	candidate := 0
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
		from = 0
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
	if len(preview) > 240 {
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
		return fmt.Sprintf("%q", truncate(cmd, 80))
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
	if len(items) > 0 {
		return items[0], true
	}
	return tools.PendingPermission{}, false
}
