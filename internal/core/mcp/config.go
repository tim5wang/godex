package mcp

import (
	"encoding/json"
	"os"
)

const ServerTypeFilesystem = "filesystem"

// Config stores the small read-only MCP server registry for the local agent.
type Config struct {
	Servers []ServerConfig `json:"servers"`
}

// ServerConfig describes one configured read-only MCP resource provider.
type ServerConfig struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Root string `json:"root"`
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
