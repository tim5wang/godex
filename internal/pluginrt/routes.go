package pluginrt

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// pluginRoute is one plugin-owned HTTP prefix: the plugin registered its
// handlers on a dedicated mux and the manager dispatches requests that fall
// under the prefix to it.
type pluginRoute struct {
	pluginID string
	mux      *http.ServeMux
}

// dispatcher serves requests for one registered prefix by looking up the
// owning plugin's mux at request time — so a deactivation (which removes the
// route) immediately yields 404 without touching the root mux.
type dispatcher struct {
	m      *Manager
	prefix string
}

func (d dispatcher) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.m.routesMu.RLock()
	route := d.m.routes[d.prefix]
	d.m.routesMu.RUnlock()
	if route == nil {
		http.NotFound(w, r)
		return
	}
	route.mux.ServeHTTP(w, r)
}

// registerPluginRoutes creates a dedicated mux for prefix, runs register on
// it, and records the route. Patterns registered by the plugin must use the
// full request path (same style as the httpapi mux), e.g.
// "GET /v1/taskboard/cards".
func (m *Manager) registerPluginRoutes(pluginID, prefix string, register func(mux *http.ServeMux)) error {
	prefix = strings.TrimRight(strings.TrimSpace(prefix), "/")
	if !strings.HasPrefix(prefix, "/") || prefix == "/" {
		return fmt.Errorf("plugin %s route prefix %q must start with / and not be the root", pluginID, prefix)
	}
	if register == nil {
		return fmt.Errorf("plugin %s route register callback is required", pluginID)
	}
	m.routesMu.Lock()
	defer m.routesMu.Unlock()
	if existing := m.routes[prefix]; existing != nil && existing.pluginID != pluginID {
		return fmt.Errorf("plugin %s route prefix %q already owned by plugin %s", pluginID, prefix, existing.pluginID)
	}
	sub := http.NewServeMux()
	register(sub)
	m.routes[prefix] = &pluginRoute{pluginID: pluginID, mux: sub}
	// Late activation: if the root mux is already mounted, attach the
	// dispatcher now (http.ServeMux.Handle is safe for concurrent use).
	if m.routeRoot != nil {
		m.mountRouteLocked(m.routeRoot, prefix)
	}
	return nil
}

// removePluginRoutes unmounts one prefix registered by pluginID. Removing a
// route the plugin no longer owns is a no-op (idempotent reversal).
func (m *Manager) removePluginRoutes(pluginID, prefix string) {
	m.routesMu.Lock()
	defer m.routesMu.Unlock()
	route := m.routes[prefix]
	if route == nil || route.pluginID != pluginID {
		return
	}
	delete(m.routes, prefix)
}

// mountRouteLocked attaches the dispatcher for prefix to root. Callers must
// hold routesMu (write).
func (m *Manager) mountRouteLocked(root *http.ServeMux, prefix string) {
	d := dispatcher{m: m, prefix: prefix}
	root.Handle(prefix, d)
	root.Handle(prefix+"/", d)
}

// MountRoutes mounts every currently registered plugin prefix on root and
// remembers it so plugins activated later are mounted automatically. Safe to
// call once during httpapi assembly.
func (m *Manager) MountRoutes(root *http.ServeMux) {
	if root == nil {
		return
	}
	m.routesMu.Lock()
	defer m.routesMu.Unlock()
	m.routeRoot = root
	for prefix := range m.routes {
		m.mountRouteLocked(root, prefix)
	}
}

// RoutePrefixes returns the currently registered prefixes sorted (diagnostics).
func (m *Manager) RoutePrefixes() []string {
	m.routesMu.RLock()
	defer m.routesMu.RUnlock()
	out := make([]string, 0, len(m.routes))
	for prefix := range m.routes {
		out = append(out, prefix)
	}
	sort.Strings(out)
	return out
}
