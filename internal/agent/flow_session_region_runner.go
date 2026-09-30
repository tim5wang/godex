package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/tim5wang/godex/internal/core/flow"
)

type flowSessionRegionRunner struct {
	agent          *Agent
	ctx            context.Context
	rec            flowSessionRecord
	compiled       *flow.Compiled
	event          FlowSessionEvent
	trigger        *flow.SessionTrigger
	region         flowSessionRegion
	eventContext   map[string]any
	sessionState   map[string]any
	nodeExecutions *[]FlowSessionNodeExecution

	outputs        map[string]map[string]any
	done           map[string]bool
	scheduled      map[string]bool
	executionNodes []flow.CompiledNode
	nodeIDs        []string
	branchRoutes   map[string]string
	remaining      int
}

type flowSessionRegionNodeResult struct {
	outputs             map[string]any
	sessionState        map[string]any
	sessionStateUpdated bool
	branchTarget        *flow.CompiledNode
}

func newFlowSessionRegionRunner(
	agent *Agent,
	ctx context.Context,
	rec flowSessionRecord,
	compiled *flow.Compiled,
	event FlowSessionEvent,
	trigger *flow.SessionTrigger,
	region flowSessionRegion,
	nodeExecutions *[]FlowSessionNodeExecution,
) (*flowSessionRegionRunner, error) {
	payload := map[string]any{}
	if len(event.Payload) != 0 {
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return nil, fmt.Errorf("decode session event payload: %w", err)
		}
	}
	eventContext := map[string]any{
		"flow_session_id": rec.SessionID,
		"flow_id":         rec.FlowID,
		"version":         rec.Version,
		"source":          event.Source,
		"source_sequence": event.SourceSequence,
		"sequence":        event.Sequence,
		"type":            event.Type,
		"correlation_id":  event.CorrelationID,
		"occurred_at":     event.OccurredAt,
		"received_at":     event.ReceivedAt,
		"payload":         payload,
	}
	sessionState, err := cloneFlowSessionState(rec.State)
	if err != nil {
		return nil, fmt.Errorf("copy session state: %w", err)
	}

	runner := &flowSessionRegionRunner{
		agent:          agent,
		ctx:            ctx,
		rec:            rec,
		compiled:       compiled,
		event:          event,
		trigger:        trigger,
		region:         region,
		eventContext:   eventContext,
		sessionState:   sessionState,
		nodeExecutions: nodeExecutions,
		outputs:        make(map[string]map[string]any, len(region.nodes)),
		done:           make(map[string]bool, len(region.nodes)),
		scheduled:      make(map[string]bool, len(region.nodes)),
		executionNodes: make([]flow.CompiledNode, 0, len(region.nodes)),
		branchRoutes:   make(map[string]string),
	}
	for _, node := range compiled.Nodes {
		if _, inRegion := region.nodes[node.ID]; !inRegion {
			continue
		}
		runner.executionNodes = append(runner.executionNodes, node)
		runner.scheduled[node.ID] = true
	}
	runner.nodeIDs = make([]string, 0, len(runner.executionNodes)+1)
	runner.remaining = len(runner.executionNodes)
	return runner, nil
}

func (r *flowSessionRegionRunner) execute() (
	map[string]any,
	[]string,
	map[string]string,
	[]flowSessionOutput,
	error,
) {
	for r.remaining > 0 {
		progressed := false
		for _, node := range r.executionNodes {
			if !r.scheduled[node.ID] || r.done[node.ID] || !r.ready(node) {
				continue
			}
			if err := r.ctx.Err(); err != nil {
				return nil, r.nodeIDs, r.branchRoutes, nil, err
			}
			if err := r.executeNode(node); err != nil {
				return nil, r.nodeIDs, r.branchRoutes, nil, err
			}
			progressed = true
		}
		if !progressed {
			return nil, r.nodeIDs, r.branchRoutes, nil,
				fmt.Errorf("session trigger %q region did not make progress", r.trigger.EventType)
		}
	}

	outputs, err := collectFlowSessionOutputs(
		r.rec, r.event, r.nodeIDs, r.executionNodes, r.scheduled, r.outputs,
	)
	if err != nil {
		return nil, r.nodeIDs, r.branchRoutes, nil, err
	}
	return r.sessionState, r.nodeIDs, r.branchRoutes, outputs, nil
}

func (r *flowSessionRegionRunner) ready(node flow.CompiledNode) bool {
	for _, dependency := range node.DependsOn {
		if !r.done[dependency] {
			return false
		}
	}
	return true
}

func (r *flowSessionRegionRunner) executeNode(compiledNode flow.CompiledNode) error {
	startedAt := time.Now()
	recordExecution := func(status string) {
		if r.nodeExecutions == nil {
			return
		}
		*r.nodeExecutions = append(*r.nodeExecutions, FlowSessionNodeExecution{
			NodeID:     compiledNode.ID,
			Status:     status,
			DurationMS: time.Since(startedAt).Milliseconds(),
		})
	}
	fail := func(err error) error {
		status := "failed"
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			status = "canceled"
		}
		recordExecution(status)
		return err
	}

	result, err := r.runNode(compiledNode)
	if err != nil {
		return fail(err)
	}
	if result.sessionStateUpdated {
		r.sessionState = result.sessionState
	}
	recordExecution("completed")
	r.outputs[compiledNode.ID] = result.outputs
	r.done[compiledNode.ID] = true
	r.nodeIDs = append(r.nodeIDs, compiledNode.ID)
	r.remaining--
	return r.scheduleBranch(result.branchTarget)
}

func (r *flowSessionRegionRunner) runNode(
	compiledNode flow.CompiledNode,
) (flowSessionRegionNodeResult, error) {
	nodeInput, err := compileFlowNode(compiledNode, false)
	if err != nil {
		return flowSessionRegionNodeResult{}, err
	}
	isStep := compiledNode.Kind == flow.KindStep
	phase := "node_started"
	if isStep {
		phase = "agent_started"
	}
	reportFlowSessionProgress(r.ctx, FlowSessionProgressUpdate{
		NodeID: compiledNode.ID,
		Phase:  phase,
	})
	if err := validateFlowSessionNodeConfiguration(compiledNode, nodeInput); err != nil {
		return flowSessionRegionNodeResult{}, err
	}

	node := workflowNode{ID: compiledNode.ID, Title: compiledNode.Title}
	nodeState := r.nodeState()
	raw, branchTarget, err := r.executeNodeAction(compiledNode, node, nodeInput, nodeState)
	if err != nil {
		return flowSessionRegionNodeResult{}, err
	}
	return validateFlowSessionNodeResult(node, nodeInput, raw, compiledNode.Kind == flow.KindFunction, branchTarget)
}

func (r *flowSessionRegionRunner) nodeState() workflowState {
	workflowNodes := make([]workflowNode, 0, len(r.outputs))
	for _, prior := range r.executionNodes {
		if priorOutputs, ok := r.outputs[prior.ID]; ok {
			workflowNodes = append(workflowNodes, workflowNode{ID: prior.ID, Outputs: priorOutputs})
		}
	}
	sessionContext := map[string]any{
		"id":      r.rec.SessionID,
		"state":   r.sessionState,
		"version": r.rec.Version,
	}
	return workflowState{
		Summary: workflowSummary{
			RunInputs: r.rec.Inputs,
			TemplateVars: map[string]any{
				"event":   r.eventContext,
				"session": sessionContext,
			},
		},
		Nodes: workflowNodes,
	}
}

func (r *flowSessionRegionRunner) executeNodeAction(
	compiledNode flow.CompiledNode,
	node workflowNode,
	nodeInput workflowNodeInput,
	nodeState workflowState,
) (any, *flow.CompiledNode, error) {
	var raw any
	var selectedBranchTarget *flow.CompiledNode
	var err error
	switch compiledNode.Kind {
	case flow.KindBranch:
		route, targetID, err := selectFlowSessionBranch(compiledNode.Branch, r.outputs)
		if err != nil {
			return nil, nil, fmt.Errorf("node %s: %w", node.ID, err)
		}
		target, exists := r.region.branchTargets[compiledNode.ID][targetID]
		if !exists {
			return nil, nil, fmt.Errorf("branch node %q selected unavailable route target %q", compiledNode.ID, targetID)
		}
		r.branchRoutes[compiledNode.ID] = route
		raw = map[string]any{"choice": route}
		target.DependsOn = []string{compiledNode.ID}
		selectedBranchTarget = &target
	case flow.KindFunction:
		nodeInput.Function.Network = &flow.NetworkPolicy{Policy: "allowlist"}
		handlerContext := r.agent.workflowFunctionContext(nodeState, node)
		handlerContext["event"] = r.eventContext
		handlerContext["session"] = nodeState.Summary.TemplateVars["session"]
		if err := flow.ValidateJSONSchemaValue(handlerContext, nodeInput.Function.InputSchema, "function input"); err != nil {
			return nil, nil, fmt.Errorf("node %s input contract violation: %w", node.ID, err)
		}
		raw, err = r.agent.runWorkflowFunction(r.ctx, nodeInput.Function, handlerContext)
	case flow.KindService:
		nodeInput.Service.Network = r.compiled.Network
		raw, err = r.agent.runFlowSessionService(r.ctx, nodeState, nodeInput.Service, nodeInput.Retry, compiledNode.TimeoutSec)
	case flow.KindLLM:
		raw, err = r.agent.runFlowSessionLLM(r.ctx, nodeState, r.rec, nodeInput, compiledNode.TimeoutSec)
	case flow.KindStep:
		node.ID = compiledNode.ID
		node.Title = compiledNode.Title
		node.Prompt = nodeInput.Prompt
		node.AgentType = nodeInput.AgentType
		node.AgentRef = nodeInput.AgentRef
		node.WriteScope = nodeInput.WriteScope
		node.DependsOn = compiledNode.DependsOn
		node.OutputSpec = nodeInput.OutputSpec
		node.Retry = nodeInput.Retry
		raw, err = r.agent.runFlowSessionStep(r.ctx, nodeState, r.rec, r.event, node, compiledNode.TimeoutSec)
	default:
		return nil, nil, fmt.Errorf("session region node %q has unsupported kind %q", compiledNode.ID, compiledNode.Kind)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("node %s: %w", node.ID, err)
	}
	return raw, selectedBranchTarget, nil
}

func (r *flowSessionRegionRunner) scheduleBranch(target *flow.CompiledNode) error {
	if target == nil {
		return nil
	}
	if r.scheduled[target.ID] {
		return fmt.Errorf("branch route target %q was already scheduled in the session region", target.ID)
	}
	for _, skippedNodeID := range r.region.branchSkippedNodes[target.ID] {
		r.done[skippedNodeID] = true
	}
	r.executionNodes = append(r.executionNodes, *target)
	r.scheduled[target.ID] = true
	r.remaining++
	for _, downstream := range r.region.branchDownstream[target.ID] {
		if r.scheduled[downstream.ID] {
			return fmt.Errorf("branch route downstream node %q was already scheduled in the session region", downstream.ID)
		}
		r.executionNodes = append(r.executionNodes, downstream)
		r.scheduled[downstream.ID] = true
		r.remaining++
	}
	return nil
}

func validateFlowSessionNodeConfiguration(node flow.CompiledNode, input workflowNodeInput) error {
	switch node.Kind {
	case flow.KindFunction:
		if input.Function == nil {
			return fmt.Errorf("session region function node %q has no function configuration", node.ID)
		}
	case flow.KindService:
		if input.Service == nil {
			return fmt.Errorf("session region service node %q has no service configuration", node.ID)
		}
	case flow.KindLLM, flow.KindStep, flow.KindBranch:
	default:
		return fmt.Errorf("session region node %q has unsupported kind %q", node.ID, node.Kind)
	}
	return nil
}

func validateFlowSessionNodeResult(
	node workflowNode,
	input workflowNodeInput,
	raw any,
	isFunction bool,
	branchTarget *flow.CompiledNode,
) (flowSessionRegionNodeResult, error) {
	result, ok := raw.(map[string]any)
	if !ok {
		return flowSessionRegionNodeResult{}, fmt.Errorf("node %s must return one JSON object", node.ID)
	}
	if isFunction {
		if err := flow.ValidateJSONSchemaValue(result, input.Function.OutputSchema, "function output"); err != nil {
			return flowSessionRegionNodeResult{}, fmt.Errorf("node %s output contract violation: %w", node.ID, err)
		}
	}
	if err := validateWorkflowOutputSpecs(input.OutputSpec, result, "node "+node.ID+" outputs"); err != nil {
		return flowSessionRegionNodeResult{}, err
	}
	updated := flowSessionRegionNodeResult{outputs: result, branchTarget: branchTarget}
	if nextState, exists := result["session_state"]; exists {
		stateMap, ok := nextState.(map[string]any)
		if !ok {
			return flowSessionRegionNodeResult{}, fmt.Errorf("node %s session_state output must be an object", node.ID)
		}
		sessionState, err := cloneFlowSessionState(stateMap)
		if err != nil {
			return flowSessionRegionNodeResult{}, fmt.Errorf("node %s session_state is not JSON-compatible: %w", node.ID, err)
		}
		updated.sessionState = sessionState
		updated.sessionStateUpdated = true
	}
	return updated, nil
}
