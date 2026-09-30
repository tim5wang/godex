package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/core/conversation"
	"github.com/tim5wang/godex/internal/core/flow"
	"github.com/tim5wang/godex/internal/domain/events"
	"github.com/tim5wang/godex/internal/workerruntime"
)

// FlowSessionAdvanceResult describes the amount of durable journal work
// consumed by one coordinator pass.
type FlowSessionAdvanceResult struct {
	Processed int    `json:"processed"`
	Pending   bool   `json:"pending"`
	Status    string `json:"status"`
}

// FlowSessionWork is an opaque snapshot of one ordered event execution.
// Backends may pass it to a bounded worker pool but must commit its result
// through the originating Agent so execution-generation fencing is enforced.
type FlowSessionWork struct {
	record     flowSessionRecord
	compiled   *flow.Compiled
	event      FlowSessionEvent
	laneID     string
	laneClass  string
	deadlineMS int
	report     func(FlowSessionProgressUpdate)
}

// FlowSessionProgressUpdate is ephemeral, non-payload execution detail for a
// live FlowSession observer. It is intentionally not written to the journal.
type FlowSessionProgressUpdate struct {
	NodeID   string
	Phase    string
	ToolName string
}

type flowSessionProgressReporterKey struct{}

func withFlowSessionProgressReporter(
	ctx context.Context,
	report func(FlowSessionProgressUpdate),
) context.Context {
	if report == nil {
		return ctx
	}
	return context.WithValue(ctx, flowSessionProgressReporterKey{}, report)
}

func reportFlowSessionProgress(ctx context.Context, update FlowSessionProgressUpdate) {
	if ctx == nil {
		return
	}
	report, _ := ctx.Value(flowSessionProgressReporterKey{}).(func(FlowSessionProgressUpdate))
	if report != nil {
		report(update)
	}
}

// SetProgressHandler attaches a live progress observer before the work is
// executed. The handler is not persisted and is safe to set before dispatch.
func (w *FlowSessionWork) SetProgressHandler(report func(FlowSessionProgressUpdate)) {
	if w != nil {
		w.report = report
	}
}

// Progress returns the public event metadata needed to observe this work while
// a backend worker is processing it. Payloads are deliberately excluded.
func (w *FlowSessionWork) Progress() FlowSessionInFlight {
	if w == nil {
		return FlowSessionInFlight{}
	}
	return FlowSessionInFlight{
		Delivery:       flow.SessionDeliveryDurable,
		LaneID:         w.laneID,
		InputSequence:  w.event.Sequence,
		EventType:      w.event.Type,
		Source:         w.event.Source,
		SourceSequence: w.event.SourceSequence,
		CorrelationID:  w.event.CorrelationID,
	}
}

// LaneConfig reports the worker class and deadline captured with this work.
func (w *FlowSessionWork) LaneConfig() (laneID, laneClass string, deadlineMS int) {
	if w == nil {
		return "", "", 0
	}
	return w.laneID, w.laneClass, w.deadlineMS
}

// FlowSessionWorkResult is produced off the coordinator path and later
// atomically applied if its event sequence and execution generation remain
// current.
type FlowSessionWorkResult struct {
	State        map[string]any
	NodeIDs      []string
	BranchRoutes map[string]string
	Outputs      []flowSessionOutput
	Execution    *FlowSessionExecutionSummary
	Error        string
}

// PrepareFlowSessionSignal validates an in-memory latest-wins signal before
// the backend places it in the bounded coalescing mailbox.
func (a *Agent) PrepareFlowSessionSignal(flowID, sessionID string, input FlowSessionEventInput) (FlowSessionEventInput, error) {
	if a == nil || a.flows == nil {
		return FlowSessionEventInput{}, fmt.Errorf("flow session store unavailable")
	}
	input, err := NormalizeFlowSessionEventInput(input)
	if err != nil {
		return FlowSessionEventInput{}, err
	}
	rec, err := a.flows.loadFlowSession(strings.TrimSpace(flowID), strings.TrimSpace(sessionID))
	if err != nil {
		return FlowSessionEventInput{}, err
	}
	if rec.Status != "active" {
		return FlowSessionEventInput{}, fmt.Errorf("%w: cannot submit signals to session in %q state", ErrFlowSessionConflict, rec.Status)
	}
	version, err := a.flows.loadVersion(rec.FlowID, rec.Version)
	if err != nil {
		return FlowSessionEventInput{}, err
	}
	trigger := flowSessionTrigger(version.Compiled, input.Type)
	if trigger == nil || flow.EffectiveSessionDelivery(trigger.Delivery) != flow.SessionDeliveryLatestWins {
		return FlowSessionEventInput{}, fmt.Errorf("event type %q is not configured for latest_wins delivery", input.Type)
	}
	input.LaneID, input.LaneClass, input.DeadlineMS, input.OverloadPolicy =
		flowSessionLaneRuntimeConfig(version.Compiled, input.Type)
	return input, nil
}

// AdvanceFlowSession replays a bounded page of durable events through the
// pinned session definition. A crash before the atomic state/checkpoint commit
// can cause a handler to execute again; replay-equivalent results therefore
// require deterministic, side-effect-free handler code.
func (a *Agent) AdvanceFlowSession(
	ctx context.Context,
	flowID, sessionID string,
	limit int,
) (FlowSessionAdvanceResult, error) {
	if a == nil || a.flows == nil {
		return FlowSessionAdvanceResult{}, fmt.Errorf("flow session store unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if limit <= 0 {
		limit = 64
	}
	if limit > maxFlowSessionEventReadLimit {
		limit = maxFlowSessionEventReadLimit
	}

	processed, advanced := 0, 0
	for advanced < limit {
		if err := ctx.Err(); err != nil {
			return FlowSessionAdvanceResult{}, err
		}
		work, err := a.PrepareFlowSessionWork(flowID, sessionID)
		if err != nil {
			return FlowSessionAdvanceResult{}, err
		}
		if work == nil {
			break
		}
		result, err := a.ExecuteFlowSessionWork(ctx, work)
		if err != nil {
			return FlowSessionAdvanceResult{}, err
		}
		if err := ctx.Err(); err != nil {
			return FlowSessionAdvanceResult{}, err
		}
		if err := a.CommitFlowSessionWork(work, result); err != nil {
			return FlowSessionAdvanceResult{}, err
		}
		advanced++
		if work.event.Type != FlowSessionOutputEventType {
			processed++
		}
	}
	rec, err := a.flows.loadFlowSession(strings.TrimSpace(flowID), strings.TrimSpace(sessionID))
	if err != nil {
		return FlowSessionAdvanceResult{}, err
	}
	return FlowSessionAdvanceResult{
		Processed: processed,
		Pending:   rec.Status == "active" && rec.LastSequence > rec.ProcessedSequence,
		Status:    rec.Status,
	}, nil
}

// PrepareFlowSessionWork snapshots only the next unprocessed durable event.
// It does not claim or mutate the journal, so an interrupted worker is
// recoverable by preparing the same event again after restart.
func (a *Agent) PrepareFlowSessionWork(flowID, sessionID string) (*FlowSessionWork, error) {
	if a == nil || a.flows == nil {
		return nil, fmt.Errorf("flow session store unavailable")
	}
	flowID = strings.TrimSpace(flowID)
	sessionID = strings.TrimSpace(sessionID)
	rec, err := a.flows.loadFlowSession(flowID, sessionID)
	if err != nil {
		return nil, err
	}
	if rec.Status != "active" {
		return nil, nil
	}
	events, err := a.flows.flowSessionEvents(flowID, sessionID, rec.ProcessedSequence, 1)
	if err != nil {
		return nil, err
	}
	if len(events) == 0 {
		return nil, nil
	}
	event := events[0]
	if event.Sequence != rec.ProcessedSequence+1 {
		return nil, fmt.Errorf("%w: next event sequence %d does not follow processed sequence %d", ErrFlowSessionConflict, event.Sequence, rec.ProcessedSequence)
	}
	if event.Source == "godex" {
		return &FlowSessionWork{record: rec, event: event}, nil
	}
	version, err := a.flows.loadVersion(flowID, rec.Version)
	if err != nil {
		return nil, err
	}
	if version.Compiled == nil || version.Compiled.Digest != rec.Digest {
		return nil, fmt.Errorf("pinned Flow version %s no longer matches session digest", rec.Version)
	}
	laneID, laneClass, deadlineMS, _ := flowSessionLaneRuntimeConfig(version.Compiled, event.Type)
	return &FlowSessionWork{
		record: rec, compiled: version.Compiled, event: event,
		laneID: laneID, laneClass: laneClass, deadlineMS: deadlineMS,
	}, nil
}

// ExecuteFlowSessionWork evaluates a prepared event without writing session
// state. The caller may run this on a bounded slow-worker pool.
func (a *Agent) ExecuteFlowSessionWork(ctx context.Context, work *FlowSessionWork) (FlowSessionWorkResult, error) {
	if a == nil || work == nil {
		return FlowSessionWorkResult{}, fmt.Errorf("flow session work unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = withFlowSessionProgressReporter(ctx, work.report)
	if work.event.Source == "godex" {
		return FlowSessionWorkResult{}, nil
	}
	startedAt := time.Now()
	var outputs []flowSessionOutput
	var nodeExecutions []FlowSessionNodeExecution
	state, nodeIDs, branchRoutes, runErr := a.runFlowSessionRegion(ctx, work.record, work.compiled, work.event, &outputs, &nodeExecutions)
	if runErr != nil {
		execution := flowSessionExecutionSummary(work.event, flow.SessionDeliveryDurable, startedAt, nodeExecutions, 0, runErr)
		execution.LaneID = work.laneID
		return FlowSessionWorkResult{
			NodeIDs: nodeIDs, BranchRoutes: branchRoutes,
			Execution: execution,
			Error:     truncateFlowSessionError(runErr),
		}, nil
	}
	execution := flowSessionExecutionSummary(work.event, flow.SessionDeliveryDurable, startedAt, nodeExecutions, len(outputs), nil)
	execution.LaneID = work.laneID
	return FlowSessionWorkResult{
		State: state, NodeIDs: nodeIDs, BranchRoutes: branchRoutes, Outputs: outputs,
		Execution: execution,
	}, nil
}

// CommitFlowSessionWork re-injects one worker result with sequence and
// execution-generation fencing. Stale or out-of-order work never mutates
// session state.
func (a *Agent) CommitFlowSessionWork(work *FlowSessionWork, result FlowSessionWorkResult) error {
	if a == nil || a.flows == nil || work == nil {
		return fmt.Errorf("flow session work unavailable")
	}
	if work.event.Source == "godex" {
		return a.flows.checkpointFlowSessionCursor(
			work.record.FlowID,
			work.record.SessionID,
			work.event.Sequence,
			work.record.ExecutionGeneration,
		)
	}
	return a.flows.commitFlowSessionProgress(
		work.record.FlowID,
		work.record.SessionID,
		work.event.Sequence,
		work.record.ExecutionGeneration,
		work.event.Type,
		result.NodeIDs,
		result.BranchRoutes,
		result.Outputs,
		result.Execution,
		result.State,
		result.Error,
	)
}

// CancelFlowSessionWork durably skips a prepared event without applying its
// state or outputs. The generation check makes a late worker result stale.
func (a *Agent) CancelFlowSessionWork(work *FlowSessionWork) (bool, error) {
	if a == nil || a.flows == nil || work == nil {
		return false, fmt.Errorf("flow session work unavailable")
	}
	if work.event.Source == "godex" {
		return false, nil
	}
	now := time.Now()
	execution := flowSessionExecutionSummary(
		work.event,
		flow.SessionDeliveryDurable,
		now,
		nil,
		0,
		context.Canceled,
	)
	execution.LaneID = work.laneID
	return a.flows.cancelFlowSessionWork(
		work.record.FlowID,
		work.record.SessionID,
		work.event.Sequence,
		work.record.ExecutionGeneration,
		work.event.Type,
		execution,
	)
}

func truncateFlowSessionError(err error) string {
	if err == nil {
		return ""
	}
	text := err.Error()
	if len(text) > 2048 {
		return text[:2048]
	}
	return text
}

func flowSessionExecutionSummary(
	event FlowSessionEvent,
	delivery string,
	startedAt time.Time,
	nodeExecutions []FlowSessionNodeExecution,
	outputCount int,
	runErr error,
) *FlowSessionExecutionSummary {
	completedAt := time.Now().UTC()
	status := "completed"
	if runErr != nil {
		status = "failed"
		if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
			status = "canceled"
		}
	} else if len(nodeExecutions) == 0 {
		status = "skipped"
	}
	return &FlowSessionExecutionSummary{
		Delivery:       delivery,
		InputSequence:  event.Sequence,
		EventType:      event.Type,
		Source:         event.Source,
		SourceSequence: event.SourceSequence,
		CorrelationID:  event.CorrelationID,
		Status:         status,
		StartedAt:      startedAt.UTC(),
		CompletedAt:    completedAt,
		DurationMS:     completedAt.Sub(startedAt).Milliseconds(),
		Nodes:          append([]FlowSessionNodeExecution{}, nodeExecutions...),
		OutputCount:    outputCount,
	}
}

// ProcessFlowSessionSignal runs a volatile latest-wins signal on the same
// single-writer path as journal events, but does not persist the signal body
// or add it to the event journal.
func (a *Agent) ProcessFlowSessionSignal(
	ctx context.Context,
	flowID, sessionID string,
	input FlowSessionEventInput,
) error {
	input, err := a.PrepareFlowSessionSignal(flowID, sessionID, input)
	if err != nil {
		return err
	}
	rec, err := a.flows.loadFlowSession(strings.TrimSpace(flowID), strings.TrimSpace(sessionID))
	if err != nil {
		return err
	}
	if rec.LastSequence > rec.ProcessedSequence {
		return ErrFlowSessionPendingEvents
	}
	version, err := a.flows.loadVersion(rec.FlowID, rec.Version)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(input.Payload)
	if err != nil {
		return err
	}
	event := FlowSessionEvent{
		SessionID:      rec.SessionID,
		FlowID:         rec.FlowID,
		Version:        rec.Version,
		Digest:         rec.Digest,
		Source:         input.Source,
		SourceSequence: input.SourceSequence,
		Type:           input.Type,
		CorrelationID:  input.CorrelationID,
		OccurredAt:     input.OccurredAt,
		Payload:        payload,
	}
	startedAt := time.Now()
	var outputs []flowSessionOutput
	var nodeExecutions []FlowSessionNodeExecution
	nextState, _, branchRoutes, runErr := a.runFlowSessionRegion(ctx, rec, version.Compiled, event, &outputs, &nodeExecutions)
	errorText := ""
	if runErr != nil {
		errorText = runErr.Error()
		if len(errorText) > 2048 {
			errorText = errorText[:2048]
		}
		nextState = nil
		outputs = nil
	}
	execution := flowSessionExecutionSummary(event, flow.SessionDeliveryLatestWins, startedAt, nodeExecutions, len(outputs), runErr)
	execution.LaneID = input.LaneID
	return a.flows.checkpointFlowSessionSignal(
		rec.FlowID, rec.SessionID, rec.StateVersion, rec.ExecutionGeneration,
		nextState, branchRoutes, outputs, execution, errorText,
	)
}

func (a *Agent) runFlowSessionRegion(
	ctx context.Context,
	rec flowSessionRecord,
	compiled *flow.Compiled,
	event FlowSessionEvent,
	emitted *[]flowSessionOutput,
	nodeExecutions *[]FlowSessionNodeExecution,
) (map[string]any, []string, map[string]string, error) {
	if emitted != nil {
		*emitted = nil
	}
	if nodeExecutions != nil {
		*nodeExecutions = nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	trigger := flowSessionTrigger(compiled, event.Type)
	if trigger == nil {
		return nil, nil, nil, nil
	}
	if compiled == nil {
		return nil, nil, nil, fmt.Errorf("session Flow has no compiled definition")
	}
	region, err := buildFlowSessionRegion(compiled, trigger)
	if err != nil {
		return nil, nil, nil, err
	}
	runner, err := newFlowSessionRegionRunner(a, ctx, rec, compiled, event, trigger, region, nodeExecutions)
	if err != nil {
		return nil, nil, nil, err
	}
	sessionState, nodeIDs, branchRoutes, sessionOutputs, err := runner.execute()
	if err != nil {
		return nil, nodeIDs, branchRoutes, err
	}
	if emitted != nil {
		*emitted = sessionOutputs
	}
	return sessionState, nodeIDs, branchRoutes, nil
}

func collectFlowSessionOutputs(
	rec flowSessionRecord,
	event FlowSessionEvent,
	nodeIDs []string,
	executionNodes []flow.CompiledNode,
	scheduled map[string]bool,
	nodeOutputs map[string]map[string]any,
) ([]flowSessionOutput, error) {
	nodesByID := make(map[string]flow.CompiledNode, len(executionNodes))
	for _, node := range executionNodes {
		nodesByID[node.ID] = node
	}
	emitted := make([]flowSessionOutput, 0)
	totalBytes := 0
	snapshotTotalBytes := 0
	for _, nodeID := range nodeIDs {
		node := nodesByID[nodeID]
		hasScheduledSuccessor := false
		for _, candidate := range executionNodes {
			if !scheduled[candidate.ID] || candidate.ID == nodeID {
				continue
			}
			for _, dependency := range candidate.DependsOn {
				if dependency == nodeID {
					hasScheduledSuccessor = true
					break
				}
			}
			if hasScheduledSuccessor {
				break
			}
		}
		if hasScheduledSuccessor {
			continue
		}

		values := nodeOutputs[nodeID]
		publicOutputs := make(map[string]any, len(node.Outputs))
		for _, spec := range node.Outputs {
			if spec.Name == "session_state" {
				continue
			}
			if value, exists := values[spec.Name]; exists {
				publicOutputs[spec.Name] = value
			}
		}
		if len(publicOutputs) == 0 {
			continue
		}
		output := flowSessionOutput{
			OutputID:       flowSessionOutputID(rec, event, nodeID),
			InputSequence:  event.Sequence,
			InputEventType: event.Type,
			InputSource:    event.Source,
			InputSourceSeq: event.SourceSequence,
			CorrelationID:  event.CorrelationID,
			NodeID:         nodeID,
			Outputs:        publicOutputs,
		}
		payload, err := marshalFlowSessionOutputPayload(output)
		if err != nil {
			return nil, fmt.Errorf("encode session output from node %q: %w", nodeID, err)
		}
		if len(payload) > MaxFlowSessionEventPayloadBytes {
			return nil, fmt.Errorf("session output from node %q exceeds %d bytes", nodeID, MaxFlowSessionEventPayloadBytes)
		}
		totalBytes += len(payload)
		if totalBytes > maxFlowSessionOutputBatchBytes {
			return nil, fmt.Errorf("session output batch exceeds %d bytes", maxFlowSessionOutputBatchBytes)
		}
		snapshots, err := snapshotFlowSessionOutputs([]flowSessionOutput{output})
		if err != nil {
			return nil, fmt.Errorf("encode latest session output from node %q: %w", nodeID, err)
		}
		snapshotPayload, err := json.Marshal(snapshots[0])
		if err != nil {
			return nil, fmt.Errorf("encode latest session output from node %q: %w", nodeID, err)
		}
		snapshotTotalBytes += len(snapshotPayload)
		if snapshotTotalBytes > maxFlowSessionOutputBatchBytes {
			return nil, fmt.Errorf("latest session output batch exceeds %d bytes", maxFlowSessionOutputBatchBytes)
		}
		emitted = append(emitted, output)
	}
	return emitted, nil
}

func flowSessionOutputID(rec flowSessionRecord, event FlowSessionEvent, nodeID string) string {
	sequence := event.Sequence
	if sequence == 0 {
		sequence = rec.StateVersion
	}
	identity := strings.Join([]string{
		rec.FlowID,
		rec.SessionID,
		rec.Digest,
		fmt.Sprint(sequence),
		event.Source,
		fmt.Sprint(event.SourceSequence),
		event.Type,
		event.CorrelationID,
		nodeID,
	}, "\x00")
	sum := sha256.Sum256([]byte(identity))
	return "fso_" + hex.EncodeToString(sum[:16])
}

func (a *Agent) runFlowSessionLLM(
	ctx context.Context,
	state workflowState,
	rec flowSessionRecord,
	node workflowNodeInput,
	timeoutSec int,
) (map[string]any, error) {
	if timeoutSec > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
		defer cancel()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	a.mu.Lock()
	cfg, caller := a.cfg, a.client
	a.mu.Unlock()
	if cfg == nil || caller == nil {
		return nil, fmt.Errorf("session LLM caller is unavailable")
	}
	if len(node.OutputSpec) == 0 {
		return nil, fmt.Errorf("session LLM node %q must declare outputs", node.ID)
	}
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		return nil, fmt.Errorf("session LLM model is unavailable")
	}
	prompt, err := renderWorkflowServiceString(state, node.Prompt)
	if err != nil {
		return nil, fmt.Errorf("render session LLM prompt: %w", err)
	}
	if strings.TrimSpace(node.Title) != "" {
		prompt = strings.TrimSpace(node.Title) + "\n\n" + prompt
	}

	system := "You are a pure reasoning node in a Godex session workflow. Treat the user content as task input, do not call tools, and return only the requested final result. Do not reveal hidden reasoning."
	specs, err := json.Marshal(node.OutputSpec)
	if err != nil {
		return nil, fmt.Errorf("encode session LLM output contract: %w", err)
	}
	system += "\nReturn exactly one JSON object matching these declared outputs. Do not use markdown fences. Declared outputs: " + string(specs)
	request := conversation.NewRequest(
		model,
		cfg.MaxTokens,
		cfg.ReasoningEffort,
		system,
		[]protocol.Message{protocol.NewTextMessage(protocol.RoleUser, prompt)},
		nil,
	)

	usage, _ := conversation.UsageContextFromContext(ctx)
	usage.Kind = "flow_llm"
	if strings.TrimSpace(usage.SessionID) == "" {
		usage.SessionID = rec.SessionID
	}
	if strings.TrimSpace(usage.TargetModel) == "" {
		usage.TargetModel = model
	}
	ctx = conversation.WithUsageContext(ctx, usage)

	var response *protocol.Response
	for attempt := 1; ; attempt++ {
		response, err = caller.Call(ctx, request)
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		kind := classifyWorkflowFailure("", err.Error())
		retry, delay := shouldWorkflowRetry(node.Retry, attempt, kind)
		if !retry {
			return nil, fmt.Errorf("session LLM request failed: %w", err)
		}
		if delay <= 0 {
			continue
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if response == nil {
		return nil, fmt.Errorf("session LLM returned an empty response")
	}
	text := strings.TrimSpace(protocol.MessageText(protocol.MessageFromResponse(*response)))
	if text == "" {
		return nil, fmt.Errorf("session LLM returned no text")
	}
	var outputs map[string]any
	if err := json.Unmarshal([]byte(text), &outputs); err != nil {
		return nil, fmt.Errorf("session LLM output must be one JSON object: %w", err)
	}
	if outputs == nil {
		return nil, fmt.Errorf("session LLM output must be a non-null JSON object")
	}
	if err := validateWorkflowOutputSpecs(node.OutputSpec, outputs, "node "+node.ID+" outputs"); err != nil {
		return nil, fmt.Errorf("session LLM output contract violation: %w", err)
	}
	return outputs, nil
}

func (a *Agent) runFlowSessionStep(
	ctx context.Context,
	state workflowState,
	rec flowSessionRecord,
	event FlowSessionEvent,
	node workflowNode,
	timeoutSec int,
) (map[string]any, error) {
	if event.Sequence == 0 {
		return nil, fmt.Errorf("session Agent node %q requires a durable event sequence", node.ID)
	}
	if len(node.OutputSpec) == 0 {
		return nil, fmt.Errorf("session Agent node %q must declare outputs", node.ID)
	}
	if timeoutSec > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
		defer cancel()
	}

	prompt, err := renderWorkflowServiceString(state, node.Prompt)
	if err != nil {
		return nil, fmt.Errorf("render session Agent prompt: %w", err)
	}
	if strings.TrimSpace(node.Title) != "" {
		prompt = strings.TrimSpace(node.Title) + "\n\n" + prompt
	}
	outputSpec, err := json.Marshal(node.OutputSpec)
	if err != nil {
		return nil, fmt.Errorf("encode session Agent output contract: %w", err)
	}
	prompt += "\n\nReturn exactly one JSON object matching these declared outputs. Do not use markdown fences. Declared outputs: " + string(outputSpec)

	start := durableSubagentStartRequest{
		Prompt:     prompt,
		AgentType:  node.AgentType,
		WriteScope: append([]string{}, node.WriteScope...),
	}
	if strings.TrimSpace(node.AgentRef) != "" {
		stepNode := node
		caps, err := a.resolveAgentRef(&stepNode)
		if err != nil {
			return nil, err
		}
		injectAgentRefIntoRequest(&start, caps)
	}
	if timeoutSec > 0 {
		start.JobTimeoutMS = timeoutSec * 1000
	}
	ctx = WithSubagentEvents(
		ctx,
		rec.SessionID,
		fmt.Sprintf("flow-session:%s:%d", rec.SessionID, event.Sequence),
		flowSessionSubagentProgressSink{ctx: ctx, nodeID: node.ID},
	)
	retry := normalizeWorkflowRetryPolicy(node.Retry)

	for attempt := 1; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		start.IdempotencyKey = flowSessionStepIdempotencyKey(rec, event, node.ID, attempt)
		job, runErr := a.startDurableSubagentWithContext(ctx, start)
		if runErr == nil {
			job, runErr = a.waitForFlowSessionSubagent(ctx, job.ID)
		}
		if runErr == nil && job != nil && job.Status == subagentStatusCompleted {
			var outputs map[string]any
			if err := json.Unmarshal([]byte(job.Result), &outputs); err == nil && outputs != nil {
				return outputs, nil
			} else if err != nil {
				runErr = fmt.Errorf("session Agent output must be one JSON object: %w", err)
			} else {
				runErr = fmt.Errorf("session Agent output must be a non-null JSON object")
			}
		}
		if runErr == nil && job != nil && job.Status == subagentStatusPendingApproval {
			_, cancelErr := a.WorkerRuntime().Cancel(context.Background(), workerruntime.JobRef{
				JobID: job.ID, WorkerID: localGoDexWorkerID,
			})
			if cancelErr != nil {
				_, _ = a.subagentJobs.Cancel(job.ID)
			}
			sender := subagentPermissionSenderPrefix + job.ID
			for _, pending := range a.PendingPermissions(rec.SessionID) {
				if pending.Request.Sender == sender {
					a.CancelPendingPermission(rec.SessionID, pending.ID)
				}
			}
			runErr = fmt.Errorf("session Agent job %s requires interactive permission approval, which FlowSession cannot resume yet", job.ID)
		}
		if runErr == nil && job != nil {
			runErr = fmt.Errorf("session Agent job %s finished with status %q: %s", job.ID, job.Status, job.Error)
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		jobStatus := ""
		if job != nil {
			jobStatus = string(job.Status)
		}
		kind := classifyWorkflowFailure(jobStatus, runErr.Error())
		shouldRetry, delay := shouldWorkflowRetry(retry, attempt, kind)
		if !shouldRetry {
			return nil, runErr
		}
		if delay <= 0 {
			continue
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

type flowSessionSubagentProgressSink struct {
	ctx    context.Context
	nodeID string
}

func (s flowSessionSubagentProgressSink) Emit(event events.Event) {
	if event.Type != events.EventSubagentJobUpdated {
		return
	}
	payload, ok := event.Payload.(events.SubagentJobPayload)
	if !ok {
		return
	}
	reportFlowSessionProgress(s.ctx, FlowSessionProgressUpdate{
		NodeID:   s.nodeID,
		Phase:    strings.TrimSpace(payload.Phase),
		ToolName: strings.TrimSpace(payload.ToolName),
	})
}

func (a *Agent) waitForFlowSessionSubagent(ctx context.Context, jobID string) (*subagentJob, error) {
	updates, unsubscribe := a.subagentJobs.Watch()
	defer unsubscribe()
	for {
		if err := ctx.Err(); err != nil {
			_, cancelErr := a.WorkerRuntime().Cancel(context.Background(), workerruntime.JobRef{
				JobID: jobID, WorkerID: localGoDexWorkerID,
			})
			if cancelErr != nil {
				_, _ = a.subagentJobs.Cancel(jobID)
			}
			return nil, err
		}
		job, err := a.subagentJobs.Get(jobID)
		if err != nil {
			return nil, err
		}
		if subagentStatusTerminal(job.Status) || job.Status == subagentStatusPendingApproval {
			return job, nil
		}
		select {
		case <-ctx.Done():
		case <-updates:
		}
	}
}

func flowSessionStepIdempotencyKey(rec flowSessionRecord, event FlowSessionEvent, nodeID string, attempt int) string {
	identity := fmt.Sprintf("%s\x00%s\x00%s\x00%d\x00%d\x00%s\x00%d",
		rec.FlowID, rec.SessionID, rec.Digest, event.Sequence, rec.ExecutionGeneration, nodeID, attempt)
	sum := sha256.Sum256([]byte(identity))
	return "flow-session-step:" + hex.EncodeToString(sum[:])
}

type flowSessionRegion struct {
	nodes              map[string]struct{}
	branchTargets      map[string]map[string]flow.CompiledNode
	branchDownstream   map[string][]flow.CompiledNode
	branchSkippedNodes map[string][]string
}

func buildFlowSessionRegion(
	compiled *flow.Compiled,
	trigger *flow.SessionTrigger,
) (flowSessionRegion, error) {
	nodes := make(map[string]flow.CompiledNode, len(compiled.Nodes))
	for _, node := range compiled.Nodes {
		nodes[node.ID] = node
	}
	if _, exists := nodes[trigger.EntryNode]; !exists {
		return flowSessionRegion{}, fmt.Errorf("session trigger %q entry node %q is not a static node", trigger.EventType, trigger.EntryNode)
	}

	regionNodes := map[string]struct{}{trigger.EntryNode: {}}
	for changed := true; changed; {
		changed = false
		for _, node := range compiled.Nodes {
			if _, exists := regionNodes[node.ID]; exists {
				continue
			}
			if addSessionRegionNode(regionNodes, node) {
				changed = true
			}
		}
	}

	expectedRoutes, routeOwners, err := collectFlowSessionExpectedRoutes(regionNodes, nodes)
	if err != nil {
		return flowSessionRegion{}, err
	}
	branchTargets, allowedBranchEdges, err := indexFlowSessionBranchRoutes(
		compiled, trigger, regionNodes, expectedRoutes,
	)
	if err != nil {
		return flowSessionRegion{}, err
	}

	branchDownstream, branchSkippedNodes, dynamicNodeIDs, err := collectFlowSessionBranchDownstream(compiled.Nodes, routeOwners)
	if err != nil {
		return flowSessionRegion{}, err
	}
	// Nodes reachable from an appended route target are only part of the
	// selected route. Do not leave them in the unconditional trigger region:
	// doing so would either run an unselected route or stall on its dependency.
	for nodeID := range dynamicNodeIDs {
		delete(regionNodes, nodeID)
	}

	if err := validateFlowSessionRegionScopes(
		regionNodes, nodes, trigger.Delivery, branchDownstream, branchSkippedNodes,
	); err != nil {
		return flowSessionRegion{}, err
	}
	if err := validateFlowSessionRegionEdges(
		compiled.Edges, regionNodes, dynamicNodeIDs, allowedBranchEdges,
	); err != nil {
		return flowSessionRegion{}, err
	}
	return flowSessionRegion{
		nodes:              regionNodes,
		branchTargets:      branchTargets,
		branchDownstream:   branchDownstream,
		branchSkippedNodes: branchSkippedNodes,
	}, nil
}

func collectFlowSessionExpectedRoutes(
	regionNodes map[string]struct{},
	nodes map[string]flow.CompiledNode,
) (map[string]map[string]string, map[string]string, error) {
	expectedRoutes := make(map[string]map[string]string)
	routeOwners := make(map[string]string)
	for nodeID := range regionNodes {
		node := nodes[nodeID]
		if node.Kind != flow.KindBranch || node.Branch == nil {
			continue
		}
		if _, sourceInRegion := regionNodes[node.Branch.Source]; !sourceInRegion {
			return nil, nil, fmt.Errorf("session branch node %q source %q is outside its trigger region", nodeID, node.Branch.Source)
		}
		routes := make(map[string]string, len(node.Branch.Cases)+1)
		for _, branchCase := range node.Branch.Cases {
			route := strings.TrimSpace(branchCase.Name)
			if route == "" {
				route = strings.TrimSpace(branchCase.To)
			}
			if _, duplicate := routes[route]; duplicate {
				return nil, nil, fmt.Errorf("session branch node %q has duplicate route %q", nodeID, route)
			}
			routes[route] = branchCase.To
			if owner, duplicate := routeOwners[branchCase.To]; duplicate && owner != nodeID {
				return nil, nil, fmt.Errorf("session branch target %q is shared by branch nodes %q and %q", branchCase.To, owner, nodeID)
			}
			routeOwners[branchCase.To] = nodeID
		}
		if _, duplicate := routes[branchDefaultRoute]; duplicate {
			return nil, nil, fmt.Errorf("session branch node %q case name %q is reserved", nodeID, branchDefaultRoute)
		}
		routes[branchDefaultRoute] = node.Branch.DefaultTo
		if owner, duplicate := routeOwners[node.Branch.DefaultTo]; duplicate && owner != nodeID {
			return nil, nil, fmt.Errorf("session branch target %q is shared by branch nodes %q and %q", node.Branch.DefaultTo, owner, nodeID)
		}
		routeOwners[node.Branch.DefaultTo] = nodeID
		expectedRoutes[nodeID] = routes
	}
	return expectedRoutes, routeOwners, nil
}

func indexFlowSessionBranchRoutes(
	compiled *flow.Compiled,
	trigger *flow.SessionTrigger,
	regionNodes map[string]struct{},
	expectedRoutes map[string]map[string]string,
) (map[string]map[string]flow.CompiledNode, map[string]struct{}, error) {
	branchTargets := make(map[string]map[string]flow.CompiledNode, len(expectedRoutes))
	foundRoutes := make(map[string]map[string]int, len(expectedRoutes))
	allowedBranchEdges := make(map[string]struct{})
	for _, edge := range compiled.Edges {
		if _, fromInRegion := regionNodes[edge.From]; !fromInRegion {
			continue
		}
		branchRoutes, isBranch := expectedRoutes[edge.From]
		targetID, routeExists := branchRoutes[edge.When.Choice]
		if !isBranch || !routeExists || targetID != edge.Append.ID || strings.TrimSpace(edge.FromPrefix) != "" {
			return nil, nil, fmt.Errorf("session regions do not support control-flow append edge %q", edge.ID)
		}
		if _, exists := foundRoutes[edge.From]; !exists {
			foundRoutes[edge.From] = make(map[string]int)
			branchTargets[edge.From] = make(map[string]flow.CompiledNode)
		}
		foundRoutes[edge.From][edge.When.Choice]++
		if foundRoutes[edge.From][edge.When.Choice] != 1 {
			return nil, nil, fmt.Errorf("session branch node %q has duplicate compiled route %q", edge.From, edge.When.Choice)
		}
		if err := validateFlowSessionAppendNode(edge.Append, trigger.Delivery); err != nil {
			return nil, nil, err
		}
		branchTargets[edge.From][edge.Append.ID] = edge.Append
		allowedBranchEdges[edge.ID] = struct{}{}
	}
	for branchID, routes := range expectedRoutes {
		for route := range routes {
			if foundRoutes[branchID][route] != 1 {
				return nil, nil, fmt.Errorf("session branch node %q is missing compiled route %q", branchID, route)
			}
		}
	}
	return branchTargets, allowedBranchEdges, nil
}

func validateFlowSessionRegionScopes(
	regionNodes map[string]struct{},
	nodes map[string]flow.CompiledNode,
	delivery string,
	branchDownstream map[string][]flow.CompiledNode,
	branchSkippedNodes map[string][]string,
) error {
	for nodeID := range regionNodes {
		node := nodes[nodeID]
		if node.Kind == flow.KindStep && flow.EffectiveSessionDelivery(delivery) != flow.SessionDeliveryDurable {
			return fmt.Errorf("session Agent node %q requires durable event delivery", nodeID)
		}
		if err := validateFlowSessionRegionNode(regionNodes, node); err != nil {
			return err
		}
	}
	for targetID, downstream := range branchDownstream {
		routeScope := make(map[string]struct{}, len(regionNodes)+len(downstream)+1)
		for nodeID := range regionNodes {
			routeScope[nodeID] = struct{}{}
		}
		routeScope[targetID] = struct{}{}
		for _, node := range downstream {
			routeScope[node.ID] = struct{}{}
		}
		for _, skippedNodeID := range branchSkippedNodes[targetID] {
			routeScope[skippedNodeID] = struct{}{}
		}
		if err := validateFlowSessionRegionNodes(routeScope, downstream, delivery); err != nil {
			return err
		}
	}
	return nil
}

func validateFlowSessionRegionNodes(
	routeScope map[string]struct{},
	nodes []flow.CompiledNode,
	delivery string,
) error {
	for _, node := range nodes {
		if node.Kind == flow.KindStep && flow.EffectiveSessionDelivery(delivery) != flow.SessionDeliveryDurable {
			return fmt.Errorf("session Agent node %q requires durable event delivery", node.ID)
		}
		if err := validateFlowSessionRegionNode(routeScope, node); err != nil {
			return err
		}
	}
	return nil
}

func validateFlowSessionRegionEdges(
	edges []flow.CompiledEdge,
	regionNodes map[string]struct{},
	dynamicNodeIDs map[string]struct{},
	allowedBranchEdges map[string]struct{},
) error {
	for _, edge := range edges {
		if _, allowed := allowedBranchEdges[edge.ID]; allowed {
			continue
		}
		_, fromInRegion := regionNodes[edge.From]
		_, toInRegion := regionNodes[edge.Append.ID]
		_, fromInDynamicRoute := dynamicNodeIDs[edge.From]
		_, toInDynamicRoute := dynamicNodeIDs[edge.Append.ID]
		if fromInRegion || toInRegion || fromInDynamicRoute || toInDynamicRoute {
			return fmt.Errorf("session regions do not support control-flow append edge %q", edge.ID)
		}
	}
	return nil
}

func collectFlowSessionBranchDownstream(
	nodes []flow.CompiledNode,
	routeOwners map[string]string,
) (map[string][]flow.CompiledNode, map[string][]string, map[string]struct{}, error) {
	chains := make(map[string]map[string]struct{}, len(routeOwners))
	for targetID := range routeOwners {
		chain := map[string]struct{}{targetID: {}}
		for changed := true; changed; {
			changed = false
			for _, node := range nodes {
				if _, alreadyIncluded := chain[node.ID]; alreadyIncluded {
					continue
				}
				dependsOnRoute := false
				for _, dependency := range node.DependsOn {
					if _, included := chain[dependency]; included {
						dependsOnRoute = true
						break
					}
				}
				if !dependsOnRoute {
					continue
				}
				if node.Kind == flow.KindBranch {
					return nil, nil, nil, fmt.Errorf("session branch route %q does not support nested branch node %q", targetID, node.ID)
				}
				chain[node.ID] = struct{}{}
				changed = true
			}
		}
		chains[targetID] = chain
	}

	downstreamOwners := make(map[string]string)
	dynamicNodeIDs := make(map[string]struct{})
	branchDownstream := make(map[string][]flow.CompiledNode, len(chains))
	for targetID, chain := range chains {
		branchID := routeOwners[targetID]
		dynamicNodeIDs[targetID] = struct{}{}
		for nodeID := range chain {
			if nodeID == targetID {
				continue
			}
			if owner, exists := downstreamOwners[nodeID]; exists && owner != branchID {
				return nil, nil, nil, fmt.Errorf(
					"session branch route chains from different branches cannot merge at node %q",
					nodeID,
				)
			}
			downstreamOwners[nodeID] = branchID
			dynamicNodeIDs[nodeID] = struct{}{}
		}
		for _, node := range nodes {
			if _, included := chain[node.ID]; included && node.ID != targetID {
				branchDownstream[targetID] = append(branchDownstream[targetID], node)
			}
		}
	}
	branchSkippedNodes := make(map[string][]string, len(chains))
	for targetID, chain := range chains {
		branchID := routeOwners[targetID]
		for siblingTargetID, siblingBranchID := range routeOwners {
			if siblingBranchID != branchID || siblingTargetID == targetID {
				continue
			}
			for _, node := range nodes {
				if _, inSibling := chains[siblingTargetID][node.ID]; !inSibling {
					continue
				}
				if _, inSelected := chain[node.ID]; inSelected {
					continue
				}
				branchSkippedNodes[targetID] = append(branchSkippedNodes[targetID], node.ID)
			}
		}
	}
	return branchDownstream, branchSkippedNodes, dynamicNodeIDs, nil
}

func addSessionRegionNode(region map[string]struct{}, node flow.CompiledNode) bool {
	for _, dependency := range node.DependsOn {
		if _, exists := region[dependency]; exists {
			region[node.ID] = struct{}{}
			return true
		}
	}
	return false
}

func validateFlowSessionRegionNode(region map[string]struct{}, node flow.CompiledNode) error {
	for _, dependency := range node.DependsOn {
		if _, exists := region[dependency]; !exists {
			return fmt.Errorf("session region node %q depends on node %q outside its trigger region", node.ID, dependency)
		}
	}
	if strings.TrimSpace(node.PreScript) != "" || strings.TrimSpace(node.PostScript) != "" {
		return fmt.Errorf("session region node %q does not support pre_script or post_script", node.ID)
	}
	switch node.Kind {
	case flow.KindFunction:
		if node.Function == nil || !strings.EqualFold(strings.TrimSpace(node.Function.Runtime), flow.FunctionRuntimeJS) ||
			strings.TrimSpace(node.Function.Ref) != "" {
			return fmt.Errorf("session region node %q is not an inline JavaScript function", node.ID)
		}
	case flow.KindService:
		if node.Service == nil {
			return fmt.Errorf("session region node %q has no service configuration", node.ID)
		}
	case flow.KindLLM:
	case flow.KindStep:
		if len(node.Outputs) == 0 {
			return fmt.Errorf("session region Agent node %q must declare outputs", node.ID)
		}
	case flow.KindBranch:
		if node.Branch == nil {
			return fmt.Errorf("session branch node %q has no branch configuration", node.ID)
		}
	default:
		return fmt.Errorf("session region node %q has unsupported kind %q", node.ID, node.Kind)
	}
	return nil
}

func validateFlowSessionAppendNode(node flow.CompiledNode, delivery string) error {
	if strings.TrimSpace(node.PreScript) != "" || strings.TrimSpace(node.PostScript) != "" {
		return fmt.Errorf("session branch route target %q does not support pre_script or post_script", node.ID)
	}
	switch node.Kind {
	case flow.KindFunction:
		if node.Function == nil || !strings.EqualFold(strings.TrimSpace(node.Function.Runtime), flow.FunctionRuntimeJS) ||
			strings.TrimSpace(node.Function.Ref) != "" {
			return fmt.Errorf("session branch route target %q is not an inline JavaScript function", node.ID)
		}
	case flow.KindService:
		if node.Service == nil {
			return fmt.Errorf("session branch route target %q has no service configuration", node.ID)
		}
	case flow.KindLLM:
		if len(node.Outputs) == 0 {
			return fmt.Errorf("session branch route LLM node %q must declare outputs", node.ID)
		}
	case flow.KindStep:
		if flow.EffectiveSessionDelivery(delivery) != flow.SessionDeliveryDurable {
			return fmt.Errorf("session branch route Agent node %q requires durable event delivery", node.ID)
		}
		if len(node.Outputs) == 0 {
			return fmt.Errorf("session branch route Agent node %q must declare outputs", node.ID)
		}
	default:
		return fmt.Errorf("session branch route target %q has unsupported kind %q", node.ID, node.Kind)
	}
	return nil
}

func selectFlowSessionBranch(
	branch *flow.CompiledBranch,
	outputs map[string]map[string]any,
) (route, target string, err error) {
	if branch == nil {
		return "", "", fmt.Errorf("branch configuration is missing")
	}
	sourceOutputs, ok := outputs[branch.Source]
	if !ok {
		return "", "", fmt.Errorf("branch source node %q has no outputs", branch.Source)
	}
	source := workflowNode{
		ID:      branch.Source,
		Status:  workflowStatusCompleted,
		Outputs: sourceOutputs,
	}
	for _, branchCase := range branch.Cases {
		if workflowConditionMatchesNode(flowConditionToWorkflow(branchCase.Condition), source) {
			return branchCase.Name, branchCase.To, nil
		}
	}
	return branchDefaultRoute, branch.DefaultTo, nil
}

func (a *Agent) runFlowSessionService(
	ctx context.Context,
	state workflowState,
	spec *workflowServiceSpec,
	retry *workflowRetryPolicy,
	timeoutSec int,
) (map[string]any, error) {
	if timeoutSec > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
		defer cancel()
	}
	retry = normalizeWorkflowRetryPolicy(retry)
	for attempt := 1; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		outputs, err := a.callWorkflowService(ctx, state, spec)
		if err == nil {
			return outputs, nil
		}
		var failure *workflowServiceFailure
		if !errors.As(err, &failure) {
			return nil, err
		}
		shouldRetry, delay := shouldWorkflowRetry(retry, attempt, failure.retryKind)
		if !shouldRetry || ctx.Err() != nil {
			return nil, err
		}
		if delay <= 0 {
			continue
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func flowSessionTrigger(compiled *flow.Compiled, eventType string) *flow.SessionTrigger {
	if compiled == nil || compiled.SessionWorkflow == nil {
		return nil
	}
	eventType = strings.TrimSpace(eventType)
	for i := range compiled.SessionWorkflow.Triggers {
		if strings.TrimSpace(compiled.SessionWorkflow.Triggers[i].EventType) == eventType {
			return &compiled.SessionWorkflow.Triggers[i]
		}
	}
	return nil
}

func flowSessionLaneRuntimeConfig(compiled *flow.Compiled, eventType string) (id, class string, deadlineMS int, overload string) {
	class = flow.SessionLaneClassStandard
	overload = flow.SessionLaneOverloadCoalesceLatest
	trigger := flowSessionTrigger(compiled, eventType)
	if trigger == nil || compiled.SessionWorkflow == nil || strings.TrimSpace(trigger.LaneID) == "" {
		return
	}
	for _, lane := range compiled.SessionWorkflow.Lanes {
		if lane.ID != trigger.LaneID {
			continue
		}
		id = lane.ID
		if lane.Class != "" {
			class = lane.Class
		}
		deadlineMS = lane.DeadlineMS
		if lane.OverloadPolicy != "" {
			overload = lane.OverloadPolicy
		}
		return
	}
	return
}

func cloneFlowSessionState(state map[string]any) (map[string]any, error) {
	if state == nil {
		return map[string]any{}, nil
	}
	data, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	var cloned map[string]any
	if err := json.Unmarshal(data, &cloned); err != nil {
		return nil, err
	}
	if cloned == nil {
		cloned = map[string]any{}
	}
	return cloned, nil
}
