package relay

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/core/identity"
)

// TestProxyHandlerRecordsForwardedUsage is the §7 basic-tier property: a forwarded
// request produces exactly one metering record, attributed to the caller the
// NodeAccess seam identified.
func TestProxyHandlerRecordsForwardedUsage(t *testing.T) {
	hub, wsURL := serveRelayHub(t, func(nodeID, credential string) bool {
		return credential == "ck_secret"
	})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	agent := NewAgent(AgentConfig{CenterURL: wsURL, NodeID: "node-a", Credential: "ck_secret", Handler: handler})
	if err := agent.Start(context.Background()); err != nil {
		t.Fatalf("agent start: %v", err)
	}
	defer agent.Stop(context.Background())
	waitOnline(t, hub, "node-a")

	var recorded []ForwardUsage
	proxy := NewProxyHandler(hub, func(r *http.Request) bool { return true })
	proxy.AuthorizeNode = func(r *http.Request, nodeID string) identity.Decision {
		return identity.Decision{Allowed: true, TenantID: "t1", UserID: "u1"}
	}
	proxy.UsageRecorder = func(usage ForwardUsage) { recorded = append(recorded, usage) }

	server := httptest.NewServer(proxy)
	defer server.Close()

	req, err := http.NewRequest("POST", server.URL+"/control/nodes/node-a/proxy/sessions",
		strings.NewReader(`{"prompt":"hi"}`))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	if len(recorded) != 1 {
		t.Fatalf("recorded %d usage rows, want exactly 1", len(recorded))
	}
	got := recorded[0]
	if got.TenantID != "t1" || got.UserID != "u1" {
		t.Errorf("attribution = %s/%s, want t1/u1 (from the decision, not the request)", got.TenantID, got.UserID)
	}
	if got.NodeID != "node-a" || got.Method != "POST" {
		t.Errorf("node/method = %s/%s, want node-a/POST", got.NodeID, got.Method)
	}
	if got.Status != http.StatusOK {
		t.Errorf("status = %d, want 200", got.Status)
	}
	if got.RequestBytes == 0 {
		t.Error("request bytes = 0; the request body was not counted")
	}
	if got.ResponseBytes != len(`{"ok":true}`) {
		t.Errorf("response bytes = %d, want %d", got.ResponseBytes, len(`{"ok":true}`))
	}
}

// TestProxyHandlerRecordsDeniedRequestsAsNothing: a request refused BEFORE the
// forward must not be metered as forwarded traffic — it never reached a node, so
// billing it would charge a tenant for traffic the center refused.
func TestProxyHandlerRecordsDeniedRequestsAsNothing(t *testing.T) {
	proxy := NewProxyHandler(nil, nil)
	var recorded []ForwardUsage
	proxy.UsageRecorder = func(usage ForwardUsage) { recorded = append(recorded, usage) }
	proxy.AuthorizeNode = func(r *http.Request, nodeID string) identity.Decision {
		return identity.Decision{Allowed: false, Reason: "cross_tenant"}
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/control/nodes/node-a/proxy/meta", nil)
	proxy.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if len(recorded) != 0 {
		t.Errorf("denied request was metered: %+v", recorded)
	}
}

// TestProxyHandlerRecordsFailedForward: a forward that never reached the node is
// still recorded, with the failure status. A tenant hammering an offline node is
// exactly what reconciliation should surface, so dropping these rows would hide it.
func TestProxyHandlerRecordsFailedForward(t *testing.T) {
	hub, _ := serveRelayHub(t, func(nodeID, credential string) bool { return true })
	var recorded []ForwardUsage
	proxy := NewProxyHandler(hub, func(r *http.Request) bool { return true })
	proxy.UsageRecorder = func(usage ForwardUsage) { recorded = append(recorded, usage) }

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/control/nodes/ghost-node/proxy/meta", nil)
	proxy.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 for an offline node", rec.Code)
	}
	if len(recorded) != 1 {
		t.Fatalf("recorded %d rows, want 1 for the failed forward", len(recorded))
	}
	if recorded[0].Status != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 so the row is self-describing", recorded[0].Status)
	}
	if recorded[0].NodeID != "ghost-node" {
		t.Errorf("node = %q, want ghost-node", recorded[0].NodeID)
	}
}

// TestProxyHandlerRecordsLocalDirect: a self-node request is served in-process but
// is still a tenant's traffic through the proxy surface. Omitting it would make a
// tenant's reported usage depend on where its node happened to be attached.
func TestProxyHandlerRecordsLocalDirect(t *testing.T) {
	hub, _ := serveRelayHub(t, func(nodeID, credential string) bool { return true })
	local := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		payload, _ := json.Marshal(map[string]string{"local": "yes"})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(payload)
	})

	var recorded []ForwardUsage
	proxy := NewProxyHandler(hub, func(r *http.Request) bool { return true })
	proxy.SetLocalHandler("self-node", local)
	proxy.UsageRecorder = func(usage ForwardUsage) { recorded = append(recorded, usage) }

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/control/nodes/self-node/proxy/sessions",
		strings.NewReader(`{"prompt":"hi"}`))
	req.Header.Set("Content-Type", "application/json")
	proxy.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (local dispatch)", rec.Code)
	}
	if len(recorded) != 1 {
		t.Fatalf("recorded %d rows, want 1 for the local dispatch", len(recorded))
	}
	if recorded[0].ResponseBytes == 0 {
		t.Error("response bytes = 0 for a local dispatch; the counting writer did not observe the body")
	}
}

// TestCountingWriterForwardsHijack: the local path serves the SAME surface as the
// API server, including WebSocket upgrades (terminal, watch frames). A wrapper
// that hides http.Hijacker would break those paths — the hazard httpapi's own gzip
// writer documents.
func TestCountingWriterForwardsHijack(t *testing.T) {
	var hijacked bool
	underlying := hijackableWriter{ResponseWriter: httptest.NewRecorder(), onHijack: func() { hijacked = true }}
	wrapped := &countingWriter{ResponseWriter: underlying, status: http.StatusOK}

	if _, _, err := wrapped.Hijack(); err != nil {
		t.Fatalf("Hijack through the wrapper failed: %v", err)
	}
	if !hijacked {
		t.Error("Hijack did not reach the underlying writer")
	}
	if wrapped.Unwrap() == nil {
		t.Error("Unwrap returned nil; http.ResponseController could not reach the real writer")
	}
}

// hijackableWriter is a ResponseWriter that reports Hijack support.
type hijackableWriter struct {
	http.ResponseWriter
	onHijack func()
}

func (h hijackableWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h.onHijack != nil {
		h.onHijack()
	}
	return nil, nil, nil
}
