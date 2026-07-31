package agent

import (
	"sort"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/core/memory"
	"github.com/tim5wang/godex/internal/core/protocol"
	"github.com/tim5wang/godex/internal/core/teammate"
	"github.com/tim5wang/godex/internal/domain/automation"
	"github.com/tim5wang/godex/internal/domain/message"
	"github.com/tim5wang/godex/internal/domain/task"
	"github.com/tim5wang/godex/internal/domain/todo"
	"github.com/tim5wang/godex/internal/tools"
)

// AddMessage adds a user message.
func (a *Agent) AddMessage(content string) {
	a.AddEnvelope(message.NewCLIEnvelope("repl", a.cfg.LeadName, content, a.now()))
}

// AddEnvelope adds a generic user-facing runtime envelope into the conversation.
func (a *Agent) AddEnvelope(envelope message.Envelope) {
	a.appendMessage(envelope.ToProtocolMessage(protocol.RoleUser, "", false))
}

// AppendRuntimeFeedback appends model-visible runtime guidance without treating
// it as a new user-submitted message in the UI timeline.
func (a *Agent) AppendRuntimeFeedback(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	a.appendMessage(protocol.NewEphemeralTextMessage(protocol.KindBackground, text))
}

// GetMessages returns current messages.
func (a *Agent) GetMessages() []protocol.Message {
	a.mu.Lock()
	defer a.mu.Unlock()
	return protocol.CloneMessages(a.messages)
}

// TranscriptRefs returns persisted transcript archive references for the session.
func (a *Agent) TranscriptRefs() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string{}, a.transcriptRefs...)
}

// HistorySearchRuntime exposes the session-bound history search runtime.
func (a *Agent) HistorySearchRuntime() tools.HistorySearchRuntime {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.historySearch
}

// ClearMessages clears conversation prompt state while keeping durable session
// records such as timeline, turns, permissions, memory, and tasks intact.
func (a *Agent) ClearMessages() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.messages = nil
	a.transcriptRefs = nil
	a.pendingResume = nil
	a.toolHandler.ResetActiveToolsToDefaults()
	a.historyVersion++
	a.lastCompactedVersion = 0
	a.resetCacheStats()
}

// TruncateMessages resets the transcript to the first count messages.
func (a *Agent) TruncateMessages(count int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	switch {
	case count <= 0:
		a.messages = nil
	case count >= len(a.messages):
		return
	default:
		a.messages = protocol.CloneMessages(a.messages[:count])
	}
	a.historyVersion++
}

// SetIdle sets the idle state.
func (a *Agent) SetIdle(idle bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.idleRequested = idle
}

// TaskMgr returns the task manager.
func (a *Agent) TaskMgr() *task.Manager {
	return a.taskMgr
}

// TeamMgr returns the team manager.
func (a *Agent) TeamMgr() *teammate.Manager {
	return a.teamMgr
}

// ToolCatalog returns the current tool bundle/catalog state.
func (a *Agent) ToolCatalog() tools.ToolCatalog {
	return a.toolHandler.Catalog()
}

func (a *Agent) pendingResumeState() *PendingResumeState {
	a.mu.Lock()
	defer a.mu.Unlock()
	return clonePendingResumeState(a.pendingResume)
}

// PendingResumeState returns the currently blocked user turn, if any.
func (a *Agent) PendingResumeState() *PendingResumeState {
	return a.pendingResumeState()
}

// SetPendingResume stores one blocked user turn for replay after approval.
func (a *Agent) SetPendingResume(requestID string, priorMessageCount int, envelope message.Envelope, runtimeCtx automation.SessionContext, injections ...message.Envelope) {
	a.mu.Lock()
	defer a.mu.Unlock()
	normalizedInjections := make([]message.Envelope, 0, len(injections))
	for _, item := range injections {
		normalizedInjections = append(normalizedInjections, item.Normalized())
	}
	a.pendingResume = &PendingResumeState{
		RequestID:         strings.TrimSpace(requestID),
		PriorMessageCount: priorMessageCount,
		Envelope:          envelope.Normalized(),
		Injections:        normalizedInjections,
		RuntimeContext:    runtimeCtx.Clone(),
	}
}

// ClearPendingResume discards any blocked-turn replay state.
func (a *Agent) ClearPendingResume() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pendingResume = nil
}

// ActiveSkillNames returns the currently activated skill names.
func (a *Agent) ActiveSkillNames() []string {
	a.mu.Lock()
	defer a.mu.Unlock()

	names := make([]string, 0, len(a.activeSkills))
	for name := range a.activeSkills {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// MsgBus returns the message bus.
func (a *Agent) MsgBus() *message.Bus {
	return a.msgBus
}

// TodoMgr returns the todo manager.
func (a *Agent) TodoMgr() *todo.Manager {
	return a.todoMgr
}

// MemoryMgr returns the durable memory manager.
func (a *Agent) MemoryMgr() *memory.Manager {
	return a.memoryMgr
}

// CurrentModel returns the currently configured model for this session runtime.
func (a *Agent) CurrentModel() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return strings.TrimSpace(a.cfg.Model)
}

func (a *Agent) appendMessage(msg protocol.Message) {
	if len(msg.Content) == 0 {
		return
	}
	if msg.Metadata == nil {
		msg.Metadata = &protocol.Metadata{}
	}
	if strings.TrimSpace(msg.Metadata.Timestamp) == "" {
		msg.Metadata.Timestamp = a.now().UTC().Format(time.RFC3339Nano)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.messages = append(a.messages, msg.Clone())
	a.historyVersion++
}

// AppendAssistantText appends one assistant-visible text reply into the session transcript.
func (a *Agent) AppendAssistantText(text string, kind protocol.MessageKind) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	msg := protocol.NewTextMessage(protocol.RoleAssistant, text)
	if kind != "" || !a.now().IsZero() {
		msg.Metadata = &protocol.Metadata{Kind: kind, Timestamp: a.now().UTC().Format(time.RFC3339Nano)}
	}
	a.appendMessage(msg)
}

// AppendAssistantDelivery appends one assistant-visible message with optional
// attachment metadata into the session transcript.
func (a *Agent) AppendAssistantDelivery(text string, kind protocol.MessageKind, attachments []message.AttachmentRef) {
	msg := protocol.NewTextMessage(protocol.RoleAssistant, strings.TrimSpace(text))
	if kind != "" || len(attachments) > 0 || !a.now().IsZero() {
		msg.Metadata = &protocol.Metadata{Kind: kind, Timestamp: a.now().UTC().Format(time.RFC3339Nano)}
	}
	if len(attachments) > 0 {
		if msg.Metadata == nil {
			msg.Metadata = &protocol.Metadata{}
		}
		msg.Metadata.Attachments = make([]protocol.Attachment, 0, len(attachments))
		for _, attachment := range attachments {
			msg.Metadata.Attachments = append(msg.Metadata.Attachments, protocol.Attachment{
				ID:        attachment.ID,
				Name:      attachment.Name,
				MIMEType:  attachment.MIMEType,
				Path:      attachment.Path,
				URL:       attachment.URL,
				SizeBytes: attachment.SizeBytes,
			})
		}
	}
	a.appendMessage(msg)
}

func (a *Agent) messageState() ([]protocol.Message, int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return protocol.CloneMessages(a.messages), a.historyVersion
}

func (a *Agent) resetIdle() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.idleRequested = false
}

func (a *Agent) consumeIdleRequest() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	idle := a.idleRequested
	a.idleRequested = false
	return idle
}
