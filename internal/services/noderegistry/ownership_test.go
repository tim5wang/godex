package noderegistry

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestNodeInputDoesNotAcceptOwnership reproduces the §1.2 finding: the
// register/heartbeat DTO must not be able to claim an identity. If TenantID
// ever reappears on NodeInput, a node could self-assign into another tenant.
func TestNodeInputDoesNotAcceptOwnership(t *testing.T) {
	body := []byte(`{"id":"node-a","tenant_id":"tenant-evil","owner_user_id":"admin","visibility":"tenant"}`)
	var input NodeInput
	if err := json.Unmarshal(body, &input); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, forbidden := range []string{"tenant_id", "owner_user_id", "visibility"} {
		if containsKey(encoded, forbidden) {
			t.Errorf("NodeInput must not carry %q; a node would be able to self-report its identity", forbidden)
		}
	}
}

func containsKey(encoded []byte, key string) bool {
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return false
	}
	_, ok := decoded[key]
	return ok
}

func TestRegistryOwnershipDefaultsToPrivate(t *testing.T) {
	registry, err := New(filepath.Join(t.TempDir(), "nodes.json"), time.Minute)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	if _, err := registry.Register(context.Background(), NodeInput{ID: "node-a", Name: "local"}); err != nil {
		t.Fatalf("register: %v", err)
	}
	node, err := registry.Get(context.Background(), "node-a")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if node.Visibility != "private" {
		t.Errorf("Visibility = %q, want private (decision ⑥)", node.Visibility)
	}
	if node.TenantID != "" || node.OwnerUserID != "" {
		t.Errorf("a self-registered node must stay unclaimed, got tenant=%q owner=%q", node.TenantID, node.OwnerUserID)
	}
}

func TestRegistrySetOwnershipPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodes.json")
	registry, err := New(path, time.Minute)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	if _, err := registry.Register(context.Background(), NodeInput{ID: "node-a"}); err != nil {
		t.Fatalf("register: %v", err)
	}
	got, err := registry.SetOwnership(context.Background(), "node-a", "tenant-1", "user-1", "tenant")
	if err != nil {
		t.Fatalf("set ownership: %v", err)
	}
	if got.TenantID != "tenant-1" || got.OwnerUserID != "user-1" || got.Visibility != "tenant" {
		t.Fatalf("ownership not applied: %#v", got)
	}

	// The claim must survive a restart, otherwise every node would fall back to
	// private/unclaimed and become unreachable after a service reload.
	reloaded, err := New(path, time.Minute)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	node, err := reloaded.Get(context.Background(), "node-a")
	if err != nil {
		t.Fatalf("get reloaded: %v", err)
	}
	if node.TenantID != "tenant-1" || node.OwnerUserID != "user-1" || node.Visibility != "tenant" {
		t.Fatalf("ownership not persisted: %#v", node)
	}
}

func TestRegistrySetOwnershipUnknownNode(t *testing.T) {
	registry, err := New(filepath.Join(t.TempDir(), "nodes.json"), time.Minute)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	if _, err := registry.SetOwnership(context.Background(), "ghost", "t", "u", ""); err == nil {
		t.Fatal("expected error assigning ownership to an unknown node")
	}
}

// TestRegistryRegisterCannotOverwriteOwnership pins the point of keeping the
// fields off NodeInput: a later register/heartbeat must not clobber a claim.
func TestRegistryRegisterCannotOverwriteOwnership(t *testing.T) {
	registry, err := New(filepath.Join(t.TempDir(), "nodes.json"), time.Minute)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	if _, err := registry.Register(context.Background(), NodeInput{ID: "node-a"}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, err := registry.SetOwnership(context.Background(), "node-a", "tenant-1", "user-1", "private"); err != nil {
		t.Fatalf("set ownership: %v", err)
	}
	if _, err := registry.Register(context.Background(), NodeInput{ID: "node-a", Name: "again"}); err != nil {
		t.Fatalf("re-register: %v", err)
	}
	node, err := registry.Get(context.Background(), "node-a")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if node.TenantID != "tenant-1" || node.OwnerUserID != "user-1" {
		t.Fatalf("re-register clobbered ownership: %#v", node)
	}
}

func TestRegistryUnknownVisibilityFallsBackToPrivate(t *testing.T) {
	registry, err := New(filepath.Join(t.TempDir(), "nodes.json"), time.Minute)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	if _, err := registry.Register(context.Background(), NodeInput{ID: "node-a"}); err != nil {
		t.Fatalf("register: %v", err)
	}
	got, err := registry.SetOwnership(context.Background(), "node-a", "t", "u", "garbage")
	if err != nil {
		t.Fatalf("set ownership: %v", err)
	}
	if got.Visibility != "private" {
		t.Errorf("Visibility = %q, want private for unknown input", got.Visibility)
	}
}

// TestRegistryLoadLegacyRecordDefaultsVisibility covers an existing nodes.json
// written before ownership existed: it must load as private, not blank.
func TestRegistryLoadLegacyRecordDefaultsVisibility(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodes.json")
	legacy := `{"nodes":[{"id":"node-old","name":"old","status":"online"}]}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatalf("write legacy registry: %v", err)
	}
	registry, err := New(path, time.Minute)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	node, err := registry.Get(context.Background(), "node-old")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if node.Visibility != "private" {
		t.Errorf("legacy node Visibility = %q, want private", node.Visibility)
	}
	if ref := node.NodeRef(); ref.Visibility != "private" {
		t.Errorf("NodeRef().Visibility = %q, want private", ref.Visibility)
	}
}

func TestNodeViewNodeRefProjectsOwnership(t *testing.T) {
	view := NodeView{ID: "node-a", TenantID: "tenant-1", OwnerUserID: "user-1", Visibility: "tenant"}
	ref := view.NodeRef()
	if ref.ID != "node-a" || ref.TenantID != "tenant-1" || ref.OwnerUserID != "user-1" || ref.Visibility != "tenant" {
		t.Fatalf("NodeRef() = %#v", ref)
	}
	refs := NodeRefs([]NodeView{view})
	if len(refs) != 1 || refs[0].ID != "node-a" {
		t.Fatalf("NodeRefs() = %#v", refs)
	}
	if got := NodeRefs(nil); len(got) != 0 {
		t.Errorf("NodeRefs(nil) = %#v, want empty", got)
	}
}
