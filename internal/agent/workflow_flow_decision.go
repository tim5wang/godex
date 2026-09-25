package agent

import (
	"context"
	"encoding/json"
	"time"

	"github.com/tim5wang/godex/internal/core/conversation"
	"github.com/tim5wang/godex/internal/core/decision"
)

// decisionUsageContext builds the metering context for a decision-model call.
// Decision calls are metered independently (kind=decision, P1.6): they never
// enter the session transcript and never consume the context budget, but they
// DO produce a usage record so run-level rollups can single them out. The
// session id is carried for attribution; the kind marks the record.
func (a *Agent) decisionUsageContext(ctx context.Context, state *workflowState) context.Context {
	if state == nil {
		return ctx
	}
	base, _ := conversation.UsageContextFromContext(ctx)
	base.Kind = "decision"
	if base.SessionID == "" {
		base.SessionID = state.Summary.SessionID
	}
	if base.JobID == "" {
		base.JobID = state.Summary.ID
	}
	return conversation.WithUsageContext(ctx, base)
}

// executeWorkflowDecision runs a decision node synchronously: it calls the
// low-cost decision Caller, records the structured verdict and completes the
// node (no subagent job, no transcript). Failures are retried per the node
// RetryPolicy and then routed per the spec's on_error mode.
func (a *Agent) executeWorkflowDecision(ctx context.Context, state *workflowState, node *workflowNode) {
	spec := node.DecisionSpec
	if spec == nil {
		a.failWorkflowDecisionNode(state, node, "decision node missing spec")
		return
	}
	caller := a.activeDecisionCaller()
	if caller == nil {
		a.handleWorkflowDecisionFailure(state, node, decision.ErrNoCaller)
		return
	}
	// P1.6: decision calls are metered independently (kind=decision). Carry
	// the session/workflow attribution so the usage record can be rolled up
	// per run while staying out of transcript and context budget.
	ctx = a.decisionUsageContext(ctx, state)
	choices := make([]decision.Choice, 0, len(spec.Choices))
	for _, ch := range spec.Choices {
		choices = append(choices, decision.Choice{ID: ch.ID, Label: ch.Label})
	}
	var timeout time.Duration
	if spec.TimeoutMS > 0 {
		timeout = time.Duration(spec.TimeoutMS) * time.Millisecond
	}
	res, err := caller.Decide(ctx, decision.Request{
		Provider:     spec.Provider,
		Question:     node.Prompt,
		DecisionType: spec.DecisionType,
		Choices:      choices,
		Timeout:      timeout,
	})
	if err != nil {
		a.handleWorkflowDecisionFailure(state, node, err)
		return
	}
	a.completeWorkflowDecision(state, node, res, "")
}

// handleWorkflowDecisionFailure applies RetryPolicy first, then on_error.
func (a *Agent) handleWorkflowDecisionFailure(state *workflowState, node *workflowNode, callErr error) {
	executedAttempts := node.Attempt
	if executedAttempts <= 0 {
		executedAttempts = nextWorkflowAttempt(*node)
	}
	kind := classifyDecisionFailure(callErr)
	if retry, delay := shouldWorkflowRetry(node.Retry, executedAttempts, kind); retry {
		a.scheduleWorkflowNodeRetry(state, node, executedAttempts, callErr.Error(), kind, delay)
		return
	}
	spec := node.DecisionSpec
	mode := decisionOnErrorFailClosed
	if spec != nil && spec.OnError != "" {
		mode = spec.OnError
	}
	errText := callErr.Error()
	switch mode {
	case decisionOnErrorFailOpen:
		choice := ""
		if spec != nil {
			choice = spec.DefaultChoice
		}
		a.completeWorkflowDecision(state, node, decision.Result{Choice: choice, Confidence: 0}, errText)
	case decisionOnErrorFail:
		a.failWorkflowDecisionNode(state, node, errText)
	default: // fail_closed: route to the llm fallback via the reserved choice.
		a.completeWorkflowDecision(state, node, decision.Result{Choice: decisionErrorChoice, Confidence: 0}, errText)
	}
}

// failWorkflowDecisionNode terminates a decision node as error (on_error=fail
// or invalid spec).
func (a *Agent) failWorkflowDecisionNode(state *workflowState, node *workflowNode, message string) {
	now := time.Now().UTC()
	node.Status = workflowStatusError
	node.Attempt = nextWorkflowAttempt(*node)
	node.Error = message
	node.JobID = ""
	node.UpdatedAt = now
	node.FinishedAt = now
	if handoffErr := a.finalizeWorkflowNodeHandoff(state, node, nil, ""); handoffErr != nil {
		node.Error = handoffErr.Error()
	}
	_ = a.workflows.appendEvent(state.Summary.ID, map[string]interface{}{
		"event":   "decision_failed",
		"node_id": node.ID,
		"error":   message,
		"at":      now,
	})
}

// completeWorkflowDecision records a successful (or fail_open/fail_closed
// synthetic) decision verdict and finalizes the node handoff.
func (a *Agent) completeWorkflowDecision(state *workflowState, node *workflowNode, res decision.Result, errText string) {
	now := time.Now().UTC()
	result := &workflowDecisionResult{
		Choice:     res.Choice,
		Confidence: res.Confidence,
		Calibrated: res.Calibrated,
		Score:      res.Score,
		Model:      res.Model,
		LatencyMS:  res.Latency.Milliseconds(),
		Error:      errText,
		Raw:        res.Raw,
	}
	node.Status = workflowStatusCompleted
	node.Attempt = nextWorkflowAttempt(*node)
	node.Decision = result
	outputs := res.Outputs()
	if errText != "" {
		outputs["error"] = errText
	}
	node.Outputs = outputs
	node.Error = ""
	node.JobID = ""
	node.UpdatedAt = now
	node.FinishedAt = now
	if res.Choice == decisionErrorChoice {
		// fail_closed: semantic "blocked, needs llm/manual fallback".
		node.Verdict = workflowVerdictBlocked
	}
	payload, _ := json.Marshal(result)
	node.ResultPreview = previewSubagentResultForModel(string(payload))
	if err := a.finalizeWorkflowNodeHandoff(state, node, nil, string(payload)); err != nil {
		node.Status = workflowStatusError
		node.Error = err.Error()
		return
	}
	event := map[string]interface{}{
		"event":      "decision_made",
		"node_id":    node.ID,
		"choice":     result.Choice,
		"confidence": result.Confidence,
		"provider":   result.Model,
		"latency_ms": result.LatencyMS,
		"at":         now,
	}
	if errText != "" {
		event["event"] = "decision_fallback"
		event["error"] = errText
	}
	_ = a.workflows.appendEvent(state.Summary.ID, event)
	// Unified observability event (debug log: one line per node done + latency).
	_ = a.workflows.appendEvent(state.Summary.ID, map[string]interface{}{
		"event":      "node_completed",
		"node_id":    node.ID,
		"latency_ms": a.workflowNodeLatency(*node, now),
		"at":         now,
	})
}

// scheduleWorkflowNodeRetry returns a failed node to pending with a backoff
// gate. No handoff is written for the failed attempt; the next attempt gets a
// fresh execution.
func (a *Agent) scheduleWorkflowNodeRetry(state *workflowState, node *workflowNode, executedAttempts int, reason, kind string, delay time.Duration) {
	now := time.Now().UTC()
	node.Status = workflowStatusPending
	node.JobID = ""
	node.Error = ""
	node.Verdict = ""
	node.HandoffRef = ""
	node.HandoffDigest = ""
	node.ArtifactRefs = nil
	node.FinishedAt = time.Time{}
	node.Attempt = executedAttempts
	node.NextRetryAt = now.Add(delay)
	node.UpdatedAt = now
	_ = a.workflows.appendEvent(state.Summary.ID, map[string]interface{}{
		"event":       "node_retry",
		"node_id":     node.ID,
		"attempt":     executedAttempts + 1,
		"reason":      reason,
		"failure":     kind,
		"wait_ms":     delay.Milliseconds(),
		"retry_after": node.NextRetryAt,
		"at":          now,
	})
}

// workflowNodeRetryDue gates pending nodes waiting on a retry backoff.
func workflowNodeRetryDue(node *workflowNode, now time.Time) bool {
	return node.NextRetryAt.IsZero() || !now.Before(node.NextRetryAt)
}
