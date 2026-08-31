package mcp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const (
	// ServerTypeFilesystem is a read-only local directory resource provider.
	ServerTypeFilesystem = "filesystem"
	// ServerTypeStdio is an external MCP server speaking JSON-RPC 2.0 over
	// stdio (the cross-runtime plugin bridge: any language can implement one).
	ServerTypeStdio = "stdio"
	// ServerTypeHTTP is a remote MCP server speaking JSON-RPC 2.0 over the
	// Streamable HTTP transport (a single POST endpoint). This is how business
	// systems register tools with the Agent Step Platform (Phase A).
	ServerTypeHTTP = "streamable-http"
)

// Config stores the MCP server registry for the local agent. Servers may be
// read-only filesystem resource providers, full stdio MCP servers (tools), or
// remote Streamable HTTP MCP servers (tools).
type Config struct {
	Servers []ServerConfig `json:"servers"`
}

// ServerConfig describes one configured MCP server.
type ServerConfig struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Root string `json:"root,omitempty"`
	// Command/Args/Env configure a stdio MCP server process. Command is
	// required for type "stdio"; Root is only used by filesystem servers.
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	// URL/Headers configure a remote Streamable HTTP server. URL is required
	// for type "streamable-http"; Headers are merged into every POST (e.g.
	// Authorization). SessionRequired is reserved for future Mcp-Session-Id
	// support; the current client is stateless even when it is true.
	URL             string            `json:"url,omitempty"`
	Headers         map[string]string `json:"headers,omitempty"`
	SessionRequired bool              `json:"session_required,omitempty"`
}

// LoadConfig reads the MCP config file. Missing config is treated as empty.
func LoadConfig(path string) (Config, error) {
	if path == "" {
		return Config{}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}, nil
		}
		return Config{}, err
	}
	if len(data) == 0 {
		return Config{}, nil
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// SaveConfig atomically writes the MCP config file, creating its directory if
// needed. It is the write path used by the lifecycle-management manager so a
// newly registered server survives a process restart.
func SaveConfig(path string, cfg Config) error {
	if path == "" {
		return fmt.Errorf("mcp config path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
