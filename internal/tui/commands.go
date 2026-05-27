package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/tim5wang/godex/internal/domain/message"
	"github.com/tim5wang/godex/internal/services/commands"
	"github.com/tim5wang/godex/internal/services/localbash"
)

func (m *model) submitComposer() tea.Cmd {
	if m.submitting {
		return nil
	}

	input := strings.TrimRight(m.composer.Value(), "\n")
	if strings.TrimSpace(input) == "" {
		return nil
	}

	m.composer.Reset()
	m.recordInputHistory(input)
	m.submitting = true
	m.autoFollow = true

	if cmd, ok := commands.Parse(input); ok {
		if cmd.Name == "bash" || cmd.Name == "sh" {
			return m.execLocalBash(input)
		}
		m.status = fmt.Sprintf("Running /%s...", cmd.Name)
		startCmd := m.startWorking(m.status)
		execCmd := func() tea.Msg {
			_, err := m.backend.ExecuteCommand(m.ctx, m.sessionID, cmd)
			return commandFinishedMsg{Err: err}
		}
		return tea.Batch(startCmd, execCmd)
	}

	m.status = "Submitting turn..."
	startCmd := m.startWorking(m.status)
	envelope := message.NewTextEnvelope(message.SourceTUI, m.sessionID, m.cfg.LeadName, input, m.now())
	submitCmd := func() tea.Msg {
		_, err := m.backend.Submit(m.ctx, m.sessionID, envelope)
		return submitFinishedMsg{Err: err}
	}
	return tea.Batch(startCmd, submitCmd)
}

const bashTimeout = 30 * time.Second
const bashChunkInterval = 200 * time.Millisecond

// bashStreamEvent is sent from the reader goroutine to the main loop.
type bashStreamEvent struct {
	command  string
	chunk    string // accumulated output so far
	final    bool
	exitCode int
	err      error
}

func (m *model) execLocalBash(line string) tea.Cmd {
	raw := strings.TrimSpace(line)
	fields := strings.Fields(raw)
	if len(fields) < 2 {
		m.submitting = false
		m.appendOverlay(feedItem{
			ID:          "bash-usage",
			Kind:        feedError,
			Title:       "Usage",
			Body:        "Usage: /bash <shell command>",
			Summary:     "Usage: /bash <shell command>",
			RuntimeOnly: true,
			CreatedAt:   m.now(),
		})
		m.status = "Usage: /bash <shell command>"
		m.refreshViewport(false)
		return nil
	}
	shellCmd := strings.TrimSpace(raw[strings.Index(raw, " "):])

	// determine shell: /bash → bash, /sh → sh
	shellName := "sh"
	if fields[0] == "/bash" {
		shellName = "bash"
	}

	// cancel any previous still-running bash
	if m.bashCancel != nil {
		m.bashCancel()
		m.bashCancel = nil
	}

	ctx, cancel := context.WithTimeout(m.ctx, bashTimeout)
	m.bashCancel = cancel
	m.status = "Running /" + shellName + " (Ctrl+C to cancel)..."
	startCmd := m.startWorking(m.status)

	// seed the feed item so we can see it's running
	m.upsertOverlay(feedItem{
		ID:          "bash:" + shellCmd,
		Kind:        feedCommand,
		Title:       "/" + shellName + " (running)",
		Body:        "Running...",
		Summary:     "Running...",
		RuntimeOnly: true,
		CreatedAt:   m.now(),
	})

	// use shared localbash executor for streaming output
	var outCh <-chan localbash.OutputChunk
	if shellName == "bash" {
		outCh = localbash.RunBash(ctx, m.cfg.WorkspaceDir, shellCmd)
	} else {
		outCh = localbash.Run(ctx, m.cfg.WorkspaceDir, shellCmd)
	}

	// bridge localbash.OutputChunk -> bashStreamEvent
	bridgeCh := make(chan bashStreamEvent, 64)
	m.bashCh = bridgeCh
	go func() {
		defer cancel()
		defer close(bridgeCh)
		for chunk := range outCh {
			bridgeCh <- bashStreamEvent{
				command:  chunk.Command,
				chunk:    chunk.Output,
				final:    chunk.Final,
				exitCode: chunk.ExitCode,
				err:      chunk.Err,
			}
		}
	}()

	return tea.Batch(startCmd, listenBashStream(bridgeCh))
}

func listenBashStream(ch <-chan bashStreamEvent) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			// Channel closed without a final event (context cancelled,
			// goroutine error, etc.). Return a synthetic finished message
			// so the TUI clears submitting/working state. A nil return
			// would be silently dropped by bubbletea, causing a permanent hang.
			return bashFinishedMsg{
				command:  "",
				output:   "(stream closed unexpectedly)",
				exitCode: -1,
				err:      fmt.Errorf("bash stream closed without final event"),
			}
		}
		if ev.final {
			return bashFinishedMsg{
				command:  ev.command,
				output:   ev.chunk,
				exitCode: ev.exitCode,
				err:      ev.err,
			}
		}
		return bashChunkMsg{command: ev.command, chunk: ev.chunk}
	}
}

type bashChunkMsg struct {
	command string
	chunk   string
}

type bashFinishedMsg struct {
	command  string
	output   string
	exitCode int
	err      error
}

func (m *model) fetchSnapshotCmd() tea.Cmd {
	return func() tea.Msg {
		snapshot, err := m.backend.Snapshot(m.ctx, m.sessionID)
		return snapshotLoadedMsg{Snapshot: snapshot, Err: err}
	}
}

func (m *model) fetchContextSummaryCmd() tea.Cmd {
	return func() tea.Msg {
		summary, err := m.backend.ContextSummary(m.ctx, m.sessionID)
		return contextSummaryLoadedMsg{Summary: summary, Err: err}
	}
}

func (m *model) fetchWorkbenchCmd() tea.Cmd {
	return func() tea.Msg {
		longTasks, err := m.backend.ListLongTasks(m.ctx, m.sessionID)
		if err != nil {
			return workbenchLoadedMsg{Err: err}
		}
		subagents, err := m.backend.ListSubagents(m.ctx, m.sessionID)
		if err != nil {
			return workbenchLoadedMsg{Err: err}
		}
		return workbenchLoadedMsg{LongTasks: longTasks, Subagents: subagents}
	}
}
