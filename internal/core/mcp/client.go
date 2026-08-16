package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Resource describes one discoverable MCP resource.
type Resource struct {
	Server      string `json:"server"`
	URI         string `json:"uri"`
	Name        string `json:"name"`
	MIMEType    string `json:"mime_type,omitempty"`
	Description string `json:"description,omitempty"`
	Size        int64  `json:"size,omitempty"`
	Binary      bool   `json:"binary,omitempty"`
}

// ReadResult is the outcome of reading one resource.
type ReadResult struct {
	Server   string `json:"server"`
	URI      string `json:"uri"`
	MIMEType string `json:"mime_type,omitempty"`
	Binary   bool   `json:"binary,omitempty"`
	Text     string `json:"text,omitempty"`
	Path     string `json:"path,omitempty"`
	Summary  string `json:"summary,omitempty"`
}

// Tool is one tool exposed by a stdio MCP server.
type Tool struct {
	Server      string          `json:"server"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
}

// CallResult is the outcome of calling one stdio MCP tool.
type CallResult struct {
	Server  string `json:"server"`
	Tool    string `json:"tool"`
	Text    string `json:"text,omitempty"`
	IsError bool   `json:"is_error,omitempty"`
	Raw     string `json:"raw,omitempty"`
}

// Prompt is one prompt template exposed by a stdio MCP server.
type Prompt struct {
	Server      string          `json:"server"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Arguments   json.RawMessage `json:"arguments,omitempty"`
}

// PromptMessage is one message in a rendered prompt.
type PromptMessage struct {
	Role    string `json:"role"`
	Content string `json:"content,omitempty"`
}

// GetPromptResult is the outcome of rendering one MCP prompt.
type GetPromptResult struct {
	Server   string          `json:"server"`
	Prompt   string          `json:"prompt"`
	Messages []PromptMessage `json:"messages"`
	Raw      string          `json:"raw,omitempty"`
}

// Manager is the read-only MCP resource entrypoint.
type Manager struct {
	configPath   string
	workspaceDir string
	tempDir      string
}

// NewManager creates a new read-only MCP manager.
func NewManager(configPath, workspaceDir, tempDir string) *Manager {
	return &Manager{
		configPath:   configPath,
		workspaceDir: workspaceDir,
		tempDir:      tempDir,
	}
}

// HasConfiguredServers reports whether the local MCP config contains any servers.
func (m *Manager) HasConfiguredServers() bool {
	cfg, err := LoadConfig(m.configPath)
	return err == nil && len(cfg.Servers) > 0
}

// ListResources lists resources from all configured MCP servers.
func (m *Manager) ListResources() ([]Resource, error) {
	cfg, err := LoadConfig(m.configPath)
	if err != nil {
		return nil, err
	}

	resources := make([]Resource, 0)
	for _, server := range cfg.Servers {
		switch server.Type {
		case "", ServerTypeFilesystem:
			items, err := m.listFilesystemResources(server)
			if err != nil {
				return nil, err
			}
			resources = append(resources, items...)
		default:
			return nil, fmt.Errorf("unsupported MCP server type %q", server.Type)
		}
	}

	sort.Slice(resources, func(i, j int) bool {
		if resources[i].Server == resources[j].Server {
			return resources[i].URI < resources[j].URI
		}
		return resources[i].Server < resources[j].Server
	})
	return resources, nil
}

// ReadResource reads one configured resource by server and URI.
func (m *Manager) ReadResource(serverName, uri string) (*ReadResult, error) {
	cfg, err := LoadConfig(m.configPath)
	if err != nil {
		return nil, err
	}

	for _, server := range cfg.Servers {
		if server.Name != serverName {
			continue
		}
		switch server.Type {
		case "", ServerTypeFilesystem:
			return m.readFilesystemResource(server, uri)
		default:
			return nil, fmt.Errorf("unsupported MCP server type %q", server.Type)
		}
	}
	return nil, fmt.Errorf("mcp server not found: %s", serverName)
}

// ListTools lists tools exposed by all configured stdio MCP servers. Tools are
// discovered once per call via the MCP tools/list protocol.
func (m *Manager) ListTools(ctx context.Context) ([]Tool, error) {
	cfg, err := LoadConfig(m.configPath)
	if err != nil {
		return nil, err
	}
	var tools []Tool
	for _, server := range cfg.Servers {
		if server.Type != ServerTypeStdio {
			continue
		}
		items, err := m.listServerTools(ctx, server)
		if err != nil {
			return nil, err
		}
		tools = append(tools, items...)
	}
	sort.Slice(tools, func(i, j int) bool {
		if tools[i].Server == tools[j].Server {
			return tools[i].Name < tools[j].Name
		}
		return tools[i].Server < tools[j].Server
	})
	return tools, nil
}

// ListStdioServers returns the configured stdio MCP server names (used for
// per-server dynamic tool registration).
func (m *Manager) ListStdioServers() []string {
	cfg, err := LoadConfig(m.configPath)
	if err != nil {
		return nil
	}
	var names []string
	for _, server := range cfg.Servers {
		if server.Type != ServerTypeStdio {
			continue
		}
		names = append(names, server.Name)
	}
	sort.Strings(names)
	return names
}

// ListServerTools lists the tools of one stdio server by name.
func (m *Manager) ListServerTools(ctx context.Context, serverName string) ([]Tool, error) {
	cfg, err := LoadConfig(m.configPath)
	if err != nil {
		return nil, err
	}
	for _, server := range cfg.Servers {
		if server.Name != serverName {
			continue
		}
		if server.Type != ServerTypeStdio {
			return nil, fmt.Errorf("mcp server %s is not a stdio server", serverName)
		}
		return m.listServerTools(ctx, server)
	}
	return nil, fmt.Errorf("mcp server not found: %s", serverName)
}

// CallTool calls one tool on a stdio MCP server via the MCP tools/call
// protocol. Text content is concatenated; structured content is preserved raw.
func (m *Manager) CallTool(ctx context.Context, serverName, toolName string, args map[string]any) (*CallResult, error) {
	cfg, err := LoadConfig(m.configPath)
	if err != nil {
		return nil, err
	}
	for _, server := range cfg.Servers {
		if server.Name != serverName {
			continue
		}
		if server.Type != ServerTypeStdio {
			return nil, fmt.Errorf("mcp server %s is not a stdio server", serverName)
		}
		client, err := startStdioClient(ctx, server)
		if err != nil {
			return nil, err
		}
		defer client.close()
		result, err := client.callTool(ctx, toolName, args)
		if err != nil {
			return nil, err
		}
		text := ""
		for _, content := range result.Content {
			if content.Type == "text" {
				if text != "" {
					text += "\n"
				}
				text += content.Text
			}
		}
		raw, _ := json.Marshal(result)
		return &CallResult{
			Server:  serverName,
			Tool:    toolName,
			Text:    text,
			IsError: result.IsError,
			Raw:     string(raw),
		}, nil
	}
	return nil, fmt.Errorf("mcp server not found: %s", serverName)
}

// listServerTools lists the tools of one stdio server.
func (m *Manager) listServerTools(ctx context.Context, server ServerConfig) ([]Tool, error) {
	client, err := startStdioClient(ctx, server)
	if err != nil {
		return nil, err
	}
	defer client.close()
	items, err := client.listTools(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Tool, 0, len(items))
	for _, item := range items {
		out = append(out, Tool{
			Server:      server.Name,
			Name:        item.Name,
			Description: item.Description,
			InputSchema: item.InputSchema,
		})
	}
	return out, nil
}

// ListPrompts lists prompts exposed by all configured stdio MCP servers via
// the MCP prompts/list protocol.
func (m *Manager) ListPrompts(ctx context.Context) ([]Prompt, error) {
	cfg, err := LoadConfig(m.configPath)
	if err != nil {
		return nil, err
	}
	var prompts []Prompt
	for _, server := range cfg.Servers {
		if server.Type != ServerTypeStdio {
			continue
		}
		items, err := m.listServerPrompts(ctx, server)
		if err != nil {
			return nil, err
		}
		prompts = append(prompts, items...)
	}
	sort.Slice(prompts, func(i, j int) bool {
		if prompts[i].Server == prompts[j].Server {
			return prompts[i].Name < prompts[j].Name
		}
		return prompts[i].Server < prompts[j].Server
	})
	return prompts, nil
}

// GetPrompt renders one prompt on a stdio MCP server via the MCP prompts/get
// protocol. Text content is concatenated per message; raw output preserved.
func (m *Manager) GetPrompt(ctx context.Context, serverName, promptName string, arguments map[string]any) (*GetPromptResult, error) {
	cfg, err := LoadConfig(m.configPath)
	if err != nil {
		return nil, err
	}
	for _, server := range cfg.Servers {
		if server.Name != serverName {
			continue
		}
		if server.Type != ServerTypeStdio {
			return nil, fmt.Errorf("mcp server %s is not a stdio server", serverName)
		}
		client, err := startStdioClient(ctx, server)
		if err != nil {
			return nil, err
		}
		defer client.close()
		messages, err := client.getPrompt(ctx, promptName, arguments)
		if err != nil {
			return nil, err
		}
		rendered := make([]PromptMessage, 0, len(messages))
		for _, msg := range messages {
			content := ""
			var text struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(msg.Content, &text); err == nil {
				content = text.Text
			}
			rendered = append(rendered, PromptMessage{Role: msg.Role, Content: content})
		}
		raw, _ := json.Marshal(messages)
		return &GetPromptResult{
			Server:   serverName,
			Prompt:   promptName,
			Messages: rendered,
			Raw:      string(raw),
		}, nil
	}
	return nil, fmt.Errorf("mcp server not found: %s", serverName)
}

// listServerPrompts lists the prompts of one stdio server.
func (m *Manager) listServerPrompts(ctx context.Context, server ServerConfig) ([]Prompt, error) {
	client, err := startStdioClient(ctx, server)
	if err != nil {
		return nil, err
	}
	defer client.close()
	items, err := client.listPrompts(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Prompt, 0, len(items))
	for _, item := range items {
		out = append(out, Prompt{
			Server:      server.Name,
			Name:        item.Name,
			Description: item.Description,
			Arguments:   item.Arguments,
		})
	}
	return out, nil
}

func (m *Manager) listFilesystemResources(server ServerConfig) ([]Resource, error) {
	root, err := resolveServerRoot(server.Root, m.workspaceDir)
	if err != nil {
		return nil, err
	}
	items := make([]Resource, 0, 32)
	if err := filepath.WalkDir(root, func(filePath string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			if filePath != root && strings.HasPrefix(entry.Name(), ".godex") {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, filePath)
		if err != nil {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		mimeType := detectMIMEType(filePath)
		items = append(items, Resource{
			Server:      server.Name,
			URI:         filepath.ToSlash(rel),
			Name:        entry.Name(),
			MIMEType:    mimeType,
			Description: fmt.Sprintf("%s resource from %s", mimeType, server.Name),
			Size:        info.Size(),
			Binary:      isBinaryMIME(mimeType),
		})
		return nil
	}); err != nil {
		return nil, err
	}
	return items, nil
}

func (m *Manager) readFilesystemResource(server ServerConfig, uri string) (*ReadResult, error) {
	root, err := resolveServerRoot(server.Root, m.workspaceDir)
	if err != nil {
		return nil, err
	}
	fullPath := filepath.Join(root, filepath.FromSlash(uri))
	info, err := os.Stat(fullPath)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("resource %q is a directory", uri)
	}

	data, err := os.ReadFile(fullPath)
	if err != nil {
		return nil, err
	}

	mimeType := detectMIMEType(fullPath)
	if isBinaryBytes(data, mimeType) {
		targetDir := filepath.Join(m.tempDir, "mcp", server.Name)
		if err := os.MkdirAll(targetDir, 0755); err != nil {
			return nil, err
		}
		targetPath := filepath.Join(targetDir, filepath.Base(fullPath))
		if err := os.WriteFile(targetPath, data, 0644); err != nil {
			return nil, err
		}
		return &ReadResult{
			Server:   server.Name,
			URI:      uri,
			MIMEType: mimeType,
			Binary:   true,
			Path:     targetPath,
			Summary:  fmt.Sprintf("Binary resource copied to %s", targetPath),
		}, nil
	}

	return &ReadResult{
		Server:   server.Name,
		URI:      uri,
		MIMEType: mimeType,
		Binary:   false,
		Text:     string(data),
		Summary:  fmt.Sprintf("Loaded text resource %s from %s", uri, server.Name),
	}, nil
}

func resolveServerRoot(root, workspaceDir string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", fmt.Errorf("missing MCP server root")
	}
	if filepath.IsAbs(root) {
		return root, nil
	}
	if workspaceDir == "" {
		return "", fmt.Errorf("relative MCP server root requires workspace dir")
	}
	return filepath.Join(workspaceDir, filepath.FromSlash(root)), nil
}

func detectMIMEType(filePath string) string {
	switch strings.ToLower(filepath.Ext(filePath)) {
	case ".md":
		return "text/markdown"
	case ".txt", ".log", ".json", ".yaml", ".yml", ".xml", ".go", ".js", ".ts", ".tsx", ".jsx", ".css", ".html", ".sh":
		return "text/plain"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".pdf":
		return "application/pdf"
	default:
		return "application/octet-stream"
	}
}

func isBinaryMIME(mimeType string) bool {
	return !strings.HasPrefix(mimeType, "text/")
}

func isBinaryBytes(data []byte, mimeType string) bool {
	if !isBinaryMIME(mimeType) {
		return false
	}
	if len(data) == 0 {
		return false
	}
	return bytes.IndexByte(data, 0) >= 0 || mimeType != "application/octet-stream"
}

// MarshalConfig is a small helper for tests and fixtures.
func MarshalConfig(cfg Config) ([]byte, error) {
	return json.MarshalIndent(cfg, "", "  ")
}
