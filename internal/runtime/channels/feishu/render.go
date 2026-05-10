package feishu

import (
	"fmt"
	"strings"

	larkcard "github.com/larksuite/oapi-sdk-go/v3/card"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/tim5wang/godex/internal/runtime/channels"
)

const (
	maxPostChunkRunes = 2600
)

func renderReplyPlanPosts(plan channels.ReplyPlan) ([]string, error) {
	body := renderPostBody(plan)
	if strings.TrimSpace(body) == "" {
		return nil, nil
	}

	chunks := splitReplyChunks(body, maxPostChunkRunes)
	posts := make([]string, 0, len(chunks))
	for idx, chunk := range chunks {
		post, err := buildPostMessage(planTitle(plan, idx > 0), chunk)
		if err != nil {
			return nil, err
		}
		posts = append(posts, post)
	}
	return posts, nil
}

func renderReplyPlanCard(plan channels.ReplyPlan) (string, error) {
	template := larkcard.TemplateBlue
	switch strings.TrimSpace(plan.Status) {
	case "completed":
		template = larkcard.TemplateGreen
	case "error", "failed":
		template = larkcard.TemplateRed
	}

	body := strings.TrimSpace(renderCardBody(plan))
	if body == "" {
		body = feishuText(textCompletedFallback)
	}

	elements := make([]larkcard.MessageCardElement, 0, 3)
	elements = append(elements,
		larkcard.NewMessageCardDiv().
			Text(larkcard.NewMessageCardLarkMd().Content(body).Build()).
			Build(),
	)

	noteParts := make([]larkcard.MessageCardNoteElement, 0, len(plan.Artifacts))
	for _, artifact := range plan.Artifacts {
		if label := artifactLabel(artifact); label != "" {
			noteParts = append(noteParts, larkcard.NewMessageCardPlainText().Content(feishuText(textArtifactLabel)+": "+label).Build())
		}
	}
	if len(noteParts) > 0 {
		elements = append(elements,
			larkcard.NewMessageCardNote().Elements(noteParts).Build(),
		)
	}

	card := larkcard.NewMessageCard().
		Config(
			larkcard.NewMessageCardConfig().
				EnableForward(true).
				UpdateMulti(true).
				WideScreenMode(true).
				Build(),
		).
		Header(
			larkcard.NewMessageCardHeader().
				Template(template).
				Title(larkcard.NewMessageCardPlainText().Content(planTitle(plan, false)).Build()).
				Build(),
		).
		Elements(elements).
		Build()
	content, err := card.JSON()
	if err != nil {
		return "", err
	}
	return content, nil
}

func renderProcessingCard() (string, error) {
	plan := channels.ReplyPlan{
		Text:   feishuText(textAckProcessing),
		Status: "running",
	}
	return renderReplyPlanCard(plan)
}

func buildPostMessage(title, chunk string) (string, error) {
	content := larkim.NewMessagePostContent().ContentTitle(title)
	for _, row := range postRows(chunk) {
		content.AppendContent(row)
	}
	post := (&larkim.MessagePost{}).ZhCn(content.Build())
	return post.String()
}

func postRows(chunk string) [][]larkim.MessagePostElement {
	lines := strings.Split(strings.ReplaceAll(strings.TrimSpace(chunk), "\r\n", "\n"), "\n")
	rows := make([][]larkim.MessagePostElement, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		rows = append(rows, []larkim.MessagePostElement{
			&larkim.MessagePostText{Text: line, UnEscape: true},
		})
	}
	if len(rows) == 0 {
		rows = append(rows, []larkim.MessagePostElement{
			&larkim.MessagePostText{Text: strings.TrimSpace(chunk), UnEscape: true},
		})
	}
	return rows
}

func renderPostBody(plan channels.ReplyPlan) string {
	sections := make([]string, 0, 4)
	if text := strings.TrimSpace(plan.Text); text != "" {
		sections = append(sections, text)
	}
	if plan.Todos != nil {
		if todoText := strings.TrimSpace(channels.RenderReplyTodos(*plan.Todos)); todoText != "" {
			sections = append(sections, todoText)
		}
	}
	if len(plan.Tools) > 0 {
		lines := []string{feishuText(textToolsSummary) + "："}
		for _, tool := range plan.Tools {
			name := strings.TrimSpace(tool.Name)
			if name == "" {
				name = feishuText(textToolFallbackName)
			}
			status := localizeStatus(tool.Status)
			line := fmt.Sprintf("- %s (%s)", name, status)
			if output := strings.TrimSpace(tool.Output); output != "" {
				line += ": " + output
			} else if tool.Error != "" {
				line += ": " + strings.TrimSpace(tool.Error)
			}
			lines = append(lines, line)
		}
		sections = append(sections, strings.Join(lines, "\n"))
	}
	if len(plan.Approvals) > 0 {
		sections = append(sections, strings.Join(renderApprovalBodyLines(plan.Approvals, false), "\n"))
	}
	if len(plan.Notices) > 0 {
		lines := []string{feishuText(textNotes) + "："}
		for _, notice := range plan.Notices {
			if trimmed := strings.TrimSpace(notice); trimmed != "" {
				lines = append(lines, "- "+trimmed)
			}
		}
		if len(lines) > 1 {
			sections = append(sections, strings.Join(lines, "\n"))
		}
	}
	return strings.Join(sections, "\n\n")
}

func renderCardBody(plan channels.ReplyPlan) string {
	sections := make([]string, 0, 5)
	if text := strings.TrimSpace(plan.Text); text != "" {
		sections = append(sections, text)
	}
	meta := make([]string, 0, 4)
	if status := strings.TrimSpace(plan.Status); status != "" {
		meta = append(meta, fmt.Sprintf("**%s**：%s", feishuText(textStatusLabel), localizeStatus(status)))
	}
	if cmd := strings.TrimSpace(plan.Command); cmd != "" {
		meta = append(meta, fmt.Sprintf("**%s**：`/%s`", feishuText(textCommandLabel), cmd))
	}
	if len(meta) > 0 {
		sections = append(sections, strings.Join(meta, "\n"))
	}
	if plan.Todos != nil {
		if todoText := strings.TrimSpace(channels.RenderReplyTodos(*plan.Todos)); todoText != "" {
			sections = append(sections, "**任务进度**\n"+todoText)
		}
	}
	if len(plan.Tools) > 0 {
		lines := []string{fmt.Sprintf("**%s**", feishuText(textToolsSummary))}
		for _, tool := range plan.Tools {
			name := strings.TrimSpace(tool.Name)
			if name == "" {
				name = feishuText(textToolFallbackName)
			}
			status := localizeStatus(tool.Status)
			line := fmt.Sprintf("- `%s` (%s)", name, status)
			if tool.Output != "" {
				line += " " + truncateRunesLocal(tool.Output, 180)
			} else if tool.Error != "" {
				line += " " + truncateRunesLocal(tool.Error, 180)
			}
			lines = append(lines, line)
		}
		sections = append(sections, strings.Join(lines, "\n"))
	}
	if len(plan.Approvals) > 0 {
		sections = append(sections, strings.Join(renderApprovalBodyLines(plan.Approvals, true), "\n"))
	}
	if len(plan.Notices) > 0 {
		lines := []string{fmt.Sprintf("**%s**", feishuText(textNotes))}
		for _, notice := range plan.Notices {
			if trimmed := strings.TrimSpace(notice); trimmed != "" {
				lines = append(lines, "- "+trimmed)
			}
		}
		if len(lines) > 1 {
			sections = append(sections, strings.Join(lines, "\n"))
		}
	}
	return strings.Join(sections, "\n\n")
}

func renderApprovalBodyLines(approvals []channels.ReplyApproval, markdown bool) []string {
	title := "Approval request:"
	if markdown {
		title = "**Approval request**"
	}
	lines := []string{title}
	for _, approval := range approvals {
		tool := strings.TrimSpace(approval.ToolName)
		if tool == "" {
			tool = feishuText(textToolFallbackName)
		}
		action := strings.TrimSpace(approval.Action)
		header := "- " + tool
		if markdown {
			header = "- `" + tool + "`"
		}
		if action != "" {
			header += " / " + action
		}
		if id := strings.TrimSpace(approval.RequestID); id != "" {
			header += " (" + id + ")"
		}
		lines = append(lines, header)
		if command := strings.TrimSpace(approval.Command); command != "" {
			lines = append(lines, "  command: "+command)
		}
		if len(approval.Paths) > 0 {
			lines = append(lines, "  paths: "+strings.Join(approval.Paths, ", "))
		}
		for _, item := range approval.InputPreview {
			if strings.TrimSpace(item.Key) == "" {
				continue
			}
			lines = append(lines, "  "+item.Key+": "+item.Value)
		}
		if reason := strings.TrimSpace(approval.Reason); reason != "" {
			lines = append(lines, "  reason: "+reason)
		}
	}
	return lines
}

func truncateRunesLocal(input string, limit int) string {
	if limit <= 0 {
		return input
	}
	runes := []rune(input)
	if len(runes) <= limit {
		return input
	}
	return string(runes[:limit]) + "..."
}

func planTitle(plan channels.ReplyPlan, continued bool) string {
	base := feishuText(textReplyTitleBase)
	if cmd := strings.TrimSpace(plan.Command); cmd != "" {
		base = feishuText(textReplyTitleCommand, cmd)
	}
	switch strings.TrimSpace(plan.Status) {
	case "completed":
		base += feishuText(textReplyTitleCompletedSuffix)
	case "error", "failed":
		base += feishuText(textReplyTitleErrorSuffix)
	case "running":
		base += feishuText(textReplyTitleRunningSuffix)
	}
	if continued {
		base += feishuText(textReplyTitleContinuedSuffix)
	}
	return base
}
