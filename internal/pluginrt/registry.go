package pluginrt

import (
	"sort"
	"strings"
	"sync"
)

// Registry is a scope-aware capability registry: it records which plugin
// provides which capability in which scope, so dependency resolution and
// unloads are scope-correct. It is the runtime table behind the Graph's
// static checks.
type Registry struct {
	mu sync.RWMutex
	// scope -> capability -> provider plugin ids
	byScope map[string]map[string][]string
}

// NewRegistry creates an empty scope-aware capability registry.
func NewRegistry() *Registry {
	return &Registry{byScope: make(map[string]map[string][]string)}
}

// Record registers that plugin provides capability in scope. Idempotent.
func (r *Registry) Record(scopeID, capability, pluginID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	capabilities := r.byScope[scopeID]
	if capabilities == nil {
		capabilities = make(map[string][]string)
		r.byScope[scopeID] = capabilities
	}
	for _, existing := range capabilities[capability] {
		if existing == pluginID {
			return
		}
	}
	capabilities[capability] = append(capabilities[capability], pluginID)
}

// Revoke removes plugin as provider of capability in scope. Idempotent.
func (r *Registry) Revoke(scopeID, capability, pluginID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	capabilities := r.byScope[scopeID]
	if capabilities == nil {
		return
	}
	providers := capabilities[capability]
	kept := providers[:0]
	for _, existing := range providers {
		if existing != pluginID {
			kept = append(kept, existing)
		}
	}
	if len(kept) == 0 {
		delete(capabilities, capability)
	} else {
		capabilities[capability] = kept
	}
}

// RevokeAll removes every capability record of plugin in scope.
func (r *Registry) RevokeAll(scopeID, pluginID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	capabilities := r.byScope[scopeID]
	if capabilities == nil {
		return
	}
	for capability, providers := range capabilities {
		kept := providers[:0]
		for _, existing := range providers {
			if existing != pluginID {
				kept = append(kept, existing)
			}
		}
		if len(kept) == 0 {
			delete(capabilities, capability)
		} else {
			capabilities[capability] = kept
		}
	}
}

// Providers returns the plugin ids providing capability in scope, sorted.
func (r *Registry) Providers(scopeID, capability string) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	providers := append([]string{}, r.byScope[scopeID][capability]...)
	sort.Strings(providers)
	return providers
}

// Provided reports whether capability has at least one provider in scope.
func (r *Registry) Provided(scopeID, capability string) bool {
	return len(r.Providers(scopeID, capability)) > 0
}

// Capabilities returns the sorted capability names recorded in scope.
func (r *Registry) Capabilities(scopeID string) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.byScope[scopeID]))
	for capability := range r.byScope[scopeID] {
		names = append(names, capability)
	}
	sort.Strings(names)
	return names
}

// MatchCapability returns the recorded capability string (with version) that
// satisfies requirement, or "" when none matches in scope.
func (r *Registry) MatchCapability(scopeID, requirement string) string {
	req, err := parseCapability(requirement)
	if err != nil {
		return ""
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for capability := range r.byScope[scopeID] {
		if matches(requirement, capability) || strings.TrimSpace(capability) == req.Name {
			return capability
		}
	}
	return ""
}
