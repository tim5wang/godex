package backend

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/agent"
	"github.com/tim5wang/godex/internal/plugins/taskboard"
)

// TaskboardExecutor runs taskboard cards in isolated sessions by launching
// durable subagents from a live host session's agent (M1: manual trigger;
// host cron scheduling is M3). The subagent gets the full card content and
// handover protocol in its prompt; on settlement the executor closes the
// execution record and submits the card for human acceptance (in_review).
type TaskboardExecutor struct {
	service *Service
	ledger  *taskboard.Ledger
	poll    time.Duration
	timeout time.Duration
}

// NewTaskboardExecutor wires the executor against the backend service and
// the shared taskboard ledger.
func NewTaskboardExecutor(service *Service, ledger *taskboard.Ledger) *TaskboardExecutor {
	return &TaskboardExecutor{service: service, ledger: ledger, poll: 2 * time.Second, timeout: 6 * time.Hour}
}

// hostSession returns the most recently active loaded session to host the
// execution subagent. M1 limitation: taskboard execution needs at least one
// live session (host-cron scheduling without sessions is M3).
func (e *TaskboardExecutor) hostSession() *sessionState {
	s := e.service
	s.mu.Lock()
	defer s.mu.Unlock()
	var best *sessionState
	for _, sess := range s.sessions {
		if sess == nil || sess.agent == nil {
			continue
		}
		if best == nil || sess.lastActive.After(best.lastActive) {
			best = sess
		}
	}
	return best
}

// Execute launches one isolated session for the card. The ledger records the
// execution start; a watcher goroutine finalizes it when the job settles.
func (e *TaskboardExecutor) Execute(ctx context.Context, card taskboard.Card) (string, string, error) {
	host := e.hostSession()
	if host == nil || host.agent == nil {
		return "", "", fmt.Errorf("taskboard: no active session available to host execution; open a session first")
	}
	rootDir := e.projectRoot(card.ProjectID)
	var writeScope []string
	if rootDir != "" {
		writeScope = []string{rootDir}
	}
	job, err := host.agent.StartDurableSubagentWithContext(ctx, executionPrompt(card, rootDir), "general-purpose", writeScope)
	if err != nil {
		return "", "", fmt.Errorf("taskboard: start execution: %w", err)
	}
	executionID := "exec-" + job.IDString()
	sessionID := job.IDString()
	// Record the hosting session so the UI can jump to its live progress.
	// ProjectDir is part of the session identity hash — without it the
	// frontend rebuilds a different session id and falls back to creating
	// a new chat.
	hostRef := &taskboard.HostRef{
		SessionID:  host.id,
		Channel:    host.locator.Channel,
		Key:        host.locator.Key,
		UserID:     host.locator.UserID,
		ProjectDir: host.locator.Metadata["project_dir"],
	}
	if _, err := e.ledger.StartExecution(card.ID, executionID, sessionID, "taskboard", hostRef); err != nil {
		return "", "", err
	}
	go e.watch(card.ID, executionID, host.id, sessionID)
	return executionID, sessionID, nil
}

// projectRoot resolves the project's root dir for the write scope.
func (e *TaskboardExecutor) projectRoot(projectID string) string {
	for _, project := range e.ledger.ListProjects() {
		if project.ID == projectID {
			return project.RootDir
		}
	}
	return ""
}

// sessionAgent returns the agent of a loaded session (nil if absent).
func (e *TaskboardExecutor) sessionAgent(sessionID string) *agent.Agent {
	e.service.mu.Lock()
	defer e.service.mu.Unlock()
	if sess := e.service.sessions[sessionID]; sess != nil {
		return sess.agent
	}
	return nil
}

// watch polls the durable subagent until it settles, then closes the
// execution and moves the card to in_review (human acceptance gate).
func (e *TaskboardExecutor) watch(cardID, executionID, hostSessionID, jobID string) {
	poll, timeout := e.poll, e.timeout
	if poll <= 0 {
		poll = 2 * time.Second
	}
	if timeout <= 0 {
		timeout = 6 * time.Hour
	}
	deadline := time.Now().Add(timeout)
	var view agent.DurableSubagentJobView
	for time.Now().Before(deadline) {
		time.Sleep(poll)
		host := e.sessionAgent(hostSessionID)
		if host == nil {
			continue
		}
		v, err := host.GetDurableSubagent(hostSessionID, jobID)
		if err != nil {
			continue
		}
		view = v
		switch view.Status {
		case "completed", "failed", "cancelled":
			e.settle(cardID, executionID, view)
			return
		}
	}
	// Timed out: cancel the execution record so the card can be re-run.
	_, _ = e.ledger.FinishExecution(cardID, executionID, taskboard.ExecutionCancelled, "execution watch timed out")
}

// settle closes the execution and submits the card for acceptance.
func (e *TaskboardExecutor) settle(cardID, executionID string, view agent.DurableSubagentJobView) {
	status := taskboard.ExecutionCompleted
	switch view.Status {
	case "failed":
		status = taskboard.ExecutionFailed
	case "cancelled":
		status = taskboard.ExecutionCancelled
	}
	summary := strings.TrimSpace(view.Result)
	if summary == "" && strings.TrimSpace(view.Error) != "" {
		summary = "error: " + strings.TrimSpace(view.Error)
	}
	card, err := e.ledger.FinishExecution(cardID, executionID, status, summary)
	if err != nil {
		return
	}
	// Submit for human acceptance: in_progress -> in_review (holder is
	// already cleared by FinishExecution, so the move passes the hold gate).
	if card.Status == taskboard.StatusInProgress {
		_, _ = e.ledger.MoveCard(cardID, card.Version, taskboard.StatusInReview, "taskboard")
	}
}

// executionPrompt builds the isolated-session opening prompt: card frame +
// handover protocol (report back a structured summary for the ledger).
func executionPrompt(card taskboard.Card, rootDir string) string {
	var b strings.Builder
	b.WriteString("你在独立会话中执行任务看板的一张任务卡。完成后输出结构化总结（做了什么 / 改动文件 / 自验结果 / 剩余风险），该总结会写入任务执行记录。\n\n")
	b.WriteString("# 任务 " + card.ID + "\n")
	b.WriteString("标题: " + card.Title + "\n")
	if card.Description != "" {
		b.WriteString("描述: " + card.Description + "\n")
	}
	if card.Prompt != "" {
		b.WriteString("\n执行指令:\n" + card.Prompt + "\n")
	}
	if len(card.Checklist) > 0 {
		b.WriteString("\n验收清单:\n")
		for _, item := range card.Checklist {
			b.WriteString("- [ ] " + item.Text + "\n")
		}
	}
	if len(card.Comments) > 0 {
		b.WriteString("\n评论（最新需求，先读后动）:\n")
		for _, comment := range card.Comments {
			b.WriteString("- [" + comment.Author + "] " + comment.Text + "\n")
		}
	}
	if rootDir != "" {
		b.WriteString("\n工作目录边界: " + rootDir + "\n")
	}
	return b.String()
}
