package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestControlNodeIDFromYAML(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "godex.yaml"), []byte(strings.Join([]string{
		"control:",
		"  node_id: my-laptop",
		"  node_name: dev box",
	}, "\n")), 0600); err != nil {
		t.Fatalf("write godex.yaml: %v", err)
	}

	manager := newTestManager(t, workspace)
	cfg := manager.Current()
	if cfg.Control.NodeID != "my-laptop" {
		t.Fatalf("expected control.node_id %q, got %q", "my-laptop", cfg.Control.NodeID)
	}
	if cfg.Control.NodeName != "dev box" {
		t.Fatalf("expected control.node_name %q, got %q", "dev box", cfg.Control.NodeName)
	}
}

func TestControlNodeIDFromEnvOverridesYAML(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "godex.yaml"), []byte(strings.Join([]string{
		"control:",
		"  node_id: from-yaml",
	}, "\n")), 0600); err != nil {
		t.Fatalf("write godex.yaml: %v", err)
	}
	t.Setenv("GODEX_CONTROL_NODE_ID", "from-env")

	manager := newTestManager(t, workspace)
	if got := manager.Current().Control.NodeID; got != "from-env" {
		t.Fatalf("expected env to override yaml, got %q", got)
	}
}

func TestControlDefaultNodeFromYAMLAndEnv(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "godex.yaml"), []byte(strings.Join([]string{
		"control:",
		"  default_node: yaml-node",
	}, "\n")), 0600); err != nil {
		t.Fatalf("write godex.yaml: %v", err)
	}

	manager := newTestManager(t, workspace)
	if got := manager.Current().Control.DefaultNode; got != "yaml-node" {
		t.Fatalf("expected control.default_node %q, got %q", "yaml-node", got)
	}

	t.Setenv("GODEX_CONTROL_DEFAULT_NODE", "env-node")
	manager2 := newTestManager(t, workspace)
	if got := manager2.Current().Control.DefaultNode; got != "env-node" {
		t.Fatalf("expected env default_node %q, got %q", "env-node", got)
	}
}

func TestManagerUpdateControlNodeIdentity(t *testing.T) {
	workspace := t.TempDir()
	manager := newTestManager(t, workspace)

	view, err := manager.Update(t.Context(), UpdateRequest{Values: map[string]any{
		"control.node_id":      "update-node",
		"control.default_node": "update-default",
	}})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if got := manager.Current().Control.NodeID; got != "update-node" {
		t.Fatalf("expected node_id %q after update, got %q", "update-node", got)
	}
	if got := manager.Current().Control.DefaultNode; got != "update-default" {
		t.Fatalf("expected default_node %q after update, got %q", "update-default", got)
	}
	if view.Fields["control.node_id"].Source != SourceYAML {
		t.Fatalf("expected source yaml after update, got %q", view.Fields["control.node_id"].Source)
	}

	data, err := os.ReadFile(filepath.Join(testHomeForWorkspace(workspace), "godex.yaml"))
	if err != nil {
		t.Fatalf("read godex.yaml: %v", err)
	}
	if !strings.Contains(string(data), "node_id: update-node") {
		t.Fatalf("expected node_id written to godex.yaml, got:\n%s", data)
	}
	if !strings.Contains(string(data), "default_node: update-default") {
		t.Fatalf("expected default_node written to godex.yaml, got:\n%s", data)
	}
}

func TestManagerSchemaIncludesNodeIdentityFields(t *testing.T) {
	manager := newTestManager(t, t.TempDir())

	var control *SectionSchema
	for i := range manager.Schema() {
		if manager.Schema()[i].ID == "control" {
			control = &manager.Schema()[i]
			break
		}
	}
	if control == nil {
		t.Fatal("expected control schema section")
	}

	paths := map[string]bool{}
	for _, f := range control.Fields {
		paths[f.Path] = true
	}
	for _, want := range []string{"control.node_id", "control.default_node"} {
		if !paths[want] {
			t.Fatalf("expected schema field %q in control section", want)
		}
	}
}
