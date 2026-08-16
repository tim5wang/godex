package pluginrt

import (
	"fmt"
	"strings"
	"sync"
)

// CredentialBroker exposes named secrets (from process env or a loaded .env
// map) to plugins under an explicit allowlist (阶段 C: HTTP/credential/KV
// broker — credential slice). A plugin may read only the secret names its
// manifest/install authorized; everything else returns an error, so a plugin
// can never enumerate or exfiltrate unrelated credentials.
type CredentialBroker struct {
	mu        sync.RWMutex
	env       func(string) (string, bool)
	allowed   map[string]map[string]struct{} // pluginID -> allowed secret names
}

// NewCredentialBroker builds a broker reading secrets from the given lookup
// (e.g. os.LookupEnv or a merged .env map). allowed maps plugin id to the set
// of secret names that plugin may read.
func NewCredentialBroker(lookup func(string) (string, bool), allowed map[string][]string) *CredentialBroker {
	b := &CredentialBroker{
		env:     lookup,
		allowed: make(map[string]map[string]struct{}, len(allowed)),
	}
	for pluginID, names := range allowed {
		set := make(map[string]struct{}, len(names))
		for _, name := range names {
			name = strings.TrimSpace(name)
			if name != "" {
				set[name] = struct{}{}
			}
		}
		if len(set) > 0 {
			b.allowed[pluginID] = set
		}
	}
	return b
}

// Allow grants pluginID access to the named secrets (used at activation time
// from the plugin's declared credential permissions).
func (b *CredentialBroker) Allow(pluginID string, names ...string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	set := b.allowed[pluginID]
	if set == nil {
		set = make(map[string]struct{})
		b.allowed[pluginID] = set
	}
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name != "" {
			set[name] = struct{}{}
		}
	}
}

// Get returns the named secret for pluginID, or an error when the plugin is
// not authorized or the secret is not set. Authorized-but-unset secrets return
// an explicit "not set" error (distinct from "not allowed").
func (b *CredentialBroker) Get(pluginID, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("credential broker: empty secret name")
	}
	b.mu.RLock()
	allowed, pluginAllowed := b.allowed[strings.TrimSpace(pluginID)]
	b.mu.RUnlock()
	if !pluginAllowed {
		return "", fmt.Errorf("credential broker: plugin %q has no credential permissions", pluginID)
	}
	if _, ok := allowed[name]; !ok {
		return "", fmt.Errorf("credential broker: plugin %q is not allowed to read %q", pluginID, name)
	}
	if b.env == nil {
		return "", fmt.Errorf("credential broker: environment lookup unavailable")
	}
	value, ok := b.env(name)
	if !ok || strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("credential broker: secret %q is not set", name)
	}
	return value, nil
}

// adapterFunc returns a wasmrt-compatible host callback bound to a plugin id.
func (b *CredentialBroker) adapterFunc(pluginID string) func(string) (string, error) {
	return func(name string) (string, error) {
		return b.Get(pluginID, name)
	}
}
