package tui

import (
	"fmt"
	"strings"

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
