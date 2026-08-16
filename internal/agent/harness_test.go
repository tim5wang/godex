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

func TestRequestedHarnessResolverHonorsInput(t *testing.T) {
	resolve := NewRequestedHarnessResolver("godex")
	got, err := resolve(context.Background(), HarnessTurnInput{Harness: "codex"})
	if err != nil || got != "codex" {
		t.Fatalf("resolver with request = %q, %v, want codex", got, err)
	}
	got, err = resolve(context.Background(), HarnessTurnInput{})
	if err != nil || got != "godex" {
		t.Fatalf("resolver without request = %q, %v, want godex", got, err)
	}
}

// TestRunWithOptionsRoutesToRegisteredHarness verifies a per-turn engine
// request (RunOptions.Harness) routes through the registered harness instead
// of the default godex loop.
func TestRunWithOptionsRoutesToRegisteredHarness(t *testing.T) {
	agent := &Agent{}
	other := &fakeHarness{id: "codex"}
	agent.RegisterHarness("codex", other)

	err := agent.RunWithOptions(context.Background(), RunOptions{
		SessionID: "s1",
		TurnID:    "t1",
		Harness:   "codex",
	})
	if err != nil {
		t.Fatalf("RunWithOptions routed turn: %v", err)
	}
	if other.runTurns != 1 {
		t.Fatalf("expected codex harness to run 1 turn, got %d", other.runTurns)
	}
}

// TestRunWithOptionsUnavailableHarnessErrors verifies a request for an engine
// that was never registered surfaces a clear error.
func TestRunWithOptionsUnavailableHarnessErrors(t *testing.T) {
	agent := &Agent{}
	err := agent.RunWithOptions(context.Background(), RunOptions{SessionID: "s1", Harness: "missing"})
	if err == nil {
		t.Fatalf("expected error for unavailable harness")
	}
	if !strings.Contains(err.Error(), "missing") {
		t.Fatalf("expected error to name harness, got %v", err)
	}
}

// TestRunWithOptionsSwitchingHarnessResetsSession verifies that moving a
// session from one engine to another resets both engines' session state.
func TestRunWithOptionsSwitchingHarnessResetsSession(t *testing.T) {
	agent := &Agent{}
	first := &fakeHarness{id: "codex"}
	second := &fakeHarness{id: "opencode"}
	agent.RegisterHarness("codex", first)
	agent.RegisterHarness("opencode", second)

	if err := agent.RunWithOptions(context.Background(), RunOptions{SessionID: "s1", Harness: "codex"}); err != nil {
		t.Fatalf("first engine turn: %v", err)
	}
	if err := agent.RunWithOptions(context.Background(), RunOptions{SessionID: "s1", Harness: "opencode"}); err != nil {
		t.Fatalf("second engine turn: %v", err)
	}
	if len(first.resetSessions) != 1 || first.resetSessions[0] != "s1" {
		t.Fatalf("expected old engine reset once, got %v", first.resetSessions)
	}
	if len(second.resetSessions) != 1 || second.resetSessions[0] != "s1" {
		t.Fatalf("expected new engine reset once, got %v", second.resetSessions)
	}

	// Same engine again: no reset.
	if err := agent.RunWithOptions(context.Background(), RunOptions{SessionID: "s1", Harness: "opencode"}); err != nil {
		t.Fatalf("same engine turn: %v", err)
	}
	if len(second.resetSessions) != 1 {
		t.Fatalf("expected no reset on same engine, got %v", second.resetSessions)
	}
}

// TestRunWithOptionsDefaultHarnessStaysOnGodex verifies that an empty or
// explicit godex request keeps the built-in engine (which here fails with the
// missing-caller error instead of touching any registered engine).
func TestRunWithOptionsDefaultHarnessStaysOnGodex(t *testing.T) {
	agent := newTestAgent(t, 4096)
	agent.client = nil // force the default loop to fail on missing caller
	other := &fakeHarness{id: "codex"}
	agent.RegisterHarness("codex", other)

	// A nil caller means the godex default loop fails with a missing-caller
	// error rather than routing to the registered engine.
	err := agent.RunWithOptions(context.Background(), RunOptions{SessionID: "s1", TurnID: "t1"})
	if err == nil || !strings.Contains(err.Error(), "missing caller") {
		t.Fatalf("expected missing-caller error from default loop, got %v", err)
	}
	if other.runTurns != 0 {
		t.Fatalf("expected default loop not to touch registered engine, got %d runs", other.runTurns)
	}
}

// TestRegisterHarnessAfterRouterBuiltIsDynamic verifies engines registered
// after the router is first built (e.g. after the first routed turn) remain
// available — the research-doc P2 fix that removes the sync.Once snapshot.
func TestRegisterHarnessAfterRouterBuiltIsDynamic(t *testing.T) {
	agent := newTestAgent(t, 4096)
	agent.client = nil // force the default loop to fail fast, not run a real turn

	// Force the router to be built before registering a new engine.
	if err := agent.RunWithOptions(context.Background(), RunOptions{SessionID: "s1", TurnID: "t1"}); err == nil {
		t.Fatal("expected missing-caller error from default loop")
	}

	late := &fakeHarness{id: "late"}
	agent.RegisterHarness("late", late)
	if err := agent.RunWithOptions(context.Background(), RunOptions{SessionID: "s2", TurnID: "t2", Harness: "late"}); err != nil {
		t.Fatalf("late-registered harness should route, got %v", err)
	}
	if late.runTurns != 1 {
		t.Fatalf("expected late harness to run 1 turn, got %d", late.runTurns)
	}

	// Replacing a late engine at runtime also works.
	replacement := &fakeHarness{id: "late"}
	agent.RegisterHarness("late", replacement)
	if err := agent.RunWithOptions(context.Background(), RunOptions{SessionID: "s3", TurnID: "t3", Harness: "late"}); err != nil {
		t.Fatalf("replaced harness should route, got %v", err)
	}
	if replacement.runTurns != 1 {
		t.Fatalf("expected replacement to run 1 turn, got %d", replacement.runTurns)
	}
}
