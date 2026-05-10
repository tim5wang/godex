package heartbeat

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
	mu            sync.Mutex
	locators      []rtbackend.SessionLocator
	sinks         map[string]events.Sink
	assistantText string
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
	_ = ctx
	_ = envelope
	b.mu.Lock()
	sink := b.sinks[sessionID]
	text := b.assistantText
	b.mu.Unlock()
	if text == "" {
		text = "heartbeat update"
	}
	if sink != nil {
		sink.Emit(events.Event{
			SessionID: sessionID,
			Type:      events.EventAssistantTextDelta,
			Timestamp: time.Now(),
			Payload:   events.TextPayload{Role: "assistant", Text: text},
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

func TestTestNowSuppressesOnOKToken(t *testing.T) {
	root := t.TempDir()
	checklistPath := filepath.Join(root, "HEARTBEAT.md")
	if err := os.WriteFile(checklistPath, []byte("- check inbox"), 0644); err != nil {
		t.Fatalf("write checklist: %v", err)
	}
	store := NewFileStore(root)
	backend := &fakeBackend{assistantText: "HEARTBEAT_OK"}
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

	enabled := true
	rule, err := service.SetRule(SetRuleInput{Enabled: &enabled})
	if err != nil {
		t.Fatalf("set rule: %v", err)
	}
	run, err := service.TestNow(context.Background())
	if err != nil {
		t.Fatalf("test now: %v", err)
	}
	if run.Status != RuleStatusSuppressed || !run.Suppressed {
		t.Fatalf("expected suppressed run, got %+v", run)
	}
	if len(deliverer.targets) != 0 {
		t.Fatalf("expected no delivery for suppressed heartbeat, got %+v", deliverer.targets)
	}
	storedRule, err := store.GetRule()
	if err != nil {
		t.Fatalf("get rule: %v", err)
	}
	if storedRule.LastStatus != RuleStatusSuppressed {
		t.Fatalf("expected stored rule to be suppressed, got %+v (seed %+v)", storedRule, rule)
	}
}

func TestDispatchDueHonorsActiveHours(t *testing.T) {
	root := t.TempDir()
	checklistPath := filepath.Join(root, "HEARTBEAT.md")
	if err := os.WriteFile(checklistPath, []byte("- check inbox"), 0644); err != nil {
		t.Fatalf("write checklist: %v", err)
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
	now := time.Date(2026, 4, 21, 0, 30, 0, 0, time.UTC)
	service.now = func() time.Time { return now }

	enabled := true
	interval := 1800
	start := "09:00"
	end := "18:00"
	target := automation.DeliveryTarget{Kind: automation.DeliveryKindSession, SessionID: "web-1"}
	rule, err := service.SetRule(SetRuleInput{
		Enabled:          &enabled,
		IntervalSeconds:  &interval,
		ActiveHoursStart: &start,
		ActiveHoursEnd:   &end,
		DeliveryTarget:   &target,
	})
	if err != nil {
		t.Fatalf("set rule: %v", err)
	}
	rule.NextRunAt = now.Add(-time.Minute)
	if err := store.SaveRule(rule); err != nil {
		t.Fatalf("seed rule: %v", err)
	}

	if err := service.dispatchDue(context.Background()); err != nil {
		t.Fatalf("dispatch due: %v", err)
	}
	if len(deliverer.targets) != 0 {
		t.Fatalf("expected no delivery outside active hours, got %+v", deliverer.targets)
	}

	service.now = func() time.Time {
		return time.Date(2026, 4, 21, 1, 30, 0, 0, time.UTC)
	}
	if err := service.dispatchDue(context.Background()); err != nil {
		t.Fatalf("dispatch due second pass: %v", err)
	}
	if len(deliverer.targets) != 1 || !strings.Contains(deliverer.plans[0].RenderText(), "needs attention") {
		t.Fatalf("expected delivery inside active hours, got targets=%+v plans=%+v", deliverer.targets, deliverer.plans)
	}
}
