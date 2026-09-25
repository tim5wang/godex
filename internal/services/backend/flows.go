// Flow Spec v1 service methods (F1b): thin wrappers over a workspace-scoped
// flow agent so the HTTP API and future consumers share one access path.
// Flow definitions, validation, publishing and runs are not bound to a
// specific session; the shared agent drives the durable workflow engine.
package backend

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/tim5wang/godex/internal/agent"
	"github.com/tim5wang/godex/internal/core/flow"
)

// flowsAgentMu guards the lazy workspace-scoped flow agent construction.
var flowsAgentMu sync.Mutex

// flowAgent returns a workspace-scoped agent used for flow management and
// run execution. It is rebuilt when the config changes; callers never retain
// it across requests.
func (s *Service) flowAgent() (*agent.Agent, error) {
	if s == nil || s.cfg == nil {
		return nil, fmt.Errorf("flow service unavailable")
	}
	flowsAgentMu.Lock()
	defer flowsAgentMu.Unlock()
	return agent.NewForSession(s.cfg, s.shared, ""), nil
}

// ListFlows returns all flows with their status lanes.
func (s *Service) ListFlows() ([]agent.FlowSummaryView, error) {
	a, err := s.flowAgent()
	if err != nil {
		return nil, err
	}
	return a.ListFlows()
}

// ListFlowVersions returns all versions of one flow.
func (s *Service) ListFlowVersions(flowID string) ([]agent.FlowVersionView, error) {
	a, err := s.flowAgent()
	if err != nil {
		return nil, err
	}
	return a.ListFlowVersions(flowID)
}

// DeleteFlowVersion removes one stored version (protected while it has
// active runs).
func (s *Service) DeleteFlowVersion(flowID, version string) error {
	a, err := s.flowAgent()
	if err != nil {
		return err
	}
	return a.DeleteFlowVersion(flowID, version)
}

// DeleteFlow removes a flow entirely (protected while it has active runs).
func (s *Service) DeleteFlow(flowID string) error {
	a, err := s.flowAgent()
	if err != nil {
		return err
	}
	return a.DeleteFlow(flowID)
}

// GetFlowVersion returns one stored version (definition included).
func (s *Service) GetFlowVersion(flowID, version string) (agent.FlowVersionView, error) {
	a, err := s.flowAgent()
	if err != nil {
		return agent.FlowVersionView{}, err
	}
	return a.GetFlowVersion(flowID, version)
}

// CreateFlow validates, compiles and stores a new flow version.
func (s *Service) CreateFlow(args agent.FlowCreateArgs) (agent.FlowVersionView, error) {
	a, err := s.flowAgent()
	if err != nil {
		return agent.FlowVersionView{}, err
	}
	return a.CreateFlow(args)
}

// ValidateFlow re-validates a supplied definition without saving.
func (s *Service) ValidateFlow(def *flow.Definition) (string, error) {
	a, err := s.flowAgent()
	if err != nil {
		return "", err
	}
	return a.ValidateFlow(def)
}

// GenerateFlowSpec drafts a Flow Spec v1 definition from a natural-language
// business description via the LLM (P2.5). The result is validated but not
// saved; the caller decides whether to persist it as a new version.
func (s *Service) GenerateFlowSpec(ctx context.Context, description string) (*flow.Definition, error) {
	a, err := s.flowAgent()
	if err != nil {
		return nil, err
	}
	return a.GenerateFlowSpec(ctx, description)
}

// AmendFlowSpec modifies an existing Flow Spec definition per a
// natural-language change request (multi-turn incremental editing). The FULL
// modified definition is returned, validated but NOT saved.
func (s *Service) AmendFlowSpec(ctx context.Context, current *flow.Definition, change string) (*flow.Definition, error) {
	a, err := s.flowAgent()
	if err != nil {
		return nil, err
	}
	return a.AmendFlowSpec(ctx, current, change)
}

// PublishFlow promotes a draft/gray version to published.
func (s *Service) PublishFlow(flowID, version string) (agent.FlowVersionView, error) {
	a, err := s.flowAgent()
	if err != nil {
		return agent.FlowVersionView{}, err
	}
	return a.PublishFlow(flowID, version)
}

// CreateFlowRun starts a run: resolves the version, creates the durable
// workflow and writes the run record.
func (s *Service) CreateFlowRun(ctx context.Context, flowID, version string, inputs map[string]any) (agent.FlowRunView, error) {
	a, err := s.flowAgent()
	if err != nil {
		return agent.FlowRunView{}, err
	}
	return a.CreateFlowRun(ctx, flowID, version, inputs)
}

// CreateFlowRunIdempotent creates or replays a gateway FlowRun using the
// durable idempotency metadata stored with its run record.
func (s *Service) CreateFlowRunIdempotent(ctx context.Context, flowID, version string, inputs map[string]any, keyHash, requestHash string) (agent.FlowRunView, bool, error) {
	a, err := s.flowAgent()
	if err != nil {
		return agent.FlowRunView{}, false, err
	}
	return a.CreateFlowRunIdempotent(ctx, flowID, version, inputs, keyHash, requestHash)
}

// StartFlowRun starts the run's durable workflow (ready nodes execute).
func (s *Service) StartFlowRun(ctx context.Context, flowID, runID string) (agent.FlowRunView, error) {
	a, err := s.flowAgent()
	if err != nil {
		return agent.FlowRunView{}, err
	}
	view, err := a.StartFlowRun(ctx, flowID, runID)
	if err == nil {
		s.trackFlowRun(agent.FlowRunRef{FlowID: flowID, RunID: runID}, view.Status)
	}
	return view, err
}

// AdvanceFlowRun is used by the backend lifecycle reconciler to resume an
// auto-scheduled run after a node completion or process restart.
func (s *Service) AdvanceFlowRun(ctx context.Context, flowID, runID string) (agent.FlowRunView, error) {
	a, err := s.flowAgent()
	if err != nil {
		return agent.FlowRunView{}, err
	}
	return a.AdvanceFlowRun(ctx, flowID, runID)
}

// WaitFlowRun waits for the run's workflow to reach a terminal state.
func (s *Service) WaitFlowRun(ctx context.Context, flowID, runID string, timeoutMS int) (agent.FlowRunView, error) {
	a, err := s.flowAgent()
	if err != nil {
		return agent.FlowRunView{}, err
	}
	return a.WaitFlowRun(ctx, flowID, runID, timeoutMS)
}

// RefreshFlowRun re-reads the run record (status synced from the workflow).
func (s *Service) RefreshFlowRun(flowID, runID string) (agent.FlowRunView, error) {
	a, err := s.flowAgent()
	if err != nil {
		return agent.FlowRunView{}, err
	}
	return a.RefreshFlowRun(flowID, runID)
}

// CancelFlowRun cancels the run's workflow.
func (s *Service) CancelFlowRun(ctx context.Context, flowID, runID string) (agent.FlowRunView, error) {
	a, err := s.flowAgent()
	if err != nil {
		return agent.FlowRunView{}, err
	}
	view, err := a.CancelFlowRun(ctx, flowID, runID)
	if err == nil {
		s.untrackFlowRun(agent.FlowRunRef{FlowID: flowID, RunID: runID})
	}
	return view, err
}

// ListFlowRuns returns all runs of one flow (newest first).
func (s *Service) ListFlowRuns(flowID string) ([]agent.FlowRunView, error) {
	a, err := s.flowAgent()
	if err != nil {
		return nil, err
	}
	return a.ListFlowRuns(flowID)
}

// ListHumanTasks returns the human task queue (newest first). Empty queue or
// status matches all.
func (s *Service) ListHumanTasks(queue, status string) ([]agent.HumanTaskView, error) {
	a, err := s.flowAgent()
	if err != nil {
		return nil, err
	}
	return a.ListHumanTasks(queue, status)
}

// ReplyFlowRunHuman completes the human node of a run with the submitted
// value, marks the task replied, and continues the run.
func (s *Service) ReplyFlowRunHuman(ctx context.Context, flowID, runID, nodeID string, value any) (agent.FlowRunView, error) {
	a, err := s.flowAgent()
	if err != nil {
		return agent.FlowRunView{}, err
	}
	return a.ReplyFlowRunHuman(ctx, flowID, runID, nodeID, value)
}

// CheckHumanTaskTimeouts lazily applies on_timeout for overdue pending human
// tasks and returns the tasks that changed.
func (s *Service) CheckHumanTaskTimeouts(now time.Time) ([]agent.HumanTaskView, error) {
	a, err := s.flowAgent()
	if err != nil {
		return nil, err
	}
	return a.CheckHumanTaskTimeouts(now)
}

// FlowRunEvents returns the append-only event log of a run's workflow (the
// data source for the SSE stream endpoint, P1.2).
func (s *Service) FlowRunEvents(flowID, runID string) ([]map[string]any, error) {
	a, err := s.flowAgent()
	if err != nil {
		return nil, err
	}
	return a.FlowRunEvents(flowID, runID)
}

// DiagnoseFlowRun packages a failed run's event log + definition, asks the
// LLM for a diagnosis (root cause + suggestions + optional fixed definition),
// and returns the validated result WITHOUT saving (P3 Agent 闭环 §22.2).
func (s *Service) DiagnoseFlowRun(ctx context.Context, flowID, runID string) (*agent.FlowDiagnosis, error) {
	a, err := s.flowAgent()
	if err != nil {
		return nil, err
	}
	return a.DiagnoseFlowRun(ctx, flowID, runID)
}

// StepFlowRun advances a debug run by exactly one node (single-stepping) and
// returns the refreshed per-node state with outputs/context for the debug UI.
func (s *Service) StepFlowRun(ctx context.Context, flowID, runID string) (*agent.StepFlowView, error) {
	a, err := s.flowAgent()
	if err != nil {
		return nil, err
	}
	return a.StepFlowRun(ctx, flowID, runID)
}

// InspectFlows aggregates run health across published flows over a window
// (P3 Agent 闭环 §22.2 定期巡检).
func (s *Service) InspectFlows(windowHours int) (*agent.FlowInspectionReport, error) {
	a, err := s.flowAgent()
	if err != nil {
		return nil, err
	}
	return a.InspectFlows(windowHours)
}
