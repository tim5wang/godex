package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Round-trip: settings-page save of context_window_tokens must survive
// write-to-disk and reload (regression for template dropping the field).
func TestManagerPersistsCompactionWindowTokens(t *testing.T) {
	workspace := t.TempDir()
	manager := newTestManager(t, workspace)

	view, err := manager.Update(t.Context(), UpdateRequest{
		Values: map[string]any{
			"agent.compaction.context_window_tokens": 270000,
			"agent.compaction.trigger_ratio":         0.75,
		},
	})
	if err != nil {
		t.Fatalf("update compaction window tokens: %v", err)
	}
	if got, _ := view.StoredValues["agent.compaction.context_window_tokens"].(int); got != 270000 {
		t.Fatalf("expected stored context_window_tokens 270000, got %#v", view.StoredValues["agent.compaction.context_window_tokens"])
	}
	if got := manager.Current().Compaction.ContextWindowTokens; got != 270000 {
		t.Fatalf("expected effective context window 270000, got %d", got)
	}

	// Written yaml must contain the field.
	home := testHomeForWorkspace(workspace)
	raw, err := os.ReadFile(filepath.Join(home, "godex.yaml"))
	if err != nil {
		t.Fatalf("read written config: %v", err)
	}
	if !strings.Contains(string(raw), "context_window_tokens: 270000") {
		t.Fatalf("written yaml missing context_window_tokens, got:\n%s", raw)
	}
	if !strings.Contains(string(raw), "trigger_ratio: 0.75") {
		t.Fatalf("written yaml missing trigger_ratio, got:\n%s", raw)
	}

	// Reload from disk must preserve the value.
	reloaded, err := NewManager(Options{HomeDir: home, WorkspaceDir: workspace})
	if err != nil {
		t.Fatalf("reload manager: %v", err)
	}
	if got := reloaded.Current().Compaction.ContextWindowTokens; got != 270000 {
		t.Fatalf("expected reloaded context window 270000, got %d", got)
	}
	if got := reloaded.Current().Compaction.TriggerRatio; got != 0.75 {
		t.Fatalf("expected reloaded trigger ratio 0.75, got %v", got)
	}
}
