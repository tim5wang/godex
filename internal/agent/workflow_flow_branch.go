package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// executeWorkflowBranch runs a branch gateway node synchronously: it
// evaluates its cases in order against the source node's outputs, writes the
// matched route name (case name/to, else the reserved "default") into
// outputs.choice and completes the node. Routing to targets happens through
// condition edges with when.choice (compiled by flow.Compile), so exactly one
// target is appended — mutual exclusion is structural.
func (a *Agent) executeWorkflowBranch(state *workflowState, node *workflowNode) {
	spec := node.BranchSpec
	if spec == nil {
		a.failWorkflowBranchNode(state, node, "branch node missing spec")
		return
	}
	src := workflowNodeByID(state.Nodes, spec.Source)
	if src == nil {
		a.failWorkflowBranchNode(state, node, fmt.Sprintf("branch source node %q not found", spec.Source))
		return
	}
	route := branchDefaultRoute
	for _, c := range spec.Cases {
		if workflowConditionMatchesNode(c.Condition, *src) {
			route = c.Name
			break
		}
	}
	a.completeWorkflowBranch(state, node, route)
}

// completeWorkflowBranch records the branch verdict (route in outputs.choice)
// and finalizes the node handoff (no subagent job, no transcript).
func (a *Agent) completeWorkflowBranch(state *workflowState, node *workflowNode, route string) {
	now := time.Now().UTC()
	outputs := map[string]any{"choice": route}
	if err := validateWorkflowOutputSpecs(node.OutputSpec, outputs, "node "+node.ID+" outputs"); err != nil {
		a.failWorkflowBranchNode(state, node, "output contract violation: "+err.Error())
		return
	}
	node.Status = workflowStatusCompleted
	node.Attempt = nextWorkflowAttempt(*node)
	node.Outputs = outputs
	node.Error = ""
	node.JobID = ""
	node.UpdatedAt = now
	node.FinishedAt = now
	payload, _ := json.Marshal(outputs)
	node.ResultPreview = previewSubagentResultForModel(string(payload))
	if err := a.finalizeWorkflowNodeHandoff(state, node, nil, string(payload)); err != nil {
		node.Status = workflowStatusError
		node.Error = err.Error()
		return
	}
	_ = a.workflows.appendEvent(state.Summary.ID, map[string]interface{}{
		"event":   "branch_routed",
		"node_id": node.ID,
		"route":   route,
		"at":      now,
	})
	// Unified observability event (debug log: one line per node done + latency).
	_ = a.workflows.appendEvent(state.Summary.ID, map[string]interface{}{
		"event":      "node_completed",
		"node_id":    node.ID,
		"latency_ms": a.workflowNodeLatency(*node, now),
		"at":         now,
	})
}

// failWorkflowBranchNode terminates a branch node as error (invalid spec /
// missing source). It mirrors failWorkflowDecisionNode.
func (a *Agent) failWorkflowBranchNode(state *workflowState, node *workflowNode, message string) {
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
		"event":   "branch_failed",
		"node_id": node.ID,
		"error":   message,
		"at":      now,
	})
}

// workflowNodeByID returns the node with the given ID, or nil.
func workflowNodeByID(nodes []workflowNode, id string) *workflowNode {
	id = strings.TrimSpace(id)
	for i := range nodes {
		if nodes[i].ID == id {
			return &nodes[i]
		}
	}
	return nil
}
