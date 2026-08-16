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

// MCPToolRunner provides access to tools exposed by stdio MCP servers (the
// cross-runtime plugin bridge).
type MCPToolRunner interface {
	ListTools(ctx context.Context) ([]mcp.Tool, error)
	CallTool(ctx context.Context, serverName, toolName string, args map[string]any) (*mcp.CallResult, error)
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

type listMCPToolsArgs struct{}

// NewListMCPToolsTool lists tools exposed by configured stdio MCP servers.
func NewListMCPToolsTool(runner MCPToolRunner) Tool {
	return NewTypedTool(NewToolSpec("list_mcp_tools", "List tools exposed by configured stdio MCP servers", map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
	}, nil), func(ctx context.Context, args listMCPToolsArgs) (ToolResult, error) {
		_ = args
		tools, err := runner.ListTools(ctx)
		if err != nil {
			return ToolResult{}, err
		}
		return ToolResult{Structured: map[string]interface{}{"tools": tools}}, nil
	})
}

type callMCPToolArgs struct {
	Server    string         `json:"server"`
	Tool      string         `json:"tool"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

// NewCallMCPToolTool calls one tool on a configured stdio MCP server.
func NewCallMCPToolTool(runner MCPToolRunner) Tool {
	return NewTypedTool(NewToolSpec("call_mcp_tool", "Call one tool on a configured stdio MCP server", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"server":    map[string]string{"type": "string"},
			"tool":      map[string]string{"type": "string"},
			"arguments": map[string]interface{}{"type": "object"},
		},
		"required": []string{"server", "tool"},
	}, nil), func(ctx context.Context, args callMCPToolArgs) (ToolResult, error) {
		if strings.TrimSpace(args.Server) == "" {
			return ToolResult{}, fmt.Errorf("missing server argument")
		}
		if strings.TrimSpace(args.Tool) == "" {
			return ToolResult{}, fmt.Errorf("missing tool argument")
		}
		result, err := runner.CallTool(ctx, args.Server, args.Tool, args.Arguments)
		if err != nil {
			return ToolResult{}, err
		}
		if result.IsError {
			return ToolResult{}, fmt.Errorf("mcp tool %s/%s failed: %s", args.Server, args.Tool, result.Text)
		}
		return ToolResult{Text: result.Text, Structured: result}, nil
	})
}
