package heartbeat

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/tim5wang/godex/internal/domain/automation"
)

func newWatchdogTestService(t *testing.T, scriptBody string) (*Service, *fakeBackend, *fakeDeliverer, *FileStore) {
	t.Helper()
	root := t.TempDir()
	checklistPath := filepath.Join(root, "HEARTBEAT.md")
	if err := os.WriteFile(checklistPath, []byte("- check inbox"), 0644); err != nil {
		t.Fatalf("write checklist: %v", err)
	}
	script := filepath.Join(root, "wd.sh")
	if scriptBody != "" {
		if err := os.WriteFile(script, []byte(scriptBody), 0755); err != nil {
			t.Fatalf("write watchdog script: %v", err)
		}
	}
	store := NewFileStore(root)
	backend := &fakeBackend{assistantText: "needs attention"}
	deliverer := &fakeDeliverer{}
	service := NewService(Config{
		Enabled:                true,
		TickSeconds:            1,
		ChecklistPath:          checklistPath,
		WorkspaceDir:           root,
		StateDir:               filepath.Join(root, ".godex"),
		OKToken:                "HEARTBEAT_OK",
		DefaultIntervalSeconds: 1800,
		DefaultTimezone:        "Asia/Shanghai",
	}, store, backend, deliverer)
	return service, backend, deliverer, store
}

func TestRunRuleWatchdogExitZeroRunsAgent(t *testing.T) {
	service, backend, deliverer, _ := newWatchdogTestService(t, "#!/bin/sh\nexit 0\n")
	root := service.cfg.WorkspaceDir
	script := filepath.Join(root, "wd.sh")
	enabled := true
	target := automation.DeliveryTarget{Kind: automation.DeliveryKindSession, SessionID: "web-1"}
	if _, err := service.SetRule(SetRuleInput{Enabled: &enabled, WatchdogScript: &script, DeliveryTarget: &target}); err != nil {
		t.Fatalf("set rule: %v", err)
	}
	run, err := service.TestNow(context.Background())
	if err != nil {
		t.Fatalf("test now: %v", err)
	}
	if run.Status != RuleStatusCompleted {
		t.Fatalf("expected completed run when watchdog passes, got %+v", run)
	}
	if run.Suppressed {
		t.Fatal("watchdog pass must not suppress")
	}
	if len(backend.locators) != 1 {
		t.Fatalf("expected agent to run once, got %d opens", len(backend.locators))
	}
	if len(deliverer.targets) != 1 {
		t.Fatalf("expected delivery, got %+v", deliverer.targets)
	}
}

func TestRunRuleWatchdogExitNonZeroSkipsAgent(t *testing.T) {
	service, backend, deliverer, store := newWatchdogTestService(t, "#!/bin/sh\necho skip\nexit 5\n")
	enabled := true
	script := filepath.Join(service.cfg.WorkspaceDir, "wd.sh")
	if _, err := service.SetRule(SetRuleInput{Enabled: &enabled, WatchdogScript: &script}); err != nil {
		t.Fatalf("set rule: %v", err)
	}
	run, err := service.TestNow(context.Background())
	if err != nil {
		t.Fatalf("test now: %v", err)
	}
	if run.Status != RuleStatusSuppressed || !run.Suppressed {
		t.Fatalf("expected suppressed run, got %+v", run)
	}
	if len(backend.locators) != 0 {
		t.Fatalf("agent must not run when watchdog skips, got %d opens", len(backend.locators))
	}
	if len(deliverer.targets) != 0 {
		t.Fatalf("no delivery for skipped run, got %+v", deliverer.targets)
	}
	stored, err := store.GetRule()
	if err != nil {
		t.Fatalf("get rule: %v", err)
	}
	if stored.LastStatus != RuleStatusSuppressed {
		t.Fatalf("expected stored rule suppressed, got %+v", stored)
	}
}

func TestRunRuleWatchdogMissingScriptErrors(t *testing.T) {
	service, backend, _, _ := newWatchdogTestService(t, "")
	enabled := true
	script := filepath.Join(service.cfg.WorkspaceDir, "missing.sh")
	if _, err := service.SetRule(SetRuleInput{Enabled: &enabled, WatchdogScript: &script}); err != nil {
		t.Fatalf("set rule: %v", err)
	}
	run, err := service.TestNow(context.Background())
	if err == nil {
		t.Fatalf("expected error for missing watchdog script, got run %+v", run)
	}
	if run.Status != RuleStatusError {
		t.Fatalf("expected error status, got %+v", run)
	}
	if len(backend.locators) != 0 {
		t.Fatalf("agent must not run when watchdog errors, got %d opens", len(backend.locators))
	}
}
