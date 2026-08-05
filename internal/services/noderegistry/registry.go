package noderegistry

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tim5wang/godex/internal/core/config"
)

const (
	StatusOnline  = "online"
	StatusOffline = "offline"
)

type NodeInput struct {
	ID           string            `json:"id"`
	Name         string            `json:"name,omitempty"`
	Endpoint     string            `json:"endpoint,omitempty"`
	WorkspaceDir string            `json:"workspace_dir,omitempty"`
	GodexHome    string            `json:"godex_home,omitempty"`
	Version      string            `json:"version,omitempty"`
	Capabilities []string          `json:"capabilities,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
	TrustLevel   string            `json:"trust_level,omitempty"`
}

type NodeView struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Endpoint      string            `json:"endpoint,omitempty"`
	WorkspaceDir  string            `json:"workspace_dir,omitempty"`
	GodexHome     string            `json:"godex_home,omitempty"`
	Status        string            `json:"status"`
	Version       string            `json:"version,omitempty"`
	Capabilities  []string          `json:"capabilities,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
	LastSeen      time.Time         `json:"last_seen,omitempty"`
	RegisteredAt  time.Time         `json:"registered_at,omitempty"`
	UpdatedAt     time.Time         `json:"updated_at,omitempty"`
	Source        string            `json:"source,omitempty"`
	CredentialHash string           `json:"credential_hash,omitempty"`
	RelayStatus   string            `json:"relay_status,omitempty"`
	LastHealth    time.Time         `json:"last_health,omitempty"`
	TrustLevel    string            `json:"trust_level,omitempty"`
}

type Registry struct {
	mu           sync.Mutex
	path         string
	offlineAfter time.Duration
	now          func() time.Time
	nodes        map[string]NodeView
}

func New(path string, offlineAfter time.Duration) (*Registry, error) {
	if offlineAfter <= 0 {
		offlineAfter = 60 * time.Second
	}
	r := &Registry{
		path:         path,
		offlineAfter: offlineAfter,
		now:          time.Now,
		nodes:        map[string]NodeView{},
	}
	if err := r.load(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Registry) SetNow(now func() time.Time) {
	if now == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.now = now
}

func (r *Registry) Register(ctx context.Context, input NodeInput) (NodeView, error) {
	_ = ctx
	input.ID = strings.TrimSpace(input.ID)
	if input.ID == "" {
		return NodeView{}, fmt.Errorf("missing node id")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	node := r.mergeLocked(input, now)
	node.Status = StatusOnline
	node.LastSeen = now
	node.Source = firstNonEmpty(node.Source, "registered")
	r.nodes[node.ID] = node
	return node, r.saveLocked()
}

func (r *Registry) Heartbeat(ctx context.Context, id string, input NodeInput) (NodeView, error) {
	_ = ctx
	id = strings.TrimSpace(id)
	if id == "" {
		return NodeView{}, fmt.Errorf("missing node id")
	}
	input.ID = id
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	node := r.mergeLocked(input, now)
	node.Status = StatusOnline
	node.LastSeen = now
	node.Source = firstNonEmpty(node.Source, "heartbeat")
	r.nodes[node.ID] = node
	return node, r.saveLocked()
}

func (r *Registry) SeedConfigured(ctx context.Context, inputs []NodeInput) error {
	_ = ctx
	if len(inputs) == 0 {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	for _, input := range inputs {
		input.ID = strings.TrimSpace(input.ID)
		if input.ID == "" {
			continue
		}
		node := r.mergeLocked(input, now)
		if node.LastSeen.IsZero() || now.Sub(node.LastSeen) > r.offlineAfter {
			node.Status = StatusOffline
		}
		node.Source = "config"
		r.nodes[node.ID] = node
	}
	return r.saveLocked()
}

// SetCredentialHash stores the hash of a per-node credential. The plaintext
// credential is never persisted; only the digest is kept for hello validation.
func (r *Registry) SetCredentialHash(ctx context.Context, id, hash string) error {
	_ = ctx
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("missing node id")
	}
	hash = strings.TrimSpace(hash)
	if hash == "" {
		return fmt.Errorf("missing credential hash")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	node, ok := r.nodes[id]
	if !ok {
		return os.ErrNotExist
	}
	node.CredentialHash = hash
	node.UpdatedAt = r.now()
	r.nodes[id] = node
	return r.saveLocked()
}

// SetRelayStatus updates the relay channel state and health timestamp for a node.
func (r *Registry) SetRelayStatus(ctx context.Context, id, status string) error {
	_ = ctx
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("missing node id")
	}
	status = strings.TrimSpace(status)
	if status == "" {
		return fmt.Errorf("missing relay status")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	node, ok := r.nodes[id]
	if !ok {
		return os.ErrNotExist
	}
	node.RelayStatus = status
	node.LastHealth = r.now()
	node.UpdatedAt = r.now()
	r.nodes[id] = node
	return r.saveLocked()
}

func (r *Registry) List(ctx context.Context) ([]NodeView, error) {
	_ = ctx
	r.mu.Lock()
	defer r.mu.Unlock()
	items := make([]NodeView, 0, len(r.nodes))
	for _, node := range r.nodes {
		items = append(items, r.viewLocked(node))
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Status != items[j].Status {
			return items[i].Status == StatusOnline
		}
		return strings.ToLower(items[i].Name) < strings.ToLower(items[j].Name)
	})
	return items, nil
}

func (r *Registry) Get(ctx context.Context, id string) (NodeView, error) {
	_ = ctx
	id = strings.TrimSpace(id)
	r.mu.Lock()
	defer r.mu.Unlock()
	node, ok := r.nodes[id]
	if !ok {
		return NodeView{}, os.ErrNotExist
	}
	return r.viewLocked(node), nil
}

func (r *Registry) mergeLocked(input NodeInput, now time.Time) NodeView {
	node := r.nodes[input.ID]
	if node.ID == "" {
		node.ID = input.ID
		node.RegisteredAt = now
	}
	node.UpdatedAt = now
	if v := strings.TrimSpace(input.Name); v != "" {
		node.Name = v
	}
	if node.Name == "" {
		node.Name = input.ID
	}
	if v := strings.TrimSpace(input.Endpoint); v != "" {
		node.Endpoint = v
	}
	if v := strings.TrimSpace(input.WorkspaceDir); v != "" {
		node.WorkspaceDir = v
	}
	if v := strings.TrimSpace(input.GodexHome); v != "" {
		node.GodexHome = v
	}
	if v := strings.TrimSpace(input.Version); v != "" {
		node.Version = v
	}
	if len(input.Capabilities) > 0 {
		node.Capabilities = cleanStrings(input.Capabilities)
	}
	if len(input.Metadata) > 0 {
		node.Metadata = cleanMap(input.Metadata)
	}
	if v := strings.TrimSpace(input.TrustLevel); v != "" {
		node.TrustLevel = v
	}
	return node
}

func (r *Registry) viewLocked(node NodeView) NodeView {
	if !node.LastSeen.IsZero() && r.now().Sub(node.LastSeen) > r.offlineAfter {
		node.Status = StatusOffline
	}
	if node.Status == "" {
		node.Status = StatusOffline
	}
	node.Capabilities = append([]string{}, node.Capabilities...)
	node.Metadata = cleanMap(node.Metadata)
	return node
}

func (r *Registry) load() error {
	if strings.TrimSpace(r.path) == "" {
		return nil
	}
	data, err := os.ReadFile(r.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var stored struct {
		Nodes []NodeView `json:"nodes"`
	}
	if err := json.Unmarshal(data, &stored); err != nil {
		return fmt.Errorf("parse node registry: %w", err)
	}
	for _, node := range stored.Nodes {
		node.ID = strings.TrimSpace(node.ID)
		if node.ID == "" {
			continue
		}
		if node.Name == "" {
			node.Name = node.ID
		}
		r.nodes[node.ID] = node
	}
	return nil
}

func (r *Registry) saveLocked() error {
	if strings.TrimSpace(r.path) == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(r.path), 0755); err != nil {
		return err
	}
	nodes := make([]NodeView, 0, len(r.nodes))
	for _, node := range r.nodes {
		nodes = append(nodes, node)
	}
	sort.SliceStable(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	payload, err := json.MarshalIndent(struct {
		Nodes []NodeView `json:"nodes"`
	}{Nodes: nodes}, "", "  ")
	if err != nil {
		return err
	}
	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, append(payload, '\n'), 0600); err != nil {
		return err
	}
	return os.Rename(tmp, r.path)
}

func EnsureNodeID(stateDir string) (string, error) {
	path := filepath.Join(stateDir, "node.json")
	data, err := os.ReadFile(path)
	if err == nil {
		var stored struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(data, &stored) == nil && strings.TrimSpace(stored.ID) != "" {
			return strings.TrimSpace(stored.ID), nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	id, err := randomID("node_")
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return "", err
	}
	payload, _ := json.MarshalIndent(map[string]string{"id": id}, "", "  ")
	if err := os.WriteFile(path, append(payload, '\n'), 0600); err != nil {
		return "", err
	}
	return id, nil
}

func SelfNode(cfg *config.Config, endpoint string) (NodeInput, error) {
	return SelfNodeWithVersion(cfg, endpoint, "dev")
}

func SelfNodeWithVersion(cfg *config.Config, endpoint, godexVersion string) (NodeInput, error) {
	id, err := EnsureNodeID(cfg.StateDir)
	if err != nil {
		return NodeInput{}, err
	}
	name := strings.TrimSpace(cfg.Control.NodeName)
	if name == "" {
		if host, hostErr := os.Hostname(); hostErr == nil {
			name = host
		}
	}
	if name == "" {
		name = filepath.Base(cfg.WorkspaceDir)
	}
	return NodeInput{
		ID:           id,
		Name:         name,
		Endpoint:     endpoint,
		WorkspaceDir: cfg.WorkspaceDir,
		GodexHome:    cfg.HomeDir,
		Version:      firstNonEmpty(godexVersion, "dev"),
		Capabilities: RuntimeCapabilities(cfg),
	}, nil
}

func ConfiguredNodes(items []config.ControlNodeConfig) []NodeInput {
	nodes := make([]NodeInput, 0, len(items))
	for _, item := range items {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			continue
		}
		nodes = append(nodes, NodeInput{
			ID:           id,
			Name:         item.Name,
			Endpoint:     item.Endpoint,
			WorkspaceDir: item.WorkspaceDir,
			GodexHome:    item.GodexHome,
			Version:      item.Version,
			Capabilities: append([]string{}, item.Capabilities...),
		})
	}
	return nodes
}

func RuntimeCapabilities(cfg *config.Config) []string {
	capabilities := []string{"chat", "sessions", "tools", "skills", "packages", "memory"}
	if cfg.Tools.Subagent.MaxBatchSize > 0 || cfg.Tools.Subagent.MaxConcurrentJobs > 0 {
		capabilities = append(capabilities, "subagent")
	}
	if cfg.Cron.Enabled || cfg.Heartbeat.Enabled {
		capabilities = append(capabilities, "automation")
	}
	if cfg.Tools.Browser.Enabled {
		capabilities = append(capabilities, "browser")
	}
	if cfg.Feishu.Enabled {
		capabilities = append(capabilities, "feishu")
	}
	if cfg.Weixin.Enabled {
		capabilities = append(capabilities, "weixin")
	}
	return cleanStrings(capabilities)
}

func EndpointForAddr(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return ""
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		if strings.HasPrefix(addr, "http://") || strings.HasPrefix(addr, "https://") {
			return addr
		}
		return "http://" + addr
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

func randomID(prefix string) (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(buf), nil
}

func cleanStrings(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func cleanMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		out[key] = strings.TrimSpace(value)
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
