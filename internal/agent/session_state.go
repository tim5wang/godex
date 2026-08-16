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

// Harness returns the default godex engine as a Harness, giving callers a
// stable seam for engine switching (roadmap 5.1) without changing the
// existing RunWithOptions path.
func (a *Agent) Harness() Harness {
	return NewGodexHarness(a)
}

// RegisterHarness registers an additional engine for per-turn switching
// (roadmap 6.4). The built-in godex engine is always available; registering
// the same id replaces the previous entry. Registration is dynamic: engines
// registered after the router is first built remain available (research doc
// P2 item 3 removes the sync.Once snapshot limitation).
func (a *Agent) RegisterHarness(id string, harness Harness) {
	a.mu.Lock()
	if a.extraHarnesses == nil {
		a.extraHarnesses = map[string]Harness{}
	}
	a.extraHarnesses[id] = harness
	router := a.harnessRouterVal
	a.mu.Unlock()
	if router != nil {
		if dynamic, ok := router.(interface{ Register(id string, harness Harness) }); ok {
			dynamic.Register(id, harness)
		}
	}
}

// RegisterConfiguredACPHarnesses registers one external-agent Harness for each
// configured ACP agent (阶段 C: Pi/其他 ACP agent 的 Harness adapter). Ids are
// "acp:<agent-id>", so a turn may request e.g. RunOptions.Harness =
// "acp:codex" to delegate the whole turn to that external engine.
func (a *Agent) RegisterConfiguredACPHarnesses() {
	if a == nil || a.cfg == nil || len(a.cfg.ACP.Agents) == 0 {
		return
	}
	for id, cfg := range a.cfg.ACP.Agents {
		if strings.TrimSpace(id) == "" {
			continue
		}
		a.RegisterHarness("acp:"+id, NewACPHarness(id, cfg))
	}
}

// harnessRouter lazily builds the engine router used when a turn requests a
// non-default harness (RunOptions.Harness). Adapters are the godex engine
// plus every engine registered via RegisterHarness; the resolver honors the
// per-turn request and defaults to godex.
func (a *Agent) harnessRouter() Harness {
	a.harnessOnce.Do(func() {
		adapters := map[string]Harness{"godex": NewGodexHarness(a)}
		a.mu.Lock()
		for id, harness := range a.extraHarnesses {
			adapters[id] = harness
		}
		router := NewHarnessRouter(adapters, NewRequestedHarnessResolver("godex"))
		a.harnessRouterVal = router
		a.mu.Unlock()
	})
	return a.harnessRouterVal
}

func (a *Agent) appendMessage(msg protocol.Message) {
	if len(msg.Content) == 0 {
		return
	}
	if msg.Metadata == nil {
		msg.Metadata = &protocol.Metadata{}
	}
	if strings.TrimSpace(msg.Metadata.Timestamp) == "" {
		msg.Metadata.Timestamp = a.safeNow().UTC().Format(time.RFC3339Nano)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.messages = append(a.messages, msg.Clone())
	a.historyVersion++
}

// safeNow returns the agent clock or the zero time when unset (bare agents in
// tests and minimal harness setups do not configure a clock).
func (a *Agent) safeNow() time.Time {
	if a == nil || a.now == nil {
		return time.Time{}
	}
	return a.now()
}

// AppendAssistantText appends one assistant-visible text reply into the session transcript.
func (a *Agent) AppendAssistantText(text string, kind protocol.MessageKind) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	msg := protocol.NewTextMessage(protocol.RoleAssistant, text)
	now := a.safeNow()
	if kind != "" || !now.IsZero() {
		msg.Metadata = &protocol.Metadata{Kind: kind, Timestamp: now.UTC().Format(time.RFC3339Nano)}
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
