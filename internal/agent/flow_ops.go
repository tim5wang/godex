// Flow Spec v1 public operations on the agent: versioned definition CRUD,
// validate/publish, and FlowRun lifecycle. HTTP layer and backend Service
// call these; the durable engine bodies live in the workflow store.
package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/core/flow"
)

// FlowVersionView is the public projection of one stored flow version.
type FlowVersionView struct {
	FlowID      string           `json:"flow_id"`
	Version     string           `json:"version"`
	Status      string           `json:"status"`
	Name        string           `json:"name,omitempty"`
	Description string           `json:"description,omitempty"`
	Nodes       int              `json:"nodes"`
	Edges       int              `json:"edges"`
	Digest      string           `json:"digest,omitempty"`
	CreatedAt   time.Time        `json:"created_at"`
	UpdatedAt   time.Time        `json:"updated_at"`
	Definition  *flow.Definition `json:"definition,omitempty"`
}

// FlowSummaryView is the flow-level summary (id + current status lanes).
type FlowSummaryView struct {
	FlowID    string `json:"flow_id"`
	Draft     string `json:"draft,omitempty"`
	Gray      string `json:"gray,omitempty"`
	Published string `json:"published,omitempty"`
}

// FlowRunView is the public projection of a FlowRun record.
type FlowRunView struct {
	RunID      string         `json:"run_id"`
	FlowID     string         `json:"flow_id"`
	Version    string         `json:"version"`
	Digest     string         `json:"digest"`
	Status     string         `json:"status"`
	WorkflowID string         `json:"workflow_id,omitempty"`
	Inputs     map[string]any `json:"inputs,omitempty"`
	Outputs    map[string]any `json:"outputs,omitempty"`
	Error      string         `json:"error,omitempty"`
	StartedAt  time.Time      `json:"started_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
	FinishedAt time.Time      `json:"finished_at,omitempty"`
}

// FlowCreateArgs is the body of flow creation/update.
type FlowCreateArgs struct {
	FlowID   string          `json:"flow_id"`
	Version  string          `json:"version"`
	Status   string          `json:"status,omitempty"`
	Def      *flow.Definition `json:"definition"`
}

// CreateFlow stores a new draft version of a flow (validated + compiled at
// save time so validate/publish are cheap and the artifact is on disk).
//
// An empty draft (definition nil or nodes empty) is allowed: the flow object
// can be created first with only flow_id/version, then filled in later via
// the flow UI (templates / visual editor / natural language). Such a version
// has no compiled artifact and cannot be published or run until it is.
func (a *Agent) CreateFlow(args FlowCreateArgs) (FlowVersionView, error) {
	if a == nil || a.flows == nil {
		return FlowVersionView{}, fmt.Errorf("flow store unavailable")
	}
	def := args.Def
	flowID := strings.TrimSpace(args.FlowID)
	if flowID == "" && def != nil {
		flowID = strings.TrimSpace(def.FlowID)
	}
	if flowID == "" {
		return FlowVersionView{}, fmt.Errorf("missing flow_id")
	}
	version := strings.TrimSpace(args.Version)
	if version == "" && def != nil {
		version = strings.TrimSpace(def.Version)
	}
	if version == "" {
		return FlowVersionView{}, fmt.Errorf("missing version")
	}
	// Empty drafts (no nodes) are stored without a compiled artifact.
	var compiled *flow.Compiled
	if def != nil && len(def.Nodes) > 0 {
		c, err := flow.Compile(def)
		if err != nil {
			return FlowVersionView{}, err
		}
		compiled = c
	}
	status := strings.TrimSpace(args.Status)
	if status == "" {
		status = FlowStatusDraft
	}
	if def == nil {
		def = &flow.Definition{FlowID: flowID, Version: version, Status: status, Nodes: []flow.Node{}, Edges: []flow.Edge{}}
	}
	now := time.Now().UTC()
	if err := a.flows.saveVersion(flowID, flowVersionRecord{
		Version:   version,
		Status:    status,
		Flow:      def,
		Compiled:  compiled,
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		return FlowVersionView{}, err
	}
	if err := a.flows.setStatusLane(flowID, status, version); err != nil {
		return FlowVersionView{}, err
	}
	return a.GetFlowVersion(flowID, version)
}

// GetFlowVersion returns one stored version (definition included).
func (a *Agent) GetFlowVersion(flowID, version string) (FlowVersionView, error) {
	if a == nil || a.flows == nil {
		return FlowVersionView{}, fmt.Errorf("flow store unavailable")
	}
	rec, err := a.flows.loadVersion(flowID, version)
	if err != nil {
		return FlowVersionView{}, err
	}
	return flowVersionView(rec), nil
}

// ListFlows returns flow ids with their status lanes.
func (a *Agent) ListFlows() ([]FlowSummaryView, error) {
	if a == nil || a.flows == nil {
		return nil, fmt.Errorf("flow store unavailable")
	}
	ids, err := a.flows.listFlows()
	if err != nil {
		return nil, err
	}
	out := make([]FlowSummaryView, 0, len(ids))
	for _, id := range ids {
		cur, err := a.flows.loadCurrent(id)
		if err != nil {
			continue
		}
		out = append(out, FlowSummaryView{FlowID: id, Draft: cur.Draft, Gray: cur.Gray, Published: cur.Published})
	}
	return out, nil
}

// ListFlowVersions returns all versions of one flow.
func (a *Agent) ListFlowVersions(flowID string) ([]FlowVersionView, error) {
	if a == nil || a.flows == nil {
		return nil, fmt.Errorf("flow store unavailable")
	}
	recs, err := a.flows.listVersions(flowID)
	if err != nil {
		return nil, err
	}
	out := make([]FlowVersionView, 0, len(recs))
	for _, r := range recs {
		out = append(out, flowVersionView(r))
	}
	return out, nil
}

// ValidateFlow re-validates a stored (or supplied) definition without saving.
func (a *Agent) ValidateFlow(def *flow.Definition) (string, error) {
	if def == nil {
		return "", fmt.Errorf("missing flow definition")
	}
	compiled, err := flow.Compile(def)
	if err != nil {
		return "", err
	}
	return compiled.Digest, nil
}

// PublishFlow moves a draft/gray version to the published lane (gray keeps
// the gray lane; published also sets published). Only draft/gray can publish.
func (a *Agent) PublishFlow(flowID, version string) (FlowVersionView, error) {
	if a == nil || a.flows == nil {
		return FlowVersionView{}, fmt.Errorf("flow store unavailable")
	}
	rec, err := a.flows.loadVersion(flowID, version)
	if err != nil {
		return FlowVersionView{}, err
	}
	if rec.Status != FlowStatusDraft && rec.Status != FlowStatusGray {
		return FlowVersionView{}, fmt.Errorf("version %s status %q cannot be published", version, rec.Status)
	}
	// An empty draft (no compiled artifact) cannot be published: it has no
	// runnable definition yet.
	if rec.Compiled == nil {
		return FlowVersionView{}, fmt.Errorf("version %s has no runnable definition (empty flow): fill in nodes first", version)
	}
	rec.Status = FlowStatusPublished
	rec.UpdatedAt = time.Now().UTC()
	if err := a.flows.saveVersion(flowID, rec); err != nil {
		return FlowVersionView{}, err
	}
	if err := a.flows.setStatusLane(flowID, FlowStatusPublished, version); err != nil {
		return FlowVersionView{}, err
	}
	return a.GetFlowVersion(flowID, version)
}

// ResolveRunVersion picks the version to run: explicit version, else the
// published lane, else gray, else draft. Returns the record.
func (a *Agent) ResolveRunVersion(flowID, version string) (flowVersionRecord, error) {
	if a == nil || a.flows == nil {
		return flowVersionRecord{}, fmt.Errorf("flow store unavailable")
	}
	if v := strings.TrimSpace(version); v != "" {
		return a.flows.loadVersion(flowID, v)
	}
	cur, err := a.flows.loadCurrent(flowID)
	if err != nil {
		return flowVersionRecord{}, err
	}
	for _, v := range []string{cur.Published, cur.Gray, cur.Draft} {
		if v != "" {
			return a.flows.loadVersion(flowID, v)
		}
	}
	return flowVersionRecord{}, fmt.Errorf("flow %s has no versions", flowID)
}

// CreateFlowRun starts a FlowRun: resolves the version, compiles to engine
// inputs, creates the durable workflow and writes the run record. It returns
// the run view with a running workflow ready for StartFlowRun.
func (a *Agent) CreateFlowRun(ctx context.Context, flowID, version string, inputs map[string]any) (FlowRunView, error) {
	if a == nil || a.flows == nil || a.workflows == nil {
		return FlowRunView{}, fmt.Errorf("flow runtime unavailable")
	}
	rec, err := a.ResolveRunVersion(flowID, version)
	if err != nil {
		return FlowRunView{}, err
	}
	if rec.Compiled == nil {
		return FlowRunView{}, fmt.Errorf("version %s has no compiled artifact", rec.Version)
	}
	nodes, edges, err := compileFlowToWorkflowInputs(rec.Compiled)
	if err != nil {
		return FlowRunView{}, err
	}
	// Each run gets its own durable workflow (unique workflow_id), so
	// repeated runs of the same version never collide.
	runID := "fr_" + fmt.Sprintf("%d", time.Now().UTC().UnixNano())
	workflowID := "fl_" + flowID + "_" + rec.Version + "_" + runID
	sessionID := flowSessionID(ctx)
	state, err := a.workflows.create(sessionID, workflowID, nodes, edges)
	if err != nil {
		return FlowRunView{}, err
	}
	// P2.3: persist the run inputs on the workflow summary so prompt variable
	// references {{inputs.<name>}} resolve at run time.
	if len(inputs) > 0 {
		state.Summary.RunInputs = inputs
		if err := a.workflows.save(state); err != nil {
			return FlowRunView{}, err
		}
	}
	now := time.Now().UTC()
	runRec := flowRunRecord{
		RunID:      runID,
		FlowID:     flowID,
		Version:    rec.Version,
		Digest:     rec.Compiled.Digest,
		SessionID:  sessionID,
		WorkflowID: state.Summary.ID,
		Status:     workflowStatusPending,
		Inputs:     inputs,
		StartedAt:  now,
		UpdatedAt:  now,
	}
	if err := a.flows.saveRun(flowID, runRec); err != nil {
		return FlowRunView{}, err
	}
	return flowRunView(runRec), nil
}

// StartFlowRun starts the durable workflow of a run (ready nodes execute).
func (a *Agent) StartFlowRun(ctx context.Context, flowID, runID string) (FlowRunView, error) {
	if a == nil || a.flows == nil {
		return FlowRunView{}, fmt.Errorf("flow runtime unavailable")
	}
	rec, err := a.flows.loadRun(flowID, runID)
	if err != nil {
		return FlowRunView{}, err
	}
	if rec.WorkflowID == "" {
		return FlowRunView{}, fmt.Errorf("run %s has no workflow", runID)
	}
	view, err := a.startWorkflowReadyNodes(ctx, rec.WorkflowID)
	if err != nil {
		return FlowRunView{}, err
	}
	rec.Status = view.Status
	rec.UpdatedAt = time.Now().UTC()
	if err := a.flows.saveRun(flowID, rec); err != nil {
		return FlowRunView{}, err
	}
	return flowRunView(rec), nil
}

// StepFlowView is the single-step debug view: run identity + the refreshed
// workflow node states (status/outputs per node) so the UI can render the
// per-node context variables after each step.
type StepFlowView struct {
	RunID      string         `json:"run_id"`
	FlowID     string         `json:"flow_id"`
	Status     string         `json:"status"`
	Started    string         `json:"started,omitempty"`
	Nodes      []NodeStepView `json:"nodes"`
	Terminal   bool           `json:"terminal"`
}

// NodeStepView is one node's debug state.
type NodeStepView struct {
	ID       string         `json:"id"`
	Kind     string         `json:"kind"`
	Title    string         `json:"title,omitempty"`
	Status   string         `json:"status"`
	Outputs  map[string]any `json:"outputs,omitempty"`
	Error    string         `json:"error,omitempty"`
	Decision *workflowDecisionResult `json:"decision,omitempty"`
}

// StepFlowRun advances a debug run by exactly one node (single-stepping) and
// returns the refreshed per-node state for the debug panel. Returns an error
// when the run is terminal.
func (a *Agent) StepFlowRun(ctx context.Context, flowID, runID string) (*StepFlowView, error) {
	if a == nil || a.flows == nil {
		return nil, fmt.Errorf("flow runtime unavailable")
	}
	rec, err := a.flows.loadRun(flowID, runID)
	if err != nil {
		return nil, err
	}
	if rec.WorkflowID == "" {
		return nil, fmt.Errorf("run %s has no workflow", runID)
	}
	view, err := a.stepWorkflow(ctx, rec.WorkflowID)
	if err != nil {
		return nil, err
	}
	rec.Status = view.Status
	rec.UpdatedAt = time.Now().UTC()
	if err := a.flows.saveRun(flowID, rec); err != nil {
		return nil, err
	}
	out := &StepFlowView{
		RunID:    runID,
		FlowID:   flowID,
		Status:   view.Status,
		Started:  firstStarted(view.Started),
		Terminal: workflowTerminalStatus(view.Status),
		Nodes:    make([]NodeStepView, 0, len(view.Nodes)),
	}
	for _, n := range view.Nodes {
		out.Nodes = append(out.Nodes, NodeStepView{
			ID:       n.ID,
			Kind:     n.Kind,
			Title:    n.Title,
			Status:   n.Status,
			Outputs:  n.Outputs,
			Error:    n.Error,
			Decision: n.Decision,
		})
	}
	return out, nil
}

// firstStarted returns the first started node id from a step run.
func firstStarted(ids []string) string {
	if len(ids) == 0 {
		return ""
	}
	return ids[0]
}

// workflowTerminalStatus reports whether a workflow summary status is terminal.
func workflowTerminalStatus(status string) bool {
	switch status {
	case workflowStatusCompleted, workflowStatusError, workflowStatusCanceled:
		return true
	default:
		return false
	}
}

// WaitFlowRun waits for the run's workflow to reach a terminal state.
func (a *Agent) WaitFlowRun(ctx context.Context, flowID, runID string, timeoutMS int) (FlowRunView, error) {
	if a == nil || a.flows == nil {
		return FlowRunView{}, fmt.Errorf("flow runtime unavailable")
	}
	rec, err := a.flows.loadRun(flowID, runID)
	if err != nil {
		return FlowRunView{}, err
	}
	if rec.WorkflowID == "" {
		return FlowRunView{}, fmt.Errorf("run %s has no workflow", runID)
	}
	if timeoutMS <= 0 {
		timeoutMS = 60000
	}
	if _, err := a.waitWorkflow(ctx, rec.WorkflowID, "all", timeoutMS); err != nil {
		return FlowRunView{}, err
	}
	return a.RefreshFlowRun(flowID, runID)
}

// RefreshFlowRun re-reads the run record (status synced from the workflow).
func (a *Agent) RefreshFlowRun(flowID, runID string) (FlowRunView, error) {
	if a == nil || a.flows == nil {
		return FlowRunView{}, fmt.Errorf("flow runtime unavailable")
	}
	rec, err := a.flows.loadRun(flowID, runID)
	if err != nil {
		return FlowRunView{}, err
	}
	if rec.WorkflowID != "" {
		if state, err := a.workflowState(rec.WorkflowID); err == nil {
			rec.Status = state.Summary.Status
			rec.UpdatedAt = time.Now().UTC()
			rec.FinishedAt = state.Summary.UpdatedAt
			_ = a.flows.saveRun(flowID, rec)
		}
	}
	// Deliver the on_complete webhook once the run reaches a terminal state
	// (P1.3). Guarded by rec.WebhookSent for at-most-once semantics.
	a.maybeSendOnComplete(flowID, runID, &rec)
	return flowRunView(rec), nil
}

// CancelFlowRun cancels the run's workflow (all nodes).
func (a *Agent) CancelFlowRun(ctx context.Context, flowID, runID string) (FlowRunView, error) {
	if a == nil || a.flows == nil {
		return FlowRunView{}, fmt.Errorf("flow runtime unavailable")
	}
	rec, err := a.flows.loadRun(flowID, runID)
	if err != nil {
		return FlowRunView{}, err
	}
	if rec.WorkflowID != "" {
		if _, err := a.cancelWorkflowNode(ctx, rec.WorkflowID, ""); err != nil {
			return FlowRunView{}, err
		}
	}
	rec.Status = workflowStatusCanceled
	rec.UpdatedAt = time.Now().UTC()
	rec.FinishedAt = rec.UpdatedAt
	if err := a.flows.saveRun(flowID, rec); err != nil {
		return FlowRunView{}, err
	}
	// Cancel is a terminal state: deliver the on_complete webhook directly
	// (RefreshFlowRun cannot infer cancel from the workflow state because
	// completed + canceled node mix resolves to "running").
	a.maybeSendOnComplete(flowID, runID, &rec)
	return flowRunView(rec), nil
}

// ListFlowRuns returns all runs of one flow (newest first).
func (a *Agent) ListFlowRuns(flowID string) ([]FlowRunView, error) {
	if a == nil || a.flows == nil {
		return nil, fmt.Errorf("flow runtime unavailable")
	}
	recs, err := a.flows.listRuns(flowID)
	if err != nil {
		return nil, err
	}
	out := make([]FlowRunView, 0, len(recs))
	for i := len(recs) - 1; i >= 0; i-- {
		out = append(out, flowRunView(recs[i]))
	}
	return out, nil
}

// FlowRunEvents returns the append-only event log of a run's workflow
// (created/start/handoff/agent_graph_run/... as JSON lines). It is the data
// source for the SSE stream endpoint (P1.2).
func (a *Agent) FlowRunEvents(flowID, runID string) ([]map[string]any, error) {
	if a == nil || a.flows == nil || a.workflows == nil {
		return nil, fmt.Errorf("flow runtime unavailable")
	}
	rec, err := a.flows.loadRun(flowID, runID)
	if err != nil {
		return nil, err
	}
	if rec.WorkflowID == "" {
		return nil, fmt.Errorf("run %s has no workflow", runID)
	}
	path := filepath.Join(a.workflows.dir, rec.WorkflowID, workflowEventsFile)
	return readWorkflowEvents(path), nil
}

func flowVersionView(rec flowVersionRecord) FlowVersionView {
	v := FlowVersionView{
		FlowID:      "",
		Version:     rec.Version,
		Status:      rec.Status,
		CreatedAt:   rec.CreatedAt,
		UpdatedAt:   rec.UpdatedAt,
		Definition:  rec.Flow,
	}
	if rec.Flow != nil {
		v.FlowID = rec.Flow.FlowID
		v.Name = rec.Flow.Name
		v.Description = rec.Flow.Description
		v.Nodes = len(rec.Flow.Nodes)
		v.Edges = len(rec.Flow.Edges)
	}
	if rec.Compiled != nil {
		v.Digest = rec.Compiled.Digest
	}
	return v
}

func flowRunView(rec flowRunRecord) FlowRunView {
	return FlowRunView{
		RunID:      rec.RunID,
		FlowID:     rec.FlowID,
		Version:    rec.Version,
		Digest:     rec.Digest,
		Status:     rec.Status,
		WorkflowID: rec.WorkflowID,
		Inputs:     rec.Inputs,
		Outputs:    rec.Outputs,
		Error:      rec.Error,
		StartedAt:  rec.StartedAt,
		UpdatedAt:  rec.UpdatedAt,
		FinishedAt: rec.FinishedAt,
	}
}

// flowSessionID extracts the session id from ctx (empty in headless runs).
func flowSessionID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	// Best-effort: reuse the subagent event target's session when present.
	return subagentEventTargetFromContext(ctx).sessionID
}
