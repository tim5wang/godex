package tui

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/tim5wang/godex/internal/domain/message"
	"github.com/tim5wang/godex/internal/services/commands"
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

	// cancel any previous still-running bash
	if m.bashCancel != nil {
		m.bashCancel()
		m.bashCancel = nil
	}

	ctx, cancel := context.WithTimeout(m.ctx, bashTimeout)
	m.bashCancel = cancel
	m.status = "Running /bash (Ctrl+C to cancel)..."
	startCmd := m.startWorking(m.status)

	// seed the feed item so we can see it's running
	m.upsertOverlay(feedItem{
		ID:          "bash:" + shellCmd,
		Kind:        feedCommand,
		Title:       "/bash (running)",
		Body:        "Running...",
		Summary:     "Running...",
		RuntimeOnly: true,
		CreatedAt:   m.now(),
	})

	execCmd := func() tea.Msg {
		defer cancel()

		cmd := exec.CommandContext(ctx, "sh", "-c", shellCmd)
		cmd.Dir = m.cfg.WorkspaceDir
		output, err := cmd.CombinedOutput()

		code := exitCode(err)
		outputStr := string(output)

		if ctx.Err() != nil {
			if outputStr == "" {
				outputStr = "(cancelled)"
			}
			code = -1
		}

		return bashFinishedMsg{
			command:  shellCmd,
			output:   outputStr,
			exitCode: code,
			err:      err,
		}
	}
	return tea.Batch(startCmd, execCmd)
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.ExitCode()
	}
	return -1
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
