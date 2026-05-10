package repl

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chzyer/readline"

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
	newRL   func(*readline.Config) (*readline.Instance, error)
	session string
	printMu sync.Mutex
}

// New creates a REPL session bound to the shared backend.
func New(cfg *config.Config, service *backend.Service, stdout, stderr io.Writer) *Session {
	return &Session{
		cfg:     cfg,
		backend: service,
		stdout:  stdout,
		stderr:  stderr,
		newRL:   readline.NewEx,
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

	rl, err := s.newReadline()
	if err != nil {
		return fmt.Errorf("initialize interactive prompt: %w", err)
	}
	defer rl.Close()

	for {
		line, err := rl.Readline()
		if err == readline.ErrInterrupt {
			if strings.TrimSpace(line) == "" {
				return nil
			}
			continue
		}
		if err == io.EOF {
			return nil
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
		if toolName := strings.TrimSpace(req.ToolName); toolName != "" {
			lines = append(lines, "Tool: "+toolName)
		}
		if action := strings.TrimSpace(req.Action); action != "" {
			lines = append(lines, "Action: "+action)
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
		"Approve once: /approve",
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

func (s *Session) newReadline() (*readline.Instance, error) {
	historyPath := filepath.Join(s.cfg.TeamDir, "repl_history")
	return s.newRL(&readline.Config{
		Prompt:            "> ",
		HistoryFile:       historyPath,
		HistorySearchFold: true,
		InterruptPrompt:   "^C",
		EOFPrompt:         "exit",
	})
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

func (s *Session) printf(w io.Writer, format string, args ...interface{}) {
	s.printMu.Lock()
	defer s.printMu.Unlock()
	fmt.Fprintf(w, format, args...)
}
