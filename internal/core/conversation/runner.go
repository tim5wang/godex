package conversation

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/core/modelcontext"
	"github.com/tim5wang/godex/internal/core/protocol"
)

// ErrMaxTurnsReached indicates the shared runner exhausted its turn budget.
var ErrMaxTurnsReached = errors.New("conversation runner reached max turns")

// ErrRepeatedToolCalls indicates the model is repeating the same tool call without progress.
var ErrRepeatedToolCalls = errors.New("conversation runner repeated identical tool calls")

const defaultMaxRepeatedTools = 8
const defaultToolTimeout = 30 * time.Minute
const repeatedToolCycleWindow = 12
const defaultMaxRepeatedPollingTools = 5
const defaultMaxStalledTaskPollingTools = 8
const defaultMaxEmptyResponses = 3
const defaultMaxLengthRecoveries = 4
const defaultMaxModelRetries = 2

// defaultMaxNoMutationRounds caps consecutive tool rounds with no file
// mutation (edit_file/write_file) before the loop guard nudges the model.
// Research spirals ("I found the root cause, one more confirmation...") look
// exactly like this; real implementation work writes files within a few
// rounds.
const defaultMaxNoMutationRounds = 12

// maxReasoningLengthRecoveries bounds how many times the runner re-requests
// after a reasoning-budget overflow (finish_reason=length + empty answer +
// reasoning_content present). Two attempts are enough: if the brevity nudge
// does not produce an answer, the empty-response error path takes over.
const maxReasoningLengthRecoveries = 2
const defaultMaxInjectionsPerTurn = 8
const defaultMaxInjectionCycles = 4
const defaultMaxLoopGuardRecoveries = 5
const benignRepeatableToolCountPrefix = "benign-repeatable:"

// LoopGuardMode controls the loop guard abort behavior.
type LoopGuardMode string

const (
	LoopGuardModeStrict   LoopGuardMode = "strict"
	LoopGuardModeBalanced LoopGuardMode = "balanced"
	LoopGuardModeWarn     LoopGuardMode = "warn"
)

func normalizeLoopGuardMode(mode string) LoopGuardMode {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case string(LoopGuardModeStrict), "":
		return LoopGuardModeStrict
	case string(LoopGuardModeBalanced):
		return LoopGuardModeBalanced
	case string(LoopGuardModeWarn):
		return LoopGuardModeWarn
	default:
		return LoopGuardModeStrict
	}
}

const (
	PhaseModelRequest     = "model_request"
	PhaseAwaitingTools    = "awaiting_tools"
	PhaseToolsCompleted   = "tools_completed"
	PhaseFinalResponse    = "final_response"
	PhaseError            = "error"
	PhaseInterrupted      = "interrupted"
	PhaseContextSanitized = "context_sanitized"
	PhaseInjectionDrained = "injection_drained"
	PhaseRecoveryAttempt  = "recovery_attempted"
)

// ExecutedTool captures a completed tool invocation.
type ExecutedTool struct {
	ID            string
	Name          string
	Input         map[string]interface{}
	Output        string
	Error         string
	ArtifactPaths []string
	Code          string
	RecoveryHint  string
	TimedOut      bool
	DurationMS    int64
}

// ToolExecutionResult is the structured outcome returned by the tool executor.
type ToolExecutionResult struct {
	Output        string
	ArtifactPaths []string
	Code          string
	RecoveryHint  string
	TimedOut      bool
}

// ToolStuckEvent reports a tool call that has exceeded its watchdog threshold
// but has not yet hit the hard timeout.
type ToolStuckEvent struct {
	ID        string
	Name      string
	Input     map[string]interface{}
	Elapsed   time.Duration
	Timeout   time.Duration
	Threshold time.Duration
}

// ToolResultFilter can replace the model-visible output for a completed tool.
// It is intended for context hygiene such as large-result stubbing.
type ToolResultFilter func(context.Context, ExecutedTool) ExecutedTool

// PhaseEvent describes a runner lifecycle checkpoint.
type PhaseEvent struct {
	Phase        string
	Iteration    int
	Model        string
	Message      string
	ToolID       string
	ToolName     string
	RecoveryHint string
}

// InjectionDrain describes one successful mid-turn injection drain.
type InjectionDrain struct {
	Messages       []protocol.Message
	Count          int
	Remaining      int
	InjectionCycle int
	Mode           string
	Summary        string
}

// Result describes the outcome of a runner execution.
type Result struct {
	LastAssistantText string
	Turns             int
	Completed         bool
	Stopped           bool
	HadInjections     bool
	RecoveryHint      string
}

type stopAfterToolError interface {
	StopConversationAfterTool() bool
}

type permissionPendingToolError interface {
	PendingPermissionRequestID() string
}

// Runner executes the shared assistant/tool loop.
type Runner struct {
	Caller                Caller
	BuildRequest          func(context.Context) (protocol.Request, error)
	AppendAssistant       func(protocol.Message)
	AppendToolResults     func(protocol.Message)
	AppendRuntimeFeedback func(protocol.Message)
	ExecuteTool           func(context.Context, string, map[string]interface{}) (ToolExecutionResult, error)
	OnAssistantText       func(string)
	OnAssistantTextDelta  func(string)
	// OnAssistantThinkingDelta receives model reasoning deltas as they stream
	// (OpenAI Responses reasoning_summary_text / reasoning_text, Anthropic
	// extended thinking). It mirrors OnAssistantTextDelta but for chain-of-
	// thought: frontends surface it as a live "thinking" stream, and the
	// deltas are also accumulated into the final response's ReasoningContent.
	OnAssistantThinkingDelta   func(string)
	OnExecutedTools            func([]ExecutedTool)
	OnToolStarted              func(protocol.Block)
	OnToolFinished             func(ExecutedTool)
	OnToolStuck                func(ToolStuckEvent)
	OnPhase                    func(PhaseEvent)
	DrainInjections            func(context.Context, int) (InjectionDrain, error)
	AppendInjectedMessages     func([]protocol.Message)
	ToolResultFilter           ToolResultFilter
	AfterTurn                  func()
	StopAfterTools             func() bool
	MaxTurns                   int
	MaxEmptyResponses          int
	MaxLengthRecoveries        int
	MaxNoMutationRounds        int
	MaxInjectionsPerTurn       int
	MaxInjectionCycles         int
	MaxRepeatedTools           int
	MaxRepeatedPollingTools    int
	MaxStalledTaskPollingTools int
	MaxLoopGuardRecoveries     int
	MaxModelRetries            int
	LoopGuardMode              LoopGuardMode
	ToolTimeout                time.Duration
}

// NewRequest builds a provider request from structured messages.
func NewRequest(model string, maxTokens int, reasoningEffort string, system string, messages []protocol.Message, tools []protocol.ToolSchema) protocol.Request {
	return protocol.Request{
		Model:           model,
		MaxTokens:       maxTokens,
		ReasoningEffort: reasoningEffort,
		System:          system,
		Messages:        protocol.ToAPIMessages(messages),
		Tools:           tools,
	}
}

// NewRequestFromAPIMessages builds a provider request from already-serialized API messages.
func NewRequestFromAPIMessages(model string, maxTokens int, reasoningEffort string, system string, messages []protocol.APIMessage, tools []protocol.ToolSchema) protocol.Request {
	return protocol.Request{
		Model:           model,
		MaxTokens:       maxTokens,
		ReasoningEffort: reasoningEffort,
		System:          system,
		Messages:        messages,
		Tools:           tools,
	}
}

// Run executes the conversation loop until completion, stop request, or turn limit.
func (r Runner) Run(ctx context.Context) (*Result, error) {
	if r.Caller == nil {
		return nil, fmt.Errorf("missing caller")
	}
	if r.BuildRequest == nil {
		return nil, fmt.Errorf("missing request builder")
	}
	if r.ExecuteTool == nil {
		return nil, fmt.Errorf("missing tool executor")
	}

	maxTurns := r.MaxTurns
	if maxTurns <= 0 {
		maxTurns = 1
	}
	toolTimeout := r.ToolTimeout
	if toolTimeout <= 0 {
		toolTimeout = defaultToolTimeout
	}
	maxRepeatedTools := r.MaxRepeatedTools
	if maxRepeatedTools <= 0 {
		maxRepeatedTools = defaultMaxRepeatedTools
	}
	maxRepeatedPollingTools := r.MaxRepeatedPollingTools
	if maxRepeatedPollingTools <= 0 {
		maxRepeatedPollingTools = defaultMaxRepeatedPollingTools
	}
	maxStalledTaskPollingTools := r.MaxStalledTaskPollingTools
	if maxStalledTaskPollingTools <= 0 {
		maxStalledTaskPollingTools = defaultMaxStalledTaskPollingTools
	}
	maxEmptyResponses := r.MaxEmptyResponses
	if maxEmptyResponses <= 0 {
		maxEmptyResponses = defaultMaxEmptyResponses
	}
	maxLengthRecoveries := r.MaxLengthRecoveries
	if maxLengthRecoveries <= 0 {
		maxLengthRecoveries = defaultMaxLengthRecoveries
	}
	maxModelRetries := r.MaxModelRetries
	if maxModelRetries <= 0 {
		maxModelRetries = defaultMaxModelRetries
	}
	maxInjectionsPerTurn := r.MaxInjectionsPerTurn
	if maxInjectionsPerTurn <= 0 {
		maxInjectionsPerTurn = defaultMaxInjectionsPerTurn
	}
	maxInjectionCycles := r.MaxInjectionCycles
	if maxInjectionCycles <= 0 {
		maxInjectionCycles = defaultMaxInjectionCycles
	}
	maxLoopGuardRecoveries := r.MaxLoopGuardRecoveries
	if maxLoopGuardRecoveries <= 0 {
		maxLoopGuardRecoveries = defaultMaxLoopGuardRecoveries
	}
	maxNoMutationRounds := r.MaxNoMutationRounds
	if maxNoMutationRounds <= 0 {
		maxNoMutationRounds = defaultMaxNoMutationRounds
	}
	loopGuardMode := r.LoopGuardMode
	if loopGuardMode == "" {
		loopGuardMode = LoopGuardModeStrict
	}
	guard := newLoopGuard(loopGuardConfig{
		MaxRepeatedTools:           maxRepeatedTools,
		MaxRepeatedPollingTools:    maxRepeatedPollingTools,
		MaxStalledTaskPollingTools: maxStalledTaskPollingTools,
		MaxRecoveries:              maxLoopGuardRecoveries,
		MaxNoMutationRounds:        maxNoMutationRounds,
		Mode:                       loopGuardMode,
	})

	result := &Result{}
	emptyResponses := 0
	lengthRecoveries := 0
	reasoningLengthRecoveries := 0
	injectionCycles := 0
	for turn := 0; turn < maxTurns; turn++ {
		result.Turns = turn + 1

		req, err := r.BuildRequest(ctx)
		if err != nil {
			r.emitPhase(PhaseEvent{Phase: PhaseError, Iteration: turn + 1, Message: err.Error()})
			return nil, err
		}
		req.Messages = SanitizeMessagesForProvider(req.Messages)
		r.emitPhase(PhaseEvent{Phase: PhaseContextSanitized, Iteration: turn + 1, Model: req.Model})
		r.emitPhase(PhaseEvent{Phase: PhaseModelRequest, Iteration: turn + 1, Model: req.Model})

		var resp *protocol.Response
		var streamed bool
		for attempt := 0; ; attempt++ {
			var callErr error
			resp, streamed, callErr = r.callModel(ctx, req)
			if callErr == nil {
				break
			}
			if errors.Is(ctx.Err(), context.Canceled) || errors.Is(callErr, context.Canceled) {
				result.Stopped = true
				result.RecoveryHint = diagnosticRecoveryHint(turn+1, nil)
				r.emitPhase(PhaseEvent{Phase: PhaseInterrupted, Iteration: turn + 1, Model: req.Model, Message: callErr.Error(), RecoveryHint: result.RecoveryHint})
				return result, callErr
			}
			// 5.2 Turn Error routing: retryable/transient model errors get a
			// bounded in-turn retry budget instead of failing the turn on the
			// first provider hiccup. Non-retryable errors surface immediately.
			// The retry loop stays inside the current turn, so retries do not
			// consume the outer maxTurns budget.
			class := ClassifyTurnError(callErr)
			if (class == TurnErrorRetryable || class == TurnErrorTransient) && attempt < maxModelRetries {
				r.emitPhase(PhaseEvent{Phase: PhaseRecoveryAttempt, Iteration: turn + 1, Model: req.Model, Message: fmt.Sprintf("model error classified as %s; retrying (%d/%d): %v", class, attempt+1, maxModelRetries, callErr), RecoveryHint: diagnosticRecoveryHint(turn+1, nil)})
				continue
			}
			result.Stopped = true
			result.RecoveryHint = diagnosticRecoveryHint(turn+1, nil)
			r.emitPhase(PhaseEvent{Phase: PhaseError, Iteration: turn + 1, Model: req.Model, Message: callErr.Error(), RecoveryHint: result.RecoveryHint})
			return result, callErr
		}

		assistantMsg := protocol.MessageFromResponse(*resp)
		if !protocol.HasToolUse(resp.Content) && strings.TrimSpace(protocol.MessageText(assistantMsg)) == "" {
			// Reasoning-budget overflow: the model (e.g. deepseek v4) spends its
			// entire output budget on reasoning_content and stops with
			// finish_reason=length and NO answer. Blindly retrying the same
			// request reproduces the same overflow, so re-request with a
			// brevity instruction instead.
			if resp.StopReason == "length" && strings.TrimSpace(resp.ReasoningContent) != "" && reasoningLengthRecoveries < maxReasoningLengthRecoveries {
				reasoningLengthRecoveries++
				r.emitPhase(PhaseEvent{Phase: PhaseRecoveryAttempt, Iteration: turn + 1, Model: req.Model, Message: "model exhausted output budget on reasoning; requesting direct answer", RecoveryHint: "The provider's reasoning consumed the output token budget; the runner requested a direct, concise answer."})
				if r.AppendInjectedMessages != nil {
					r.AppendInjectedMessages([]protocol.Message{protocol.NewTextMessage(protocol.RoleUser, "Your previous response consumed its output token budget on reasoning and produced no answer. Answer directly and concisely now; keep reasoning minimal.")})
				}
				continue
			}
			if emptyResponses < maxEmptyResponses {
				emptyResponses++
				r.emitPhase(PhaseEvent{Phase: PhaseRecoveryAttempt, Iteration: turn + 1, Model: req.Model, Message: "empty model response; retrying", RecoveryHint: "Retrying the model request because the provider returned an empty response."})
				continue
			}
			// Safety valve: an exhausted empty-response streak means the
			// provider is failing (e.g. it rejects tool-result messages or is
			// overloaded). Surface the failure as a hard error instead of
			// fabricating a "diagnostic handoff" — a fake completion that ends
			// the turn without doing work and invites the user to restart the
			// same stalled loop by saying "continue".
			result.Stopped = true
			result.RecoveryHint = diagnosticRecoveryHint(turn+1, nil)
			err := fmt.Errorf("LLM provider returned empty responses %d times in a row; aborting instead of producing a fake handoff (provider/gateway may reject the request shape, the model may have exhausted its output budget on reasoning, or the provider is overloaded)", emptyResponses)
			r.emitPhase(PhaseEvent{Phase: PhaseError, Iteration: turn + 1, Model: req.Model, Message: err.Error(), RecoveryHint: result.RecoveryHint})
			if drained := r.tryDrainInjections(ctx, maxInjectionsPerTurn, maxInjectionCycles, &injectionCycles); drained.Count > 0 {
				result.HadInjections = true
				continue
			}
			return result, err
		}
		if len(assistantMsg.Content) > 0 {
			if r.AppendAssistant != nil {
				r.AppendAssistant(assistantMsg)
			}
			if text := protocol.MessageText(assistantMsg); text != "" {
				result.LastAssistantText = text
				if !streamed && r.OnAssistantTextDelta != nil {
					r.OnAssistantTextDelta(text)
				}
				if r.OnAssistantText != nil {
					r.OnAssistantText(text)
				}
			}
		}

		if !protocol.HasToolUse(resp.Content) {
			if resp.StopReason == "length" && strings.TrimSpace(protocol.MessageText(assistantMsg)) != "" && lengthRecoveries < maxLengthRecoveries {
				lengthRecoveries++
				r.emitPhase(PhaseEvent{Phase: PhaseRecoveryAttempt, Iteration: turn + 1, Model: req.Model, Message: "model response reached length limit; requesting continuation", RecoveryHint: "The provider stopped because of the output length limit; the runner requested continuation."})
				if r.AppendInjectedMessages != nil {
					r.AppendInjectedMessages([]protocol.Message{protocol.NewTextMessage(protocol.RoleUser, "Continue exactly where the previous response stopped. Do not repeat completed content.")})
				}
				continue
			}
			r.emitPhase(PhaseEvent{Phase: PhaseFinalResponse, Iteration: turn + 1, Model: req.Model})
			if drained := r.tryDrainInjections(ctx, maxInjectionsPerTurn, maxInjectionCycles, &injectionCycles); drained.Count > 0 {
				result.HadInjections = true
				continue
			}
			if r.AfterTurn != nil {
				r.AfterTurn()
			}
			result.Completed = true
			return result, nil
		}

		r.emitPhase(PhaseEvent{Phase: PhaseAwaitingTools, Iteration: turn + 1, Model: req.Model})
		toolResultMsg, executed, toolErr := ExecuteToolUsesWithOptions(ctx, resp.Content, r.ExecuteTool, r.OnToolStarted, r.OnToolFinished, r.ToolResultFilter, ExecuteToolOptions{
			Timeout:           toolTimeout,
			StuckWarningAfter: toolStuckWarningAfter(toolTimeout),
			OnStuck:           r.OnToolStuck,
		})
		if len(toolResultMsg.Content) > 0 && r.AppendToolResults != nil {
			r.AppendToolResults(toolResultMsg)
		}
		if len(executed) > 0 && r.OnExecutedTools != nil {
			r.OnExecutedTools(executed)
		}
		if r.AfterTurn != nil {
			r.AfterTurn()
		}
		r.emitPhase(PhaseEvent{Phase: PhaseToolsCompleted, Iteration: turn + 1, Model: req.Model})
		if drained := r.tryDrainInjections(ctx, maxInjectionsPerTurn, maxInjectionCycles, &injectionCycles); drained.Count > 0 {
			result.HadInjections = true
		}
		decision := guard.Observe(executed, nil)
		if decision.Action == loopGuardRecover {
			if r.AppendRuntimeFeedback == nil {
				decision.Action = loopGuardAbort
				decision.AbortReason = "loop guard recovery was requested but runtime feedback is unavailable"
			} else {
				feedback := protocol.NewTextMessage(protocol.RoleUser, decision.Feedback)
				r.AppendRuntimeFeedback(feedback)
				r.emitPhase(PhaseEvent{
					Phase:        PhaseRecoveryAttempt,
					Iteration:    turn + 1,
					Model:        req.Model,
					Message:      "loop_guard_recovery: " + decision.Summary(),
					ToolID:       decision.Tool.ID,
					ToolName:     decision.Tool.Name,
					RecoveryHint: decision.RecoveryHint,
				})
				continue
			}
		}
		if decision.Action == loopGuardAbort {
			result.Stopped = true
			result.RecoveryHint = strings.TrimSpace(diagnosticRecoveryHint(turn+1, executed) + " " + decision.RecoveryHint)
			return result, fmt.Errorf("%w: %s", ErrRepeatedToolCalls, decision.AbortMessage())
		}
		if toolErr != nil {
			if errors.Is(ctx.Err(), context.Canceled) || errors.Is(toolErr, context.Canceled) || shouldStopAfterTool(toolErr) {
				result.Stopped = true
				result.RecoveryHint = diagnosticRecoveryHint(turn+1, executed)
				return result, toolErr
			}
			r.emitPhase(PhaseEvent{
				Phase:        PhaseRecoveryAttempt,
				Iteration:    turn + 1,
				Model:        req.Model,
				Message:      "tool_error_recovery: " + toolErr.Error(),
				RecoveryHint: "The tool error was appended as a tool result so the model can choose another approach.",
			})
		}
		if r.StopAfterTools != nil && r.StopAfterTools() {
			result.Stopped = true
			return result, nil
		}
	}

	result.RecoveryHint = diagnosticRecoveryHint(maxTurns, nil)
	return result, ErrMaxTurnsReached
}

func (r Runner) emitPhase(event PhaseEvent) {
	if r.OnPhase != nil {
		r.OnPhase(event)
	}
}

func (r Runner) tryDrainInjections(ctx context.Context, limit, maxCycles int, cycles *int) InjectionDrain {
	if r.DrainInjections == nil || r.AppendInjectedMessages == nil || cycles == nil || *cycles >= maxCycles {
		return InjectionDrain{}
	}
	drained, err := r.DrainInjections(ctx, limit)
	if err != nil || len(drained.Messages) == 0 {
		return InjectionDrain{}
	}
	*cycles++
	drained.InjectionCycle = *cycles
	if drained.Count <= 0 {
		drained.Count = len(drained.Messages)
	}
	r.AppendInjectedMessages(drained.Messages)
	r.emitPhase(PhaseEvent{Phase: PhaseInjectionDrained, Iteration: *cycles, Message: drained.Summary})
	return drained
}

func diagnosticRecoveryHint(iteration int, executed []ExecutedTool) string {
	parts := []string{fmt.Sprintf("Runner stopped at iteration %d.", iteration)}
	if len(executed) > 0 {
		last := executed[len(executed)-1]
		parts = append(parts, fmt.Sprintf("Last completed tool: %s.", strings.TrimSpace(last.Name)))
	}
	parts = append(parts, "Use resume or retry from the persisted checkpoint after reviewing the timeline phase events.")
	return strings.Join(parts, " ")
}

// SanitizeMessagesForProvider repairs provider-facing history without mutating
// persisted conversation state.
func SanitizeMessagesForProvider(messages []protocol.APIMessage) []protocol.APIMessage {
	out := make([]protocol.APIMessage, 0, len(messages))
	openTools := map[string]struct{}{}
	for _, msg := range messages {
		hadOpenTools := len(openTools) > 0
		blocks := sanitizeBlocks(msg.Content, openTools, msg.Role == protocol.RoleAssistant)
		if len(blocks) == 0 {
			continue
		}
		role := strings.TrimSpace(msg.Role)
		if role == "" {
			role = protocol.RoleUser
		}
		if hadOpenTools && len(openTools) > 0 && !onlyToolResults(blocks) {
			out = append(out, protocol.APIMessage{Role: protocol.RoleUser, Content: missingToolResultBlocks(openTools, "missing_tool_result_backfilled")})
			clear(openTools)
		}
		if len(out) > 0 && role == protocol.RoleUser && out[len(out)-1].Role == protocol.RoleUser &&
			onlyUserMergeable(out[len(out)-1].Content) && onlyUserMergeable(blocks) {
			out[len(out)-1].Content = append(out[len(out)-1].Content, protocol.TextBlock("\n\n"))
			out[len(out)-1].Content = append(out[len(out)-1].Content, blocks...)
			continue
		}
		apiMessage := protocol.APIMessage{Role: role, Content: blocks}
		if role == protocol.RoleAssistant {
			apiMessage.ReasoningContent = msg.ReasoningContent
		}
		out = append(out, apiMessage)
	}
	if len(openTools) > 0 {
		out = append(out, protocol.APIMessage{Role: protocol.RoleUser, Content: missingToolResultBlocks(openTools, "missing_tool_result_backfilled")})
	}
	return out
}

func sanitizeBlocks(blocks []protocol.Block, openTools map[string]struct{}, assistant bool) []protocol.Block {
	out := make([]protocol.Block, 0, len(blocks))
	for _, block := range blocks {
		switch block.Type {
		case protocol.BlockText:
			if strings.TrimSpace(block.Text) != "" {
				out = append(out, protocol.TextBlock(block.Text))
			}
		case protocol.BlockImage:
			if block.Source != nil {
				out = append(out, block)
			}
		case protocol.BlockToolUse:
			if assistant && strings.TrimSpace(block.ID) != "" && strings.TrimSpace(block.Name) != "" {
				openTools[block.ID] = struct{}{}
				out = append(out, protocol.ToolUseBlock(block.ID, block.Name, block.Input))
			}
		case protocol.BlockToolResult:
			if strings.TrimSpace(block.ToolUseID) == "" {
				continue
			}
			if _, ok := openTools[block.ToolUseID]; !ok {
				continue
			}
			delete(openTools, block.ToolUseID)
			content := block.Content
			if modelcontext.TooLargeForModel(content) {
				content = modelcontext.SummaryJSON(modelcontext.LargeToolResultSummary{
					ToolUseID: block.ToolUseID,
					Bytes:     len([]byte(block.Content)),
					SHA256:    modelcontext.SHA256Hex(block.Content),
					Preview:   modelcontext.TruncatedPreview(block.Content),
				})
			}
			out = append(out, protocol.ToolResultBlock(block.ToolUseID, content))
		}
	}
	return out
}

func onlyUserMergeable(blocks []protocol.Block) bool {
	for _, block := range blocks {
		if block.Type == protocol.BlockToolUse || block.Type == protocol.BlockToolResult {
			return false
		}
	}
	return true
}

func onlyToolResults(blocks []protocol.Block) bool {
	if len(blocks) == 0 {
		return false
	}
	for _, block := range blocks {
		if block.Type != protocol.BlockToolResult {
			return false
		}
	}
	return true
}

func missingToolResultBlocks(openTools map[string]struct{}, status string) []protocol.Block {
	if len(openTools) == 0 {
		return nil
	}
	ids := make([]string, 0, len(openTools))
	for id := range openTools {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	blocks := make([]protocol.Block, 0, len(ids))
	for _, id := range ids {
		blocks = append(blocks, protocol.ToolResultBlock(id, fmt.Sprintf(`{"status":%q,"note":"The original tool result was missing from provider context."}`, status)))
	}
	return blocks
}

type pollingToolRepeatState struct {
	Count    int
	Semantic string
}

func appendRecentToolFingerprints(recent []string, executed []ExecutedTool) []string {
	for _, tool := range executed {
		recent = append(recent, executedToolFingerprint(tool))
		if len(recent) > repeatedToolCycleWindow {
			recent = recent[len(recent)-repeatedToolCycleWindow:]
		}
	}
	return recent
}

func pollingToolInputFingerprint(tool ExecutedTool) string {
	if !isPollingToolCall(tool) {
		return ""
	}
	payload := map[string]interface{}{
		"name":  tool.Name,
		"input": tool.Input,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		data = []byte(fmt.Sprintf("%s:%v", tool.Name, tool.Input))
	}
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum[:])
}

func isPollingToolCall(tool ExecutedTool) bool {
	switch name := strings.ToLower(strings.TrimSpace(tool.Name)); name {
	case "tool_exchange":
		return true
	case "background":
		return isBackgroundPollingToolCall(tool)
	case "subagent":
		return isSubagentPollingToolCall(tool)
	}
	return false
}

func isBackgroundPollingToolCall(tool ExecutedTool) bool {
	if !strings.EqualFold(strings.TrimSpace(tool.Name), "background") {
		return false
	}
	action, _ := tool.Input["action"].(string)
	return strings.EqualFold(strings.TrimSpace(action), "check")
}

func isNoProgressPollingToolCall(tool ExecutedTool) bool {
	if strings.EqualFold(strings.TrimSpace(tool.Name), "background") {
		return isBackgroundPollingToolCall(tool)
	}
	return isSubagentPollingToolCall(tool)
}

func isBenignRepeatableToolCall(tool ExecutedTool) bool {
	if !strings.EqualFold(strings.TrimSpace(tool.Name), "browser") {
		return false
	}
	action, _ := tool.Input["action"].(string)
	if !strings.EqualFold(strings.TrimSpace(action), "wait") {
		return false
	}
	text, _ := tool.Input["text"].(string)
	return strings.TrimSpace(text) == ""
}

func clearBenignRepeatableToolCounts(counts map[string]int) {
	for key := range counts {
		if strings.HasPrefix(key, benignRepeatableToolCountPrefix) {
			delete(counts, key)
		}
	}
}

func isSubagentPollingToolCall(tool ExecutedTool) bool {
	if !strings.EqualFold(strings.TrimSpace(tool.Name), "subagent") {
		return false
	}
	action, _ := tool.Input["action"].(string)
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "status", "logs", "list":
		return true
	default:
		return false
	}
}

func pollingToolSemanticFingerprint(tool ExecutedTool) (string, bool) {
	var payload interface{}
	if err := json.Unmarshal([]byte(tool.Output), &payload); err != nil {
		return executedToolFingerprint(tool), false
	}
	projection, terminal := pollingToolSemanticProjection(payload)
	data, err := json.Marshal(projection)
	if err != nil {
		data = []byte(fmt.Sprintf("%v", projection))
	}
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum[:]), terminal
}

func pollingToolSemanticProjection(value interface{}) (interface{}, bool) {
	switch typed := value.(type) {
	case []interface{}:
		items := make([]interface{}, 0, len(typed))
		allTerminal := len(typed) > 0
		for _, item := range typed {
			projected, terminal := pollingToolSemanticProjection(item)
			items = append(items, projected)
			if !terminal {
				allTerminal = false
			}
		}
		return items, allTerminal
	case map[string]interface{}:
		return pollingToolMapProjection(typed)
	default:
		return value, false
	}
}

func pollingToolMapProjection(payload map[string]interface{}) (map[string]interface{}, bool) {
	projected := map[string]interface{}{}
	copyKey := func(key string) {
		if value, ok := payload[key]; ok {
			projected[key] = value
		}
	}
	for _, key := range []string{
		"status",
		"last_phase",
		"last_tool_id",
		"last_tool_name",
		"progress_count",
		"finished_at",
		"error",
		"count",
		"total",
		"task_id",
		"exit_code",
	} {
		copyKey(key)
	}
	if output, ok := payload["output"].(string); ok && strings.TrimSpace(output) != "" {
		projected["output_sha"] = hashString(output)
	}
	if preview, ok := payload["result_preview"].(string); ok && strings.TrimSpace(preview) != "" {
		projected["result_preview_sha"] = hashString(preview)
	}
	if progress, ok := payload["progress"].([]interface{}); ok {
		projected["progress_count"] = len(progress)
		if len(progress) > 0 {
			if latest, ok := progress[len(progress)-1].(map[string]interface{}); ok {
				for _, key := range []string{"phase", "tool_id", "tool_name", "error", "result"} {
					if value, exists := latest[key]; exists {
						projected["last_progress_"+key] = value
					}
				}
			}
		}
	}
	status, _ := payload["status"].(string)
	return projected, isTerminalPollingStatus(status)
}

func isTerminalPollingStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "canceled", "cancelled", "interrupted", "timeout", "error", "failed", "success":
		return true
	default:
		return false
	}
}

func executedToolFingerprint(tool ExecutedTool) string {
	payload := map[string]interface{}{
		"name":   tool.Name,
		"input":  tool.Input,
		"output": tool.Output,
		"error":  tool.Error,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		data = []byte(fmt.Sprintf("%s:%v:%s:%s", tool.Name, tool.Input, tool.Output, tool.Error))
	}
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum[:])
}

func hashString(value string) string {
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", sum[:])
}

func (r Runner) callModel(ctx context.Context, req protocol.Request) (*protocol.Response, bool, error) {
	if streamer, ok := r.Caller.(StreamCaller); ok {
		resp, err := streamer.Stream(ctx, req, StreamHandler{
			OnTextDelta: r.OnAssistantTextDelta,
			OnThinkingDelta: func(thinking, signature string) {
				if thinking != "" && r.OnAssistantThinkingDelta != nil {
					r.OnAssistantThinkingDelta(thinking)
				}
			},
		})
		return resp, true, err
	}
	resp, err := r.Caller.Call(ctx, req)
	return resp, false, err
}

// ExecuteToolUses runs all tool_use blocks and returns a structured tool_result message.
func ExecuteToolUses(
	ctx context.Context,
	blocks []protocol.Block,
	run func(context.Context, string, map[string]interface{}) (ToolExecutionResult, error),
	onStarted func(protocol.Block),
	onFinished func(ExecutedTool),
) (protocol.Message, []ExecutedTool, error) {
	return ExecuteToolUsesWithFilter(ctx, blocks, run, onStarted, onFinished, nil)
}

type ExecuteToolOptions struct {
	Timeout           time.Duration
	StuckWarningAfter time.Duration
	OnStuck           func(ToolStuckEvent)
}

func ExecuteToolUsesWithFilter(
	ctx context.Context,
	blocks []protocol.Block,
	run func(context.Context, string, map[string]interface{}) (ToolExecutionResult, error),
	onStarted func(protocol.Block),
	onFinished func(ExecutedTool),
	filter ToolResultFilter,
) (protocol.Message, []ExecutedTool, error) {
	return ExecuteToolUsesWithOptions(ctx, blocks, run, onStarted, onFinished, filter, ExecuteToolOptions{})
}

func ExecuteToolUsesWithOptions(
	ctx context.Context,
	blocks []protocol.Block,
	run func(context.Context, string, map[string]interface{}) (ToolExecutionResult, error),
	onStarted func(protocol.Block),
	onFinished func(ExecutedTool),
	filter ToolResultFilter,
	options ExecuteToolOptions,
) (protocol.Message, []ExecutedTool, error) {
	toolUses := protocol.ToolUses(blocks)
	if len(toolUses) == 0 {
		return protocol.Message{}, nil, nil
	}

	resultBlocks := make([]protocol.Block, 0, len(toolUses))
	executed := make([]ExecutedTool, 0, len(toolUses))
	for idx, block := range toolUses {
		if onStarted != nil {
			onStarted(block)
		}
		startedAt := time.Now()
		execution, err := executeToolWithTimeout(ctx, options.Timeout, block.ID, block.Name, block.Input, run, options.StuckWarningAfter, options.OnStuck)
		durationMS := time.Since(startedAt).Milliseconds()
		executedTool := ExecutedTool{
			ID:            block.ID,
			Name:          block.Name,
			Input:         protocol.ToolUseBlock(block.ID, block.Name, block.Input).Input,
			Output:        execution.Output,
			ArtifactPaths: append([]string{}, execution.ArtifactPaths...),
			Code:          strings.TrimSpace(execution.Code),
			RecoveryHint:  strings.TrimSpace(execution.RecoveryHint),
			TimedOut:      execution.TimedOut,
			DurationMS:    durationMS,
		}
		output := execution.Output
		if err != nil {
			executedTool.Error = err.Error()
			output = formatToolFailureOutput(block.Name, err, execution)
			executedTool.Output = output
		}
		if filter != nil {
			filtered := filter(ctx, executedTool)
			filtered.ID = executedTool.ID
			filtered.Name = executedTool.Name
			filtered.Input = protocol.ToolUseBlock(executedTool.ID, executedTool.Name, executedTool.Input).Input
			filtered.Error = executedTool.Error
			executedTool = filtered
			output = executedTool.Output
		}
		resultBlock := protocol.ToolResultBlock(block.ID, output)
		resultBlock.IsError = strings.TrimSpace(executedTool.Error) != ""
		resultBlocks = append(resultBlocks, resultBlock)
		executed = append(executed, executedTool)
		if onFinished != nil {
			onFinished(executedTool)
		}
		if shouldStopAfterTool(err) {
			for _, skipped := range toolUses[idx+1:] {
				resultBlocks = append(resultBlocks, protocol.ToolResultBlock(skipped.ID, skippedToolOutput("skipped_due_to_pending_approval")))
			}
			return protocol.NewMessage(protocol.RoleUser, resultBlocks...), executed, err
		}
	}

	return protocol.NewMessage(protocol.RoleUser, resultBlocks...), executed, nil
}

func skippedToolOutput(status string) string {
	return fmt.Sprintf(`{"status":%q,"note":"Tool execution was skipped because an earlier tool in the same assistant message stopped the conversation."}`, status)
}

func executeToolWithTimeout(
	ctx context.Context,
	timeout time.Duration,
	id string,
	name string,
	input map[string]interface{},
	run func(context.Context, string, map[string]interface{}) (ToolExecutionResult, error),
	stuckWarningAfter time.Duration,
	onStuck func(ToolStuckEvent),
) (ToolExecutionResult, error) {
	if timeout <= 0 {
		return run(ctx, name, input)
	}
	toolCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	type outcome struct {
		result ToolExecutionResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := run(toolCtx, name, input)
		done <- outcome{result: result, err: err}
	}()
	var stuck <-chan time.Time
	if stuckWarningAfter > 0 && stuckWarningAfter < timeout {
		timer := time.NewTimer(stuckWarningAfter)
		defer timer.Stop()
		stuck = timer.C
	}
	select {
	case item := <-done:
		return item.result, item.err
	case <-stuck:
		if onStuck != nil {
			onStuck(ToolStuckEvent{
				ID:        strings.TrimSpace(id),
				Name:      strings.TrimSpace(name),
				Input:     protocol.ToolUseBlock(id, name, input).Input,
				Elapsed:   stuckWarningAfter,
				Timeout:   timeout,
				Threshold: stuckWarningAfter,
			})
		}
		select {
		case item := <-done:
			return item.result, item.err
		case <-toolCtx.Done():
			if errors.Is(toolCtx.Err(), context.DeadlineExceeded) {
				return ToolExecutionResult{
					Code:         "tool_timeout",
					RecoveryHint: recoveryHintForToolFailure(name, "tool_timeout"),
					TimedOut:     true,
				}, fmt.Errorf("tool %q timed out after %s", name, timeout)
			}
			return ToolExecutionResult{}, toolCtx.Err()
		}
	case <-toolCtx.Done():
		if errors.Is(toolCtx.Err(), context.DeadlineExceeded) {
			return ToolExecutionResult{
				Code:         "tool_timeout",
				RecoveryHint: recoveryHintForToolFailure(name, "tool_timeout"),
				TimedOut:     true,
			}, fmt.Errorf("tool %q timed out after %s", name, timeout)
		}
		return ToolExecutionResult{}, toolCtx.Err()
	}
}

func toolStuckWarningAfter(timeout time.Duration) time.Duration {
	if timeout <= 2*time.Second {
		return 0
	}
	half := timeout / 2
	if half > 30*time.Second {
		return 30 * time.Second
	}
	if half < time.Second {
		return time.Second
	}
	return half
}

func formatToolFailureOutput(name string, err error, execution ToolExecutionResult) string {
	code := strings.TrimSpace(execution.Code)
	if code == "" {
		code = classifyToolErrorCode(err)
	}
	hint := strings.TrimSpace(execution.RecoveryHint)
	if hint == "" {
		hint = recoveryHintForToolFailure(name, code)
	}
	payload := map[string]interface{}{
		"status":        "error",
		"code":          code,
		"tool":          strings.TrimSpace(name),
		"error":         strings.TrimSpace(err.Error()),
		"recovery_hint": hint,
	}
	var pending permissionPendingToolError
	if errors.As(err, &pending) {
		code = "permission_pending"
		payload["status"] = "permission_pending"
		payload["code"] = code
		payload["request_id"] = strings.TrimSpace(pending.PendingPermissionRequestID())
		payload["recovery_hint"] = "Wait for approval before retrying this exact tool call. The user can approve once, pattern, timebox, count, or session scope."
	}
	if strings.TrimSpace(execution.Output) != "" {
		payload["output"] = execution.Output
	}
	if len(execution.ArtifactPaths) > 0 {
		payload["artifact_paths"] = append([]string{}, execution.ArtifactPaths...)
	}
	if execution.TimedOut {
		payload["timed_out"] = true
	}
	data, jsonErr := json.Marshal(payload)
	if jsonErr != nil {
		if strings.TrimSpace(execution.Output) != "" {
			return fmt.Sprintf("Error: %v\n%s", err, execution.Output)
		}
		return fmt.Sprintf("Error: %v", err)
	}
	return string(data)
}

func classifyToolErrorCode(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "tool_timeout"
	}
	text := strings.ToLower(strings.TrimSpace(err.Error()))
	switch {
	case strings.Contains(text, "timed out") || strings.Contains(text, "deadline exceeded"):
		return "tool_timeout"
	case strings.Contains(text, "permission"):
		return "permission_denied"
	case strings.Contains(text, "not found") || strings.Contains(text, "unknown"):
		return "tool_unavailable"
	default:
		return "tool_error"
	}
}

func recoveryHintForToolFailure(name, code string) string {
	tool := strings.ToLower(strings.TrimSpace(name))
	if code == "tool_timeout" {
		switch tool {
		case "browser":
			return "Browser action timed out. Consider retrying with a smaller action timeout, using web_fetch/web_search for static content, taking a screenshot, or switching to code/static analysis."
		case "web_fetch", "web_search":
			return "Network lookup timed out. Try a narrower query, fetch a specific URL, or answer from available local context."
		case "bash", "background":
			return "Shell command timed out. Try a narrower command, add command-level timeout, or run as a background task."
		default:
			return "Tool timed out. Try a narrower operation, a cheaper alternative tool, or continue from available context."
		}
	}
	switch tool {
	case "browser":
		return "Browser tool failed. Consider web_fetch/web_search for static pages, a fresh browser snapshot, or a simpler browser action."
	default:
		return "Tool failed. Use the error and any preserved output to choose a safer fallback or ask for clarification."
	}
}

func shouldStopAfterTool(err error) bool {
	if err == nil {
		return false
	}
	var stopper stopAfterToolError
	return errors.As(err, &stopper) && stopper.StopConversationAfterTool()
}
