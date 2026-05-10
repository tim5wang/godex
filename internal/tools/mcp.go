package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/tim5wang/godex/internal/core/mcp"
)

// MCPReader provides read-only access to configured MCP resources.
type MCPReader interface {
	ListResources() ([]mcp.Resource, error)
	ReadResource(serverName, uri string) (*mcp.ReadResult, error)
}

type listMCPResourcesArgs struct{}

type readMCPResourceArgs struct {
	Server string `json:"server"`
	URI    string `json:"uri"`
}

// NewListMCPResourcesTool creates a new MCP resource listing tool.
func NewListMCPResourcesTool(reader MCPReader) Tool {
	return NewTypedTool(NewToolSpec("list_mcp_resources", "List resources from configured read-only MCP servers", map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
	}, nil), func(ctx context.Context, args listMCPResourcesArgs) (ToolResult, error) {
		_ = ctx
		resources, err := reader.ListResources()
		if err != nil {
			return ToolResult{}, err
		}
		return ToolResult{Structured: map[string]interface{}{"resources": resources}}, nil
	})
}

// NewReadMCPResourceTool creates a new MCP resource read tool.
func NewReadMCPResourceTool(reader MCPReader) Tool {
	return NewTypedTool(NewToolSpec("read_mcp_resource", "Read one resource from a configured read-only MCP server", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"server": map[string]string{"type": "string"},
			"uri":    map[string]string{"type": "string"},
		},
		"required": []string{"server", "uri"},
	}, nil), func(ctx context.Context, args readMCPResourceArgs) (ToolResult, error) {
		_ = ctx
		if strings.TrimSpace(args.Server) == "" {
			return ToolResult{}, fmt.Errorf("missing server argument")
		}
		if strings.TrimSpace(args.URI) == "" {
			return ToolResult{}, fmt.Errorf("missing uri argument")
		}
		result, err := reader.ReadResource(args.Server, args.URI)
		if err != nil {
			return ToolResult{}, err
		}
		return ToolResult{Structured: result}, nil
	})
}
