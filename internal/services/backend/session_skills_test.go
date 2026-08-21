package backend

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/protocol"
)

// writeTestSkill creates a minimal discoverable skill at
// <skillsDir>/<name>/SKILL.md so the loader treats it as installed.
func writeTestSkill(t *testing.T, skillsDir, name, body string) {
	t.Helper()
	dir := filepath.Join(skillsDir, name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir skill %s: %v", name, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0644); err != nil {
		t.Fatalf("write skill %s: %v", name, err)
	}
}

func newSkillsTestConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := newTestConfig(t)
	writeTestSkill(t, cfg.SkillsDir, "alpha", "# Alpha\n\nAlpha core instructions.\n")
	writeTestSkill(t, cfg.SkillsDir, "bravo", "# Bravo\n\nBravo core instructions.\n")
	writeTestSkill(t, cfg.SkillsDir, "charlie", "# Charlie\n\nCharlie core instructions.\n")
	return cfg
}

func activeSkillNames(t *testing.T, service *Service, sessionID string) []string {
	t.Helper()
	session, err := service.requireSession(sessionID)
	if err != nil {
		t.Fatalf("require session: %v", err)
	}
	return session.agent.ActiveSkillNames()
}

// TestRequestedSessionSkillsLoadedOnNewSession verifies that a new session
// opened with requested_skills metadata starts with exactly those installed
// skills instead of the global default set.
func TestRequestedSessionSkillsLoadedOnNewSession(t *testing.T) {
	cfg := newSkillsTestConfig(t)
	cfg.DefaultSkills = []string{"charlie"} // must NOT be loaded when requested skills are present
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("ok")}}}})

	opened, err := service.OpenSession(context.Background(), SessionLocator{
		Channel:  "web",
		Key:      "skills-pick",
		Metadata: map[string]string{sessionRequestedSkillsMetadataKey: "alpha, bravo"},
	})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	got := activeSkillNames(t, service, opened.SessionID)
	want := []string{"alpha", "bravo"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expected active skills %v, got %v", want, got)
	}
}

// TestRequestedSessionSkillsMissingTolerated verifies that requesting a skill
// that is not installed does not fail the session; it is simply skipped.
func TestRequestedSessionSkillsMissingTolerated(t *testing.T) {
	cfg := newSkillsTestConfig(t)
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("ok")}}}})

	opened, err := service.OpenSession(context.Background(), SessionLocator{
		Channel:  "web",
		Key:      "skills-missing",
		Metadata: map[string]string{sessionRequestedSkillsMetadataKey: "alpha, does-not-exist"},
	})
	if err != nil {
		t.Fatalf("open session with missing requested skill should not fail: %v", err)
	}
	got := activeSkillNames(t, service, opened.SessionID)
	want := []string{"alpha"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expected active skills %v, got %v", want, got)
	}
}

// TestDefaultSkillsFallbackWithoutRequestedSkills verifies that a new session
// with no requested skills falls back to the global team.default_skills.
func TestDefaultSkillsFallbackWithoutRequestedSkills(t *testing.T) {
	cfg := newSkillsTestConfig(t)
	cfg.DefaultSkills = []string{"bravo", "charlie"}
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("ok")}}}})

	opened, err := service.OpenSession(context.Background(), SessionLocator{Channel: "web", Key: "skills-default"})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	got := activeSkillNames(t, service, opened.SessionID)
	want := []string{"bravo", "charlie"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expected default active skills %v, got %v", want, got)
	}
}

// TestRequestedSkillsDoNotAffectSessionIdentity verifies requested_skills is
// NOT part of the session identity hash: the same locator with different skill
// selections resolves to the same session id, so resuming never forks a new
// session just because the skill preset differs.
func TestRequestedSkillsDoNotAffectSessionIdentity(t *testing.T) {
	cfg := newSkillsTestConfig(t)
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("ok")}}}})

	first, err := service.OpenSession(context.Background(), SessionLocator{
		Channel:  "web",
		Key:      "identity",
		Metadata: map[string]string{sessionRequestedSkillsMetadataKey: "alpha"},
	})
	if err != nil {
		t.Fatalf("open first session: %v", err)
	}
	second, err := service.OpenSession(context.Background(), SessionLocator{
		Channel:  "web",
		Key:      "identity",
		Metadata: map[string]string{sessionRequestedSkillsMetadataKey: "bravo"},
	})
	if err != nil {
		t.Fatalf("open second session: %v", err)
	}
	if first.SessionID != second.SessionID {
		t.Fatalf("requested_skills must not change session identity: %q != %q", first.SessionID, second.SessionID)
	}
}
