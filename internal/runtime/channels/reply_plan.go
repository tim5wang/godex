package channels

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/tim5wang/godex/internal/domain/events"
	"github.com/tim5wang/godex/internal/platform/textutil"
	"github.com/tim5wang/godex/internal/tools"
)

// PlanSender optionally delivers a structured reply plan instead of a plain string.
type PlanSender interface {
	SendReplyPlan(context.Context, ReplyPlan) error
}

// ReplyPlan is the aggregated, platform-neutral result of one inbound turn.
type ReplyPlan struct {
	Text      string          `json:"text,omitempty"`
	Notices   []string        `json:"notices,omitempty"`
	Tools     []ReplyTool     `json:"tools,omitempty"`
	Todos     *ReplyTodoList  `json:"todos,omitempty"`
	Artifacts []ReplyArtifact `json:"artifacts,omitempty"`
	Approvals []ReplyApproval `json:"approvals,omitempty"`
	Command   string          `json:"command,omitempty"`
	Status    string          `json:"status,omitempty"`
}

// ReplyTool is a compact summary of one tool lifecycle.
type ReplyTool struct {
	ID     string `json:"id,omitempty"`
	Name   string `json:"name,omitempty"`
	Status string `json:"status,omitempty"`
	Output string `json:"output,omitempty"`
	Error  string `json:"error,omitempty"`
}

// ReplyTodoList is a compact, platform-neutral todo-list summary.
type ReplyTodoList struct {
	Items      []ReplyTodoItem `json:"items,omitempty"`
	Total      int             `json:"total"`
	Completed  int             `json:"completed"`
	InProgress int             `json:"in_progress"`
	Pending    int             `json:"pending"`
}

// ReplyTodoItem is one rendered todo item for channel replies.
type ReplyTodoItem struct {
	ID         int    `json:"id,omitempty"`
	Content    string `json:"content"`
	Status     string `json:"status"`
	ActiveForm string `json:"active_form,omitempty"`
}

// ReplyArtifact points to one generated artifact path.
type ReplyArtifact struct {
	Path string `json:"path,omitempty"`
	Name string `json:"name,omitempty"`
}

// ReplyApproval is a safe, platform-neutral summary of one pending approval.
type ReplyApproval struct {
	RequestID    string              `json:"request_id,omitempty"`
	ToolName     string              `json:"tool_name,omitempty"`
	Action       string              `json:"action,omitempty"`
	Command      string              `json:"command,omitempty"`
	Paths        []string            `json:"paths,omitempty"`
	InputPreview []ReplyInputPreview `json:"input_preview,omitempty"`
	Reason       string              `json:"reason,omitempty"`
	Source       string              `json:"source,omitempty"`
	Sender       string              `json:"sender,omitempty"`
}

// ReplyInputPreview is one sanitized key/value from a pending tool input.
type ReplyInputPreview struct {
	Key   string `json:"key,omitempty"`
	Value string `json:"value,omitempty"`
}

// RenderText converts the plan into a plain text fallback for simple channels.
func (p ReplyPlan) RenderText() string {
	sections := make([]string, 0, 4)

	if text := strings.TrimSpace(p.Text); text != "" {
		sections = append(sections, text)
	}

	if p.Todos != nil {
		if todoText := strings.TrimSpace(RenderReplyTodos(*p.Todos)); todoText != "" {
			sections = append(sections, todoText)
		}
	}

	if len(p.Artifacts) > 0 {
		lines := []string{"Artifacts:"}
		for _, artifact := range p.Artifacts {
			label := strings.TrimSpace(artifact.Name)
			if label == "" {
				label = filepath.Base(strings.TrimSpace(artifact.Path))
			}
			if label == "" {
				label = strings.TrimSpace(artifact.Path)
			}
			if path := strings.TrimSpace(artifact.Path); path != "" && path != label {
				lines = append(lines, "- "+label+" ("+path+")")
				continue
			}
			lines = append(lines, "- "+label)
		}
		sections = append(sections, strings.Join(lines, "\n"))
	}

	if len(p.Approvals) > 0 {
		sections = append(sections, strings.Join(renderApprovalLines(p.Approvals, false), "\n"))
	}

	if len(p.Notices) > 0 {
		lines := []string{"Notes:"}
		for _, notice := range p.Notices {
			if trimmed := strings.TrimSpace(notice); trimmed != "" {
				lines = append(lines, "- "+trimmed)
			}
		}
		if len(lines) > 1 {
			sections = append(sections, strings.Join(lines, "\n"))
		}
	}

	if len(sections) == 0 && len(p.Tools) > 0 {
		lines := []string{"Tool summary:"}
		for _, tool := range p.Tools {
			name := strings.TrimSpace(tool.Name)
			if name == "" {
				name = "tool"
			}
			status := strings.TrimSpace(tool.Status)
			if status == "" {
				status = "completed"
			}
			line := "- " + name + " (" + status + ")"
			if output := strings.TrimSpace(tool.Output); output != "" {
				line += ": " + output
			} else if tool.Error != "" {
				line += ": " + strings.TrimSpace(tool.Error)
			}
			lines = append(lines, line)
		}
		sections = append(sections, strings.Join(lines, "\n"))
	}

	if len(sections) == 0 {
		return "Completed."
	}
	return strings.Join(sections, "\n\n")
}

func RenderReplyTodos(todos ReplyTodoList) string {
	payload := events.TodoListPayload{
		Items:      make([]events.TodoItemPayload, 0, len(todos.Items)),
		Total:      todos.Total,
		Completed:  todos.Completed,
		InProgress: todos.InProgress,
		Pending:    todos.Pending,
	}
	for _, item := range todos.Items {
		payload.Items = append(payload.Items, events.TodoItemPayload{
			ID:         item.ID,
			Content:    item.Content,
			Status:     item.Status,
			ActiveForm: item.ActiveForm,
		})
	}
	return payload.RenderPlain()
}

func replyTodosFromPayload(payload events.TodoListPayload) *ReplyTodoList {
	todos := &ReplyTodoList{
		Items:      make([]ReplyTodoItem, 0, len(payload.Items)),
		Total:      payload.Total,
		Completed:  payload.Completed,
		InProgress: payload.InProgress,
		Pending:    payload.Pending,
	}
	for _, item := range payload.Items {
		todos.Items = append(todos.Items, ReplyTodoItem{
			ID:         item.ID,
			Content:    item.Content,
			Status:     item.Status,
			ActiveForm: item.ActiveForm,
		})
	}
	return todos
}

func cloneReplyTodos(input *ReplyTodoList) *ReplyTodoList {
	if input == nil {
		return nil
	}
	cloned := *input
	cloned.Items = append([]ReplyTodoItem{}, input.Items...)
	return &cloned
}

func ReplyApprovalFromPending(pending tools.PendingPermission) ReplyApproval {
	req := pending.Request
	return ReplyApproval{
		RequestID:    strings.TrimSpace(pending.ID),
		ToolName:     strings.TrimSpace(req.ToolName),
		Action:       strings.TrimSpace(req.Action),
		Command:      textutil.TruncateRunes(strings.TrimSpace(req.Command), 320),
		Paths:        append([]string{}, req.Paths...),
		InputPreview: previewPermissionInput(req),
		Reason:       textutil.TruncateRunes(strings.TrimSpace(pending.Reason), 320),
		Source:       strings.TrimSpace(req.Source),
		Sender:       strings.TrimSpace(req.Sender),
	}
}

func renderApprovalLines(approvals []ReplyApproval, markdown bool) []string {
	lines := []string{"Approval request:"}
	for _, approval := range approvals {
		id := strings.TrimSpace(approval.RequestID)
		tool := strings.TrimSpace(approval.ToolName)
		if tool == "" {
			tool = "tool"
		}
		action := strings.TrimSpace(approval.Action)
		header := "- " + tool
		if action != "" {
			header += " / " + action
		}
		if id != "" {
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
			if item.Key == "" {
				continue
			}
			lines = append(lines, "  "+item.Key+": "+item.Value)
		}
		if reason := strings.TrimSpace(approval.Reason); reason != "" {
			lines = append(lines, "  reason: "+reason)
		}
		source := strings.TrimSpace(approval.Source)
		sender := strings.TrimSpace(approval.Sender)
		if source != "" || sender != "" {
			lines = append(lines, "  source: "+strings.TrimSpace(source+" "+sender))
		}
	}
	_ = markdown
	return lines
}

func previewPermissionInput(req tools.PermissionRequest) []ReplyInputPreview {
	input := req.Input
	if len(input) == 0 && strings.TrimSpace(req.Command) == "" && len(req.Paths) == 0 {
		return nil
	}

	items := make([]ReplyInputPreview, 0, 6)
	seen := make(map[string]struct{}, 8)
	add := func(key string, value interface{}) {
		key = strings.TrimSpace(key)
		if key == "" {
			return
		}
		lower := strings.ToLower(key)
		if _, ok := seen[lower]; ok {
			return
		}
		seen[lower] = struct{}{}
		items = append(items, ReplyInputPreview{Key: key, Value: previewValue(key, value)})
	}
	if req.Command != "" {
		add("command", req.Command)
	}
	if len(req.Paths) == 1 {
		add("path", req.Paths[0])
	} else if len(req.Paths) > 1 {
		add("paths", req.Paths)
	}
	for _, key := range []string{"command", "path", "paths", "url", "pattern", "content"} {
		if value, ok := input[key]; ok {
			add(key, value)
		}
	}
	if len(items) >= 6 {
		return items[:6]
	}
	keys := make([]string, 0, len(input))
	for key := range input {
		lower := strings.ToLower(strings.TrimSpace(key))
		if _, ok := seen[lower]; ok {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		add(key, input[key])
		if len(items) >= 6 {
			break
		}
	}
	return items
}

func previewValue(key string, value interface{}) string {
	if sensitivePreviewKey(key) {
		return "[redacted]"
	}
	switch typed := value.(type) {
	case string:
		return textutil.TruncateRunes(strings.TrimSpace(typed), 320)
	case []string:
		return textutil.TruncateRunes(strings.Join(typed, ", "), 320)
	default:
		data, err := json.Marshal(typed)
		if err != nil {
			return textutil.TruncateRunes(strings.TrimSpace(fmt.Sprint(typed)), 320)
		}
		return textutil.TruncateRunes(string(data), 320)
	}
}

func sensitivePreviewKey(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	for _, marker := range []string{"secret", "token", "key", "password"} {
		if strings.Contains(key, marker) {
			return true
		}
	}
	return false
}

type replyCollector struct {
	mu        sync.Mutex
	builder   strings.Builder
	notices   []string
	toolOrder []string
	tools     map[string]*ReplyTool
	todos     *ReplyTodoList
	artifacts []ReplyArtifact
}

func (c *replyCollector) Emit(event events.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()

	switch event.Type {
	case events.EventAssistantTextDelta:
		payload, ok := event.Payload.(events.TextPayload)
		if ok {
			c.builder.WriteString(payload.Text)
		}
	case events.EventWarningRaised, events.EventErrorRaised:
		payload, ok := event.Payload.(events.NoticePayload)
		if ok && strings.TrimSpace(payload.Message) != "" {
			c.notices = append(c.notices, strings.TrimSpace(payload.Message))
		}
	case events.EventToolCallStarted:
		payload, ok := event.Payload.(events.ToolCallPayload)
		if ok {
			tool := c.tool(payload.ID, payload.Name)
			tool.Status = "running"
		}
	case events.EventToolCallFinished:
		payload, ok := event.Payload.(events.ToolCallPayload)
		if ok {
			if payload.Name == "todo_write" && strings.TrimSpace(payload.Error) == "" {
				c.removeTool(payload.ID, payload.Name)
				return
			}
			tool := c.tool(payload.ID, payload.Name)
			if strings.TrimSpace(payload.Error) != "" {
				tool.Status = "failed"
				tool.Error = strings.TrimSpace(payload.Error)
			} else {
				tool.Status = "completed"
				tool.Output = textutil.TruncateRunes(strings.TrimSpace(payload.Output), 240)
			}
			for _, path := range payload.ArtifactPaths {
				path = strings.TrimSpace(path)
				if path == "" || c.hasArtifact(path) {
					continue
				}
				c.artifacts = append(c.artifacts, ReplyArtifact{
					Path: path,
					Name: filepath.Base(path),
				})
			}
		}
	case events.EventTodoListUpdated:
		payload, ok := event.Payload.(events.TodoListPayload)
		if ok {
			c.todos = replyTodosFromPayload(payload)
		}
	}
}

func (c *replyCollector) removeTool(id, name string) {
	key := strings.TrimSpace(id)
	if key == "" {
		prefix := name + "#"
		for _, candidate := range c.toolOrder {
			if strings.HasPrefix(candidate, prefix) {
				key = candidate
				break
			}
		}
	}
	if key == "" || c.tools == nil {
		return
	}
	delete(c.tools, key)
	for i, candidate := range c.toolOrder {
		if candidate == key {
			c.toolOrder = append(c.toolOrder[:i], c.toolOrder[i+1:]...)
			return
		}
	}
}

func (c *replyCollector) Plan() ReplyPlan {
	c.mu.Lock()
	defer c.mu.Unlock()

	plan := ReplyPlan{
		Text:      strings.TrimSpace(c.builder.String()),
		Notices:   append([]string{}, c.notices...),
		Todos:     cloneReplyTodos(c.todos),
		Artifacts: append([]ReplyArtifact{}, c.artifacts...),
	}
	if len(c.toolOrder) == 0 {
		return plan
	}

	plan.Tools = make([]ReplyTool, 0, len(c.toolOrder))
	for _, key := range c.toolOrder {
		tool := c.tools[key]
		if tool == nil {
			continue
		}
		plan.Tools = append(plan.Tools, *tool)
	}
	return plan
}

func (c *replyCollector) tool(id, name string) *ReplyTool {
	if c.tools == nil {
		c.tools = make(map[string]*ReplyTool)
	}
	key := strings.TrimSpace(id)
	if key == "" {
		key = name + "#" + strconvItoa(len(c.toolOrder)+1)
	}
	if existing := c.tools[key]; existing != nil {
		if existing.Name == "" {
			existing.Name = name
		}
		return existing
	}
	tool := &ReplyTool{ID: strings.TrimSpace(id), Name: strings.TrimSpace(name)}
	c.tools[key] = tool
	c.toolOrder = append(c.toolOrder, key)
	return tool
}

func (c *replyCollector) hasArtifact(path string) bool {
	for _, artifact := range c.artifacts {
		if artifact.Path == path {
			return true
		}
	}
	return false
}

func sendReply(ctx context.Context, reply ReplySender, plan ReplyPlan) error {
	if sender, ok := reply.(PlanSender); ok {
		return sender.SendReplyPlan(ctx, plan)
	}
	return reply.SendText(ctx, plan.RenderText())
}

func strconvItoa(v int) string {
	if v == 0 {
		return "0"
	}
	var digits [20]byte
	pos := len(digits)
	for v > 0 {
		pos--
		digits[pos] = byte('0' + v%10)
		v /= 10
	}
	return string(digits[pos:])
}
