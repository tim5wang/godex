package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/tim5wang/godex/internal/wasmrt"
)

// WasmPluginRunner is the interface a session provides for a loaded WASM tool
// plugin. It is implemented by the agent-side wasm plugin holder.
type WasmPluginRunner interface {
	// WasmPlugin returns the loaded runtime for the named plugin, or nil.
	WasmPlugin(id string) *wasmrt.Plugin
}

// NewWasmToolRunner builds a runner over a fixed plugin instance (used in
// tests and single-plugin setups).
func NewWasmToolRunner(plugin *wasmrt.Plugin) WasmPluginRunner {
	return &fixedWasmRunner{plugin: plugin}
}

type fixedWasmRunner struct {
	plugin *wasmrt.Plugin
}

func (r *fixedWasmRunner) WasmPlugin(id string) *wasmrt.Plugin {
	if id == "" || r.plugin == nil {
		return nil
	}
	return r.plugin
}

// WasmToolOptions controls how one WASM-declared tool is exposed.
type WasmToolOptions struct {
	// PluginID identifies the owning wasm plugin (used for diagnostics).
	PluginID string
}

// NewWasmCallTool creates a tool that calls one tool on a WASM plugin. The
// declared tool name is fixed into the spec; the caller passes the plugin's
// declared input schema arguments directly, which flow through verbatim.
func NewWasmCallTool(runner WasmPluginRunner, decl wasmrt.ToolDecl, opts WasmToolOptions) (Tool, error) {
	if runner == nil {
		return nil, fmt.Errorf("wasm tool %s: nil runner", decl.Name)
	}
	name := sanitizeWasmToolName(decl.Name)
	if name == "" {
		return nil, fmt.Errorf("wasm tool %s: invalid name", decl.Name)
	}
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
		description = fmt.Sprintf("WASM tool %s (plugin %s)", decl.Name, opts.PluginID)
	}
	return NewTypedTool(NewToolSpec(name, description, schema, nil), func(ctx context.Context, args map[string]any) (ToolResult, error) {
		plugin := runner.WasmPlugin(opts.PluginID)
		if plugin == nil {
			return ToolResult{}, fmt.Errorf("wasm plugin %s is not loaded", opts.PluginID)
		}
		result, err := plugin.CallTool(ctx, decl.Name, args)
		if err != nil {
			return ToolResult{}, err
		}
		return ToolResult{Structured: result, Text: stringifyWasmResult(result)}, nil
	}), nil
}

// stringifyWasmResult renders a wasm result for text channels.
func stringifyWasmResult(result any) string {
	switch typed := result.(type) {
	case string:
		return typed
	case nil:
		return ""
	default:
		if data, err := json.Marshal(typed); err == nil {
			return string(data)
		}
		return fmt.Sprintf("%v", typed)
	}
}

// sanitizeWasmToolName maps a plugin tool name to a safe tool identifier:
// lowercase, alphanumeric plus underscore, prefixed with wasm_ when the name
// would collide with builtin tool names or contain invalid characters.
func sanitizeWasmToolName(name string) string {
	out := make([]rune, 0, len(name)+5)
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
			out = append(out, r)
		case r >= 'A' && r <= 'Z':
			out = append(out, r-'A'+'a')
		case r >= '0' && r <= '9':
			out = append(out, r)
		case r == '_' || r == '-':
			out = append(out, '_')
		}
	}
	if len(out) == 0 {
		return ""
	}
	return string(out)
}
