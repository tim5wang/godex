package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDefaultScopeWriteGuardEnabled verifies the 6.2 M4 write-path guard
// (tools.execution.scope_write) defaults to enabled.
func TestDefaultScopeWriteGuardEnabled(t *testing.T) {
	manager := newTestManager(t, t.TempDir())
	cfg := manager.Current()
	if !cfg.Tools.Execution.ScopeWrite {
		t.Fatalf("expected scope_write to default to true, got %#v", cfg.Tools.Execution)
	}
}

// TestScopeWriteGuardDisabledViaYAML verifies godex.yaml can opt out of the
// write-path guard (tools.execution.scope_write: false).
func TestScopeWriteGuardDisabledViaYAML(t *testing.T) {
	workspace := t.TempDir()
	yamlBody := "tools:\n  execution:\n    scope_write: false\n"
	if err := os.WriteFile(filepath.Join(workspace, "godex.yaml"), []byte(yamlBody), 0644); err != nil {
		t.Fatalf("write godex.yaml: %v", err)
	}
	manager, err := NewManager(Options{WorkspaceDir: workspace, HomeDir: testHomeForWorkspace(workspace)})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	cfg := manager.Current()
	if cfg.Tools.Execution.ScopeWrite {
		t.Fatalf("expected scope_write to be false from yaml, got %#v", cfg.Tools.Execution)
	}
}

// TestScopeWriteSchemaField verifies the settings schema exposes the
// scope_write field under tools.execution.
func TestScopeWriteSchemaField(t *testing.T) {
	manager := newTestManager(t, t.TempDir())
	var found bool
	for _, section := range manager.Schema() {
		if section.ID != "tools-execution" {
			continue
		}
		for _, field := range section.Fields {
			if field.Path == "tools.execution.scope_write" {
				found = true
				break
			}
		}
	}
	if !found {
		t.Fatal("expected tools.execution.scope_write field in tools-execution schema section")
	}
}
