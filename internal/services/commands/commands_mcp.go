package commands

import (
	"context"
	"fmt"
	"strings"

	"github.com/tim5wang/godex/internal/agent"
	"github.com/tim5wang/godex/internal/core/mcp"
)

func (s *Service) executeMCP(ctx context.Context, a *agent.Agent, cmd Command) (Result, error) {
	args := cmd.Args
	if len(args) == 0 {
		args = []string{"list"}
	}

	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "list":
		if len(args) != 1 {
			return Result{}, fmt.Errorf("usage: /mcp list")
		}
		servers, err := a.ListMCPServers()
		if err != nil {
			return Result{}, err
		}
		return Result{Name: "mcp", Output: renderMCPServers(servers)}, nil
	case "tools":
		if len(args) != 2 || strings.TrimSpace(args[1]) == "" {
			return Result{}, fmt.Errorf("usage: /mcp tools <server>")
		}
		items, err := a.ListMCPServerTools(ctx, args[1])
		if err != nil {
			return Result{}, err
		}
		return Result{Name: "mcp", Output: renderMCPServerTools(args[1], items)}, nil
	case "load":
		if len(args) != 2 || strings.TrimSpace(args[1]) == "" {
			return Result{}, fmt.Errorf("usage: /mcp load <server>")
		}
		items, err := a.LoadMCPServer(ctx, args[1])
		if err != nil {
			return Result{}, err
		}
		return Result{
			Name:            "mcp",
			Output:          fmt.Sprintf("Loaded %d tools from MCP server %q into this session.", len(items), args[1]),
			RefreshSnapshot: true,
		}, nil
	default:
		return Result{}, fmt.Errorf("unknown /mcp subcommand %q", args[0])
	}
}

func renderMCPServers(servers []mcp.ServerConfig) string {
	if len(servers) == 0 {
		return "No MCP servers configured."
	}
	var out strings.Builder
	out.WriteString("Configured MCP servers:")
	for _, server := range servers {
		out.WriteString("\n- ")
		out.WriteString(server.Name)
		if server.Type != "" {
			out.WriteString(" (")
			out.WriteString(server.Type)
			out.WriteByte(')')
		}
	}
	out.WriteString("\nUse /mcp tools <server> to inspect tools or /mcp load <server> to activate them.")
	return out.String()
}

func renderMCPServerTools(serverName string, items []mcp.Tool) string {
	if len(items) == 0 {
		return fmt.Sprintf("MCP server %q exposes no tools.", serverName)
	}
	var out strings.Builder
	fmt.Fprintf(&out, "Tools from MCP server %q:", serverName)
	for _, item := range items {
		out.WriteString("\n- ")
		out.WriteString(item.Name)
		if description := strings.Join(strings.Fields(item.Description), " "); description != "" {
			out.WriteString(": ")
			out.WriteString(description)
		}
	}
	return out.String()
}
