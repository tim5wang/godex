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
		// E3a: Flow-level network policy rides on every outbound node so its
		// HTTP client can enforce it per-run.
		if ni.Function != nil {
			ni.Function.Network = c.Network
		}
		if ni.Service != nil {
			ni.Service.Network = c.Network
		}
		nodes = append(nodes, ni)
	}
	edges := make([]workflowEdgeInput, 0, len(c.Edges))
	for _, e := range c.Edges {
		appendNode, err := compileFlowNode(e.Append, true)
		if err != nil {
			return nil, nil, fmt.Errorf("edge %s: %w", e.ID, err)
		}
		// E3a: append-template outbound nodes enforce the same Flow policy.
		if appendNode.Function != nil {
			appendNode.Function.Network = c.Network
		}
		if appendNode.Service != nil {
			appendNode.Service.Network = c.Network
		}
		edges = append(edges, workflowEdgeInput{
			ID:            e.ID,
			From:          e.From,
			FromPrefix:    e.FromPrefix,
			When:          flowConditionToWorkflow(e.When),
			Append:        appendNode,
			MaxIterations: e.MaxIterations,
			IterationKey:  e.IterationKey,
		})
	}
	return nodes, edges, nil
}

// compileFlowNode maps one flow.CompiledNode to a workflowNodeInput. Most
// append templates have no dependencies; loop templates carry a {source}
// dependency which is expanded by the workflow edge machinery.
func compileFlowNode(n flow.CompiledNode, appendTemplate bool) (workflowNodeInput, error) {
	ni := workflowNodeInput{
		ID:         n.ID,
		Kind:       flowKindToEngine(n.Kind),
		Title:      n.Title,
		Prompt:     n.Prompt,
		TimeoutSec: n.TimeoutSec,
		AgentType:  n.AgentType,
		AgentRef:   n.AgentRef,
		WriteScope: append([]string{}, n.WriteScope...),
	}
	if !appendTemplate || len(n.DependsOn) > 0 {
		ni.DependsOn = append([]string{}, n.DependsOn...)
	}
	if !appendTemplate || len(n.HandoffFrom) > 0 {
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
		timeoutMS := n.Decision.TimeoutMS
		if n.TimeoutSec > 0 {
			nodeTimeoutMS := n.TimeoutSec * 1000
			if timeoutMS <= 0 || nodeTimeoutMS < timeoutMS {
				timeoutMS = nodeTimeoutMS
			}
		}
		ni.Decision = &workflowDecisionSpec{
			Provider:      n.Decision.Provider,
			Question:      n.Prompt,
			DecisionType:  n.Decision.DecisionType,
			TimeoutMS:     timeoutMS,
			OnError:       n.Decision.OnError,
			DefaultChoice: n.Decision.DefaultChoice,
		}
		for _, ch := range n.Decision.Choices {
			ni.Decision.Choices = append(ni.Decision.Choices, workflowDecisionChoice{ID: ch.ID, Label: ch.Label})
		}
	}
	if n.Human != nil {
		timeoutMS := n.Human.TimeoutMS
		if n.TimeoutSec > 0 {
			nodeTimeoutMS := n.TimeoutSec * 1000
			if timeoutMS <= 0 || nodeTimeoutMS < timeoutMS {
				timeoutMS = nodeTimeoutMS
			}
		}
		ni.Human = &workflowHumanSpec{
			Queue:          n.Human.Queue,
			AssigneePolicy: n.Human.AssigneePolicy,
			Form:           n.Human.Form,
			TimeoutMS:      timeoutMS,
			OnTimeout:      n.Human.OnTimeout,
			ResultVar:      n.Human.ResultVar,
		}
	}
	if n.Function != nil {
		ni.Function = &workflowFunctionSpec{
			Runtime:      n.Function.Runtime,
			Source:       n.Function.Source,
			Ref:          n.Function.Ref,
			Handler:      n.Function.Handler,
			TimeoutSec:   n.TimeoutSec,
			InputSchema:  append([]byte{}, n.Function.InputSchema...),
			OutputSchema: append([]byte{}, n.Function.OutputSchema...),
		}
	}
	if n.Service != nil {
		ni.Service = &workflowServiceSpec{
			Method:  n.Service.Method,
			URL:     n.Service.URL,
			Headers: n.Service.Headers,
			Body:    append([]byte{}, n.Service.Body...),
			Auth:    n.Service.Auth,
		}
	}
	// E3b: optional bash scripts around the node's main work.
	ni.PreScript = n.PreScript
	ni.PostScript = n.PostScript
	// P2.3: carry the declared typed outputs so the engine can resolve
	// {{nodes.<id>.outputs.<field>}} references at run time.
	for _, v := range n.Outputs {
		ni.OutputSpec = append(ni.OutputSpec, workflowVarDef{
			Name: v.Name, Type: v.Type, Desc: v.Desc, Required: v.Required,
			Schema: append([]byte{}, v.Schema...),
		})
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
	case flow.KindFunction:
		return workflowNodeKindFunction
	case flow.KindService:
		return workflowNodeKindService
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
