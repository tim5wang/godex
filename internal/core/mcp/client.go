package mcp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
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

// toolsListCacheTTL bounds how long a per-server tools/list result is reused
// without re-probing the server. Session open and tool registration call
// tools/list on every open; caching the (rarely changing) tool declarations
// avoids cold-starting a stdio daemon on every session.
const toolsListCacheTTL = 5 * time.Minute

// clientIdleTTL bounds how long a persistent MCP client is kept alive after
// its last use. stdio daemons (e.g. codebase-memory-mcp) exit when their last
// client disconnects, so godex closes idle clients to let them stop and free
// memory while keeping them warm across bursts of activity.
const clientIdleTTL = 90 * time.Second

// toolsCacheEntry is one cached tools/list result for a server.
type toolsCacheEntry struct {
	tools     []Tool
	fetchedAt time.Time
	// sig is the server config signature the tools were fetched under; a
	// config change makes the entry a cache miss automatically.
	sig string
}

// clientEntry is a persistent per-server rpcClient shared across calls. mu
// serializes operations on the underlying client so concurrent callers never
// interleave writes on the same stdio pipe.
type clientEntry struct {
	sig    string
	client rpcClient
	// lastUsed is the unix-nano timestamp of the last completed operation.
	lastUsed atomic.Int64
	mu       sync.Mutex
}

// Manager is the read-only MCP resource entrypoint.
type Manager struct {
	configPath   string
	workspaceDir string
	tempDir      string

	mu sync.RWMutex
	// toolsCache caches the tools/list result per server (see
	// toolsListCacheTTL) so session open / tool registration do not spawn a
	// stdio daemon on every call.
	toolsCache map[string]*toolsCacheEntry
	// clients holds persistent per-server rpcClient instances (see
	// clientIdleTTL) so a stdio daemon stays warm across tool calls instead
	// of being spawned and torn down per request.
	clients map[string]*clientEntry
}

// NewManager creates a new read-only MCP manager.
func NewManager(configPath, workspaceDir, tempDir string) *Manager {
	return &Manager{
		configPath:   configPath,
		workspaceDir: workspaceDir,
		tempDir:      tempDir,
	}
}

// serverSignature returns a stable hash of the transport-relevant fields of a
// ServerConfig. It is used to invalidate the tools/list cache and the
// persistent client pool when a server's configuration changes.
func serverSignature(server ServerConfig) string {
	h := sha256.New()
	io.WriteString(h, server.Name)
	io.WriteString(h, "\x00"+server.Type)
	io.WriteString(h, "\x00"+server.Command)
	for _, arg := range server.Args {
		io.WriteString(h, "\x00arg:"+arg)
	}
	envKeys := make([]string, 0, len(server.Env))
	for k := range server.Env {
		envKeys = append(envKeys, k)
	}
	sort.Strings(envKeys)
	for _, k := range envKeys {
		io.WriteString(h, "\x00env:"+k+"="+server.Env[k])
	}
	io.WriteString(h, "\x00url:"+server.URL)
	hdrKeys := make([]string, 0, len(server.Headers))
	for k := range server.Headers {
		hdrKeys = append(hdrKeys, k)
	}
	sort.Strings(hdrKeys)
	for _, k := range hdrKeys {
		io.WriteString(h, "\x00hdr:"+k+"="+server.Headers[k])
	}
	io.WriteString(h, "\x00root:"+server.Root)
	return fmt.Sprintf("%x", h.Sum(nil))
}

// acquireClient returns the persistent client for server, creating one when
// none exists (or the existing one is stale: config changed or idle past
// clientIdleTTL). The returned client is owned by the Manager until
// invalidateClient closes it; callers serialize operations via entry.mu.
func (m *Manager) acquireClient(ctx context.Context, server ServerConfig) (*clientEntry, error) {
	sig := serverSignature(server)
	now := time.Now()

	m.mu.RLock()
	e := m.clients[server.Name]
	m.mu.RUnlock()
	if e != nil && e.sig == sig && now.Sub(time.Unix(0, e.lastUsed.Load())) < clientIdleTTL {
		e.lastUsed.Store(now.UnixNano())
		return e, nil
	}

	// Slow path: create outside the manager lock (a cold stdio start can take
	// seconds and must not block other Manager operations).
	client, err := clientFor(ctx, server)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	// Another goroutine may have created a matching, non-idle entry meanwhile;
	// prefer it and discard ours.
	if existing, ok := m.clients[server.Name]; ok && existing.sig == sig && time.Since(time.Unix(0, existing.lastUsed.Load())) < clientIdleTTL {
		m.mu.Unlock()
		_ = client.close()
		existing.lastUsed.Store(time.Now().UnixNano())
		return existing, nil
	}
	if old, ok := m.clients[server.Name]; ok {
		delete(m.clients, server.Name)
		go old.client.close()
	}
	if m.clients == nil {
		m.clients = map[string]*clientEntry{}
	}
	e = &clientEntry{sig: sig, client: client}
	e.lastUsed.Store(now.UnixNano())
	m.clients[server.Name] = e
	m.mu.Unlock()
	return e, nil
}

// withClient runs fn against the persistent client for server, serialized per
// server. If fn fails because the underlying connection is dead
// (errConnClosed), the stale client is dropped so the next call recreates it;
// application-level JSON-RPC errors leave the connection intact.
func withClient[T any](m *Manager, ctx context.Context, server ServerConfig, fn func(rpcClient) (T, error)) (T, error) {
	var zero T
	e, err := m.acquireClient(ctx, server)
	if err != nil {
		return zero, err
	}
	e.mu.Lock()
	result, opErr := fn(e.client)
	if opErr == nil {
		e.lastUsed.Store(time.Now().UnixNano())
	}
	e.mu.Unlock()
	invalidated := false
	if opErr != nil && errors.Is(opErr, errConnClosed) {
		invalidated = m.invalidateClient(server.Name, e)
	}
	// If this entry was replaced while the operation ran (rare: a concurrent
	// caller created a fresh client after this one looked idle), close the
	// orphaned client so its daemon does not leak. When invalidateClient
	// already removed and closed this entry, there is nothing left to close.
	m.mu.RLock()
	current := m.clients[server.Name]
	m.mu.RUnlock()
	if !invalidated && current != e {
		go e.client.close()
	}
	return result, opErr
}

// invalidateClient closes and removes the persistent client for server, but
// only when the current entry is still e. This lets a caller drop a client
// that it knows is dead without racing a concurrent caller that may have
// replaced the entry with a fresh one. It reports whether the entry was
// actually removed (and thus closed). Callers may hold no manager lock.
func (m *Manager) invalidateClient(name string, e *clientEntry) bool {
	m.mu.Lock()
	current, ok := m.clients[name]
	if !ok || current != e {
		m.mu.Unlock()
		return false
	}
	delete(m.clients, name)
	delete(m.toolsCache, name)
	go e.client.close()
	m.mu.Unlock()
	return true
}

// invalidateServerLocked drops the tools cache and closes the persistent
// client for one server. The manager lock must be held.
func (m *Manager) invalidateServerLocked(name string) {
	delete(m.toolsCache, name)
	if e, ok := m.clients[name]; ok {
		delete(m.clients, name)
		go e.client.close()
	}
}

// Close closes every persistent MCP client (stdio processes / http clients),
// letting daemons that stop on last-committed-client-disconnect exit. It is
// safe to call multiple times and while operations are in flight; in-flight
// operations finish against the client they already hold.
func (m *Manager) Close() {
	m.mu.Lock()
	for name, e := range m.clients {
		delete(m.clients, name)
		go e.client.close()
	}
	m.mu.Unlock()
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

// ListTools lists tools exposed by all configured MCP servers (stdio or
// streamable-http). Tools are discovered once per call via the MCP tools/list
// protocol.
func (m *Manager) ListTools(ctx context.Context) ([]Tool, error) {
	cfg, err := LoadConfig(m.configPath)
	if err != nil {
		return nil, err
	}
	var tools []Tool
	for _, server := range cfg.Servers {
		if server.Type != ServerTypeStdio && server.Type != ServerTypeHTTP {
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

// ListToolServers returns the configured MCP server names that expose tools
// (stdio or streamable-http), used for per-server dynamic tool registration.
func (m *Manager) ListToolServers() []string {
	cfg, err := LoadConfig(m.configPath)
	if err != nil {
		return nil
	}
	var names []string
	for _, server := range cfg.Servers {
		if server.Type != ServerTypeStdio && server.Type != ServerTypeHTTP {
			continue
		}
		names = append(names, server.Name)
	}
	sort.Strings(names)
	return names
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

// rpcClient is the common JSON-RPC surface shared by both MCP transports
// (stdio process and remote Streamable HTTP). Manager dispatches to the right
// transport via clientFor.
type rpcClient interface {
	initialize(ctx context.Context) (string, error)
	listTools(ctx context.Context) ([]mcpTool, error)
	callTool(ctx context.Context, name string, args map[string]any) (mcpCallResult, error)
	listPrompts(ctx context.Context) ([]mcpPrompt, error)
	getPrompt(ctx context.Context, name string, arguments map[string]any) ([]mcpPromptMessage, error)
	close() error
}

// clientFor returns the rpcClient for the given server config.
func clientFor(ctx context.Context, server ServerConfig) (rpcClient, error) {
	switch server.Type {
	case ServerTypeStdio:
		return startStdioClient(ctx, server)
	case ServerTypeHTTP:
		return startHTTPClient(server)
	default:
		return nil, fmt.Errorf("unsupported MCP server type %q", server.Type)
	}
}

// ListServerTools lists the tools of one MCP server by name (stdio or
// streamable-http).
func (m *Manager) ListServerTools(ctx context.Context, serverName string) ([]Tool, error) {
	cfg, err := LoadConfig(m.configPath)
	if err != nil {
		return nil, err
	}
	for _, server := range cfg.Servers {
		if server.Name != serverName {
			continue
		}
		return m.listServerTools(ctx, server)
	}
	return nil, fmt.Errorf("mcp server not found: %s", serverName)
}

// CallTool calls one tool on an MCP server (stdio or streamable-http) via the
// MCP tools/call protocol. Text content is concatenated; structured content is
// preserved raw. The underlying client is reused across calls (see
// clientIdleTTL) so a stdio daemon stays warm instead of cold-starting per call.
func (m *Manager) CallTool(ctx context.Context, serverName, toolName string, args map[string]any) (*CallResult, error) {
	cfg, err := LoadConfig(m.configPath)
	if err != nil {
		return nil, err
	}
	for _, server := range cfg.Servers {
		if server.Name != serverName {
			continue
		}
		return withClient(m, ctx, server, func(c rpcClient) (*CallResult, error) {
			result, err := c.callTool(ctx, toolName, args)
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
		})
	}
	return nil, fmt.Errorf("mcp server not found: %s", serverName)
}

// listServerTools lists the tools of one server (stdio or streamable-http).
// The result is cached per server (see toolsListCacheTTL) so repeated session
// opens / tool registrations do not spawn a stdio daemon on every call; the
// cache is keyed by the server config signature so a config change refetches.
func (m *Manager) listServerTools(ctx context.Context, server ServerConfig) ([]Tool, error) {
	sig := serverSignature(server)
	m.mu.RLock()
	entry := m.toolsCache[server.Name]
	m.mu.RUnlock()
	if entry != nil && entry.sig == sig && time.Since(entry.fetchedAt) < toolsListCacheTTL {
		out := make([]Tool, len(entry.tools))
		copy(out, entry.tools)
		return out, nil
	}

	items, err := withClient(m, ctx, server, func(c rpcClient) ([]Tool, error) {
		raw, err := c.listTools(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]Tool, 0, len(raw))
		for _, item := range raw {
			out = append(out, Tool{
				Server:      server.Name,
				Name:        item.Name,
				Description: item.Description,
				InputSchema: append(json.RawMessage(nil), item.InputSchema...),
			})
		}
		return out, nil
	})
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	if m.toolsCache == nil {
		m.toolsCache = map[string]*toolsCacheEntry{}
	}
	m.toolsCache[server.Name] = &toolsCacheEntry{tools: items, fetchedAt: time.Now(), sig: sig}
	m.mu.Unlock()
	return items, nil
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
		return withClient(m, ctx, server, func(c rpcClient) (*GetPromptResult, error) {
			messages, err := c.getPrompt(ctx, promptName, arguments)
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
		})
	}
	return nil, fmt.Errorf("mcp server not found: %s", serverName)
}

// listServerPrompts lists the prompts of one stdio server.
func (m *Manager) listServerPrompts(ctx context.Context, server ServerConfig) ([]Prompt, error) {
	return withClient(m, ctx, server, func(c rpcClient) ([]Prompt, error) {
		items, err := c.listPrompts(ctx)
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
	})
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
	fullPath, err := resolveFilesystemResourcePath(root, uri)
	if err != nil {
		return nil, err
	}
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

func resolveFilesystemResourcePath(root, uri string) (string, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if filepath.IsAbs(filepath.FromSlash(uri)) {
		return "", fmt.Errorf("resource %q escapes MCP filesystem root", uri)
	}
	candidate := filepath.Join(rootAbs, filepath.Clean(filepath.FromSlash(uri)))
	rel, err := filepath.Rel(rootAbs, candidate)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("resource %q escapes MCP filesystem root", uri)
	}
	resolvedRoot, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", err
	}
	resolvedRel, err := filepath.Rel(resolvedRoot, resolved)
	if err != nil || resolvedRel == ".." || strings.HasPrefix(resolvedRel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("resource %q escapes MCP filesystem root through symbolic link", uri)
	}
	return candidate, nil
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
