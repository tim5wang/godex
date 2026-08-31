package cron

import (
	"context"
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

// fakeBackendWithModel is a Backend that also implements SetSessionModelProfile.
// Tests use it to assert that cron.runJob calls SetSessionModelProfile with the
// job's selected profile after OpenSession returns.
type fakeBackendWithModel struct {
	fakeBackend

	mu             sync.Mutex
	selectedCalls  []selectedProfileCall
	selectedErr    error
	selectedBlocks map[string]chan struct{}
}

type selectedProfileCall struct {
	SessionID string
	ProfileID string
}

func newFakeBackendWithModel() *fakeBackendWithModel {
	return &fakeBackendWithModel{
		selectedBlocks: make(map[string]chan struct{}),
	}
}

func (b *fakeBackendWithModel) SetSessionModelProfile(ctx context.Context, sessionID, profileID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.selectedCalls = append(b.selectedCalls, selectedProfileCall{SessionID: sessionID, ProfileID: profileID})
	return b.selectedErr
}

func (b *fakeBackendWithModel) SelectedCalls() []selectedProfileCall {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]selectedProfileCall, len(b.selectedCalls))
	copy(out, b.selectedCalls)
	return out
}

func TestCreateJobPersistsModelProfileID(t *testing.T) {
	store := NewFileStore(t.TempDir())
	backend := &fakeBackend{}
	deliverer := &fakeDeliverer{}
	service := NewService(Config{Enabled: true, DefaultTimezone: "Asia/Shanghai"}, store, backend, deliverer)

	job, err := service.CreateJob(CreateJobInput{
		Name:            "with-model",
		Message:         "hello",
		Schedule:        Schedule{Type: ScheduleEvery, EverySeconds: 60},
		SessionMode:     SessionModeShared,
		ModelProfileID:  "openai-fast",
		Enabled:         true,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if job.ModelProfileID != "openai-fast" {
		t.Fatalf("expected job model profile id %q, got %q", "openai-fast", job.ModelProfileID)
	}

	loaded, err := store.GetJob(job.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if loaded.ModelProfileID != "openai-fast" {
		t.Fatalf("expected persisted model profile id %q, got %q", "openai-fast", loaded.ModelProfileID)
	}
}

func TestUpdateJobCanChangeModelProfileID(t *testing.T) {
	store := NewFileStore(t.TempDir())
	backend := &fakeBackend{}
	deliverer := &fakeDeliverer{}
	service := NewService(Config{Enabled: true, DefaultTimezone: "Asia/Shanghai"}, store, backend, deliverer)

	job, err := service.CreateJob(CreateJobInput{
		Name:           "switch-model",
		Message:        "hello",
		Schedule:       Schedule{Type: ScheduleEvery, EverySeconds: 3600},
		SessionMode:    SessionModeShared,
		ModelProfileID: "openai-fast",
		Enabled:        true,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	originalNextRun := job.NextRunAt

	newProfile := "anthropic-sonnet"
	updated, err := service.UpdateJob(UpdateJobInput{
		ID:             job.ID,
		ModelProfileID: &newProfile,
	})
	if err != nil {
		t.Fatalf("update job: %v", err)
	}
	if updated.ModelProfileID != "anthropic-sonnet" {
		t.Fatalf("expected updated model profile id %q, got %q", "anthropic-sonnet", updated.ModelProfileID)
	}
	// model-profile change should not recompute next_run_at, only the schedule / enabled / timezone
	// should. This mirrors the existing TestUpdateJobPreservesNextRunForMetadataOnlyChanges rule.
	if !updated.NextRunAt.Equal(originalNextRun) {
		t.Fatalf("expected next_run_at to be preserved on model-profile update, got %s want %s", updated.NextRunAt, originalNextRun)
	}

	clearProfile := ""
	updated, err = service.UpdateJob(UpdateJobInput{
		ID:             job.ID,
		ModelProfileID: &clearProfile,
	})
	if err != nil {
		t.Fatalf("clear profile: %v", err)
	}
	if updated.ModelProfileID != "" {
		t.Fatalf("expected cleared model profile id, got %q", updated.ModelProfileID)
	}
}

func TestRunJobAppliesModelProfileToBackend(t *testing.T) {
	store := NewFileStore(t.TempDir())
	backend := newFakeBackendWithModel()
	deliverer := &fakeDeliverer{}
	service := NewService(Config{Enabled: true, DefaultTimezone: "Asia/Shanghai"}, store, backend, deliverer)

	job, err := service.CreateJob(CreateJobInput{
		Name:           "apply-model",
		Message:        "hello",
		Schedule:       Schedule{Type: ScheduleEvery, EverySeconds: 60},
		SessionMode:    SessionModeShared,
		ModelProfileID: "anthropic-sonnet",
		Enabled:        true,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	run, err := service.RunNow(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("run now: %v", err)
	}
	run = waitForRunStatus(t, store, job.ID, run.ID, 10*time.Second)
	if run.Status != JobStatusCompleted {
		t.Fatalf("expected completed run, got %s", run.Status)
	}

	calls := backend.SelectedCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 SetSessionModelProfile call, got %d (%v)", len(calls), calls)
	}
	if calls[0].ProfileID != "anthropic-sonnet" {
		t.Fatalf("expected profile %q, got %q", "anthropic-sonnet", calls[0].ProfileID)
	}
	if calls[0].SessionID == "" {
		t.Fatalf("expected non-empty session id in SetSessionModelProfile call")
	}

	// The cron locator should also advertise the selected profile so that
	// other backend consumers (e.g. openai stream) can read it from metadata
	// without re-deriving the model from session state.
	backend.mu.Lock()
	locator := backend.locators[0]
	backend.mu.Unlock()
	if got := strings.TrimSpace(locator.Metadata["model_profile_id"]); got != "anthropic-sonnet" {
		t.Fatalf("expected locator metadata model_profile_id %q, got %q", "anthropic-sonnet", got)
	}
}

func TestRunJobSkipsSetSessionModelProfileWhenNoProfileSelected(t *testing.T) {
	store := NewFileStore(t.TempDir())
	backend := newFakeBackendWithModel()
	deliverer := &fakeDeliverer{}
	service := NewService(Config{Enabled: true, DefaultTimezone: "Asia/Shanghai"}, store, backend, deliverer)

	job, err := service.CreateJob(CreateJobInput{
		Name:        "no-model",
		Message:     "hello",
		Schedule:    Schedule{Type: ScheduleEvery, EverySeconds: 60},
		SessionMode: SessionModeShared,
		// no ModelProfileID -> use session default
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	run, err := service.RunNow(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("run now: %v", err)
	}
	run = waitForRunStatus(t, store, job.ID, run.ID, 10*time.Second)
	if run.Status != JobStatusCompleted {
		t.Fatalf("expected completed run, got %s", run.Status)
	}

	if calls := backend.SelectedCalls(); len(calls) != 0 {
		t.Fatalf("expected no SetSessionModelProfile calls when profile is empty, got %d (%v)", len(calls), calls)
	}
}

func TestRunJobRecordsErrorWhenSetSessionModelProfileFails(t *testing.T) {
	store := NewFileStore(t.TempDir())
	backend := newFakeBackendWithModel()
	backend.selectedErr = &backendProfileNotFoundError{profileID: "missing-profile"}
	deliverer := &fakeDeliverer{}
	service := NewService(Config{Enabled: true, DefaultTimezone: "Asia/Shanghai"}, store, backend, deliverer)

	job, err := service.CreateJob(CreateJobInput{
		Name:           "bad-model",
		Message:        "hello",
		Schedule:       Schedule{Type: ScheduleEvery, EverySeconds: 60},
		SessionMode:    SessionModeShared,
		ModelProfileID: "missing-profile",
		Enabled:        true,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	run, err := service.RunNow(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("run now: %v", err)
	}
	run = waitForRunStatus(t, store, job.ID, run.ID, 10*time.Second)
	if !strings.Contains(run.Error, "missing-profile") {
		t.Fatalf("expected run error to surface missing profile, got %v", run.Error)
	}
	if run.Status != JobStatusError {
		t.Fatalf("expected run status %q, got %q", JobStatusError, run.Status)
	}
}

// backendProfileNotFoundError is a tiny stand-in for backend's profile-not-found
// error to keep the test independent of backend types.
type backendProfileNotFoundError struct {
	profileID string
}

func (e *backendProfileNotFoundError) Error() string {
	return "model profile not found: " + e.profileID
}

// keep imports honest if a test above stops using any of them.
var (
	_ = time.Time{}
	_ = events.Sink(nil)
	_ = channels.ReplyPlan{}
	_ = message.Envelope{}
	_ = rtbackend.OpenedSession{}
	_ = automation.DeliveryTarget{}
)
