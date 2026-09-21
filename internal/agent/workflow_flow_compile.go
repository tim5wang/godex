package agent

import (
	"fmt"

	"github.com/tim5wang/godex/internal/core/flow"
)

// compileFlowToWorkflowInputs lowers a validated flow.Compiled into durable
// engine inputs (workflowNodeInput/workflowEdgeInput). Kind mapping (Flow
// Spec → engine): step → subagent_task (default), llm → llm_task; decision /
// branch pass through. data_dependency/handoff edges are already folded into
// CompiledNode.DependsOn/HandoffFrom; CompiledEdge entries become condition
// edges (when + append template). This is the F1a compile adapter — the Flow
// version store and /v1/flows API land in F1b.
func compileFlowToWorkflowInputs(c *flow.Compiled) ([]workflowNodeInput, []workflowEdgeInput, error) {
	if c == nil {
		return nil, nil, fmt.Errorf("nil compiled flow")
	}
	nodes := make([]workflowNodeInput, 0, len(c.Nodes))
	for _, n := range c.Nodes {
		ni, err := compileFlowNode(n, false)
		if err != nil {
			return nil, nil, err
		}
		nodes = append(nodes, ni)
	}
	edges := make([]workflowEdgeInput, 0, len(c.Edges))
	for _, e := range c.Edges {
		appendNode, err := compileFlowNode(e.Append, true)
		if err != nil {
			return nil, nil, fmt.Errorf("edge %s: %w", e.ID, err)
		}
		edges = append(edges, workflowEdgeInput{
			ID:            e.ID,
			From:          e.From,
			When:          flowConditionToWorkflow(e.When),
			Append:        appendNode,
			MaxIterations: e.MaxIterations,
			IterationKey:  e.IterationKey,
		})
	}
	return nodes, edges, nil
}

// compileFlowNode maps one flow.CompiledNode to a workflowNodeInput.
// appendTemplate controls whether DependsOn/HandoffFrom are carried (append
// templates get their deps set by the engine edge machinery, not statically).
func compileFlowNode(n flow.CompiledNode, appendTemplate bool) (workflowNodeInput, error) {
	ni := workflowNodeInput{
		ID:         n.ID,
		Kind:       flowKindToEngine(n.Kind),
		Title:      n.Title,
		Prompt:     n.Prompt,
		AgentType:  n.AgentType,
		AgentRef:   n.AgentRef,
		WriteScope: append([]string{}, n.WriteScope...),
	}
	if !appendTemplate {
		ni.DependsOn = append([]string{}, n.DependsOn...)
		ni.HandoffFrom = append([]string{}, n.HandoffFrom...)
	}
	if n.Retry != nil {
		ni.Retry = &workflowRetryPolicy{
			MaxAttempts:        n.Retry.MaxAttempts,
			InitialIntervalMS:  n.Retry.InitialIntervalMS,
			BackoffCoefficient: n.Retry.BackoffCoefficient,
			MaxIntervalMS:      n.Retry.MaxIntervalMS,
			Jitter:             n.Retry.Jitter,
			RetryOn:            append([]string{}, n.Retry.RetryOn...),
			NonRetryable:       append([]string{}, n.Retry.NonRetryable...),
		}
	}
	if n.Decision != nil {
		ni.Decision = &workflowDecisionSpec{
			Provider:      n.Decision.Provider,
			Question:      n.Prompt,
			DecisionType:  n.Decision.DecisionType,
			TimeoutMS:     n.Decision.TimeoutMS,
			OnError:       n.Decision.OnError,
			DefaultChoice: n.Decision.DefaultChoice,
		}
		for _, ch := range n.Decision.Choices {
			ni.Decision.Choices = append(ni.Decision.Choices, workflowDecisionChoice{ID: ch.ID, Label: ch.Label})
		}
	}
	if n.Human != nil {
		ni.Human = &workflowHumanSpec{
			Queue:          n.Human.Queue,
			AssigneePolicy: n.Human.AssigneePolicy,
			Form:           n.Human.Form,
			TimeoutMS:      n.Human.TimeoutMS,
			OnTimeout:      n.Human.OnTimeout,
			ResultVar:      n.Human.ResultVar,
		}
	}
	// P2.3: carry the declared typed outputs so the engine can resolve
	// {{nodes.<id>.outputs.<field>}} references at run time.
	for _, v := range n.Outputs {
		ni.OutputSpec = append(ni.OutputSpec, workflowVarDef{Name: v.Name, Type: v.Type, Desc: v.Desc})
	}
	if n.Branch != nil {
		b := &workflowBranchSpec{
			Source:    n.Branch.Source,
			DefaultTo: n.Branch.DefaultTo,
		}
		for _, c := range n.Branch.Cases {
			b.Cases = append(b.Cases, workflowBranchCase{
				Name:      c.Name,
				To:        c.To,
				Condition: flowConditionToWorkflow(c.Condition),
			})
		}
		ni.Branch = b
	}
	return ni, nil
}

// flowKindToEngine maps Flow Spec node kinds to engine kinds. step defaults
// to subagent_task (the engine default); llm maps to the pure-reasoning kind;
// human maps to user_input (the engine's blocked-wait node, P1.1).
func flowKindToEngine(k string) string {
	switch k {
	case flow.KindStep:
		return "subagent_task"
	case flow.KindLLM:
		return "llm_task"
	case flow.KindDecision:
		return workflowNodeKindDecision
	case flow.KindBranch:
		return workflowNodeKindBranch
	case flow.KindHuman:
		return workflowNodeKindUserInput
	default:
		return k
	}
}

// flowConditionToWorkflow maps a Flow Spec condition onto the engine's
// structured edge predicate (same field set, including all/any nesting).
func flowConditionToWorkflow(c flow.Condition) workflowEdgeCondition {
	out := workflowEdgeCondition{
		Status:  c.Status,
		Verdict: c.Verdict,
		Node:    c.Node,
		Choice:  c.Choice,
	}
	if c.Confidence != nil {
		out.Confidence = &workflowNumCompare{Op: c.Confidence.Op, Value: c.Confidence.Value}
	}
	if c.Output != nil {
		out.Output = &workflowFieldCompare{Path: c.Output.Path, Op: c.Output.Op, Value: c.Output.Value}
	}
	if c.Not != nil {
		sub := flowConditionToWorkflow(*c.Not)
		out.Not = &sub
	}
	for _, sub := range c.All {
		out.All = append(out.All, flowConditionToWorkflow(sub))
	}
	for _, sub := range c.Any {
		out.Any = append(out.Any, flowConditionToWorkflow(sub))
	}
	return out
}
