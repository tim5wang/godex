package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/tim5wang/godex/internal/workerruntime"
)

type localGoDexWorkerRuntime struct {
	agent *Agent
}

func (r localGoDexWorkerRuntime) Dispatch(ctx context.Context, req workerruntime.JobRequest) (workerruntime.JobHandle, error) {
	a := r.agent
	if a == nil || a.subagentJobs == nil {
		return workerruntime.JobHandle{}, fmt.Errorf("local worker runtime unavailable")
	}
	req = req.Clone()
	if err := validateLocalWorkerID(req.WorkerID); err != nil {
		return workerruntime.JobHandle{}, err
	}
	start := subagentStartOptionsFromWorkerRequest(req, a.subagentMaxConcurrentJobs())
	if err := a.validateSubagentRequiredCapabilities(start.RequiredBundles, start.RequiredTools); err != nil {
		return workerruntime.JobHandle{}, err
	}
	start.ToolNames = appendRequiredSubagentTools(start.ToolNames, start.RequiredBundles, start.RequiredTools)
	if err := a.validateSubagentToolInheritance(start.ToolNames); err != nil {
		return workerruntime.JobHandle{}, err
	}
	start.WorkerID = localGoDexWorkerID
	job, err := a.subagentJobs.StartWithOptions(start)
	if err != nil {
		return workerruntime.JobHandle{}, err
	}
	target := subagentEventTargetFromContext(ctx)
	a.subagentJobs.RegisterTarget(job.ID, target)
	target.emitIdentity(job)
	if job.Status == subagentStatusPending {
		target.emit(job, "pending", "Subagent job queued.", "", "", "", "")
		return workerHandleFromSubagentJob(job), nil
	}
	target.emit(job, "started", "Subagent job started.", "", "", "", "")
	jobID := job.ID
	job, err = a.prepareSubagentWorkspace(job)
	if err != nil {
		finished, _ := a.subagentJobs.Finish(jobID, subagentStatusError, "", err.Error())
		target.emit(finished, string(subagentStatusError), "Subagent workspace preparation failed.", "", "", err.Error(), "")
		a.startPendingSubagents(target.sink)
		return workerruntime.JobHandle{}, err
	}
	target.emit(job, "worktree_prepared", "Subagent isolated workspace prepared.", "", "", "", "")
	a.runSubagentJobAsync(job.ID, target)
	return workerHandleFromSubagentJob(job), nil
}

func (r localGoDexWorkerRuntime) Resume(ctx context.Context, ref workerruntime.JobRef) (workerruntime.JobHandle, error) {
	a := r.agent
	if a == nil || a.subagentJobs == nil {
		return workerruntime.JobHandle{}, fmt.Errorf("local worker runtime unavailable")
	}
	if err := validateLocalWorkerID(ref.WorkerID); err != nil {
		return workerruntime.JobHandle{}, err
	}
	job, err := a.subagentJobs.ResumeWithLimit(ref.JobID, a.subagentMaxConcurrentJobs())
	if err != nil {
		return workerruntime.JobHandle{}, err
	}
	target := subagentEventTargetFromContext(ctx)
	a.subagentJobs.RegisterTarget(job.ID, target)
	if job.Status == subagentStatusPending {
		target.emit(job, "pending", "Subagent job queued for resume.", "", "", "", "")
		return workerHandleFromSubagentJob(job), nil
	}
	target.emit(job, "resumed", "Subagent job resumed.", "", "", "", "")
	if err := a.ensureSubagentWorkspace(job); err != nil {
		finished, _ := a.subagentJobs.Finish(job.ID, subagentStatusError, "", err.Error())
		target.emit(finished, string(subagentStatusError), "Subagent isolated workspace is unavailable.", "", "", err.Error(), "")
		a.startPendingSubagents(target.sink)
		return workerruntime.JobHandle{}, err
	}
	a.runSubagentJobAsync(job.ID, target)
	return workerHandleFromSubagentJob(job), nil
}

func (r localGoDexWorkerRuntime) Cancel(ctx context.Context, ref workerruntime.JobRef) (workerruntime.JobHandle, error) {
	a := r.agent
	if a == nil || a.subagentJobs == nil {
		return workerruntime.JobHandle{}, fmt.Errorf("local worker runtime unavailable")
	}
	if err := validateLocalWorkerID(ref.WorkerID); err != nil {
		return workerruntime.JobHandle{}, err
	}
	job, err := a.subagentJobs.Cancel(ref.JobID)
	if err != nil {
		return workerruntime.JobHandle{}, err
	}
	subagentEventTargetFromContext(ctx).emit(job, "canceled", "Subagent job canceled.", "", "", job.Error, "")
	return workerHandleFromSubagentJob(job), nil
}

func (r localGoDexWorkerRuntime) Review(ctx context.Context, req workerruntime.ReviewRequest) (workerruntime.ReviewResult, error) {
	_ = ctx
	a := r.agent
	if a == nil || a.subagentJobs == nil {
		return workerruntime.ReviewResult{}, fmt.Errorf("local worker runtime unavailable")
	}
	if err := validateLocalWorkerID(req.WorkerID); err != nil {
		return workerruntime.ReviewResult{}, err
	}
	review, err := a.reviewDurableSubagentDirect(req.JobID)
	if err != nil {
		return workerruntime.ReviewResult{}, err
	}
	return workerReviewFromSubagentReview(review), nil
}

func (r localGoDexWorkerRuntime) Merge(ctx context.Context, req workerruntime.MergeRequest) (workerruntime.MergeResult, error) {
	a := r.agent
	if a == nil || a.subagentJobs == nil {
		return workerruntime.MergeResult{}, fmt.Errorf("local worker runtime unavailable")
	}
	if err := validateLocalWorkerID(req.WorkerID); err != nil {
		return workerruntime.MergeResult{}, err
	}
	result, err := a.mergeDurableSubagentDirect(ctx, req.JobID)
	if err != nil {
		return workerruntime.MergeResult{}, err
	}
	return workerMergeFromSubagentMerge(result), nil
}

func validateLocalWorkerID(workerID string) error {
	workerID = strings.TrimSpace(workerID)
	if workerID != "" && workerID != localGoDexWorkerID {
		return fmt.Errorf("unsupported worker %q", workerID)
	}
	return nil
}
