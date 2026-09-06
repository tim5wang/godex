package backend

import (
	"context"
	"fmt"
	"strings"

	"github.com/tim5wang/godex/internal/domain/message"
	"github.com/tim5wang/godex/internal/plugins/taskboard"
)

// TaskboardExecutor runs one card in a dedicated execution session pinned to
// the card's agent template (M3, design decision Q1=B). The execution session
// is keyed by card id (channel "taskboard", key "card-<id>") so every
// execution of the same card reuses one conversation: its messages, tool
// calls, reasoning and file edits ARE the chat history — fully observable,
// no durable-subagent black box, no separate LLM connection (so no
// gateway/auth failure surface).
//
// Design: Execute() opens (or reuses) the card's execution session with the
// card's template in the locator metadata (load.go applies it via
// ApplyTemplate), guarantees the taskboard tool stays callable on top of the
// template's exact tool set, records the execution (running, host = the
// execution session itself), and submits the card workload as a message into
// that session. The agent is expected to claim the card (taskboard
// action=move → in_progress), do the work, and move it to in_review for
// human acceptance — all visible in the execution session feed.
type TaskboardExecutor struct {
	service *Service
	ledger  *taskboard.Ledger
}

// taskboardSessionChannel is the session channel used for card execution
// sessions so they group apart from web/local chats in the sessions rail.
const taskboardSessionChannel = "taskboard"

// NewTaskboardExecutor wires the executor against the backend service and
// the shared taskboard ledger.
func NewTaskboardExecutor(service *Service, ledger *taskboard.Ledger) *TaskboardExecutor {
	return &TaskboardExecutor{service: service, ledger: ledger}
}

// Execute opens (or reuses) the card's template-pinned execution session and
// submits the card workload into it. Returns the execution id and the
// execution session id (the jump-to-progress target lives in that
// conversation). Unlike the pre-M3 host dispatch, no other live session is
// required.
func (e *TaskboardExecutor) Execute(ctx context.Context, card taskboard.Card) (string, string, error) {
	rootDir := e.cardWorkDir(card)
	templateID := strings.TrimSpace(card.TemplateID)
	executionID := "exec-" + card.ID + "-" + e.service.now().Format("20060102150405.000")
	if templateID != "" {
		if _, _, err := e.service.ValidateAgentTemplate(templateID); err != nil {
			return "", "", fmt.Errorf("taskboard: agent template %q is unavailable: %w", templateID, err)
		}
	}

	locator := SessionLocator{
		Channel:  taskboardSessionChannel,
		Key:      "card-" + card.ID,
		Metadata: map[string]string{},
	}
	if rootDir != "" {
		locator.Metadata[sessionProjectDirMetadataKey] = rootDir
	}
	if templateID != "" {
		locator.Metadata["template"] = templateID
	}

	opened, err := e.service.OpenSession(ctx, locator)
	if err != nil {
		return "", "", fmt.Errorf("taskboard: 打开执行会话失败: %w", err)
	}
	sessionID := opened.SessionID
	// A card keeps one stable execution session. If PJM explicitly changes the
	// template on a later dispatch, apply the new template to that reused
	// session as well as persisting it in the locator.
	if templateID != "" && strings.TrimSpace(opened.Locator.Metadata["template"]) != templateID {
		if err := e.service.ApplyTemplateToSession(sessionID, templateID); err != nil {
			return "", "", fmt.Errorf("taskboard: apply agent template %q: %w", templateID, err)
		}
	}

	// The template pins an exact tool set; guarantee the taskboard tool stays
	// callable on top of it so the agent can claim/execute/accept the card.
	if sess, err := e.service.requireSession(sessionID); err == nil && sess.agent != nil {
		sess.agent.ActivateBundles("task_board")
	}

	hostRef := &taskboard.HostRef{
		SessionID:  sessionID,
		Channel:    taskboardSessionChannel,
		Key:        "card-" + card.ID,
		ProjectDir: rootDir,
	}
	if _, err := e.ledger.StartExecution(card.ID, executionID, sessionID, "taskboard", hostRef); err != nil {
		return "", "", err
	}
	envelope := message.NewRuntimeEnvelope(message.SourceBackground, sessionID, "taskboard", executionPrompt(card, rootDir), e.service.now(), map[string]string{
		taskboardCardIDMetadataKey: card.ID,
		taskboardTitleMetadataKey:  card.Title,
	})
	if _, err := e.service.SubmitAsync(ctx, sessionID, envelope, SubmitOptions{QueueMode: QueueModeFollowUp}); err != nil {
		// The execution record is started by the timeout path; surface the
		// delivery failure so the card does not silently hang.
		_, _ = e.ledger.FinishExecution(card.ID, executionID, taskboard.ExecutionCancelled, "投递到会话失败")
		return "", "", fmt.Errorf("taskboard: 投递执行任务到会话 %s 失败: %w", sessionID, err)
	}
	return executionID, sessionID, nil
}

// projectRoot resolves the project's default root dir (its first work dir) for
// backwards-compatible callers that only know the project id.
func (e *TaskboardExecutor) projectRoot(projectID string) string {
	for _, project := range e.ledger.ListProjects() {
		if project.ID == projectID {
			if len(project.WorkDirs) > 0 {
				return project.WorkDirs[0]
			}
			return ""
		}
	}
	return ""
}

// cardWorkDir resolves the work directory an execution session should be pinned
// to: the card's explicit WorkDir when set (validated to belong to the
// project), else the project's first work dir.
func (e *TaskboardExecutor) cardWorkDir(card taskboard.Card) string {
	for _, project := range e.ledger.ListProjects() {
		if project.ID != card.ProjectID {
			continue
		}
		if wd := strings.TrimSpace(card.WorkDir); wd != "" {
			for _, dir := range project.WorkDirs {
				if dir == wd {
					return wd
				}
			}
		}
		if len(project.WorkDirs) > 0 {
			return project.WorkDirs[0]
		}
		return ""
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
	b.WriteString("- 状态确认：派工时卡片已自动置为 in_progress（StartExecution 置位），无需再 move；直接执行即可\n")
	b.WriteString("- 执行：按卡片 Prompt 干活，用 taskboard action=checklist 勾选验收项（附证据）\n")
	b.WriteString("- 动态观测（闸门 3）：写文件后用 taskboard action=report_touched 上报实际改到的包路径（如 [\"internal/platform/tooling\"]），触发跨卡路径重叠告警\n")
	b.WriteString("- 完成：taskboard action=move to=in_review（提交人工验收，自动做 merge 预检）\n")
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
	if card.Research != nil && !card.Research.IsEmpty() {
		b.WriteString("\n### 已由 PJM 验证（不必重复排查）\n")
		if len(card.Research.Facts) > 0 {
			b.WriteString("已验证事实:\n")
			for _, f := range card.Research.Facts {
				b.WriteString("- " + f + "\n")
			}
		}
		if len(card.Research.Locations) > 0 {
			b.WriteString("\n关键落点（文件:行号）:\n")
			for _, loc := range card.Research.Locations {
				b.WriteString("- " + loc + "\n")
			}
		}
		if len(card.Research.ExcludedPaths) > 0 {
			b.WriteString("\n排除路径（不必排查）:\n")
			for _, p := range card.Research.ExcludedPaths {
				b.WriteString("- " + p + "\n")
			}
		}
		b.WriteString("\n### 执行时需自行验证的开放点\n")
		if len(card.Research.OpenQuestions) > 0 {
			for _, q := range card.Research.OpenQuestions {
				b.WriteString("- " + q + "\n")
			}
		} else {
			b.WriteString("（无待验证项，按已验证事实推进）\n")
		}
	}
	return b.String()
}
