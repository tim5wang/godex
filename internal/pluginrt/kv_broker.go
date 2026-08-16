package pluginrt

import (
	"fmt"
	"strings"
	"sync"

	"github.com/tim5wang/godex/internal/core/persistence"
)

// KVStore is the storage interface the broker needs (a subset of
// persistence.DurableMap). Using an interface keeps tests light.
type KVStore interface {
	Get(key string) (string, bool, error)
	Put(key, value string) error
	Delete(key string) error
}

// PluginKVBroker is a namespaced, scope-scoped key/value broker for plugin
// host calls (阶段 C: HTTP/credential/KV broker — KV slice). Each plugin reads
// and writes only its own namespace, so one plugin can never observe or clobber
// another's state; the backing store is durable (SQLite) and survives restarts.
//
// Wire it into wasmrt.HostCallbacks:
//
//	HostCallbacks{
//	  KVGet: broker.Get,
//	  KVSet: broker.Set,
//	}
type PluginKVBroker struct {
	store KVStore
	mu    sync.RWMutex
}

// NewPluginKVBroker wraps a durable string store (e.g. a SQLiteMap[string]).
func NewPluginKVBroker(store KVStore) *PluginKVBroker {
	return &PluginKVBroker{store: store}
}

// NewPluginKVBrokerDurable creates a broker backed by a SQLite map under
// stateDir (survives restarts). Returns an error when the store cannot be
// constructed.
func NewPluginKVBrokerDurable(stateDir, table string) (*PluginKVBroker, error) {
	store, err := persistence.NewSQLiteMap[string](stateDir, table)
	if err != nil {
		return nil, err
	}
	return NewPluginKVBroker(store), nil
}

// key namespaces a plugin KV entry so different plugins are isolated:
// "<pluginID>|<key>".
func kvKey(pluginID, key string) (string, error) {
	pluginID = strings.TrimSpace(pluginID)
	key = strings.TrimSpace(key)
	if pluginID == "" {
		return "", fmt.Errorf("plugin KV: empty plugin id")
	}
	if key == "" {
		return "", fmt.Errorf("plugin KV: empty key")
	}
	// Guard against key-based namespace confusion: a key containing the
	// separator cannot alias another plugin's namespace.
	if strings.Contains(key, "|") {
		return "", fmt.Errorf("plugin KV: key must not contain '|'")
	}
	return pluginID + "|" + key, nil
}

// Get returns the value for a plugin's key ("" when absent). Safe for
// concurrent use; the plugin id is part of the key so namespaces never leak.
func (b *PluginKVBroker) Get(pluginID, key string) string {
	full, err := kvKey(pluginID, key)
	if err != nil {
		return ""
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	value, ok, err := b.store.Get(full)
	if err != nil || !ok {
		return ""
	}
	return value
}

// Set stores a plugin's key. Safe for concurrent use.
func (b *PluginKVBroker) Set(pluginID, key, value string) error {
	full, err := kvKey(pluginID, key)
	if err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.store.Put(full, value)
}

// Delete removes a plugin's key.
func (b *PluginKVBroker) Delete(pluginID, key string) error {
	full, err := kvKey(pluginID, key)
	if err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.store.Delete(full)
}

// adapterFuncs returns the wasmrt-compatible host callbacks bound to a plugin
// id. The returned functions are safe to use directly as HostCallbacks.KVGet
// and HostCallbacks.KVSet.
func (b *PluginKVBroker) adapterFuncs(pluginID string) (func(string) string, func(string, string)) {
	return func(key string) string {
			return b.Get(pluginID, key)
		},
		func(key, value string) {
			_ = b.Set(pluginID, key, value)
		}
}
