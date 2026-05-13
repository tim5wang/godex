package agent

import (
	"context"
	"testing"
)

func TestInspectContextDoesNotRequireSandboxInspectorSurface(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()

	inspection, err := a.InspectContext(context.Background(), "session-sandbox")
	if err != nil {
		t.Fatalf("inspect context: %v", err)
	}
	if inspection.SessionID != "session-sandbox" {
		t.Fatalf("session id %q", inspection.SessionID)
	}
	if a.SandboxID() == "" {
		t.Fatalf("expected agent sandbox id")
	}
}
