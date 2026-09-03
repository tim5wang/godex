package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/conversation"
	"github.com/tim5wang/godex/internal/core/scope"
	"github.com/tim5wang/godex/internal/domain/events"
	"github.com/tim5wang/godex/internal/tools"
)

// ACPHarness is an external-agent Harness adapter (research doc 阶段 C /
// P2 recommendation): it wraps one configured ACP agent so a session can route
// whole turns to an external Agent Client Protocol engine over stdio.
//
// The near-term integration order from the research doc is:
//
//	先通过 ACP 作为任务委派 Agent
//		↓ 验证 session、事件和权限语义（P2 #2 host 统一消费 Reply）
//	再封装为 PiHarness 接管完整 Turn
//
// ACPHarness implements the first step: it consumes the stable
// HarnessTurnInput surface (Messages/WorkspaceDir/Scope) and returns the
// external agent's reply through HarnessTurnResult.Reply, which the host appends
// to the transcript and checkpoints.
type ACPHarness struct {
	agentID string
	cfg     config.ACPAgentConfig
	// PermissionPolicy optionally overrides how the harness answers the
	// external engine's session/request_permission requests (M4 权限桥). When
	// nil the harness denies every request and surfaces it as a warning event.
	PermissionPolicy tools.ACPPermissionHandler

	mu    sync.Mutex
	scope scope.Id // scope bound at first use; cross-scope reuse is rejected

	sessMu    sync.Mutex
	sess      *tools.ACPSession // live persistent session (reused across turns)
	sessionID string            // last known session id, kept for resume after reconnect
}

// NewACPHarness wraps one configured ACP agent as a Harness.
func NewACPHarness(agentID string, cfg config.ACPAgentConfig) *ACPHarness {
	return &ACPHarness{agentID: agentID, cfg: cfg}
}

// Profile returns the engine's static identity.
func (h *ACPHarness) Profile() HarnessProfile {
	name := h.agentID
	if strings.TrimSpace(h.cfg.Description) != "" {
		name = h.cfg.Description
	}
	return HarnessProfile{
		ID:   "acp:" + h.agentID,
		Name: name,
	}
}

// Models returns the external agent's model if the config hints at one; ACP
// agents do not expose model ids, so this is typically empty (the router's
// godex default still lists models).
func (h *ACPHarness) Models() []string { return nil }

// Tools returns the tools the external engine itself exposes. ACP agents run
// in their own process with their own tool surface; GoDex does not forward its
// tool registry into them, so this is empty (no misleading tool claims).
func (h *ACPHarness) Tools() []string { return nil }

// RunTurn delegates one turn to the external ACP agent. The prompt is built
// from the stable message surface (the last user turn), so the engine never
// reaches into the host Agent's internals (P2 #1). The session scope is bound
// at first use and enforced across turns (P2 #5): reusing one harness instance
// for a different scope is rejected instead of silently leaking state.
func (h *ACPHarness) RunTurn(ctx context.Context, input HarnessTurnInput) (HarnessTurnResult, error) {
	if err := h.bindScope(input.Scope); err != nil {
		return HarnessTurnResult{}, err
	}
	userMsg := lastUserMessage(input.Messages)
	if !userMessageHasPromptContent(userMsg) {
		return HarnessTurnResult{}, conversation.NewNonRetryableTurnError("acp harness " + h.agentID + ": no user prompt in turn input")
	}
	workspace := input.WorkspaceDir
	if workspace == "" {
		workspace = "."
	}
	sess, err := h.liveSession(ctx, workspace, input.Model)
	if err != nil {
		h.emitErrorEvent(input, err)
		return HarnessTurnResult{}, err
	}
	// M2: forward the whole user message as ACP content blocks — text always,
	// image attachments only when the engine advertised promptCapabilities.image.
	blocks := tools.ACPContentBlocksForMessage(*userMsg, sess.SupportsImage())
	if len(blocks) == 0 {
		// The message carried content this engine cannot consume (e.g. an
		// image-only turn against an agent without prompt image support).
		err := conversation.NewNonRetryableTurnError("acp harness " + h.agentID + ": engine does not support the message content")
		h.emitErrorEvent(input, err)
		return HarnessTurnResult{}, err
	}
	// M4: answer the engine's permission requests through GoDex policy instead
	// of the default error (which aborts the engine's gated tool call).
	sess.SetPermissionHandler(h.permissionResolver(input))
	result, err := sess.PromptBlocks(ctx, blocks, func(update tools.ACPUpdate) {
		// 阶段 C streaming handle + P2 #4: emit text deltas, thinking chunks
		// and tool-call events as they arrive so downstream sinks stream
		// instead of waiting for the full turn.
		h.emitUpdateEvents(input, []tools.ACPUpdate{update})
	})
	if err != nil {
		// The underlying agent process may have died (e.g. a network drop).
		// Drop the live session so the next turn reconnects; the recorded
		// session id is kept so the external conversation is resumed (via the
		// agent's session/load or session/resume) instead of starting fresh.
		select {
		case <-sess.Done():
			h.dropSession()
		default:
		}
		h.emitErrorEvent(input, err)
		return HarnessTurnResult{}, err
	}
	h.rememberSession(result.SessionID)
	h.emitUsageEvent(input, result.Usage, time.Now())
	return HarnessTurnResult{
		Reply:     strings.TrimSpace(result.Text),
		Completed: true,
	}, nil
}

// emitUsageEvent maps the external agent's per-turn token usage (when the
// agent reports it in the session/prompt result) onto the unified
// model_request_completed event, so timeline and cache-hit surfaces render it
// exactly like a native model request (idea ①: 上下文用量/缓存命中展示的
// consumption side). External engine usage is informational only — the tokens
// are consumed by the agent's own provider — so nothing is written to the
// GoDex usage ledger here.
func (h *ACPHarness) emitUsageEvent(input HarnessTurnInput, usage *tools.ACPTurnUsage, completedAt time.Time) {
	if input.Sink == nil || usage == nil {
		return
	}
	total := usage.InputTokens + usage.OutputTokens + usage.CachedReadTokens + usage.CachedWriteTokens + usage.ThoughtTokens
	if total == 0 {
		return
	}
	input.Sink.Emit(events.Event{
		SessionID: input.SessionID,
		TurnID:    input.TurnID,
		Type:      events.EventModelRequestCompleted,
		Timestamp: completedAt,
		Payload: events.ModelRequestPayload{
			Model:            "acp:" + h.agentID,
			InputTokens:      usage.InputTokens,
			OutputTokens:     usage.OutputTokens,
			CacheReadTokens:  usage.CachedReadTokens,
			CacheWriteTokens: usage.CachedWriteTokens,
			CompletedAt:      completedAt,
		},
	})
}

// liveSession returns the persistent ACP session, reopening it (resuming the
// recorded session id when available) if the previous process died.
func (h *ACPHarness) liveSession(ctx context.Context, workspace, model string) (*tools.ACPSession, error) {
	h.sessMu.Lock()
	defer h.sessMu.Unlock()
	if h.sess != nil {
		select {
		case <-h.sess.Done():
			_ = h.sess.Close()
			h.sess = nil
		default:
			return h.sess, nil
		}
	}
	opened, err := tools.OpenACPSession(ctx, h.cfg, workspace, model, 0, h.sessionID)
	if err != nil {
		return nil, err
	}
	h.sess = opened
	h.sessionID = opened.SessionID()
	return opened, nil
}

// rememberSession records the live session id so a later reconnect can resume
// the same external conversation.
func (h *ACPHarness) rememberSession(sessionID string) {
	h.sessMu.Lock()
	h.sessionID = sessionID
	h.sessMu.Unlock()
}

// dropSession closes the live session and clears it (session id is kept for
// resume; callers that want a fully fresh start clear it explicitly).
func (h *ACPHarness) dropSession() {
	h.sessMu.Lock()
	if h.sess != nil {
		_ = h.sess.Close()
		h.sess = nil
	}
	h.sessMu.Unlock()
}

// bindScope binds the harness to a scope on first use and rejects later
// cross-scope reuse (P2 #5: explicit external-engine scope).
func (h *ACPHarness) bindScope(inputScope scope.Id) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.scope == "" {
		h.scope = inputScope
		return nil
	}
	if h.scope != inputScope {
		return conversation.NewNonRetryableTurnError(
			fmt.Sprintf("acp harness %s is bound to scope %q but the turn is in scope %q", h.agentID, h.scope, inputScope))
	}
	return nil
}

// ResetSession unbinds the harness scope and closes the live session so a
// fresh session can rebind it (called by the router when a session switches
// engines). The recorded session id is cleared so an engine switch starts a
// new external conversation.
func (h *ACPHarness) ResetSession(ctx context.Context, sessionID string) error {
	h.mu.Lock()
	h.scope = ""
	h.mu.Unlock()
	h.sessMu.Lock()
	if h.sess != nil {
		_ = h.sess.Close()
		h.sess = nil
	}
	h.sessionID = ""
	h.sessMu.Unlock()
	return nil
}

// Close releases engine resources (the live session process, if any).
func (h *ACPHarness) Close() error {
	h.dropSession()
	return nil
}

// emitErrorEvent maps an external-engine turn failure onto the unified
// error_raised event (P2 #4).
func (h *ACPHarness) emitErrorEvent(input HarnessTurnInput, err error) {
	if sink := input.Sink; sink != nil && err != nil {
		sink.Emit(events.Event{
			SessionID: input.SessionID,
			TurnID:    input.TurnID,
			Type:      events.EventErrorRaised,
			Timestamp: time.Now(),
			Payload: events.NoticePayload{
				Message:   err.Error(),
				Code:      "acp_harness_error",
				ActorKind: "agent",
				ActorID:   h.agentID,
			},
		})
	}
}

// permissionResolver returns the session/request_permission handler for one
// turn: it surfaces every request as a warning event on the turn sink, then
// applies the harness's PermissionPolicy when set, or a default deny (M4
// 权限桥: an external engine's gated operations never run without an explicit
// GoDex-side decision, and every request is auditable via the event stream).
func (h *ACPHarness) permissionResolver(input HarnessTurnInput) tools.ACPPermissionHandler {
	return func(ctx context.Context, req tools.ACPPermissionRequest) (tools.ACPPermissionResponse, error) {
		resp, err := tools.DenyACPPermissionRequest(req)
		if h.PermissionPolicy != nil {
			resp, err = h.PermissionPolicy(ctx, req)
		}
		h.emitPermissionEvent(input, req, err == nil)
		return resp, err
	}
}

// emitPermissionEvent surfaces one session/request_permission decision as a
// warning event so hosts see (and can audit) what the external engine asked
// for and what GoDex answered.
func (h *ACPHarness) emitPermissionEvent(input HarnessTurnInput, req tools.ACPPermissionRequest, decided bool) {
	if input.Sink == nil {
		return
	}
	title := ""
	if req.ToolCall.Title != nil {
		title = *req.ToolCall.Title
	}
	optionNames := make([]string, 0, len(req.Options))
	for _, opt := range req.Options {
		optionNames = append(optionNames, string(opt.OptionId))
	}
	message := fmt.Sprintf("external engine %s requested permission for %q (options: %s)", h.agentID, title, strings.Join(optionNames, ", "))
	if !decided {
		message += "; no decision policy produced an outcome (answered with an error)"
	}
	input.Sink.Emit(events.Event{
		SessionID: input.SessionID,
		TurnID:    input.TurnID,
		Type:      events.EventWarningRaised,
		Timestamp: time.Now(),
		Payload: events.NoticePayload{
			Message:      message,
			Code:         "acp_permission_request",
			ActorKind:    "agent",
			ActorID:      h.agentID,
			RecoveryHint: "The external engine's gated operation was not allowed to run on its own.",
		},
	})
}

// emitUpdateEvents maps captured ACP updates onto GoDex events.
func (h *ACPHarness) emitUpdateEvents(input HarnessTurnInput, updates []tools.ACPUpdate) {
	sink := input.Sink
	if sink == nil {
		return
	}
	emit := func(eventType events.EventType, payload any) {
		sink.Emit(events.Event{
			SessionID: input.SessionID,
			TurnID:    input.TurnID,
			Type:      eventType,
			Timestamp: time.Now(),
			Payload:   payload,
		})
	}
	for _, update := range updates {
		switch update.Kind {
		case "message_chunk":
			if strings.TrimSpace(update.Text) == "" {
				continue
			}
			emit(events.EventAssistantTextDelta, events.TextPayload{Role: protocol.RoleAssistant, Text: update.Text})
		case "thought_chunk":
			if strings.TrimSpace(update.Text) == "" {
				continue
			}
			emit(events.EventAssistantThinkingDelta, events.TextPayload{Role: protocol.RoleAssistant, Text: update.Text})
		case "tool_call":
			emit(events.EventToolCallStarted, events.ToolCallPayload{
				ID:    update.ToolCallID,
				Name:  update.Name,
				Input: update.Input,
			})
		case "tool_call_update":
			emit(events.EventToolCallFinished, events.ToolCallPayload{
				ID:    update.ToolCallID,
				Name:  update.Name,
				Input: update.Input,
			})
		case "plan", "permission_request", "permission_denied":
			// P2 #4: advisory/plan/permission updates from the external engine
			// surface as warnings so nothing the engine reports is silently
			// dropped.
			emit(events.EventWarningRaised, events.NoticePayload{
				Message:      fmt.Sprintf("external engine %s reported %s", h.agentID, update.Kind),
				Code:         "acp_external_update",
				ActorKind:    "agent",
				ActorID:      h.agentID,
				RecoveryHint: "The external engine continues; inspect the raw update for details.",
			})
		}
	}
}

// lastUserPrompt extracts the most recent user text from the message snapshot
// for building the external-agent prompt. It is kept for callers that need the
// plain text only (and unit-tested); RunTurn uses lastUserMessage + block
// conversion instead so attachments can be forwarded (M2).
func lastUserPrompt(messages func() []protocol.Message) string {
	if messages == nil {
		return ""
	}
	items := messages()
	for i := len(items) - 1; i >= 0; i-- {
		if items[i].Role != protocol.RoleUser {
			continue
		}
		var text strings.Builder
		for _, block := range items[i].Content {
			if block.Type == protocol.BlockText {
				text.WriteString(block.Text)
			}
		}
		if trimmed := strings.TrimSpace(text.String()); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// lastUserMessage returns the most recent user-authored message in the
// snapshot, preserving every content block (text + image) so the external
// engine receives the whole user turn (M2). Nil when there is no user message.
func lastUserMessage(messages func() []protocol.Message) *protocol.Message {
	if messages == nil {
		return nil
	}
	items := messages()
	for i := len(items) - 1; i >= 0; i-- {
		if items[i].Role != protocol.RoleUser {
			continue
		}
		msg := items[i]
		return &msg
	}
	return nil
}

// userMessageHasPromptContent reports whether the message carries content that
// can be forwarded to an external engine (non-empty text, or an image).
func userMessageHasPromptContent(msg *protocol.Message) bool {
	if msg == nil {
		return false
	}
	for _, block := range msg.Content {
		if block.Type == protocol.BlockText && strings.TrimSpace(block.Text) != "" {
			return true
		}
		if block.Type == protocol.BlockImage && block.Source != nil {
			return true
		}
	}
	return false
}
