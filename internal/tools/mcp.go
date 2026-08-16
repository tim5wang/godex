package tools

import (
	"context"
	"encoding/json"
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

// MCPPromptRunner provides access to prompts exposed by stdio MCP servers.
type MCPPromptRunner interface {
	ListPrompts(ctx context.Context) ([]mcp.Prompt, error)
	GetPrompt(ctx context.Context, serverName, promptName string, arguments map[string]any) (*mcp.GetPromptResult, error)
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

type listMCPPromptsArgs struct{}

// NewListMCPPromptsTool lists prompts exposed by configured stdio MCP servers.
func NewListMCPPromptsTool(runner MCPPromptRunner) Tool {
	return NewTypedTool(NewToolSpec("list_mcp_prompts", "List prompts exposed by configured stdio MCP servers", map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
	}, nil), func(ctx context.Context, args listMCPPromptsArgs) (ToolResult, error) {
		_ = args
		prompts, err := runner.ListPrompts(ctx)
		if err != nil {
			return ToolResult{}, err
		}
		return ToolResult{Structured: map[string]interface{}{"prompts": prompts}}, nil
	})
}

type getMCPPromptArgs struct {
	Server    string         `json:"server"`
	Prompt    string         `json:"prompt"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

// NewGetMCPPromptTool renders one prompt on a configured stdio MCP server.
func NewGetMCPPromptTool(runner MCPPromptRunner) Tool {
	return NewTypedTool(NewToolSpec("get_mcp_prompt", "Render one prompt on a configured stdio MCP server", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"server":    map[string]string{"type": "string"},
			"prompt":    map[string]string{"type": "string"},
			"arguments": map[string]interface{}{"type": "object"},
		},
		"required": []string{"server", "prompt"},
	}, nil), func(ctx context.Context, args getMCPPromptArgs) (ToolResult, error) {
		if strings.TrimSpace(args.Server) == "" {
			return ToolResult{}, fmt.Errorf("missing server argument")
		}
		if strings.TrimSpace(args.Prompt) == "" {
			return ToolResult{}, fmt.Errorf("missing prompt argument")
		}
		result, err := runner.GetPrompt(ctx, args.Server, args.Prompt, args.Arguments)
		if err != nil {
			return ToolResult{}, err
		}
		var b strings.Builder
		for _, msg := range result.Messages {
			if msg.Content == "" {
				continue
			}
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(msg.Content)
		}
		return ToolResult{Text: b.String(), Structured: result}, nil
	})
}

// MCPServerCaller calls tools on one specific stdio MCP server.
type MCPServerCaller interface {
	CallTool(ctx context.Context, serverName, toolName string, args map[string]any) (*mcp.CallResult, error)
}

// NewMCPServerTool creates a first-class tool bound to one stdio MCP server:
// the declared tool name (namespaced as <server>__<tool>) calls through the
// manager to that server. This is the §5.2 'dynamic per-server tool
// registration' path — MCP tools appear directly in the catalog with the
// server as owner instead of only via the generic list/call bridge.
func NewMCPServerTool(caller MCPServerCaller, serverName string, decl mcp.Tool) (Tool, error) {
	if caller == nil {
		return nil, fmt.Errorf("mcp tool %s/%s: nil caller", serverName, decl.Name)
	}
	if strings.TrimSpace(serverName) == "" || strings.TrimSpace(decl.Name) == "" {
		return nil, fmt.Errorf("mcp tool: missing server or tool name")
	}
	name := mcpToolName(serverName, decl.Name)
	schema := map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
	}
	if len(decl.InputSchema) > 0 {
		var parsed map[string]interface{}
		if err := json.Unmarshal(decl.InputSchema, &parsed); err == nil {
			if props, ok := parsed["properties"].(map[string]interface{}); ok {
				schema["properties"] = props
			}
			if req, ok := parsed["required"]; ok {
				schema["required"] = req
			}
		}
	}
	description := decl.Description
	if description == "" {
		description = fmt.Sprintf("MCP tool %s from server %s", decl.Name, serverName)
	}
	return NewTypedTool(NewToolSpec(name, description, schema, nil), func(ctx context.Context, args map[string]any) (ToolResult, error) {
		result, err := caller.CallTool(ctx, serverName, decl.Name, args)
		if err != nil {
			return ToolResult{}, err
		}
		if result.IsError {
			return ToolResult{}, fmt.Errorf("mcp tool %s/%s failed: %s", serverName, decl.Name, result.Text)
		}
		return ToolResult{Text: result.Text, Structured: result}, nil
	}), nil
}

// mcpToolName namespaces an MCP tool as <server>__<tool> so distinct servers
// cannot collide in the shared tool catalog.
func mcpToolName(serverName, toolName string) string {
	safeServer := sanitizeWasmToolName(serverName)
	safeTool := sanitizeWasmToolName(toolName)
	if safeServer == "" {
		return safeTool
	}
	if safeTool == "" {
		return safeServer
	}
	return safeServer + "__" + safeTool
}
