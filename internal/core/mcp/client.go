package mcp

import (
	"bytes"
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
