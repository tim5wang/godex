package conversation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/contracts/protocol"
)

type runnerOptions struct {
	maxTurns                     int
	toolTimeout                  time.Duration
	maxRepeatedTools             int
	maxRepeatedPollingTools      int
	maxStalledTaskPollingTools   int
	maxEmptyResponses            int
	maxLengthRecoveries          int
	maxModelRetries              int
	maxContextOverflowRecoveries int
	maxInjectionsPerTurn         int
	maxInjectionCycles           int
	maxLoopGuardRecoveries       int
	maxNoMutationRounds          int
	loopGuardMode                LoopGuardMode
}

type runnerState struct {
	result                    *Result
	emptyResponses            int
	lengthRecoveries          int
	reasoningLengthRecoveries int
	injectionCycles           int
	reasoningMaxTokens        int
}

func (r Runner) normalizedOptions() runnerOptions {
	options := runnerOptions{
		maxTurns:                     r.MaxTurns,
		toolTimeout:                  r.ToolTimeout,
		maxRepeatedTools:             r.MaxRepeatedTools,
		maxRepeatedPollingTools:      r.MaxRepeatedPollingTools,
		maxStalledTaskPollingTools:   r.MaxStalledTaskPollingTools,
		maxEmptyResponses:            r.MaxEmptyResponses,
		maxLengthRecoveries:          r.MaxLengthRecoveries,
		maxModelRetries:              r.MaxModelRetries,
		maxContextOverflowRecoveries: r.MaxContextOverflowRecoveries,
		maxInjectionsPerTurn:         r.MaxInjectionsPerTurn,
		maxInjectionCycles:           r.MaxInjectionCycles,
		maxLoopGuardRecoveries:       r.MaxLoopGuardRecoveries,
		maxNoMutationRounds:          r.MaxNoMutationRounds,
		loopGuardMode:                r.LoopGuardMode,
	}
	if options.maxTurns <= 0 {
		options.maxTurns = 1
	}
	if options.toolTimeout <= 0 {
		options.toolTimeout = defaultToolTimeout
	}
	if options.maxRepeatedTools <= 0 {
		options.maxRepeatedTools = defaultMaxRepeatedTools
	}
	if options.maxRepeatedPollingTools <= 0 {
		options.maxRepeatedPollingTools = defaultMaxRepeatedPollingTools
	}
	if options.maxStalledTaskPollingTools <= 0 {
		options.maxStalledTaskPollingTools = defaultMaxStalledTaskPollingTools
	}
	if options.maxEmptyResponses <= 0 {
		options.maxEmptyResponses = defaultMaxEmptyResponses
	}
	if options.maxLengthRecoveries <= 0 {
		options.maxLengthRecoveries = defaultMaxLengthRecoveries
	}
	if options.maxModelRetries <= 0 {
		options.maxModelRetries = defaultMaxModelRetries
	}
	if options.maxContextOverflowRecoveries < 0 {
		options.maxContextOverflowRecoveries = 0
	}
	if options.maxInjectionsPerTurn <= 0 {
		options.maxInjectionsPerTurn = defaultMaxInjectionsPerTurn
	}
	if options.maxInjectionCycles <= 0 {
		options.maxInjectionCycles = defaultMaxInjectionCycles
	}
	if options.maxLoopGuardRecoveries <= 0 {
		options.maxLoopGuardRecoveries = defaultMaxLoopGuardRecoveries
	}
	if options.maxNoMutationRounds <= 0 {
		options.maxNoMutationRounds = defaultMaxNoMutationRounds
	}
	if options.loopGuardMode == "" {
		options.loopGuardMode = LoopGuardModeStrict
	}
	return options
}

func (o runnerOptions) newLoopGuard() *loopGuard {
	return newLoopGuard(loopGuardConfig{
		MaxRepeatedTools:           o.maxRepeatedTools,
		MaxRepeatedPollingTools:    o.maxRepeatedPollingTools,
		MaxStalledTaskPollingTools: o.maxStalledTaskPollingTools,
		MaxRecoveries:              o.maxLoopGuardRecoveries,
		MaxNoMutationRounds:        o.maxNoMutationRounds,
		Mode:                       o.loopGuardMode,
	})
}

func (r Runner) buildTurnRequest(ctx context.Context, iteration, reasoningMaxTokens int) (protocol.Request, error) {
	req, err := r.BuildRequest(ctx)
	if err != nil {
		r.emitPhase(PhaseEvent{Phase: PhaseError, Iteration: iteration, Message: err.Error()})
		return protocol.Request{}, err
	}
	if reasoningMaxTokens > 0 && (req.MaxTokens <= 0 || req.MaxTokens > reasoningMaxTokens) {
		req.MaxTokens = reasoningMaxTokens
	}
	req.Messages = SanitizeMessagesForProvider(req.Messages)
	r.emitPhase(PhaseEvent{Phase: PhaseContextSanitized, Iteration: iteration, Model: req.Model})
	r.emitPhase(PhaseEvent{Phase: PhaseModelRequest, Iteration: iteration, Model: req.Model})
	return req, nil
}

func (r Runner) callModelForTurn(ctx context.Context, req protocol.Request, iteration int, options runnerOptions, result *Result) (*protocol.Response, bool, protocol.Request, error) {
	overflowRecoveries := 0
	for attempt := 0; ; attempt++ {
		resp, streamed, callErr := r.callModel(ctx, req)
		if callErr == nil {
			return resp, streamed, req, nil
		}
		if errors.Is(ctx.Err(), context.Canceled) || errors.Is(callErr, context.Canceled) {
			result.Stopped = true
			result.RecoveryHint = diagnosticRecoveryHint(iteration, nil)
			r.emitPhase(PhaseEvent{Phase: PhaseInterrupted, Iteration: iteration, Model: req.Model, Message: callErr.Error(), RecoveryHint: result.RecoveryHint})
			return nil, false, req, callErr
		}
		if IsContextLengthError(callErr) && r.OnContextOverflow != nil && overflowRecoveries < options.maxContextOverflowRecoveries {
			overflowRecoveries++
			r.emitPhase(PhaseEvent{Phase: PhaseRecoveryAttempt, Iteration: iteration, Model: req.Model, Message: fmt.Sprintf("provider context overflow; compacting and retrying (%d/%d): %v", overflowRecoveries, options.maxContextOverflowRecoveries, callErr), RecoveryHint: "The provider rejected the request for exceeding its context window; the runner compacted history and retried."})
			if r.OnContextOverflow(ctx) {
				if rebuilt, rebuildErr := r.BuildRequest(ctx); rebuildErr == nil {
					rebuilt.Messages = SanitizeMessagesForProvider(rebuilt.Messages)
					req = rebuilt
					continue
				}
			}
		}
		class := ClassifyTurnError(callErr)
		if (class == TurnErrorRetryable || class == TurnErrorTransient) && attempt < options.maxModelRetries {
			r.emitPhase(PhaseEvent{Phase: PhaseRecoveryAttempt, Iteration: iteration, Model: req.Model, Message: fmt.Sprintf("model error classified as %s; retrying (%d/%d): %v", class, attempt+1, options.maxModelRetries, callErr), RecoveryHint: diagnosticRecoveryHint(iteration, nil)})
			continue
		}
		result.Stopped = true
		result.RecoveryHint = diagnosticRecoveryHint(iteration, nil)
		r.emitPhase(PhaseEvent{Phase: PhaseError, Iteration: iteration, Model: req.Model, Message: callErr.Error(), RecoveryHint: result.RecoveryHint})
		return nil, false, req, callErr
	}
}

func (r Runner) recoverEmptyResponse(ctx context.Context, req protocol.Request, resp *protocol.Response, iteration int, options runnerOptions, state *runnerState) (bool, error) {
	if resp.StopReason == "length" && strings.TrimSpace(resp.ReasoningContent) != "" && state.reasoningLengthRecoveries < maxReasoningLengthRecoveries {
		state.reasoningLengthRecoveries++
		budget := req.MaxTokens
		if budget <= 0 {
			budget = 4096
		}
		state.reasoningMaxTokens = budget / 2
		r.emitPhase(PhaseEvent{Phase: PhaseRecoveryAttempt, Iteration: iteration, Model: req.Model, Message: fmt.Sprintf("model exhausted output budget on reasoning; requesting direct answer with reduced output budget (%d)", state.reasoningMaxTokens), RecoveryHint: "The provider's reasoning consumed the output token budget; the runner capped the budget and requested a direct, concise answer."})
		if r.AppendInjectedMessages != nil {
			r.AppendInjectedMessages([]protocol.Message{protocol.NewTextMessage(protocol.RoleUser, "Your previous response consumed its entire output token budget on reasoning and produced no answer. The output budget has been reduced. Do NOT use it for reasoning: answer directly with the final result now, in plain text, as concisely as possible.")})
		}
		return true, nil
	}
	if state.emptyResponses < options.maxEmptyResponses {
		state.emptyResponses++
		r.emitPhase(PhaseEvent{Phase: PhaseRecoveryAttempt, Iteration: iteration, Model: req.Model, Message: "empty model response; retrying", RecoveryHint: "Retrying the model request because the provider returned an empty response."})
		return true, nil
	}
	state.result.Stopped = true
	state.result.RecoveryHint = diagnosticRecoveryHint(iteration, nil)
	err := fmt.Errorf("LLM provider returned empty responses %d times in a row; aborting instead of producing a fake handoff (provider/gateway may reject the request shape, the model may have exhausted its output budget on reasoning, or the provider is overloaded)", state.emptyResponses)
	r.emitPhase(PhaseEvent{Phase: PhaseError, Iteration: iteration, Model: req.Model, Message: err.Error(), RecoveryHint: state.result.RecoveryHint})
	if drained := r.tryDrainInjections(ctx, options.maxInjectionsPerTurn, options.maxInjectionCycles, &state.injectionCycles); drained.Count > 0 {
		state.result.HadInjections = true
		return true, nil
	}
	return false, err
}

func (r Runner) appendAssistantResponse(message protocol.Message, streamed bool, result *Result) {
	if len(message.Content) == 0 {
		return
	}
	if r.AppendAssistant != nil {
		r.AppendAssistant(message)
	}
	text := protocol.MessageText(message)
	if text == "" {
		return
	}
	result.LastAssistantText = text
	if !streamed && r.OnAssistantTextDelta != nil {
		r.OnAssistantTextDelta(text)
	}
	if r.OnAssistantText != nil {
		r.OnAssistantText(text)
	}
}

func (r Runner) handleFinalResponse(ctx context.Context, req protocol.Request, resp *protocol.Response, assistant protocol.Message, iteration int, options runnerOptions, state *runnerState) (bool, bool) {
	if resp.StopReason == "length" && strings.TrimSpace(protocol.MessageText(assistant)) != "" && state.lengthRecoveries < options.maxLengthRecoveries {
		state.lengthRecoveries++
		r.emitPhase(PhaseEvent{Phase: PhaseRecoveryAttempt, Iteration: iteration, Model: req.Model, Message: "model response reached length limit; requesting continuation", RecoveryHint: "The provider stopped because of the output length limit; the runner requested continuation."})
		if r.AppendInjectedMessages != nil {
			r.AppendInjectedMessages([]protocol.Message{protocol.NewTextMessage(protocol.RoleUser, "Continue exactly where the previous response stopped. Do not repeat completed content.")})
		}
		return false, true
	}
	r.emitPhase(PhaseEvent{Phase: PhaseFinalResponse, Iteration: iteration, Model: req.Model})
	if drained := r.tryDrainInjections(ctx, options.maxInjectionsPerTurn, options.maxInjectionCycles, &state.injectionCycles); drained.Count > 0 {
		state.result.HadInjections = true
		return false, true
	}
	if r.AfterTurn != nil {
		r.AfterTurn()
	}
	state.result.Completed = true
	return true, false
}

func (r Runner) handleToolResponse(ctx context.Context, req protocol.Request, resp *protocol.Response, iteration int, options runnerOptions, state *runnerState, guard *loopGuard) (bool, error) {
	r.emitPhase(PhaseEvent{Phase: PhaseAwaitingTools, Iteration: iteration, Model: req.Model})
	toolResultMsg, executed, toolErr := ExecuteToolUsesWithOptions(ctx, resp.Content, r.ExecuteTool, r.OnToolStarted, r.OnToolFinished, r.ToolResultFilter, ExecuteToolOptions{
		Timeout: options.toolTimeout, StuckWarningAfter: toolStuckWarningAfter(options.toolTimeout), OnStuck: r.OnToolStuck,
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
	r.emitPhase(PhaseEvent{Phase: PhaseToolsCompleted, Iteration: iteration, Model: req.Model})
	if drained := r.tryDrainInjections(ctx, options.maxInjectionsPerTurn, options.maxInjectionCycles, &state.injectionCycles); drained.Count > 0 {
		state.result.HadInjections = true
	}
	decision := guard.Observe(executed, nil)
	if decision.Action == loopGuardRecover {
		if r.AppendRuntimeFeedback == nil {
			decision.Action = loopGuardAbort
			decision.AbortReason = "loop guard recovery was requested but runtime feedback is unavailable"
		} else {
			r.AppendRuntimeFeedback(protocol.NewTextMessage(protocol.RoleUser, decision.Feedback))
			r.emitPhase(PhaseEvent{Phase: PhaseRecoveryAttempt, Iteration: iteration, Model: req.Model, Message: "loop_guard_recovery: " + decision.Summary(), ToolID: decision.Tool.ID, ToolName: decision.Tool.Name, RecoveryHint: decision.RecoveryHint})
			return false, nil
		}
	}
	if decision.Action == loopGuardAbort {
		state.result.Stopped = true
		state.result.RecoveryHint = strings.TrimSpace(diagnosticRecoveryHint(iteration, executed) + " " + decision.RecoveryHint)
		return false, fmt.Errorf("%w: %s", ErrRepeatedToolCalls, decision.AbortMessage())
	}
	if toolErr != nil {
		if errors.Is(ctx.Err(), context.Canceled) || errors.Is(toolErr, context.Canceled) || shouldStopAfterTool(toolErr) {
			state.result.Stopped = true
			state.result.RecoveryHint = diagnosticRecoveryHint(iteration, executed)
			return false, toolErr
		}
		r.emitPhase(PhaseEvent{Phase: PhaseRecoveryAttempt, Iteration: iteration, Model: req.Model, Message: "tool_error_recovery: " + toolErr.Error(), RecoveryHint: "The tool error was appended as a tool result so the model can choose another approach."})
	}
	if r.StopAfterTools != nil && r.StopAfterTools() {
		state.result.Stopped = true
		return true, nil
	}
	return false, nil
}
