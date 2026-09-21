// P1.1 human task operations on the agent: registering a human task when a
// user_input node carrying a human spec becomes ready, replying to complete
// the node and continue the run, listing the task queue, and lazily applying
// on_timeout escalation for overdue tasks. HTTP/backend call these; the
// durable records live in the human task store ({StateDir}/human-tasks/).
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// HumanTaskView is the public projection of one human task.
type HumanTaskView struct {
	TaskID         string    `json:"task_id"`
	FlowID         string    `json:"flow_id,omitempty"`
	RunID          string    `json:"run_id"`
	WorkflowID     string    `json:"workflow_id,omitempty"`
	NodeID         string    `json:"node_id"`
	Queue          string    `json:"queue"`
	AssigneePolicy string    `json:"assignee_policy,omitempty"`
	Form           any       `json:"form,omitempty"`
	Prompt         string    `json:"prompt"`
	Status         string    `json:"status"`
	Result         any       `json:"result,omitempty"`
	ResultVar      string    `json:"result_var,omitempty"`
	TimeoutMS      int       `json:"timeout_ms,omitempty"`
	OnTimeout      string    `json:"on_timeout,omitempty"`
	DueAt          time.Time `json:"due_at,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

func humanTaskView(rec humanTaskRecord) HumanTaskView {
	return HumanTaskView{
		TaskID:         rec.TaskID,
		FlowID:         rec.FlowID,
		RunID:          rec.RunID,
		WorkflowID:     rec.WorkflowID,
		NodeID:         rec.NodeID,
		Queue:          rec.Queue,
		AssigneePolicy: rec.AssigneePolicy,
		Form:           rec.Form,
		Prompt:         rec.Prompt,
		Status:         rec.Status,
		Result:         rec.Result,
		ResultVar:      rec.ResultVar,
		TimeoutMS:      rec.TimeoutMS,
		OnTimeout:      rec.OnTimeout,
		DueAt:          rec.DueAt,
		CreatedAt:      rec.CreatedAt,
		UpdatedAt:      rec.UpdatedAt,
	}
}

// registerWorkflowHumanTask creates the durable human task record for a
// user_input node that carries a human spec. It is idempotent (the node
// records its task id). The node must already have HumanSpec set.
func (a *Agent) registerWorkflowHumanTask(state *workflowState, node *workflowNode) error {
	if a == nil || a.humanTasks == nil {
		return fmt.Errorf("human task store unavailable")
	}
	if node == nil || node.HumanSpec == nil {
		return nil
	}
	if strings.TrimSpace(node.HumanTaskID) != "" {
		return nil // already registered
	}
	workflowID := ""
	if state != nil {
		workflowID = state.Summary.ID
	}
	runID, flowID := runIDAndFlowIDFromWorkflowID(workflowID)
	queue := strings.TrimSpace(node.HumanSpec.Queue)
	if queue == "" {
		queue = defaultHumanQueue
	}
	now := time.Now().UTC()
	rec := humanTaskRecord{
		TaskID:         runID + ":" + node.ID,
		FlowID:         flowID,
		RunID:          runID,
		WorkflowID:     workflowID,
		NodeID:         node.ID,
		Queue:          queue,
		AssigneePolicy: strings.TrimSpace(node.HumanSpec.AssigneePolicy),
		Form:           node.HumanSpec.Form,
		Prompt:         strings.TrimSpace(node.Prompt),
		Status:         humanTaskStatusPending,
		ResultVar:      strings.TrimSpace(node.HumanSpec.ResultVar),
		TimeoutMS:      node.HumanSpec.TimeoutMS,
		OnTimeout:      strings.TrimSpace(node.HumanSpec.OnTimeout),
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if rec.TimeoutMS > 0 {
		rec.DueAt = now.Add(time.Duration(rec.TimeoutMS) * time.Millisecond)
	}
	if err := a.humanTasks.saveTask(rec); err != nil {
		return err
	}
	node.HumanTaskID = rec.TaskID
	return nil
}

// runIDAndFlowIDFromWorkflowID splits a flow workflow id
// ("fl_{flowID}_{version}_{runID}") into its run id and flow id. The run id
// always starts with "fr_", so we split on the last "_fr_" occurrence.
func runIDAndFlowIDFromWorkflowID(workflowID string) (runID, flowID string) {
	workflowID = strings.TrimSpace(workflowID)
	idx := strings.LastIndex(workflowID, "_fr_")
	if idx < 0 {
		return "", ""
	}
	runID = workflowID[idx+1:]
	rest := strings.TrimPrefix(workflowID[:idx], "fl_")
	// flow id is the leading segment; version follows the first "_" after it.
	if i := strings.Index(rest, "_"); i > 0 {
		flowID = rest[:i]
	}
	if flowID == "" {
		flowID = rest
	}
	return runID, flowID
}

// ListHumanTasks returns the human task queue (newest first). Empty queue or
// status matches all.
func (a *Agent) ListHumanTasks(queue, status string) ([]HumanTaskView, error) {
	if a == nil || a.humanTasks == nil {
		return nil, fmt.Errorf("human task store unavailable")
	}
	recs, err := a.humanTasks.listTasks(queue, status)
	if err != nil {
		return nil, err
	}
	out := make([]HumanTaskView, 0, len(recs))
	for _, rec := range recs {
		out = append(out, humanTaskView(rec))
	}
	return out, nil
}

// ReplyFlowRunHuman completes the human node of a run with the submitted
// value, marks the task replied, and continues the run. value is stored as
// JSON text in the node result and, when the spec declares a result_var, in
// the node outputs under that key.
func (a *Agent) ReplyFlowRunHuman(ctx context.Context, flowID, runID, nodeID string, value any) (FlowRunView, error) {
	if a == nil || a.flows == nil || a.workflows == nil {
		return FlowRunView{}, fmt.Errorf("flow runtime unavailable")
	}
	runID = strings.TrimSpace(runID)
	nodeID = strings.TrimSpace(nodeID)
	if runID == "" || nodeID == "" {
		return FlowRunView{}, fmt.Errorf("missing run_id/node_id")
	}

	// Resolve the workflow for this run (flowID may be empty; try the run
	// record directly, else search the task store).
	workflowID := ""
	rec, err := a.flows.loadRun(flowID, runID)
	if err == nil && rec.WorkflowID != "" {
		workflowID = rec.WorkflowID
	} else if a.humanTasks != nil {
		if tasks, lerr := a.humanTasks.listRunTasks(runID); lerr == nil {
			for _, t := range tasks {
				if t.NodeID == nodeID && t.WorkflowID != "" {
					workflowID = t.WorkflowID
					if flowID == "" {
						flowID = t.FlowID
					}
					break
				}
			}
		}
	}
	if workflowID == "" {
		return FlowRunView{}, fmt.Errorf("run %s has no workflow for node %s", runID, nodeID)
	}

	// Serialize the submitted value as the node result.
	resultText, err := humanReplyText(value)
	if err != nil {
		return FlowRunView{}, err
	}

	// Complete the blocked user_input node (existing engine primitive).
	if _, err := a.completeWorkflowNode(workflowID, nodeID, resultText); err != nil {
		return FlowRunView{}, fmt.Errorf("complete human node: %w", err)
	}

	// Mark the task replied (best-effort; the node may be a plain user_input
	// without a human task record).
	if a.humanTasks != nil {
		if tasks, lerr := a.humanTasks.listRunTasks(runID); lerr == nil {
			for _, t := range tasks {
				if t.NodeID != nodeID || t.Status == humanTaskStatusReplied {
					continue
				}
				t.Status = humanTaskStatusReplied
				t.Result = value
				t.UpdatedAt = time.Now().UTC()
				_ = a.humanTasks.saveTask(t)
				// Write the submitted value into the node outputs under the
				// declared result_var so downstream edges can read it.
				if strings.TrimSpace(t.ResultVar) != "" {
					_ = a.writeWorkflowNodeOutput(workflowID, nodeID, t.ResultVar, value)
				}
				break
			}
		}
	}

	// Continue the run: ready nodes that depended on the human node execute.
	if _, err := a.startWorkflowReadyNodes(ctx, workflowID); err != nil {
		return FlowRunView{}, err
	}
	if flowID != "" {
		return a.RefreshFlowRun(flowID, runID)
	}
	// flowID unknown: reload the run record by scanning run dir.
	runRec, err := a.flows.loadRun("", runID)
	if err != nil {
		return FlowRunView{}, err
	}
	return flowRunView(runRec), nil
}

// writeWorkflowNodeOutput writes one key into a node's outputs map
// (persisted with the workflow state).
func (a *Agent) writeWorkflowNodeOutput(workflowID, nodeID, key string, value any) error {
	state, err := a.workflowState(workflowID)
	if err != nil {
		return err
	}
	updated := false
	for i := range state.Nodes {
		if state.Nodes[i].ID != nodeID {
			continue
		}
		if state.Nodes[i].Outputs == nil {
			state.Nodes[i].Outputs = map[string]any{}
		}
		state.Nodes[i].Outputs[key] = value
		state.Nodes[i].UpdatedAt = time.Now().UTC()
		updated = true
		break
	}
	if !updated {
		return fmt.Errorf("workflow node not found: %s", nodeID)
	}
	return a.workflows.save(state)
}

// humanReplyText renders the submitted value as the node result text.
func humanReplyText(value any) (string, error) {
	if value == nil {
		return "", nil
	}
	switch v := value.(type) {
	case string:
		return v, nil
	default:
		data, err := json.Marshal(v)
		if err != nil {
			return "", fmt.Errorf("marshal human reply: %w", err)
		}
		return string(data), nil
	}
}

// CheckHumanTaskTimeouts lazily applies on_timeout for overdue pending tasks
// (P1.1): fail → the node errors out; llm → the node is re-scheduled as an
// llm_task fallback; escalate:<queue> → the task moves to the target queue
// and its deadline resets. Returns the tasks that changed.
func (a *Agent) CheckHumanTaskTimeouts(now time.Time) ([]HumanTaskView, error) {
	if a == nil || a.humanTasks == nil || a.workflows == nil {
		return nil, fmt.Errorf("human task store unavailable")
	}
	recs, err := a.humanTasks.listTasks("", humanTaskStatusPending)
	if err != nil {
		return nil, err
	}
	var changed []HumanTaskView
	for _, rec := range recs {
		if rec.DueAt.IsZero() || now.Before(rec.DueAt) {
			continue
		}
		rec.UpdatedAt = now
		switch {
		case rec.OnTimeout == "llm":
			// Re-schedule the blocked node as an llm fallback.
			if a.rescheduleHumanNodeAsLLM(rec.WorkflowID, rec.NodeID) == nil {
				rec.Status = humanTaskStatusEscalated
			} else {
				rec.Status = humanTaskStatusError
			}
		case strings.HasPrefix(rec.OnTimeout, "escalate:"):
			target := strings.TrimSpace(strings.TrimPrefix(rec.OnTimeout, "escalate:"))
			if target != "" {
				rec.Queue = target
				rec.Status = humanTaskStatusEscalated
				if rec.TimeoutMS > 0 {
					rec.DueAt = now.Add(time.Duration(rec.TimeoutMS) * time.Millisecond)
				}
			} else {
				rec.Status = humanTaskStatusError
			}
		default: // fail (or unspecified)
			if a.failWorkflowHumanNode(rec.WorkflowID, rec.NodeID, "human task timed out") == nil {
				rec.Status = humanTaskStatusError
			}
		}
		_ = a.humanTasks.saveTask(rec)
		changed = append(changed, humanTaskView(rec))
	}
	return changed, nil
}

// rescheduleHumanNodeAsLLM flips a blocked user_input node into an llm_task
// (pending) so the next scheduler scan starts a reasoning fallback job.
func (a *Agent) rescheduleHumanNodeAsLLM(workflowID, nodeID string) error {
	state, err := a.workflowState(workflowID)
	if err != nil {
		return err
	}
	found := false
	for i := range state.Nodes {
		if state.Nodes[i].ID != nodeID {
			continue
		}
		state.Nodes[i].Kind = agentGraphNodeLLMTask
		state.Nodes[i].HumanSpec = nil
		state.Nodes[i].Status = workflowStatusPending
		state.Nodes[i].UpdatedAt = time.Now().UTC()
		found = true
		break
	}
	if !found {
		return fmt.Errorf("workflow node not found: %s", nodeID)
	}
	if err := a.workflows.save(state); err != nil {
		return err
	}
	_, err = a.startWorkflowReadyNodes(context.Background(), workflowID)
	return err
}

// failWorkflowHumanNode moves a blocked user_input node to error.
func (a *Agent) failWorkflowHumanNode(workflowID, nodeID, reason string) error {
	state, err := a.workflowState(workflowID)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	found := false
	for i := range state.Nodes {
		if state.Nodes[i].ID != nodeID {
			continue
		}
		state.Nodes[i].Status = workflowStatusError
		state.Nodes[i].Error = reason
		state.Nodes[i].UpdatedAt = now
		state.Nodes[i].FinishedAt = now
		found = true
		break
	}
	if !found {
		return fmt.Errorf("workflow node not found: %s", nodeID)
	}
	state.Summary.UpdatedAt = now
	a.refreshWorkflowStatus(&state)
	return a.workflows.save(state)
}
