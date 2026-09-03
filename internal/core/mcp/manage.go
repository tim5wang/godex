package mcp

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

// ServerStatus describes the runtime health of one configured MCP server.
type ServerStatus struct {
	Name      string    `json:"name"`
	Type      string    `json:"type"`
	Online    bool      `json:"online"`
	Error     string    `json:"error,omitempty"`
	Tools     int       `json:"tools,omitempty"`
	CheckedAt time.Time `json:"checked_at"`
}

// ListServers returns all configured MCP servers in a stable (name-sorted)
// order, without opening any connection.
func (m *Manager) ListServers() ([]ServerConfig, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	cfg, err := LoadConfig(m.configPath)
	if err != nil {
		return nil, err
	}
	out := make([]ServerConfig, len(cfg.Servers))
	copy(out, cfg.Servers)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// GetServer returns one configured server by name.
func (m *Manager) GetServer(name string) (ServerConfig, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	name = strings.TrimSpace(name)
	cfg, err := LoadConfig(m.configPath)
	if err != nil {
		return ServerConfig{}, err
	}
	for _, s := range cfg.Servers {
		if s.Name == name {
			return s, nil
		}
	}
	return ServerConfig{}, fmt.Errorf("mcp server not found: %s", name)
}

// UpsertServer creates or updates an MCP server in the registry and persists
// it to the config file. The value is copied on write so callers can reuse the
// struct without mutating the stored entry.
func (m *Manager) UpsertServer(server ServerConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := validateServer(server); err != nil {
		return err
	}
	server.Name = strings.TrimSpace(server.Name)
	cfg, err := LoadConfig(m.configPath)
	if err != nil {
		return err
	}
	replaced := false
	for i := range cfg.Servers {
		if cfg.Servers[i].Name == server.Name {
			cfg.Servers[i] = server
			replaced = true
			break
		}
	}
	if !replaced {
		cfg.Servers = append(cfg.Servers, server)
	}
	if err := SaveConfig(m.configPath, cfg); err != nil {
		return err
	}
	// The server's config changed: drop its cached tools and persistent
	// client so the next use reconnects with the new settings.
	m.invalidateServerLocked(server.Name)
	return nil
}

// DeleteServer removes a server by name from the registry and persists the
// change. A missing server is not an error (idempotent delete).
func (m *Manager) DeleteServer(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	name = strings.TrimSpace(name)
	cfg, err := LoadConfig(m.configPath)
	if err != nil {
		return err
	}
	out := cfg.Servers[:0]
	for _, s := range cfg.Servers {
		if s.Name != name {
			out = append(out, s)
		}
	}
	cfg.Servers = out
	if err := SaveConfig(m.configPath, cfg); err != nil {
		return err
	}
	m.invalidateServerLocked(name)
	return nil
}

// TestConnection opens a client for the named server and performs the MCP
// initialize handshake. It returns the discovered tool count and the round-trip
// latency. stdio servers validate the command; http servers validate the URL
// and reachability. Filesystem servers are checked for the directory.
func (m *Manager) TestConnection(ctx context.Context, name string) (*ServerStatus, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	name = strings.TrimSpace(name)
	cfg, err := LoadConfig(m.configPath)
	if err != nil {
		return nil, err
	}
	var server ServerConfig
	found := false
	for _, s := range cfg.Servers {
		if s.Name == name {
			server = s
			found = true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("mcp server not found: %s", name)
	}
	start := time.Now()
	status := ServerStatus{Name: server.Name, Type: server.Type, CheckedAt: start}

	switch server.Type {
	case ServerTypeFilesystem, "":
		if strings.TrimSpace(server.Root) == "" {
			status.Error = "filesystem server missing root"
			return &status, fmt.Errorf("%s", status.Error)
		}
		if ok, err := dirExists(server.Root); err != nil || !ok {
			if err == nil {
				status.Error = "filesystem root not found"
			} else {
				status.Error = err.Error()
			}
			return &status, fmt.Errorf("%s", status.Error)
		}
		status.Online = true
		return &status, nil
	default:
		client, err := clientFor(ctx, server)
		if err != nil {
			status.Error = err.Error()
			return &status, err
		}
		defer client.close()
		if _, err := client.initialize(ctx); err != nil {
			status.Error = err.Error()
			return &status, err
		}
		tools, err := client.listTools(ctx)
		if err != nil {
			status.Error = err.Error()
			return &status, err
		}
		status.Tools = len(tools)
		status.Online = true
		return &status, nil
	}
}

// Statuses probes every configured server and returns health for each. Servers
// that fail to handshake are reported as offline with a reason (they never
// abort the batch — one broken server does not hide the rest).
func (m *Manager) Statuses(ctx context.Context) ([]ServerStatus, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	cfg, err := LoadConfig(m.configPath)
	if err != nil {
		return nil, err
	}
	out := make([]ServerStatus, 0, len(cfg.Servers))
	for _, s := range cfg.Servers {
		status := s
		// Give each probe a bounded deadline so a hung upstream can't stall the
		// whole list.
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		st, err := m.testConnection(cctx, status)
		cancel()
		if err != nil {
			if st == nil {
				st = &ServerStatus{Name: status.Name, Type: status.Type, Online: false, Error: err.Error(), CheckedAt: time.Now()}
			}
			if st.Error == "" {
				st.Error = err.Error()
			}
		}
		out = append(out, *st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// testConnection is the unexported, lock-free probe used by Statuses (which
// already holds the read lock) and by TestConnection.
func (m *Manager) testConnection(ctx context.Context, server ServerConfig) (*ServerStatus, error) {
	start := time.Now()
	status := ServerStatus{Name: server.Name, Type: server.Type, CheckedAt: start}
	switch server.Type {
	case ServerTypeFilesystem, "":
		root := strings.TrimSpace(server.Root)
		if root == "" {
			return &status, fmt.Errorf("filesystem server missing root")
		}
		if ok, err := dirExists(root); err != nil || !ok {
			if err == nil {
				return &status, fmt.Errorf("filesystem root not found")
			}
			return &status, err
		}
		status.Online = true
		return &status, nil
	default:
		client, err := clientFor(ctx, server)
		if err != nil {
			status.Error = err.Error()
			return &status, err
		}
		defer client.close()
		if _, err := client.initialize(ctx); err != nil {
			status.Error = err.Error()
			return &status, err
		}
		tools, err := client.listTools(ctx)
		if err != nil {
			status.Error = err.Error()
			return &status, err
		}
		status.Tools = len(tools)
		status.Online = true
		return &status, nil
	}
}

// validateServer ensures a server has a name and the fields required by its
// transport type.
func validateServer(server ServerConfig) error {
	if strings.TrimSpace(server.Name) == "" {
		return fmt.Errorf("mcp server name is required")
	}
	switch server.Type {
	case ServerTypeFilesystem, "":
		if strings.TrimSpace(server.Root) == "" {
			return fmt.Errorf("filesystem server %s missing root", server.Name)
		}
	case ServerTypeStdio:
		if strings.TrimSpace(server.Command) == "" {
			return fmt.Errorf("stdio server %s missing command", server.Name)
		}
	case ServerTypeHTTP:
		if strings.TrimSpace(server.URL) == "" {
			return fmt.Errorf("streamable-http server %s missing url", server.Name)
		}
	default:
		return fmt.Errorf("unsupported MCP server type %q", server.Type)
	}
	return nil
}

func dirExists(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return info.IsDir(), nil
}
