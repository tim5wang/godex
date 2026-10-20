package mcp

import (
	"context"
	"fmt"
	"time"

	"github.com/tim5wang/godex/internal/platform/logger"
)

const toolDiscoveryTimeout = 5 * time.Second
const toolDiscoveryCooldown = 30 * time.Second

type toolDiscovery struct {
	sig    string
	done   chan struct{}
	cancel context.CancelFunc
	tools  []Tool
	err    error
}

type toolDiscoveryFailure struct {
	sig string
	err error
	at  time.Time
}

func cloneTools(items []Tool) []Tool {
	out := make([]Tool, len(items))
	copy(out, items)
	for i := range out {
		out[i].InputSchema = append([]byte(nil), out[i].InputSchema...)
	}
	return out
}

// CachedServerTools never waits for external I/O. Stale declarations remain
// usable while a single bounded background discovery refreshes the cache.
// Callers install this snapshot only at session/turn boundaries.
func (m *Manager) CachedServerTools(name string) ([]Tool, ServerStatus) {
	server, ok, err := m.serverByName(name)
	status := ServerStatus{Name: name, State: "discovering"}
	if err != nil || !ok {
		status.State = "unavailable"
		status.Error = fmt.Sprintf("MCP server %q unavailable: %v", name, err)
		return nil, status
	}
	status.Type = server.Type
	sig := serverSignature(server)
	m.mu.Lock()
	defer m.mu.Unlock()
	flight, err := m.discoverToolsLocked(server)
	var items []Tool
	if entry := m.toolsCache[name]; entry != nil && entry.sig == sig {
		items = cloneTools(entry.tools)
		status.Tools = len(items)
		status.CheckedAt = entry.fetchedAt
		status.Online = true
		status.State = "ready"
	}
	if flight != nil {
		status.State = "discovering"
	}
	if err != nil {
		status.State = "unavailable"
		status.Online = false
		status.Error = err.Error()
		if failure := m.toolFailures[name]; failure != nil {
			status.CheckedAt = failure.at
		}
	}
	return items, status
}

// discoverToolsLocked coalesces discovery across all sessions and callers.
// A cancelled waiter does not cancel the shared probe. Config invalidation and
// Manager.Close do cancel it, and its result may not repopulate an old cache.
func (m *Manager) discoverToolsLocked(server ServerConfig) (*toolDiscovery, error) {
	if m.closed {
		return nil, fmt.Errorf("mcp manager closed")
	}
	sig := serverSignature(server)
	if entry := m.toolsCache[server.Name]; entry != nil && entry.sig == sig && time.Since(entry.fetchedAt) < toolsListCacheTTL {
		return nil, nil
	}
	if failure := m.toolFailures[server.Name]; failure != nil && failure.sig == sig && time.Since(failure.at) < toolDiscoveryCooldown {
		return nil, failure.err
	}
	if flight := m.toolDiscoveries[server.Name]; flight != nil {
		if flight.sig == sig {
			return flight, nil
		}
		flight.cancel()
	}
	ctx, cancel := context.WithTimeout(context.Background(), toolDiscoveryTimeout)
	flight := &toolDiscovery{sig: sig, done: make(chan struct{}), cancel: cancel}
	if m.toolDiscoveries == nil {
		m.toolDiscoveries = make(map[string]*toolDiscovery)
	}
	m.toolDiscoveries[server.Name] = flight
	go func() {
		defer cancel()
		items, err := m.fetchServerTools(ctx, server)
		logFailure := false
		m.mu.Lock()
		flight.tools, flight.err = items, err
		if m.toolDiscoveries[server.Name] == flight {
			delete(m.toolDiscoveries, server.Name)
			if !m.closed && ctx.Err() != context.Canceled {
				if err == nil {
					if m.toolsCache == nil {
						m.toolsCache = make(map[string]*toolsCacheEntry)
					}
					m.toolsCache[server.Name] = &toolsCacheEntry{tools: items, fetchedAt: time.Now(), sig: sig}
					delete(m.toolFailures, server.Name)
				} else {
					if m.toolFailures == nil {
						m.toolFailures = make(map[string]*toolDiscoveryFailure)
					}
					m.toolFailures[server.Name] = &toolDiscoveryFailure{sig: sig, err: err, at: time.Now()}
					logFailure = true
				}
			}
		}
		close(flight.done)
		m.mu.Unlock()
		if logFailure {
			logger.Warnf("MCP tool discovery for %s unavailable (retry after %s): %v", server.Name, toolDiscoveryCooldown, err)
		}
	}()
	return flight, nil
}

// CachedStatuses is the non-blocking health view for UI polling. Explicit
// TestConnection remains available when a user requests a fresh probe.
func (m *Manager) CachedStatuses() ([]ServerStatus, error) {
	servers, err := m.ListServers()
	if err != nil {
		return nil, err
	}
	statuses := make([]ServerStatus, 0, len(servers))
	for _, server := range servers {
		if server.Type == ServerTypeStdio || server.Type == ServerTypeHTTP {
			_, status := m.CachedServerTools(server.Name)
			statuses = append(statuses, status)
		} else {
			status, err := m.testConnection(context.Background(), server)
			if err != nil {
				status.Error = err.Error()
			}
			statuses = append(statuses, *status)
		}
	}
	return statuses, nil
}
