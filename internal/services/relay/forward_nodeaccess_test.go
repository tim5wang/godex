package relay

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/tim5wang/godex/internal/core/identity"
)

// startForwardCenterWithPolicy is startForwardCenter plus a NodeAccess policy on
// the forward handler, so the denial path can be exercised.
func startForwardCenterWithPolicy(t *testing.T, authorize func(*http.Request, string) identity.Decision) (*httptest.Server, *Hub) {
	t.Helper()
	hub, hubServer := newTestHub(func(nodeID, credential string) bool { return true })
	t.Cleanup(func() { hubServer.Close(); _ = hub.Shutdown(context.Background()) })

	node := dialTestNode(t, "ws"+strings.TrimPrefix(hubServer.URL, "http"))
	t.Cleanup(node.close)
	node.send(t, Frame{Type: FrameHello, NodeID: "node-a", Credential: "x", Version: "v1.2.0"})
	if ok := node.recv(t); ok.Type != FrameHelloOK {
		t.Fatalf("expected hello_ok, got %#v", ok)
	}

	handler := NewForwardHandler(hub, nil)
	handler.AuthorizeNode = authorize
	forwardServer := httptest.NewServer(handler)
	t.Cleanup(forwardServer.Close)
	return forwardServer, hub
}

func forwardURLFor(server *httptest.Server, nodeID string) string {
	return "ws" + strings.TrimPrefix(server.URL, "http") + "/control/nodes/" + nodeID + "/forward"
}

// TestForwardHandlerAuthorizeNodeDeniesUpgrade covers the §1.3 hole on the TCP
// forward surface: a caller with a valid credential must not be able to open
// forward streams into a node it is not allowed to reach.
func TestForwardHandlerAuthorizeNodeDeniesUpgrade(t *testing.T) {
	seen := ""
	server, _ := startForwardCenterWithPolicy(t, func(_ *http.Request, nodeID string) identity.Decision {
		seen = nodeID
		return identity.Denied(identity.ReasonCrossTenant)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, resp, err := websocket.DefaultDialer.DialContext(ctx, forwardURLFor(server, "node-a"), nil)
	if err == nil {
		t.Fatal("expected the upgrade to fail for a denied caller")
	}
	if seen != "node-a" {
		t.Fatalf("AuthorizeNode saw nodeID %q, want node-a", seen)
	}
	// A denial must be an ordinary HTTP 403: the socket is refused before it is
	// upgraded, so the client gets a status instead of a socket that dies later.
	if resp == nil {
		t.Fatalf("no HTTP response recorded for the denied upgrade: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
}

// TestForwardHandlerAuthorizeNodeAllowsUpgrade is the counterpart: when the
// policy allows, the forward session still establishes.
func TestForwardHandlerAuthorizeNodeAllowsUpgrade(t *testing.T) {
	seen := ""
	server, _ := startForwardCenterWithPolicy(t, func(_ *http.Request, nodeID string) identity.Decision {
		seen = nodeID
		return identity.AllowedDecision(identity.ReasonSingleTenant)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := DialForward(ctx, forwardURLFor(server, "node-a"), "")
	if err != nil {
		t.Fatalf("DialForward: %v", err)
	}
	defer client.Close()
	if seen != "node-a" {
		t.Fatalf("AuthorizeNode saw %q, want node-a", seen)
	}
}

// TestForwardHandlerWithoutAuthorizeNodeUnchanged guards the seam from becoming
// mandatory: nil must preserve the previous behaviour for existing callers.
func TestForwardHandlerWithoutAuthorizeNodeUnchanged(t *testing.T) {
	wsURL, _, _, _ := startForwardCenter(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := DialForward(ctx, wsURL, "")
	if err != nil {
		t.Fatalf("DialForward: %v", err)
	}
	defer client.Close()
}

// TestForwardHandlerDenialBodyIsForbiddenError keeps the denial shape stable for
// clients that parse the body rather than only the status.
func TestForwardHandlerDenialBodyIsForbiddenError(t *testing.T) {
	server, _ := startForwardCenterWithPolicy(t, func(*http.Request, string) identity.Decision {
		return identity.Denied(identity.ReasonNoPrincipal)
	})

	req, err := http.NewRequest(http.MethodGet, "http"+strings.TrimPrefix(server.URL, "http")+"/control/nodes/node-a/forward", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
	var payload map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode denial body: %v", err)
	}
	if payload["error"] != "forbidden" {
		t.Fatalf("body = %#v, want error=forbidden", payload)
	}
}
