package noderegistry

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// newProxyTokenRegistry returns a registry with two registered nodes, so a
// lookup test can prove it resolves the RIGHT node rather than merely finding
// one.
func newProxyTokenRegistry(t *testing.T) (*Registry, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "nodes.json")
	registry, err := New(path, time.Minute)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	for _, in := range []NodeInput{
		{ID: "node-a", Name: "a"},
		{ID: "node-b", Name: "b"},
	} {
		if _, err := registry.Register(context.Background(), in); err != nil {
			t.Fatalf("register %s: %v", in.ID, err)
		}
	}
	return registry, path
}

func TestRegistrySetProxyTokenHashPersists(t *testing.T) {
	registry, path := newProxyTokenRegistry(t)

	if err := registry.SetProxyTokenHash(context.Background(), "node-a", "hash-a"); err != nil {
		t.Fatalf("set proxy token hash: %v", err)
	}

	node, err := registry.Get(context.Background(), "node-a")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if node.ProxyTokenHash != "hash-a" {
		t.Fatalf("expected stored proxy token hash, got %#v", node)
	}

	reloaded, err := New(path, time.Minute)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	node, err = reloaded.Get(context.Background(), "node-a")
	if err != nil {
		t.Fatalf("get reloaded: %v", err)
	}
	if node.ProxyTokenHash != "hash-a" {
		t.Fatalf("expected persisted proxy token hash, got %#v", node)
	}
}

func TestRegistrySetProxyTokenHashUnknownNode(t *testing.T) {
	registry, _ := newProxyTokenRegistry(t)
	if err := registry.SetProxyTokenHash(context.Background(), "ghost", "abc"); err == nil {
		t.Fatal("expected error setting hash for unknown node")
	}
}

// An empty digest must never be stored: it would make the node matchable by an
// empty credential, which is the opposite of what a credential is for.
func TestRegistrySetProxyTokenHashRejectsEmpty(t *testing.T) {
	registry, _ := newProxyTokenRegistry(t)
	if err := registry.SetProxyTokenHash(context.Background(), "node-a", "   "); err == nil {
		t.Fatal("expected error for an empty proxy token hash")
	}
}

// TestRegistryNodeByProxyTokenHashResolvesNodeAndTenant is the property the
// whole per-node migration exists for: a presented digest must name its node
// (and therefore that node's tenant) instead of resolving to "some node".
func TestRegistryNodeByProxyTokenHashResolvesNodeAndTenant(t *testing.T) {
	registry, _ := newProxyTokenRegistry(t)
	if _, err := registry.SetOwnership(context.Background(), "node-a", "tenant-1", "user-a", "private"); err != nil {
		t.Fatalf("set ownership: %v", err)
	}
	if err := registry.SetProxyTokenHash(context.Background(), "node-a", "hash-a"); err != nil {
		t.Fatalf("set hash a: %v", err)
	}
	if err := registry.SetProxyTokenHash(context.Background(), "node-b", "hash-b"); err != nil {
		t.Fatalf("set hash b: %v", err)
	}

	node, ok := registry.NodeByProxyTokenHash("hash-a")
	if !ok {
		t.Fatal("hash-a should resolve to a node")
	}
	if node.ID != "node-a" {
		t.Errorf("resolved id = %q, want node-a", node.ID)
	}
	if node.TenantID != "tenant-1" {
		t.Errorf("resolved tenant = %q, want tenant-1 (this is what fixes cross-tenant node access)", node.TenantID)
	}

	other, ok := registry.NodeByProxyTokenHash("hash-b")
	if !ok || other.ID != "node-b" {
		t.Fatalf("hash-b resolved to %#v ok=%v, want node-b", other, ok)
	}
}

func TestRegistryNodeByProxyTokenHashRejectsUnknownAndEmpty(t *testing.T) {
	registry, _ := newProxyTokenRegistry(t)
	if err := registry.SetProxyTokenHash(context.Background(), "node-a", "hash-a"); err != nil {
		t.Fatalf("set hash: %v", err)
	}
	if _, ok := registry.NodeByProxyTokenHash("nope"); ok {
		t.Error("an unknown digest must not resolve")
	}
	// An empty digest must not match a node that has no per-node token yet.
	if _, ok := registry.NodeByProxyTokenHash(""); ok {
		t.Error("an empty digest must not resolve")
	}
}

// TestRegistryRotateProxyTokenInvalidatesOnlyThatNode is the revocation property:
// after a node rotates, its old digest is dead while its peers are untouched.
func TestRegistryRotateProxyTokenInvalidatesOnlyThatNode(t *testing.T) {
	registry, _ := newProxyTokenRegistry(t)
	if err := registry.SetProxyTokenHash(context.Background(), "node-a", "old-a"); err != nil {
		t.Fatalf("set old hash: %v", err)
	}
	if err := registry.SetProxyTokenHash(context.Background(), "node-b", "hash-b"); err != nil {
		t.Fatalf("set b hash: %v", err)
	}

	if err := registry.SetProxyTokenHash(context.Background(), "node-a", "new-a"); err != nil {
		t.Fatalf("rotate: %v", err)
	}

	if _, ok := registry.NodeByProxyTokenHash("old-a"); ok {
		t.Error("the pre-rotation digest must stop resolving")
	}
	if node, ok := registry.NodeByProxyTokenHash("new-a"); !ok || node.ID != "node-a" {
		t.Errorf("the new digest should resolve to node-a, got %#v ok=%v", node, ok)
	}
	if node, ok := registry.NodeByProxyTokenHash("hash-b"); !ok || node.ID != "node-b" {
		t.Errorf("rotating node-a must not disturb node-b, got %#v ok=%v", node, ok)
	}
}

// TestRegistryNodeByCredentialHashResolvesNode backs the node-authenticated
// rotation endpoint: the ck_ digest identifies the caller so the request body
// never carries a node id the node could lie about.
func TestRegistryNodeByCredentialHashResolvesNode(t *testing.T) {
	registry, _ := newProxyTokenRegistry(t)
	if err := registry.SetCredentialHash(context.Background(), "node-a", "ck-hash-a"); err != nil {
		t.Fatalf("set credential hash: %v", err)
	}

	node, ok := registry.NodeByCredentialHash("ck-hash-a")
	if !ok || node.ID != "node-a" {
		t.Fatalf("credential hash resolved to %#v ok=%v, want node-a", node, ok)
	}
	if _, ok := registry.NodeByCredentialHash(""); ok {
		t.Error("an empty digest must not resolve")
	}
	if _, ok := registry.NodeByCredentialHash("nope"); ok {
		t.Error("an unknown digest must not resolve")
	}
}
