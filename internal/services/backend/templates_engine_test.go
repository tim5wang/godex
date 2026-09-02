package backend

import (
	"context"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/core/templates"
	"github.com/tim5wang/godex/internal/domain/message"
)

// TestAgentTemplateFormOptionsIncludesRegisteredEngines verifies the template
// editor's engine dropdown mirrors the real runtime harness registry: godex
// plus one entry per configured ACP agent.
func TestAgentTemplateFormOptionsIncludesRegisteredEngines(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.ACP = config.ACPConfig{Agents: map[string]config.ACPAgentConfig{
		"codex": {ID: "codex", Command: "codex"},
	}}
	service := newTestService(cfg, &stubCaller{})
	opts := service.AgentTemplateFormOptions()
	if opts == nil {
		t.Fatal("expected non-nil form options")
	}
	if !containsStr(opts.Engines, "godex") {
		t.Fatalf("expected engines to include godex, got %v", opts.Engines)
	}
	if !containsStr(opts.Engines, "acp:codex") {
		t.Fatalf("expected engines to include acp:codex, got %v", opts.Engines)
	}
}

// TestSessionTemplateEngineAppliedAtOpen verifies the full OpenSession →
// ApplyTemplate chain pins the session's engine to the template's value, so
// subsequent turns (turn.go) fall back to it when no per-turn harness is
// requested.
func TestSessionTemplateEngineAppliedAtOpen(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.ACP = config.ACPConfig{Agents: map[string]config.ACPAgentConfig{
		"codex": {ID: "codex", Command: "codex"},
	}}
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("ok")}}}})

	// Persist a template that pins the external kernel.
	if err := service.SaveAgentTemplate(templates.AgentTemplate{
		ID:     "ext-codex",
		Name:   "External Codex",
		Engine: "acp:codex",
	}); err != nil {
		t.Fatalf("save template: %v", err)
	}

	opened, err := service.OpenSession(context.Background(), SessionLocator{
		Channel:  "web",
		Key:      "engine-test",
		Metadata: map[string]string{"template": "ext-codex"},
	})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}

	service.mu.Lock()
	state := service.sessions[opened.SessionID]
	service.mu.Unlock()
	if state == nil {
		t.Fatal("expected session state to exist")
	}
	if got := state.agent.TemplateEngine(); got != "acp:codex" {
		t.Fatalf("session TemplateEngine = %q, want acp:codex", got)
	}
}

// TestSessionUnknownEngineFallsBackToGodexAndTurnWorks verifies an unknown
// engine id never rejects session creation and never blocks a turn: it falls
// back to the godex engine, so a submitted turn completes through the stub
// model caller as before.
func TestSessionUnknownEngineFallsBackToGodexAndTurnWorks(t *testing.T) {
	cfg := newTestConfig(t)
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("ok")}}}})

	if err := service.SaveAgentTemplate(templates.AgentTemplate{
		ID:     "ext-missing",
		Name:   "Missing Kernel",
		Engine: "acp:not-registered",
	}); err != nil {
		t.Fatalf("save template: %v", err)
	}

	opened, err := service.OpenSession(context.Background(), SessionLocator{
		Channel:  "web",
		Key:      "engine-fallback",
		Metadata: map[string]string{"template": "ext-missing"},
	})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}

	service.mu.Lock()
	state := service.sessions[opened.SessionID]
	service.mu.Unlock()
	if state == nil {
		t.Fatal("expected session state to exist")
	}
	if got := state.agent.TemplateEngine(); got != templates.EngineDefault {
		t.Fatalf("session TemplateEngine = %q, want %q (fallback)", got, templates.EngineDefault)
	}

	if _, err := service.Submit(context.Background(), opened.SessionID, message.NewTextEnvelope(message.SourceWeb, opened.SessionID, cfg.LeadName, "hello", time.Now())); err != nil {
		t.Fatalf("submit after unknown-engine fallback: %v", err)
	}
	// Wait for the turn to fully finish (async background writes) so the
	// test's TempDir cleanup does not race in-flight file writes.
	waitForBackendSnapshot(t, service, opened.SessionID, func(snapshot Snapshot) bool {
		return !snapshot.Running
	})
}

func containsStr(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
