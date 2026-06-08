package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// longTaskAsyncRunState tracks an in-progress async run.
type longTaskAsyncRunState struct {
	mu      sync.Mutex
	view    longTaskView
	err     error
	running bool
	done    chan struct{}
}

// longTaskAsyncRuns is a package-level store for async run state keyed by workflowID.
var longTaskAsyncRuns sync.Map

// timeNow is a small indirection so tests in this file can stub the clock
// without dragging in a global mock framework.
var timeNow = func() time.Time { return time.Now().UTC() }

// randomID returns a short pseudo-random suffix for run ids. crypto/rand
// would be overkill; the worst case is a name collision in a single
// workflow, which the caller can retry.
func randomID() string {
	const alphabet = "0123456789abcdef"
	now := time.Now().UTC().UnixNano()
	const mask = 0xf
	out := make([]byte, 12)
	for i := 0; i < 12; i++ {
		out[i] = alphabet[(now>>uint(i*4))&mask]
	}
	return string(out)
}

func containsString_(items []string, want string) bool {
	for _, it := range items {
		if it == want {
			return true
		}
	}
	return false
}

func slicesContainString(items []string, want string) bool { return containsString_(items, want) }

// stopOnFailureDefault is the default for the longtask run loop when the
// caller does not set StopOnFailure. Default true: any blocked story stops
// the run, matching the documented "任一失败 = 停" semantics.
const stopOnFailureDefault = true

func stopOnFailure(args longTaskArgs) bool {
	if args.StopOnFailure == nil {
		return stopOnFailureDefault
	}
	return *args.StopOnFailure
}

// longTaskRunAction is one step a longtask run loop can take. The state machine
// is intentionally single-step so the run loop can react to outcomes after
// every action (instead of batching multiple finalizes + starts per scan).
type longTaskRunAction int

const (
	longTaskActionNone longTaskRunAction = iota
	longTaskActionFinalizeStory
	longTaskActionWaitRunning
	longTaskActionStartOne
	longTaskActionRepair
	longTaskActionCompleted
	longTaskActionBlocked
	longTaskActionStalled
)

func (a *Agent) longTaskStatus(workflowID string) (longTaskView, error) {
	state, err := a.workflowState(workflowID)
	if err != nil {
		return longTaskView{}, err
	}
	return a.longTaskViewForState(state)
}

func (a *Agent) startLongTask(ctx context.Context, workflowID string) (longTaskView, error) {
	workflow, err := a.startWorkflowReadyNodes(ctx, workflowID)
	if err != nil {
		return longTaskView{}, err
	}
	view, err := a.longTaskViewForWorkflow(workflow)
	if err != nil {
		return longTaskView{}, err
	}
	view.Started = append([]string{}, workflow.Started...)
	return view, nil
}

func (a *Agent) waitLongTask(ctx context.Context, workflowID, mode string, timeoutMS int) (longTaskView, error) {
	workflow, err := a.waitWorkflow(ctx, workflowID, mode, timeoutMS)
	if err != nil {
		return longTaskView{}, err
	}
	view, err := a.longTaskViewForWorkflow(workflow)
	if err != nil {
		return longTaskView{}, err
	}
	view.Wait = workflow.Wait
	return view, nil
}

func (a *Agent) runLongTask(ctx context.Context, workflowID string, args longTaskArgs) (longTaskView, error) {
	if args.Async {
		return a.startAsyncLongTask(ctx, workflowID, args)
	}
	return a.runLongTaskSync(ctx, workflowID, args)
}

func (a *Agent) startAsyncLongTask(ctx context.Context, workflowID string, args longTaskArgs) (longTaskView, error) {
	state := &longTaskAsyncRunState{running: true, done: make(chan struct{})}
	if _, loaded := longTaskAsyncRuns.LoadOrStore(workflowID, state); loaded {
		longTaskAsyncRuns.Delete(workflowID)
		return longTaskView{}, fmt.Errorf("async run already in progress for %s", workflowID)
	}
	go func() {
		defer close(state.done)
		view, err := a.runLongTaskSync(ctx, workflowID, args)
		state.mu.Lock()
		state.view = view
		state.err = err
		state.running = false
		state.mu.Unlock()
	}()
	current, err := a.longTaskStatus(workflowID)
	if err != nil {
		return longTaskView{}, err
	}
	current.Run = &longTaskRunSummary{Status: workflowStatusRunning, Message: "async run started"}
	return current, nil
}

func (a *Agent) longTaskRunStatus(workflowID string) (longTaskView, error) {
	val, ok := longTaskAsyncRuns.Load(workflowID)
	if !ok {
		return a.longTaskStatus(workflowID)
	}
	state := val.(*longTaskAsyncRunState)
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.running {
		current, err := a.longTaskStatus(workflowID)
		if err != nil {
			return longTaskView{}, err
		}
		current.Run = &longTaskRunSummary{Status: workflowStatusRunning, Message: "async run in progress"}
		return current, nil
	}
	if state.err != nil {
		return longTaskView{}, state.err
	}
	longTaskAsyncRuns.Delete(workflowID)
	return state.view, nil
}

// pickNextAction returns the single next action the run loop should take for
// the current view. It is intentionally narrow: one action per call so the
// run loop can react to outcomes (e.g. finalize -> start) without burning an
// extra iteration. Stop-on-failure and stalled detection are handled here.
func pickNextAction(view longTaskView, args longTaskArgs) (longTaskRunAction, string, string) {
	// Terminal-state wins: completed or blocked take priority.
	if longTaskAllStoriesPass(view) {
		return longTaskActionCompleted, "", ""
	}
	if blockedBy, message := longTaskBlockedStory(view); blockedBy != "" {
		if args.AutoRepair {
			// Repair is folded into the same blocked action; the run loop
			// calls appendLongTaskRepair before deciding to truly stop.
			return longTaskActionRepair, blockedBy, message
		}
		// Stop on failure: any blocked story stops the run.
		if stopOnFailure(args) {
			return longTaskActionBlocked, blockedBy, message
		}
		// Otherwise: continue with the rest of the run.
	}
	// Find a pending story that needs finalization (highest priority first).
	for _, story := range sortedStoriesByPriority(view.Stories) {
		if story.Status == workflowStatusCompleted &&
			normalizeWorkflowVerdict(story.Verdict) == workflowVerdictPass &&
			story.ValidationStatus == longTaskValidationPending {
			return longTaskActionFinalizeStory, story.ID, story.NodeID
		}
	}
	// If something is running, wait for it.
	if view.Running > 0 {
		return longTaskActionWaitRunning, "", ""
	}
	// Find a pending story whose deps are all completed and start exactly
	// one. Longtask is a serial story chain, never fan-out.
	for _, story := range sortedStoriesByPriority(view.Stories) {
		if story.Status != workflowStatusPending {
			continue
		}
		if !storyNodeIDPresent(view, story.ID) {
			continue
		}
		return longTaskActionStartOne, story.ID, story.NodeID
	}
	return longTaskActionStalled, "", ""
}

// sortedStoriesByPriority returns a copy of stories ordered by priority
// ascending (ties broken by story id for determinism).
func sortedStoriesByPriority(stories []longTaskStoryView) []longTaskStoryView {
	out := make([]longTaskStoryView, len(stories))
	copy(out, stories)
	sort.SliceStable(out, func(i, j int) bool {
		pi, pj := out[i].Priority, out[j].Priority
		if pi <= 0 {
			pi = 1 << 30
		}
		if pj <= 0 {
			pj = 1 << 30
		}
		if pi != pj {
			return pi < pj
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// storyNodeIDPresent returns true when the story has a backing workflow node
// that is still actionable. A story without a node id means it has not been
// materialized into the workflow yet, which is not the case for longtask
// (stories are compiled at create-time) but guards against drift.
func storyNodeIDPresent(view longTaskView, storyID string) bool {
	for _, node := range view.Workflow.Nodes {
		if node.ID == storyID || strings.HasPrefix(node.ID, storyID+"_repair_") {
			return true
		}
	}
	return false
}

// findWorkflowNodeByID locates a node in the workflow by id. Returns nil if
// not found. Used by longtask single-node start logic.
func findWorkflowNodeByID(state workflowState, nodeID string) *workflowNode {
	for i := range state.Nodes {
		if state.Nodes[i].ID == nodeID {
			return &state.Nodes[i]
		}
	}
	return nil
}

func (a *Agent) runLongTaskSync(ctx context.Context, workflowID string, args longTaskArgs) (longTaskView, error) {
	maxIterations := args.MaxIterations
	if maxIterations <= 0 {
		maxIterations = longTaskDefaultRunMaxIterations
	}
	waitTimeoutMS := args.WaitTimeoutMS
	if waitTimeoutMS <= 0 {
		waitTimeoutMS = args.TimeoutMS
	}
	if waitTimeoutMS <= 0 {
		waitTimeoutMS = longTaskDefaultWaitTimeoutMS
	}
	maxRepairAttempts := args.MaxRepairAttempts
	if maxRepairAttempts <= 0 {
		maxRepairAttempts = 2
	}

	// Resume: when caller supplies ResumeRunID we keep the existing run
	// id and re-use its progress (Started, Finalized, Repaired). The
	// state machine is otherwise identical to a fresh run.
	runID := strings.TrimSpace(args.ResumeRunID)
	rec := longTaskRunRecord{
		RunID:         runID,
		WorkflowID:    workflowID,
		SessionID:     strings.TrimSpace(args.SessionID),
		StartedAt:     timeNow(),
		UpdatedAt:     timeNow(),
		Status:        workflowStatusRunning,
		MaxIterations: maxIterations,
		Async:         args.Async,
	}
	if runID != "" {
		existing, err := a.workflows.loadLongTaskRun(workflowID, runID)
		if err != nil {
			return longTaskView{}, fmt.Errorf("resume longtask run: %w", err)
		}
		existing.Status = workflowStatusRunning
		existing.UpdatedAt = timeNow()
		existing.SessionID = firstNonEmpty(existing.SessionID, rec.SessionID)
		rec = existing
		rec.MaxIterations = maxIterations
	} else {
		rec.RunID = "run_" + randomID()
		_ = a.workflows.writeLongTaskRun(rec)
	}

	summary := &longTaskRunSummary{Status: workflowStatusRunning, MaxIterations: maxIterations}
	// Carry over progress from any resumed run so that user-visible
	// counters in the run summary reflect the full lifetime of the run.
	summary.Iterations = rec.Iterations
	summary.Started = append([]string{}, rec.Started...)
	summary.Finalized = append([]string{}, rec.Finalized...)
	summary.Repaired = append([]longTaskRepairSummary{}, rec.Repaired...)
	rec.LastRefluxKey = rec.LastRefluxKey // preserve across resume

	finalize := func(view longTaskView, status, message, blockedBy string) (longTaskView, error) {
		now := timeNow()
		view.Run = summary
		view.Run.Status = status
		view.Run.Message = message
		view.Run.BlockedBy = blockedBy
		rec.Status = status
		rec.UpdatedAt = now
		rec.Iterations = summary.Iterations
		rec.Started = summary.Started
		rec.Finalized = summary.Finalized
		rec.Repaired = summary.Repaired
		rec.BlockedBy = blockedBy
		rec.Message = message
		_ = a.workflows.writeLongTaskRun(rec)
		return view, nil
	}

	// On ctx cancellation (HTTP client disconnect, Ctrl+C, godex shutdown)
	// preserve progress and return an "interrupted" view so callers know
	// to resume later.
	defer func() {
		if ctx.Err() == nil {
			return
		}
		now := timeNow()
		rec.Status = "interrupted"
		rec.UpdatedAt = now
		rec.Iterations = summary.Iterations
		rec.Started = summary.Started
		rec.Finalized = summary.Finalized
		rec.Repaired = summary.Repaired
		rec.Message = firstNonEmpty(rec.Message, "context canceled")
		_ = a.workflows.writeLongTaskRun(rec)
	}()

	var view longTaskView
	for i := 0; i < maxIterations; i++ {
		if ctx.Err() != nil {
			view, _ = a.longTaskStatus(workflowID)
			return finalize(view, "interrupted", firstNonEmpty(summary.Message, "context canceled"), summary.BlockedBy)
		}
		summary.Iterations = i + 1
		rec.Iterations = summary.Iterations
		rec.UpdatedAt = timeNow()
		_ = a.workflows.writeLongTaskRun(rec)

		current, err := a.longTaskStatus(workflowID)
		if err != nil {
			return longTaskView{}, err
		}
		action, storyID, arg := pickNextAction(current, args)
		switch action {
		case longTaskActionCompleted:
			return finalize(current, workflowStatusCompleted, "all stories passed", "")
		case longTaskActionBlocked:
			return finalize(current, "blocked", arg, storyID)
		case longTaskActionStalled:
			return finalize(current, "stalled", "no ready stories, running jobs, or pending validations", "")
		case longTaskActionFinalizeStory:
			finalized, err := a.finalizeLongTaskStory(ctx, workflowID, firstNonEmpty(arg, storyID))
			if err != nil {
				return longTaskView{}, err
			}
			summary.Finalized = append(summary.Finalized, storyID)
			view = finalized
			continue
		case longTaskActionWaitRunning:
			waited, err := a.waitLongTask(ctx, workflowID, "all", waitTimeoutMS)
			if err != nil {
				return longTaskView{}, err
			}
			view = waited
			continue
		case longTaskActionStartOne:
			nodeID := firstNonEmpty(arg, storyID)
			// On resume, do not re-start a story that was already started
			// in the previous run. The story may be running, completed,
			// or already finalized; pickNextAction + the workflow store
			// decide whether the node is actionable.
			if containsString_(summary.Started, nodeID) {
				view, _ = a.longTaskStatus(workflowID)
				continue
			}
			started, err := a.startLongTaskOneNode(ctx, workflowID, nodeID)
			if err != nil {
				return longTaskView{}, err
			}
			if len(started.Started) > 0 {
				summary.Started = append(summary.Started, started.Started...)
				waited, err := a.waitLongTask(ctx, workflowID, "all", waitTimeoutMS)
				if err != nil {
					return longTaskView{}, err
				}
				view = waited
			} else {
				view = started
			}
			continue
		case longTaskActionRepair:
			repaired, repairView, err := a.appendLongTaskRepair(ctx, workflowID, current, storyID, arg, maxRepairAttempts)
			if err != nil {
				return longTaskView{}, err
			}
			if repaired.RepairNodeID != "" {
				summary.Repaired = append(summary.Repaired, repaired)
				view = repairView
				continue
			}
			if stopOnFailure(args) {
				return finalize(current, "blocked", arg, storyID)
			}
			view = current
			continue
		}
	}
	if view.LongTaskID == "" {
		var err error
		view, err = a.longTaskStatus(workflowID)
		if err != nil {
			return longTaskView{}, err
		}
	}
	return finalize(view, "max_iterations", "reached max_iterations before completion", "")
}

// startLongTaskOneNode starts exactly one story node in the workflow. It
// does not fan out: even if multiple deps-complete stories are pending, only
// the requested story id is started. This is what gives longtask its
// serial-story semantics on top of the parallel-ready workflow runtime.
func (a *Agent) startLongTaskOneNode(ctx context.Context, workflowID, storyID string) (longTaskView, error) {
	state, err := a.workflowState(workflowID)
	if err != nil {
		return longTaskView{}, err
	}
	target := findWorkflowNodeByID(state, storyID)
	if target == nil {
		return a.longTaskViewForState(state)
	}
	if target.Status != workflowStatusPending {
		return a.longTaskViewForState(state)
	}
	if !workflowDepsCompleted(state.Nodes, target.DependsOn) {
		return a.longTaskViewForState(state)
	}
	if _, ok := a.startWorkflowNode(ctx, &state, target); ok {
		state.Summary.UpdatedAt = timeNow()
		a.refreshWorkflowStatus(&state)
		if _, err := a.processWorkflowEdges(&state); err != nil {
			return longTaskView{}, err
		}
		if err := a.workflows.save(state); err != nil {
			return longTaskView{}, err
		}
		_ = a.workflows.appendEvent(state.Summary.ID, map[string]interface{}{
			"event":     "start",
			"started":   []string{storyID},
			"longtask":  true,
			"at":        timeNow(),
		})
	}
	view, err := a.longTaskViewForState(state)
	if err != nil {
		return longTaskView{}, err
	}
	if target.Status == workflowStatusRunning {
		view.Started = []string{storyID}
	}
	return view, nil
}
