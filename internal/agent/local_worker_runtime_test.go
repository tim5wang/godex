package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/core/protocol"
	"github.com/tim5wang/godex/internal/workerruntime"
)

type fakeWorkerRuntime struct {
	dispatchReq workerruntime.JobRequest
	resumeRef   workerruntime.JobRef
	cancelRef   workerruntime.JobRef
	reviewReq   workerruntime.ReviewRequest
	mergeReq    workerruntime.MergeRequest
}

func (f *fakeWorkerRuntime) Dispatch(ctx context.Context, req workerruntime.JobRequest) (workerruntime.JobHandle, error) {
	_ = ctx
	f.dispatchReq = req.Clone()
	return workerruntime.JobHandle{JobID: "job-fake", WorkerID: localGoDexWorkerID, Status: workerruntime.StatusRunning}, nil
}

func (f *fakeWorkerRuntime) Resume(ctx context.Context, ref workerruntime.JobRef) (workerruntime.JobHandle, error) {
	_ = ctx
	f.resumeRef = ref
	return workerruntime.JobHandle{JobID: ref.JobID, WorkerID: localGoDexWorkerID, Status: workerruntime.StatusRunning}, nil
}

func (f *fakeWorkerRuntime) Cancel(ctx context.Context, ref workerruntime.JobRef) (workerruntime.JobHandle, error) {
	_ = ctx
	f.cancelRef = ref
	return workerruntime.JobHandle{JobID: ref.JobID, WorkerID: localGoDexWorkerID, Status: workerruntime.StatusCanceled}, nil
}

func (f *fakeWorkerRuntime) Review(ctx context.Context, req workerruntime.ReviewRequest) (workerruntime.ReviewResult, error) {
	_ = ctx
	f.reviewReq = req
	return workerruntime.ReviewResult{JobID: req.JobID, WorkerID: localGoDexWorkerID}, nil
}

func (f *fakeWorkerRuntime) Merge(ctx context.Context, req workerruntime.MergeRequest) (workerruntime.MergeResult, error) {
	_ = ctx
	f.mergeReq = req
	return workerruntime.MergeResult{JobID: req.JobID, WorkerID: localGoDexWorkerID, Status: subagentMergeNoChanges}, nil
}

func TestStartDurableSubagentUsesWorkerRuntime(t *testing.T) {
	a := newTestAgent(t, 4096)
	fake := &fakeWorkerRuntime{}
	a.workerRuntime = fake

	job, err := a.StartDurableSubagent("inspect", "Explore", nil)
	if err != nil {
		t.Fatalf("start durable subagent: %v", err)
	}
	if job.ID != "job-fake" {
		t.Fatalf("job id %q", job.ID)
	}
	if fake.dispatchReq.Prompt != "inspect" || fake.dispatchReq.WorkerID != localGoDexWorkerID {
		t.Fatalf("unexpected dispatch request: %+v", fake.dispatchReq)
	}
}

func TestLocalWorkerRuntimeDispatchStartsDurableSubagent(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.client = repeatedTextCaller("worker done")

	handle, err := a.WorkerRuntime().Dispatch(context.Background(), workerruntime.JobRequest{
		WorkerID:  localGoDexWorkerID,
		AgentType: "general-purpose",
		Prompt:    "inspect worker runtime",
		Capabilities: workerruntime.CapabilitySet{
			ToolNames:  []string{"bash", "read_file", "write_file", "edit_file"},
			WriteScope: []string{"notes"},
			SandboxID:  a.SandboxID(),
		},
		MaxTurns: 3,
	})
	if err != nil {
		t.Fatalf("dispatch worker job: %v", err)
	}
	if handle.JobID == "" {
		t.Fatalf("expected job id")
	}
	completed := waitForSubagentStatus(t, a.subagentJobs, handle.JobID, subagentStatusCompleted)
	if completed.WorkerID != localGoDexWorkerID {
		t.Fatalf("worker id %q", completed.WorkerID)
	}
	if completed.Result != "worker done" {
		t.Fatalf("result %q", completed.Result)
	}
}

func TestLocalWorkerRuntimeReviewAndMerge(t *testing.T) {
	a := newTestAgent(t, 4096)
	initGitRepo(t, a.cfg.WorkspaceDir)
	a.client = &sequenceCaller{responses: []protocol.Response{
		{Content: []protocol.Block{
			protocol.ToolUseBlock("edit", "write_file", map[string]interface{}{"path": "notes/out.txt", "content": "worker\n"}),
		}},
		{Content: []protocol.Block{protocol.TextBlock("done")}},
	}}

	handle, err := a.WorkerRuntime().Dispatch(context.Background(), workerruntime.JobRequest{
		WorkerID:  localGoDexWorkerID,
		AgentType: "general-purpose",
		Prompt:    "write notes",
		Capabilities: workerruntime.CapabilitySet{
			ToolNames:  []string{"bash", "read_file", "write_file", "edit_file"},
			WriteScope: []string{"notes"},
			SandboxID:  a.SandboxID(),
		},
		MaxTurns: 5,
	})
	if err != nil {
		t.Fatalf("dispatch worker job: %v", err)
	}
	waitForSubagentStatus(t, a.subagentJobs, handle.JobID, subagentStatusCompleted)

	review, err := a.WorkerRuntime().Review(context.Background(), workerruntime.ReviewRequest{JobID: handle.JobID, WorkerID: localGoDexWorkerID})
	if err != nil {
		t.Fatalf("review worker job: %v", err)
	}
	if len(review.Changes) != 1 || review.Changes[0].Path != "notes/out.txt" {
		t.Fatalf("unexpected review changes: %+v", review.Changes)
	}

	merge, err := a.WorkerRuntime().Merge(context.Background(), workerruntime.MergeRequest{JobID: handle.JobID, WorkerID: localGoDexWorkerID})
	if err != nil {
		t.Fatalf("merge worker job: %v", err)
	}
	if merge.Status != subagentMergeMerged {
		t.Fatalf("merge status %q", merge.Status)
	}
}

func TestLocalWorkerRuntimeCancel(t *testing.T) {
	a := newTestAgent(t, 4096)
	release := make(chan struct{})
	a.client = blockingSubagentCaller{release: release}

	handle, err := a.WorkerRuntime().Dispatch(context.Background(), workerruntime.JobRequest{
		WorkerID:  localGoDexWorkerID,
		AgentType: "Explore",
		Prompt:    "wait",
		Capabilities: workerruntime.CapabilitySet{
			ToolNames: []string{"bash", "read_file"},
			SandboxID: a.SandboxID(),
		},
		MaxTurns: 3,
	})
	if err != nil {
		t.Fatalf("dispatch worker job: %v", err)
	}
	waitForSubagentActive(t, a.subagentJobs, handle.JobID)

	canceled, err := a.WorkerRuntime().Cancel(context.Background(), workerruntime.JobRef{JobID: handle.JobID, WorkerID: localGoDexWorkerID})
	if err != nil {
		t.Fatalf("cancel worker job: %v", err)
	}
	if canceled.Status != workerruntime.StatusCanceled {
		t.Fatalf("status %q", canceled.Status)
	}
	waitForSubagentInactive(t, a.subagentJobs, handle.JobID)
}

func TestLocalWorkerRuntimeRejectsOtherWorkerID(t *testing.T) {
	a := newTestAgent(t, 4096)
	_, err := a.WorkerRuntime().Dispatch(context.Background(), workerruntime.JobRequest{
		WorkerID:  "worker:remote:test",
		AgentType: "Explore",
		Prompt:    "inspect",
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported worker") {
		t.Fatalf("expected unsupported worker error, got %v", err)
	}
}

func TestLocalWorkerRuntimeRejectsOtherWorkerIDForControlMethods(t *testing.T) {
	a := newTestAgent(t, 4096)
	runtime := a.WorkerRuntime()
	ctx := context.Background()

	if _, err := runtime.Resume(ctx, workerruntime.JobRef{JobID: "job-1", WorkerID: "worker:remote:test"}); err == nil || !strings.Contains(err.Error(), "unsupported worker") {
		t.Fatalf("expected resume unsupported worker error, got %v", err)
	}
	if _, err := runtime.Cancel(ctx, workerruntime.JobRef{JobID: "job-1", WorkerID: "worker:remote:test"}); err == nil || !strings.Contains(err.Error(), "unsupported worker") {
		t.Fatalf("expected cancel unsupported worker error, got %v", err)
	}
	if _, err := runtime.Review(ctx, workerruntime.ReviewRequest{JobID: "job-1", WorkerID: "worker:remote:test"}); err == nil || !strings.Contains(err.Error(), "unsupported worker") {
		t.Fatalf("expected review unsupported worker error, got %v", err)
	}
	if _, err := runtime.Merge(ctx, workerruntime.MergeRequest{JobID: "job-1", WorkerID: "worker:remote:test"}); err == nil || !strings.Contains(err.Error(), "unsupported worker") {
		t.Fatalf("expected merge unsupported worker error, got %v", err)
	}
}

func TestLocalWorkerRuntimeDispatchValidatesRequiredTools(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()

	_, err := a.WorkerRuntime().Dispatch(context.Background(), workerruntime.JobRequest{
		WorkerID:  localGoDexWorkerID,
		AgentType: "Explore",
		Prompt:    "need inactive web",
		Capabilities: workerruntime.CapabilitySet{
			ToolNames:     []string{"read_file"},
			RequiredTools: []string{"web_search"},
			SandboxID:     a.SandboxID(),
		},
	})
	if err == nil || !strings.Contains(err.Error(), "web_search") {
		t.Fatalf("expected missing required tool validation, got %v", err)
	}
}

func waitForSubagentActive(t *testing.T, store *subagentJobStore, id string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		store.mu.Lock()
		_, active := store.cancels[id]
		store.mu.Unlock()
		if active {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for subagent %s to become active", id)
}

func waitForSubagentInactive(t *testing.T, store *subagentJobStore, id string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		store.mu.Lock()
		_, active := store.cancels[id]
		store.mu.Unlock()
		if !active {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for subagent %s to stop", id)
}
