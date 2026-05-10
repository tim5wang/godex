package noderegistry

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestRegistryRegisterHeartbeatAndOfflineStatus(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodes.json")
	registry, err := New(path, 60*time.Second)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	now := time.Date(2026, 5, 3, 10, 0, 0, 0, time.UTC)
	registry.SetNow(func() time.Time { return now })

	node, err := registry.Register(context.Background(), NodeInput{
		ID:           "node-a",
		Name:         "local",
		WorkspaceDir: "/repo/a",
		Capabilities: []string{"chat", "tools", "chat"},
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if node.Status != StatusOnline {
		t.Fatalf("expected online status, got %#v", node)
	}

	registry.SetNow(func() time.Time { return now.Add(2 * time.Minute) })
	list, err := registry.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].Status != StatusOffline {
		t.Fatalf("expected stale node to render offline, got %#v", list)
	}

	node, err = registry.Heartbeat(context.Background(), "node-a", NodeInput{Version: "dev"})
	if err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if node.Status != StatusOnline || node.Version != "dev" {
		t.Fatalf("expected heartbeat to revive node, got %#v", node)
	}
}

func TestRegistryPersistsNodes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodes.json")
	registry, err := New(path, time.Minute)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	if _, err := registry.Register(context.Background(), NodeInput{ID: "node-a", Name: "local"}); err != nil {
		t.Fatalf("register: %v", err)
	}

	reloaded, err := New(path, time.Minute)
	if err != nil {
		t.Fatalf("reload registry: %v", err)
	}
	node, err := reloaded.Get(context.Background(), "node-a")
	if err != nil {
		t.Fatalf("get reloaded node: %v", err)
	}
	if node.Name != "local" {
		t.Fatalf("expected persisted name, got %#v", node)
	}
}
