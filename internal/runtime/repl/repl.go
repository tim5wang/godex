package repl

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/domain/events"
	"github.com/tim5wang/godex/internal/domain/message"
	"github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/commands"
	"github.com/tim5wang/godex/internal/tools"
)

var defaultLocator = backend.SessionLocator{Channel: "local", Key: "default"}

// Session runs the interactive terminal loop against the shared runtime backend.
type Session struct {
	cfg     *config.Config
	backend *backend.Service
	stdout  io.Writer
	stderr  io.Writer
	// newEd produces a grapheme-aware line editor backed by
	// bubbles/textarea + tea.Program. It replaces the previous
	// chzyer/readline factory, which moved the cursor by raw byte
	// offset and left the cursor "half a character" in when the
	// line contained CJK. Overridable by tests so they can inject
	// a fake editor that returns canned lines without spawning
	// a tea.Program.
	newEd   func(ctx context.Context, prompt string, in io.Reader, out io.Writer) (*Editor, error)
	session string
	// printMu is held by both handleEvent (via printf) and the
	// Run loop (via writePrompt and println) so a streaming
	// assistant text delta cannot interleave with the prompt or
	// with command output. It is a *sync.Mutex (not a value) so
	// the field never gets copied by struct literals; go vet's
	// copylocks checker would otherwise flag any test or future
	// helper that does &Session{printMu: someMutex}.
	printMu *sync.Mutex
}

// New creates a REPL session bound to the shared backend.
func New(cfg *config.Config, service *backend.Service, stdout, stderr io.Writer) *Session {
	return &Session{
		cfg:     cfg,
		backend: service,
		stdout:  stdout,
		stderr:  stderr,
		newEd:   newEditor,
		printMu: &sync.Mutex{},
	}
}

// Run starts the interactive loop.
func (s *Session) Run(ctx context.Context) error {
	opened, err := s.backend.OpenSession(ctx, defaultLocator)
	if err != nil {
		return fmt.Errorf("open repl session: %w", err)
	}
	s.session = opened.SessionID
	s.printBanner()

	unsubscribe, err := s.backend.AttachSink(s.session, events.SinkFunc(s.handleEvent))
	if err != nil {
		return fmt.Errorf("attach repl event sink: %w", err)
	}
	defer unsubscribe()

	ed, err := s.newEd(ctx, "> ", os.Stdin, s.stdout)
	if err != nil {
		return fmt.Errorf("initialize interactive prompt: %w", err)
	}
	defer ed.Close()

	for {
		// The REPL loop owns the prompt. Editor is configured
		// with io.Discard (see newEditor) so tea.Program never
		// touches REPL stdout, and we print "> " ourselves under
		// the same printMu that guards handleEvent's printf calls
		// so a streaming assistant text delta cannot tear the
		// prompt from the line the user is about to type on.
		s.writePrompt(s.stdout)
		line, err := ed.ReadLine()
		// Ctrl+D (EOF) and Ctrl+C (clean quit) both flow through
		// here. The editor returns io.EOF on either path; a
		// genuinely empty buffer on Enter is reported as
		// ErrEmptyLine and silently re-prompts, matching the
		// historical readline behaviour.
		if err == io.EOF {
			return nil
		}
		if errors.Is(err, ErrEmptyLine) {
			continue
		}
		if err != nil {
			return fmt.Errorf("read input: %w", err)
		}

		input := strings.TrimSpace(line)
		if input == "" {
			continue
		}

		if cmd, ok := commands.Parse(input); ok {
			result, execErr := s.backend.ExecuteCommand(ctx, s.session, cmd)
			if result.Output != "" {
				s.println(s.stdout, result.Output)
			}
			if execErr != nil && ctx.Err() != nil {
				return execErr
			}
			continue
		}

		result, submitErr := s.backend.Submit(ctx, s.session, message.NewCLIEnvelope(s.session, s.cfg.LeadName, input, time.Now()))
		if result != nil && result.PendingApproval {
			items, pendingErr := s.backend.PendingPermissions(ctx, s.session)
			if pendingErr != nil {
				s.printf(s.stderr, "Warning: failed to load pending approvals: %v\n", pendingErr)
			} else {
				s.println(s.stdout, renderPendingApproval(result.PendingRequestID, s.session, items))
			}
		}
		if submitErr != nil && ctx.Err() != nil {
			return submitErr
		}
	}
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
		status := item.Status
		if status == "" {
			status = tools.PermissionStatusPending
		}
		lines = append(lines, "Status: "+string(status))
		if toolName := strings.TrimSpace(req.ToolName); toolName != "" {
			lines = append(lines, "Tool: "+toolName)
		}
		if action := strings.TrimSpace(req.Action); action != "" {
			lines = append(lines, "Action: "+action)
		}
		if intent := strings.TrimSpace(tools.PermissionIntentSummary(item)); intent != "" {
			lines = append(lines, "Intent: "+intent)
		}
		if risk := strings.TrimSpace(tools.PermissionRiskSummary(req)); risk != "" {
			lines = append(lines, "Risk: "+risk)
		}
		if expiry := strings.TrimSpace(tools.PermissionExpirySummary(item, time.Now())); expiry != "" {
			lines = append(lines, "Expiry: "+expiry)
		}
		if command := strings.TrimSpace(req.Command); command != "" {
			lines = append(lines, "Command: "+command)
		}
		if len(req.Paths) > 0 {
			lines = append(lines, "Paths: "+strings.Join(req.Paths, ", "))
		}
		if reason := strings.TrimSpace(item.Reason); reason != "" {
			lines = append(lines, "Reason: "+reason)
		}
	}
	lines = append(lines,
		"",
		"Inspect approvals: /approve status",
		"Approve once: /approve",
		"Approve current task: /approve task",
		"Approve for session: /approve session",
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

func (s *Session) printBanner() {
	s.println(s.stdout, "🤖 GoDex - AI Coding Agent")
	s.println(s.stdout, "Type your messages (Ctrl+C to exit)")
	s.println(s.stdout, "Commands: /compact, /tasks, /team, /inbox, /todos, /insights, /doctor, /help")
	s.println(s.stdout)
}

func (s *Session) handleEvent(event events.Event) {
	switch event.Type {
	case events.EventAssistantTextDelta:
		if payload, ok := event.Payload.(events.TextPayload); ok && payload.Text != "" {
			s.printf(s.stdout, "%s", payload.Text)
		}
	case events.EventAssistantMessageComplete:
		if payload, ok := event.Payload.(events.TextPayload); ok && payload.Text != "" {
			s.printf(s.stdout, "\n")
		}
	case events.EventToolCallFinished:
		if payload, ok := event.Payload.(events.ToolCallPayload); ok {
			output := payload.Output
			if len(output) > 200 {
				output = output[:200]
			}
			s.printf(s.stdout, "> %s:\n%s\n", payload.Name, output)
		}
	case events.EventWarningRaised:
		if payload, ok := event.Payload.(events.NoticePayload); ok && payload.Message != "" {
			s.printf(s.stderr, "Warning: %s\n", payload.Message)
		}
	case events.EventErrorRaised:
		if payload, ok := event.Payload.(events.NoticePayload); ok && payload.Message != "" {
			s.printf(s.stderr, "Error: %s\n", payload.Message)
		}
	}
}

func (s *Session) println(w io.Writer, args ...interface{}) {
	s.printMu.Lock()
	defer s.printMu.Unlock()
	fmt.Fprintln(w, args...)
}

// writePrompt writes the REPL's interactive prompt prefix ("> ")
// to w under the session's printMu. It does NOT append a newline
// — chzyer/readline parity, and the user types on the same line
// as the prompt in a non-TUI REPL. The function exists as a
// single point of truth for the prompt string so the test in
// repl_test.go can pin both the literal and the no-newline
// guarantee.
func (s *Session) writePrompt(w io.Writer) {
	s.printMu.Lock()
	defer s.printMu.Unlock()
	fmt.Fprint(w, "> ")
}

func (s *Session) printf(w io.Writer, format string, args ...interface{}) {
	s.printMu.Lock()
	defer s.printMu.Unlock()
	fmt.Fprintf(w, format, args...)
}
