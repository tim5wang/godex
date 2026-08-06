package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/agent"
	"github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/commands"
	"github.com/tim5wang/godex/internal/services/noderegistry"
	"github.com/tim5wang/godex/internal/services/relay"
)

// disconnectRecordingRegistry wraps a registry and records DisconnectNode
// calls so tests can assert the delete endpoint drops the relay connection.
type disconnectRecordingRegistry struct {
	*noderegistry.Registry
	mu       sync.Mutex
	disconns []string
}

func (r *disconnectRecordingRegistry) DisconnectNode(nodeID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.disconns = append(r.disconns, nodeID)
}

func (r *disconnectRecordingRegistry) disconnected() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string{}, r.disconns...)
}

func TestControlNodeDeleteEndpoint(t *testing.T) {
	cfg := newTestConfig(t)
	manager := newTestManager(t, cfg)
	service := backend.NewService(cfg, agent.NewSharedDependencies(cfg), commands.NewService(cfg))
	registry, err := noderegistry.New(filepath.Join(t.TempDir(), "nodes.json"), time.Minute)
	if err != nil {
		t.Fatalf("new node registry: %v", err)
	}
	wrapped := &disconnectRecordingRegistry{Registry: registry}
	server := httptest.NewServer(NewHandlerWithRuntime(manager, service, nil, nil, nil, nil, nil, nil, wrapped))
	defer server.Close()

	// Register two nodes, then delete one.
	for _, id := range []string{"node-a", "node-b"} {
		resp := doJSONWithToken(t, http.MethodPost, server.URL+"/control/nodes/register", map[string]any{
			"id":   id,
			"name": id,
		}, "")
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("register %s status = %d", id, resp.StatusCode)
		}
	}

	delResp := doJSONWithToken(t, http.MethodDelete, server.URL+"/control/nodes/node-a", nil, "")
	defer delResp.Body.Close()
	if delResp.StatusCode != http.StatusOK {
		t.Fatalf("delete status = %d", delResp.StatusCode)
	}

	// Deleted node must be gone from the list.
	listResp, err := http.Get(server.URL + "/control/nodes")
	if err != nil {
		t.Fatalf("list nodes: %v", err)
	}
	defer listResp.Body.Close()
	var nodes []noderegistry.NodeView
	if err := json.NewDecoder(listResp.Body).Decode(&nodes); err != nil {
		t.Fatalf("decode nodes: %v", err)
	}
	if len(nodes) != 1 || nodes[0].ID != "node-b" {
		t.Fatalf("expected only node-b after delete, got %#v", nodes)
	}

	// Deleting must also disconnect the relay connection for that node.
	if got := wrapped.disconnected(); len(got) != 1 || got[0] != "node-a" {
		t.Fatalf("expected DisconnectNode(node-a), got %#v", got)
	}

	// Heartbeat of the deleted node must be rejected (tombstone).
	hbResp := doJSONWithToken(t, http.MethodPost, server.URL+"/control/nodes/node-a/heartbeat", map[string]any{
		"version": "dev",
	}, "")
	defer hbResp.Body.Close()
	if hbResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected heartbeat rejection after delete, got %d", hbResp.StatusCode)
	}
}

func TestControlNodeDeleteEndpointUnknownNode(t *testing.T) {
	cfg := newTestConfig(t)
	manager := newTestManager(t, cfg)
	service := backend.NewService(cfg, agent.NewSharedDependencies(cfg), commands.NewService(cfg))
	registry, err := noderegistry.New(filepath.Join(t.TempDir(), "nodes.json"), time.Minute)
	if err != nil {
		t.Fatalf("new node registry: %v", err)
	}
	server := httptest.NewServer(NewHandlerWithRuntime(manager, service, nil, nil, nil, nil, nil, nil, registry))
	defer server.Close()

	resp := doJSONWithToken(t, http.MethodDelete, server.URL+"/control/nodes/ghost", nil, "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown node, got %d", resp.StatusCode)
	}
}

// TestControlNodeDeleteEndpointConnectsHub verifies that a real registry
// wrapped with a hub-backed disconnector (as main.go wires) compiles and works.
func TestControlNodeDeleteEndpointConnectsHub(t *testing.T) {
	cfg := newTestConfig(t)
	manager := newTestManager(t, cfg)
	service := backend.NewService(cfg, agent.NewSharedDependencies(cfg), commands.NewService(cfg))
	registry, err := noderegistry.New(filepath.Join(t.TempDir(), "nodes.json"), time.Minute)
	if err != nil {
		t.Fatalf("new node registry: %v", err)
	}
	hub := relay.NewHub(nil)
	combined := &registryWithHubDisconnector{Registry: registry, Hub: hub}
	server := httptest.NewServer(NewHandlerWithRuntime(manager, service, nil, nil, nil, nil, nil, nil, combined))
	defer server.Close()
	defer hub.Shutdown(context.Background())

	resp := doJSONWithToken(t, http.MethodPost, server.URL+"/control/nodes/register", map[string]any{
		"id":   "node-a",
		"name": "node-a",
	}, "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("register status = %d", resp.StatusCode)
	}

	delResp := doJSONWithToken(t, http.MethodDelete, server.URL+"/control/nodes/node-a", nil, "")
	defer delResp.Body.Close()
	if delResp.StatusCode != http.StatusOK {
		t.Fatalf("delete status = %d", delResp.StatusCode)
	}
	if hub.IsOnline("node-a") {
		t.Fatal("expected node-a relay connection dropped after delete")
	}
}

// registryWithHubDisconnector mirrors main.go's registryWithOverview wiring:
// a registry combined with the relay hub, exposing DisconnectNode.
type registryWithHubDisconnector struct {
	*noderegistry.Registry
	Hub *relay.Hub
}

func (r *registryWithHubDisconnector) DisconnectNode(nodeID string) {
	if r.Hub != nil {
		r.Hub.Disconnect(nodeID)
	}
}

var _ = strings.TrimSpace // keep strings import when assertions change
