package backend

import (
	"context"
	"fmt"
	"strings"

	"github.com/tim5wang/godex/internal/domain/message"
	"github.com/tim5wang/godex/internal/plugins/taskboard"
)

// TaskboardExecutor handoffs a card to a host session's main agent so it
// claims and executes the work in its own conversation flow. The run's tool
// calls, reasoning, and file edits ARE the chat history — fully observable,
// no durable-subagent black box, no separate LLM connection (so no
// gateway/auth failure surface).
//
// Design: Execute() records the execution (running, host = target session)
// and submits the card workload as a message into that session. The main
// agent, which now has the taskboard tool injected per-session, is expected
// to claim the card (taskboard action=move → in_progress), do the work, and
// move it to in_review for human acceptance — all visible in the feed.
type TaskboardExecutor struct {
	service *Service
	ledger  *taskboard.Ledger
}

// NewTaskboardExecutor wires the executor against the backend service and
// the shared taskboard ledger.
func NewTaskboardExecutor(service *Service, ledger *taskboard.Ledger) *TaskboardExecutor {
	return &TaskboardExecutor{service: service, ledger: ledger}
}

// hostSession returns the most recently active loaded session to hand the
// card to. Taskboard execution requires at least one live session (host-cron
// scheduling without sessions is M3).
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

// Execute hands the card to the host session's main agent and records the
// execution. Returns the execution id and the host session id (the jump-to-
// progress target lives in that conversation).
func (e *TaskboardExecutor) Execute(ctx context.Context, card taskboard.Card) (string, string, error) {
	host := e.hostSession()
	if host == nil || host.agent == nil {
		return "", "", fmt.Errorf("taskboard: 没有可执行任务的活动会话，请先打开一个会话")
	}
	rootDir := e.projectRoot(card.ProjectID)
	executionID := "exec-" + card.ID + "-" + e.service.now().Format("20060102150405")
	hostRef := &taskboard.HostRef{
		SessionID:  host.id,
		Channel:    host.locator.Channel,
		Key:        host.locator.Key,
		UserID:     host.locator.UserID,
		ProjectDir: host.locator.Metadata["project_dir"],
	}
	if _, err := e.ledger.StartExecution(card.ID, executionID, host.id, "taskboard", hostRef); err != nil {
		return "", "", err
	}
	envelope := message.NewRuntimeEnvelope(message.SourceBackground, host.id, "taskboard", executionPrompt(card, rootDir), e.service.now(), map[string]string{
		"taskboard_card_id": card.ID,
		"taskboard_title":   card.Title,
	})
	if _, err := e.service.SubmitAsync(ctx, host.id, envelope, SubmitOptions{QueueMode: QueueModeFollowUp}); err != nil {
		// The execution record is started by the timeout path; surface the
		// delivery failure so the card does not silently hang.
		_, _ = e.ledger.FinishExecution(card.ID, executionID, taskboard.ExecutionCancelled, "投递到会话失败")
		return "", "", fmt.Errorf("taskboard: 投递执行任务到会话 %s 失败: %w", host.id, err)
	}
	return executionID, host.id, nil
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

// executionPrompt builds the message handed into the host session: the card
// frame plus a self-contained claim-and-execute protocol. The agent is told
// to manage the card via the taskboard tool so status transitions are visible
// in the feed.
func executionPrompt(card taskboard.Card, rootDir string) string {
	var b strings.Builder
	b.WriteString("任务看板有一条任务卡需要你在当前对话中认领并执行。用 taskboard 工具：\n")
	b.WriteString("- 先 taskboard action=get 读卡片（含评论与验收清单）\n")
	b.WriteString("- 认领：taskboard action=move to=in_progress\n")
	b.WriteString("- 执行：按卡片 Prompt 干活，用 taskboard action=checklist 勾选验收项（附证据）\n")
	b.WriteString("- 完成：taskboard action=move to=in_review（提交人工验收）\n")
	b.WriteString("完成后在对话里输出结构化总结（做了什么/改动文件/自验结果/剩余风险）。\n\n")
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
