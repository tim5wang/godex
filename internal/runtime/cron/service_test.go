package cron

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/domain/automation"
	"github.com/tim5wang/godex/internal/domain/events"
	"github.com/tim5wang/godex/internal/domain/message"
	"github.com/tim5wang/godex/internal/runtime/channels"
	rtbackend "github.com/tim5wang/godex/internal/services/backend"
)

type fakeBackend struct {
	mu       sync.Mutex
	locators []rtbackend.SessionLocator
	sinks    map[string]events.Sink
	started  chan struct{}
	block    chan struct{}
}

func (b *fakeBackend) OpenSession(ctx context.Context, locator rtbackend.SessionLocator) (*rtbackend.OpenedSession, error) {
	_ = ctx
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.sinks == nil {
		b.sinks = make(map[string]events.Sink)
	}
	sessionID := locator.Channel + ":" + locator.Key
	b.locators = append(b.locators, locator)
	return &rtbackend.OpenedSession{SessionID: sessionID, Locator: locator}, nil
}

func (b *fakeBackend) Submit(ctx context.Context, sessionID string, envelope message.Envelope) (*rtbackend.SubmitResult, error) {
	_ = envelope
	if b.started != nil {
		select {
		case b.started <- struct{}{}:
		default:
		}
	}
	if b.block != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-b.block:
		}
	}
	b.mu.Lock()
	sink := b.sinks[sessionID]
	b.mu.Unlock()
	if sink != nil {
		sink.Emit(events.Event{
			SessionID: sessionID,
			Type:      events.EventAssistantTextDelta,
			Timestamp: time.Now(),
			Payload:   events.TextPayload{Role: "assistant", Text: "scheduled reply"},
		})
	}
	return &rtbackend.SubmitResult{SessionID: sessionID, TurnID: "turn-1", Completed: true, UpdatedAt: time.Now()}, nil
}

func (b *fakeBackend) AttachSink(sessionID string, sink events.Sink) (func(), error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.sinks == nil {
		b.sinks = make(map[string]events.Sink)
	}
	b.sinks[sessionID] = sink
	return func() {
		b.mu.Lock()
		delete(b.sinks, sessionID)
		b.mu.Unlock()
	}, nil
}

// SetSessionModelProfile is the default no-op for the Backend interface. The
// dedicated fakeBackendWithModel override records calls and returns
// configurable errors for the model-selection tests.
func (b *fakeBackend) SetSessionModelProfile(_ context.Context, _ string, _ string) error {
	return nil
}

type fakeDeliverer struct {
	targets []automation.DeliveryTarget
	plans   []channels.ReplyPlan
	err     error
}

func (d *fakeDeliverer) Deliver(ctx context.Context, target automation.DeliveryTarget, plan channels.ReplyPlan) error {
	_ = ctx
	d.targets = append(d.targets, target.Clone())
	d.plans = append(d.plans, plan)
	return d.err
}

func writeScript(t *testing.T, root, body string) string {
	t.Helper()
	path := filepath.Join(root, "wd.sh")
	if err := os.WriteFile(path, []byte(body), 0755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	return path
}

func createJob(t *testing.T, service *Service, script string) Job {
	t.Helper()
	job, err := service.CreateJob(CreateJobInput{
		Name:           "watchdog demo",
		Message:        "run me",
		Schedule:       Schedule{Type: ScheduleEvery, EverySeconds: 60},
		SessionMode:    SessionModeShared,
		WatchdogScript: script,
		Enabled:        true,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	return job
}

func TestRunJobWatchdogExitZeroRuns(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(t.TempDir())
	backend := &fakeBackend{}
	deliverer := &fakeDeliverer{}
	service := NewService(Config{Enabled: true, DefaultTimezone: "Asia/Shanghai", WorkspaceDir: root}, store, backend, deliverer)
	job := createJob(t, service, writeScript(t, root, "#!/bin/sh\necho ready\n"))

	run, err := service.RunNow(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("run now: %v", err)
	}
	if run.Status != JobStatusCompleted {
		t.Fatalf("exit 0 must run agent, got %s", run.Status)
	}
	if run.Suppressed {
		t.Fatal("exit 0 must not suppress")
	}
	if len(backend.locators) != 1 {
		t.Fatalf("agent session must be opened, got %d locators", len(backend.locators))
	}
	if !strings.Contains(run.WatchdogOutput, "ready") {
		t.Fatalf("expected watchdog output captured, got %q", run.WatchdogOutput)
	}
}

func TestRunJobWatchdogExitNonZeroSkips(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(t.TempDir())
	backend := &fakeBackend{}
	deliverer := &fakeDeliverer{}
	service := NewService(Config{Enabled: true, DefaultTimezone: "Asia/Shanghai", WorkspaceDir: root}, store, backend, deliverer)
	job := createJob(t, service, writeScript(t, root, "#!/bin/sh\n echo skip-me\nexit 3\n"))

	run, err := service.RunNow(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("non-zero exit is a skip, not an error, got %v", err)
	}
	if run.Status != JobStatusSuppressed {
		t.Fatalf("non-zero exit must suppress, got %s", run.Status)
	}
	if !run.Suppressed {
		t.Fatal("run must be flagged suppressed")
	}
	if len(backend.locators) != 0 {
		t.Fatalf("skipped run must not open an agent session, got %d locators", len(backend.locators))
	}
	if len(deliverer.targets) != 0 {
		t.Fatalf("skipped run must not deliver, got %d targets", len(deliverer.targets))
	}
	if !strings.Contains(run.Error, "watchdog skipped") {
		t.Fatalf("expected skip reason captured, got %q", run.Error)
	}
}

func TestRunJobWatchdogMissingScriptErrors(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(t.TempDir())
	backend := &fakeBackend{}
	deliverer := &fakeDeliverer{}
	service := NewService(Config{Enabled: true, DefaultTimezone: "Asia/Shanghai", WorkspaceDir: root}, store, backend, deliverer)
	job := createJob(t, service, filepath.Join(root, "nope.sh"))

	run, err := service.RunNow(context.Background(), job.ID)
	if err == nil {
		t.Fatalf("missing script must error")
	}
	if run.Status != JobStatusError {
		t.Fatalf("missing script must record error, got %s", run.Status)
	}
	if len(backend.locators) != 0 {
		t.Fatalf("error must not open an agent session, got %d locators", len(backend.locators))
	}
}

func TestRunJobWatchdogTimeoutErrors(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(t.TempDir())
	backend := &fakeBackend{}
	deliverer := &fakeDeliverer{}
	service := NewService(Config{Enabled: true, DefaultTimezone: "Asia/Shanghai", WorkspaceDir: root}, store, backend, deliverer)
	job := createJob(t, service, writeScript(t, root, "#!/bin/sh\nsleep 30\n"))

	run, err := service.RunNow(context.Background(), job.ID)
	if err == nil {
		t.Fatalf("timeout must error")
	}
	if run.Status != JobStatusError {
		t.Fatalf("timeout must record error, got %s", run.Status)
	}
	if len(backend.locators) != 0 {
		t.Fatalf("error must not open an agent session, got %d locators", len(backend.locators))
	}
}

func TestRunJobWatchdogDefaultScriptFallsBack(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(t.TempDir())
	backend := &fakeBackend{}
	deliverer := &fakeDeliverer{}
	service := NewService(Config{Enabled: true, DefaultTimezone: "Asia/Shanghai", WorkspaceDir: root, DefaultWatchdogScript: writeScript(t, root, "exit 1\n")}, store, backend, deliverer)
	job, err := service.CreateJob(CreateJobInput{
		Name:        "default wd",
		Message:     "run me",
		Schedule:    Schedule{Type: ScheduleEvery, EverySeconds: 60},
		SessionMode: SessionModeShared,
		Enabled:     true,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	run, err := service.RunNow(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("default watchdog should skip, not error, got %v", err)
	}
	if run.Status != JobStatusSuppressed {
		t.Fatalf("default watchdog must suppress, got %s", run.Status)
	}
}

func TestFileStoreRoundTrip(t *testing.T) {
	store := NewFileStore(t.TempDir())
	job := Job{
		ID:        "job-1",
		Name:      "demo",
		Message:   "hello",
		Enabled:   true,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Schedule:  Schedule{Type: ScheduleEvery, EverySeconds: 60},
	}
	if err := store.SaveJob(job); err != nil {
		t.Fatalf("save job: %v", err)
	}
	loaded, err := store.GetJob(job.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if loaded.Message != job.Message {
		t.Fatalf("expected message %q, got %q", job.Message, loaded.Message)
	}

	run := RunLog{ID: "run-1", JobID: job.ID, Status: JobStatusCompleted, StartedAt: time.Now()}
	if err := store.AppendRunLog(run); err != nil {
		t.Fatalf("append run: %v", err)
	}
	runs, err := store.ListRunLogs(job.ID, 10)
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs) != 1 || runs[0].ID != run.ID {
		t.Fatalf("unexpected runs: %#v", runs)
	}
}

func TestRunNowUsesSharedOrIsolatedSessionLocator(t *testing.T) {
	tests := []struct {
		name        string
		sessionMode SessionMode
	}{
		{name: "shared", sessionMode: SessionModeShared},
		{name: "isolated", sessionMode: SessionModeIsolated},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := NewFileStore(t.TempDir())
			backend := &fakeBackend{}
			deliverer := &fakeDeliverer{}
			service := NewService(Config{Enabled: true, DefaultTimezone: "Asia/Shanghai"}, store, backend, deliverer)
			job, err := service.CreateJob(CreateJobInput{
				Name:           "demo",
				Message:        "run me",
				Schedule:       Schedule{Type: ScheduleEvery, EverySeconds: 60},
				SessionMode:    tc.sessionMode,
				DeliveryTarget: automation.DeliveryTarget{Kind: automation.DeliveryKindSession, SessionID: "web-1"},
				Enabled:        true,
			})
			if err != nil {
				t.Fatalf("create job: %v", err)
			}
			run, err := service.RunNow(context.Background(), job.ID)
			if err != nil {
				t.Fatalf("run now: %v", err)
			}
			if run.SessionID == "" {
				t.Fatalf("expected run session id")
			}
			if len(backend.locators) != 1 {
				t.Fatalf("expected one locator, got %d", len(backend.locators))
			}
			locator := backend.locators[0]
			if locator.Channel != "cron" {
				t.Fatalf("unexpected locator channel: %#v", locator)
			}
			if tc.sessionMode == SessionModeShared && locator.Key != job.ID {
				t.Fatalf("expected shared key %q, got %q", job.ID, locator.Key)
			}
			if tc.sessionMode == SessionModeIsolated && !strings.HasPrefix(locator.Key, job.ID+":") {
				t.Fatalf("expected isolated key prefix %q:, got %q", job.ID, locator.Key)
			}
			if len(deliverer.targets) != 1 || deliverer.targets[0].SessionID != "web-1" {
				t.Fatalf("unexpected delivery targets: %#v", deliverer.targets)
			}
		})
	}
}

func TestRunNowPersistsBlockedDelivery(t *testing.T) {
	store := NewFileStore(t.TempDir())
	backend := &fakeBackend{}
	deliverer := &fakeDeliverer{err: automation.NewBlockedError("missing context token")}
	service := NewService(Config{Enabled: true, DefaultTimezone: "Asia/Shanghai"}, store, backend, deliverer)
	job, err := service.CreateJob(CreateJobInput{
		Name:           "weixin push",
		Message:        "hello",
		Schedule:       Schedule{Type: ScheduleEvery, EverySeconds: 60},
		SessionMode:    SessionModeShared,
		DeliveryTarget: automation.DeliveryTarget{Kind: automation.DeliveryKindChannel, Channel: "weixin", Recipient: "user-1"},
		Enabled:        true,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	run, err := service.RunNow(context.Background(), job.ID)
	if err == nil {
		t.Fatalf("expected blocked delivery error")
	}
	if run.Status != JobStatusDeliveryBlocked {
		t.Fatalf("expected delivery_blocked, got %s", run.Status)
	}
	storedJob, err := store.GetJob(job.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if storedJob.LastStatus != JobStatusDeliveryBlocked {
		t.Fatalf("expected job last status delivery_blocked, got %s", storedJob.LastStatus)
	}
	logs, err := store.ListRunLogs(job.ID, 1)
	if err != nil {
		t.Fatalf("list run logs: %v", err)
	}
	if len(logs) != 1 || logs[0].Status != JobStatusDeliveryBlocked {
		t.Fatalf("unexpected logs: %#v", logs)
	}
}

func TestDispatchDueExecutesPastAtJob(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	backend := &fakeBackend{}
	deliverer := &fakeDeliverer{}
	service := NewService(Config{Enabled: true, DefaultTimezone: "Asia/Shanghai", MaxConcurrentRuns: 1}, store, backend, deliverer)
	now := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }

	job := Job{
		ID:          "job-1",
		Name:        "once",
		Message:     "do it",
		Enabled:     true,
		CreatedAt:   now.Add(-time.Hour),
		UpdatedAt:   now.Add(-time.Hour),
		Schedule:    Schedule{Type: ScheduleAt, At: now.Add(-time.Minute)},
		SessionMode: SessionModeShared,
		Timezone:    "Asia/Shanghai",
		NextRunAt:   now.Add(-time.Minute),
	}
	if err := store.SaveJob(job); err != nil {
		t.Fatalf("save job: %v", err)
	}
	if err := service.dispatchDue(context.Background()); err != nil {
		t.Fatalf("dispatch due: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	storedJob, err := store.GetJob(job.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if storedJob.LastStatus == "" {
		t.Fatalf("expected job to run")
	}
	if !storedJob.NextRunAt.IsZero() {
		t.Fatalf("expected at job next run to be cleared, got %s", storedJob.NextRunAt)
	}
	if len(deliverer.targets) != 0 {
		t.Fatalf("expected no delivery for zero target, got %#v", deliverer.targets)
	}
	if _, err := os.Stat(filepath.Join(root, "cron", "runs", job.ID)); err != nil {
		t.Fatalf("expected run log directory: %v", err)
	}
}

func TestStopWaitsForInFlightRunCancellation(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	backend := &fakeBackend{
		started: make(chan struct{}, 1),
		block:   make(chan struct{}),
	}
	service := NewService(Config{
		Enabled:           true,
		TickSeconds:       1,
		DefaultTimezone:   "Asia/Shanghai",
		MaxConcurrentRuns: 1,
	}, store, backend, nil)
	now := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }

	job := Job{
		ID:          "job-stop",
		Name:        "stop-test",
		Message:     "wait",
		Enabled:     true,
		CreatedAt:   now.Add(-time.Hour),
		UpdatedAt:   now.Add(-time.Hour),
		Schedule:    Schedule{Type: ScheduleEvery, EverySeconds: 60},
		SessionMode: SessionModeShared,
		Timezone:    "Asia/Shanghai",
		NextRunAt:   now.Add(-time.Minute),
	}
	if err := store.SaveJob(job); err != nil {
		t.Fatalf("save job: %v", err)
	}

	if err := service.Start(context.Background()); err != nil {
		t.Fatalf("start service: %v", err)
	}

	select {
	case <-backend.started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for cron run to start")
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := service.Stop(stopCtx); err != nil {
		t.Fatalf("stop service: %v", err)
	}

	logs, err := store.ListRunLogs(job.ID, 1)
	if err != nil {
		t.Fatalf("list run logs: %v", err)
	}
	if len(logs) != 1 || logs[0].Status != JobStatusError || !strings.Contains(logs[0].Error, context.Canceled.Error()) {
		t.Fatalf("expected canceled run log, got %#v", logs)
	}
}

func TestBuildCronPromptMarksExecutionMode(t *testing.T) {
	job := Job{
		ID:      "job-1",
		Message: "提醒：验收voice search项目",
	}
	prompt := buildCronPrompt(job)
	for _, want := range []string{
		"scheduled cron execution",
		"Do not create, update, toggle, delete, run, or test cron or heartbeat tasks",
		"提醒：验收voice search项目",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("expected prompt to contain %q, got %q", want, prompt)
		}
	}
}

func TestUpdateJobPreservesNextRunForMetadataOnlyChanges(t *testing.T) {
	store := NewFileStore(t.TempDir())
	backend := &fakeBackend{}
	deliverer := &fakeDeliverer{}
	service := NewService(Config{Enabled: true, DefaultTimezone: "Asia/Shanghai"}, store, backend, deliverer)
	now := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }

	job, err := service.CreateJob(CreateJobInput{
		Name:           "before",
		Message:        "hello",
		Schedule:       Schedule{Type: ScheduleEvery, EverySeconds: 3600},
		SessionMode:    SessionModeShared,
		DeliveryTarget: automation.DeliveryTarget{Kind: automation.DeliveryKindSession, SessionID: "web-1"},
		Enabled:        true,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	originalNextRun := job.NextRunAt
	newName := "after"

	updated, err := service.UpdateJob(UpdateJobInput{
		ID:   job.ID,
		Name: &newName,
	})
	if err != nil {
		t.Fatalf("update job: %v", err)
	}
	if !updated.NextRunAt.Equal(originalNextRun) {
		t.Fatalf("expected next_run_at to be preserved, got %s want %s", updated.NextRunAt, originalNextRun)
	}
}
