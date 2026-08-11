package agent

import (
	"context"
	"strings"
	"testing"
)

// fakeHarness records calls for router behavior verification.
type fakeHarness struct {
	id           string
	models       []string
	tools        []string
	runTurns     int
	resetSessions []string
	closed       bool
}

func (f *fakeHarness) Profile() HarnessProfile {
	return HarnessProfile{ID: f.id, Name: f.id, Models: f.models, Tools: f.tools}
}
func (f *fakeHarness) Models() []string { return f.models }
func (f *fakeHarness) Tools() []string  { return f.tools }
func (f *fakeHarness) RunTurn(context.Context, HarnessTurnInput) (HarnessTurnResult, error) {
	f.runTurns++
	return HarnessTurnResult{Completed: true, Reply: "ok from " + f.id}, nil
}
func (f *fakeHarness) ResetSession(_ context.Context, sessionID string) error {
	f.resetSessions = append(f.resetSessions, sessionID)
	return nil
}
func (f *fakeHarness) Close() error {
	f.closed = true
	return nil
}

func TestGodexHarnessImplementsHarness(t *testing.T) {
	var _ Harness = NewGodexHarness(nil)
}

func TestHarnessRouterRoutesToSelectedAdapter(t *testing.T) {
	primary := &fakeHarness{id: "godex", models: []string{"m1"}, tools: []string{"read_file"}}
	other := &fakeHarness{id: "codex", models: []string{"m2"}, tools: []string{"bash"}}
	router := NewHarnessRouter(map[string]Harness{"godex": primary, "codex": other}, NewDefaultHarnessResolver("codex"))

	result, err := router.RunTurn(context.Background(), HarnessTurnInput{SessionID: "s1", TurnID: "t1"})
	if err != nil {
		t.Fatalf("RunTurn: unexpected error: %v", err)
	}
	if primary.runTurns != 0 {
		t.Fatalf("expected primary harness untouched, got %d runs", primary.runTurns)
	}
	if other.runTurns != 1 || result.Reply != "ok from codex" {
		t.Fatalf("expected routed to codex, got %+v / %+v", other.runTurns, result)
	}
	if got := router.Profile().ID; got != "godex" {
		t.Fatalf("router.Profile().ID = %q, want godex (default profile)", got)
	}
	if got := router.Models(); len(got) != 1 || got[0] != "m1" {
		t.Fatalf("router.Models() = %v, want [m1]", got)
	}
}

func TestHarnessRouterResetsSessionOnSwitch(t *testing.T) {
	primary := &fakeHarness{id: "godex"}
	other := &fakeHarness{id: "codex"}
	var selected string
	router := NewHarnessRouter(map[string]Harness{"godex": primary, "codex": other}, func(_ context.Context, _ HarnessTurnInput) (string, error) {
		return selected, nil
	})

	selected = "godex"
	if _, err := router.RunTurn(context.Background(), HarnessTurnInput{SessionID: "s1"}); err != nil {
		t.Fatalf("first turn: %v", err)
	}
	selected = "codex"
	if _, err := router.RunTurn(context.Background(), HarnessTurnInput{SessionID: "s1"}); err != nil {
		t.Fatalf("second turn: %v", err)
	}
	if len(primary.resetSessions) != 1 || primary.resetSessions[0] != "s1" {
		t.Fatalf("expected old harness reset once, got %v", primary.resetSessions)
	}
	if len(other.resetSessions) != 1 || other.resetSessions[0] != "s1" {
		t.Fatalf("expected new harness reset once, got %v", other.resetSessions)
	}

	// Same harness again: no reset.
	if _, err := router.RunTurn(context.Background(), HarnessTurnInput{SessionID: "s1"}); err != nil {
		t.Fatalf("third turn: %v", err)
	}
	if len(primary.resetSessions) != 1 {
		t.Fatalf("expected no reset on same harness, got %v", primary.resetSessions)
	}
}

func TestHarnessRouterCloseClosesEachAdapterOnce(t *testing.T) {
	primary := &fakeHarness{id: "godex"}
	other := &fakeHarness{id: "codex"}
	router := NewHarnessRouter(map[string]Harness{"godex": primary, "codex": other}, nil)
	if err := router.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !primary.closed || !other.closed {
		t.Fatalf("expected all adapters closed, primary=%v other=%v", primary.closed, other.closed)
	}
}

func TestHarnessRouterUnavailableAdapter(t *testing.T) {
	router := NewHarnessRouter(map[string]Harness{"godex": &fakeHarness{id: "godex"}}, NewDefaultHarnessResolver("missing"))
	_, err := router.RunTurn(context.Background(), HarnessTurnInput{SessionID: "s1"})
	if err == nil {
		t.Fatalf("expected error for unavailable harness")
	}
	if !strings.Contains(err.Error(), "missing") {
		t.Fatalf("expected error to name harness, got %v", err)
	}
}

func TestGodexHarnessRunTurnDelegatesToAgentLoop(t *testing.T) {
	// NewGodexHarness with a nil agent returns a completed no-op result so the
	// seam is safe before wiring; a real agent is exercised in integration
	// tests through RunWithOptions.
	harness := NewGodexHarness(nil)
	result, err := harness.RunTurn(context.Background(), HarnessTurnInput{SessionID: "s1"})
	if err != nil {
		t.Fatalf("RunTurn with nil agent: %v", err)
	}
	if result.Completed {
		t.Fatalf("expected no completion from nil agent, got %+v", result)
	}
	if err := harness.ResetSession(context.Background(), "s1"); err != nil {
		t.Fatalf("ResetSession: %v", err)
	}
	if err := harness.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}
