package noderegistry

import (
	"context"
	"os"
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

func TestNormalizeKind(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"pc", KindPC},
		{"cloud", KindCloud},
		{"phone", KindPhone},
		// Case and surrounding space are not a different kind.
		{"  PHONE  ", KindPhone},
		// Absent means "not stated", which is a pc until told otherwise.
		{"", DefaultKind},
		{"   ", DefaultKind},
		// An unknown value is a typo, not a new kind: the set stays closed so
		// consumers can switch on it exhaustively.
		{"laptop", DefaultKind},
	}
	for _, tc := range cases {
		if got := NormalizeKind(tc.in); got != tc.want {
			t.Errorf("NormalizeKind(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestRegistryKindDefaultsOnRegister pins the default for a node that declares
// nothing: consumers may rely on a view always carrying a kind.
func TestRegistryKindDefaultsOnRegister(t *testing.T) {
	registry, err := New(filepath.Join(t.TempDir(), "nodes.json"), time.Minute)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	node, err := registry.Register(context.Background(), NodeInput{ID: "node-a", Name: "local"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if node.Kind != DefaultKind {
		t.Fatalf("kind = %q, want default %q", node.Kind, DefaultKind)
	}
}

// TestRegistryHeartbeatWithoutKindPreservesDeclaredKind guards a real regression
// risk: heartbeats are frequent and a partial payload must not silently downgrade
// a phone or cloud node back to the default.
func TestRegistryHeartbeatWithoutKindPreservesDeclaredKind(t *testing.T) {
	registry, err := New(filepath.Join(t.TempDir(), "nodes.json"), time.Minute)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	if _, err := registry.Register(context.Background(), NodeInput{ID: "node-a", Name: "local", Kind: KindPhone}); err != nil {
		t.Fatalf("register: %v", err)
	}

	// A heartbeat carrying only unrelated fields, as a node with no kind set sends.
	node, err := registry.Heartbeat(context.Background(), "node-a", NodeInput{Version: "dev"})
	if err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if node.Kind != KindPhone {
		t.Fatalf("heartbeat cleared kind: got %q, want %q", node.Kind, KindPhone)
	}

	// An explicit kind does update it.
	node, err = registry.Heartbeat(context.Background(), "node-a", NodeInput{Kind: KindCloud})
	if err != nil {
		t.Fatalf("heartbeat with kind: %v", err)
	}
	if node.Kind != KindCloud {
		t.Fatalf("explicit kind ignored: got %q, want %q", node.Kind, KindCloud)
	}
}

// TestRegistryKindPersistsAcrossReload covers the on-disk round trip: a kind that
// only survived in memory would reset on every restart.
func TestRegistryKindPersistsAcrossReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodes.json")
	registry, err := New(path, time.Minute)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	if _, err := registry.Register(context.Background(), NodeInput{ID: "node-a", Kind: KindCloud}); err != nil {
		t.Fatalf("register: %v", err)
	}

	reloaded, err := New(path, time.Minute)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	node, err := reloaded.Get(context.Background(), "node-a")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if node.Kind != KindCloud {
		t.Fatalf("kind after reload = %q, want %q", node.Kind, KindCloud)
	}
}

// TestRegistryKindDefaultsForLegacySnapshot covers a snapshot written before kind
// existed. It bypasses mergeLocked (load reads the file directly), so the default
// has to be applied on read as well as on write.
func TestRegistryKindDefaultsForLegacySnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodes.json")
	legacy := []byte(`{"nodes":[{"id":"old-node","name":"legacy","status":"online"}]}`)
	if err := os.WriteFile(path, legacy, 0600); err != nil {
		t.Fatalf("write legacy snapshot: %v", err)
	}

	registry, err := New(path, time.Minute)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	node, err := registry.Get(context.Background(), "old-node")
	if err != nil {
		t.Fatalf("get legacy node: %v", err)
	}
	if node.Kind != DefaultKind {
		t.Fatalf("legacy node kind = %q, want default %q", node.Kind, DefaultKind)
	}

	// List goes through the same view path and must agree.
	list, err := registry.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].Kind != DefaultKind {
		t.Fatalf("legacy list kind = %#v, want default %q", list, DefaultKind)
	}
}
