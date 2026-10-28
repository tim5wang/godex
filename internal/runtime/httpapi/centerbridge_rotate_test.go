package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/tim5wang/godex/internal/services/noderegistry"
)

// rotatingCenter answers 401 unless the request carries the accepted token,
// recording every credential it saw so a test can assert the retry really used
// the rotated one.
type rotatingCenter struct {
	mu     sync.Mutex
	accept string
	seen   []string
	nodes  map[string]noderegistry.NodeView
}

func (c *rotatingCenter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	auth := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	c.mu.Lock()
	c.seen = append(c.seen, auth)
	c.mu.Unlock()
	if auth != c.accept {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/control/nodes/")
	if node, ok := c.nodes[id]; ok {
		_ = json.NewEncoder(w).Encode(node)
		return
	}
	w.WriteHeader(http.StatusNotFound)
}

func (c *rotatingCenter) credentials() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.seen...)
}

// TestCenterBridgeRotatesCredentialAndRetriesOn401 is the self-heal property the
// per-node migration relies on: a node whose nk_ was refused re-issues its own
// credential and retries, so the rollout converges without an operator visiting
// every machine.
func TestCenterBridgeRotatesCredentialAndRetriesOn401(t *testing.T) {
	center := &rotatingCenter{
		accept: "nk_fresh",
		nodes:  map[string]noderegistry.NodeView{"node-a": {ID: "node-a"}},
	}
	srv := httptest.NewServer(center)
	t.Cleanup(srv.Close)

	b := NewCenterBridge(srv.URL, "nk_stale")
	b.Client = srv.Client()
	rotateCalls := 0
	b.SetUnauthorizedHandler(func(context.Context) (string, error) {
		rotateCalls++
		return "nk_fresh", nil
	})

	node, err := b.GetNode(context.Background(), "node-a")
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if node.ID != "node-a" {
		t.Errorf("node id = %q, want node-a", node.ID)
	}
	if rotateCalls != 1 {
		t.Errorf("rotation attempts = %d, want exactly 1", rotateCalls)
	}
	// The retry must carry the NEW credential; replaying the refused request
	// would send the very token the center just rejected.
	if got := center.credentials(); len(got) != 2 || got[0] != "nk_stale" || got[1] != "nk_fresh" {
		t.Errorf("credentials seen = %v, want [nk_stale nk_fresh]", got)
	}
	// The rotated token is installed on the bridge, so later calls do not re-401.
	if _, token := b.Endpoint(); token != "nk_fresh" {
		t.Errorf("bridge token = %q, want the rotated nk_fresh", token)
	}
}

// TestCenterBridgeCooldownPreventsRotationStorm pins the guard against turning a
// permanently-rejected credential into one rotation request per proxied call.
func TestCenterBridgeCooldownPreventsRotationStorm(t *testing.T) {
	// The center never accepts, so every call 401s.
	center := &rotatingCenter{accept: "nk_never", nodes: map[string]noderegistry.NodeView{}}
	srv := httptest.NewServer(center)
	t.Cleanup(srv.Close)

	b := NewCenterBridge(srv.URL, "nk_stale")
	b.Client = srv.Client()
	rotateCalls := 0
	b.SetUnauthorizedHandler(func(context.Context) (string, error) {
		rotateCalls++
		return "nk_also_rejected", nil
	})

	for i := 0; i < 3; i++ {
		if _, err := b.GetNode(context.Background(), "node-a"); err == nil {
			t.Fatal("a persistently rejected credential must surface an error")
		}
	}
	if rotateCalls != 1 {
		t.Errorf("rotation attempts = %d, want 1 within the cooldown window", rotateCalls)
	}
}

// A bridge with no rotation handler (a center-side bridge, or an un-joined node)
// must surface the 401 rather than pretend to heal.
func TestCenterBridge401WithoutHandlerSurfaces(t *testing.T) {
	center := &rotatingCenter{accept: "nk_other", nodes: map[string]noderegistry.NodeView{}}
	srv := httptest.NewServer(center)
	t.Cleanup(srv.Close)

	b := NewCenterBridge(srv.URL, "nk_stale")
	b.Client = srv.Client()

	if _, err := b.GetNode(context.Background(), "node-a"); err == nil {
		t.Fatal("expected an error when the credential is refused and cannot be rotated")
	}
	if got := center.credentials(); len(got) != 1 {
		t.Errorf("credentials seen = %v, want a single attempt with no retry", got)
	}
}

// A failing rotation must not be retried within the cooldown either, and must
// leave the bridge on its existing token.
func TestCenterBridgeFailedRotationLeavesTokenAndDoesNotStorm(t *testing.T) {
	center := &rotatingCenter{accept: "nk_other", nodes: map[string]noderegistry.NodeView{}}
	srv := httptest.NewServer(center)
	t.Cleanup(srv.Close)

	b := NewCenterBridge(srv.URL, "nk_stale")
	b.Client = srv.Client()
	rotateCalls := 0
	b.SetUnauthorizedHandler(func(context.Context) (string, error) {
		rotateCalls++
		return "", context.DeadlineExceeded
	})

	for i := 0; i < 2; i++ {
		if _, err := b.GetNode(context.Background(), "node-a"); err == nil {
			t.Fatal("expected an error")
		}
	}
	if rotateCalls != 1 {
		t.Errorf("rotation attempts = %d, want 1", rotateCalls)
	}
	if _, token := b.Endpoint(); token != "nk_stale" {
		t.Errorf("bridge token = %q, want the unchanged nk_stale", token)
	}
}
