package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/tim5wang/godex/internal/core/mcp"
)

// ListMCPServers returns the configured MCP server registry without connecting
// to any server.
func (a *Agent) ListMCPServers() ([]mcp.ServerConfig, error) {
	if a == nil || a.mcpMgr == nil {
		return nil, fmt.Errorf("mcp runtime is unavailable")
	}
	return a.mcpMgr.ListServers()
}

// ListMCPServerTools discovers one configured server's tool declarations.
func (a *Agent) ListMCPServerTools(ctx context.Context, serverName string) ([]mcp.Tool, error) {
	if a == nil || a.mcpMgr == nil {
		return nil, fmt.Errorf("mcp runtime is unavailable")
	}
	serverName = strings.TrimSpace(serverName)
	if serverName == "" {
		return nil, fmt.Errorf("mcp server name is required")
	}
	if _, err := a.mcpMgr.GetServer(serverName); err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return a.mcpMgr.ListServerTools(ctx, serverName)
}

// LoadMCPServer exposes one configured server's tools on this session's active
// tool surface. Active tool names are included in the persisted session state.
func (a *Agent) LoadMCPServer(ctx context.Context, serverName string) ([]mcp.Tool, error) {
	serverName = strings.TrimSpace(serverName)
	if serverName == "" {
		return nil, fmt.Errorf("mcp server name is required")
	}
	server, err := a.mcpServerConfig(serverName)
	if err != nil {
		return nil, err
	}
	if server.Type != mcp.ServerTypeStdio && server.Type != mcp.ServerTypeHTTP {
		return nil, fmt.Errorf("mcp server %q does not expose tools (type %q)", serverName, server.Type)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	decls, err := a.mcpMgr.ListServerTools(ctx, serverName)
	if err != nil {
		return nil, err
	}
	if len(decls) == 0 {
		return nil, fmt.Errorf("mcp server %q exposes no tools", serverName)
	}
	if err := a.RegisterTransientMCPServerTools(ctx, "mcp:"+serverName, serverName); err != nil {
		return nil, err
	}
	return decls, nil
}

func (a *Agent) mcpServerConfig(serverName string) (mcp.ServerConfig, error) {
	if a == nil || a.mcpMgr == nil {
		return mcp.ServerConfig{}, fmt.Errorf("mcp runtime is unavailable")
	}
	return a.mcpMgr.GetServer(serverName)
}
