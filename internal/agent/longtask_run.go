package agent

import (
	"context"
	"fmt"
	"sync"
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
	summary := &longTaskRunSummary{Status: workflowStatusRunning, MaxIterations: maxIterations}
	var view longTaskView
	for i := 0; i < maxIterations; i++ {
		summary.Iterations = i + 1
		current, err := a.longTaskStatus(workflowID)
		if err != nil {
			return longTaskView{}, err
		}
		if longTaskAllStoriesPass(current) {
			current.Run = summary
			current.Run.Status = workflowStatusCompleted
			current.Run.Message = "all stories passed"
			return current, nil
		}
		if blockedBy, message := longTaskBlockedStory(current); blockedBy != "" {
			if args.AutoRepair {
				repaired, repairView, err := a.appendLongTaskRepair(ctx, workflowID, current, blockedBy, message, maxRepairAttempts)
				if err != nil {
					return longTaskView{}, err
				}
				if repaired.RepairNodeID != "" {
					summary.Repaired = append(summary.Repaired, repaired)
					view = repairView
					continue
				}
			}
			current.Run = summary
			current.Run.Status = "blocked"
			current.Run.BlockedBy = blockedBy
			current.Run.Message = message
			return current, nil
		}

		progressed := false
		for _, story := range current.Stories {
			if story.Status == workflowStatusCompleted && normalizeWorkflowVerdict(story.Verdict) == workflowVerdictPass && story.ValidationStatus == longTaskValidationPending {
				finalized, err := a.finalizeLongTaskStory(ctx, workflowID, firstNonEmpty(story.NodeID, story.ID))
				if err != nil {
					return longTaskView{}, err
				}
				summary.Finalized = append(summary.Finalized, story.ID)
				view = finalized
				progressed = true
				if blockedBy, message := longTaskBlockedStory(finalized); blockedBy != "" {
					if args.AutoRepair {
						repaired, repairView, err := a.appendLongTaskRepair(ctx, workflowID, finalized, blockedBy, message, maxRepairAttempts)
						if err != nil {
							return longTaskView{}, err
						}
						if repaired.RepairNodeID != "" {
							summary.Repaired = append(summary.Repaired, repaired)
							view = repairView
							progressed = true
							continue
						}
					}
					finalized.Run = summary
					finalized.Run.Status = "blocked"
					finalized.Run.BlockedBy = blockedBy
					finalized.Run.Message = message
					return finalized, nil
				}
				if longTaskAllStoriesPass(finalized) {
					finalized.Run = summary
					finalized.Run.Status = workflowStatusCompleted
					finalized.Run.Message = "all stories passed"
					return finalized, nil
				}
			}
		}
		if progressed {
			continue
		}

		if current.Running > 0 {
			waited, err := a.waitLongTask(ctx, workflowID, "all", waitTimeoutMS)
			if err != nil {
				return longTaskView{}, err
			}
			view = waited
			progressed = true
			continue
		}

		started, err := a.startLongTask(ctx, workflowID)
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
			progressed = true
			continue
		}
		view = started
		if !progressed {
			view.Run = summary
			view.Run.Status = "stalled"
			view.Run.Message = "no ready stories, running jobs, or pending validations"
			return view, nil
		}
	}
	if view.LongTaskID == "" {
		var err error
		view, err = a.longTaskStatus(workflowID)
		if err != nil {
			return longTaskView{}, err
		}
	}
	view.Run = summary
	view.Run.Status = "max_iterations"
	view.Run.Message = "reached max_iterations before completion"
	return view, nil
}
