package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/services/noderegistry"
)

// fakeCenter is a minimal stand-in for the center's /control/nodes surface.
type fakeCenter struct {
	t         *testing.T
	nodes     []noderegistry.NodeView
	overview  map[string]any
	gotHeader string
}

func (f *fakeCenter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.gotHeader = r.Header.Get("Authorization")
	switch {
	case r.URL.Path == "/api/control/nodes":
		_ = json.NewEncoder(w).Encode(f.nodes)
	case strings.HasPrefix(r.URL.Path, "/api/control/nodes/") && strings.HasSuffix(r.URL.Path, "/overview"):
		_ = json.NewEncoder(w).Encode(f.overview)
	case strings.HasPrefix(r.URL.Path, "/api/control/nodes/"):
		id := strings.TrimPrefix(r.URL.Path, "/api/control/nodes/")
		id = strings.TrimSuffix(id, "/overview")
		for _, n := range f.nodes {
			if n.ID == id {
				_ = json.NewEncoder(w).Encode(n)
				return
			}
		}
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
	default:
		http.Error(w, `{"error":"unexpected path "+r.URL.Path}`, http.StatusNotFound)
	}
}

func newTestBridge(t *testing.T, center *fakeCenter) *CenterBridge {
	srv := httptest.NewServer(center)
	t.Cleanup(srv.Close)
	b := NewCenterBridge(srv.URL, "tok_center")
	b.Client = srv.Client()
	return b
}

func TestCenterBridgeListNodesAuthAndCache(t *testing.T) {
	center := &fakeCenter{t: t, nodes: []noderegistry.NodeView{{ID: "node-b", Name: "B"}}}
	b := newTestBridge(t, center)

	ctx := context.Background()
	nodes, err := b.ListNodes(ctx)
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	if len(nodes) != 1 || nodes[0].ID != "node-b" {
		t.Fatalf("unexpected nodes: %+v", nodes)
	}
	if center.gotHeader != "Bearer tok_center" {
		t.Fatalf("authorization header = %q, want Bearer tok_center", center.gotHeader)
	}
	// Second call within TTL must hit the cache, not the center.
	center.nodes = []noderegistry.NodeView{{ID: "node-c"}}
	if _, err := b.ListNodes(ctx); err != nil {
		t.Fatalf("cached ListNodes: %v", err)
	}
}

func TestCenterBridgeListNodesDegradesOnUnreachable(t *testing.T) {
	b := NewCenterBridge("http://127.0.0.1:1", "tok") // nothing listens on :1
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := b.ListNodes(ctx); err == nil {
		t.Fatal("expected error from unreachable center")
	}
}

func TestCenterBridgeGetOverview(t *testing.T) {
	center := &fakeCenter{
		t: t,
		overview: map[string]any{
			"overview": map[string]any{"node_id": "node-b", "version": "v1"},
		},
	}
	b := newTestBridge(t, center)
	ov, err := b.GetOverview(context.Background(), "node-b")
	if err != nil {
		t.Fatalf("GetOverview: %v", err)
	}
	if ov.NodeID != "node-b" || ov.Version != "v1" {
		t.Fatalf("unexpected overview: %+v", ov)
	}
}

func TestMergeNodeViews(t *testing.T) {
	local := []noderegistry.NodeView{{ID: "self", Name: "Self", Status: "online"}, {ID: "dup", Name: "Local Dup", Status: "online"}}
	remote := []noderegistry.NodeView{{ID: "dup", Name: "Center Dup", Status: "offline"}, {ID: "node-b", Name: "B", Status: "online"}}
	out := MergeNodeViews(local, remote)
	byID := map[string]noderegistry.NodeView{}
	for _, n := range out {
		byID[n.ID] = n
	}
	if byID["dup"].Name != "Local Dup" {
		t.Fatalf("local must win on id conflict: %+v", byID["dup"])
	}
	if byID["node-b"].Source != "center" {
		t.Fatalf("center-only node must be marked source=center: %+v", byID["node-b"])
	}
	if byID["self"].Source == "center" {
		t.Fatalf("local node must not be marked center: %+v", byID["self"])
	}
	if len(out) != 3 {
		t.Fatalf("merge size = %d, want 3", len(out))
	}
}

func TestCenterBridgeServeProxyStreamsSSE(t *testing.T) {
	var centerCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		centerCalls++
		if r.URL.Path != "/api/control/nodes/node-b/proxy/v1/echo" {
			t.Errorf("proxy path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tok_center" {
			t.Errorf("proxy authorization = %q", got)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"x\":1}\n\n")
		_, _ = io.WriteString(w, "data: {\"x\":2}\n\n")
	}))
	t.Cleanup(srv.Close)
	b := NewCenterBridge(srv.URL, "tok_center")
	b.Client = srv.Client()

	req := httptest.NewRequest(http.MethodPost, "/control/nodes/node-b/proxy/v1/echo", strings.NewReader(`{"q":1}`))
	req.Header.Set("Authorization", "Bearer tok_local")
	rr := httptest.NewRecorder()
	b.ServeProxy(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `"x":1`) || !strings.Contains(rr.Body.String(), `"x":2`) {
		t.Fatalf("SSE body = %q", rr.Body.String())
	}
	if centerCalls != 1 {
		t.Fatalf("center calls = %d", centerCalls)
	}
}

func TestBridgeProxyHandlerRoutesLocalVsCenter(t *testing.T) {
	localCalls := 0
	local := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		localCalls++
		w.WriteHeader(http.StatusTeapot)
	})
	bridgeCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bridgeCalls++
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(srv.Close)
	bridge := NewCenterBridge(srv.URL, "tok_center")
	bridge.Client = srv.Client()

	h := NewBridgeProxyHandler(local, bridge, func(id string) bool { return id == "node-local" })

	// Local node → local handler.
	req := httptest.NewRequest(http.MethodGet, "/control/nodes/node-local/proxy/v1/terminal/x/output", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusTeapot || localCalls != 1 {
		t.Fatalf("local route: code=%d localCalls=%d", rr.Code, localCalls)
	}

	// Center node → bridge.
	req = httptest.NewRequest(http.MethodGet, "/control/nodes/node-b/proxy/v1/terminal/x/output", nil)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated || bridgeCalls != 1 {
		t.Fatalf("bridge route: code=%d bridgeCalls=%d", rr.Code, bridgeCalls)
	}

	// No bridge configured → 503 for center node.
	h2 := NewBridgeProxyHandler(local, nil, func(id string) bool { return id == "node-local" })
	req = httptest.NewRequest(http.MethodGet, "/control/nodes/node-b/proxy/v1/terminal/x/output", nil)
	rr = httptest.NewRecorder()
	h2.ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("no-bridge code = %d, want 503", rr.Code)
	}
}
