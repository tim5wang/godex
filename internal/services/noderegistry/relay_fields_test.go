package noderegistry

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestRegistrySetCredentialHashPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodes.json")
	registry, err := New(path, time.Minute)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	if _, err := registry.Register(context.Background(), NodeInput{ID: "node-a", Name: "local"}); err != nil {
		t.Fatalf("register: %v", err)
	}

	if err := registry.SetCredentialHash(context.Background(), "node-a", "deadbeef"); err != nil {
		t.Fatalf("set credential hash: %v", err)
	}

	node, err := registry.Get(context.Background(), "node-a")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if node.CredentialHash != "deadbeef" {
		t.Fatalf("expected stored credential hash, got %#v", node)
	}

	reloaded, err := New(path, time.Minute)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	node, err = reloaded.Get(context.Background(), "node-a")
	if err != nil {
		t.Fatalf("get reloaded: %v", err)
	}
	if node.CredentialHash != "deadbeef" {
		t.Fatalf("expected persisted credential hash, got %#v", node)
	}
}

func TestRegistrySetCredentialHashUnknownNode(t *testing.T) {
	registry, err := New(filepath.Join(t.TempDir(), "nodes.json"), time.Minute)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	if err := registry.SetCredentialHash(context.Background(), "ghost", "abc"); err == nil {
		t.Fatal("expected error setting hash for unknown node")
	}
}

func TestRegistryRelayStatusAndHealth(t *testing.T) {
	registry, err := New(filepath.Join(t.TempDir(), "nodes.json"), time.Minute)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	now := time.Date(2026, 5, 3, 10, 0, 0, 0, time.UTC)
	registry.SetNow(func() time.Time { return now })
	if _, err := registry.Register(context.Background(), NodeInput{ID: "node-a", Name: "local"}); err != nil {
		t.Fatalf("register: %v", err)
	}

	if err := registry.SetRelayStatus(context.Background(), "node-a", "connected"); err != nil {
		t.Fatalf("set relay status: %v", err)
	}
	node, err := registry.Get(context.Background(), "node-a")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if node.RelayStatus != "connected" {
		t.Fatalf("expected relay status connected, got %q", node.RelayStatus)
	}
	if !node.LastHealth.Equal(now) {
		t.Fatalf("expected last health at now, got %v", node.LastHealth)
	}

	registry.SetNow(func() time.Time { return now.Add(time.Minute) })
	if err := registry.SetRelayStatus(context.Background(), "node-a", "disconnected"); err != nil {
		t.Fatalf("set relay status: %v", err)
	}
	node, _ = registry.Get(context.Background(), "node-a")
	if node.RelayStatus != "disconnected" {
		t.Fatalf("expected relay status disconnected, got %q", node.RelayStatus)
	}
	if !node.LastHealth.Equal(now.Add(time.Minute)) {
		t.Fatalf("expected last health updated, got %v", node.LastHealth)
	}
}

func TestRegistryTrustLevelRoundTrip(t *testing.T) {
	registry, err := New(filepath.Join(t.TempDir(), "nodes.json"), time.Minute)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	if _, err := registry.Register(context.Background(), NodeInput{ID: "node-a", Name: "local", TrustLevel: "guarded-remote"}); err != nil {
		t.Fatalf("register: %v", err)
	}
	node, err := registry.Get(context.Background(), "node-a")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if node.TrustLevel != "guarded-remote" {
		t.Fatalf("expected trust level guarded-remote, got %q", node.TrustLevel)
	}
}

func TestHeartbeatPreservesCredentialHash(t *testing.T) {
	registry, err := New(filepath.Join(t.TempDir(), "nodes.json"), time.Minute)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	if _, err := registry.Register(context.Background(), NodeInput{ID: "node-a", Name: "local"}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := registry.SetCredentialHash(context.Background(), "node-a", "deadbeef"); err != nil {
		t.Fatalf("set credential hash: %v", err)
	}
	if _, err := registry.Heartbeat(context.Background(), "node-a", NodeInput{Version: "v2"}); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	node, err := registry.Get(context.Background(), "node-a")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if node.CredentialHash != "deadbeef" {
		t.Fatalf("heartbeat must not overwrite credential hash, got %#v", node)
	}
}

func TestRegistryListIncludesRelayFields(t *testing.T) {
	registry, err := New(filepath.Join(t.TempDir(), "nodes.json"), time.Minute)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	if _, err := registry.Register(context.Background(), NodeInput{ID: "node-a", Name: "local", TrustLevel: "local"}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := registry.SetRelayStatus(context.Background(), "node-a", "connected"); err != nil {
		t.Fatalf("set relay status: %v", err)
	}
	list, err := registry.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected one node, got %d", len(list))
	}
	if list[0].RelayStatus != "connected" || list[0].TrustLevel != "local" {
		t.Fatalf("expected relay fields in list view, got %#v", list[0])
	}
}
