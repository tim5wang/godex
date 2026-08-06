package noderegistry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureNodeIDUsesPreferredWhenNoStateFile(t *testing.T) {
	stateDir := t.TempDir()
	id, err := EnsureNodeID(stateDir, "my-laptop")
	if err != nil {
		t.Fatalf("ensure node id: %v", err)
	}
	if id != "my-laptop" {
		t.Fatalf("expected preferred id %q, got %q", "my-laptop", id)
	}
	data, err := os.ReadFile(filepath.Join(stateDir, "node.json"))
	if err != nil {
		t.Fatalf("read node.json: %v", err)
	}
	if !strings.Contains(string(data), "my-laptop") {
		t.Fatalf("expected preferred id persisted to node.json, got:\n%s", data)
	}
}

func TestEnsureNodeIDTrimsPreferred(t *testing.T) {
	stateDir := t.TempDir()
	id, err := EnsureNodeID(stateDir, "  my-laptop  ")
	if err != nil {
		t.Fatalf("ensure node id: %v", err)
	}
	if id != "my-laptop" {
		t.Fatalf("expected trimmed id %q, got %q", "my-laptop", id)
	}
}

// TestEnsureNodeIDExplicitPreferredOverridesExisting: a user-configured
// node_id (join --id) is the source of truth; it must win over an earlier
// auto-generated or stale id in node.json, and node.json is updated to match.
func TestEnsureNodeIDExplicitPreferredOverridesExisting(t *testing.T) {
	stateDir := t.TempDir()
	first, err := EnsureNodeID(stateDir, "")
	if err != nil {
		t.Fatalf("ensure node id: %v", err)
	}
	if !strings.HasPrefix(first, "node_") {
		t.Fatalf("expected auto-generated id with node_ prefix, got %q", first)
	}

	again, err := EnsureNodeID(stateDir, "my-laptop")
	if err != nil {
		t.Fatalf("ensure node id again: %v", err)
	}
	if again != "my-laptop" {
		t.Fatalf("expected explicit preferred id %q to win over existing %q, got %q", "my-laptop", first, again)
	}
	data, err := os.ReadFile(filepath.Join(stateDir, "node.json"))
	if err != nil {
		t.Fatalf("read node.json: %v", err)
	}
	if !strings.Contains(string(data), "my-laptop") {
		t.Fatalf("expected node.json updated to explicit id, got:\n%s", data)
	}
}

func TestEnsureNodeIDGeneratesRandomWhenNoPreferred(t *testing.T) {
	stateDir := t.TempDir()
	id, err := EnsureNodeID(stateDir, "")
	if err != nil {
		t.Fatalf("ensure node id: %v", err)
	}
	if !strings.HasPrefix(id, "node_") {
		t.Fatalf("expected node_ prefixed random id, got %q", id)
	}
	if len(id) != len("node_")+16 {
		t.Fatalf("expected 16 hex chars after prefix, got %q", id)
	}
}

// TestEnsureNodeIDKeepsExistingWhenNoPreferred: without an explicit preferred
// id, an existing node.json id is kept (idempotent for auto-generated ids).
func TestEnsureNodeIDKeepsExistingWhenNoPreferred(t *testing.T) {
	stateDir := t.TempDir()
	first, err := EnsureNodeID(stateDir, "")
	if err != nil {
		t.Fatalf("ensure node id: %v", err)
	}
	again, err := EnsureNodeID(stateDir, "")
	if err != nil {
		t.Fatalf("ensure node id again: %v", err)
	}
	if again != first {
		t.Fatalf("expected existing id %q kept without preferred, got %q", first, again)
	}
}

// TestEnsureNodeIDLatestPreferredWinsAcrossCalls: when the operator re-runs
// join with a new id, the newest explicit id wins (re-onboarding).
func TestEnsureNodeIDLatestPreferredWinsAcrossCalls(t *testing.T) {
	stateDir := t.TempDir()
	first, err := EnsureNodeID(stateDir, "node-a")
	if err != nil {
		t.Fatalf("ensure node id: %v", err)
	}
	if first != "node-a" {
		t.Fatalf("expected preferred id on first call, got %q", first)
	}
	second, err := EnsureNodeID(stateDir, "node-b")
	if err != nil {
		t.Fatalf("ensure node id second call: %v", err)
	}
	if second != "node-b" {
		t.Fatalf("expected newest explicit preferred id %q to win, got %q", "node-b", second)
	}
}
