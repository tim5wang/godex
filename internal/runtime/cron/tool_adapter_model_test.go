package cron

import (
	"context"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/domain/automation"
)

func TestToolAdapterRoundTripModelProfileID(t *testing.T) {
	store := NewFileStore(t.TempDir())
	backend := &fakeBackend{}
	deliverer := &fakeDeliverer{}
	service := NewService(Config{Enabled: true, DefaultTimezone: "Asia/Shanghai"}, store, backend, deliverer)
	adapter := NewToolAdapter(service)

	created, err := adapter.CreateJob(automation.CronCreateInput{
		Name:            "tool-create",
		Message:         "hello",
		Schedule:        automation.CronSchedule{Type: "every", EverySeconds: 60},
		SessionMode:     "shared",
		ModelProfileID:  "openai-fast",
		Enabled:         true,
		CreatedBy:       "tool",
		CreatedFromSession: "session-1",
	})
	if err != nil {
		t.Fatalf("tool create job: %v", err)
	}
	if created.ModelProfileID != "openai-fast" {
		t.Fatalf("expected automation.CronJob.ModelProfileID %q, got %q", "openai-fast", created.ModelProfileID)
	}

	fetched, err := adapter.GetJob(created.ID)
	if err != nil {
		t.Fatalf("tool get job: %v", err)
	}
	if fetched.ModelProfileID != "openai-fast" {
		t.Fatalf("expected fetched model profile id %q, got %q", "openai-fast", fetched.ModelProfileID)
	}

	newProfile := "anthropic-sonnet"
	updated, err := adapter.UpdateJob(automation.CronUpdateInput{
		ID:             created.ID,
		ModelProfileID: &newProfile,
	})
	if err != nil {
		t.Fatalf("tool update job: %v", err)
	}
	if updated.ModelProfileID != "anthropic-sonnet" {
		t.Fatalf("expected updated model profile id %q, got %q", "anthropic-sonnet", updated.ModelProfileID)
	}

	listed, err := adapter.ListJobs()
	if err != nil {
		t.Fatalf("tool list jobs: %v", err)
	}
	if len(listed) != 1 || listed[0].ModelProfileID != "anthropic-sonnet" {
		t.Fatalf("expected listed job with model profile id %q, got %#v", "anthropic-sonnet", listed)
	}
}

func TestToolAdapterRoundTripWatchdogScript(t *testing.T) {
	store := NewFileStore(t.TempDir())
	backend := &fakeBackend{}
	deliverer := &fakeDeliverer{}
	service := NewService(Config{Enabled: true, DefaultTimezone: "Asia/Shanghai"}, store, backend, deliverer)
	adapter := NewToolAdapter(service)

	created, err := adapter.CreateJob(automation.CronCreateInput{
		Name:            "tool-wd",
		Message:         "hello",
		Schedule:        automation.CronSchedule{Type: "every", EverySeconds: 60},
		SessionMode:     "shared",
		WatchdogScript:  "/tmp/wd.sh",
		Enabled:         true,
		CreatedBy:       "tool",
		CreatedFromSession: "session-1",
	})
	if err != nil {
		t.Fatalf("tool create job: %v", err)
	}
	if created.WatchdogScript != "/tmp/wd.sh" {
		t.Fatalf("expected automation.CronJob.WatchdogScript %q, got %q", "/tmp/wd.sh", created.WatchdogScript)
	}

	fetched, err := adapter.GetJob(created.ID)
	if err != nil {
		t.Fatalf("tool get job: %v", err)
	}
	if fetched.WatchdogScript != "/tmp/wd.sh" {
		t.Fatalf("expected fetched watchdog script %q, got %q", "/tmp/wd.sh", fetched.WatchdogScript)
	}

	newScript := "/tmp/wd2.sh"
	updated, err := adapter.UpdateJob(automation.CronUpdateInput{
		ID:             created.ID,
		WatchdogScript: &newScript,
	})
	if err != nil {
		t.Fatalf("tool update job: %v", err)
	}
	if updated.WatchdogScript != "/tmp/wd2.sh" {
		t.Fatalf("expected updated watchdog script %q, got %q", "/tmp/wd2.sh", updated.WatchdogScript)
	}

	listed, err := adapter.ListJobs()
	if err != nil {
		t.Fatalf("tool list jobs: %v", err)
	}
	if len(listed) != 1 || listed[0].WatchdogScript != "/tmp/wd2.sh" {
		t.Fatalf("expected listed job with watchdog script %q, got %#v", "/tmp/wd2.sh", listed)
	}
}

// keep imports honest
var (
	_ = context.Background
	_ = time.Now
)
