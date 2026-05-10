package evalharness

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/core/protocol"
	evaldomain "github.com/tim5wang/godex/internal/domain/eval"
	"github.com/tim5wang/godex/internal/domain/events"
	"github.com/tim5wang/godex/internal/domain/message"
	"github.com/tim5wang/godex/internal/services/backend"
)

type fakeEvalBackend struct {
	modelProfileID string
}

func (f *fakeEvalBackend) OpenSession(ctx context.Context, locator backend.SessionLocator) (*backend.OpenedSession, error) {
	_ = ctx
	return &backend.OpenedSession{SessionID: "session-" + locator.Key, Locator: locator}, nil
}

func (f *fakeEvalBackend) SetSessionModelProfile(ctx context.Context, sessionID, profileID string) (backend.ModelsView, error) {
	_ = ctx
	_ = sessionID
	f.modelProfileID = profileID
	return backend.ModelsView{SessionProfileID: profileID}, nil
}

func (f *fakeEvalBackend) Submit(ctx context.Context, sessionID string, envelope message.Envelope) (*backend.SubmitResult, error) {
	_ = ctx
	_ = envelope
	return &backend.SubmitResult{SessionID: sessionID, TurnID: "turn-1", Completed: true, Status: "completed"}, nil
}

func (f *fakeEvalBackend) Snapshot(ctx context.Context, sessionID string) (backend.Snapshot, error) {
	_ = ctx
	return backend.Snapshot{
		SessionID: sessionID,
		Messages: []protocol.Message{
			protocol.NewTextMessage(protocol.RoleAssistant, "the answer is ready"),
		},
	}, nil
}

func (f *fakeEvalBackend) Timeline(ctx context.Context, sessionID string, limit int) ([]events.Event, error) {
	_ = ctx
	_ = sessionID
	_ = limit
	return []events.Event{{
		Type: events.EventToolCallFinished,
		Payload: events.ToolCallPayload{
			Name: "desktop",
		},
	}}, nil
}

func TestLoadSuiteValidatesRequiredFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "godex.eval.yaml")
	if err := os.WriteFile(path, []byte("name: bad\ncases:\n  - id: one\n"), 0644); err != nil {
		t.Fatalf("write suite: %v", err)
	}
	if _, err := LoadSuite(path); err == nil {
		t.Fatal("expected missing prompt error")
	}
}

func TestLoadSuiteAllowsReplayFixtureWithoutPrompt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "godex.eval.yaml")
	if err := os.WriteFile(path, []byte("name: replay\ncases:\n  - id: one\n    replay_fixture: ./fixture\n"), 0644); err != nil {
		t.Fatalf("write suite: %v", err)
	}
	suite, err := LoadSuite(path)
	if err != nil {
		t.Fatalf("load suite: %v", err)
	}
	if suite.Cases[0].ReplayFixture != "./fixture" {
		t.Fatalf("expected replay fixture, got %+v", suite.Cases[0])
	}
}

func TestRunSuiteWritesReportAndScoresExpectations(t *testing.T) {
	dir := t.TempDir()
	suitePath := filepath.Join(dir, "godex.eval.yaml")
	if err := os.WriteFile(suitePath, []byte(`name: sample
cases:
  - id: one
    prompt: say ready
    model_profile_id: fast
    expected:
      required_substrings: ["answer"]
      forbidden_substrings: ["panic"]
      required_tools: ["desktop"]
      forbidden_tools: ["bash"]
      max_tool_failures: 0
`), 0644); err != nil {
		t.Fatalf("write suite: %v", err)
	}
	fake := &fakeEvalBackend{}
	service := &Service{
		Backend: fake,
		Now: func() time.Time {
			return time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
		},
	}
	report, err := service.RunSuite(context.Background(), RunOptions{SuitePath: suitePath, OutDir: filepath.Join(dir, "runs")})
	if err != nil {
		t.Fatalf("run suite: %v", err)
	}
	if !report.Passed || report.PassedCases != 1 || fake.modelProfileID != "fast" {
		t.Fatalf("unexpected report/model: %+v model=%q", report, fake.modelProfileID)
	}
	if _, err := os.Stat(filepath.Join(dir, "runs", report.RunID, "report.json")); err != nil {
		t.Fatalf("expected report artifact: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "runs", report.RunID, "one", "snapshot.json")); err != nil {
		t.Fatalf("expected snapshot artifact: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "runs", report.RunID, "one", "timeline.json")); err != nil {
		t.Fatalf("expected timeline artifact: %v", err)
	}
	loaded, err := ReadReport(filepath.Join(dir, "runs", report.RunID))
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	if loaded.RunID != report.RunID {
		t.Fatalf("unexpected loaded report: %+v", loaded)
	}
}

func TestRunReplaySuiteDetectsExpectedInstabilitySignals(t *testing.T) {
	dir := t.TempDir()
	service := &Service{
		Now: func() time.Time {
			return time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
		},
	}
	report, err := service.RunSuite(context.Background(), RunOptions{
		SuitePath: filepath.Join("testdata", "replay", "longtask-replay.yaml"),
		OutDir:    filepath.Join(dir, "runs"),
	})
	if err != nil {
		t.Fatalf("run replay suite: %v", err)
	}
	if !report.Passed || report.PassedCases != 1 {
		t.Fatalf("expected replay suite to pass expected-signal assertions, got %+v", report)
	}
	signals := report.Results[0].InstabilitySignals
	for _, want := range []string{"repeated_assistant_message", "repeated_tool_call", "empty_tool_exchange_recommendation"} {
		if !containsString(signals, want) {
			t.Fatalf("expected signal %s in %+v", want, signals)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "runs", report.RunID, "weixin-deploy-loop", "timeline.json")); err != nil {
		t.Fatalf("expected replay timeline artifact: %v", err)
	}
}

func TestRunReplaySuiteUsesEmbeddedTestdataWhenDiskFilesAreAbsent(t *testing.T) {
	dir := t.TempDir()
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("get wd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir temp: %v", err)
	}
	defer func() {
		if err := os.Chdir(oldwd); err != nil {
			t.Fatalf("restore wd: %v", err)
		}
	}()

	service := &Service{
		Now: func() time.Time {
			return time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
		},
	}
	report, err := service.RunSuite(context.Background(), RunOptions{
		SuitePath: filepath.Join("testdata", "replay", "longtask-replay.yaml"),
		OutDir:    filepath.Join(dir, "runs"),
	})
	if err != nil {
		t.Fatalf("run embedded replay suite: %v", err)
	}
	if !report.Passed || report.PassedCases != 1 {
		t.Fatalf("expected embedded replay suite to pass, got %+v", report)
	}
	if _, err := os.Stat(filepath.Join(dir, "runs", report.RunID, "weixin-deploy-loop", "timeline.json")); err != nil {
		t.Fatalf("expected embedded replay timeline artifact: %v", err)
	}
}

func TestExpectationFailuresAreReported(t *testing.T) {
	failures := evaluateExpectations(evaldomain.Expectation{
		RequiredSubstrings: []string{"missing"},
		ForbiddenTools:     []string{"desktop"},
	}, "hello", []evaldomain.ToolCall{{Name: "desktop", Status: "finished"}})
	if len(failures) != 2 {
		t.Fatalf("expected two failures, got %+v", failures)
	}
}

func TestStabilityExpectationsCatchLoopsAndIgnoreHealthyTimeline(t *testing.T) {
	loop := []events.Event{
		{Type: events.EventAssistantMessageComplete, Payload: events.TextPayload{Text: "repeat"}},
		{Type: events.EventAssistantMessageComplete, Payload: events.TextPayload{Text: "repeat"}},
		{Type: events.EventToolCallStarted, Payload: events.ToolCallPayload{Name: "tool_exchange", Input: map[string]interface{}{"query": "ssh deploy"}}},
		{Type: events.EventToolCallFinished, Payload: events.ToolCallPayload{Name: "tool_exchange", Input: map[string]interface{}{"query": "ssh deploy"}, Output: `{"recommended_bundles":[]}`}},
	}
	maxOne := 1
	zero := 0
	failures := evaluateStabilityExpectations(evaldomain.Expectation{
		MaxRepeatedAssistantMessages:        &maxOne,
		MaxEmptyToolExchangeRecommendations: &zero,
		ForbiddenToolExchangeQueries:        []string{"ssh deploy"},
	}, analyzeTimelineStability(loop))
	if len(failures) != 3 {
		t.Fatalf("expected three stability failures, got %+v", failures)
	}

	healthy := []events.Event{
		{Type: events.EventAssistantMessageComplete, Payload: events.TextPayload{Text: "first"}},
		{Type: events.EventAssistantMessageComplete, Payload: events.TextPayload{Text: "second"}},
		{Type: events.EventToolCallStarted, Payload: events.ToolCallPayload{Name: "read_file", Input: map[string]interface{}{"path": "README.md"}}},
	}
	stability := analyzeTimelineStability(healthy)
	if len(stability.Signals) != 0 || strings.Contains(strings.Join(stability.Signals, ","), "repeated") {
		t.Fatalf("expected healthy timeline to avoid instability signals, got %+v", stability)
	}
}
