package noderegistry

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newDeleteTestRegistry(t *testing.T) *Registry {
	t.Helper()
	path := filepath.Join(t.TempDir(), "nodes.json")
	registry, err := New(path, 60*time.Second)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	return registry
}

func mustRegister(t *testing.T, r *Registry, id string) {
	t.Helper()
	if _, err := r.Register(context.Background(), NodeInput{ID: id, Name: "n-" + id}); err != nil {
		t.Fatalf("register %s: %v", id, err)
	}
}

func TestRegistryDeleteRemovesNode(t *testing.T) {
	r := newDeleteTestRegistry(t)
	mustRegister(t, r, "node-a")
	mustRegister(t, r, "node-b")

	got, err := r.Delete(context.Background(), "node-a")
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if got.ID != "node-a" {
		t.Fatalf("expected deleted node id node-a, got %q", got.ID)
	}
	if _, err := r.Get(context.Background(), "node-a"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected ErrNotExist after delete, got %v", err)
	}
	list, err := r.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].ID != "node-b" {
		t.Fatalf("expected only node-b to remain, got %#v", list)
	}
}

func TestRegistryDeleteUnknownNode(t *testing.T) {
	r := newDeleteTestRegistry(t)
	if _, err := r.Delete(context.Background(), "ghost"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected ErrNotExist for unknown node, got %v", err)
	}
}

// TestRegistryDeleteBlocksHeartbeatRevival: a deleted node must not come back
// through heartbeat (the heartbeat endpoint is upsert semantics otherwise).
func TestRegistryDeleteBlocksHeartbeatRevival(t *testing.T) {
	r := newDeleteTestRegistry(t)
	mustRegister(t, r, "node-a")
	if _, err := r.Delete(context.Background(), "node-a"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	if _, err := r.Heartbeat(context.Background(), "node-a", NodeInput{Version: "dev"}); err == nil {
		t.Fatal("expected heartbeat of deleted node to be rejected")
	}
	list, err := r.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("expected deleted node to stay gone, got %#v", list)
	}
}

// TestRegistryDeleteAllowsReRegister: re-registering the same id clears the
// tombstone so a genuinely re-onboarded node can join again.
func TestRegistryDeleteAllowsReRegister(t *testing.T) {
	r := newDeleteTestRegistry(t)
	mustRegister(t, r, "node-a")
	if _, err := r.Delete(context.Background(), "node-a"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := r.Heartbeat(context.Background(), "node-a", NodeInput{}); err == nil {
		t.Fatal("expected heartbeat rejection while tombstoned")
	}

	mustRegister(t, r, "node-a")
	node, err := r.Heartbeat(context.Background(), "node-a", NodeInput{Version: "dev"})
	if err != nil {
		t.Fatalf("heartbeat after re-register: %v", err)
	}
	if node.Status != StatusOnline {
		t.Fatalf("expected online after re-register heartbeat, got %#v", node)
	}
}

// TestRegistryDeletePersistsTombstone: the tombstone must survive a registry
// reload (center restart) so a deleted node cannot revive itself afterwards.
func TestRegistryDeletePersistsTombstone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodes.json")
	r, err := New(path, 60*time.Second)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	mustRegister(t, r, "node-a")
	if _, err := r.Delete(context.Background(), "node-a"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	reloaded, err := New(path, 60*time.Second)
	if err != nil {
		t.Fatalf("reload registry: %v", err)
	}
	if _, err := reloaded.Heartbeat(context.Background(), "node-a", NodeInput{Version: "dev"}); err == nil {
		t.Fatal("expected tombstone to survive reload and reject heartbeat")
	}
	if _, err := reloaded.Get(context.Background(), "node-a"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected node-a to stay deleted after reload, got %v", err)
	}
}
