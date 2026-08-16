// Package pluginrt is a lightweight plugin kernel for GoDex builtin Go
// components (research doc P0 / 阶段 A). It provides a versioned capability
// contract, a scope-aware capability registry, a plugin instance lifecycle
// with generations, a reversible effects ledger, and transactional reload —
// without introducing Cordis-style dynamic objects or WASM.
package pluginrt

import (
	"fmt"
	"strings"

	"github.com/tim5wang/godex/internal/core/scope"
)

// State is the lifecycle phase of a plugin instance.
type State int

const (
	// StatePending: declared but not started (dependencies not yet met).
	StatePending State = iota
	// StateLoading: Start hook is running.
	StateLoading
	// StateActive: running normally, effects registered.
	StateActive
	// StateUnloading: Stop hook is running; new calls are rejected.
	StateUnloading
	// StateDisposed: fully torn down; effects reverted.
	StateDisposed
	// StateFailed: Start failed; instance is inert.
	StateFailed
)

func (s State) String() string {
	switch s {
	case StatePending:
		return "pending"
	case StateLoading:
		return "loading"
	case StateActive:
		return "active"
	case StateUnloading:
		return "unloading"
	case StateDisposed:
		return "disposed"
	case StateFailed:
		return "failed"
	default:
		return "unknown"
	}
}

// Manifest declares one plugin: its identity, scope, and capability contract.
type Manifest struct {
	ID       string
	Version  string
	Scope    scope.Id
	Requires []string // capability requirements, e.g. "godex:log@1"
	Provides []string // capabilities this plugin provides, e.g. "godex:tool-provider@1"
}

// Validate checks manifest invariants before registration.
func (m Manifest) Validate() error {
	if strings.TrimSpace(m.ID) == "" {
		return fmt.Errorf("plugin manifest missing id")
	}
	if m.Scope == "" {
		return fmt.Errorf("plugin %s missing scope", m.ID)
	}
	if _, _, ok := scope.Parse(m.Scope); !ok {
		return fmt.Errorf("plugin %s has invalid scope %q", m.ID, m.Scope)
	}
	for _, raw := range m.Requires {
		if _, err := parseCapability(raw); err != nil {
			return fmt.Errorf("plugin %s: %v", m.ID, err)
		}
	}
	for _, raw := range m.Provides {
		if _, err := parseCapability(raw); err != nil {
			return fmt.Errorf("plugin %s: %v", m.ID, err)
		}
	}
	return nil
}

// capability is a parsed "namespace:name[@major]" capability reference.
type capability struct {
	Name  string
	Major int
	Any   bool // no @major suffix: any version satisfies
}

func parseCapability(raw string) (capability, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return capability{}, fmt.Errorf("empty capability")
	}
	name, majorRaw, _ := strings.Cut(raw, "@")
	name = strings.TrimSpace(name)
	if name == "" {
		return capability{}, fmt.Errorf("invalid capability %q: missing name", raw)
	}
	parts := strings.Split(name, ":")
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return capability{}, fmt.Errorf("invalid capability %q: expected namespace:name", raw)
	}
	cap := capability{Name: name}
	if strings.TrimSpace(majorRaw) != "" {
		n, err := parseMajor(majorRaw)
		if err != nil {
			return capability{}, fmt.Errorf("invalid capability %q: %v", raw, err)
		}
		cap.Major = n
	} else {
		cap.Any = true
	}
	return cap, nil
}

func parseMajor(value string) (int, error) {
	n := 0
	for _, r := range strings.TrimSpace(value) {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("invalid major version %q", value)
		}
		n = n*10 + int(r-'0')
	}
	return n, nil
}

// matches reports whether the provided capability satisfies a requirement.
// A requirement with no major suffix matches any provided version; with a
// suffix it requires an exact major match.
func matches(requirement, provided string) bool {
	req, err := parseCapability(requirement)
	if err != nil {
		return false
	}
	prov, err := parseCapability(provided)
	if err != nil {
		return false
	}
	if req.Name != prov.Name {
		return false
	}
	if req.Any {
		return true
	}
	return prov.Major == req.Major
}
