package mcp

import (
	"encoding/json"
	"os"
)

const (
	// ServerTypeFilesystem is a read-only local directory resource provider.
	ServerTypeFilesystem = "filesystem"
	// ServerTypeStdio is an external MCP server speaking JSON-RPC 2.0 over
	// stdio (the cross-runtime plugin bridge: any language can implement one).
	ServerTypeStdio = "stdio"
)

// Config stores the MCP server registry for the local agent. Servers may be
// read-only filesystem resource providers or full stdio MCP servers (tools).
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
