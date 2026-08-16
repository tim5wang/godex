package agent

import (
	"context"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/conversation"
	"github.com/tim5wang/godex/internal/core/protocol"
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
// HarnessTurnInput surface (Messages/WorkspaceDir) and returns the external
// agent's reply through HarnessTurnResult.Reply, which the host appends to the
// transcript and checkpoints.
type ACPHarness struct {
	agentID string
	cfg     config.ACPAgentConfig
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
// reaches into the host Agent's internals (P2 #1).
func (h *ACPHarness) RunTurn(ctx context.Context, input HarnessTurnInput) (HarnessTurnResult, error) {
	prompt := lastUserPrompt(input.Messages)
	if strings.TrimSpace(prompt) == "" {
		return HarnessTurnResult{}, conversation.NewNonRetryableTurnError("acp harness " + h.agentID + ": no user prompt in turn input")
	}
	workspace := input.WorkspaceDir
	if workspace == "" {
		workspace = "."
	}
	result, err := tools.RunACPAgent(ctx, h.cfg, workspace, prompt, 0)
	if err != nil {
		return HarnessTurnResult{}, err
	}
	// P2 #4 unified event mapping: replay the external engine's session/update
	// events as GoDex events (text deltas, tool calls) so downstream sinks see
	// the same shape as the default engine.
	h.emitUpdateEvents(input, result.UpdateEvents())
	return HarnessTurnResult{
		Reply:     strings.TrimSpace(result.Text),
		Completed: true,
	}, nil
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
		case "tool_call":
			emit(events.EventToolCallStarted, events.ToolCallPayload{
				ID:    update.Name,
				Name:  update.Name,
				Input: update.Input,
			})
		case "tool_call_update":
			emit(events.EventToolCallFinished, events.ToolCallPayload{
				ID:    update.Name,
				Name:  update.Name,
				Input: update.Input,
			})
		}
	}
}

// ResetSession is a no-op: each ACP run starts a fresh process with its own
// session (session/new per run), so there is no engine-side state to drop.
func (h *ACPHarness) ResetSession(ctx context.Context, sessionID string) error {
	return nil
}

// Close releases engine resources (none held between runs).
func (h *ACPHarness) Close() error { return nil }

// lastUserPrompt extracts the most recent user text from the message snapshot
// for building the external-agent prompt.
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
