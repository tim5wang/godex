package agent

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/core/conversation"
	"github.com/tim5wang/godex/internal/core/protocol"
	"github.com/tim5wang/godex/internal/domain/automation"
	"github.com/tim5wang/godex/internal/domain/events"
	"github.com/tim5wang/godex/internal/domain/message"
	"github.com/tim5wang/godex/internal/sandbox"
	"github.com/tim5wang/godex/internal/tools"
)

func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	runGit(t, dir, "init", "-q")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# test\n"), 0644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".godex/\n"), 0644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	runGit(t, dir, "add", "README.md", ".gitignore")
	runGit(t, dir, "commit", "-qm", "init")
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, string(out))
	}
}

type repeatedTextCaller string

func (c repeatedTextCaller) Call(ctx context.Context, req protocol.Request) (*protocol.Response, error) {
	_ = ctx
	_ = req
	return &protocol.Response{Content: []protocol.Block{protocol.TextBlock(string(c))}}, nil
}

type blockingSubagentCaller struct {
	release <-chan struct{}
}

func (c blockingSubagentCaller) Call(ctx context.Context, req protocol.Request) (*protocol.Response, error) {
	_ = req
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.release:
		return &protocol.Response{Content: []protocol.Block{protocol.TextBlock("released handoff")}}, nil
	}
}

func TestDurableSubagentCompletesAndPersistsResult(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.client = &sequenceCaller{responses: []protocol.Response{
		{Content: []protocol.Block{protocol.TextBlock("subagent handoff")}},
	}}

	job, err := a.StartDurableSubagent("inspect the repo", "Explore", nil)
	if err != nil {
		t.Fatalf("start durable subagent: %v", err)
	}
	completed := waitForSubagentStatus(t, a.subagentJobs, job.ID, subagentStatusCompleted)
	if completed.Result != "subagent handoff" {
		t.Fatalf("expected persisted subagent result, got %+v", completed)
	}

	reloaded := newSubagentJobStore(a.subagentJobs.dir)
	persisted, err := reloaded.Get(job.ID)
	if err != nil {
		t.Fatalf("get persisted subagent: %v", err)
	}
	if persisted.Status != subagentStatusCompleted || persisted.Result != "subagent handoff" {
		t.Fatalf("expected completed persisted job, got %+v", persisted)
	}
}

func TestSubagentRequiredBundlesIgnoresGenericCurrentStatusPrompt(t *testing.T) {
	prompt := "Write a local product status note describing the current Task Center state. Do not browse."
	bundles := subagentRequiredBundles(prompt, nil)
	if len(bundles) != 0 {
		t.Fatalf("expected local writing prompt not to require bundles, got %+v", bundles)
	}

	bundles = subagentRequiredBundles("Do web research and include official source links.", nil)
	if !containsString(bundles, bundleWeb) {
		t.Fatalf("expected web research prompt to require web bundle, got %+v", bundles)
	}
}

func TestDurableSubagentRecordsSandboxID(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.client = repeatedTextCaller("done")
	got := make(chan events.Event, 8)
	ctx := withSubagentEventTarget(context.Background(), subagentEventTarget{
		sessionID: "session-sandbox",
		turnID:    "turn-sandbox",
		sink: events.SinkFunc(func(event events.Event) {
			got <- event
		}),
	})

	parentSandboxID := a.SandboxID()
	job, err := a.StartDurableSubagentWithContext(ctx, "inspect sandbox id", "general-purpose", []string{"notes"})
	if err != nil {
		t.Fatalf("start subagent: %v", err)
	}
	completed := waitForSubagentStatus(t, a.subagentJobs, job.ID, subagentStatusCompleted)
	if completed.SandboxID == "" {
		t.Fatalf("expected stored sandbox id")
	}
	if completed.SandboxID == parentSandboxID {
		t.Fatalf("expected worker sandbox id to reference prepared workspace, got parent sandbox id %q", completed.SandboxID)
	}
	if want := sandbox.StableLocalID(completed.WorktreeDir); completed.SandboxID != want {
		t.Fatalf("stored sandbox id %q, want %q", completed.SandboxID, want)
	}

	view := durableSubagentJobView(completed)
	if view.SandboxID != completed.SandboxID {
		t.Fatalf("view sandbox id %q, want %q", view.SandboxID, completed.SandboxID)
	}
	foundEventSandboxID := false
	deadline := time.After(2 * time.Second)
	for !foundEventSandboxID {
		select {
		case event := <-got:
			if event.Type != events.EventSubagentJobUpdated {
				continue
			}
			payload, _ := event.Payload.(events.SubagentJobPayload)
			if payload.SandboxID == completed.SandboxID {
				foundEventSandboxID = true
			}
		case <-deadline:
			t.Fatalf("expected subagent event payload sandbox id %q", completed.SandboxID)
		}
	}
}

func TestDurableSubagentExposesWorkerID(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.client = repeatedTextCaller("done")
	got := make(chan events.Event, 8)
	ctx := withSubagentEventTarget(context.Background(), subagentEventTarget{
		sessionID: "session-worker",
		turnID:    "turn-worker",
		sink: events.SinkFunc(func(event events.Event) {
			got <- event
		}),
	})

	job, err := a.StartDurableSubagentWithContext(ctx, "inspect worker id", "Explore", nil)
	if err != nil {
		t.Fatalf("start durable subagent: %v", err)
	}
	completed := waitForSubagentStatus(t, a.subagentJobs, job.ID, subagentStatusCompleted)
	if completed.WorkerID != localGoDexWorkerID {
		t.Fatalf("worker id %q", completed.WorkerID)
	}
	view := durableSubagentJobView(completed)
	if view.WorkerID != localGoDexWorkerID {
		t.Fatalf("view worker id %q", view.WorkerID)
	}
	model := formatSubagentModelJob(completed)
	if model.WorkerID != localGoDexWorkerID {
		t.Fatalf("model worker id %q", model.WorkerID)
	}

	foundEventWorkerID := false
	deadline := time.After(2 * time.Second)
	for !foundEventWorkerID {
		select {
		case event := <-got:
			if event.Type != events.EventSubagentJobUpdated {
				continue
			}
			payload, _ := event.Payload.(events.SubagentJobPayload)
			if payload.WorkerID == localGoDexWorkerID {
				foundEventWorkerID = true
			}
		case <-deadline:
			t.Fatalf("expected subagent event payload worker id %q", localGoDexWorkerID)
		}
	}
}

func TestDurableSubagentPersistsSessionGraphSource(t *testing.T) {
	store := newSubagentJobStore(filepath.Join(t.TempDir(), "subagents"))
	job, err := store.StartWithOptions(subagentStartOptions{
		SessionID:  "session-graph",
		AgentType:  "general-purpose",
		Prompt:     "inspect graph branch",
		ToolNames:  []string{"todo_read"},
		MaxTurns:   1,
		WorkerID:   localGoDexWorkerID,
		SandboxID:  "sandbox:local:test",
		ParentID:   "turn-graph",
		BasePrompt: "base",
		RuntimeContext: automation.SessionContext{Metadata: map[string]string{
			subagentSessionGraphBranchMetadataKey: "branch:main",
			subagentSessionGraphNodeMetadataKey:   "node:checkpoint:one",
		}},
	})
	if err != nil {
		t.Fatalf("start subagent: %v", err)
	}
	if job.SourceBranchID != "branch:main" {
		t.Fatalf("expected source branch to persist, got %q", job.SourceBranchID)
	}
	if !strings.HasPrefix(job.WorkerBranchID, "branch:"+job.ID) {
		t.Fatalf("expected worker branch to derive from job id, got %q", job.WorkerBranchID)
	}
	if job.SourceNodeID != "node:checkpoint:one" {
		t.Fatalf("expected source node to persist, got %q", job.SourceNodeID)
	}
	reloaded, err := store.Get(job.ID)
	if err != nil {
		t.Fatalf("reload job: %v", err)
	}
	view := durableSubagentJobView(reloaded)
	if view.SourceBranchID != job.SourceBranchID || view.SourceNodeID != job.SourceNodeID || view.WorkerBranchID != job.WorkerBranchID {
		t.Fatalf("expected graph fields in view, got source=%q node=%q worker=%q", view.SourceBranchID, view.SourceNodeID, view.WorkerBranchID)
	}
	model := formatSubagentModelJob(reloaded)
	if model.SourceBranchID != job.SourceBranchID || model.SourceNodeID != job.SourceNodeID || model.WorkerBranchID != job.WorkerBranchID {
		t.Fatalf("expected graph fields in model view, got source=%q node=%q worker=%q", model.SourceBranchID, model.SourceNodeID, model.WorkerBranchID)
	}
}

func TestDurableSubagentOmitsSessionGraphFieldsWithoutContext(t *testing.T) {
	store := newSubagentJobStore(filepath.Join(t.TempDir(), "subagents"))
	job, err := store.StartWithOptions(subagentStartOptions{
		SessionID:  "session-no-graph",
		AgentType:  "general-purpose",
		Prompt:     "no graph context",
		ToolNames:  []string{"todo_read"},
		MaxTurns:   1,
		WorkerID:   localGoDexWorkerID,
		BasePrompt: "base",
	})
	if err != nil {
		t.Fatalf("start subagent: %v", err)
	}
	if job.SourceBranchID != "" || job.SourceNodeID != "" || job.WorkerBranchID != "" {
		t.Fatalf("expected graph fields to be empty without context, got source=%q node=%q worker=%q", job.SourceBranchID, job.SourceNodeID, job.WorkerBranchID)
	}
}

func TestWorkerRuntimePreservesRequiredToolValidation(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()

	_, err := a.startDurableSubagentWithContext(context.Background(), durableSubagentStartRequest{
		Prompt:        "need inactive web",
		AgentType:     "Explore",
		RequiredTools: []string{"web_search"},
	})
	if err == nil || !strings.Contains(err.Error(), "web_search") {
		t.Fatalf("expected missing required tool validation, got %v", err)
	}
}

func TestSubagentModelViewIncludesSandboxID(t *testing.T) {
	job := &subagentJob{
		ID:        "job-1",
		SandboxID: "sandbox:local:worker",
		Status:    subagentStatusCompleted,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	view := formatSubagentModelJob(job)
	if view.SandboxID != "sandbox:local:worker" {
		t.Fatalf("model view sandbox id %q", view.SandboxID)
	}
}

func TestDurableSubagentEmitsAndPersistsProgress(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.client = &sequenceCaller{responses: []protocol.Response{
		{Content: []protocol.Block{protocol.TextBlock("subagent progress handoff")}},
	}}
	got := make(chan events.Event, 8)
	ctx := withSubagentEventTarget(context.Background(), subagentEventTarget{
		sessionID: "session-progress",
		turnID:    "turn-progress",
		sink: events.SinkFunc(func(event events.Event) {
			got <- event
		}),
	})

	job, err := a.StartDurableSubagentWithContext(ctx, "inspect progress", "Explore", nil)
	if err != nil {
		t.Fatalf("start durable subagent: %v", err)
	}
	completed := waitForSubagentStatus(t, a.subagentJobs, job.ID, subagentStatusCompleted)
	if len(completed.Progress) < 3 {
		t.Fatalf("expected started, assistant, and completed progress, got %+v", completed.Progress)
	}

	phases := collectSubagentEventPhases(t, got, 4)
	for _, want := range []string{"started", "worktree_prepared", "assistant_message", "completed"} {
		if !containsString(phases, want) {
			t.Fatalf("expected progress phase %q in events, got %v", want, phases)
		}
	}
}

func TestDurableSubagentViewIncludesStableDisplayMetadata(t *testing.T) {
	store := newSubagentJobStore(t.TempDir())
	first, err := store.StartWithOptions(subagentStartOptions{
		SessionID:    "session-display",
		ParentTurnID: "turn-display",
		AgentType:    "reviewer",
		RoleID:       "gstack/plan-eng-review",
		Prompt:       "Review the architecture and produce a concise report.\n\nFocus on risk.",
		ToolNames:    []string{"read_file"},
		MaxTurns:     3,
	})
	if err != nil {
		t.Fatalf("start first subagent: %v", err)
	}
	second, err := store.StartWithOptions(subagentStartOptions{
		SessionID:    "session-display",
		ParentTurnID: "turn-display",
		AgentType:    "writer",
		RoleID:       "gstack/pdf-writer",
		Prompt:       "Convert the final report to PDF.",
		ToolNames:    []string{"write_file"},
		MaxTurns:     3,
	})
	if err != nil {
		t.Fatalf("start second subagent: %v", err)
	}

	firstView := durableSubagentJobView(first)
	secondView := durableSubagentJobView(second)
	if firstView.Sequence != 1 || secondView.Sequence != 2 {
		t.Fatalf("expected stable sequence 1/2, got first=%+v second=%+v", firstView, secondView)
	}
	if firstView.Objective != "Review the architecture and produce a concise report." {
		t.Fatalf("expected deterministic objective, got %+v", firstView)
	}
	if !strings.Contains(firstView.DisplayTitle, "#1") || !strings.Contains(firstView.DisplayTitle, "gstack/plan-eng-review") || !strings.Contains(firstView.DisplayTitle, firstView.Objective) {
		t.Fatalf("expected display title with sequence, role, and objective, got %+v", firstView)
	}
}

func TestDurableSubagentPersistsLoopGuardFeedback(t *testing.T) {
	a := newTestAgent(t, 4096)
	repeated := protocol.Response{Content: []protocol.Block{
		protocol.ToolUseBlock("tool-read", "read_file", map[string]interface{}{"path": "missing.txt"}),
	}}
	a.client = &sequenceCaller{responses: []protocol.Response{
		repeated,
		repeated,
		repeated,
		repeated,
		{Content: []protocol.Block{protocol.TextBlock("subagent changed strategy")}},
	}}
	got := make(chan events.Event, 128)
	target := subagentEventTarget{
		sessionID: "session-sub-loop",
		turnID:    "turn-sub-loop",
		sink: events.SinkFunc(func(event events.Event) {
			select {
			case got <- event:
			default:
			}
		}),
	}
	job, err := a.subagentJobs.Start("Explore", "inspect missing file", []string{"read_file"}, nil, 10)
	if err != nil {
		t.Fatalf("start subagent: %v", err)
	}

	a.runSubagentJob(context.Background(), job.ID, target)
	completed, err := a.subagentJobs.Get(job.ID)
	if err != nil {
		t.Fatalf("get completed subagent: %v", err)
	}
	if completed.Status != subagentStatusCompleted {
		t.Fatalf("expected completed subagent, got %+v", completed)
	}
	foundFeedback := false
	for _, msg := range completed.Messages {
		if strings.Contains(protocol.MessageText(msg), "loop_guard_recovery") {
			foundFeedback = true
			break
		}
	}
	if !foundFeedback {
		t.Fatalf("expected loop guard feedback in subagent messages, got %+v", completed.Messages)
	}
	foundProgress := false
	for _, progress := range completed.Progress {
		if progress.Phase == "loop_guard_recovery" {
			foundProgress = true
			break
		}
	}
	if !foundProgress {
		t.Fatalf("expected loop guard recovery progress, got %+v", completed.Progress)
	}
	view := durableSubagentJobView(completed)
	if view.ModelRequestCount == 0 || view.ToolCallCount == 0 || view.LastRunnerPhase == "" || view.LastIteration == 0 || view.LastRecoveryHint == "" {
		t.Fatalf("expected loop diagnostics in job view, got %+v", view)
	}
	foundRecoveryHintProgress := false
	for _, progress := range view.Progress {
		if progress.Phase == conversation.PhaseRecoveryAttempt && strings.Contains(progress.RecoveryHint, "change strategy") {
			foundRecoveryHintProgress = true
			break
		}
	}
	if !foundRecoveryHintProgress {
		t.Fatalf("expected recovery hint in progress view, got %+v", view.Progress)
	}
	foundPhase := false
	foundPayloadDiagnostics := false
	deadline := time.After(2 * time.Second)
	for !(foundPhase && foundPayloadDiagnostics) {
		select {
		case event := <-got:
			if event.Type == events.EventRunnerPhaseChanged {
				payload, _ := event.Payload.(events.RunnerPhasePayload)
				foundPhase = payload.Phase == conversation.PhaseRecoveryAttempt && strings.Contains(payload.Message, "loop_guard_recovery")
			}
			if event.Type == events.EventSubagentJobUpdated {
				payload, _ := event.Payload.(events.SubagentJobPayload)
				if payload.LastRecoveryHint != "" && payload.ModelRequestCount > 0 && payload.ToolCallCount > 0 && payload.Sequence == 1 && payload.Objective != "" && payload.DisplayTitle != "" {
					foundPayloadDiagnostics = true
				}
			}
		case <-deadline:
			t.Fatalf("expected loop guard recovery phase and payload diagnostics, phase=%v diagnostics=%v", foundPhase, foundPayloadDiagnostics)
		}
	}
}

func TestDurableSubagentWorktreeToolCreatesPendingApproval(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.client = &sequenceCaller{responses: []protocol.Response{
		{Content: []protocol.Block{
			protocol.ToolUseBlock("tool-shell", "bash", map[string]interface{}{"command": "command -v sh"}),
		}},
	}}
	ctx := tools.WithSessionContext(context.Background(), automation.SessionContext{
		SessionID: "web-subagent-permission",
		Source:    string(message.SourceWeb),
		Sender:    "user",
	})
	ctx = withSubagentEventTarget(ctx, subagentEventTarget{sessionID: "web-subagent-permission", turnID: "turn-subagent-permission"})
	job, err := a.StartDurableSubagentWithContext(ctx, "run a protected shell command", "general-purpose", []string{"notes"})
	if err != nil {
		t.Fatalf("start durable subagent: %v", err)
	}
	pending := waitForSubagentStatus(t, a.subagentJobs, job.ID, subagentStatusPendingApproval)
	if !strings.Contains(pending.Error, "requires approval") {
		t.Fatalf("expected pending approval error on subagent, got %+v", pending)
	}
	items := a.PendingPermissions("web-subagent-permission")
	if len(items) != 1 {
		t.Fatalf("expected one pending permission, got %+v", items)
	}
	if items[0].Request.ToolName != "bash" || items[0].Request.SessionID != "web-subagent-permission" || items[0].Request.Sender != "subagent:"+job.ID {
		t.Fatalf("unexpected pending permission request: %+v", items[0])
	}
}

func TestDurableSubagentLoadMarksRunningJobInterrupted(t *testing.T) {
	store := newSubagentJobStore(t.TempDir())
	job, err := store.Start("Explore", "keep working", []string{"read_file"}, nil, 3)
	if err != nil {
		t.Fatalf("seed running subagent: %v", err)
	}

	reloaded := newSubagentJobStore(store.dir)
	got, err := reloaded.Get(job.ID)
	if err != nil {
		t.Fatalf("get reloaded job: %v", err)
	}
	if got.Status != subagentStatusInterrupted {
		t.Fatalf("expected running job to reload as interrupted, got %+v", got)
	}
	if got.Error == "" {
		t.Fatalf("expected interrupted job to keep diagnostic error, got %+v", got)
	}
}

func TestDurableSubagentLoadMarksPendingJobInterrupted(t *testing.T) {
	store := newSubagentJobStore(t.TempDir())
	if _, err := store.StartWithOptions(subagentStartOptions{
		AgentType:     "Explore",
		Prompt:        "running work",
		ToolNames:     []string{"read_file"},
		MaxTurns:      3,
		MaxConcurrent: 1,
	}); err != nil {
		t.Fatalf("seed running subagent: %v", err)
	}
	job, err := store.StartWithOptions(subagentStartOptions{
		AgentType:     "Explore",
		Prompt:        "queued work",
		ToolNames:     []string{"read_file"},
		MaxTurns:      3,
		MaxConcurrent: 1,
	})
	if err != nil {
		t.Fatalf("seed pending subagent: %v", err)
	}
	if job.Status != subagentStatusPending {
		t.Fatalf("expected second job to be pending, got %+v", job)
	}

	reloaded := newSubagentJobStore(store.dir)
	got, err := reloaded.Get(job.ID)
	if err != nil {
		t.Fatalf("get reloaded job: %v", err)
	}
	if got.Status != subagentStatusInterrupted {
		t.Fatalf("expected pending job to reload as interrupted, got %+v", got)
	}
}

func TestSubagentJobStoreMigratesLegacySingleFile(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	legacy := subagentJob{
		ID:        "subagent_legacy",
		AgentType: "Explore",
		Prompt:    "legacy prompt",
		ToolNames: []string{"read_file"},
		Status:    subagentStatusCompleted,
		Result:    "legacy handoff",
		Messages: []protocol.Message{
			protocol.NewTextMessage(protocol.RoleUser, "legacy prompt"),
			protocol.NewTextMessage(protocol.RoleAssistant, "legacy handoff"),
		},
		Progress: []subagentProgressEvent{{
			Time:    now,
			Phase:   string(subagentStatusCompleted),
			Message: "done",
		}},
		MaxTurns:  3,
		CreatedAt: now,
		UpdatedAt: now,
	}
	data, err := json.MarshalIndent(legacy, "", "  ")
	if err != nil {
		t.Fatalf("marshal legacy job: %v", err)
	}
	legacyPath := filepath.Join(dir, legacy.ID+".json")
	if err := os.WriteFile(legacyPath, data, 0644); err != nil {
		t.Fatalf("write legacy job: %v", err)
	}

	store := newSubagentJobStore(dir)
	got, err := store.Get(legacy.ID)
	if err != nil {
		t.Fatalf("get migrated job: %v", err)
	}
	if got.Result != "legacy handoff" || len(got.Messages) != 2 || len(got.Progress) != 1 {
		t.Fatalf("expected migrated job to keep result/messages/progress, got %+v", got)
	}
	for _, path := range []string{
		filepath.Join(dir, legacy.ID, subagentSummaryFile),
		filepath.Join(dir, legacy.ID, subagentMessagesFile),
		filepath.Join(dir, legacy.ID, subagentProgressFile),
		filepath.Join(dir, subagentLegacyDir, legacy.ID+".json"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected migrated file %s: %v", path, err)
		}
	}
	if _, err := os.Stat(legacyPath); !os.IsNotExist(err) {
		t.Fatalf("expected legacy file to be archived, stat err=%v", err)
	}
	var summary map[string]interface{}
	if err := readJSONFile(filepath.Join(dir, legacy.ID, subagentSummaryFile), &summary); err != nil {
		t.Fatalf("read summary: %v", err)
	}
	if _, ok := summary["messages"]; ok {
		t.Fatalf("summary should not contain raw messages: %+v", summary)
	}
	if _, ok := summary["progress"]; ok {
		t.Fatalf("summary should not contain raw progress: %+v", summary)
	}
	if summary["message_count"] != float64(2) || summary["progress_count"] != float64(1) {
		t.Fatalf("expected summary counts, got %+v", summary)
	}
}

func collectSubagentEventPhases(t *testing.T, ch <-chan events.Event, want int) []string {
	t.Helper()
	deadline := time.After(2 * time.Second)
	phases := make([]string, 0, want)
	for len(phases) < want {
		select {
		case event := <-ch:
			if event.Type != events.EventSubagentJobUpdated {
				continue
			}
			payload, ok := event.Payload.(events.SubagentJobPayload)
			if !ok {
				t.Fatalf("expected subagent payload, got %#v", event.Payload)
			}
			phases = append(phases, payload.Phase)
		case <-deadline:
			t.Fatalf("timed out waiting for subagent progress events, got %v", phases)
		}
	}
	return phases
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func TestDurableSubagentResumeInterruptedJob(t *testing.T) {
	a := newTestAgent(t, 4096)
	seed, err := a.subagentJobs.Start("Explore", "resume this", []string{"read_file"}, nil, 3)
	if err != nil {
		t.Fatalf("seed running subagent: %v", err)
	}
	a.subagentJobs = newSubagentJobStore(a.subagentJobs.dir)
	interrupted, err := a.subagentJobs.Get(seed.ID)
	if err != nil {
		t.Fatalf("get interrupted job: %v", err)
	}
	if interrupted.Status != subagentStatusInterrupted {
		t.Fatalf("expected interrupted job, got %+v", interrupted)
	}

	a.client = &sequenceCaller{responses: []protocol.Response{
		{Content: []protocol.Block{protocol.TextBlock("resumed handoff")}},
	}}
	if _, err := a.ResumeDurableSubagent(seed.ID); err != nil {
		t.Fatalf("resume subagent: %v", err)
	}
	completed := waitForSubagentStatus(t, a.subagentJobs, seed.ID, subagentStatusCompleted)
	if completed.Result != "resumed handoff" {
		t.Fatalf("expected resumed result, got %+v", completed)
	}
}

func TestDurableSubagentUsesIsolatedWorktreeAndMerge(t *testing.T) {
	a := newTestAgent(t, 4096)
	initGitRepo(t, a.cfg.WorkspaceDir)
	a.RegisterTools()
	a.client = &sequenceCaller{responses: []protocol.Response{
		{Content: []protocol.Block{
			protocol.ToolUseBlock("tool-write", "write_file", map[string]interface{}{
				"path":    "notes/result.txt",
				"content": "from subagent\n",
			}),
		}},
		{Content: []protocol.Block{protocol.TextBlock("handoff")}},
	}}

	job, err := a.StartDurableSubagent("write a note", "general-purpose", []string{"notes"})
	if err != nil {
		t.Fatalf("start durable subagent: %v", err)
	}
	completed := waitForSubagentStatus(t, a.subagentJobs, job.ID, subagentStatusCompleted)
	if completed.WorktreeDir == "" {
		t.Fatalf("expected isolated worktree, got %+v", completed)
	}
	if completed.Isolation != subagentIsolationGitWorktree {
		t.Fatalf("expected git worktree isolation, got %+v", completed)
	}
	if completed.GitBranch == "" {
		t.Fatalf("expected git branch metadata, got %+v", completed)
	}
	if _, err := os.Stat(filepath.Join(a.cfg.WorkspaceDir, "notes", "result.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected main workspace to remain untouched before merge, stat err=%v", err)
	}
	if data, err := os.ReadFile(filepath.Join(completed.WorktreeDir, "notes", "result.txt")); err != nil || string(data) != "from subagent\n" {
		t.Fatalf("expected worktree change, data=%q err=%v", string(data), err)
	}

	review, err := a.ReviewDurableSubagent(job.ID)
	if err != nil {
		t.Fatalf("review durable subagent: %v", err)
	}
	if len(review.Changes) != 1 || review.Changes[0].Path != "notes/result.txt" || review.Changes[0].Status != "added" {
		t.Fatalf("expected added notes/result.txt, got %+v", review.Changes)
	}
	if !strings.Contains(review.Diff, "from subagent") {
		t.Fatalf("expected review diff to include file contents, got %q", review.Diff)
	}

	merged, err := a.MergeDurableSubagent(job.ID)
	if err != nil {
		t.Fatalf("merge durable subagent: %v", err)
	}
	if merged.Status != subagentMergeMerged || len(merged.Applied) != 1 {
		t.Fatalf("expected merged result, got %+v", merged)
	}
	if data, err := os.ReadFile(filepath.Join(a.cfg.WorkspaceDir, "notes", "result.txt")); err != nil || string(data) != "from subagent\n" {
		t.Fatalf("expected merged main workspace file, data=%q err=%v", string(data), err)
	}
}

func TestReadOnlyDurableSubagentUsesSharedWorkspaceWithoutWorktree(t *testing.T) {
	a := newTestAgent(t, 4096)
	if err := os.WriteFile(filepath.Join(a.cfg.WorkspaceDir, "README.md"), []byte("hello"), 0644); err != nil {
		t.Fatalf("write readme: %v", err)
	}
	a.RegisterTools()
	a.client = &sequenceCaller{responses: []protocol.Response{
		{Content: []protocol.Block{
			protocol.ToolUseBlock("tool-read", "read_file", map[string]interface{}{"path": "README.md"}),
		}},
		{Content: []protocol.Block{protocol.TextBlock("read-only handoff")}},
	}}

	job, err := a.StartDurableSubagent("inspect only", "Explore", nil)
	if err != nil {
		t.Fatalf("start durable subagent: %v", err)
	}
	completed := waitForSubagentStatus(t, a.subagentJobs, job.ID, subagentStatusCompleted)
	if completed.Isolation != subagentIsolationSharedReadOnly {
		t.Fatalf("expected shared readonly isolation, got %+v", completed)
	}
	if completed.WorktreeDir != a.cfg.WorkspaceDir {
		t.Fatalf("expected main workspace path for read-only execution, got %+v", completed)
	}
	if _, err := os.Stat(filepath.Join(a.subagentJobs.dir, "worktrees", job.ID)); !os.IsNotExist(err) {
		t.Fatalf("expected no subagent worktree directory, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(a.subagentJobs.dir, "baselines", job.ID)); !os.IsNotExist(err) {
		t.Fatalf("expected no subagent baseline directory, err=%v", err)
	}
}

func TestDirtyGitDurableSubagentUsesWorktreeDirtyOverlay(t *testing.T) {
	a := newTestAgent(t, 4096)
	initGitRepo(t, a.cfg.WorkspaceDir)
	a.cfg.Tools.Subagent.GitDirtyIsolation = "dirty_overlay"
	if err := os.WriteFile(filepath.Join(a.cfg.WorkspaceDir, "README.md"), []byte("# dirty\n"), 0644); err != nil {
		t.Fatalf("write dirty README: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(a.cfg.WorkspaceDir, "notes"), 0755); err != nil {
		t.Fatalf("mkdir notes: %v", err)
	}
	if err := os.WriteFile(filepath.Join(a.cfg.WorkspaceDir, "notes", "untracked.txt"), []byte("visible untracked\n"), 0644); err != nil {
		t.Fatalf("write untracked: %v", err)
	}
	if err := os.WriteFile(filepath.Join(a.cfg.WorkspaceDir, ".env"), []byte("SECRET=skip\n"), 0600); err != nil {
		t.Fatalf("write env: %v", err)
	}
	a.RegisterTools()
	a.client = &sequenceCaller{responses: []protocol.Response{
		{Content: []protocol.Block{
			protocol.ToolUseBlock("tool-read", "read_file", map[string]interface{}{"path": "README.md"}),
		}},
		{Content: []protocol.Block{protocol.TextBlock("dirty handoff")}},
	}}

	job, err := a.StartDurableSubagent("inspect dirty workspace", "general-purpose", []string{"notes"})
	if err != nil {
		t.Fatalf("start durable subagent: %v", err)
	}
	completed := waitForSubagentStatus(t, a.subagentJobs, job.ID, subagentStatusCompleted)
	if completed.Isolation != subagentIsolationGitWorktree || completed.WorkspaceOrigin != "git_dirty_overlay" {
		t.Fatalf("expected git dirty overlay worktree, got %+v", completed)
	}
	if data, err := os.ReadFile(filepath.Join(completed.WorktreeDir, "README.md")); err != nil || string(data) != "# dirty\n" {
		t.Fatalf("expected dirty tracked file overlay, data=%q err=%v", string(data), err)
	}
	if data, err := os.ReadFile(filepath.Join(completed.WorktreeDir, "notes", "untracked.txt")); err != nil || string(data) != "visible untracked\n" {
		t.Fatalf("expected safe untracked overlay, data=%q err=%v", string(data), err)
	}
	if _, err := os.Stat(filepath.Join(completed.WorktreeDir, ".env")); !os.IsNotExist(err) {
		t.Fatalf("expected .env to be skipped from dirty overlay, err=%v", err)
	}
}

func TestNonGitWriteSubagentCanBeDeniedByPolicy(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.cfg.Tools.Subagent.NonGitWriteIsolation = "deny"
	a.RegisterTools()
	a.client = &sequenceCaller{responses: []protocol.Response{
		{Content: []protocol.Block{protocol.TextBlock("should not run")}},
	}}

	if _, err := a.StartDurableSubagent("write outside git", "general-purpose", []string{"notes"}); err == nil || !strings.Contains(err.Error(), "non-git write isolation policy denies") {
		t.Fatalf("expected non-git denial, got %v", err)
	}
}

func TestCleanupMergedSubagentWorkspaceRemovesWorktreeAndBaseline(t *testing.T) {
	a := newTestAgent(t, 4096)
	job, err := a.subagentJobs.Start("general-purpose", "done", []string{"read_file", "write_file"}, []string{"notes"}, 3)
	if err != nil {
		t.Fatalf("start job: %v", err)
	}
	worktreeDir := filepath.Join(a.subagentJobs.dir, "worktrees", job.ID)
	baselineDir := filepath.Join(a.subagentJobs.dir, "baselines", job.ID)
	if err := os.MkdirAll(filepath.Join(worktreeDir, "notes"), 0755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(baselineDir, "notes"), 0755); err != nil {
		t.Fatalf("mkdir baseline: %v", err)
	}
	if _, err := a.subagentJobs.SetWorkspace(job.ID, worktreeDir, baselineDir, subagentIsolationSnapshot, "", "snapshot", "sandbox:local:test"); err != nil {
		t.Fatalf("set workspace: %v", err)
	}
	if _, err := a.subagentJobs.Finish(job.ID, subagentStatusCompleted, "done", ""); err != nil {
		t.Fatalf("finish job: %v", err)
	}
	if _, err := a.subagentJobs.SetMergeStatus(job.ID, subagentMergeNoChanges, subagentProgressEvent{Phase: "merge_reviewed"}); err != nil {
		t.Fatalf("set merge status: %v", err)
	}

	result, err := a.CleanupDurableSubagentWorkspace(job.ID)
	if err != nil {
		t.Fatalf("cleanup workspace: %v", err)
	}
	if !result.Cleaned {
		t.Fatalf("expected cleanup to run, got %+v", result)
	}
	if _, err := os.Stat(worktreeDir); !os.IsNotExist(err) {
		t.Fatalf("expected worktree removed, err=%v", err)
	}
	if _, err := os.Stat(baselineDir); !os.IsNotExist(err) {
		t.Fatalf("expected baseline removed, err=%v", err)
	}
	cleaned, err := a.subagentJobs.Get(job.ID)
	if err != nil {
		t.Fatalf("get cleaned job: %v", err)
	}
	if cleaned.CleanupState != subagentCleanupCleaned {
		t.Fatalf("expected cleanup state cleaned, got %+v", cleaned)
	}
}

func TestDurableSubagentMergeDetectsMainWorkspaceConflict(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	if err := os.MkdirAll(filepath.Join(a.cfg.WorkspaceDir, "notes"), 0755); err != nil {
		t.Fatalf("mkdir notes: %v", err)
	}
	if err := os.WriteFile(filepath.Join(a.cfg.WorkspaceDir, "notes", "result.txt"), []byte("base\n"), 0644); err != nil {
		t.Fatalf("write base file: %v", err)
	}
	a.client = &sequenceCaller{responses: []protocol.Response{
		{Content: []protocol.Block{
			protocol.ToolUseBlock("tool-edit", "edit_file", map[string]interface{}{
				"path":     "notes/result.txt",
				"old_text": "base\n",
				"new_text": "worker\n",
			}),
		}},
		{Content: []protocol.Block{protocol.TextBlock("handoff")}},
	}}

	job, err := a.StartDurableSubagent("edit a note", "general-purpose", []string{"notes"})
	if err != nil {
		t.Fatalf("start durable subagent: %v", err)
	}
	waitForSubagentStatus(t, a.subagentJobs, job.ID, subagentStatusCompleted)
	if err := os.WriteFile(filepath.Join(a.cfg.WorkspaceDir, "notes", "result.txt"), []byte("main\n"), 0644); err != nil {
		t.Fatalf("write conflicting main file: %v", err)
	}

	merged, err := a.MergeDurableSubagent(job.ID)
	if err != nil {
		t.Fatalf("merge durable subagent: %v", err)
	}
	if merged.Status != subagentMergeConflict || len(merged.Conflicts) != 1 || merged.Conflicts[0] != "notes/result.txt" {
		t.Fatalf("expected merge conflict, got %+v", merged)
	}
	if data, err := os.ReadFile(filepath.Join(a.cfg.WorkspaceDir, "notes", "result.txt")); err != nil || string(data) != "main\n" {
		t.Fatalf("expected conflicting main file to stay unchanged, data=%q err=%v", string(data), err)
	}
}

func TestSubagentToolStartStatusAndListDurableJobs(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	a.client = &sequenceCaller{responses: []protocol.Response{
		{Content: []protocol.Block{protocol.TextBlock("done async")}},
	}}

	result, err := a.handleTool(context.Background(), "task", map[string]interface{}{
		"action": "start",
		"prompt": "run async",
	})
	if err != nil {
		t.Fatalf("start subagent through tool: %v", err)
	}
	if result == "" {
		t.Fatal("expected structured tool result")
	}
	jobs := a.subagentJobs.List()
	if len(jobs) != 1 {
		t.Fatalf("expected one durable job, got %+v", jobs)
	}
	completed := waitForSubagentStatus(t, a.subagentJobs, jobs[0].ID, subagentStatusCompleted)
	if completed.Result != "done async" {
		t.Fatalf("expected async result, got %+v", completed)
	}

	if _, err := a.handleTool(context.Background(), "task", map[string]interface{}{
		"action": "status",
		"job_id": jobs[0].ID,
	}); err != nil {
		t.Fatalf("status subagent through tool: %v", err)
	}
	if _, err := a.handleTool(context.Background(), "task", map[string]interface{}{
		"action": "list",
	}); err != nil {
		t.Fatalf("list subagents through tool: %v", err)
	}
}

func TestSubagentToolRunCreatesVisibleDurableJob(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	a.client = &sequenceCaller{responses: []protocol.Response{
		{Content: []protocol.Block{protocol.TextBlock("done sync durable")}},
	}}
	eventsCh := make(chan events.Event, 32)
	ctx := withSubagentEventTarget(context.Background(), subagentEventTarget{
		sessionID: "session-sync-run",
		turnID:    "turn-sync-run",
		sink: events.SinkFunc(func(event events.Event) {
			eventsCh <- event
		}),
	})

	result, err := a.handleTool(ctx, "task", map[string]interface{}{
		"action":     "run",
		"agent_type": "Explore",
		"prompt":     "run sync as visible durable job",
	})
	if err != nil {
		t.Fatalf("run subagent through tool: %v", err)
	}
	var payload subagentRunView
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("parse run payload: %v\n%s", err, result)
	}
	if payload.Status != "completed" || payload.Result != "done sync durable" || payload.JobID == "" || payload.Wait.TimeoutMS != 0 || payload.Timeout {
		t.Fatalf("unexpected run payload: %+v", payload)
	}
	jobs := a.subagentJobs.List()
	if len(jobs) != 1 || jobs[0].ID != payload.JobID {
		t.Fatalf("expected one visible durable job, got payload=%+v jobs=%+v", payload, jobs)
	}

	sawSubagentUpdate := false
	sawIdentity := false
	for len(eventsCh) > 0 {
		event := <-eventsCh
		switch event.Type {
		case events.EventSubagentJobUpdated:
			sawSubagentUpdate = true
		case events.EventAgentIdentityUpdated:
			sawIdentity = true
		}
	}
	if !sawSubagentUpdate || !sawIdentity {
		t.Fatalf("expected visible subagent and identity events, subagent=%t identity=%t", sawSubagentUpdate, sawIdentity)
	}
}

func TestSubagentToolStatusDoesNotExposeMessages(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	a.client = repeatedTextCaller("compact handoff")

	if _, err := a.handleTool(context.Background(), "task", map[string]interface{}{
		"action": "start",
		"prompt": "run compact",
	}); err != nil {
		t.Fatalf("start subagent through tool: %v", err)
	}
	jobs := a.subagentJobs.List()
	if len(jobs) != 1 {
		t.Fatalf("expected one durable job, got %+v", jobs)
	}
	waitForSubagentStatus(t, a.subagentJobs, jobs[0].ID, subagentStatusCompleted)

	status, err := a.handleTool(context.Background(), "task", map[string]interface{}{
		"action": "status",
		"job_id": jobs[0].ID,
	})
	if err != nil {
		t.Fatalf("status subagent through tool: %v", err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(status), &payload); err != nil {
		t.Fatalf("parse status payload: %v\n%s", err, status)
	}
	if _, ok := payload["messages"]; ok {
		t.Fatalf("task status should not expose raw messages, got %s", status)
	}
	for _, forbidden := range []string{"prompt", "result", "progress"} {
		if _, ok := payload[forbidden]; ok {
			t.Fatalf("task status should not expose %q, got %s", forbidden, status)
		}
	}
	if payload["job_id"] != jobs[0].ID {
		t.Fatalf("expected compact job id %q, got %#v", jobs[0].ID, payload["job_id"])
	}
	if payload["result_preview"] != "compact handoff" {
		t.Fatalf("expected bounded result preview, got %#v in %s", payload["result_preview"], status)
	}
	if payload["identity_id"] == "" {
		t.Fatalf("expected identity_id in compact status, got %s", status)
	}
	if payload["result_digest"] == "" || payload["result_bytes"] == nil {
		t.Fatalf("expected result digest metadata in compact status, got %s", status)
	}
	if _, ok := payload["progress_count"].(float64); !ok {
		t.Fatalf("expected progress_count in compact status, got %s", status)
	}
}

func TestSubagentToolLogsReturnsBoundedProgress(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	job, err := a.subagentJobs.Start("Explore", "inspect logs", []string{"read_file"}, nil, 3)
	if err != nil {
		t.Fatalf("seed subagent: %v", err)
	}
	for i := 0; i < 30; i++ {
		if _, err := a.subagentJobs.AppendProgress(job.ID, subagentProgressEvent{
			Time:    time.Now(),
			Phase:   "step",
			Message: "progress item",
		}); err != nil {
			t.Fatalf("append progress: %v", err)
		}
	}

	logs, err := a.handleTool(context.Background(), "task", map[string]interface{}{
		"action": "logs",
		"job_id": job.ID,
		"limit":  5,
	})
	if err != nil {
		t.Fatalf("logs subagent through tool: %v", err)
	}
	var payload struct {
		Count    int                           `json:"count"`
		Total    int                           `json:"total"`
		Progress []DurableSubagentProgressView `json:"progress"`
	}
	if err := json.Unmarshal([]byte(logs), &payload); err != nil {
		t.Fatalf("parse logs payload: %v\n%s", err, logs)
	}
	if payload.Count != 5 || len(payload.Progress) != 5 {
		t.Fatalf("expected 5 bounded progress events, got %+v", payload)
	}
	if payload.Total < 30 {
		t.Fatalf("expected total progress count, got %+v", payload)
	}
}

func TestSubagentToolBatchStartsCompactJobs(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	a.client = repeatedTextCaller("batch handoff")

	out, err := a.handleTool(context.Background(), "task", map[string]interface{}{
		"action": "batch",
		"tasks": []interface{}{
			map[string]interface{}{"prompt": "inspect package"},
			map[string]interface{}{"prompt": "review docs", "agent_type": "Explore"},
		},
	})
	if err != nil {
		t.Fatalf("batch subagents through tool: %v", err)
	}
	var payload subagentBatchView
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("parse batch payload: %v\n%s", err, out)
	}
	if payload.Started != 2 || payload.Failed != 0 || len(payload.Jobs) != 2 {
		t.Fatalf("expected two started compact jobs, got %+v", payload)
	}
	if strings.Contains(out, `"messages"`) {
		t.Fatalf("batch output should not expose raw messages, got %s", out)
	}
	for _, job := range payload.Jobs {
		waitForSubagentStatus(t, a.subagentJobs, job.JobID, subagentStatusCompleted)
	}
}

func TestSubagentToolBatchUsesConfiguredLimit(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.cfg.Tools.Subagent.MaxBatchSize = 2
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	a.client = repeatedTextCaller("batch handoff")

	out, err := a.handleTool(context.Background(), "task", map[string]interface{}{
		"action": "batch",
		"tasks": []interface{}{
			map[string]interface{}{"prompt": "one"},
			map[string]interface{}{"prompt": "two"},
			map[string]interface{}{"prompt": "three"},
		},
	})
	if err != nil {
		t.Fatalf("batch subagents through tool: %v", err)
	}
	var payload subagentBatchView
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("parse batch payload: %v\n%s", err, out)
	}
	if payload.Started != 2 || payload.Failed != 1 || payload.Errors[0].Index != 2 {
		t.Fatalf("expected configured batch cap to start two and reject one, got %+v", payload)
	}
	if !strings.Contains(payload.Errors[0].Error, "2 tasks") {
		t.Fatalf("expected configured limit in error, got %+v", payload.Errors)
	}
	for _, job := range payload.Jobs {
		waitForSubagentStatus(t, a.subagentJobs, job.JobID, subagentStatusCompleted)
	}
}

func TestSubagentToolBatchQueuesAboveConfiguredConcurrency(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.cfg.Tools.Subagent.MaxBatchSize = 3
	a.cfg.Tools.Subagent.MaxConcurrentJobs = 1
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	release := make(chan struct{})
	a.client = blockingSubagentCaller{release: release}

	out, err := a.handleTool(context.Background(), "task", map[string]interface{}{
		"action": "batch",
		"tasks": []interface{}{
			map[string]interface{}{"prompt": "one"},
			map[string]interface{}{"prompt": "two"},
			map[string]interface{}{"prompt": "three"},
		},
	})
	if err != nil {
		t.Fatalf("batch subagents through tool: %v", err)
	}
	var payload subagentBatchView
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("parse batch payload: %v\n%s", err, out)
	}
	if payload.Started != 3 || payload.Failed != 0 {
		t.Fatalf("expected three accepted jobs, got %+v", payload)
	}
	if payload.Jobs[0].Status != string(subagentStatusRunning) || payload.Jobs[1].Status != string(subagentStatusPending) || payload.Jobs[2].Status != string(subagentStatusPending) {
		t.Fatalf("expected one running and two pending jobs, got %+v", payload.Jobs)
	}

	if _, err := a.subagentJobs.Cancel(payload.Jobs[0].JobID); err != nil {
		t.Fatalf("cancel first job: %v", err)
	}
	waitForSubagentStatus(t, a.subagentJobs, payload.Jobs[1].JobID, subagentStatusRunning)
	close(release)
	waitForSubagentStatus(t, a.subagentJobs, payload.Jobs[1].JobID, subagentStatusCompleted)
	waitForSubagentStatus(t, a.subagentJobs, payload.Jobs[2].JobID, subagentStatusCompleted)
}

func TestSubagentToolWaitReturnsStructuredCompletionAndErrors(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	completed, err := a.subagentJobs.Start("Explore", "done prompt", []string{"read_file"}, nil, 3)
	if err != nil {
		t.Fatalf("seed completed subagent: %v", err)
	}
	if _, err := a.subagentJobs.Finish(completed.ID, subagentStatusCompleted, strings.Repeat("handoff ", 400), ""); err != nil {
		t.Fatalf("finish completed subagent: %v", err)
	}
	failed, err := a.subagentJobs.Start("Explore", "failed prompt", []string{"read_file"}, nil, 3)
	if err != nil {
		t.Fatalf("seed failed subagent: %v", err)
	}
	if _, err := a.subagentJobs.Finish(failed.ID, subagentStatusError, "", "boom"); err != nil {
		t.Fatalf("finish failed subagent: %v", err)
	}

	out, err := a.handleTool(context.Background(), "task", map[string]interface{}{
		"action":  "wait",
		"job_ids": []interface{}{completed.ID, failed.ID},
		"mode":    "all",
	})
	if err != nil {
		t.Fatalf("wait subagents through tool: %v", err)
	}
	var payload subagentWaitView
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("parse wait payload: %v\n%s", err, out)
	}
	if payload.Status != "completed" || payload.Completed != 2 || payload.Failed != 1 || payload.Running != 0 {
		t.Fatalf("unexpected wait summary: %+v", payload)
	}
	if len(payload.Jobs) != 2 {
		t.Fatalf("expected two compact jobs, got %+v", payload)
	}
	if strings.Contains(out, `"prompt"`) || strings.Contains(out, `"progress"`) || strings.Contains(out, `"messages"`) {
		t.Fatalf("wait output should be compact, got %s", out)
	}
	if len([]rune(payload.Jobs[0].ResultPreview)) > subagentResultPreviewLimit+3 {
		t.Fatalf("expected bounded result preview, got %d runes", len([]rune(payload.Jobs[0].ResultPreview)))
	}
}

func TestSubagentToolWaitAnyAndTimeout(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	done, err := a.subagentJobs.Start("Explore", "done prompt", []string{"read_file"}, nil, 3)
	if err != nil {
		t.Fatalf("seed done subagent: %v", err)
	}
	if _, err := a.subagentJobs.Finish(done.ID, subagentStatusCompleted, "done", ""); err != nil {
		t.Fatalf("finish done subagent: %v", err)
	}
	running, err := a.subagentJobs.Start("Explore", "running prompt", []string{"read_file"}, nil, 3)
	if err != nil {
		t.Fatalf("seed running subagent: %v", err)
	}

	out, err := a.handleTool(context.Background(), "task", map[string]interface{}{
		"action":  "wait",
		"job_ids": []interface{}{done.ID, running.ID},
		"mode":    "any",
	})
	if err != nil {
		t.Fatalf("wait any through tool: %v", err)
	}
	var anyPayload subagentWaitView
	if err := json.Unmarshal([]byte(out), &anyPayload); err != nil {
		t.Fatalf("parse wait any payload: %v\n%s", err, out)
	}
	if anyPayload.Status != "completed" || anyPayload.Completed != 1 || anyPayload.Running != 1 {
		t.Fatalf("unexpected wait any summary: %+v", anyPayload)
	}

	out, err = a.handleTool(context.Background(), "task", map[string]interface{}{
		"action":     "wait",
		"job_ids":    []interface{}{running.ID},
		"timeout_ms": 1,
	})
	if err != nil {
		t.Fatalf("wait timeout through tool: %v", err)
	}
	var timeoutPayload subagentWaitView
	if err := json.Unmarshal([]byte(out), &timeoutPayload); err != nil {
		t.Fatalf("parse wait timeout payload: %v\n%s", err, out)
	}
	if timeoutPayload.Status != "timeout" || timeoutPayload.Running != 1 {
		t.Fatalf("unexpected wait timeout summary: %+v", timeoutPayload)
	}
}

func TestSubagentToolWaitWakesOnJobUpdate(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	job, err := a.subagentJobs.Start("Explore", "wait wake", []string{"read_file"}, nil, 3)
	if err != nil {
		t.Fatalf("seed running subagent: %v", err)
	}
	go func() {
		time.Sleep(50 * time.Millisecond)
		_, _ = a.subagentJobs.Finish(job.ID, subagentStatusCompleted, "done", "")
	}()

	start := time.Now()
	out, err := a.handleTool(context.Background(), "task", map[string]interface{}{
		"action":     "wait",
		"job_ids":    []interface{}{job.ID},
		"timeout_ms": 5000,
	})
	if err != nil {
		t.Fatalf("wait through tool: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("expected event-driven wait to wake quickly, took %s with output %s", elapsed, out)
	}
	var payload subagentWaitView
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("parse wait payload: %v\n%s", err, out)
	}
	if payload.Status != "completed" || payload.Completed != 1 {
		t.Fatalf("unexpected wait payload: %+v", payload)
	}
}

func TestSubagentToolBatchWaitsForStartedJobs(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	a.client = repeatedTextCaller("batch waited handoff")

	out, err := a.handleTool(context.Background(), "task", map[string]interface{}{
		"action":     "batch",
		"wait":       true,
		"timeout_ms": 5000,
		"tasks": []interface{}{
			map[string]interface{}{"prompt": "inspect package"},
			map[string]interface{}{"prompt": "review docs", "agent_type": "Explore"},
		},
	})
	if err != nil {
		t.Fatalf("batch wait through tool: %v", err)
	}
	var payload subagentBatchView
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("parse batch wait payload: %v\n%s", err, out)
	}
	if payload.Wait == nil || payload.Wait.Status != "completed" || payload.Wait.Completed != 2 {
		t.Fatalf("expected completed wait summary, got %+v", payload)
	}
	for _, job := range payload.Jobs {
		if job.Status != string(subagentStatusCompleted) || job.ResultPreview != "batch waited handoff" {
			t.Fatalf("expected compact completed job with handoff preview, got %+v", job)
		}
	}
}

func TestSubagentToolAppliesPerJobTimeoutAndMaxTurns(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.cfg.Tools.Subagent.MaxJobTimeoutMs = 100
	a.cfg.Tools.Subagent.DefaultMaxTurns = 45
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	a.client = repeatedTextCaller("configured handoff")

	if _, err := a.handleTool(context.Background(), "task", map[string]interface{}{
		"action":         "start",
		"prompt":         "run configured",
		"max_turns":      7,
		"job_timeout_ms": 1000,
	}); err != nil {
		t.Fatalf("start configured subagent through tool: %v", err)
	}
	jobs := a.subagentJobs.List()
	if len(jobs) != 1 {
		t.Fatalf("expected one job, got %+v", jobs)
	}
	if jobs[0].MaxTurns != 45 || jobs[0].JobTimeoutMS != 100 {
		t.Fatalf("expected max turns and clamped timeout, got %+v", jobs[0])
	}
	waitForSubagentStatus(t, a.subagentJobs, jobs[0].ID, subagentStatusCompleted)

	out, err := a.handleTool(context.Background(), "task", map[string]interface{}{
		"action": "batch",
		"tasks": []interface{}{
			map[string]interface{}{
				"prompt":         "batch configured",
				"max_turns":      90,
				"job_timeout_ms": 75,
			},
		},
	})
	if err != nil {
		t.Fatalf("batch configured subagent through tool: %v", err)
	}
	var payload subagentBatchView
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("parse batch payload: %v\n%s", err, out)
	}
	if len(payload.Jobs) != 1 {
		t.Fatalf("expected one batch job, got %+v", payload)
	}
	batchJob, err := a.subagentJobs.Get(payload.Jobs[0].JobID)
	if err != nil {
		t.Fatalf("get batch job: %v", err)
	}
	if batchJob.MaxTurns != 90 || batchJob.JobTimeoutMS != 75 {
		t.Fatalf("expected batch item settings, got %+v", batchJob)
	}
	waitForSubagentStatus(t, a.subagentJobs, batchJob.ID, subagentStatusCompleted)
}

func TestDurableSubagentMaxTurnsDiagnostics(t *testing.T) {
	a := newTestAgent(t, 4096)
	repeated := protocol.Response{Content: []protocol.Block{
		protocol.ToolUseBlock("tool-read", "read_file", map[string]interface{}{"path": "missing.txt"}),
	}}
	a.client = &repeatingCaller{response: repeated}
	got := make(chan events.Event, 32)
	target := subagentEventTarget{
		sessionID: "session-sub-max",
		turnID:    "turn-sub-max",
		sink: events.SinkFunc(func(event events.Event) {
			select {
			case got <- event:
			default:
			}
		}),
	}
	job, err := a.subagentJobs.Start("Explore", "inspect missing file forever", []string{"read_file"}, nil, 2)
	if err != nil {
		t.Fatalf("start subagent: %v", err)
	}

	a.runSubagentJob(context.Background(), job.ID, target)
	completed, err := a.subagentJobs.Get(job.ID)
	if err != nil {
		t.Fatalf("get completed subagent: %v", err)
	}
	if completed.Status != subagentStatusError || !strings.Contains(completed.Error, "reached max turns after 2 turns") || !strings.Contains(completed.Error, job.ID) || !strings.Contains(completed.Error, "last_tool=read_file") {
		t.Fatalf("expected max-turn subagent diagnostic error, got %+v", completed)
	}
	view := durableSubagentJobView(completed)
	if view.MaxTurns != 2 {
		t.Fatalf("expected max turns in job view, got %+v", view)
	}
	foundPhaseMaxTurns := false
	foundJobMaxTurns := false
	deadline := time.After(2 * time.Second)
	for !(foundPhaseMaxTurns && foundJobMaxTurns) {
		select {
		case event := <-got:
			switch payload := event.Payload.(type) {
			case events.RunnerPhasePayload:
				if event.Type == events.EventRunnerPhaseChanged && payload.MaxTurns == 2 && payload.ActorKind == "subagent" {
					foundPhaseMaxTurns = true
				}
			case events.SubagentJobPayload:
				if event.Type == events.EventSubagentJobUpdated && payload.MaxTurns == 2 && strings.Contains(payload.Error, "reached max turns") {
					foundJobMaxTurns = true
				}
			}
		case <-deadline:
			t.Fatalf("expected subagent max-turn events phase=%v job=%v", foundPhaseMaxTurns, foundJobMaxTurns)
		}
	}
}

func TestDurableSubagentTimesOutJob(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	a.client = blockingSubagentCaller{release: make(chan struct{})}

	if _, err := a.handleTool(context.Background(), "task", map[string]interface{}{
		"action":         "start",
		"prompt":         "block until timeout",
		"job_timeout_ms": 50,
	}); err != nil {
		t.Fatalf("start timeout subagent through tool: %v", err)
	}
	jobs := a.subagentJobs.List()
	if len(jobs) != 1 {
		t.Fatalf("expected one job, got %+v", jobs)
	}
	timedOut := waitForSubagentStatus(t, a.subagentJobs, jobs[0].ID, subagentStatusTimeout)
	if !strings.Contains(timedOut.Error, "timed out after 50ms") {
		t.Fatalf("expected timeout diagnostic, got %+v", timedOut)
	}
	if timedOut.FinishedAt.IsZero() {
		t.Fatalf("expected timeout to finish job, got %+v", timedOut)
	}
}

func TestDurableSubagentDefaultTimeoutDisabled(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	a.client = blockingSubagentCaller{release: make(chan struct{})}

	if _, err := a.handleTool(context.Background(), "task", map[string]interface{}{
		"action": "start",
		"prompt": "keep running without timeout",
	}); err != nil {
		t.Fatalf("start no-timeout subagent through tool: %v", err)
	}
	jobs := a.subagentJobs.List()
	if len(jobs) != 1 {
		t.Fatalf("expected one job, got %+v", jobs)
	}
	time.Sleep(100 * time.Millisecond)
	running, err := a.subagentJobs.Get(jobs[0].ID)
	if err != nil {
		t.Fatalf("get no-timeout job: %v", err)
	}
	if running.Status != subagentStatusRunning || running.JobTimeoutMS != 0 {
		t.Fatalf("expected default timeout to be disabled, got %+v", running)
	}
	if _, err := a.subagentJobs.Cancel(running.ID); err != nil {
		t.Fatalf("cancel no-timeout job: %v", err)
	}
	waitForSubagentStatus(t, a.subagentJobs, running.ID, subagentStatusCanceled)
}

func TestSubagentPromptRewritesWorkspaceAbsolutePaths(t *testing.T) {
	a := newTestAgent(t, 4096)
	input := "Review " + filepath.Join(a.cfg.WorkspaceDir, "temp/pi-go/internal/subagent") + " now"
	got := a.rewriteSubagentPromptWorkspacePaths(input)
	if strings.Contains(got, a.cfg.WorkspaceDir) {
		t.Fatalf("expected workspace absolute path to be removed, got %q", got)
	}
	if !strings.Contains(filepath.ToSlash(got), "temp/pi-go/internal/subagent") {
		t.Fatalf("expected relative path to remain, got %q", got)
	}
}

func TestSubagentReadFilePassesRangeArguments(t *testing.T) {
	var gotPath string
	var gotLimit, gotOffset, gotStartLine int
	_, err := executeSubagentToolWithHandlers(context.Background(), "read_file", map[string]interface{}{
		"path":       "notes.txt",
		"limit":      120,
		"offset":     7,
		"start_line": 3,
	}, subagentToolHandlers{
		readFile: func(ctx context.Context, path string, limit, offset, startLine, maxLines int) (conversation.ToolExecutionResult, error) {
			_ = ctx
			gotPath = path
			gotLimit = limit
			gotOffset = offset
			gotStartLine = startLine
			return conversation.ToolExecutionResult{Output: "ok"}, nil
		},
	})
	if err != nil {
		t.Fatalf("execute read_file: %v", err)
	}
	if gotPath != "notes.txt" || gotLimit != 120 || gotOffset != 7 || gotStartLine != 3 {
		t.Fatalf("unexpected read_file args: path=%q limit=%d offset=%d start_line=%d", gotPath, gotLimit, gotOffset, gotStartLine)
	}
}

func TestSubagentToolExecutionDeniesToolOutsideJobAllowList(t *testing.T) {
	a := newTestAgent(t, 4096)
	job := &subagentJob{
		ID:         "subagent_test",
		ToolNames:  []string{"read_file"},
		WriteScope: []string{"notes"},
	}
	_, err := a.executeSubagentToolForJob(context.Background(), "write_file", map[string]interface{}{
		"path":    "notes/result.txt",
		"content": "denied",
	}, job)
	if err == nil || !strings.Contains(err.Error(), "capability denied") {
		t.Fatalf("expected capability denied error, got %v", err)
	}
}

func TestSubagentRoleToolPolicyRestrictsShell(t *testing.T) {
	a := newTestAgent(t, 4096)
	job := &subagentJob{
		ID:          "subagent_shell_policy",
		ToolNames:   []string{"bash"},
		WorktreeDir: t.TempDir(),
		ToolPolicy:  []string{"shell:allow:go test*", "shell:deny:go test ./danger*"},
	}

	if _, err := a.executeSubagentToolForJob(context.Background(), "bash", map[string]interface{}{
		"command": "echo ok",
	}, job); err == nil || !strings.Contains(err.Error(), "does not match any allowed") {
		t.Fatalf("expected shell allow policy denial, got %v", err)
	}
	if _, err := a.executeSubagentToolForJob(context.Background(), "bash", map[string]interface{}{
		"command": "go test ./danger/...",
	}, job); err == nil || !strings.Contains(err.Error(), "denied by policy pattern") {
		t.Fatalf("expected shell deny policy denial, got %v", err)
	}
}

func TestDurableSubagentWithoutWriteScopeIsReadOnly(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.client = &sequenceCaller{responses: []protocol.Response{
		{Content: []protocol.Block{protocol.TextBlock("read-only handoff")}},
	}}

	job, err := a.StartDurableSubagent("inspect only", "general-purpose", nil)
	if err != nil {
		t.Fatalf("start durable subagent: %v", err)
	}
	if containsString(job.ToolNames, "write_file") || containsString(job.ToolNames, "edit_file") {
		t.Fatalf("expected no write tools without write scope, got %+v", job.ToolNames)
	}
	if containsString(job.ToolNames, "bash") {
		t.Fatalf("expected read-only subagent to avoid shell tools, got %+v", job.ToolNames)
	}
	completed := waitForSubagentStatus(t, a.subagentJobs, job.ID, subagentStatusCompleted)
	for _, capability := range job.Identity.CapabilitySummary {
		if strings.HasPrefix(capability, "file:write:") {
			t.Fatalf("expected no write capability without write scope, got %+v", job.Identity.CapabilitySummary)
		}
	}
	if completed.Result != "read-only handoff" {
		t.Fatalf("expected completed read-only handoff, got %+v", completed)
	}
}

func TestDurableSubagentInheritsActiveWebToolsForResearchPrompt(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleWeb)
	a.client = &sequenceCaller{responses: []protocol.Response{
		{Content: []protocol.Block{protocol.TextBlock("research handoff")}},
	}}

	job, err := a.StartDurableSubagent("请进行网络检索调研，并返回带源头链接的报告", "general-purpose", nil)
	if err != nil {
		t.Fatalf("start durable web research subagent: %v", err)
	}
	if !containsString(job.ToolNames, "web_search") || !containsString(job.ToolNames, "web_fetch") {
		t.Fatalf("expected web tools to be granted to research subagent, got %+v", job.ToolNames)
	}
	if containsString(job.ToolNames, "bash") || containsString(job.ToolNames, "write_file") || containsString(job.ToolNames, "edit_file") {
		t.Fatalf("expected web research subagent without write scope to stay read-only except web tools, got %+v", job.ToolNames)
	}
	waitForSubagentStatus(t, a.subagentJobs, job.ID, subagentStatusCompleted)
}

func TestDurableSubagentRequiresWebBundleBeforeResearchPrompt(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()

	_, err := a.StartDurableSubagent("web research with source links and official pages", "general-purpose", nil)
	if err == nil {
		t.Fatal("expected missing web capability error")
	}
	for _, want := range []string{"subagent_capability_required", "web", "tool_exchange"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("expected error to mention %q, got %v", want, err)
		}
	}
}

func TestDurableSubagentResolvesPackageRole(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.client = &sequenceCaller{responses: []protocol.Response{
		{Content: []protocol.Block{protocol.TextBlock("role handoff")}},
	}}
	source := t.TempDir()
	if err := os.MkdirAll(filepath.Join(source, "roles"), 0755); err != nil {
		t.Fatalf("mkdir roles: %v", err)
	}
	manifest := `name: agent-kit
version: 0.1.0
resources:
  roles:
    - roles/reviewer.yaml
`
	if err := os.WriteFile(filepath.Join(source, "godex.package.yaml"), []byte(manifest), 0644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	role := `id: agent-kit:reviewer
name: Reviewer
description: Reviews code without editing
base_prompt: Keep the review tight.
tools:
  - read_file
  - write_file
write_enabled: false
default_bundles:
  - core_code
`
	if err := os.WriteFile(filepath.Join(source, "roles", "reviewer.yaml"), []byte(role), 0644); err != nil {
		t.Fatalf("write role: %v", err)
	}
	if _, err := a.InstallPackage(source); err != nil {
		t.Fatalf("install role package: %v", err)
	}

	job, err := a.StartDurableSubagent("review this", "agent-kit:reviewer", nil)
	if err != nil {
		t.Fatalf("start durable role subagent: %v", err)
	}
	if job.RoleID != "agent-kit:reviewer" || job.RoleName != "Reviewer" || job.PackageName != "agent-kit" {
		t.Fatalf("expected role metadata, got %+v", job)
	}
	if containsString(job.ToolNames, "write_file") {
		t.Fatalf("did not expect write tool for read-only role, got %+v", job.ToolNames)
	}
	if !strings.Contains(job.BasePrompt, "Keep the review tight.") {
		t.Fatalf("expected role base prompt, got %q", job.BasePrompt)
	}
	completed := waitForSubagentStatus(t, a.subagentJobs, job.ID, subagentStatusCompleted)
	if completed.Result != "role handoff" {
		t.Fatalf("expected role subagent result, got %+v", completed)
	}
}

func waitForSubagentStatus(t *testing.T, store *subagentJobStore, id string, status subagentJobStatus) *subagentJob {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		job, err := store.Get(id)
		if err == nil && job.Status == status {
			return job
		}
		time.Sleep(10 * time.Millisecond)
	}
	job, _ := store.Get(id)
	t.Fatalf("timed out waiting for subagent %s status %s, got %+v", id, status, job)
	return nil
}
