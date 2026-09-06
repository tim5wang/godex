package backend

import (
	"context"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/agent"
	"github.com/tim5wang/godex/internal/domain/message"
)

type backendTestHarness struct {
	id     string
	called bool
}

func (h *backendTestHarness) Profile() agent.HarnessProfile {
	return agent.HarnessProfile{ID: h.id, Name: h.id}
}
func (h *backendTestHarness) Models() []string { return nil }
func (h *backendTestHarness) Tools() []string  { return nil }
func (h *backendTestHarness) RunTurn(context.Context, agent.HarnessTurnInput) (agent.HarnessTurnResult, error) {
	h.called = true
	return agent.HarnessTurnResult{Reply: "external reply", Completed: true}, nil
}
func (h *backendTestHarness) ResetSession(context.Context, string) error { return nil }
func (h *backendTestHarness) Close() error                               { return nil }

func TestHarnessMetadataRoutesAndSnapshotListsHarnesses(t *testing.T) {
	service := newTestService(newTestConfig(t), &stubCaller{})
	opened, err := service.OpenSession(context.Background(), SessionLocator{Channel: "web", Key: "harness-route"})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	state, err := service.requireSession(opened.SessionID)
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	harness := &backendTestHarness{id: "acp:test"}
	state.agent.RegisterHarness(harness.id, harness)
	envelope := message.NewTextEnvelope(message.SourceWeb, opened.SessionID, "user", "hello", service.now())
	envelope.Metadata = map[string]string{"harness": harness.id}
	if _, err := service.Submit(context.Background(), opened.SessionID, envelope); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if !harness.called {
		t.Fatal("selected harness was not invoked")
	}
	snapshot, err := service.Snapshot(context.Background(), opened.SessionID)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if strings.Join(snapshot.HarnessIDs, ",") != "acp:test,godex" {
		t.Fatalf("snapshot harness ids = %v", snapshot.HarnessIDs)
	}
}

func TestHarnessMetadataRejectsUnavailableHarness(t *testing.T) {
	service := newTestService(newTestConfig(t), &stubCaller{})
	opened, err := service.OpenSession(context.Background(), SessionLocator{Channel: "web", Key: "harness-invalid"})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	envelope := message.NewTextEnvelope(message.SourceWeb, opened.SessionID, "user", "hello", service.now())
	envelope.Metadata = map[string]string{"harness": "acp:missing"}
	_, err = service.Submit(context.Background(), opened.SessionID, envelope)
	if err == nil || !strings.Contains(err.Error(), `harness "acp:missing" is unavailable`) || !strings.Contains(err.Error(), "godex") {
		t.Fatalf("expected explicit unavailable harness error with choices, got %v", err)
	}
}
