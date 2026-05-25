package tui

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
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

	// channel for streaming events from reader goroutine to main loop
	ch := make(chan bashStreamEvent, 64)
	m.bashCh = ch

	// launch reader goroutine
	go func() {
		defer cancel()
		defer close(ch)

		cmd := exec.CommandContext(ctx, "sh", "-c", shellCmd)
		cmd.Dir = m.cfg.WorkspaceDir

		stdout, err := cmd.StdoutPipe()
		if err != nil {
			ch <- bashStreamEvent{command: shellCmd, final: true, exitCode: -1, err: err}
			return
		}
		stderr, err := cmd.StderrPipe()
		if err != nil {
			ch <- bashStreamEvent{command: shellCmd, final: true, exitCode: -1, err: err}
			return
		}

		if err := cmd.Start(); err != nil {
			ch <- bashStreamEvent{command: shellCmd, final: true, exitCode: -1, err: err}
			return
		}

		var (
			accumulated strings.Builder
			mu          sync.Mutex
		)

		// read both pipes concurrently into accumulated
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); copyTo(&accumulated, &mu, stdout) }()
		go func() { defer wg.Done(); copyTo(&accumulated, &mu, stderr) }()

		// ticker to flush chunks periodically
		ticker := time.NewTicker(bashChunkInterval)
		defer ticker.Stop()

		readDone := make(chan struct{})
		go func() {
			wg.Wait()
			close(readDone)
		}()

	loop:
		for {
			select {
			case <-ticker.C:
				mu.Lock()
				if accumulated.Len() > 0 {
					ch <- bashStreamEvent{command: shellCmd, chunk: accumulated.String()}
				}
				mu.Unlock()
			case <-readDone:
				break loop
			case <-ctx.Done():
				// context cancelled (timeout or user), kill the process
				if cmd.Process != nil {
					cmd.Process.Kill()
				}
				break loop
			}
		}

		// drain remaining chunks after loop exit
		mu.Lock()
		finalOutput := accumulated.String()
		mu.Unlock()

		waitErr := cmd.Wait()
		code := exitCode(waitErr)
		if ctx.Err() != nil {
			if finalOutput == "" {
				finalOutput = "(cancelled)"
			}
			code = -1
		}

		ch <- bashStreamEvent{
			command:  shellCmd,
			chunk:    finalOutput,
			final:    true,
			exitCode: code,
			err:      waitErr,
		}
	}()

	return tea.Batch(startCmd, listenBashStream(ch))
}

func copyTo(dst *strings.Builder, mu *sync.Mutex, src io.Reader) {
	buf := make([]byte, 4096)
	for {
		n, readErr := src.Read(buf)
		if n > 0 {
			mu.Lock()
			dst.Write(buf[:n])
			mu.Unlock()
		}
		if readErr != nil {
			break
		}
	}
}

func listenBashStream(ch <-chan bashStreamEvent) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return nil
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

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.ExitCode()
	}
	return -1
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
