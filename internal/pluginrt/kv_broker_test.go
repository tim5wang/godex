package pluginrt

import (
	"github.com/tim5wang/godex/internal/core/persistence"
	"path/filepath"
	"testing"
)

func TestPluginKVBrokerNamespaceIsolation(t *testing.T) {
	broker := NewPluginKVBroker(persistence.NewMemoryMap[string]())
	if err := broker.Set("plugin-a", "greeting", "hello a"); err != nil {
		t.Fatalf("set a: %v", err)
	}
	if err := broker.Set("plugin-b", "greeting", "hello b"); err != nil {
		t.Fatalf("set b: %v", err)
	}
	if got := broker.Get("plugin-a", "greeting"); got != "hello a" {
		t.Fatalf("plugin-a greeting = %q, want hello a", got)
	}
	if got := broker.Get("plugin-b", "greeting"); got != "hello b" {
		t.Fatalf("plugin-b greeting = %q, want hello b", got)
	}
	// Same key, different namespace: fully isolated.
	if got := broker.Get("plugin-a", "greeting"); got == broker.Get("plugin-b", "greeting") {
		t.Fatal("namespaces must not collide")
	}
	// Missing key -> "".
	if got := broker.Get("plugin-a", "nope"); got != "" {
		t.Fatalf("missing key = %q, want empty", got)
	}
	// Delete removes only the plugin's own entry.
	if err := broker.Delete("plugin-a", "greeting"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if got := broker.Get("plugin-a", "greeting"); got != "" {
		t.Fatalf("expected deleted, got %q", got)
	}
	if got := broker.Get("plugin-b", "greeting"); got != "hello b" {
		t.Fatalf("plugin-b must survive plugin-a delete, got %q", got)
	}
}

func TestPluginKVBrokerRejectsBadKeys(t *testing.T) {
	broker := NewPluginKVBroker(persistence.NewMemoryMap[string]())
	if err := broker.Set("", "k", "v"); err == nil {
		t.Fatal("expected error for empty plugin id")
	}
	if err := broker.Set("p", "", "v"); err == nil {
		t.Fatal("expected error for empty key")
	}
	if err := broker.Set("p", "a|b", "v"); err == nil {
		t.Fatal("expected error for key containing separator")
	}
	if got := broker.Get("", "k"); got != "" {
		t.Fatalf("empty plugin id get = %q", got)
	}
}

func TestPluginKVBrokerAdapterFuncs(t *testing.T) {
	broker := NewPluginKVBroker(persistence.NewMemoryMap[string]())
	get, set := broker.adapterFuncs("plugin-x")
	set("k", "v")
	if got := get("k"); got != "v" {
		t.Fatalf("adapter get = %q, want v", got)
	}
	// The adapter is bound to plugin-x; plugin-y cannot read it.
	if got := broker.Get("plugin-y", "k"); got != "" {
		t.Fatalf("plugin-y read plugin-x key = %q", got)
	}
}

func TestPluginKVBrokerDurable(t *testing.T) {
	dir := t.TempDir()
	table := "plugin_kv"

	broker, err := NewPluginKVBrokerDurable(dir, table)
	if err != nil {
		t.Fatalf("durable broker: %v", err)
	}
	if err := broker.Set("plugin-a", "persisted", "yes"); err != nil {
		t.Fatalf("set: %v", err)
	}

	// Reopen a fresh broker over the same store: value survives.
	reopened, err := NewPluginKVBrokerDurable(dir, table)
	if err != nil {
		t.Fatalf("reopen broker: %v", err)
	}
	if got := reopened.Get("plugin-a", "persisted"); got != "yes" {
		t.Fatalf("durable get = %q, want yes", got)
	}
	_ = filepath.Join
}
